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
	"time"
)

// run executes git with -C dir and returns combined-stdout on success.
// The error includes the stderr tail so IPC callers can surface git's own
// diagnostics (e.g. "error: patch failed: hello.txt:1").
func run(dir string, args ...string) (string, error) {
	return runWithEnv(dir, nil, args...)
}

// runWithEnv is run with extra environment layered onto the process env —
// ExtraEditsBeyondPatch points GIT_INDEX_FILE at a temporary index so its
// read-tree/apply/ls-files pipeline never touches the repo's real index.
func runWithEnv(dir string, env []string, args ...string) (string, error) {
	argv := append([]string{"-C", dir}, args...)
	cmd := exec.Command("git", argv...)
	cmd.Env = append(os.Environ(), env...)
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
	if err := StageExistingPaths(repoPath, unionPaths(aPaths, bPaths)); err != nil {
		return fmt.Errorf("stage patch paths: %w", err)
	}
	return nil
}

// StageExistingPaths stages the patch's path set by ACTUAL state, not by the
// remembered path list (pitfall #57, diff #138): `apply --3way` records a
// rename in the index AS IT APPLIES, and the pre-image path then matches
// neither the working tree nor the index, so a bare `git add -- <pre-image>`
// dies on "pathspec ... did not match any files" — every accept of a rename
// diff wedged at staging. A path absent from both can only be such an
// already-recorded deletion (index absence IS the staged-delete record —
// there is no third state), so skipping it loses nothing. Survivors go to
// pathspec-scoped `git add --`, which since Git 2.0 records modifications,
// unrecorded deletions (clean working-tree-only hunks), and untracked
// post-image files alike. The scope stays exactly the patch's paths — P0
// (never sweep unrelated main-checkout changes) is unchanged. The M20
// already-landed accept routes its patch paths through the same filter: a
// rename that already landed leaves the pre-image a ghost there too, and an
// unmatched pathspec would void the bookkeeping stage silently.
func StageExistingPaths(repoPath string, paths []string) error {
	if len(paths) == 0 {
		return nil
	}
	indexed, err := run(repoPath, append([]string{"ls-files", "-z", "--"}, paths...)...)
	if err != nil {
		return fmt.Errorf("probe index paths: %w", err)
	}
	inIndex := make(map[string]struct{}, len(paths))
	for _, p := range strings.Split(strings.TrimRight(indexed, "\x00"), "\x00") {
		if p != "" {
			inIndex[p] = struct{}{}
		}
	}
	stage := make([]string, 0, len(paths))
	for _, p := range paths {
		if _, ok := inIndex[p]; ok {
			stage = append(stage, p)
			continue
		}
		// Untracked post-image: a clean working-tree-only hunk created the
		// file without recording it. Patch paths are slash-separated;
		// translate for the filesystem probe.
		if _, statErr := os.Lstat(filepath.Join(repoPath, filepath.FromSlash(p))); statErr == nil {
			stage = append(stage, p)
		}
	}
	return StagePaths(repoPath, stage)
}

// StagePaths stages exactly paths — both pre- and post-image, so deletions
// and renames record correctly. ApplyDiff runs it post-apply over
// StageExistingPaths' survivors; the M20 already-landed accept reaches it
// through StageExistingPaths for the same ghost-path reason (nothing was
// applied to stage, and `git commit -- <untracked>` would refuse an
// untracked post-image file the user produced out-of-band).
func StagePaths(repoPath string, paths []string) error {
	if len(paths) == 0 {
		return nil
	}
	_, err := run(repoPath, append([]string{"add", "--"}, paths...)...)
	return err
}

