package ipc

// M15 B-strategy-1 (O-1, RUNG-0 ONLY): autonomy streak observability.
// This file INSTRUMENTS the human review loop so a later milestone can
// have the data to decide whether any auto-apply rung is justified. The
// journals it reads are the review_action events and diffs rows the
// review loop produces. M16 (O-1 v2): the auto_apply pref IS now
// consumed — "main" activates the auto-land pipeline (autoland.go) —
// but streaks count HUMAN resolutions only: review_action rows carrying
// actor:auto_panel are tallied separately (AutoAccepted) and never feed
// the rung ratchet, or it would grade itself.
//
// Diff classes (deterministic, from the journaled patch file):
//
//	C0  never-auto: touches a protected path (.odo/, wiki/), OR >5 files,
//	    OR >300 changed lines, OR creates a new top-level directory.
//	C1  docs/wiki/comments only: every file is documentation by path, or
//	    every changed content line is blank/comment.
//	C2  tests only: every file is a test by path/name.
//	C3  small in-scope source: <=3 files, <=100 lines, and every touched
//	    path was previously accepted in the same workstream.
//	""  unclassified: anything else (or an unreadable patch).
//
// Streak = consecutive human-accepted diffs of one class with zero human
// rejects of that class and zero detected reverts. A later accepted diff
// that reverts an earlier accept breaks the earlier accept's class streak
// (heuristic: resolves within 7 days after the accept, shares >=1 touched
// path, and mirrors >=80% of the earlier accept's added or removed lines;
// the reset is CONSERVATIVE — the class streak restarts from the
// reverting accept rather than surgically dropping one contribution).
// Diffs whose patch files are unreadable cannot be classified or
// revert-checked; the report counts them honestly.
//
// Thresholds: 10 clean accepts -> rung-1 eligibility; +20 more (30 total)
// -> rung-2. All thresholds live in the constants block below.
//
// fix-INT W5 (Guardian risk taxonomy): the same scan tallies the second
// audit table — RiskReport — from the risk_class/risk_classifier/
// risk_evidence keys the W5 write sites journal (risk.go). Risk classes
// measure HAZARD of content (credential_probe, data_exfil, destructive,
// security_weakening, supply_chain); C-classes measure automaticability
// of shape. The two axes are orthogonal — a C1 docs accept is "none"
// risk; a C3 small source diff reading .env is credential_probe.
// Multi-label: one resolution can feed several risk rows (column sums
// may exceed Resolutions). Rows lacking risk_class entirely are the
// pre-W5 remainder, counted as Unrated. Observational only: these
// tallies never feed the streaks (gating belongs to the ratchet wave —
// instrument-before-gate, the M15 rung-0 precedent).

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/yingliang-zhang/odo/internal/adapter"
	"github.com/yingliang-zhang/odo/internal/git"
	"github.com/yingliang-zhang/odo/internal/store"
)

// Autonomy classification + rung thresholds. See the header comment for
// the class definitions and the streak rule.
const (
	// C0 gates (never-auto).
	autonomyMaxC0Files = 5   // > this many files => C0
	autonomyMaxC0Lines = 300 // > this many changed lines (added+removed) => C0

	// C3 bounds (small in-scope source).
	autonomyMaxC3Files = 3
	autonomyMaxC3Lines = 100

	// Rung eligibility: clean human-accept streaks per class.
	autonomyRung1Streak = 10                       // 10 clean accepts -> rung-1 eligibility
	autonomyRung2Streak = autonomyRung1Streak + 20 // +20 more -> rung-2

	// Revert heuristic: a later accept resolving within this window that
	// shares a path and mirrors this fraction of added/removed lines.
	autonomyRevertWindowDays = 7
	autonomyRevertLineRatio  = 0.8
)

// AutonomyClassReport is one class row of the autonomy report.
type AutonomyClassReport struct {
	Class         string `json:"class"` // "C0" | "C1" | "C2" | "C3" | "unclassified"
	Description   string `json:"description"`
	Accepted      int    `json:"accepted"`
	Rejected      int    `json:"rejected"`
	Streak        int    `json:"streak"`
	NextThreshold int    `json:"next_threshold"` // next rung target (0 = terminal / n/a for C0 & unclassified)
	Eligible      string `json:"eligible"`       // "" | "rung-1" | "rung-2" (always "" at rung 0 — eligibility math only)
}

