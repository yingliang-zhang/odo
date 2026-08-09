# Odo Rename + Install + Cleanup — Tri-Model Review Brief

## 1. Task

Three operations the user wants to perform before starting fresh with Odo:
1. **GitHub rename**: rename the repo from `odo` to `odo-agent` (and update all local references)
2. **Install to Applications**: build a release .app and install to /Applications
3. **Clean test artifacts**: remove all test/dev residue so the user starts with a clean Odo

Review each operation for safety, correctness, and completeness.

## 2. Current State

### Repository
- **Git remote**: `git@github.com:yingliang-zhang/odo.git` (SSH)
- **Another remote**: `ananke-archive` (old archived repo, unrelated)
- **go.mod**: `module github.com/yingliang-zhang/odo`
- **tauri.conf.json**: `"productName": "Odo"`, `"identifier": "com.yingliangzhang.odo"`, `"title": "Odo"`
- **Binary**: `gui/src-tauri/target/debug/bundle/macos/Odo.app` exists (debug build)
- **No release build** yet
- **Cargo**: available at `~/.cargo/bin/cargo` (not in PATH — needs `~/.cargo/bin` prepended)

### Test/dev artifacts to clean
- `.odo/sessions/` — 36 session dirs from test runs
- `.odo/diffs/` — 11 diff files from test runs
- `.odo/daemon.log` — 246 lines of test daemon logs
- `.odo/journal.sqlite` — 36KB test journal (has test conversations/events)
- `gui/dist/` — Vite build output
- `gui/test-results/` — Playwright test artifacts (may not exist)
- `wiki/` — 2 epoch notes + topics from test distill runs
- `.odo/ledger.md` — 2 epochs from test distills
- `.odo/worktrees/` — empty (0 dirs, already cleaned)

### What to PRESERVE
- `~/.odo/user.md` — just created, keep
- `~/.odo/prefs.md` — global preferences, keep
- Source code (all .go, .tsx, .ts, .rs, .css files)
- `.gitignore`, `README.md`, `memory/log.md`, `docs/`

## 3. Operations

### Op1: GitHub rename odo → odo-agent

GitHub repo rename is done in GitHub Settings (UI, not CLI). After rename:
- GitHub auto-redirects old URL → new URL
- Local remote `origin` needs updating to `git@github.com:yingliang-zhang/odo-agent.git`
- `go.mod` module path changes: `github.com/yingliang-zhang/odo` → `github.com/yingliang-zhang/odo-agent`
- All Go imports referencing `github.com/yingliang-zhang/odo` need updating
- `tauri.conf.json` identifier may need updating: `com.yingliangzhang.odo` → `com.yingliangzhang.odo-agent`
- `tauri.conf.json` productName may stay "Odo" (product name) or change to "Odo Agent"
- README references to repo URL need updating

### Op2: Install to /Applications

Steps:
1. Build release: `cargo tauri build` (or `cargo tauri build --debug` for faster)
2. Copy `.app` bundle to `/Applications/`
3. May need codesign for Gatekeeper

### Op3: Clean test artifacts

Remove all test/dev residue:
- `.odo/sessions/*` (36 dirs)
- `.odo/diffs/*` (11 files)
- `.odo/daemon.log`
- `.odo/journal.sqlite` (or reset — delete and let bootstrap recreate)
- `.odo/ledger.md` (test distills)
- `wiki/*` (test epoch notes + topics)
- `gui/dist/` (rebuild on next dev)
- `gui/test-results/` if exists

Preserve: source code, user.md, prefs.md, .gitignore

## 4. Questions for Reviewers

**Q1**: For the GitHub rename, what's the complete checklist of files/references that need updating? Is `go.mod` module rename safe (all imports are internal)? Should the tauri identifier change?
**Q2**: For the install, is `cargo tauri build` (release) the right command? Does it need codesign? Is there a faster path (debug build)?
**Q3**: For the cleanup, is there anything that should NOT be deleted? Will deleting `journal.sqlite` cause issues on next bootstrap? Will deleting `wiki/` cause issues?
**Q4**: Is there a recommended order for these three operations (rename first? clean first? install last?)
**Q5**: Any risks or gotchas the user should know about?

Read the Odo repo (`/Users/yingliangzhang/Projects/odo`) to ground your analysis. Write your complete analysis as text. Do NOT write files to the repository.
