package ipc

// M15 B-strategy-1 (O-1, RUNG-0 ONLY): autonomy streak observability.
// This file INSTRUMENTS the human review loop so a later milestone can
// have the data to decide whether any auto-apply rung is justified. It
// changes no behavior: nothing here applies, skips, or re-orders a
// review; the journals it reads are the same review_action events and
// diffs rows the human loop already produces. The auto_apply pref is
// parsed (settings.go) and displayed, never consumed.
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
	Resolutions          int                   `json:"resolutions"`      // human accept/reject events
	UnreadableDiffs      int                   `json:"unreadable_diffs"` // patch file missing/parse error: unclassified + no revert evidence
	AutoApply            string                `json:"auto_apply"`       // prefs value, PARSED ONLY (rung 0)
	CurrentRung          int                   `json:"current_rung"`     // always 0 today
	RungThresholds       map[string]int        `json:"rung_thresholds"`
	RevertCheck          string                `json:"revert_check"` // how streaks treat reverts
	Classes              []AutonomyClassReport `json:"classes"`
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
		if strings.HasPrefix(f.Path, ".odo/") || strings.HasPrefix(f.Path, "wiki/") {
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
		CurrentRung: 0, // rung-0 instrumentation: no auto-apply exists
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
			if ev.Type != store.EventReviewAction {
				continue
			}
			var p struct {
				Action string `json:"action"`
				DiffID int64  `json:"diff_id"`
			}
			if json.Unmarshal(ev.Payload, &p) != nil {
				continue
			}
			if p.Action != "accept" && p.Action != "reject" {
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
