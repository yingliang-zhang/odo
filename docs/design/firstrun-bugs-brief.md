# Odo First-Run Bugs — Tri-Model Analysis Brief

## 1. Task

Analyze two bugs the user hit on first real use of Odo from /Applications:

### Bug 1: Enter sends mid-IME-composition

User typed "现在odo" (Chinese + English mix). While composing with IME (input method editor), pressing Enter to confirm the IME composition sent the message instead of inserting a newline or confirming the candidate.

**Current code** (ChatSurface.tsx:427):
```tsx
if (e.key === "Enter" && !e.shiftKey && !e.metaKey && !e.ctrlKey) {
  e.preventDefault();
  void submitDraft();
  return;
}
```

This fires on every Enter, including IME composition confirmation. The standard fix is checking `e.nativeEvent.isComposing` or `e.keyCode === 229`.

### Bug 2: `omp: command not found` (exit status 127)

The OMP wrapper script (`omp_with_timeout.sh:1169`) runs `omp -p … --yolo --max-time N`, but `omp` is not on the daemon's PATH.

**Current code** (omp.go:206):
```go
cmd := exec.Command(a.wrapperPath, args...)
cmd.Dir = workdir
```

The daemon process inherits the environment of whatever started it. When launched from /Applications/Odo.app, the PATH is the minimal macOS default (`/usr/bin:/bin:/usr/sbin:/sbin`), NOT the user's shell PATH which includes `~/.omp/bin`, `~/.local/bin`, conda paths, etc. The wrapper script itself is found (absolute path from prefs.md), but `omp` inside the wrapper is looked up via PATH.

**Error**: `omp_with_timeout.sh: line 1169: omp: command not found` → exit 127

## 2. Questions for Reviewers

**Q1**: For Bug 1 (IME), what's the correct fix? Check `isComposing` on the React synthetic event or the native event? Are there edge cases (different IME implementations, Safari vs Chrome webview)? Should we also handle `compositionstart`/`compositionend` events as a belt-and-braces approach?

**Q2**: For Bug 2 (PATH), what's the correct fix? Options:
- (a) Set `cmd.Env` in omp.go to include `~/.omp/bin` and common paths
- (b) Resolve `omp` to an absolute path in the daemon before spawning the wrapper
- (c) Have the wrapper resolve `omp` itself (it already knows where OMP lives)
- (d) Use the user's shell environment (login shell) to get PATH
- What does the wrapper script already do for PATH resolution? Read `~/.hermes/profiles/orchestrator/scripts/omp_with_timeout.sh`.

**Q3**: Are there other PATH-dependent commands the wrapper or OMP might need? (e.g., `go`, `node`, `git`, `rg`, `python3`)

**Q4**: Is there a broader issue with the daemon's environment when launched from a .app bundle vs terminal? What environment does `launchctl` / Finder provide vs a shell?

**Q5**: Should the Enter-sends behavior be configurable (some users want Enter=newline, Cmd+Enter=send)? Or is the current "Enter sends, Shift+Enter newlines" the right default for a chat-like interface?

Read the Odo repo (`/Users/yingliangzhang/Projects/odo`) and the wrapper script to ground your analysis. Write your complete analysis as text. Do NOT write files to the repository.
