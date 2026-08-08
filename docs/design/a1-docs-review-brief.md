# A1 Clipboard Paste Fix + Docs Cleanup — Tri-Model Review Brief

## 1. Task

Review two commits:
- `6ecbac0` — "feat: A1 clipboard paste image fix — save_attachment daemon cmd"
- `1e0001c` — "docs: update README + memory/log.md for M8-M11 + PR1-3 + diff/P2/A1"

### Commit 6ecbac0 (A1: Clipboard paste fix)

5 files changed, 104 insertions, 4 deletions.

**Problem**: Tauri webview `clipboardData.files` exposes only `File.name` (no real path). Daemon's `QueryWithImages` uses `os.ReadFile(imgPath)` from disk → ENOENT. Only drag-drop worked (Tauri provides real paths). The most common Mac screenshot path (⌃⌘⇧4 → paste) was broken.

**Fix**: New `save_attachment` daemon command accepts `{name, base64data}`, writes to `.odo/attachments/<timestamp>-<name>`, returns the absolute path. ChatSurface paste handler reads files as data URLs via `FileReader.readAsDataURL`, strips the base64 prefix, calls `saveAttachment`, gets a real path back.

Files:
- `internal/ipc/protocol.go`: `CmdSaveAttachment` constant + `Request.Data` field + `Response.Path` field
- `internal/ipc/server.go`: `handleSaveAttachment` function (base64 decode, filepath.Base path traversal guard, MkdirAll, timestamp prefix)
- `gui/src-tauri/src/lib.rs`: `save_attachment` Tauri command + `invoke_handler` entry
- `gui/src/api.ts`: `saveAttachment()` function + `SaveAttachmentResponse` interface
- `gui/src/components/ChatSurface.tsx`: `handlePaste` rewritten to use `FileReader.readAsDataURL` → `saveAttachment` → real path

### Commit 1e0001c (Docs cleanup)

2 files changed, 95 insertions, 5 deletions.

- README.md: commit count 147→162, LOC 27K→29K, E2E count 44→43, added 6 milestone rows (PR1-3, Diff Line Numbers, P2 A11y, Clipboard Paste Fix), rewrote Planned section (DEFER + WONTFIX with reasons)
- memory/log.md: appended M8-M11 + PR1-3 + diff/P2/A1 entries (was stuck at M7, HEAD c7bd684)

## 2. Review Criteria

**RC1: save_attachment daemon handler correctness**
- `handleSaveAttachment` validates name and data are non-empty
- `filepath.Base(req.Name)` prevents path traversal (strips directory components)
- `os.MkdirAll(attachDir, 0o755)` creates `.odo/attachments/` if needed
- Timestamp prefix (`<millis>-<name>`) prevents collisions
- `base64.StdEncoding.DecodeString` decodes the data
- `os.WriteFile(dest, data, 0o644)` writes the file
- Returns `Response{OK: true, Path: dest}` with the absolute path
- Verify: read the handler and confirm all steps are correct

**RC2: Protocol additions**
- `CmdSaveAttachment = "save_attachment"` constant added
- `Request.Data string` field with json tag `"data,omitempty"`
- `Response.Path string` field with json tag `"path,omitempty"`
- Dispatch switch has `case CmdSaveAttachment:`
- Verify: read protocol.go and server.go dispatch

**RC3: Rust bridge**
- `save_attachment` Tauri command with `name`, `data`, `project_root` params
- Forwards to daemon as JSON with `cmd: "save_attachment"`
- Registered in `invoke_handler` list
- Verify: read lib.rs and confirm the command and registration

**RC4: TS API function**
- `saveAttachment(name, data, projectRoot?)` returns `Promise<SaveAttachmentResponse>`
- `SaveAttachmentResponse` has `ok`, `error?`, `path?` fields
- Uses `invoke` with correct command name `"save_attachment"`
- Verify: read api.ts

**RC5: ChatSurface paste handler**
- `handlePaste` is now `async`
- Reads each file via `FileReader.readAsDataURL` → gets data URL
- Strips `data:...;base64,` prefix → raw base64
- Calls `saveAttachment(file.name, base64)` → gets real path
- Adds real path to attachments (not just filename)
- Error handling: catch block skips failed files silently
- Verify: read ChatSurface.tsx handlePaste

**RC6: Drag-drop unchanged**
- Tauri drag-drop event handler still uses `p.paths` directly (real paths)
- HTML5 drag-drop fallback still uses `f.name` (browser dev mode only)
- Verify: confirm drag-drop paths are not affected

**RC7: E2E compatibility**
- 43/43 E2E tests pass
- No E2E test asserts on paste behavior (paste is hard to test in Playwright)
- Verify: check if any E2E test could be affected

**RC8: Docs accuracy**
- README commit count and LOC are approximately correct (verify with git log --oneline | wc -l)
- memory/log.md entries match actual commits (verify a few commit hashes)
- Planned section reflects tri-model evaluation conclusions
- WONTFIX items have reasons
- Verify: spot-check accuracy

## 3. Instructions

1. Read the changed files in the live repo.
2. For each RC1–RC8, independently verify.
3. Check the daemon handler for security issues (path traversal, base64 injection).
4. Verify the paste handler correctly strips the data URL prefix.
5. Confirm docs match reality.
6. Give ACCEPT or REJECT per criterion, then an overall verdict.

Write your complete analysis as text. Do NOT write files to the repository.
