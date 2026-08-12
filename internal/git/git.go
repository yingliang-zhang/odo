// Package git shells out to the git binary for all repository operations.
// The daemon never links libgit2 and never parses .git internals.
package git

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
)

// run executes git with -C dir and returns combined-stdout on success.
// The error includes the stderr tail so IPC callers can surface git's own
// diagnostics (e.g. "error: patch failed: hello.txt:1").
func run(dir string, args ...string) (string, error) {
	argv := append([]string{"-C", dir}, args...)
	cmd := exec.Command("git", argv...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		tail := strings.TrimSpace(stderr.String())
		if tail == "" {
			tail = err.Error()
		}
		return "", fmt.Errorf("git %s: %s", strings.Join(args, " "), tail)
	}
	return stdout.String(), nil
}

// CreateWorktree adds a detached worktree of repoPath at HEAD. Detach-only
// (B-class design): run worktrees never name a branch — accepted diffs land
// on the main working tree, so a symbolic HEAD was pure decoration, and
// force-updating it after accepts was the "already used by worktree /
// cannot force update" failure vector (F2/F3).
func CreateWorktree(repoPath, worktreePath string) error {
	_, err := run(repoPath, "worktree", "add", "--detach", worktreePath, "HEAD")
	return err
}

// PruneWorktrees drops .git/worktrees bookkeeping entries whose checkout
// dir no longer exists (the ONLY thing prune does). The startup sweeper
// runs it after reclaiming dirs so stale metadata can't accumulate.
func PruneWorktrees(repoPath string) error {
	_, err := run(repoPath, "worktree", "prune")
	return err
}

// ListOdoBranches returns the short names of every local branch under
// refs/heads/odo/ (legacy M11c workstream refs). The startup sweeper
// retires them; nil/empty on repos with none.
func ListOdoBranches(repoPath string) ([]string, error) {
	out, err := run(repoPath, "for-each-ref", "--format=%(refname:short)", "refs/heads/odo/")
	if err != nil {
		return nil, err
	}
	var branches []string
	for _, line := range strings.Split(strings.TrimSpace(out), "\n") {
		if line != "" {
			branches = append(branches, line)
		}
	}
	return branches, nil
}

// DeleteBranchMerged deletes a branch ONLY when it is merged into HEAD
// (git -d): odo/* refs only ever accumulated accepted content, so a merged
// delete loses nothing; a divergent ref refuses and stays for a human.
func DeleteBranchMerged(repoPath, branch string) error {
	_, err := run(repoPath, "branch", "-d", branch)
	return err
}

// RemoveWorktree force-removes a worktree. The worktree index may be dirty
// (ExtractDiff stages everything), so --force is required. A missing path is
// not an error.
func RemoveWorktree(repoPath, worktreePath string) error {
	if _, err := run(repoPath, "worktree", "remove", "--force", worktreePath); err != nil {
		// Tolerate worktrees deleted out from under us.
		if strings.Contains(err.Error(), "is not a working tree") ||
			strings.Contains(err.Error(), "No such file or directory") {
			return nil
		}
		return err
	}
	return nil
}

// ExtractDiff returns the full diff of the worktree against its HEAD,
// including new (untracked) files: everything is staged first, then diffed
// --cached. Staging writes only to the worktree-private index, never to the
// user's repository index. Returns "" when the worktree is unchanged.
func ExtractDiff(worktreePath string) (string, error) {
	if _, err := run(worktreePath, "add", "-A"); err != nil {
		return "", err
	}
	return run(worktreePath, "diff", "--cached", "HEAD")
}