// ProbeApplyClean tests whether diffPath would apply cleanly via --3way onto
// repoPath's current HEAD, using a throwaway worktree that touches neither
// the main checkout nor the diff's own worktree. Returns (true, "", nil) if
// the 3-way merge is clean; (false, detail, nil) if it produces conflicts
// (detail is git's stderr tail); (false, "", err) on other failures (e.g.
// missing base blobs, worktree creation failure). The temp worktree is
// removed unconditionally.
func ProbeApplyClean(repoPath, diffPath string) (clean bool, detail string, err error) {
	// The apply runs INSIDE the temp worktree, so a relative diffPath would
	// resolve against it — pin the patch to an absolute path first.
	abs, err := filepath.Abs(diffPath)
	if err != nil {
		return false, "", fmt.Errorf("probe patch path: %w", err)
	}
	tmp, err := os.MkdirTemp("", "odo-probe-*")
	if err != nil {
		return false, "", fmt.Errorf("probe worktree dir: %w", err)
	}
	// The temp worktree is removed unconditionally: RemoveWorktree tolerates
	// a failed/never-completed add, and RemoveAll is belt-and-suspenders for
	// the empty MkdirTemp dir in that case.
	defer func() {
		_ = RemoveWorktree(repoPath, tmp)
		_ = os.RemoveAll(tmp)
	}()
	if _, err := run(repoPath, "worktree", "add", "--detach", tmp, "HEAD"); err != nil {
		return false, "", fmt.Errorf("probe worktree add: %w", err)
	}
	if _, applyErr := run(tmp, "apply", "--3way", abs); applyErr != nil {
		// A failed --3way that left unmerged index entries is a merge
		// conflict (reportable, detail = git's own diagnostics); anything
		// else — missing base blobs, unreadable patch — is a plain error.
		if conflicts, cerr := HasUnmergedEntries(tmp); cerr == nil && conflicts {
			return false, applyErr.Error(), nil
		}
		return false, "", applyErr
	}
	return true, "", nil
}

// ProbeAlreadyLanded is the content-roundtrip check: if diffPath applies
// cleanly in REVERSE (git apply --reverse --check) against repoPath's
// working tree, every line the patch would add is already there — the
// diff's content reached main through a path the daemon never applied (a
// manual merge, a cherry-pick, the user's own identical edit). Pure
// tree-state: no commit ancestry is consulted, and a PARTIAL landing
// reverse-checks dirty, correctly reporting false (the ordinary rebase
// path then adjudicates). Read-only — --check writes nothing. Returns
// (true, "", nil) when the post-image is fully present; (false, detail,
// nil) when not (detail is git's diagnostics); (false, "", err) when the
// check itself could not run (no git binary, not a repo).
func ProbeAlreadyLanded(repoPath, diffPath string) (landed bool, detail string, err error) {
	abs, err := filepath.Abs(diffPath)
	if err != nil {
		return false, "", fmt.Errorf("already-landed probe patch path: %w", err)
	}
	cmd := exec.Command("git", "-C", repoPath, "apply", "--reverse", "--check", abs)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	runErr := cmd.Run()
	if runErr == nil {
		return true, "", nil
	}
	// exit 1 is the mismatch verdict ("error: patch failed"); anything
	// else — not a repo, unreadable patch, no git binary — is an error,
	// not evidence about the tree.
	var exitErr *exec.ExitError
	if errors.As(runErr, &exitErr) && exitErr.ExitCode() == 1 {
		tail := strings.TrimSpace(stderr.String())
		if tail == "" {
			tail = runErr.Error()
		}
		return false, tail, nil
	}
	return false, "", fmt.Errorf("git apply --reverse --check: %w (%s)", runErr, strings.TrimSpace(stderr.String()))
}

// PathsDifferFromHEAD reports whether any of paths differ between HEAD and
// the checkout (working tree or index) — i.e. whether a path-scoped commit
// would record anything. Used by the already-landed accept: content that
// arrived uncommitted still needs the accept commit; content already
// committed must skip it (an empty commit is a git error). exit 1 from
// `git diff --quiet` means differences; anything else is a real error.
func PathsDifferFromHEAD(repoPath string, paths []string) (bool, error) {
	if len(paths) == 0 {
		return false, nil
	}
	args := append([]string{"-C", repoPath, "diff", "--quiet", "HEAD", "--"}, paths...)
	cmd := exec.Command("git", args...)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	err := cmd.Run()
	if err == nil {
		return false, nil
	}
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) && exitErr.ExitCode() == 1 {
		return true, nil
	}
	tail := strings.TrimSpace(stderr.String())
	if tail == "" {
		tail = err.Error()
	}
	return false, fmt.Errorf("git diff --quiet HEAD: %s", tail)
}

