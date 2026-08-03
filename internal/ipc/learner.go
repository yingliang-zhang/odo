package ipc

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/yingliang-zhang/odo/internal/store"
)

// M4 (Learning): the learner pass runs at the distill epoch boundary,
// proposing behavior rules for .odo/memory.md (project) and ~/.odo/user.md
// (global, recurrence-gated). Proposals land in the journal only; a human
// review (apply_memory) is the single write path (ADR-0003 inv 1, 7).

const (
	// learnerTimeout bounds the one-shot orchestrator learner run that
	// follows the distill one-shot. Both run inside the blocking distill
	// IPC call; the GUI read timeout accounts for their sum.
	learnerTimeout = 5 * time.Minute
	// memoryCap bounds .odo/memory.md and ~/.odo/user.md at read AND at
	// apply (ADR-0003: 4 KB ≈ 1k tokens).
	memoryCap = 4 * 1024

	memoryFileName  = "memory.md"
	archiveFileName = "memory-archive.md"
)

// sha16 returns the first 16 hex chars of SHA-256(b) — the injection receipt
// and memory_update before/after digests.
func sha16(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:8])
}

// readProjectMemory reads <projectRoot>/.odo/memory.md capped at memoryCap
// with a line-boundary cut (mirrors readUserMemory). "" when absent/empty.
// Injection-only: the apply write path reads the file in FULL instead
// (ADR-0003 inv 3, no silent truncation).
func readProjectMemory(projectRoot string) string {
	b, err := os.ReadFile(filepath.Join(projectRoot, ".odo", memoryFileName))
	if err != nil {
		return ""
	}
	return capAtLineBoundary(string(b), memoryCap)
}

// readArchive reads <projectRoot>/.odo/memory-archive.md (append-only, never
// injected, returned uncapped). "" when absent.
func readArchive(projectRoot string) string {
	b, err := os.ReadFile(filepath.Join(projectRoot, ".odo", archiveFileName))
	if err != nil {
		return ""
	}
	return string(b)
}

// readFileFull reads path without any cap ("" when absent). Apply-write
// bases and read_memory both need the truth, not the injection cut.
func readFileFull(path string) string {
	b, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	return string(b)
}

// memoryLineRe parses one daemon-written rule line:
// `- <rule> — cites: <note>; reaffirmed: <epoch>`. The cites group stops at
// ';' so `reaffirmed` is optional (M4 format).
var memoryLineRe = regexp.MustCompile(`^- (.+?) — cites: ([^;]+)\s*(?:; reaffirmed: (\d+))?$`)

// memoryRule is one parsed memory.md line. Opaque lines (human hand-edits
// without a cites: tag, comments, blanks) are preserved verbatim but are
// never rotation/retraction candidates (spec §3).
type memoryRule struct {
	text       string // rule body ("" for opaque lines)
	cites      string // evidence note name
	reaffirmed int    // last-reaffirmed epoch (rotation recency key)
	opaque     bool   // not a parseable rule line
	influx     bool   // just accepted in this apply (never evictable)
	raw        string // original line, preserved byte-for-byte
}

// parseMemoryLines splits content into per-line rules preserving the file's
// exact line strings so apply can rewrite without reformatting human edits.
func parseMemoryLines(content string) []memoryRule {
	trimmed := strings.TrimRight(content, "\n")
	if trimmed == "" {
		return nil
	}
	var rules []memoryRule
	for _, line := range strings.Split(trimmed, "\n") {
		if m := memoryLineRe.FindStringSubmatch(line); m != nil {
			r := memoryRule{text: m[1], cites: m[2], raw: line}
			if m[3] != "" {
				fmt.Sscanf(m[3], "%d", &r.reaffirmed)
			}
			rules = append(rules, r)
			continue
		}
		rules = append(rules, memoryRule{opaque: true, raw: line})
	}
	return rules
}

// normalizeRule folds case, trims, and collapses whitespace — the comparison
// form for contradiction and recurrence matching (spec: trim/lower/collapse ws).
func normalizeRule(s string) string {
	return strings.Join(strings.Fields(strings.ToLower(strings.TrimSpace(s))), " ")
}

// --- learner output contract -------------------------------------------------

