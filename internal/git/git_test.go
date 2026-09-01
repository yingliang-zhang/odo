package git

// Detach-only regression tests (B-class workstream↔git design): EVERY run
// worktree is a detached checkout at the repo HEAD, so N runs of one
// workstream never collide on a branch ref (the M11c "already used by
// worktree" failure class is gone by construction), and nothing but accepts
// moves refs. ApplyDiff/CommitPaths pin the path-scoped accept contract
// (P0: an accept must never sweep unrelated main-checkout changes into its
// commit).

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// runCommand returns trimmed git stdout, failing the test on error.
func runCommand(t *testing.T, dir string, args ...string) string {
	t.Helper()
	out, err := run(dir, args...)
	if err != nil {
		t.Fatalf("git %v: %v", args, err)
	}
	return strings.TrimSpace(out)
}

func TestCreateWorktreeDetached(t *testing.T) {
	repo := t.TempDir()
	mustRun(t, repo, "init", "-b", "main")
	mustRun(t, repo, "config", "user.email", "odo@test")
	mustRun(t, repo, "config", "user.name", "odo")
	writeAndCommit(t, repo, "base.txt", "base")
	c1 := mustRun(t, repo, "rev-parse", "HEAD")

	// N worktrees from one workstream's runs: no branch, no collision —
	// the M11c "already used by worktree" class cannot occur.
	w1 := filepath.Join(t.TempDir(), "w1")
	if err := CreateWorktree(repo, w1); err != nil {
		t.Fatalf("first create: %v", err)
	}
	w2 := filepath.Join(t.TempDir(), "w2")
	if err := CreateWorktree(repo, w2); err != nil {
		t.Fatalf("second create: %v", err)
	}
	for _, w := range []string{w1, w2} {
		if got := mustRun(t, w, "rev-parse", "HEAD"); got != c1 {
			t.Errorf("%s HEAD = %s, want base %s", w, got, c1)
		}
		if _, err := run(w, "symbolic-ref", "HEAD"); err == nil {
			t.Errorf("%s is on a branch, want detached HEAD", w)
		}
	}
	// Run worktrees create NO refs at all: refs/heads must still be just main.
	if got := runCommand(t, repo, "for-each-ref", "--format=%(refname)", "refs/heads"); got != "refs/heads/main" {
		t.Errorf("refs/heads = %q, want only refs/heads/main", got)
	}
	// Cleanup.
	for _, w := range []string{w1, w2} {
		if err := RemoveWorktree(repo, w); err != nil {
			t.Errorf("remove %s: %v", w, err)
		}
	}
}

func writeAndCommit(t *testing.T, dir, name, content string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	mustRun(t, dir, "add", name)
	mustRun(t, dir, "commit", "-m", "add "+name)
}

// mustRun runs git in dir and fails the test on error.
func mustRun(t *testing.T, dir string, args ...string) string {
	t.Helper()
	out, err := run(dir, args...)
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
	return strings.TrimSpace(out)
}

// newPatchRepo builds a repo with committed base.txt + tracked.txt and
// returns its root. It mirrors what initRepo does for the ipc suite.
func newPatchRepo(t *testing.T) string {
	t.Helper()
	repo := t.TempDir()
	mustRun(t, repo, "init", "-b", "main")
	mustRun(t, repo, "config", "user.email", "odo@test")
	mustRun(t, repo, "config", "user.name", "odo")
	writeAndCommit(t, repo, "base.txt", "base\n")
	writeAndCommit(t, repo, "tracked.txt", "user file\n")
	return repo
}