// ApplyDiff applies a unified diff file to the working tree of repoPath.
//
// P0 (accept must not sweep the main checkout): after a successful apply it
// stages ONLY the paths the patch touches — both pre- and post-image, so
// deletions and renames record correctly — never the user's unrelated
// working-tree or index changes. The old `git add -A` folded whatever the
// user had lying around (wiki restructures, half-finished edits) into the
// accept commit.
//
// P1 (retry guardrail): it refuses to run while the index has unmerged
// entries. --3way on top of an in-progress conflict conflates two merges,
// and a retry of a previously-conflicted accept would stage half-resolved
// files. The caller keeps the diff pending; the user resolves or resets the
// conflict and retries.
//
// --3way handles targets whose content drifted since the diff's base.
func ApplyDiff(repoPath, diffPath string) error {
	if conflicts, err := HasUnmergedEntries(repoPath); err != nil {
		return fmt.Errorf("check unmerged entries: %w", err)
	} else if conflicts {
		return errors.New("index has unmerged entries: resolve or reset the in-progress conflict first")
	}
	aPaths, bPaths, err := DiffPaths(diffPath)
	if err != nil {
		return fmt.Errorf("parse patch paths: %w", err)
	}
	if _, err := run(repoPath, "apply", "--3way", diffPath); err != nil {
		return err
	}
	paths := unionPaths(aPaths, bPaths)
	if len(paths) == 0 {
		return nil
	}
	if _, err := run(repoPath, append([]string{"add", "--"}, paths...)...); err != nil {
		return fmt.Errorf("stage patch paths: %w", err)
	}
	return nil
}

// CapturePatchBaseline records, for each patch path, whether it is tracked
// in HEAD and whether it exists on disk BEFORE an apply attempt. Rollback
// needs both axes separately: a path tracked in HEAD restores via
// reset+checkout; a path the patch created (neither axis) must be deleted;
// a pre-existing UNTRACKED user file (onDisk only) must be left untouched —
// git apply refuses to clobber it, so the failure left the user's bytes in
// place and deleting them would be data loss.
func CapturePatchBaseline(repoPath string, paths []string) (inHEAD, onDisk map[string]bool, err error) {
	inHEAD = map[string]bool{}
	onDisk = map[string]bool{}
	if len(paths) == 0 {
		return inHEAD, onDisk, nil
	}
	args := append([]string{"ls-tree", "-r", "HEAD", "--name-only", "-z", "--"}, paths...)
	out, err := run(repoPath, args...)
	if err != nil {
		return nil, nil, fmt.Errorf("baseline ls-tree: %w", err)
	}
	for _, p := range strings.Split(strings.TrimRight(out, "\x00"), "\x00") {
		if p != "" {
			inHEAD[p] = true
		}
	}
	for _, p := range paths {
		if _, statErr := os.Stat(filepath.Join(repoPath, p)); statErr == nil {
			onDisk[p] = true
		}
	}
	return inHEAD, onDisk, nil
}

// RollbackPatchApply restores the main checkout to pre-accept state after
// a failed ApplyDiff, limited to the patch's own paths (I7: the attempt
// never leaves self-produced working-tree damage or unmerged index
// entries, and never touches anything outside the patch). tracked-in-HEAD
// paths reset to HEAD content (index + working tree, unmerged entries
// dropped); patch-created files (absent from HEAD AND from the pre-apply
// disk baseline) are removed; pre-existing untracked user files survive.
// Errors are joined — a partial rollback names every path it failed on.
func RollbackPatchApply(repoPath string, paths []string, inHEAD, onDisk map[string]bool) error {
	var tracked, created []string
	for _, p := range paths {
		switch {
		case inHEAD[p]:
			tracked = append(tracked, p)
		case !onDisk[p]:
			created = append(created, p)
		}
	}
	var errs []error
	if len(tracked) > 0 {
		if _, err := run(repoPath, append([]string{"reset", "-q", "HEAD", "--"}, tracked...)...); err != nil {
			errs = append(errs, fmt.Errorf("reset tracked: %w", err))
		}
		if _, err := run(repoPath, append([]string{"checkout", "--"}, tracked...)...); err != nil {
			errs = append(errs, fmt.Errorf("checkout tracked: %w", err))
		}
	}
	for _, p := range created {
		// git rm clears staged and unmerged index entries plus the file;
		// ignore-unmatch covers a path apply never actually created.
		if _, err := run(repoPath, "rm", "-q", "-f", "--ignore-unmatch", "--", p); err != nil {
			errs = append(errs, fmt.Errorf("rm created %s: %w", p, err))
			continue
		}
		// git rm is a no-op on a path with no index entry (plain new file
		// from a non-3way hunk) — remove the file itself.
		if _, statErr := os.Stat(filepath.Join(repoPath, p)); statErr == nil {
			if err := os.Remove(filepath.Join(repoPath, p)); err != nil && !errors.Is(err, os.ErrNotExist) {
				errs = append(errs, fmt.Errorf("remove created %s: %w", p, err))
			}
		}
	}
	return errors.Join(errs...)
}

