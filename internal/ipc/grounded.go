package ipc

// D2 of the 2026-08-27 control-plane hardening DESIGN LOCK
// (docs/design/control-plane-hardening-lock.md): the repo-grounded
// reviewer leg. Exactly ONE leg per review/audit fan-out gets read-only
// repository tools so it can verify missed callers, interface/contract
// constraints, schema drift, and cross-file invariants instead of
// judging a diff blind. The leg is deterministic, bounded, and fully
// journaled; its verdict weighs exactly like every other leg (no extra
// authority — weighting is D8, a later wave).
//
// Moving parts:
//
//	planGrounded        prefs resolution (grounded_reviewer:, fallback the
//	                    fan-out line's FIRST entry, journaled resolved_by)
//	                    + the grounded_review_required posture
//	                    (always|gate_sources|never, default gate_sources —
//	                    a degraded grounded leg on a D1 gate-source diff is
//	                    Infra, so the round fails closed).
//	computeGroundedScope  the LLM-free allowlist: touched paths ∪ same-dir
//	                    siblings ∪ repo files importing a touched package
//	                    (bounded grep, the fstools caps) ∪ repo-internal
//	                    packages the touched files import. Non-Go diffs
//	                    degrade to touched + same-dir; a computation
//	                    failure degrades to touched-only and flags
//	                    scope_truncated (fail-visible).
//	scopedToolExecutor  the allowlist decorator around the rooted FS
//	                    executor: out-of-scope reads are refused with a
//	                    model-visible error (QueryWithTools journals the
//	                    refusal — model-visible ⟺ logged holds for
//	                    refusals too) and the groundedTotalBytes budget is
//	                    enforced session-wide. The executor appends each
//	                    ToolAudit BEFORE the tool output returns to the
//	                    model (the client's loop), so citing-without-
//	                    calling is mechanically detectable.
//
// Caps (D9-C lock): the grounded leg carries THREE budgets that move
// together with the diff-size ladder (32K→256K; the round cap lagged it
// by two rungs — 8→16 on 2026-08-29, then K3 died 5× on "tool loop
// exceeded 16 rounds" over #118/#120 while the ungrounded legs accepted
// the same diffs):
//
//	rounds    groundedDefaultRounds, DEFAULT 40, resolved per plan by
//	          groundedToolRoundsCap — the fix ships ACTIVE (a default of
//	          16 ships the incident unfixed); the env/prefs line is the
//	          escape hatch back down, never the activation mechanism.
//	bytes     groundedTotalBytes 256KB fail-soft across rounds (per-read
//	          stays fsReadBytesCap 64KB). Budget exhaustion never frees
//	          the leg from its verdict: refusals steer the model to
//	          answer, the flag tool_budget_exhausted is journaled, and a
//	          missing verdict token hits the existing fail-closed
//	          degradation (reviewVerdict forces needs_fixes; the audit
//	          leg reads parse_error).
//	wall      groundedLegDeadline — above the 16-round baseline the
//	          leg's outer deadline scales ×rounds/16, so the typed infra
//	          death stays "round capacity", not a misleading wall-clock
//	          timeout.

import (
	"context"
	"encoding/json"
	"fmt"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/yingliang-zhang/odo/internal/adapter"
	"github.com/yingliang-zhang/odo/internal/moa"
)

const (
	// groundedDefaultRounds is the grounded leg's tool-loop round cap and
	// the D9-C default — it equals the client's hard ceiling (moa
	// maxToolRounds = 40) and ships ACTIVE (see the header's budget
	// ladder): the ODO_GROUNDED_TOOL_ROUNDS env or the
	// grounded_tool_rounds: prefs line turns it back down, never up.
	groundedDefaultRounds = 40
	// groundedBaselineRounds is the pre-D9-C cap (8 → 16 on 2026-08-29,
	// the #101/#102/#105 ruling): the wall-clock interlock's scaling
	// baseline only.
	groundedBaselineRounds = 16
	// groundedRoundsEnv is the round-cap escape hatch (header's rounds
	// budget). Empty/invalid reads as absent.
	groundedRoundsEnv = "ODO_GROUNDED_TOOL_ROUNDS"
	// groundedTotalBytes caps the cumulative tool-result bytes one
	// grounded leg may be served across all rounds.
	groundedTotalBytes = 256 * 1024
	// groundedToolCallsCap bounds journaled tool audit entries per leg.
	// Raised 64 → 96 (D9-C): 40 rounds × ≥2 calls/round can exceed 64 —
	// the audit trail must survive the full loop (tool_rounds_used keeps
	// the true count when this truncates).
	groundedToolCallsCap = 96
)