// learnerResult is the JSON object the orchestrator one-shot must emit
// (spec §2 output contract).
type learnerResult struct {
	Memory []struct {
		Rule        string `json:"rule"`
		Evidence    string `json:"evidence"`
		Contradicts string `json:"contradicts"`
	} `json:"memory"`
	User []struct {
		Rule     string   `json:"rule"`
		Projects []string `json:"projects"`
	} `json:"user"`
	Reaffirm []string `json:"reaffirm"`
}

// parseLearnerOutput decodes the one-shot's raw text. Unknown fields are
// ignored; a wrong field type or non-JSON input is an error (learner failure
// path — distill continues, spec §2).
func parseLearnerOutput(raw string) (*learnerResult, error) {
	// Some models wrap JSON in a fence; parse from the first '{' to the
	// last '}' rather than rejecting outright.
	start := strings.Index(raw, "{")
	end := strings.LastIndex(raw, "}")
	if start < 0 || end <= start {
		return nil, fmt.Errorf("no JSON object in learner output")
	}
	var res learnerResult
	if err := json.Unmarshal([]byte(raw[start:end+1]), &res); err != nil {
		return nil, fmt.Errorf("learner output JSON: %w", err)
	}
	return &res, nil
}

// siblingMemory is one registered sibling project's memory.md content as
// staged for the learner (name from the registry row).
type siblingMemory struct {
	name    string
	content string
}

// vetoStats counts daemon-side evidence vetoes (journaled in the propose
// event's stats, spec §2).
type vetoStats struct {
	MemoryKept    int `json:"memory_kept"`
	MemoryDropped int `json:"memory_dropped"`
	UserKept      int `json:"user_kept"`
	UserDropped   int `json:"user_dropped"`
}

// vetLearnerOutput applies the daemon-side evidence checks the LLM's
// self-tagging can't be trusted with (ADR-0003 inv 4 discipline):
//   - memory proposals: evidence must equal the just-written note name and the
//     rule must not already appear verbatim in current memory.md;
//   - user proposals: the normalized rule must appear in ≥2 distinct
//     registered projects' staged inputs ({own memory.md, new note} ∪ sibling
//     memory.mds); kept proposals get daemon-verified Projects names.
//     (With <2 registered projects this gate can never pass.)
//
// ownName names the bound project in the verified projects list.
func vetLearnerOutput(res *learnerResult, noteName, ownMem, noteContent, ownName string, siblings []siblingMemory) (proposals []MemoryProposal, reaffirm []string, stats vetoStats) {
	for _, mp := range res.Memory {
		if mp.Rule == "" || mp.Evidence != noteName || strings.Contains(ownMem, mp.Rule) {
			stats.MemoryDropped++
			continue
		}
		p := MemoryProposal{Target: "memory.md", Rule: mp.Rule, Evidence: mp.Evidence}
		if mp.Contradicts != "" {
			p.Contradicts = mp.Contradicts
		}
		proposals = append(proposals, p)
		stats.MemoryKept++
	}

	// Per-project staged input haystacks (normalized): bound project gets
	// {memory.md, new note}; each sibling gets its memory.md.
	norm := normalizeRule
	ownHay := norm(ownMem) + "\n" + norm(noteContent)
	for _, up := range res.User {
		nr := norm(up.Rule)
		if nr == "" {
			stats.UserDropped++
			continue
		}
		var matched []string
		if strings.Contains(ownHay, nr) {
			matched = append(matched, ownName)
		}
		for _, sib := range siblings {
			if strings.Contains(norm(sib.content), nr) {
				matched = append(matched, sib.name)
			}
		}
		if len(matched) < 2 {
			stats.UserDropped++ // recurrence gate: ≥2 distinct projects
			continue
		}
		proposals = append(proposals, MemoryProposal{Target: "user.md", Rule: up.Rule, Projects: matched})
		stats.UserKept++
	}

	// Reaffirm targets survive only if they name an existing daemon-formatted
	// rule (an opaque line has no reaffirmed field to bump).
	existing := map[string]bool{}
	for _, r := range parseMemoryLines(ownMem) {
		if !r.opaque {
			existing[norm(r.text)] = true
		}
	}
	for _, t := range res.Reaffirm {
		if existing[norm(t)] {
			reaffirm = append(reaffirm, t)
		}
	}
	return proposals, reaffirm, stats
}