// AutonomyReport is the rung-0 observability snapshot, shared verbatim by
// `odo autonomy audit` (read-only sqlite) and the daemon's autonomy_status
// IPC (same SQL on the live store).
type AutonomyReport struct {
	ProjectRoot          string                `json:"project_root"`
	Journal              string                `json:"journal"`
	WorkstreamsScanned   int                   `json:"workstreams_scanned"`
	ConversationsScanned int                   `json:"conversations_scanned"`
	Resolutions          int                   `json:"resolutions"`      // human accept/reject events (auto-land excluded)
	AutoAccepted         int                   `json:"auto_accepted"`    // M16: diffs landed by the auto-land pipeline (never streak-feeding)
	UnreadableDiffs      int                   `json:"unreadable_diffs"` // patch file missing/parse error: unclassified + no revert evidence
	AutoApply            string                `json:"auto_apply"`       // prefs value ("main" = M16 auto-land consumed)
	CurrentRung          int                   `json:"current_rung"`     // always 0 today
	RungThresholds       map[string]int        `json:"rung_thresholds"`
	RevertCheck          string                `json:"revert_check"` // how streaks treat reverts
	Classes              []AutonomyClassReport `json:"classes"`
	Settle               SettleTallies         `json:"settle"` // M18 ladder facts (audit header line)
	Risk                 RiskReport            `json:"risk"`   // fix-INT W5 Guardian risk tallies (second audit table)
}

// SettleTallies (M18 batch B) are the settle-ladder facts scanned from the
// same journal surfaces the ladder derives its state from — one compact
// audit header line, never per-row noise. Pure observability: the
// classification above never reads them (the regression test pins that
// these rows can't move a single class or streak).
type SettleTallies struct {
	ReviseRounds     int `json:"revise_rounds"`      // auto_revise_round rows (spawned repair rounds)
	Suspensions      int `json:"suspensions"`        // memory_update{cause:ladder_suspended}
	Resumes          int `json:"resumes"`            // memory_update{cause:ladder_resumed} (human accepts only)
	ReviseNoProgress int `json:"revise_no_progress"` // blocked{revise_no_progress} hard stops
	VisualGateBlocks int `json:"visual_gate_blocks"` // blocked{human_gate_visual} (visual class → human)
}

// RiskReport (fix-INT W5) is the second audit table, parallel to the
// C-class table (not a cross-tab — that needs a diff×diff join and
// belongs with the ratchet wave). ComputeAutonomy scans the same
// review_action rows; when risk_class is present it tallies each class
// into the row's bucket (multi-label: a diff contributes ≤1 to each of
// its classes — column sums may exceed Resolutions, printed honestly).
// Pure observability: the C0–C3 classification above never reads these
// (risk classes measure hazard, not automaticability; the regression
// test pins that a full risk vocabulary moves no streak).
type RiskReport struct {
	Classes []RiskClassReport `json:"classes"`
	Unrated int               `json:"unrated"` // pre-W5 rows (no risk_class key)
}

// RiskClassReport is one risk-class row of the audit table.
type RiskClassReport struct {
	Class        string `json:"class"`
	Description  string `json:"description"`
	Accepted     int    `json:"accepted"`      // human accept
	Rejected     int    `json:"rejected"`      // human reject
	AutoAccepted int    `json:"auto_accepted"` // auto-panel accept
	AutoBlocked  int    `json:"auto_blocked"`  // auto_land_blocked (any reason)
}

// riskClassDescription labels the risk rows (also the CLI's legend).
var riskClassDescription = map[string]string{
	"credential_probe":   "reads secret-shaped material (env *_KEY/_TOKEN/_SECRET/_PASSWORD, ~/.ssh/id_*, .aws/credentials, .gnupg, keychain)",
	"data_exfil":         "co-adds a local-source read + network egress in one hunk",
	"destructive":        "file deletion, or rm -rf/RemoveAll/rmtree/DROP TABLE/push --force/reset --hard in added lines",
	"security_weakening": "added line weakens a control (InsecureSkipVerify, --insecure, //nosec, chmod 777/666, CORS *, auth-disable)",
	"supply_chain":       "touches a dependency manifest/lockfile (autoLandSupplyChainFiles SSOT)",
	"none":               "rated clean (no class trigger; distinguishes rated-clean from pre-W5 unrated)",
}