// groundedReviewNotice is the additive prompt sentence every grounded
// panel leg carries (buildReviewPrompt grounded=true). Ungrounded legs
// keep byte-identical prompts.
const groundedReviewNotice = "You have read-only tools over the repository (read_file, grep, glob), " +
	"scoped to the diff's files and their one-hop import neighborhood; use them to check missed " +
	"callers, interface/contract constraints, schema or generated-artifact drift, cross-file " +
	"invariants; every read is journaled."

// --- scope ----------------------------------------------------------------

// groundedScope is the grounded leg's allowlist: files are exact entries,
// dirs admit everything beneath them (package siblings and imported
// packages are directory-granular). Keyed by cleaned absolute path under
// the same root the fs executor resolves (symlink-evaluated).
type groundedScope struct {
	files     map[string]bool
	dirs      map[string]bool
	truncated bool // import-neighborhood computation degraded (fail-visible)
}

func (sc groundedScope) count() int { return len(sc.files) + len(sc.dirs) }

// sha identifies the allowlist for the journal: sha16 of the sorted
// "f:"/"d:" entries — two scopes with the same content hash equal.
func (sc groundedScope) sha() string {
	entries := make([]string, 0, len(sc.files)+len(sc.dirs))
	for f := range sc.files {
		entries = append(entries, "f:"+f)
	}
	for d := range sc.dirs {
		entries = append(entries, "d:"+d)
	}
	sort.Strings(entries)
	return sha16([]byte(strings.Join(entries, "\n")))
}

// allows reports whether abs is in scope: an exact file entry, an exact
// dir entry (a grep/glob base IS the dir), or under any dir entry.
func (sc groundedScope) allows(abs string) bool {
	if sc.files[abs] {
		return true
	}
	for dir := abs; ; dir = filepath.Dir(dir) {
		if sc.dirs[dir] {
			return true
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return false
		}
	}
}

// computeGroundedScope builds the allowlist from the diff's repo-relative
// touched paths (git.PatchPaths / git.PatchPathsText output). Failures
// degrade: no go.mod ⇒ touched+same-dir with truncated set (fail-visible);
// a grep cap trip likewise flags truncated.
func computeGroundedScope(root string, touched []string) groundedScope {
	sc := groundedScope{files: map[string]bool{}, dirs: map[string]bool{}}
	var goFiles []string
	for _, rel := range touched {
		clean := filepath.Clean(filepath.FromSlash(rel))
		if clean == "." || filepath.IsAbs(clean) || strings.HasPrefix(clean, "..") {
			continue
		}
		abs := filepath.Join(root, clean)
		sc.files[abs] = true
		// Touched package siblings are the same directory (Go packages
		// are directory-scoped); the non-Go degrade is the same rule —
		// touched + same-dir.
		sc.dirs[filepath.Dir(abs)] = true
		if strings.HasSuffix(clean, ".go") {
			goFiles = append(goFiles, abs)
		}
	}
	if len(goFiles) == 0 {
		return sc // non-Go paths: touched + same-dir, nothing more
	}
	module, err := modulePath(root)
	if err != nil {
		// The import neighborhood cannot be keyed without the module
		// path — degrade to touched + same-dir, fail-visible.
		sc.truncated = true
		return sc
	}
	// One hop out: repo-internal packages the touched files import.
	for _, gf := range goFiles {
		for _, imp := range fileImports(gf) {
			switch {
			case imp == module:
				sc.dirs[root] = true
			default:
				if rel, ok := strings.CutPrefix(imp, module+"/"); ok {
					sc.dirs[filepath.Join(root, filepath.FromSlash(rel))] = true
				}
			}
		}
	}
	// One hop back: repo files whose import block references a touched
	// package (exact-quoted match), bounded by the fstools grep caps.
	pkgs := map[string]bool{}
	for _, gf := range goFiles {
		rel, err := filepath.Rel(root, filepath.Dir(gf))
		if err != nil || strings.HasPrefix(rel, "..") {
			continue
		}
		imp := module
		if rel != "." {
			imp += "/" + filepath.ToSlash(rel)
		}
		pkgs[imp] = true
	}
	if len(pkgs) > 0 {
		hits, capped := grepGoImports(root, pkgs)
		for _, h := range hits {
			sc.files[h] = true
		}
		if capped {
			sc.truncated = true
		}
	}
	return sc
}