// HasUnmergedEntries reports whether repoPath's index carries unresolved
// merge-conflict entries (stage > 0, per `git ls-files -u`).
func HasUnmergedEntries(repoPath string) (bool, error) {
	out, err := run(repoPath, "ls-files", "-u")
	if err != nil {
		return false, err
	}
	return strings.TrimSpace(out) != "", nil
}

// CommitPaths creates a commit limited to the given paths: the current
// working-tree state of exactly those paths, regardless of what else is
// staged or dirty. Used after applying a diff so the next worktree (created
// from HEAD) includes the accepted files without sweeping unrelated user
// changes into the accept commit. Requires git user.name and user.email to
// be configured.
func CommitPaths(repoPath, message string, paths []string) error {
	if len(paths) == 0 {
		return nil
	}
	args := append([]string{"commit", "-m", message, "--no-verify", "--"}, paths...)
	_, err := run(repoPath, args...)
	return err
}

// PatchPaths returns the deduplicated union of pre- and post-image paths of
// every file the patch touches, in file order. Both sides are needed to
// stage renames (old name deleted, new name added) and deletions (a-side
// only), and to guard protected paths on either side of a rename.
func PatchPaths(pathOnDisk string) ([]string, error) {
	aPaths, bPaths, err := DiffPaths(pathOnDisk)
	if err != nil {
		return nil, err
	}
	return unionPaths(aPaths, bPaths), nil
}

// PatchPathsText is PatchPaths for diff text already in memory (the M18
// batch-B visual gate derives paths from the diff bytes under review, not
// from journal metadata).
func PatchPathsText(diffText string) []string {
	aPaths, bPaths := diffPathsText(diffText)
	return unionPaths(aPaths, bPaths)
}

// unionPaths concatenates a- and b-side patch paths deduplicated, keeping
// file order. Pre-images come first so a rename's deletion stages alongside
// its addition.
func unionPaths(a, b []string) []string {
	seen := make(map[string]struct{}, len(a)+len(b))
	out := make([]string, 0, len(a)+len(b))
	for _, paths := range [][]string{a, b} {
		for _, p := range paths {
			if _, ok := seen[p]; !ok {
				seen[p] = struct{}{}
				out = append(out, p)
			}
		}
	}
	return out
}

// diffGitLine splits the "a/<x> b/<y>" tail of a "diff --git" header.
// Either side is C-quoted when it contains spaces or non-ASCII bytes.
var diffGitLine = regexp.MustCompile(`^("a/(?:[^"\\]|\\.)*"|a/\S+) ("b/(?:[^"\\]|\\.)*"|b/\S+)$`)

// DiffPaths parses the unified diff at pathOnDisk and returns the a-side
// (pre-image) and b-side (post-image) path of every file header, in file
// order. Paths come from "--- a/<x>" / "+++ b/<y>" lines, falling back to
// the "diff --git" header for changes without content hunks (mode-only,
// pure renames). C-quoted paths are unquoted to real filesystem names so
// the results work directly as git pathspecs; malformed quoting is kept
// verbatim and git apply stays the authority on the patch format. Pure
// additions yield only a b-side, pure deletions only an a-side.
func DiffPaths(pathOnDisk string) (aPaths, bPaths []string, err error) {
	data, err := os.ReadFile(pathOnDisk)
	if err != nil {
		return nil, nil, err
	}
	aPaths, bPaths = diffPathsText(string(data))
	return aPaths, bPaths, nil
}

// diffPathsText is the DiffPaths core over in-memory diff text.
func diffPathsText(text string) (aPaths, bPaths []string) {
	pendingA, pendingB := "", "" // diff --git sides, used when no ---/+++ appears
	for _, line := range strings.Split(text, "\n") {
		switch {
		case strings.HasPrefix(line, "diff --git "):
			if pendingA != "" {
				aPaths = append(aPaths, pendingA)
			}
			if pendingB != "" {
				bPaths = append(bPaths, pendingB)
			}
			pendingA, pendingB = "", ""
			if m := diffGitLine.FindStringSubmatch(strings.TrimPrefix(line, "diff --git ")); m != nil {
				pendingA = unquoteDiffPath(m[1], "a/")
				pendingB = unquoteDiffPath(m[2], "b/")
			}
		case strings.HasPrefix(line, "--- "):
			pendingA = "" // the --- line resolves the a-side (even /dev/null)
			if p := unquoteDiffPath(strings.TrimPrefix(line, "--- "), "a/"); p != "" {
				aPaths = append(aPaths, p)
			}
		case strings.HasPrefix(line, "+++ "):
			pendingB = "" // the +++ line resolves the b-side (even /dev/null)
			if p := unquoteDiffPath(strings.TrimPrefix(line, "+++ "), "b/"); p != "" {
				bPaths = append(bPaths, p)
			}
		}
	}
	if pendingA != "" {
		aPaths = append(aPaths, pendingA)
	}
	if pendingB != "" {
		bPaths = append(bPaths, pendingB)
	}
	return aPaths, bPaths
}