// classDescription labels the class rows (also the CLI's legend).
var classDescription = map[string]string{
	"C0":           "never-auto (protected paths / >5 files / >300 lines / new top-level dir)",
	"C1":           "docs, wiki, comments only",
	"C2":           "tests only",
	"C3":           "small in-scope source (<=3 files, <=100 lines, previously accepted paths)",
	"unclassified": "everything else (excluded from rung progress)",
}

// autonomyClassOrder fixes display/JSON row order.
var autonomyClassOrder = []string{"C0", "C1", "C2", "C3", "unclassified"}

// docExts classify a file as documentation by name.
var docExts = map[string]bool{".md": true, ".mdx": true, ".rst": true, ".adoc": true, ".txt": true}

// isDocsPath reports whether a path is documentation (docs dirs or doc
// extensions). wiki/ files qualify but never reach this check — the
// protected-path C0 gate runs first.
func isDocsPath(p string) bool {
	if ext := strings.ToLower(filepath.Ext(p)); docExts[ext] {
		return true
	}
	switch strings.Split(filepath.ToSlash(p), "/")[0] {
	case "docs", "doc", "documentation":
		return true
	}
	return false
}

// isTestPath reports whether a path is a test file (test dirs or test
// naming conventions across the repo's languages).
func isTestPath(p string) bool {
	p = filepath.ToSlash(p)
	base := p[strings.LastIndex(p, "/")+1:]
	if strings.HasSuffix(base, "_test.go") || strings.HasSuffix(base, "_test.py") ||
		strings.HasPrefix(base, "test_") || strings.Contains(base, ".test.") || strings.Contains(base, ".spec.") {
		return true
	}
	switch strings.Split(p, "/")[0] {
	case "test", "tests", "__tests__":
		return true
	}
	return false
}

// classifyDiff assigns the class per the header definitions, evaluated in
// the strict order C0 -> C1 -> C2 -> C3 -> unclassified. newTopDir reports
// the new-top-level-directory signal (resolved by the caller against the
// diff's base commit); inScope holds paths previously accepted in this
// workstream.
func classifyDiff(stat git.PatchStat, newTopDir bool, inScope map[string]bool) string {
	if len(stat.Files) == 0 {
		return "unclassified" // nothing reviewable to classify (pure rename/mode change)
	}
	files, lines := len(stat.Files), stat.Added+stat.Removed

	// C0 — never-auto.
	if files > autonomyMaxC0Files || lines > autonomyMaxC0Lines || newTopDir {
		return "C0"
	}
	for _, f := range stat.Files {
		if isProtectedPath(f.Path) {
			return "C0"
		}
	}

	// C1 — docs/comments only.
	docs := true
	for _, f := range stat.Files {
		if !isDocsPath(f.Path) && !f.CommentOnly {
			docs = false
			break
		}
	}
	if docs {
		return "C1"
	}

	// C2 — tests only.
	tests := true
	for _, f := range stat.Files {
		if !isTestPath(f.Path) {
			tests = false
			break
		}
	}
	if tests {
		return "C2"
	}

	// C3 — small in-scope source.
	if files <= autonomyMaxC3Files && lines <= autonomyMaxC3Lines && len(inScope) > 0 {
		in := true
		for _, f := range stat.Files {
			if !inScope[f.Path] {
				in = false
				break
			}
		}
		if in {
			return "C3"
		}
	}
	return "unclassified"
}

// resolutionRec joins a human review event to its diff row.
type resolutionRec struct {
	wsID   int64
	convID int64
	seq    int
	at     time.Time
	action string
	diff   store.Diff
}

// acceptedRec is one accepted diff's revert-matching record.
type acceptedRec struct {
	class   string
	at      time.Time
	paths   map[string]bool
	added   int
	removed int
}

// TopDirsResolver resolves the top-level entries of the project tree at a
// base commit. Production uses git ls-tree; tests inject a stub.
type TopDirsResolver func(baseSHA string) (map[string]bool, error)

// parseJournalTime reads a journal created_at. The sqlite driver
// normalizes datetime('now') values to RFC3339 on scan; raw text on disk
// stays "2006-01-02 15:04:05" UTC. Both parse as UTC — comparisons stay
// consistent either way.
func parseJournalTime(s string) (time.Time, error) {
	if t, err := time.Parse(time.RFC3339, s); err == nil {
		return t, nil
	}
	return time.Parse("2006-01-02 15:04:05", s)
}