// modulePath reads the module line of root's go.mod.
func modulePath(root string) (string, error) {
	data, err := os.ReadFile(filepath.Join(root, "go.mod"))
	if err != nil {
		return "", err
	}
	for _, line := range strings.Split(string(data), "\n") {
		if m, ok := strings.CutPrefix(strings.TrimSpace(line), "module "); ok {
			if m = strings.TrimSpace(m); m != "" {
				return m, nil
			}
		}
	}
	return "", fmt.Errorf("no module line in go.mod")
}

// fileImports parses one Go file's import block (parser.ImportsOnly —
// cheap and exact). An unreadable or unparseable file (a diff-added new
// file) yields nil: its same-dir sibling entry already covers it.
func fileImports(path string) []string {
	f, err := parser.ParseFile(token.NewFileSet(), path, nil, parser.ImportsOnly)
	if err != nil {
		return nil
	}
	out := make([]string, 0, len(f.Imports))
	for _, imp := range f.Imports {
		if p, err := strconv.Unquote(imp.Path.Value); err == nil && p != "" {
			out = append(out, p)
		}
	}
	return out
}

// grepGoImports finds repo .go files whose import block references one of
// pkgs — an exact-quoted path match, so "a/pkg1" never matches "a/pkg10"
// or "a/pkg1/sub". The budget is the fstools grep engine's: per-file size
// cap (silent skip, same as the engine), total scan cap and match cap
// (both flag truncation — a capped scope is fail-visible). Best-effort:
// unreadable entries are skipped like the engine skips them.
func grepGoImports(root string, pkgs map[string]bool) (files []string, capped bool) {
	pats := make([]*regexp.Regexp, 0, len(pkgs))
	for p := range pkgs {
		pats = append(pats, regexp.MustCompile(`"`+regexp.QuoteMeta(p)+`"`))
	}
	var scanned int64
	_ = filepath.WalkDir(root, func(path string, d os.DirEntry, werr error) error {
		if werr != nil {
			return nil
		}
		if d.IsDir() {
			if name := d.Name(); name == ".git" || name == "node_modules" {
				return filepath.SkipDir
			}
			return nil
		}
		if !d.Type().IsRegular() || !strings.HasSuffix(d.Name(), ".go") {
			return nil
		}
		info, err := d.Info()
		if err != nil || info.Size() > fsGrepFileCap {
			return nil
		}
		if scanned+info.Size() > fsGrepScanCap {
			capped = true
			return nil // skip over-budget file, keep scanning others
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return nil
		}
		scanned += info.Size()
	file:
		for _, line := range strings.Split(string(data), "\n") {
			if len(files) >= fsGrepMatchesCap {
				capped = true
				return errWalkAbort
			}
			for _, re := range pats {
				if re.MatchString(line) {
					files = append(files, path)
					break file
				}
			}
		}
		return nil
	})
	return files, capped
}

// --- scoped executor ------------------------------------------------------

// scopedToolExecutor wraps the rooted FS executor with the grounded
// leg's allowlist: every tool call's path must resolve into the computed
// scope; out-of-scope reads are refused with a model-visible error and
// QueryWithTools journals the refusal (model-visible ⟺ logged holds for
// refusals too). The decorator also enforces the session read budget
// (groundedTotalBytes): the read that crosses the cap is served, then
// every later call is refused — budget exhaustion never frees the leg
// from its verdict, it steers the model to issue it.
type scopedToolExecutor struct {
	inner *fsToolExecutor
	scope groundedScope

	mu        sync.Mutex
	readBytes int
	exhausted bool
}

// Execute implements moa.ToolExecutor.
func (e *scopedToolExecutor) Execute(ctx context.Context, call moa.ToolCall) (string, error) {
	e.mu.Lock()
	if e.exhausted {
		e.mu.Unlock()
		return "", fmt.Errorf("grounded read budget exhausted (%dKB served) — issue your verdict now with what you have", groundedTotalBytes/1024)
	}
	e.mu.Unlock()
	if err := e.checkPath(call); err != nil {
		return "", err
	}
	out, err := e.inner.Execute(ctx, call)
	if err == nil {
		e.mu.Lock()
		e.readBytes += len(out)
		if e.readBytes > groundedTotalBytes {
			e.exhausted = true
		}
		e.mu.Unlock()
	}
	return out, err
}