// learnerPrompt renders the learner one-shot prompt with inputs in the
// spec's stable order: new note → own memory.md → siblings → user.md.
func learnerPrompt(noteName, noteContent, ownMem string, siblings []siblingMemory, userMem string) string {
	var b strings.Builder
	b.WriteString(`You are running odo's memory learner pass. Extract behavior-shaping rules from the newly distilled epoch note below. A rule is an imperative statement that changes what an agent DOES on every future run ("always run go test before claiming done", "prefer compact output") — not a record, fact, or narrative.

Output JSON ONLY (no prose, no markdown fence), exactly this shape:
{"memory":[{"rule":"<imperative>","evidence":"` + noteName + `","contradicts":""}],"user":[{"rule":"<imperative>","projects":["<p1>","<p2>"]}],"reaffirm":["<existing rule text>"]}

Rules:
- memory: behavioral rules from the NEW note only, absent from the current memory.md. "evidence" must be exactly "` + noteName + `". "contradicts" is optional: the verbatim text of one existing memory.md rule the new rule contradicts ("" otherwise).
- user: a rule proposing promotion to the global user.md is allowed ONLY when the same rule (same meaning, any wording) already appears in at least 2 registered projects' inputs below (this project's memory.md or new note, plus sibling memory.md files). Name those projects in "projects". If fewer than 2 projects are shown, "user" must be [].
- reaffirm: optional list of memory.md rule texts from the CURRENT memory.md that the new note shows still being followed.
- Use empty arrays when nothing qualifies. Output the JSON object and nothing else.

=== NEW EPOCH NOTE: ` + noteName + ` ===
`)
	b.WriteString(orEmpty(noteContent))
	b.WriteString("\n\n=== CURRENT .odo/memory.md (this project) ===\n")
	b.WriteString(orEmpty(ownMem))
	b.WriteString("\n\n=== SIBLING PROJECTS' memory.md FILES ===\n")
	if len(siblings) == 0 {
		b.WriteString("(none registered)")
	} else {
		for i, sib := range siblings {
			if i > 0 {
				b.WriteString("\n")
			}
			fmt.Fprintf(&b, "--- project: %s ---\n%s", sib.name, orEmpty(sib.content))
		}
	}
	b.WriteString("\n\n=== ~/.odo/user.md (global) ===\n")
	b.WriteString(orEmpty(userMem))
	b.WriteString("\n")
	return b.String()
}

func orEmpty(s string) string {
	if strings.TrimSpace(s) == "" {
		return "(empty)"
	}
	return s
}

// runLearner executes the learner one-shot for the just-distilled note and
// journals the outcome (spec §2). It returns the number of pending proposals
// in the new batch (0 when the learner failed or nothing survived the veto).
// epoch is the distilled note's epoch (conversation epoch BEFORE the
// increment), so batch identity is `latest distill newEpoch − 1` (spec §5).
func (s *Server) runLearner(ctx context.Context, conversationID int64, noteName, noteContent string, epoch int) int {
	fail := func(err error) {
		_, _ = s.store.AppendEvent(ctx, conversationID, store.EventMemoryUpdate, mustJSON(map[string]interface{}{
			"layer":  "learner",
			"cause":  "failed",
			"detail": err.Error(),
		}))
	}

	ownMem := readProjectMemory(s.projectRoot) // prompt input is capped (§2)
	ownName := filepath.Base(s.resolvedRoot)

	sibs := siblingMemories(s.resolvedRoot)
	userMem := readUserMemory()

	ad := s.distillAdapter
	if ad == nil {
		ad = s.adapters[""] // same fallback as runDistillAgent
	}
	raw, err := runOneShot(ctx, ad, learnerPrompt(noteName, noteContent, ownMem, sibs, userMem), learnerTimeout)
	if err != nil {
		fail(fmt.Errorf("learner run: %w", err))
		return 0
	}
	res, err := parseLearnerOutput(raw)
	if err != nil {
		fail(err)
		return 0
	}
	proposals, reaffirm, stats := vetLearnerOutput(res, noteName, ownMem, noteContent, ownName, sibs)
	if len(proposals) == 0 {
		// Zero proposals after the veto = no batch this distill; any older
		// pending batch is superseded (spec §5). Nothing to journal (a
		// zero-length propose would leave a reviewable "batch" of nothing).
		return 0
	}
	payload := map[string]interface{}{
		"action":    "memory_propose",
		"epoch":     epoch,
		"proposals": proposals,
		"reaffirm":  reaffirm,
		"stats":     stats,
	}
	if _, err := s.store.AppendEvent(ctx, conversationID, store.EventReviewAction, mustJSON(payload)); err != nil {
		fail(fmt.Errorf("journal memory_propose: %w", err))
		return 0
	}
	return len(proposals)
}