// ExtraEditsBeyondPatch returns the subset of the patch's own paths whose
// WORKING-TREE bytes differ from the patch's post-image (tri-review P1,
// 2026-08-24, the "already-landed sweep"). The accept path's M20
// reverse-apply probe sees the patch only at hunk granularity: user edits
// on the same file BEYOND the hunks survive the probe, and CommitPaths
// records whole working-tree files — without this check the already-
// landed accept commit sweeps those uncommitted edits in. Byte-exact
// comparison is sound where hunk context is not.
//
// The expected post-image is reconstructed in a TEMPORARY index
// (GIT_INDEX_FILE pointed at a throwaway file — never the repo's real
// index, refs, or working tree): `read-tree HEAD` loads the committed
// pre-image and `git apply --cached` folds the patch in. Two placements
// of the landed content both reconstruct exactly:
//   - content UNCOMMITTED (HEAD carries the pre-image): the forward
//     apply succeeds, the temp index holds the post-image;
//   - content already COMMITTED (HEAD IS the post-image — a manual
//     merge/cherry-pick landed it): the reverse --check succeeds, so the
//     HEAD-seeded temp index is the post-image as-is.
//
// Both directions failing means HEAD drifted inside the hunks
// themselves; the probe then names every patch path whose worktree bytes
// differ from HEAD — an ambiguous path is refused rather than swept
// (conservative).
//
// Per path, the expected blob sha from `ls-files -s` (absent entry = the
// post-image deletes the path) must equal `git hash-object` of the
// working-tree file (or the file must be gone exactly when deleted).
// Anything else is an extra edit. The repo's own state is untouched:
// every index operation runs under GIT_INDEX_FILE, --check reads nothing
// but bytes, and hash-object is content-addressing only.
func ExtraEditsBeyondPatch(repoPath, diffPath string) ([]string, error) {
	paths, err := PatchPaths(diffPath)
	if err != nil {
		return nil, fmt.Errorf("extra-edits probe paths: %w", err)
	}
	if len(paths) == 0 {
		return nil, nil
	}
	// The apply runs with -C repoPath — pin the patch absolute, as with
	// the other probes.
	abs, err := filepath.Abs(diffPath)
	if err != nil {
		return nil, fmt.Errorf("extra-edits probe patch path: %w", err)
	}
	// GIT_INDEX_FILE needs a path, not an open handle; a missing file
	// lets read-tree seed a fresh index.
	tmpIdx, err := os.CreateTemp("", "odo-extra-index-*")
	if err != nil {
		return nil, fmt.Errorf("extra-edits temp index: %w", err)
	}
	idx := tmpIdx.Name()
	_ = tmpIdx.Close()
	_ = os.Remove(idx)
	defer func() { _ = os.Remove(idx) }()
	env := []string{"GIT_INDEX_FILE=" + idx}

	if _, err := runWithEnv(repoPath, env, "read-tree", "HEAD"); err != nil {
		return nil, fmt.Errorf("extra-edits read-tree HEAD: %w", err)
	}
	if _, err := runWithEnv(repoPath, env, "apply", "--cached", abs); err != nil {
		// The apply is all-or-nothing; re-seed HEAD so the next step's
		// provenance stays obvious regardless.
		if _, rerr := runWithEnv(repoPath, env, "read-tree", "HEAD"); rerr != nil {
			return nil, fmt.Errorf("extra-edits read-tree HEAD (reverse placement): %w", rerr)
		}
		if _, cerr := runWithEnv(repoPath, env, "apply", "--cached", "--reverse", "--check", abs); cerr != nil {
			// Neither direction positions the patch against HEAD — the
			// hunk regions themselves drifted. Refuse-side: name every
			// patch path whose worktree bytes differ from the HEAD bytes
			// (the failed atomic apply left the index exactly HEAD).
			return diffWorktreeFromIndex(repoPath, env, paths)
		}
		// HEAD already IS the post-image (the content arrived committed);
		// --check wrote nothing, so the HEAD-seeded index is the
		// expectation as-is.
	}
	return diffWorktreeFromIndex(repoPath, env, paths)
}