// checkPath refuses calls whose target resolves outside the allowlist.
// Inputs the inner executor would reject on their own (bad JSON, an
// unknown tool name) fall through to its errors — no scope bypass: those
// calls can never reach the filesystem.
func (e *scopedToolExecutor) checkPath(call moa.ToolCall) error {
	switch call.Name {
	case "read_file", "grep", "glob":
	default:
		return nil // unknown tool: the inner executor errors on it
	}
	var in struct {
		Path string `json:"path"`
	}
	if err := json.Unmarshal(call.Input, &in); err != nil {
		return nil // malformed input: the inner executor errors on it
	}
	abs, err := e.inner.resolve(in.Path)
	if err != nil {
		return err // root/deny violation: already model-visible
	}
	if !e.scope.allows(abs) {
		return fmt.Errorf("%s: %s is outside the grounded review scope (%d allowed entries — the diff's files, their same-dir siblings, and their one-hop import neighborhood)",
			call.Name, e.inner.display(abs), e.scope.count())
	}
	return nil
}

// getExhausted reports whether the total read budget tripped.
func (e *scopedToolExecutor) getExhausted() bool {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.exhausted
}

// --- plan -----------------------------------------------------------------

// groundedPlan is the fan-out's grounded-leg plan: which model runs
// grounded, how it was resolved, whether the mode requires it this
// round, and the computed scope. ok=false is an INIT FAILURE — the leg
// cannot measure what it may read (unparseable diff paths, an empty
// touched set, the test seam); detail says why.
type groundedPlan struct {
	idx        int
	resolvedBy string // "prefs" | "first"
	required   bool
	root       string
	scope      groundedScope
	// rounds is the D9-C tool-loop round cap, resolved ONCE here (by
	// planGrounded via groundedToolRoundsCap) so the review and audit
	// legs run the same budget.
	rounds int
	ok     bool
	detail string
}

// roundsCap returns the plan's round budget — a zero-value plan (built
// outside planGrounded) reads as the D9-C default.
func (p groundedPlan) roundsCap() int {
	if p.rounds >= 1 {
		return p.rounds
	}
	return groundedDefaultRounds
}

// groundedToolRoundsCap resolves the grounded legs' tool-loop round cap.
// The D9-C default ships ACTIVE (40 — a default of 16 ships the #118/#120
// incident unfixed); ODO_GROUNDED_TOOL_ROUNDS, then the
// grounded_tool_rounds: prefs line, is the escape hatch back down, never
// the activation mechanism; an explicit Server field wins (the test
// seam). Above-ceiling values read as the ceiling (QueryWithTools clamps
// there regardless).
func (s *Server) groundedToolRoundsCap() int {
	n := s.groundedToolRounds
	if n < 1 {
		if v := strings.TrimSpace(os.Getenv(groundedRoundsEnv)); v != "" {
			if parsed, err := strconv.Atoi(v); err == nil {
				n = parsed
			}
		}
	}
	if n < 1 {
		if v := adapter.LoadPrefsRaw("grounded_tool_rounds"); v != "" {
			if parsed, err := strconv.Atoi(v); err == nil {
				n = parsed
			}
		}
	}
	if n < 1 {
		return groundedDefaultRounds
	}
	if n > groundedDefaultRounds {
		return groundedDefaultRounds
	}
	return n
}

// groundedLegDeadline is the D9-C wall-clock interlock (DSF option b,
// K3/GLM concur): a round cap above the 16-round baseline scales the
// leg's outer deadline by rounds/16, so a legitimate long chain dies a
// typed ROUND-CAPACITY death — never a misleading wall-clock timeout.
func groundedLegDeadline(base time.Duration, rounds int) time.Duration {
	if rounds > groundedBaselineRounds {
		return base * time.Duration(rounds) / groundedBaselineRounds
	}
	return base
}

// groundedRequiredMode resolves grounded_review_required with the lock's
// default: absent or anything unknown reads "gate_sources".
func groundedRequiredMode() string {
	switch mode := strings.ToLower(strings.TrimSpace(adapter.LoadPrefsRaw("grounded_review_required"))); mode {
	case "always", "never":
		return mode
	default:
		return "gate_sources"
	}
}

// resolveScopeRoot normalizes the scope root EXACTLY like
// newFSToolExecutorRooted (home expansion, absolute, symlink-evaluated):
// the allowlist and the executor's resolve() must key the same path
// space, or a repo under a symlinked root (macOS /var, linked homes)
// would refuse every grounded read.
func resolveScopeRoot(root string) string {
	home, _ := os.UserHomeDir()
	root = expandHomePath(root, home)
	if abs, err := filepath.Abs(root); err == nil {
		root = abs
	}
	if real, err := filepath.EvalSymlinks(root); err == nil {
		root = real
	}
	return root
}