// siblingMemories returns up to 3 registered sibling projects' memory.md
// contents, newest-registered first (spec §2 input 3). The bound project
// (resolved-root equality) is excluded. Reads exactly
// filepath.Join(row.Root, ".odo", "memory.md") — roots were EvalSymlinks'd
// at registration, no symlink resolution at read time.
func siblingMemories(resolvedRoot string) []siblingMemory {
	rows := registeredProjects()
	sibs := make([]RegistryRow, 0, len(rows))
	for _, r := range rows {
		if r.Root == resolvedRoot {
			continue
		}
		sibs = append(sibs, r)
	}
	sort.Slice(sibs, func(i, j int) bool { return sibs[i].Added > sibs[j].Added })
	if len(sibs) > 3 {
		sibs = sibs[:3]
	}
	out := make([]siblingMemory, 0, len(sibs))
	for _, r := range sibs {
		path := filepath.Join(r.Root, ".odo", memoryFileName)
		if filepath.Clean(path) != filepath.Join(r.Root, ".odo", memoryFileName) {
			continue // defensive: only the literal constructed path
		}
		out = append(out, siblingMemory{
			name:    r.Name,
			content: capAtLineBoundary(readFileFull(path), memoryCap),
		})
	}
	return out
}

// --- apply: rotation / retraction planning (no disk I/O) ----------------------

// acceptedRule is one accepted memory.md proposal with the fields the write
// path depends on (NOT a bare string — evidence and contradicts steer the
// line format and retraction match, spec §3).
type acceptedRule struct {
	rule        string
	evidence    string
	contradicts string
}

// acceptedUserRule is one accepted user.md proposal; projects are the
// daemon-verified recurrence matches, never the LLM's self-tagged list.
type acceptedUserRule struct {
	rule     string
	projects []string
}

// memoryApplyPlan is the fully computed memory.md apply result, produced
// without touching disk so the apply stays all-or-nothing.
type memoryApplyPlan struct {
	content              string   // new full memory.md
	archiveAppend        string   // blocks to append to memory-archive.md ("" = none)
	rotated              []string // rule texts evicted to the archive (overflow)
	retracted            []string // rule texts moved to the archive (conflict)
	unmatchedContradicts []string // contradicts texts matching nothing (journaled)
	added                int      // new rule lines appended
	reaffirmed           int      // existing rules bumped to epoch
}