// diffWorktreeFromIndex names the subset of paths whose working-tree
// content differs from the index env selects (GIT_INDEX_FILE): the
// expected blob sha comes from `ls-files -s` (no entry = the post-image
// deletes the path); the actual sha from `git hash-object` of the
// worktree file under the repo's REAL index (hashing is index-agnostic).
func diffWorktreeFromIndex(repoPath string, env []string, paths []string) ([]string, error) {
	ls, err := runWithEnv(repoPath, env, append([]string{"ls-files", "-s", "-z", "--"}, paths...)...)
	if err != nil {
		return nil, fmt.Errorf("extra-edits ls-files: %w", err)
	}
	want := make(map[string]string, len(paths)) // path → expected blob sha; absent = deleted post-image
	for _, e := range strings.Split(strings.TrimRight(ls, "\x00"), "\x00") {
		tab := strings.IndexByte(e, '\t')
		if tab < 0 { // empty tail after the last NUL
			continue
		}
		f := strings.Fields(e[:tab]) // "<mode> <sha> <stage>"
		if len(f) < 2 {
			continue
		}
		want[e[tab+1:]] = f[1]
	}
	var extra []string
	for _, p := range paths {
		expect, tracked := want[p]
		_, statErr := os.Stat(filepath.Join(repoPath, p))
		switch {
		case !tracked && errors.Is(statErr, os.ErrNotExist):
			// Deletion is the expectation and it holds — never an edit.
		case !tracked:
			extra = append(extra, p) // post-image deletes; the worktree re-created the file
		case errors.Is(statErr, os.ErrNotExist):
			extra = append(extra, p) // post-image has content; the worktree lost the file
		default:
			got, herr := run(repoPath, "hash-object", "--", p)
			if herr != nil {
				return nil, fmt.Errorf("extra-edits hash-object %s: %w", p, herr)
			}
			if strings.TrimSpace(got) != expect {
				extra = append(extra, p)
			}
		}
	}
	return extra, nil
}

// IndexEditsBeyondHEAD returns the subset of paths whose REAL index entry
// diverges from HEAD's tree entry — staged user content on the patch's own
// paths (tri-review P1, 2026-08-24). The accept flow's other guards miss
// this shape: DirtyPaths's porcelain check runs only on the fresh-apply
// path, and ExtraEditsBeyondPatch compares WORKTREE bytes against the
// post-image — so a staged edit whose worktree happens to hold the
// post-image (or any staged content under an already-landed or refresh
// accept) survives every check, and the stage+commit pair then rewrites
// the index entry wholesale, losing the staged bytes with no record.
//
// The comparison is entry-for-entry over `ls-files -s -z` (the real index)
// vs `ls-tree -z HEAD` and names any divergence: a staged edit (blob sha
// or mode differs), a staged new file (index-only entry), a staged
// deletion (HEAD-only entry), or a non-zero stage (a conflict side
// HasUnmergedEntries's caller also refuses, pinned here so the helper's
// contract stands alone). Zero paths is a no-op. Read-only: ls-files and
// ls-tree never touch index, HEAD, or worktree.
func IndexEditsBeyondHEAD(repoPath string, paths []string) ([]string, error) {
	if len(paths) == 0 {
		return nil, nil
	}
	liveIdx, err := run(repoPath, append([]string{"ls-files", "-s", "-z", "--"}, paths...)...)
	if err != nil {
		return nil, fmt.Errorf("staged-edits ls-files: %w", err)
	}
	headTree, err := run(repoPath, append([]string{"ls-tree", "-z", "HEAD", "--"}, paths...)...)
	if err != nil {
		return nil, fmt.Errorf("staged-edits ls-tree: %w", err)
	}
	// path → "<mode> <sha>" fingerprints on both sides; a path whose index
	// entries are ONLY non-zero stages records in unmerged instead.
	index := make(map[string]string, len(paths))
	unmerged := make(map[string]bool, len(paths))
	for _, e := range strings.Split(strings.TrimRight(liveIdx, "\x00"), "\x00") {
		tab := strings.IndexByte(e, '\t')
		if tab < 0 { // empty tail after the last NUL
			continue
		}
		f := strings.Fields(e[:tab]) // "<mode> <sha> <stage>"
		if len(f) < 3 {
			continue
		}
		if f[2] != "0" {
			unmerged[e[tab+1:]] = true
			continue
		}
		index[e[tab+1:]] = f[0] + " " + f[1]
	}
	head := make(map[string]string, len(paths))
	for _, e := range strings.Split(strings.TrimRight(headTree, "\x00"), "\x00") {
		tab := strings.IndexByte(e, '\t')
		if tab < 0 {
			continue
		}
		f := strings.Fields(e[:tab]) // "<mode> <type> <sha>"
		if len(f) < 3 {
			continue
		}
		head[e[tab+1:]] = f[0] + " " + f[2]
	}
	var staged []string
	for _, p := range paths {
		if unmerged[p] {
			staged = append(staged, p)
			continue
		}
		i, inIndex := index[p]
		h, inHEAD := head[p]
		if inIndex != inHEAD || i != h {
			staged = append(staged, p)
		}
	}
	return staged, nil
}