// unquoteDiffPath normalizes one path field of a diff header: drops an
// optional trailing timestamp, resolves C-style quoting, and strips the
// a//b/ prefix. Returns "" for /dev/null and for sides that don't carry
// the expected prefix (malformed input — git apply adjudicates those).
func unquoteDiffPath(target, prefix string) string {
	if i := strings.IndexByte(target, '\t'); i >= 0 {
		target = target[:i]
	}
	target = strings.TrimSpace(target)
	if target == "/dev/null" {
		return ""
	}
	if strings.HasPrefix(target, "\"") {
		if u, err := strconv.Unquote(target); err == nil {
			target = u
		} else {
			target = strings.Trim(target, "\"")
		}
	}
	if !strings.HasPrefix(target, prefix) {
		return ""
	}
	return strings.TrimPrefix(target, prefix)
}

// FileStat is one file's change stats inside a unified diff.
type FileStat struct {
	Path        string // b-side (post-image); a-side for deletions
	Added       int
	Removed     int
	NewFile     bool
	DeletedFile bool
	CommentOnly bool // every changed content line is blank or starts a comment
}

// PatchStat aggregates one patch file's per-file and total line stats.
type PatchStat struct {
	Files   []FileStat
	Added   int
	Removed int
}

// commentMarkers classify a trimmed content line as comment-only. The set
// is deliberately language-agnostic (the autonomy classifier treats any
// change composed solely of these lines as C1 regardless of file type).
var commentMarkers = []string{"//", "#", "/*", "*", "<!--", "-->"}

// testAssertionTokens are the Go test-assertion markers the auto-land
// weakened-tests gate counts in *_test.go hunks. t.Error covers Errorf,
// t.Fatal covers Fatalf; assert./require. cover the testify family.
var testAssertionTokens = []string{"assert.", "require.", "t.Error", "t.Fatal", "t.Fail"}

// TestAssertionDelta counts assertion tokens on added vs removed content
// lines across the *_test.go files of the patch at pathOnDisk (global
// across files, so moving a test between files nets zero). Comment lines
// (commentMarkers) are skipped on both sides — a commented-out assertion
// changed the proof, not just the text. A diff whose removed count
// exceeds its added count shrinks proof coverage — the auto-land gate
// blocks "agent weakens its own tests" (the silent-disaster mode the
// panel's reviewers independently converged on).
// Each hunk side arms independently: a deletion (b-side /dev/null) still
// counts its removed assertions, a pure addition only its added ones.
func TestAssertionDelta(pathOnDisk string) (added, removed int, err error) {
	data, err := os.ReadFile(pathOnDisk)
	if err != nil {
		return 0, 0, err
	}
	var aTest, bTest bool // current file block's a/b sides are *_test.go
	for _, line := range strings.Split(string(data), "\n") {
		switch {
		case strings.HasPrefix(line, "diff --git "):
			aTest, bTest = false, false
		case strings.HasPrefix(line, "--- "):
			p := unquoteDiffPath(strings.TrimPrefix(line, "--- "), "a/")
			aTest = strings.HasSuffix(p, "_test.go")
			if !strings.HasPrefix(line, "--- /dev/null") {
				bTest = aTest // default until +++ overrides (handles missing +++)
			}
		case strings.HasPrefix(line, "+++ "):
			p := unquoteDiffPath(strings.TrimPrefix(line, "+++ "), "b/")
			bTest = p != "" && strings.HasSuffix(p, "_test.go")
		case strings.HasPrefix(line, "+") && !strings.HasPrefix(line, "+++") && bTest:
			// Comment lines don't execute: `+// assert.X` must not count as
			// added proof, or commenting an assertion out nets zero and the
			// weakened-tests gate waves a dead check through (M16 panel).
			if hasAnyPrefix(strings.TrimSpace(line[1:]), commentMarkers) {
				continue
			}
			for _, tok := range testAssertionTokens {
				added += strings.Count(line, tok)
			}
		case strings.HasPrefix(line, "-") && !strings.HasPrefix(line, "---") && aTest:
			// Mirror: removing a comment line never counted as lost proof.
			if hasAnyPrefix(strings.TrimSpace(line[1:]), commentMarkers) {
				continue
			}
			for _, tok := range testAssertionTokens {
				removed += strings.Count(line, tok)
			}
		}
	}
	return added, removed, nil
}