// planMemoryApply computes the new memory.md plus archive appends for an
// acceptance set (spec §3): append accepted lines, bump reaffirm targets,
// rotate overflow (whole rules, lowest reaffirmed first, influx excluded,
// opaque never eligible), retract contradicts matches with a record.
// old is the FULL uncapped current file.
func planMemoryApply(old string, accepted []acceptedRule, reaffirm []string, epoch int) memoryApplyPlan {
	var plan memoryApplyPlan
	now := time.Now().UTC().Format(time.RFC3339)
	rules := parseMemoryLines(old)

	// (2) Reaffirm bumps precede appends so an incoming rule is never a
	// reaffirm target (targets exist in the file already — vetted).
	for _, target := range reaffirm {
		nt := normalizeRule(target)
		if nt == "" {
			continue
		}
		for i := range rules {
			if !rules[i].opaque && normalizeRule(rules[i].text) == nt {
				rules[i].reaffirmed = epoch
				rules[i].raw = fmt.Sprintf("- %s — cites: %s; reaffirmed: %d",
					rules[i].text, rules[i].cites, epoch)
				plan.reaffirmed++
				break
			}
		}
	}

	// (1) Append accepted rules; each is influx (newest ⇒ never evictable).
	// A rule whose normalized text is already stored is skipped: the apply
	// is journaled only after every write succeeds, so a mid-write I/O
	// failure leaves the batch pending and the retry replans against an
	// already-applied memory.md — the skip makes that retry converge
	// instead of double-appending.
	for _, a := range accepted {
		if na := normalizeRule(a.rule); na != "" {
			dup := false
			for i := range rules {
				if normalizeRule(rules[i].text) == na {
					dup = true
					break
				}
			}
			if dup {
				continue
			}
		}
		line := fmt.Sprintf("- %s — cites: %s; reaffirmed: %d", a.rule, a.evidence, epoch)
		rules = append(rules, memoryRule{
			text: a.rule, cites: a.evidence, reaffirmed: epoch, influx: true, raw: line,
		})
		plan.added++
	}

	// (3) Overflow rotation: evict lowest-reaffirmed candidates (never
	// influx, never opaque) until the projection fits the cap.
	var rotatedRaw []string
	for contentLen(rules) > memoryCap {
		idx := -1
		for i := range rules {
			if rules[i].opaque || rules[i].influx {
				continue
			}
			if idx < 0 || rules[i].reaffirmed < rules[idx].reaffirmed {
				idx = i
			}
		}
		if idx < 0 {
			break // nothing eligible left; opaque text keeps the file over cap
		}
		plan.rotated = append(plan.rotated, rules[idx].text)
		rotatedRaw = append(rotatedRaw, rules[idx].raw)
		rules = append(rules[:idx], rules[idx+1:]...)
	}
	if len(rotatedRaw) > 0 {
		var ab strings.Builder
		fmt.Fprintf(&ab, "\n## %s — rotated from memory.md (overflow)\n", now)
		for _, raw := range rotatedRaw {
			ab.WriteString(raw + "\n")
		}
		plan.archiveAppend = ab.String()
	}

	// (4) Retraction-with-record: a matched contradicts removes the stored
	// rule into the archive; no match is surfaced, never silent.
	for _, a := range accepted {
		nc := normalizeRule(a.contradicts)
		if nc == "" {
			continue
		}
		idx := -1
		for i := range rules {
			if rules[i].opaque || rules[i].influx {
				continue
			}
			if normalizeRule(rules[i].text) == nc {
				idx = i
				break
			}
		}
		if idx < 0 {
			plan.unmatchedContradicts = append(plan.unmatchedContradicts, a.contradicts)
			continue
		}
		plan.retracted = append(plan.retracted, rules[idx].text)
		prefix := a.rule
		if len(prefix) > 60 {
			prefix = strings.TrimSpace(prefix[:60]) + "…"
		}
		plan.archiveAppend += fmt.Sprintf("\n## %s — retracted: %s (conflict)\n%s\n",
			now, prefix, rules[idx].raw)
		rules = append(rules[:idx], rules[idx+1:]...)
	}

	// (5) Render: preserved raw lines, LF-joined, single trailing newline.
	var out strings.Builder
	for _, r := range rules {
		out.WriteString(r.raw + "\n")
	}
	plan.content = out.String()
	return plan
}

// contentLen sums rendered bytes (one '\n' per line).
func contentLen(rules []memoryRule) int {
	n := 0
	for _, r := range rules {
		n += len(r.raw) + 1
	}
	return n
}

// userRuleBody extracts the rule portion of a stored user.md line
// (`- <rule> — seen: <p1>, <p2>`): drop the "- " marker and everything from
// the " — seen:" separator on. Lines without the daemon shape reduce to
// their whole text.
func userRuleBody(line string) string {
	body := strings.TrimPrefix(line, "- ")
	if i := strings.Index(body, " — seen:"); i >= 0 {
		body = body[:i]
	}
	return body
}

