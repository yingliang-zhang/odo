package ipc

// M15 (O-1 rung-0): the autonomy engine battery — pure classification
// boundaries first, then journal-level streak math, revert-breaks (inside
// and outside the 7-day window), and the daemon IPC handler.

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/yingliang-zhang/odo/internal/git"
	"github.com/yingliang-zhang/odo/internal/store"
)

// ------------------------------------------------------------- class boundaries

// fs builds one FileStat with content lines (CommentOnly defaults false —
// a real content line is assumed).
func fs(path string, added, removed int) git.FileStat {
	return git.FileStat{Path: path, Added: added, Removed: removed}
}

func TestClassifyDiffBoundaries(t *testing.T) {
	fileCount := func(n int) git.PatchStat {
		var st git.PatchStat
		for i := range n {
			st.Files = append(st.Files, fs(fmt.Sprintf("src/f%d.go", i), 10, 10))
			st.Added += 10
			st.Removed += 10
		}
		return st
	}
	lineCount := func(lines int) git.PatchStat {
		return git.PatchStat{Files: []git.FileStat{fs("src/f.go", lines, 0)}, Added: lines}
	}

	tests := []struct {
		name      string
		stat      git.PatchStat
		newTop    bool
		inScope   map[string]bool
		wantClass string
	}{
		{"5 files not C0", fileCount(5), false, nil, "unclassified"},
		{"6 files C0", fileCount(6), false, nil, "C0"},
		{"300 lines not C0", lineCount(300), false, nil, "unclassified"},
		{"301 lines C0", lineCount(301), false, nil, "C0"},
		{"protected .odo path C0", git.PatchStat{Files: []git.FileStat{fs(".odo/memory.md", 1, 0)}, Added: 1}, false, nil, "C0"},
		{"protected wiki path C0", git.PatchStat{Files: []git.FileStat{fs("wiki/x.md", 1, 0)}, Added: 1}, false, nil, "C0"},
		{"new top-level dir C0", lineCount(10), true, nil, "C0"},
		{"docs + docs dir C1", git.PatchStat{
			Files: []git.FileStat{fs("README.md", 3, 1), fs("docs/guide.txt", 2, 0)}, Added: 5, Removed: 1}, false, nil, "C1"},
		{"comment-only source C1", git.PatchStat{
			Files: []git.FileStat{{Path: "src/f.go", Added: 4, Removed: 1, CommentOnly: true}}, Added: 4, Removed: 1}, false, nil, "C1"},
		{"docs + source not C1", git.PatchStat{
			Files: []git.FileStat{fs("README.md", 1, 0), fs("src/f.go", 1, 0)}, Added: 2}, false, nil, "unclassified"},
		{"tests only C2", git.PatchStat{
			Files: []git.FileStat{fs("internal/ipc/auto_test.go", 5, 5), fs("gui/e2e/panel.spec.ts", 2, 0)}, Added: 7, Removed: 5}, false, nil, "C2"},
		{"tests + source not C2", git.PatchStat{
			Files: []git.FileStat{fs("x_test.go", 2, 0), fs("src/f.go", 2, 0)}, Added: 4}, false, nil, "unclassified"},
		{"C3 small in-scope", git.PatchStat{
			Files: []git.FileStat{fs("src/f.go", 40, 30)}, Added: 40, Removed: 30}, false,
			map[string]bool{"src/f.go": true}, "C3"},
		{"C3 boundary 3 files 100 lines", git.PatchStat{
			Files: []git.FileStat{fs("a.go", 30, 10), fs("b.go", 30, 10), fs("c.go", 15, 5)}, Added: 75, Removed: 25}, false,
			map[string]bool{"a.go": true, "b.go": true, "c.go": true}, "C3"},
		{"101 lines not C3", git.PatchStat{
			Files: []git.FileStat{fs("a.go", 71, 30)}, Added: 71, Removed: 30}, false,
			map[string]bool{"a.go": true}, "unclassified"},
		{"out-of-scope not C3", git.PatchStat{
			Files: []git.FileStat{fs("src/f.go", 2, 2)}, Added: 2, Removed: 2}, false,
			map[string]bool{"src/other.go": true}, "unclassified"},
		{"empty in-scope set not C3", git.PatchStat{
			Files: []git.FileStat{fs("src/f.go", 2, 2)}, Added: 2, Removed: 2}, false, nil, "unclassified"},
		{"no files unclassified", git.PatchStat{}, false, nil, "unclassified"},
	}
	for _, tc := range tests {
		if got := classifyDiff(tc.stat, tc.newTop, tc.inScope); got != tc.wantClass {
			t.Errorf("%s: class = %q, want %q", tc.name, got, tc.wantClass)
		}
	}
}