// planGrounded resolves and preps the fan-out's one grounded leg.
//
//   - Model: prefs grounded_reviewer: (model@provider) must exactly match
//     one entry of the fan-out's model line; absent or unmatched ⇒ the
//     line's FIRST entry. resolved_by records "prefs" | "first".
//   - Required: grounded_review_required always ⇒ every round; never ⇒
//     no round; gate_sources (default) ⇒ exactly on diffs touching a D1
//     gate-source path (isGateSourcePath).
//   - Init failure (pathsErr, the test seam): the leg ships degraded —
//     Infra when required (the round fails closed), a plain ungrounded
//     leg otherwise. An unmeasurable touched set also flips required on
//     under any never-exempt mode: "unknown whether gate source" must
//     not read as "not gate source". An EMPTY touched set is NOT an
//     init failure — an empty diff legitimately scopes to nothing.
func (s *Server) planGrounded(models []reviewModel, root string, touched []string, pathsErr error) groundedPlan {
	p := groundedPlan{resolvedBy: "first", root: resolveScopeRoot(root), ok: true, rounds: s.groundedToolRoundsCap()}
	if pref := strings.TrimSpace(adapter.LoadPrefsRaw("grounded_reviewer")); pref != "" {
		wantM, wantP := adapter.ParseModelProvider(pref)
		for i, m := range models {
			if m.model == wantM && m.provider == wantP {
				p.idx, p.resolvedBy = i, "prefs"
				break
			}
		}
	}
	gate := false
	for _, t := range touched {
		if isGateSourcePath(t) {
			gate = true
			break
		}
	}
	switch groundedRequiredMode() {
	case "always":
		p.required = true
	case "never":
	default: // gate_sources
		p.required = gate
	}
	switch {
	case s.groundedInitFailForTest != "":
		p.ok, p.detail = false, s.groundedInitFailForTest
	case pathsErr != nil:
		p.ok, p.detail = false, "cannot enumerate the diff's touched paths: "+pathsErr.Error()
	}
	if !p.ok {
		if groundedRequiredMode() != "never" {
			p.required = true
		}
		return p
	}
	p.scope = computeGroundedScope(p.root, touched)
	// An EMPTY scope is legitimate (an empty diff touches nothing): the
	// grounded leg still runs — every tool call refuses, the verdict
	// contract is unchanged. Init failure is reserved for an
	// UNMEASURABLE touched set (pathsErr, the test seam).
	return p
}

// receipts stamps the identity/scope half of a grounded audit leg's
// result (the tool half arrives after the loop runs).
func (p groundedPlan) receipts(res *auditLegResult) {
	res.Grounded = true
	res.ResolvedBy = p.resolvedBy
	res.ScopeSHA16 = p.scope.sha()
	res.ScopeFiles = p.scope.count()
	res.ScopeTruncated = p.scope.truncated
}

// infraReview is the required-but-init-failed grounded panel leg: it
// never ran, but the mode required grounding this round, so the round
// must fail closed (panelInfraLeg reads Infra; the blocked row then
// journals grounding:"degraded").
func (p groundedPlan) infraReview(m reviewModel, client *moa.Client) ReviewResult {
	return ReviewResult{
		Model:      m.model + "@" + m.provider,
		Verdict:    "needs_fixes",
		Comments:   "grounded leg init failed: " + p.detail,
		Infra:      true,
		Grounded:   true,
		ResolvedBy: p.resolvedBy,
		BaseURL:    scrubBaseURL(client.BaseURL),
	}
}

// groundedLegDegraded reports whether any grounded leg failed this round
// (Infra — init failure or a hard loop error while required). The
// panel_infra blocked row then journals grounding:"degraded": a required
// grounding that never ran is visible, never implicit. Non-grounded
// infra rounds keep their exact byte shape.
func groundedLegDegraded(reviews []ReviewResult) bool {
	for _, r := range reviews {
		if r.Grounded && r.Infra {
			return true
		}
	}
	return false
}

// --- runners ---------------------------------------------------------------

// capToolAudits bounds the journaled tool audit at groundedToolCallsCap.
func capToolAudits(calls []moa.ToolAudit) ([]moa.ToolAudit, bool) {
	if len(calls) > groundedToolCallsCap {
		return calls[:groundedToolCallsCap], true
	}
	return calls, false
}