// PatchStats parses the unified diff at pathOnDisk into per-file and total
// line stats. Paths follow the same a/b-side and C-quoting rules as
// DiffPaths. A file with zero content lines (mode-only changes) reports
// CommentOnly false — there is no evidence either way.
func PatchStats(pathOnDisk string) (PatchStat, error) {
	data, err := os.ReadFile(pathOnDisk)
	if err != nil {
		return PatchStat{}, err
	}
	var stat PatchStat
	idx := -1 // current file in stat.Files, -1 = outside any file block
	var pendNew, pendDel bool
	nonComment := map[int]bool{} // files with at least one non-comment content line
	appendFile := func(p string) {
		stat.Files = append(stat.Files, FileStat{Path: p, NewFile: pendNew, DeletedFile: pendDel})
		idx = len(stat.Files) - 1
	}
	for _, line := range strings.Split(string(data), "\n") {
		switch {
		case strings.HasPrefix(line, "diff --git "):
			idx, pendNew, pendDel = -1, false, false
		case strings.HasPrefix(line, "new file mode"):
			pendNew = true
		case strings.HasPrefix(line, "deleted file mode"):
			pendDel = true
		case strings.HasPrefix(line, "--- "):
			if p := unquoteDiffPath(strings.TrimPrefix(line, "--- "), "a/"); p != "" {
				appendFile(p)
			}
		case strings.HasPrefix(line, "+++ "):
			if p := unquoteDiffPath(strings.TrimPrefix(line, "+++ "), "b/"); p != "" {
				// Post-image name wins (renames): the change is reviewed
				// under the new path.
				if idx >= 0 {
					stat.Files[idx].Path = p
				} else {
					appendFile(p)
				}
			}
		}
		if idx < 0 {
			continue
		}
		var content string
		switch {
		case strings.HasPrefix(line, "+") && !strings.HasPrefix(line, "+++"):
			stat.Files[idx].Added++
			stat.Added++
			content = line[1:]
		case strings.HasPrefix(line, "-") && !strings.HasPrefix(line, "---"):
			stat.Files[idx].Removed++
			stat.Removed++
			content = line[1:]
		default:
			continue
		}
		if trimmed := strings.TrimSpace(content); trimmed != "" && !hasAnyPrefix(trimmed, commentMarkers) {
			nonComment[idx] = true
		}
	}
	for i := range stat.Files {
		f := &stat.Files[i]
		f.CommentOnly = f.Added+f.Removed > 0 && !nonComment[i]
	}
	return stat, nil
}

// hasAnyPrefix reports whether s starts with any of the given prefixes.
func hasAnyPrefix(s string, prefixes []string) bool {
	for _, p := range prefixes {
		if strings.HasPrefix(s, p) {
			return true
		}
	}
	return false
}

// ListTreeNames lists the top-level entry names of the tree at sha
// (read-only). Used by the autonomy classifier's new-top-level-directory
// check against a diff's base commit.
func ListTreeNames(repoPath, sha string) ([]string, error) {
	out, err := run(repoPath, "ls-tree", "--name-only", sha)
	if err != nil {
		return nil, err
	}
	var names []string
	for _, line := range strings.Split(strings.TrimRight(out, "\n"), "\n") {
		if line != "" {
			names = append(names, line)
		}
	}
	return names, nil
}

// CurrentSHA returns the full HEAD commit SHA of repoPath.
func CurrentSHA(repoPath string) (string, error) {
	out, err := run(repoPath, "rev-parse", "HEAD")
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(out), nil
}