// TestPatchStatsCommentOnly pins the comment-only detection over real
// patch text (classifyDiff consumes FileStat.CommentOnly).
func TestPatchStatsCommentOnly(t *testing.T) {
	patch := strings.Join([]string{
		"diff --git a/src/f.go b/src/f.go",
		"--- a/src/f.go",
		"+++ b/src/f.go",
		"@@ -1,3 +1,4 @@",
		" // leading comment",
		"+// added comment",
		"+",
		"+ * doc line",
		"-// removed comment",
		"",
	}, "\n")
	path := filepath.Join(t.TempDir(), "c.diff")
	if err := os.WriteFile(path, []byte(patch), 0o644); err != nil {
		t.Fatal(err)
	}
	stat, err := git.PatchStats(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(stat.Files) != 1 || stat.Added != 3 || stat.Removed != 1 {
		t.Fatalf("stat = %+v, want 1 file +3/-1", stat)
	}
	if !stat.Files[0].CommentOnly {
		t.Error("all-comment change not detected as CommentOnly")
	}
}

func TestPatchStatsNotCommentOnly(t *testing.T) {
	patch := "diff --git a/src/f.go b/src/f.go\n--- a/src/f.go\n+++ b/src/f.go\n@@ -1 +1,2 @@\n // c\n+x := 1\n"
	path := filepath.Join(t.TempDir(), "n.diff")
	if err := os.WriteFile(path, []byte(patch), 0o644); err != nil {
		t.Fatal(err)
	}
	stat, err := git.PatchStats(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(stat.Files) != 1 || stat.Files[0].CommentOnly {
		t.Errorf("stat = %+v, want CommentOnly false (real code line)", stat)
	}
}

// ------------------------------------------------------------- journal fixtures

// autonomyFixture is a seeded project with one workstream + conversation.
type autonomyFixture struct {
	st  *store.Store
	p   store.Project
	c   store.Conversation
	dir string // patch files live here
}

func newAutonomyFixture(t *testing.T) autonomyFixture {
	t.Helper()
	ctx := context.Background()
	dir := t.TempDir()
	st, err := store.Open(filepath.Join(dir, ".odo", "journal.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	p, err := st.CreateOrGetProject(ctx, dir, "p")
	if err != nil {
		t.Fatal(err)
	}
	w, err := st.CreateOrGetWorkstream(ctx, p.ID, "main")
	if err != nil {
		t.Fatal(err)
	}
	c, err := st.CreateConversation(ctx, w.ID, "")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	return autonomyFixture{st: st, p: p, c: c, dir: dir}
}

// patchDoc / patchSrc / patchTest build compact but well-formed unified
// diffs the parser can count.
func patchDoc(path string, adds int) string {
	var b strings.Builder
	fmt.Fprintf(&b, "diff --git a/%s b/%s\n--- a/%s\n+++ b/%s\n@@ -1 +1,%d @@\n", path, path, path, path, adds)
	for i := range adds {
		fmt.Fprintf(&b, "+line %d\n", i)
	}
	return b.String()
}

func patchSrc(path string, adds, dels int, newFile bool) string {
	var b strings.Builder
	fmt.Fprintf(&b, "diff --git a/%s b/%s\n", path, path)
	if newFile {
		b.WriteString("new file mode 100644\n--- /dev/null\n")
	} else {
		fmt.Fprintf(&b, "--- a/%s\n", path)
	}
	fmt.Fprintf(&b, "+++ b/%s\n@@ -1,%d +1,%d @@\n", path, dels, adds)
	for i := range dels {
		fmt.Fprintf(&b, "-old %d\n", i)
	}
	for i := range adds {
		fmt.Fprintf(&b, "+new %d\n", i)
	}
	return b.String()
}

// revertPatch removes `dels` lines and adds `adds` — a revert candidate.
func patchDel(path string, dels int) string { return patchSrc(path, 0, dels, false) }

// resolve records a human review for the latest diff at the given
// timestamp ("2006-01-02 15:04:05").
func (f autonomyFixture) resolve(t *testing.T, d store.Diff, action, at string) {
	t.Helper()
	ev, err := f.st.AppendEvent(context.Background(), f.c.ID, store.EventReviewAction,
		fmt.Sprintf(`{"action":%q,"diff_id":%d}`, action, d.ID))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.st.DB().Exec(`UPDATE events SET created_at = ? WHERE id = ?`, at, ev.ID); err != nil {
		t.Fatal(err)
	}
}

// addDiff writes a patch file and inserts its diff row.
func (f autonomyFixture) addDiff(t *testing.T, name, patch string) store.Diff {
	t.Helper()
	path := filepath.Join(f.dir, name)
	if err := os.WriteFile(path, []byte(patch), 0o644); err != nil {
		t.Fatal(err)
	}
	d, err := f.st.InsertDiff(context.Background(), f.c.ID, path, "")
	if err != nil {
		t.Fatal(err)
	}
	return d
}

func classRow(t *testing.T, r AutonomyReport, class string) AutonomyClassReport {
	t.Helper()
	for _, c := range r.Classes {
		if c.Class == class {
			return c
		}
	}
	t.Fatalf("class row %q missing: %+v", class, r.Classes)
	return AutonomyClassReport{}
}

// ------------------------------------------------------------- streak tests

// TestAutonomyStreaks: C1 accumulates and resets on a same-class reject;
// a first-time source file is unclassified, then becomes C3 once its path
// has an accepted history in the workstream.
func TestAutonomyStreaks(t *testing.T) {
	f := newAutonomyFixture(t)
	t.Setenv("HOME", t.TempDir()) // auto_apply reads defaults ("off")

	d1 := f.addDiff(t, "d1.diff", patchDoc("README.md", 3))
	f.resolve(t, d1, "accept", "2026-08-01 10:00:01")
	d2 := f.addDiff(t, "d2.diff", patchDoc("docs/guide.md", 2))
	f.resolve(t, d2, "accept", "2026-08-01 10:00:02")
	d3 := f.addDiff(t, "d3.diff", patchSrc("src/main.go", 5, 0, false))
	f.resolve(t, d3, "accept", "2026-08-01 10:00:03")
	d4 := f.addDiff(t, "d4.diff", patchSrc("src/main.go", 2, 1, false))
	f.resolve(t, d4, "accept", "2026-08-01 10:00:04")
	d5 := f.addDiff(t, "d5.diff", patchDoc("README.md", 1))
	f.resolve(t, d5, "reject", "2026-08-01 10:00:05")
	d6 := f.addDiff(t, "d6.diff", patchDoc("README.md", 4))
	f.resolve(t, d6, "accept", "2026-08-01 10:00:06")

	r, err := ComputeAutonomy(context.Background(), f.st, f.p, nil)
	if err != nil {
		t.Fatal(err)
	}
	if r.Resolutions != 6 {
		t.Errorf("resolutions = %d, want 6", r.Resolutions)
	}
	c1 := classRow(t, r, "C1")
	if c1.Accepted != 3 || c1.Rejected != 1 || c1.Streak != 1 {
		t.Errorf("C1 = %+v, want 3 accepted / 1 rejected / streak 1 (reset by reject)", c1)
	}
	c3 := classRow(t, r, "C3")
	if c3.Accepted != 1 || c3.Streak != 1 {
		t.Errorf("C3 = %+v, want 1 accepted from the in-scope second src diff", c3)
	}
	un := classRow(t, r, "unclassified")
	if un.Accepted != 1 || un.Streak != 1 || un.NextThreshold != 0 {
		t.Errorf("unclassified = %+v, want 1 accepted (first-time src), no rung threshold", un)
	}
	if r.AutoApply != "off" || r.CurrentRung != 0 {
		t.Errorf("pref/rung = %q/%d, want off/0 (rung-0 instrumentation)", r.AutoApply, r.CurrentRung)
	}
}

// TestAutonomyRevertBreak: a 90%-mirrored removal of an accepted docs
// diff inside the window breaks the C1 streak; the reverting accept
// itself restarts it at 1.
func TestAutonomyRevertBreak(t *testing.T) {
	f := newAutonomyFixture(t)
	t.Setenv("HOME", t.TempDir())

	d1 := f.addDiff(t, "a.diff", patchDoc("guide.md", 10))
	f.resolve(t, d1, "accept", "2026-08-01 09:00:00")
	d2 := f.addDiff(t, "b.diff", patchSrc("src/x.go", 20, 20, false))
	f.resolve(t, d2, "accept", "2026-08-01 09:00:01")
	// Removes 9 of the 10 added lines on the same file: revert.
	d3 := f.addDiff(t, "c.diff", patchDel("guide.md", 9))
	f.resolve(t, d3, "accept", "2026-08-01 09:00:02")

	r, err := ComputeAutonomy(context.Background(), f.st, f.p, nil)
	if err != nil {
		t.Fatal(err)
	}
	c1 := classRow(t, r, "C1")
	if c1.Accepted != 2 || c1.Streak != 1 {
		t.Errorf("C1 = %+v, want 2 accepted / streak 1 (revert broke, reverting accept restarts)", c1)
	}
}

// TestAutonomyRevertOutsideWindow: the same mirrored removal landing
// after the 7-day window is a new change, not a revert — no break.
func TestAutonomyRevertOutsideWindow(t *testing.T) {
	f := newAutonomyFixture(t)
	t.Setenv("HOME", t.TempDir())

	d1 := f.addDiff(t, "a.diff", patchDoc("guide.md", 10))
	f.resolve(t, d1, "accept", "2026-08-01 09:00:00")
	d2 := f.addDiff(t, "c.diff", patchDel("guide.md", 9))
	f.resolve(t, d2, "accept", "2026-08-20 09:00:00") // day 19

	r, err := ComputeAutonomy(context.Background(), f.st, f.p, nil)
	if err != nil {
		t.Fatal(err)
	}
	c1 := classRow(t, r, "C1")
	if c1.Accepted != 2 || c1.Streak != 2 {
		t.Errorf("C1 = %+v, want 2 accepted / streak 2 (outside the 7d window)", c1)
	}
}

// TestAutonomyStatusHandler: the daemon IPC returns the same snapshot the
// CLI prints (shared ComputeAutonomy) against the live store.
func TestAutonomyStatusHandler(t *testing.T) {
	f := newAutonomyFixture(t)
	t.Setenv("HOME", t.TempDir())

	d1 := f.addDiff(t, "d1.diff", patchDoc("README.md", 3))
	f.resolve(t, d1, "accept", "2026-08-01 10:00:01")

	s := &Server{store: f.st, projectRoot: f.dir}
	resp, err := s.handleAutonomyStatus(context.Background(), Request{})
	if err != nil {
		t.Fatal(err)
	}
	if resp.Autonomy == nil {
		t.Fatal("autonomy missing from response")
	}
	if resp.Autonomy.Resolutions != 1 {
		t.Errorf("resolutions = %d, want 1", resp.Autonomy.Resolutions)
	}
	if c1 := classRow(t, *resp.Autonomy, "C1"); c1.Streak != 1 {
		t.Errorf("C1 = %+v, want streak 1", c1)
	}
}