// toolReadBytes sums the served tool-result bytes (the budget's spend);
// refused calls contribute zero (their ResultBytes is 0 by construction).
func toolReadBytes(calls []moa.ToolAudit) int {
	n := 0
	for _, c := range calls {
		n += c.ResultBytes
	}
	return n
}

// reviewWithModelGrounded runs the D2 grounded review leg: the same
// verdict contract as reviewWithModel (a failed or verdict-less run still
// degrades to needs_fixes, truncation still forces it), plus the scoped
// read-only tool loop (D9-C: maxRounds = plan.rounds — the default-40
// cap — and the wall deadline scales with it, groundedLegDeadline) and
// the full receipt set — including the degraded rows, whose refusals and
// audits stay journaled.
func (s *Server) reviewWithModelGrounded(ctx context.Context, m reviewModel, prompt string, plan groundedPlan) ReviewResult {
	label := m.model + "@" + m.provider
	client := s.sharedMoa()
	scoped := &scopedToolExecutor{inner: newFSToolExecutorRooted(plan.root), scope: plan.scope}
	rounds := plan.roundsCap()
	lctx, cancel := context.WithTimeout(ctx, groundedLegDeadline(s.legTimeout(m.model), rounds))
	defer cancel()
	res, calls, err := client.QueryWithTools(lctx, m.model,
		"You are a code reviewer. Review the following diff and provide your verdict.",
		prompt, moaFSTools(), scoped.Execute, rounds)
	roundsUsed := len(calls) // D9-C: BEFORE capToolAudits truncation
	calls, callsTruncated := capToolAudits(calls)
	rr := ReviewResult{
		Model: label, Grounded: true, ResolvedBy: plan.resolvedBy, BaseURL: scrubBaseURL(client.BaseURL),
		ToolCalls: calls, ToolCallsTruncated: callsTruncated, ToolRoundsUsed: roundsUsed,
		ReadBytes:  toolReadBytes(calls),
		ScopeSHA16: plan.scope.sha(), ScopeFiles: plan.scope.count(), ScopeTruncated: plan.scope.truncated,
		ToolBudgetExhausted: scoped.getExhausted(),
	}
	if err != nil {
		// Fail-closed degradation. Tool-loop exhaustion is an INFRA
		// failure regardless of the required posture: the review leg's
		// reasoning machinery failed before it could judge the diff, so
		// its verdict is not direction evidence (P1 diff #101, 2026-08-29:
		// a gui-only diff's grounded leg burned 8 rounds and its
		// synthesized needs_fixes was counted as real dissent, cascading
		// the diff into a repair-cap block). Infra = required OR a loop
		// exhaustion; init/transport failures keep Infra = required only
		// (they may be posture-specific). panelInfraLeg then parks the
		// round blocked-pending {panel_infra} — recovered on the next
		// pipeline trigger, never by discarding the diff.
		loopExhausted := strings.Contains(err.Error(), "tool loop exceeded")
		rr.Verdict = "needs_fixes"
		rr.Comments = "grounded review failed: " + err.Error()
		if loopExhausted {
			// D9-C fail-visible: name the class. The round-cap death is
			// fail-HARD (infra, no verdict); byte-budget exhaustion is
			// fail-SOFT (tool_budget_exhausted + a verdict) — the row
			// must never blur them. The journaled call names/args above
			// let the next post-mortem distinguish linear progress from
			// degenerate re-reads.
			rr.Comments = "grounded review failed: tool round-cap death (fail-hard): " + err.Error()
		}
		rr.Infra = plan.required || loopExhausted
		return rr
	}
	v := reviewVerdict(label, res.Text, res.Truncated)
	rr.Verdict, rr.Comments, rr.Truncated = v.Verdict, v.Comments, v.Truncated
	// Structured-verdict propagation: the blockers list rides the
	// journaled row, and a malformed structured answer marks the leg
	// Infra regardless of the grounded posture (the model acknowledged
	// the schema and broke it — never direction evidence).
	rr.Blockers, rr.Infra = v.Blockers, v.Infra
	rr.RequestSHA16, rr.RequestBytes = res.RequestSHA16, res.RequestBytes
	if rr.Verdict != "accept" {
		// M18 batch B discipline, mirrored from reviewWithModel: a
		// non-accept leg journals its reasoning (the client's thinking
		// blocks when present, else the full text approximation).
		if res.Thinking != "" {
			rr.ThinkingMD = capDetail(res.Thinking)
		} else {
			rr.ThinkingMD = capDetail(res.Text)
		}
	}
	return rr
}