// generatePatch diffs base.txt's current working-tree content against the
// index, mimicking ExtractDiff: the modification is staged first so the
// post-image blob lands in the object store (--3way can always build it),
// then everything is restored.
func generatePatch(t *testing.T, repo, content string) string {
	t.Helper()
	if err := os.WriteFile(filepath.Join(repo, "base.txt"), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	mustRun(t, repo, "add", "base.txt")
	// Untrimmed: stripping the final newline would corrupt the last hunk line
	// (git apply demands the terminator or a "\ No newline" marker).
	patch, err := run(repo, "diff", "--cached", "HEAD")
	if err != nil {
		t.Fatal(err)
	}
	mustRun(t, repo, "reset", "-q")
	mustRun(t, repo, "checkout", "--", "base.txt")
	path := filepath.Join(t.TempDir(), "change.diff")
	if err := os.WriteFile(path, []byte(patch), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

// TestApplyDiffPathScopedCommit pins P0: accepting a diff stages and commits
// ONLY the patch's paths. Unrelated user state in the main checkout — dirty
// tracked files, staged-but-uncommitted edits, untracked scratch — survives
// exactly as it was, and never rides into the accept commit.
func TestApplyDiffPathScopedCommit(t *testing.T) {
	repo := newPatchRepo(t)
	patch := generatePatch(t, repo, "patched\n")

	// User state the accept must not touch: an unstaged edit, a staged edit,
	// and an untracked scratch file.
	if err := os.WriteFile(filepath.Join(repo, "tracked.txt"), []byte("user edit\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	writeAndCommit(t, repo, "staged.txt", "v1\n")
	if err := os.WriteFile(filepath.Join(repo, "staged.txt"), []byte("v2\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	mustRun(t, repo, "add", "staged.txt")
	if err := os.WriteFile(filepath.Join(repo, "scratch.txt"), []byte("scratch\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	if err := ApplyDiff(repo, patch); err != nil {
		t.Fatalf("ApplyDiff: %v", err)
	}
	if got := mustRun(t, repo, "cat-file", "-p", ":base.txt"); got != "patched" {
		t.Errorf("index base.txt = %q, want patched (apply + path-scoped stage)", got)
	}
	status := mustRun(t, repo, "status", "--porcelain")
	for _, want := range []string{"M  base.txt", "M  staged.txt", " M tracked.txt", "?? scratch.txt"} {
		if !strings.Contains(status, want) {
			t.Errorf("status missing %q after apply:\n%s", want, status)
		}
	}

	if err := CommitPaths(repo, "odo: accept diff #1", []string{"base.txt"}); err != nil {
		t.Fatalf("CommitPaths: %v", err)
	}
	if got := mustRun(t, repo, "show", "--format=", "--name-only", "HEAD"); got != "base.txt" {
		t.Errorf("accept commit files = %q, want exactly base.txt", got)
	}
	// The user's staged and dirty files were not swept into the commit.
	status = mustRun(t, repo, "status", "--porcelain")
	for _, want := range []string{"M  staged.txt", " M tracked.txt", "?? scratch.txt"} {
		if !strings.Contains(status, want) {
			t.Errorf("status missing %q after commit:\n%s", want, status)
		}
	}
	if strings.Contains(status, "base.txt") {
		t.Errorf("base.txt left uncommitted after CommitPaths:\n%s", status)
	}
}

// TestApplyDiffRefusesUnmerged pins the P1 retry guardrail: ApplyDiff
// refuses to run against an index with unresolved merge conflicts, and the
// refusal names the problem so the user can resolve and retry.
func TestApplyDiffRefusesUnmerged(t *testing.T) {
	repo := t.TempDir()
	mustRun(t, repo, "init", "-b", "main")
	mustRun(t, repo, "config", "user.email", "odo@test")
	mustRun(t, repo, "config", "user.name", "odo")
	writeAndCommit(t, repo, "foo.txt", "base\n")

	mustRun(t, repo, "checkout", "-b", "side")
	writeAndCommit(t, repo, "foo.txt", "side\n")
	mustRun(t, repo, "checkout", "main")
	writeAndCommit(t, repo, "foo.txt", "main\n")
	if _, err := run(repo, "merge", "side"); err == nil {
		t.Fatal("merge unexpectedly clean; test needs a conflict")
	}
	if conflicts, err := HasUnmergedEntries(repo); err != nil || !conflicts {
		t.Fatalf("HasUnmergedEntries = %v, %v; want true after conflicted merge", conflicts, err)
	}

	patch := filepath.Join(t.TempDir(), "noop.diff")
	if err := os.WriteFile(patch, []byte("diff --git a/x b/x\nnew file mode 100644\n--- /dev/null\n+++ b/x\n@@ -0,0 +1 @@\n+x\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := ApplyDiff(repo, patch); err == nil || !strings.Contains(err.Error(), "unmerged") {
		t.Fatalf("ApplyDiff on conflicted index = %v, want an unmerged-entries refusal", err)
	}

	mustRun(t, repo, "merge", "--abort")
	if conflicts, err := HasUnmergedEntries(repo); err != nil || conflicts {
		t.Fatalf("HasUnmergedEntries = %v, %v; want false after merge --abort", conflicts, err)
	}
}

// snapshotPatch captures the current worktree-vs-HEAD state exactly like
// the drain does (ExtractDiff: `git add -A`, `git diff --cached HEAD`),
// then restores the tree to HEAD so the repo can go on playing the
// main-checkout role. The staged post-image blobs stay in the object
// store, so --3way can always build them at apply time.
func snapshotPatch(t *testing.T, repo string) string {
	t.Helper()
	mustRun(t, repo, "add", "-A")
	patch, err := run(repo, "diff", "--cached", "HEAD")
	if err != nil {
		t.Fatal(err)
	}
	if strings.TrimSpace(patch) == "" {
		t.Fatal("snapshot is empty; the scenario mutation is missing")
	}
	mustRun(t, repo, "reset", "-q", "--hard", "HEAD")
	path := filepath.Join(t.TempDir(), "scenario.diff")
	if err := os.WriteFile(path, []byte(patch), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

// TestApplyDiffStagesByState pins pitfall #57 (diff #138, 2026-09-01): the
// accept's post-apply staging derives its path list from ACTUAL tree/index
// state, never from the patch's remembered paths — `apply --3way` records
// a rename in the index as it applies, the pre-image path vanishes from
// both tree and index, and the old remembered-path `git add -- <pre-image>`
// died on "fatal: pathspec ... did not match any files", wedging every
// accept of a rename diff. Every row also re-pins P0: an unrelated dirty
// user file is never swept into staging.
func TestApplyDiffStagesByState(t *testing.T) {
	write := func(t *testing.T, dir, name, body string) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	cases := []struct {
		name     string
		base     map[string]string               // committed pre-image (plus the base commit itself)
		mutate   func(t *testing.T, dir string)  // the agent's change
		drift    func(t *testing.T, repo string) // main-checkout HEAD drift after the base was cut
		inIndex  []string                        // must be in ls-files after ApplyDiff
		notIndex []string                        // must be ABSENT from ls-files after ApplyDiff
		check    func(t *testing.T, repo string) // pinned staged state
	}{
		{
			// The diff #138 shape verbatim: rename + edit near the top,
			// HEAD drifted disjointly at the tail — --3way merges, records
			// the rename in the index, and the pre-image becomes a ghost
			// pathspec. The merged post-image must carry BOTH edits.
			name: "rename pre-image vanishes mid-apply (pitfall #57)",
			base: map[string]string{"panel.txt": strings.Repeat("panel line\n", 20)},
			mutate: func(t *testing.T, dir string) {
				if err := os.Rename(filepath.Join(dir, "panel.txt"), filepath.Join(dir, "receipts.txt")); err != nil {
					t.Fatal(err)
				}
				body, err := os.ReadFile(filepath.Join(dir, "receipts.txt"))
				if err != nil {
					t.Fatal(err)
				}
				write(t, dir, "receipts.txt", strings.Replace(string(body), "panel line\n", "receipts line\n", 1))
			},
			drift: func(t *testing.T, repo string) {
				body, err := os.ReadFile(filepath.Join(repo, "panel.txt"))
				if err != nil {
					t.Fatal(err)
				}
				write(t, repo, "panel.txt", string(body)+"drift tail\n")
				mustRun(t, repo, "add", "panel.txt")
				mustRun(t, repo, "commit", "-q", "-m", "drift")
			},
			inIndex:  []string{"receipts.txt", "user.txt"},
			notIndex: []string{"panel.txt"},
			check: func(t *testing.T, repo string) {
				staged := mustRun(t, repo, "cat-file", "-p", ":receipts.txt")
				for _, want := range []string{"receipts line", "drift tail"} {
					if !strings.Contains(staged, want) {
						t.Errorf("staged receipts.txt missing %q (3-way merge loss):\n%s", want, staged)
					}
				}
				status := mustRun(t, repo, "status", "--porcelain")
				if !strings.Contains(status, "R  panel.txt -> receipts.txt") {
					t.Errorf("status missing staged rename:\n%s", status)
				}
			},
		},
		{
			// A clean delete applies to the working tree only, so the
			// index still lists the path: exercises the index-resident
			// branch of the state filter (stage the deletion via add -A).
			name: "delete stages the index-resident pre-image",
			base: map[string]string{"old.txt": "old\n"},
			mutate: func(t *testing.T, dir string) {
				if err := os.Remove(filepath.Join(dir, "old.txt")); err != nil {
					t.Fatal(err)
				}
			},
			inIndex:  []string{"user.txt"},
			notIndex: []string{"old.txt"},
			check: func(t *testing.T, repo string) {
				status := mustRun(t, repo, "status", "--porcelain")
				if !strings.Contains(status, "D  old.txt") {
					t.Errorf("status missing staged deletion:\n%s", status)
				}
			},
		},
		{
			// Baseline preserved: a modify + an untracked new file both
			// stage (the new file via the working-tree-resident branch).
			name: "modify plus new file",
			base: map[string]string{"base.txt": "base\n"},
			mutate: func(t *testing.T, dir string) {
				write(t, dir, "base.txt", "patched\n")
				write(t, dir, "new.txt", "new\n")
			},
			inIndex:  []string{"base.txt", "new.txt", "user.txt"},
			notIndex: nil,
			check: func(t *testing.T, repo string) {
				status := mustRun(t, repo, "status", "--porcelain")
				for _, want := range []string{"M  base.txt", "A  new.txt"} {
					if !strings.Contains(status, want) {
						t.Errorf("status missing %q:\n%s", want, status)
					}
				}
			},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			repo := t.TempDir()
			mustRun(t, repo, "init", "-b", "main")
			mustRun(t, repo, "config", "user.email", "odo@test")
			mustRun(t, repo, "config", "user.name", "odo")
			for name, body := range tc.base {
				writeAndCommit(t, repo, name, body)
			}
			writeAndCommit(t, repo, "user.txt", "user file\n")
			tc.mutate(t, repo)
			patch := snapshotPatch(t, repo)
			if tc.drift != nil {
				tc.drift(t, repo)
			}
			// P0 obstacle: an unrelated unstaged user edit must survive
			// exactly as it was, never swept by the accept's staging.
			write(t, repo, "user.txt", "user edit\n")

			if err := ApplyDiff(repo, patch); err != nil {
				t.Fatalf("ApplyDiff: %v", err)
			}
			index := mustRun(t, repo, "ls-files")
			for _, p := range tc.inIndex {
				if !strings.Contains(index, p) {
					t.Errorf("ls-files missing %q:\n%s", p, index)
				}
			}
			for _, p := range tc.notIndex {
				if strings.Contains(index, p) {
					t.Errorf("ls-files unexpectedly carries %q:\n%s", p, index)
				}
			}
			if tc.check != nil {
				tc.check(t, repo)
			}
			if status := mustRun(t, repo, "status", "--porcelain"); !strings.Contains(status, " M user.txt") {
				t.Errorf("unrelated edit swept or lost, want \" M user.txt\":\n%s", status)
			}
		})
	}
}

// TestExtractDiffRename pins the drain side of pitfall #57: worktree
// extraction has always been rename-aware (`git add -A` + `diff --cached`),
// so a bare rename inside the run's worktree lands in the archived patch
// as a rename hunk — the accept path then relies on its rename from/to
// headers. (The remembered-path failure only ever existed at accept time.)
func TestExtractDiffRename(t *testing.T) {
	repo := t.TempDir()
	mustRun(t, repo, "init", "-b", "main")
	mustRun(t, repo, "config", "user.email", "odo@test")
	mustRun(t, repo, "config", "user.name", "odo")
	writeAndCommit(t, repo, "panel.txt", strings.Repeat("panel line\n", 20))

	wt := filepath.Join(t.TempDir(), "wt")
	if err := CreateWorktree(repo, wt); err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := RemoveWorktree(repo, wt); err != nil {
			t.Errorf("remove worktree: %v", err)
		}
	}()
	// A bare mv — the agent's usual rename shape; nothing else staged.
	if err := os.Rename(filepath.Join(wt, "panel.txt"), filepath.Join(wt, "receipts.txt")); err != nil {
		t.Fatal(err)
	}
	diff, err := ExtractDiff(wt)
	if err != nil {
		t.Fatalf("ExtractDiff: %v", err)
	}
	for _, want := range []string{"rename from panel.txt", "rename to receipts.txt"} {
		if !strings.Contains(diff, want) {
			t.Errorf("extracted diff missing %q:\n%s", want, diff)
		}
	}
}

// TestDiffPaths pins the patch-path parser: which side carries a path for
// adds/deletes/modifies, header fallback for mode-only and pure renames,
// and C-quote resolution into real filesystem names.
func TestDiffPaths(t *testing.T) {
	cases := []struct {
		name  string
		patch string
		wantA []string
		wantB []string
	}{
		{
			name:  "modify carries both sides",
			patch: "diff --git a/f.txt b/f.txt\nindex 1111111..2222222 100644\n--- a/f.txt\n+++ b/f.txt\n@@ -1 +1 @@\n-a\n+b\n",
			wantA: []string{"f.txt"}, wantB: []string{"f.txt"},
		},
		{
			name:  "new file is b-side only",
			patch: "diff --git a/new.txt b/new.txt\nnew file mode 100644\n--- /dev/null\n+++ b/new.txt\n@@ -0,0 +1 @@\n+x\n",
			wantA: nil, wantB: []string{"new.txt"},
		},
		{
			name:  "delete is a-side only",
			patch: "diff --git a/old.txt b/old.txt\ndeleted file mode 100644\n--- a/old.txt\n+++ /dev/null\n@@ -1 +0,0 @@\n-x\n",
			wantA: []string{"old.txt"}, wantB: nil,
		},
		{
			name:  "pure rename falls back to the header",
			patch: "diff --git a/before.txt b/after.txt\nsimilarity index 100%\nrename from before.txt\nrename to after.txt\n",
			wantA: []string{"before.txt"}, wantB: []string{"after.txt"},
		},
		{
			name:  "mode-only falls back to the header",
			patch: "diff --git a/run.sh b/run.sh\nold mode 100644\nnew mode 100755\n",
			wantA: []string{"run.sh"}, wantB: []string{"run.sh"},
		},
		{
			name:  "quoted paths unquote to real names",
			patch: "diff --git \"a/src/f\\303\\251.go\" \"b/src/f\\303\\251.go\"\nindex 1111111..2222222 100644\n--- \"a/src/f\\303\\251.go\"\n+++ \"b/src/f\\303\\251.go\"\n@@ -1 +1 @@\n-a\n+b\n",
			wantA: []string{"src/fé.go"}, wantB: []string{"src/fé.go"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "c.diff")
			if err := os.WriteFile(path, []byte(tc.patch), 0o644); err != nil {
				t.Fatal(err)
			}
			a, b, err := DiffPaths(path)
			if err != nil {
				t.Fatal(err)
			}
			if strings.Join(a, ",") != strings.Join(tc.wantA, ",") {
				t.Errorf("a-side = %v, want %v", a, tc.wantA)
			}
			if strings.Join(b, ",") != strings.Join(tc.wantB, ",") {
				t.Errorf("b-side = %v, want %v", b, tc.wantB)
			}
		})
	}
}

// TestTestAssertionDelta pins the weakened-tests gate's counting contract:
// assertion tokens are counted per side ONLY inside *_test.go blocks
// (global across files, so a move nets zero), comment lines never count
// on either side (commenting out an assertion nets a removal), and
// /dev/null sides arm independently. The auto-land gate blocks when
// removed > added.
func TestTestAssertionDelta(t *testing.T) {
	cases := []struct {
		name       string
		patch      string
		wantAdd    int
		wantRemove int
	}{
		{
			name: "modify test file counts both sides",
			patch: "diff --git a/x_test.go b/x_test.go\n" +
				"--- a/x_test.go\n+++ b/x_test.go\n@@ -1,2 +1,3 @@\n" +
				" func TestX(t *testing.T) {\n" +
				"-\tassert.Equal(t, 1, got)\n" +
				"+\tassert.Equal(t, 2, got)\n" +
				"+\trequire.NoError(t, err)\n",
			wantAdd:    2, // assert.Equal + require.NoError
			wantRemove: 1,
		},
		{
			name: "assertion tokens in non-test files are ignored",
			patch: "diff --git a/main.go b/main.go\n" +
				"--- a/main.go\n+++ b/main.go\n@@ -1 +1 @@\n" +
				"-\tassert.Equal(t, 0, x)\n" +
				"+\tassert.Equal(t, 1, x)\n",
		},
		{
			name: "per-block reset: tokens after a test block in a main.go block are ignored",
			patch: "diff --git a/x_test.go b/x_test.go\n" +
				"--- a/x_test.go\n+++ b/x_test.go\n@@ -1 +1,2 @@\n" +
				" func TestX(t *testing.T) {}\n" +
				"+\tt.Error(\"one\")\n" +
				"diff --git a/main.go b/main.go\n" +
				"--- a/main.go\n+++ b/main.go\n@@ -1 +1,2 @@\n" +
				" package main\n" +
				"+\tt.Error(\"two\")\n+\trequire.True(t, true)\n",
			wantAdd: 1, // only the _test.go line
		},
		{
			name: "new test file counts added only",
			patch: "diff --git a/new_test.go b/new_test.go\n" +
				"new file mode 100644\n--- /dev/null\n+++ b/new_test.go\n@@ -0,0 +1,2 @@\n" +
				"+\trequire.True(t, ok)\n" +
				"+\tt.Fatal(\"boom\")\n",
			wantAdd: 2, // require.True + t.Fatal
		},
		{
			name: "deleted test file counts removed only",
			patch: "diff --git a/old_test.go b/old_test.go\n" +
				"deleted file mode 100644\n--- a/old_test.go\n+++ /dev/null\n@@ -1,2 +0,0 @@\n" +
				"-\trequire.True(t, ok)\n" +
				"-\tt.Errorf(\"boom\")\n",
			wantRemove: 2, // t.Error matches t.Errorf
		},
		{
			name: "multiple tokens on one line add up",
			patch: "diff --git a/x_test.go b/x_test.go\n" +
				"--- a/x_test.go\n+++ b/x_test.go\n@@ -1 +1 @@\n" +
				"-\tt.Fail()\n" +
				"+\tassert.NoError(t, require.True(t, ok))\n",
			wantAdd:    2, // assert. + require. on the same line
			wantRemove: 1,
		},
		{
			name: "rename test file nets zero",
			patch: "diff --git a/x_test.go b/y_test.go\n" +
				"--- a/x_test.go\n+++ b/y_test.go\n@@ -1 +1 @@\n" +
				"-\tassert.True(t, ok)\n" +
				"+\tassert.True(t, ok)\n",
			wantAdd:    1,
			wantRemove: 1,
		},
		{
			// M16 panel evasion: `+// assert.X` used to count as added and
			// netted zero against the removed live assertion.
			name: "commenting out an assertion nets a removal",
			patch: "diff --git a/x_test.go b/x_test.go\n" +
				"--- a/x_test.go\n+++ b/x_test.go\n@@ -1 +1 @@\n" +
				"-\tassert.Equal(t, 3, got)\n" +
				"+\t// assert.Equal(t, 3, got)\n",
			wantAdd:    0,
			wantRemove: 1,
		},
		{
			name: "added comment mentioning tokens does not count",
			patch: "diff --git a/x_test.go b/x_test.go\n" +
				"--- a/x_test.go\n+++ b/x_test.go\n@@ -1 +1,2 @@\n" +
				" func TestX(t *testing.T) {}\n" +
				"+\t// TODO: add require.NoError and t.Fatal coverage\n",
			wantAdd: 0,
		},
		{
			name: "removed comment mentioning tokens was never proof",
			patch: "diff --git a/x_test.go b/x_test.go\n" +
				"--- a/x_test.go\n+++ b/x_test.go\n@@ -1,2 +1 @@\n" +
				"-\t// stale: assert.True used to live here\n" +
				" func TestX(t *testing.T) {}\n",
			wantRemove: 0,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "d.diff")
			if err := os.WriteFile(path, []byte(tc.patch), 0o644); err != nil {
				t.Fatal(err)
			}
			added, removed, err := TestAssertionDelta(path)
			if err != nil {
				t.Fatal(err)
			}
			if added != tc.wantAdd || removed != tc.wantRemove {
				t.Errorf("delta = +%d/-%d, want +%d/-%d", added, removed, tc.wantAdd, tc.wantRemove)
			}
		})
	}
}

// ------------------------------------------- P0a: ProbeApplyClean (stale-diff refresh probe)

// probeLeaks counts leftover probe worktrees: registered .git/worktrees
// entries beyond the main checkout and odo-probe-* dirs in the OS temp dir.
func probeLeaks(t *testing.T, repo string) (worktrees int, dirs int) {
	t.Helper()
	worktrees = strings.Count(mustRun(t, repo, "worktree", "list", "--porcelain"), "worktree ")
	matches, err := filepath.Glob(filepath.Join(os.TempDir(), "odo-probe-*"))
	if err != nil {
		t.Fatal(err)
	}
	return worktrees, len(matches)
}

// TestProbeApplyClean_CleanOnDrift (P0a): HEAD moved past the diff's base
// but on a DISJOINT path — the diff's embedded base blobs let --3way merge
// it onto the new HEAD, so the probe reports clean. The probe runs entirely
// inside a throwaway worktree: main's HEAD, files, and index are untouched.
func TestProbeApplyClean_CleanOnDrift(t *testing.T) {
	repo := newPatchRepo(t)
	patch := generatePatch(t, repo, "patched\n")
	headBefore := mustRun(t, repo, "rev-parse", "HEAD")
	writeAndCommit(t, repo, "drift.txt", "drift\n") // disjoint drift
	headDrifted := mustRun(t, repo, "rev-parse", "HEAD")
	if headDrifted == headBefore {
		t.Fatal("setup bug: drift commit did not move HEAD")
	}

	clean, detail, err := ProbeApplyClean(repo, patch)
	if err != nil || !clean || detail != "" {
		t.Fatalf("ProbeApplyClean = (%v, %q, %v), want (true, \"\", nil)", clean, detail, err)
	}
	// Main untouched: same HEAD, original file content, pristine status.
	if got := mustRun(t, repo, "rev-parse", "HEAD"); got != headDrifted {
		t.Errorf("main HEAD = %s after the probe, want %s (probe must not move main)", got, headDrifted)
	}
	if got := readFile(repo, "base.txt"); got != "base\n" {
		t.Errorf("main base.txt = %q, want the pre-probe content (probe leaked into main)", got)
	}
	if status := mustRun(t, repo, "status", "--porcelain"); status != "" {
		t.Errorf("main status = %q after the probe, want clean", status)
	}
}

// TestProbeApplyClean_ConflictOnOverlap (P0a): HEAD moved by editing the
// SAME file the patch rewrites — the 3-way merge conflicts, the probe
// reports it (non-empty detail carrying git's diagnostics, no error), and
// main is untouched.
func TestProbeApplyClean_ConflictOnOverlap(t *testing.T) {
	repo := newPatchRepo(t)
	patch := generatePatch(t, repo, "patched\n")
	writeAndCommit(t, repo, "base.txt", "user drift\n") // overlapping drift
	headDrifted := mustRun(t, repo, "rev-parse", "HEAD")

	clean, detail, err := ProbeApplyClean(repo, patch)
	if err != nil || clean {
		t.Fatalf("ProbeApplyClean = (%v, %q, %v), want (false, detail, nil)", clean, detail, err)
	}
	if !strings.Contains(detail, "base.txt") {
		t.Errorf("detail = %q, want git's conflict diagnostics naming base.txt", detail)
	}
	if got := mustRun(t, repo, "rev-parse", "HEAD"); got != headDrifted {
		t.Errorf("main HEAD = %s after the probe, want %s", got, headDrifted)
	}
	if got := readFile(repo, "base.txt"); got != "user drift\n" {
		t.Errorf("main base.txt = %q, want the drifted content (probe leaked into main)", got)
	}
	if status := mustRun(t, repo, "status", "--porcelain"); status != "" {
		t.Errorf("main status = %q after the probe, want clean", status)
	}
}

// ------------------------------------------- M20: ProbeAlreadyLanded + PathsDifferFromHEAD

// twoFilePatch returns a patch that rewrites base.txt AND adds new.txt,
// cut from the repo's current HEAD (the tree is restored afterwards).
func twoFilePatch(t *testing.T, repo string) string {
	t.Helper()
	if err := os.WriteFile(filepath.Join(repo, "base.txt"), []byte("patched\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repo, "new.txt"), []byte("new\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	mustRun(t, repo, "add", "-A")
	// mustRun trims whitespace — restore the hunk-terminating newline,
	// without which git apply reads a corrupt patch.
	patch := mustRun(t, repo, "diff", "--cached", "HEAD") + "\n"
	// reset --hard restores index+tree to HEAD, index-only new.txt included.
	mustRun(t, repo, "reset", "-q", "--hard", "HEAD")
	path := filepath.Join(t.TempDir(), "two.diff")
	if err := os.WriteFile(path, []byte(patch), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

// TestProbeAlreadyLanded (M20): the reverse-apply roundtrip — full
// post-image present (committed OR uncommitted) → landed; base state,
// partial landings, and drift content all report not-landed, and the
// check never writes to the tree.
func TestProbeAlreadyLanded(t *testing.T) {
	t.Run("committed post-image is landed", func(t *testing.T) {
		repo := newPatchRepo(t)
		patch := generatePatch(t, repo, "patched\n")
		writeAndCommit(t, repo, "base.txt", "patched\n") // identical content, side path
		landed, detail, err := ProbeAlreadyLanded(repo, patch)
		if err != nil || !landed || detail != "" {
			t.Fatalf("ProbeAlreadyLanded = (%v, %q, %v), want (true, \"\", nil)", landed, detail, err)
		}
		if status := mustRun(t, repo, "status", "--porcelain"); status != "" {
			t.Errorf("main status = %q, want clean (the probe is read-only)", status)
		}
	})

	t.Run("uncommitted post-image is landed", func(t *testing.T) {
		repo := newPatchRepo(t)
		patch := generatePatch(t, repo, "patched\n")
		if err := os.WriteFile(filepath.Join(repo, "base.txt"), []byte("patched\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		landed, _, err := ProbeAlreadyLanded(repo, patch)
		if err != nil || !landed {
			t.Fatalf("ProbeAlreadyLanded = (%v, %v), want (true, nil) for an identical uncommitted edit", landed, err)
		}
	})

	t.Run("base state is not landed", func(t *testing.T) {
		repo := newPatchRepo(t)
		patch := generatePatch(t, repo, "patched\n")
		landed, detail, err := ProbeAlreadyLanded(repo, patch)
		if err != nil || landed || detail == "" {
			t.Fatalf("ProbeAlreadyLanded = (%v, %q, %v), want (false, detail, nil)", landed, detail, err)
		}
	})

	t.Run("partial landing is not landed", func(t *testing.T) {
		repo := newPatchRepo(t)
		patch := twoFilePatch(t, repo)
		writeAndCommit(t, repo, "base.txt", "patched\n") // ONLY the first half lands
		landed, _, err := ProbeAlreadyLanded(repo, patch)
		if err != nil || landed {
			t.Fatalf("ProbeAlreadyLanded = (%v, %v), want (false, nil) — partial must never reconcile", landed, err)
		}
	})

	t.Run("drifted content is not landed", func(t *testing.T) {
		repo := newPatchRepo(t)
		patch := generatePatch(t, repo, "patched\n")
		writeAndCommit(t, repo, "base.txt", "other drift\n")
		landed, _, err := ProbeAlreadyLanded(repo, patch)
		if err != nil || landed {
			t.Fatalf("ProbeAlreadyLanded = (%v, %v), want (false, nil)", landed, err)
		}
	})

	t.Run("multi-file full landing is landed", func(t *testing.T) {
		repo := newPatchRepo(t)
		patch := twoFilePatch(t, repo)
		writeAndCommit(t, repo, "base.txt", "patched\n")
		writeAndCommit(t, repo, "new.txt", "new\n")
		landed, _, err := ProbeAlreadyLanded(repo, patch)
		if err != nil || !landed {
			t.Fatalf("ProbeAlreadyLanded = (%v, %v), want (true, nil)", landed, err)
		}
	})

	t.Run("not a repo is an error", func(t *testing.T) {
		repo := newPatchRepo(t)
		patch := generatePatch(t, repo, "patched\n")
		landed, _, err := ProbeAlreadyLanded(filepath.Join(t.TempDir(), "no-repo"), patch)
		if err == nil || landed {
			t.Fatalf("ProbeAlreadyLanded = (%v, %v), want (false, err)", landed, err)
		}
	})
}

// TestExtraEditsBeyondPatch (tri-review P1, 2026-08-24): the already-
// landed accept's byte-level guard. The M20 probe is hunk-granular, so
// the guard reconstructs the post-image in a temp index and compares
// worktree blob shas: exact post-image (uncommitted OR committed) passes;
// extra edits beyond the hunks, replaced content on a deleted path, or
// extra bytes on a patch-created file are named; the repo's real index,
// HEAD, and working tree stay untouched throughout.
func TestExtraEditsBeyondPatch(t *testing.T) {
	write := func(t *testing.T, repo, name, content string) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(repo, name), []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	t.Run("exact uncommitted post-image passes", func(t *testing.T) {
		repo := newPatchRepo(t)
		patch := generatePatch(t, repo, "patched\n")
		write(t, repo, "base.txt", "patched\n")
		if extra, err := ExtraEditsBeyondPatch(repo, patch); err != nil || len(extra) != 0 {
			t.Fatalf("ExtraEditsBeyondPatch = (%v, %v), want (nil, nil)", extra, err)
		}
	})

	t.Run("trailing extra edit beyond the hunks is named", func(t *testing.T) {
		repo := newPatchRepo(t)
		writeAndCommit(t, repo, "base.txt", "l1\nl2\nl3\nl4\nl5\n")
		// The patch rewrites ONE line; the landed probe tolerates the
		// rest of the file drifting (that tolerance is the finding).
		patch := generatePatch(t, repo, "l1\nl2\nL3\nl4\nl5\n")
		write(t, repo, "base.txt", "l1\nl2\nL3\nl4\nl5\nuser tail beyond hunk\n")
		indexBefore := mustRun(t, repo, "ls-files", "-s", "--", "base.txt")
		statusBefore := mustRun(t, repo, "status", "--porcelain")
		extra, err := ExtraEditsBeyondPatch(repo, patch)
		if err != nil || len(extra) != 1 || extra[0] != "base.txt" {
			t.Fatalf("ExtraEditsBeyondPatch = (%v, %v), want ([base.txt], nil)", extra, err)
		}
		// Read-only against the repo's own state: real index and porcelain
		// unchanged (the probe ran on a GIT_INDEX_FILE temp index).
		if got := mustRun(t, repo, "ls-files", "-s", "--", "base.txt"); got != indexBefore {
			t.Errorf("real index after probe = %q, want untouched %q", got, indexBefore)
		}
		if got := mustRun(t, repo, "status", "--porcelain"); got != statusBefore {
			t.Errorf("porcelain after probe = %q, want pre-existing dirt only %q", got, statusBefore)
		}
	})

	t.Run("committed post-image passes", func(t *testing.T) {
		repo := newPatchRepo(t)
		patch := generatePatch(t, repo, "patched\n")
		writeAndCommit(t, repo, "base.txt", "patched\n") // side-channel landing, worktree clean
		if extra, err := ExtraEditsBeyondPatch(repo, patch); err != nil || len(extra) != 0 {
			t.Fatalf("ExtraEditsBeyondPatch = (%v, %v), want (nil, nil)", extra, err)
		}
	})

	t.Run("committed post-image plus uncommitted extra is named", func(t *testing.T) {
		repo := newPatchRepo(t)
		patch := generatePatch(t, repo, "patched\n")
		writeAndCommit(t, repo, "base.txt", "patched\n")
		write(t, repo, "base.txt", "patched\nuncommitted extra\n")
		extra, err := ExtraEditsBeyondPatch(repo, patch)
		if err != nil || len(extra) != 1 || extra[0] != "base.txt" {
			t.Fatalf("ExtraEditsBeyondPatch = (%v, %v), want ([base.txt], nil)", extra, err)
		}
	})

	t.Run("patch-created file: exact passes, extra content named", func(t *testing.T) {
		repo := newPatchRepo(t)
		patch := twoFilePatch(t, repo) // rewrites base.txt AND creates new.txt
		write(t, repo, "base.txt", "patched\n")
		write(t, repo, "new.txt", "new\n")
		if extra, err := ExtraEditsBeyondPatch(repo, patch); err != nil || len(extra) != 0 {
			t.Fatalf("exact untracked post-image = (%v, %v), want (nil, nil)", extra, err)
		}
		write(t, repo, "new.txt", "new\nMORE\n") // extra content on the created path
		extra, err := ExtraEditsBeyondPatch(repo, patch)
		if err != nil || len(extra) != 1 || extra[0] != "new.txt" {
			t.Fatalf("ExtraEditsBeyondPatch = (%v, %v), want ([new.txt], nil)", extra, err)
		}
	})

	t.Run("deletion patch: gone passes, recreated named", func(t *testing.T) {
		repo := newPatchRepo(t)
		// A straight deletion patch, generated like ExtractDiff (add -A
		// in the worktree, diff --cached, restore).
		if err := os.Remove(filepath.Join(repo, "tracked.txt")); err != nil {
			t.Fatal(err)
		}
		mustRun(t, repo, "add", "-A")
		patch, err := run(repo, "diff", "--cached", "HEAD")
		if err != nil {
			t.Fatal(err)
		}
		mustRun(t, repo, "reset", "-q", "--hard", "HEAD")
		patchPath := filepath.Join(t.TempDir(), "del.diff")
		if err := os.WriteFile(patchPath, []byte(patch), 0o644); err != nil {
			t.Fatal(err)
		}
		// Post-image = deleted; worktree deletion (uncommitted) passes.
		if err := os.Remove(filepath.Join(repo, "tracked.txt")); err != nil {
			t.Fatal(err)
		}
		if extra, err := ExtraEditsBeyondPatch(repo, patchPath); err != nil || len(extra) != 0 {
			t.Fatalf("deleted post-image = (%v, %v), want (nil, nil)", extra, err)
		}
		// The user re-created the path with their own bytes — extra edit.
		write(t, repo, "tracked.txt", "resurrected by user\n")
		extra, err := ExtraEditsBeyondPatch(repo, patchPath)
		if err != nil || len(extra) != 1 || extra[0] != "tracked.txt" {
			t.Fatalf("ExtraEditsBeyondPatch = (%v, %v), want ([tracked.txt], nil)", extra, err)
		}
	})

	t.Run("not a repo is an error", func(t *testing.T) {
		repo := newPatchRepo(t)
		patch := generatePatch(t, repo, "patched\n")
		if _, err := ExtraEditsBeyondPatch(filepath.Join(t.TempDir(), "no-repo"), patch); err == nil {
			t.Fatal("want an error outside a repo, got nil")
		}
	})
}

// TestIndexEditsBeyondHEAD (tri-review P1, 2026-08-24): the accept gate's
// index-vs-HEAD probe. Every staged divergence on a queried path is named
// — an edited blob, a staged new file, a staged deletion — while a clean
// index and zero-path input stay silent; the probe leaves index, HEAD,
// and worktree exactly as found.
func TestIndexEditsBeyondHEAD(t *testing.T) {
	t.Run("staged edit names the path", func(t *testing.T) {
		repo := newPatchRepo(t)
		if err := os.WriteFile(filepath.Join(repo, "base.txt"), []byte("staged sketch\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		mustRun(t, repo, "add", "base.txt")
		indexBefore := mustRun(t, repo, "ls-files", "-s", "--", "base.txt")
		staged, err := IndexEditsBeyondHEAD(repo, []string{"base.txt", "tracked.txt"})
		if err != nil || len(staged) != 1 || staged[0] != "base.txt" {
			t.Fatalf("IndexEditsBeyondHEAD = (%v, %v), want ([base.txt], nil — tracked.txt stays clean)", staged, err)
		}
		// Read-only: the staged entry survives the probe verbatim.
		if got := mustRun(t, repo, "ls-files", "-s", "--", "base.txt"); got != indexBefore {
			t.Errorf("index entry after probe = %q, want untouched %q", got, indexBefore)
		}
		if got := mustRun(t, repo, "status", "--porcelain"); got != "M  base.txt" {
			t.Errorf("porcelain after probe = %q, want the staged edit only", got)
		}
	})

	t.Run("clean index names nothing", func(t *testing.T) {
		repo := newPatchRepo(t)
		staged, err := IndexEditsBeyondHEAD(repo, []string{"base.txt", "tracked.txt"})
		if err != nil || len(staged) != 0 {
			t.Fatalf("IndexEditsBeyondHEAD = (%v, %v), want (nil, nil)", staged, err)
		}
		// Unstaged worktree dirt is NOT this probe's axis (DirtyPaths
		// owns it): the index still matches HEAD.
		if err := os.WriteFile(filepath.Join(repo, "base.txt"), []byte("unstaged dirt\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		staged, err = IndexEditsBeyondHEAD(repo, []string{"base.txt"})
		if err != nil || len(staged) != 0 {
			t.Fatalf("unstaged-only dirt = (%v, %v), want (nil, nil)", staged, err)
		}
	})

	t.Run("staged new file not in HEAD is named", func(t *testing.T) {
		repo := newPatchRepo(t)
		if err := os.WriteFile(filepath.Join(repo, "brand-new.txt"), []byte("staged new\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		mustRun(t, repo, "add", "brand-new.txt")
		staged, err := IndexEditsBeyondHEAD(repo, []string{"brand-new.txt"})
		if err != nil || len(staged) != 1 || staged[0] != "brand-new.txt" {
			t.Fatalf("IndexEditsBeyondHEAD = (%v, %v), want ([brand-new.txt], nil)", staged, err)
		}
	})

	t.Run("staged deletion is named", func(t *testing.T) {
		repo := newPatchRepo(t)
		// --cached removes only the index entry (worktree survives) —
		// the pure staged-deletion shape.
		mustRun(t, repo, "rm", "-q", "--cached", "--", "tracked.txt")
		staged, err := IndexEditsBeyondHEAD(repo, []string{"tracked.txt"})
		if err != nil || len(staged) != 1 || staged[0] != "tracked.txt" {
			t.Fatalf("IndexEditsBeyondHEAD = (%v, %v), want ([tracked.txt], nil)", staged, err)
		}
	})

	t.Run("zero paths is a no-op", func(t *testing.T) {
		repo := newPatchRepo(t)
		if staged, err := IndexEditsBeyondHEAD(repo, nil); err != nil || staged != nil {
			t.Fatalf("IndexEditsBeyondHEAD(nil) = (%v, %v), want (nil, nil)", staged, err)
		}
	})

	t.Run("not a repo is an error", func(t *testing.T) {
		if _, err := IndexEditsBeyondHEAD(filepath.Join(t.TempDir(), "no-repo"), []string{"x.txt"}); err == nil {
			t.Fatal("want an error outside a repo, got nil")
		}
	})
}

// TestPathsDifferFromHEAD (M20): exit-1 quarantine — differences report
// true (staged OR unstaged), a clean path set reports false, and real
// errors surface as errors, never as a diff verdict.
func TestPathsDifferFromHEAD(t *testing.T) {
	repo := newPatchRepo(t)
	if differs, err := PathsDifferFromHEAD(repo, []string{"base.txt"}); err != nil || differs {
		t.Fatalf("clean tree = (%v, %v), want (false, nil)", differs, err)
	}
	if err := os.WriteFile(filepath.Join(repo, "base.txt"), []byte("dirty\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if differs, err := PathsDifferFromHEAD(repo, []string{"base.txt"}); err != nil || !differs {
		t.Fatalf("unstaged edit = (%v, %v), want (true, nil)", differs, err)
	}
	mustRun(t, repo, "add", "base.txt")
	if differs, err := PathsDifferFromHEAD(repo, []string{"base.txt"}); err != nil || !differs {
		t.Fatalf("staged edit = (%v, %v), want (true, nil)", differs, err)
	}
	mustRun(t, repo, "reset", "-q", "--hard", "HEAD")
	if differs, err := PathsDifferFromHEAD(repo, nil); err != nil || differs {
		t.Fatalf("empty paths = (%v, %v), want (false, nil)", differs, err)
	}
	if _, err := PathsDifferFromHEAD(filepath.Join(t.TempDir(), "no-repo"), []string{"x"}); err == nil {
		t.Fatal("not-a-repo = nil error, want error")
	}
}

// TestProbeApplyClean_CleansUp (P0a): the throwaway worktree is removed
// unconditionally — neither a clean nor a conflicting probe leaves a
// registered worktree entry or an odo-probe-* dir behind.
func TestProbeApplyClean_CleansUp(t *testing.T) {
	repo := newPatchRepo(t)
	conflictPatch := generatePatch(t, repo, "patched again\n")
	writeAndCommit(t, repo, "base.txt", "user drift\n") // makes conflictPatch conflict
	wtBefore, dirsBefore := probeLeaks(t, repo)

	// The clean leg needs a patch that still merges onto HEAD after one
	// more drift commit: a new-file patch cut from the CURRENT (drifted)
	// HEAD, then drift again on a disjoint path.

	disjoint := t.TempDir()
	mustRun(t, repo, "worktree", "add", "--detach", filepath.Join(disjoint, "scratch"), "HEAD")
	scratch := filepath.Join(disjoint, "scratch")
	if err := os.WriteFile(filepath.Join(scratch, "added.txt"), []byte("added\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	mustRun(t, scratch, "add", "-A")
	disjointPatch, err := run(scratch, "diff", "--cached", "HEAD")
	if err != nil {
		t.Fatal(err)
	}
	mustRun(t, repo, "worktree", "remove", "--force", scratch)
	patchPath := filepath.Join(disjoint, "d.diff")
	if err := os.WriteFile(patchPath, []byte(disjointPatch), 0o644); err != nil {
		t.Fatal(err)
	}
	writeAndCommit(t, repo, "second-drift.txt", "drift\n") // disjoint: probe is clean

	if clean, _, err := ProbeApplyClean(repo, patchPath); err != nil || !clean {
		t.Fatalf("disjoint probe = (%v, _, %v), want clean", clean, err)
	}
	conflictPath := filepath.Join(disjoint, "c.diff")
	if data, err := os.ReadFile(conflictPatch); err != nil {
		t.Fatal(err)
	} else if err := os.WriteFile(conflictPath, data, 0o644); err != nil {
		t.Fatal(err)
	}
	if clean, _, err := ProbeApplyClean(repo, conflictPath); err != nil || clean {
		t.Fatalf("overlap probe = (%v, _, %v), want conflict", clean, err)
	}

	wtAfter, dirsAfter := probeLeaks(t, repo)
	if wtAfter != wtBefore {
		t.Errorf("registered worktrees = %d, want %d (a probe leaked a worktree entry)", wtAfter, wtBefore)
	}
	if dirsAfter != dirsBefore {
		t.Errorf("odo-probe-* dirs = %d, want %d (a probe leaked its checkout)", dirsAfter, dirsBefore)
	}
}

// readFile returns the working-tree content of one repo file.
func readFile(repo, name string) string {
	data, err := os.ReadFile(filepath.Join(repo, name))
	if err != nil {
		return ""
	}
	return string(data)
}

// ShowHEADFile judges the COMMIT, never the checkout (the .odo-verify
// advisory's configured check): uncommitted edits are invisible to it,
// and an absent-from-HEAD file is an error.
func TestShowHEADFile(t *testing.T) {
	repo := newPatchRepo(t)
	if got, err := ShowHEADFile(repo, "base.txt"); err != nil || got != "base\n" {
		t.Fatalf("ShowHEADFile(base.txt) = %q, %v", got, err)
	}
	// Uncommitted edits never reach run worktrees; the HEAD read must
	// not see them either.
	if err := os.WriteFile(filepath.Join(repo, "base.txt"), []byte("dirty\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if got, err := ShowHEADFile(repo, "base.txt"); err != nil || got != "base\n" {
		t.Errorf("ShowHEADFile with uncommitted edit = %q, %v, want committed content", got, err)
	}
	if _, err := ShowHEADFile(repo, "no-such-file"); err == nil {
		t.Error("absent-from-HEAD file: want error (fail-open to the advisory)")
	}
}

// DirtyPaths backs the accept/refresh pre-apply refusal (tri-review P0):
// staged, unstaged, and untracked changes on the queried paths are all
// named; clean paths and paths outside the query set are not.
func TestDirtyPaths(t *testing.T) {
	repo := newPatchRepo(t)
	writeAndCommit(t, repo, "staged.txt", "base")
	writeAndCommit(t, repo, "outside.txt", "base")

	// base.txt: unstaged edit; staged.txt: staged edit; new.txt: untracked.
	// outside.txt is dirty too but never queried — scoping must hold.
	if err := os.WriteFile(filepath.Join(repo, "base.txt"), []byte("edited\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repo, "staged.txt"), []byte("edited\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	mustRun(t, repo, "add", "staged.txt")
	if err := os.WriteFile(filepath.Join(repo, "new.txt"), []byte("new\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repo, "outside.txt"), []byte("edited\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	dirty, err := DirtyPaths(repo, []string{"base.txt", "staged.txt", "new.txt", "clean.txt"})
	if err != nil {
		t.Fatal(err)
	}
	got := strings.Join(dirty, ",")
	for _, want := range []string{"base.txt", "staged.txt", "new.txt"} {
		if !strings.Contains(got, want) {
			t.Errorf("dirty = %q, want it to name %q", got, want)
		}
	}
	for _, absent := range []string{"clean.txt", "outside.txt"} {
		if strings.Contains(got, absent) {
			t.Errorf("dirty = %q, want %q excluded (clean or out of scope)", got, absent)
		}
	}

	// Commit the edits: everything drops out of the result.
	mustRun(t, repo, "add", "-A")
	mustRun(t, repo, "commit", "-m", "settle")
	if dirty, err = DirtyPaths(repo, []string{"base.txt", "staged.txt", "new.txt"}); err != nil || len(dirty) != 0 {
		t.Errorf("after commit: dirty = %v, %v, want empty", dirty, err)
	}
}