// ShowHEADFile returns path's content as committed in HEAD, read via the
// git binary (`git show HEAD:path`) — never the working copy: run
// worktrees materialize HEAD's tracked content, so decisions about what a
// future run will see must consult the commit, not the checkout (the
// .odo-verify advisory's configured check). Any failure — file absent
// from HEAD, not a repo, no git binary — is returned as error.
func ShowHEADFile(repoPath, path string) (string, error) {
	return run(repoPath, "show", "HEAD:"+path)
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

// HasPathChanges reports whether the given paths carry staged, unstaged,
// or untracked changes — the daemon's own-directory "anything to commit?"
// probe (wiki auto-commit skips a no-op commit).
func HasPathChanges(repoPath string, paths []string) (bool, error) {
	dirty, err := DirtyPaths(repoPath, paths)
	if err != nil {
		return false, err
	}
	return len(dirty) > 0, nil
}

// DirtyPaths returns the subset of the given paths carrying staged,
// unstaged, or untracked changes. This is the accept/refresh pre-apply
// refusal check (tri-review P0, 2026-08-24): git apply --3way over a
// user's uncommitted work either fails — and RollbackPatchApply's
// reset+checkout then restores HEAD bytes over edits the tool never
// touched — or merges cleanly and sweeps those edits into the accept
// commit. Naming the dirty paths up front turns both outcomes into a
// clear, retryable refusal. porcelain -z records rename/copy partners
// as a bare second field; those ride along best-effort (the caller only
// names paths in the refusal message, never acts on them).
func DirtyPaths(repoPath string, paths []string) ([]string, error) {
	if len(paths) == 0 {
		return nil, nil
	}
	args := append([]string{"status", "--porcelain", "-z", "--"}, paths...)
	out, err := run(repoPath, args...)
	if err != nil {
		return nil, err
	}
	var dirty []string
	for _, f := range strings.Split(strings.TrimRight(out, "\x00"), "\x00") {
		switch {
		case len(f) > 3:
			dirty = append(dirty, f[3:]) // "XY <path>"
		case f != "":
			dirty = append(dirty, f) // rename/copy partner path
		}
	}
	return dirty, nil
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

// HeadCommitTime returns the commit time of the repoPath HEAD commit
// (unix seconds, converted to UTC). Read-only, same CurrentSHA precedent.
func HeadCommitTime(repoPath string) (time.Time, error) {
	out, err := run(repoPath, "log", "-1", "--format=%ct", "HEAD")
	if err != nil {
		return time.Time{}, err
	}
	sec, err := strconv.ParseInt(strings.TrimSpace(out), 10, 64)
	if err != nil {
		return time.Time{}, fmt.Errorf("git log: parse HEAD commit time %q: %w", strings.TrimSpace(out), err)
	}
	return time.Unix(sec, 0).UTC(), nil
}

// DiffRange returns `git diff base..head` for repoPath (commit-to-commit —
// the main checkout's working tree never pollutes the result). M19 /loop:
// the Mode A audit subject is diff(frozen_base..HEAD).
func DiffRange(repoPath, base, head string) (string, error) {
	return run(repoPath, "diff", base+".."+head)
}