// planUserApply computes the new user.md for an accepted set, refusing the
// whole set when the result would exceed memoryCap (spec §3: never truncate
// a user file, error names the offending rule). old is FULL uncapped.
//
// A rule whose normalized body is already stored is skipped (spec §5: no
// duplicate lines) — mirroring planMemoryApply's accepted-rule skip. Writes
// go archive → user.md → memory.md, so a mid-write failure after the user
// write leaves the batch pending and the retry replans against an
// already-applied user.md; the skip makes that retry converge instead of
// double-appending (a duplicate append near the cap would push the file
// over memoryCap and refuse the batch forever, deadlocking the epoch).
func planUserApply(old string, accepted []acceptedUserRule) (string, error) {
	out := strings.TrimRight(old, "\n")
	existing := map[string]bool{}
	for _, line := range strings.Split(out, "\n") {
		if nb := normalizeRule(userRuleBody(line)); nb != "" {
			existing[nb] = true
		}
	}
	for _, a := range accepted {
		// Skip by rule BODY, not by seen set: a re-proposed rule with a
		// different recurrence list is still the same rule. The na != ""
		// guard mirrors memory.md — an empty normalized body never skips.
		if na := normalizeRule(a.rule); na != "" {
			if existing[na] {
				continue
			}
			existing[na] = true
		}
		line := fmt.Sprintf("- %s — seen: %s", a.rule, strings.Join(a.projects, ", "))
		join := ""
		if out != "" {
			join = "\n"
		}
		if len(out)+len(join)+len(line)+1 > memoryCap {
			return "", fmt.Errorf("user.md would exceed %d bytes: rule %q", memoryCap, a.rule)
		}
		out += join + line
	}
	if out == "" {
		return "", nil
	}
	return out + "\n", nil
}

// --- pending batch recovery (journal-only persistence, spec §5) ---------------

// proposePayload is the journal shape journaled by runLearner.
type proposePayload struct {
	Action    string           `json:"action"`
	Epoch     int              `json:"epoch"`
	Proposals []MemoryProposal `json:"proposals"`
	Reaffirm  []string         `json:"reaffirm"`
}

// pendingBatch is a recovered memory_propose event plus its consumption state.
type pendingBatch struct {
	epoch     int
	seq       int
	proposals []MemoryProposal
	reaffirm  []string
	consumed  bool // a memory_apply for this epoch exists
	exists    bool
}

// findPendingBatch recovers the pending proposal batch from the journal: the
// memory_propose whose epoch equals (latest distill review_action's newEpoch
// − 1). A new distill supersedes any older batch even when it emits none
// (spec "Batch identity"). events are seq-ascending; scanned newest-first.
func findPendingBatch(events []store.Event) pendingBatch {
	// Pass 1: the latest distill's review_action pins the pending epoch
	// (newEpoch − 1 = the note's pre-increment epoch). Any older batch is
	// superseded — even when the newest distill emitted no propose.
	pendingEpoch := -1
	for i := len(events) - 1; i >= 0; i-- {
		if events[i].Type != store.EventReviewAction {
			continue
		}
		var p struct {
			Action string `json:"action"`
			Epoch  int    `json:"epoch"`
		}
		if json.Unmarshal(events[i].Payload, &p) == nil && p.Action == "distill" {
			pendingEpoch = p.Epoch - 1
			break
		}
	}
	if pendingEpoch < 0 {
		return pendingBatch{}
	}
	// Pass 2 (newest-first): an apply for the pending epoch consumes the
	// batch; the propose for it is the pending review.
	for i := len(events) - 1; i >= 0; i-- {
		if events[i].Type != store.EventReviewAction {
			continue
		}
		var p struct {
			Action string `json:"action"`
			Epoch  int    `json:"epoch"`
		}
		if json.Unmarshal(events[i].Payload, &p) != nil || p.Epoch != pendingEpoch {
			continue
		}
		switch p.Action {
		case "memory_apply":
			return pendingBatch{epoch: pendingEpoch, consumed: true, exists: true}
		case "memory_propose":
			var pp proposePayload
			if json.Unmarshal(events[i].Payload, &pp) != nil {
				return pendingBatch{}
			}
			return pendingBatch{
				epoch:     pendingEpoch,
				seq:       events[i].Seq,
				proposals: pp.Proposals,
				reaffirm:  pp.Reaffirm,
				exists:    true,
			}
		}
	}
	return pendingBatch{}
}