// GitTopDirsResolver returns the resolver backed by `git ls-tree` at the
// project root (read-only; the git binary is an existing runtime dep).
func GitTopDirsResolver(projectRoot string) TopDirsResolver {
	return func(baseSHA string) (map[string]bool, error) {
		if baseSHA == "" {
			return nil, fmt.Errorf("no base sha")
		}
		names, err := git.ListTreeNames(projectRoot, baseSHA)
		if err != nil {
			return nil, err
		}
		out := make(map[string]bool, len(names))
		for _, n := range names {
			out[n] = true
		}
		return out, nil
	}
}

// ComputeAutonomy walks every resolution of the project in time order,
// classifies each diff, and folds accept/reject/revert events into
// per-class streaks. Pure read of the journal + patch files; the top-dir
// signal is the only repo lookup and it is injected.
func ComputeAutonomy(ctx context.Context, st *store.Store, project store.Project, resolveTopDirs TopDirsResolver) (AutonomyReport, error) {
	report := AutonomyReport{
		ProjectRoot: project.RootPath,
		Journal:     filepath.Join(project.RootPath, ".odo", "journal.sqlite"),
		AutoApply:   adapter.ReadSettings().AutoApply,
		CurrentRung: 0, // rungs stay streak-derived; auto_apply==main consumes the pref without a rung elevation (M16)
		RungThresholds: map[string]int{
			"rung_1": autonomyRung1Streak,
			"rung_2": autonomyRung2Streak,
		},
		RevertCheck: fmt.Sprintf("heuristic: >=%.0f%% mirrored lines, >=1 shared path, within %dd",
			autonomyRevertLineRatio*100, autonomyRevertWindowDays),
	}

	streams, err := st.ListWorkstreams(ctx, project.ID)
	if err != nil {
		return report, err
	}
	report.WorkstreamsScanned = len(streams)

	// fix-INT W5: the risk table's tally rows in severity-rank order
	// (riskClassOrder, risk.go). Assembled into report at return.
	riskTally := map[string]*RiskClassReport{}
	for _, c := range riskClassOrder {
		riskTally[c] = &RiskClassReport{Class: c, Description: riskClassDescription[c]}
	}

	var recs []resolutionRec
	for _, w := range streams {
		c, cerr := st.GetActiveConversation(ctx, w.ID)
		if cerr != nil {
			continue
		}
		report.ConversationsScanned++
		events, lerr := st.ListEvents(ctx, c.ID, 0)
		if lerr != nil {
			continue
		}
		diffs, derr := st.ListDiffs(ctx, c.ID)
		if derr != nil {
			continue
		}
		byID := make(map[int64]store.Diff, len(diffs))
		for _, d := range diffs {
			byID[d.ID] = d
		}
		for _, ev := range events {
			// M18 batch B: settle-ladder tallies ride the same scan. They
			// fold ONLY into report.Settle — classification paths below
			// never see them (the blocked/round/ledger rows aren't
			// accept/reject resolutions by construction; the batch-B
			// regression test pins that).
			if ev.Type == store.EventMemoryUpdate {
				var m struct {
					Layer string `json:"layer"`
					Cause string `json:"cause"`
				}
				if json.Unmarshal(ev.Payload, &m) == nil && m.Layer == "auto_land" {
					switch m.Cause {
					case "ladder_suspended":
						report.Settle.Suspensions++
					case "ladder_resumed":
						report.Settle.Resumes++
					}
				}
				continue
			}
			if ev.Type != store.EventReviewAction {
				continue
			}
			var p struct {
				Action    string    `json:"action"`
				DiffID    int64     `json:"diff_id"`
				Actor     string    `json:"actor"`
				Reason    string    `json:"reason"`
				RiskClass *[]string `json:"risk_class"` // nil = pre-W5 row (unrated bucket)
			}
			if json.Unmarshal(ev.Payload, &p) != nil {
				continue
			}
			switch p.Action {
			case "auto_revise_round":
				report.Settle.ReviseRounds++
			case "auto_land_blocked":
				switch p.Reason {
				case "revise_no_progress":
					report.Settle.ReviseNoProgress++
				case "human_gate_visual":
					report.Settle.VisualGateBlocks++
				}
			}
			// fix-INT W5: fold the risk receipt into the second audit table.
			// moa_review / auto_revise_round rows carry the receipt too, but
			// they resolve nothing — their classes feed no bucket (a verdict
			// evidence row and a spawned round are not accept/reject/block
			// outcomes); they DO rate, so they never inflate Unrated. Rows
			// with NO risk_class key are the pre-W5 remainder — counted
			// honestly (the unclassified/unreadable_diffs posture).
			switch p.Action {
			case "accept", "reject", "auto_land_blocked", "moa_review", "auto_revise_round":
				if p.RiskClass == nil {
					report.Risk.Unrated++
					break
				}
				seen := map[string]bool{} // ≤1 per class per row (defensive; classifyRisk emits unique classes)
				for _, class := range *p.RiskClass {
					if seen[class] {
						continue
					}
					seen[class] = true
					row := riskTally[class]
					if row == nil { // forward-compat: a future wave's class still reports
						row = &RiskClassReport{Class: class, Description: "unknown class (emitter newer than this audit)"}
						riskTally[class] = row
					}
					switch {
					case p.Action == "auto_land_blocked":
						row.AutoBlocked++
					case p.Action == "accept" && p.Actor == autoActor:
						row.AutoAccepted++
					case p.Action == "accept" && p.Actor == loopActor:
						// M19: loop lands ride actor auto_loop — invisible to
						// the autonomy audit (the ratchet must not drink its
						// own bathwater, the M16 rule extended).
					case p.Action == "accept":
						row.Accepted++
					case p.Action == "reject":
						row.Rejected++
					}
				}
			}
			// W6 regression-pinned: parked goals never move human-streak
			// math — their user_message{park:true} rows died at the
			// review_action type filter above, and the queue's own
			// review_action rows (run_prompt{goal_seqs},
			// parked_goal_dropped{goal_seq}) fall through here.
			if p.Action != "accept" && p.Action != "reject" {
				continue
			}
			// M16: pipeline-landed diffs are tallied, never streaked —
			// the ratchet must not drink its own bathwater. M19: loop
			// lands (auto_loop) are likewise excluded — they contribute
			// no ratchet input either way (loop rows invisible to the audit).
			if p.Actor == autoActor {
				if p.Action == "accept" {
					report.AutoAccepted++
				}
				continue
			}
			if p.Actor == loopActor {
				continue
			}
			d, ok := byID[p.DiffID]
			if !ok {
				continue
			}
			at, terr := parseJournalTime(ev.CreatedAt)
			if terr != nil {
				continue
			}
			recs = append(recs, resolutionRec{wsID: w.ID, convID: c.ID, seq: ev.Seq, at: at, action: p.Action, diff: d})
		}
	}
	// Chronological resolution order; deterministic on same-second ties.
	sort.SliceStable(recs, func(i, j int) bool {
		if !recs[i].at.Equal(recs[j].at) {
			return recs[i].at.Before(recs[j].at)
		}
		if recs[i].convID != recs[j].convID {
			return recs[i].convID < recs[j].convID
		}
		return recs[i].seq < recs[j].seq
	})
	report.Resolutions = len(recs)

	tally := map[string]*AutonomyClassReport{}
	for _, c := range autonomyClassOrder {
		tally[c] = &AutonomyClassReport{
			Class: c, Description: classDescription[c],
			NextThreshold: autonomyRung1Streak,
		}
	}
	tally["C0"].NextThreshold = 0           // never-auto: no rung progress
	tally["unclassified"].NextThreshold = 0 // excluded from rung progress

	wsAcceptedPaths := map[int64]map[string]bool{} // C3 in-scope evidence
	var accepted []acceptedRec                     // revert candidates
	topDirCache := map[string]map[string]bool{}    // baseSHA -> tree names

	classify := func(rec resolutionRec) (string, git.PatchStat) {
		stat, perr := git.PatchStats(rec.diff.PathOnDisk)
		if perr != nil {
			report.UnreadableDiffs++
			return "unclassified", git.PatchStat{}
		}
		// New-top-dir signal: any new file whose first path segment was
		// absent from the base tree. Unresolvable bases (missing SHA,
		// object pruned) leave the signal false — never C0 by accident.
		newTop := false
		base := ""
		if rec.diff.BaseSHA != nil {
			base = *rec.diff.BaseSHA
		}
		var tree map[string]bool
		if base != "" && resolveTopDirs != nil {
			var ok bool
			if tree, ok = topDirCache[base]; !ok {
				if t, rerr := resolveTopDirs(base); rerr == nil {
					tree = t
				}
				topDirCache[base] = tree
			}
		}
		for _, f := range stat.Files {
			if !f.NewFile {
				continue
			}
			p := filepath.ToSlash(f.Path)
			slash := strings.Index(p, "/")
			if slash > 0 && tree != nil && !tree[p[:slash]] {
				newTop = true
				break
			}
		}
		return classifyDiff(stat, newTop, wsAcceptedPaths[rec.wsID]), stat
	}

	// resetStreak zeroes a row's streak and rung progress (reject or
	// revert). Non-rung classes stay threshold-less.
	resetStreak := func(row *AutonomyClassReport) {
		row.Streak = 0
		row.Eligible = ""
		if row.Class == "C0" || row.Class == "unclassified" {
			row.NextThreshold = 0
		} else {
			row.NextThreshold = autonomyRung1Streak
		}
	}

	for _, rec := range recs {
		class, stat := classify(rec)
		row := tally[class]
		if rec.action == "reject" {
			row.Rejected++
			resetStreak(row)
			continue
		}
		row.Accepted++
		// Revert sweep: does this accept undo an earlier accept?
		for _, a := range accepted {
			if a.at.Add(autonomyRevertWindowDays * 24 * time.Hour).Before(rec.at) {
				continue // outside the 7-day window (accepted is ordered)
			}
			shared := false
			for p := range a.paths {
				if containsPath(stat, p) {
					shared = true
					break
				}
			}
			if !shared {
				continue
			}
			// Mirror clauses guard against zero sides: 80% of an absent
			// side is 0, and any change "matches 0".
			if (a.added > 0 && float64(stat.Removed) >= autonomyRevertLineRatio*float64(a.added)) ||
				(a.removed > 0 && float64(stat.Added) >= autonomyRevertLineRatio*float64(a.removed)) {
				// The earlier accept did not stick — its class streak
				// breaks here (this accept itself still counts).
				resetStreak(tally[a.class])
			}
		}
		// Grow C3's evidence base + record for future revert matching.
		if len(stat.Files) > 0 {
			if wsAcceptedPaths[rec.wsID] == nil {
				wsAcceptedPaths[rec.wsID] = map[string]bool{}
			}
			paths := map[string]bool{}
			for _, f := range stat.Files {
				wsAcceptedPaths[rec.wsID][f.Path] = true
				paths[f.Path] = true
			}
			accepted = append(accepted, acceptedRec{
				class: class, at: rec.at, paths: paths, added: stat.Added, removed: stat.Removed,
			})
		}
		row.Streak++
		switch {
		case row.Streak >= autonomyRung2Streak:
			row.Eligible = "rung-2"
			row.NextThreshold = 0
		case row.Streak >= autonomyRung1Streak:
			row.Eligible = "rung-1"
			row.NextThreshold = autonomyRung2Streak
		}
	}

	report.Classes = []AutonomyClassReport{}
	for _, c := range autonomyClassOrder {
		report.Classes = append(report.Classes, *tally[c])
	}
	// fix-INT W5: risk rows in severity-rank order; forward-compat
	// extras (classes this audit predates) sorted for determinism.
	report.Risk.Classes = make([]RiskClassReport, 0, len(riskTally))
	for _, c := range riskClassOrder {
		report.Risk.Classes = append(report.Risk.Classes, *riskTally[c])
		delete(riskTally, c)
	}
	if len(riskTally) > 0 {
		extras := make([]string, 0, len(riskTally))
		for c := range riskTally {
			extras = append(extras, c)
		}
		sort.Strings(extras)
		for _, c := range extras {
			report.Risk.Classes = append(report.Risk.Classes, *riskTally[c])
		}
	}
	return report, nil
}

// containsPath reports whether the patch stats touch path p.
func containsPath(stat git.PatchStat, p string) bool {
	for _, f := range stat.Files {
		if f.Path == p {
			return true
		}
	}
	return false
}

// handleAutonomyStatus implements autonomy_status: the rung-0 snapshot for
// the DiffViewer header, computed from the same journal reads the CLI
// shells (daemon-side, against the live store — the SSOT).
func (s *Server) handleAutonomyStatus(ctx context.Context, req Request) (Response, error) {
	p, err := s.store.GetProjectByRoot(ctx, s.projectRoot)
	if err != nil {
		return Response{}, err
	}
	report, err := ComputeAutonomy(ctx, s.store, p, GitTopDirsResolver(s.projectRoot))
	if err != nil {
		return Response{}, err
	}
	return Response{Autonomy: &report}, nil
}
