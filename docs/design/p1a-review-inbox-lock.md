# DESIGN LOCK: P1a — Cross-Workstream Pending-Review Inbox

> Tri-model MoA consolidation (K3/GLM/DSF, --thinking max, 540s, blind-sealed). 3/3 converged.

## Core design (3/3 convergence)

One new IPC command `list_all_pending_diffs` + one new GUI tab `"review"` in the existing ContextPanel. The daemon returns all pending diffs across all workstreams of the active project (single SQL JOIN, not Go iteration). The GUI renders them in an aggregate panel with accept/reject by diffID (already cross-workstream via `handleDiffAction`).

## IPC contract (3/3 convergence)

### New command: `list_all_pending_diffs`

**Request**: `{cmd: "list_all_pending_diffs", project_root: "<root>"}` — same shape as `pending_counts`.

**Response**: `Response{OK: true, AllPendingDiffs: []DiffInfoEx}` — new additive field on `Response`.

### `DiffInfoEx` struct (new, extends DiffInfo)

```go
type DiffInfoEx struct {
    DiffInfo                    // embedded: {ID, Status, Path, Content}
    ConversationID int64  `json:"conversation_id"`
    WorkstreamID   int64  `json:"workstream_id"`
    WorkstreamName string `json:"workstream_name"`
}
```

JSON flattens the embedding → `{id, status, path, content, workstream_name, conversation_id, workstream_id}`. Full diff content via `os.ReadFile` (same as `pendingDiffInfos`, server.go:3995). Unreadable file → `Content == ""` (row still actionable).

## Store query (3/3 convergence — single JOIN)

```go
type PendingDiffRow struct {
    store.Diff
    WorkstreamID   int64
    WorkstreamName string
}

func (s *Store) ListAllPendingDiffs(ctx context.Context, projectID int64) ([]PendingDiffRow, error)
```

```sql
SELECT d.id, d.conversation_id, d.path_on_disk, d.base_sha,
       d.worktree_path, d.status, d.created_at,
       w.id, w.name
FROM diffs d
JOIN conversations c ON d.conversation_id = c.id
JOIN workstreams w ON c.workstream_id = w.id
WHERE d.status = ? AND w.project_id = ? AND w.status = ?
ORDER BY w.id, d.id
```

- `w.status = 'active'` filter: excludes soft-deleted workstreams (defense-in-depth; `DeleteWorkstream` refuses when pending diffs exist)
- `ORDER BY w.id, d.id`: matches sidebar workstream ordering + per-conversation diff ordering
- Single query, not Go iteration (the JOIN already exists in `PendingDiffCountsByWorkstream`, diffs.go:148)
- Inbox SQL MUST reproduce `PendingDiffCountsByWorkstream` scope (join ALL conversations, not just active) — drift desyncs rows from sidebar pills

## IPC handler (3/3 convergence)

```go
func (s *Server) handleListAllPendingDiffs(ctx context.Context, req Request) (Response, error)
```

- `resolveProject(ctx, req.ProjectRoot)` → `s.store.ListAllPendingDiffs(ctx, p.ID)` → for each row: `os.ReadFile(d.PathOnDisk)` for content → build `DiffInfoEx` → return `Response{OK: true, AllPendingDiffs: items}`
- Read-only: no journal writes, no locks (mirrors `handlePendingCounts`)
- Dispatch: `case CmdListAllPendingDiffs: resp, err = s.handleListAllPendingDiffs(ctx, req)` beside `CmdPendingCounts`

## GUI panel (3/3 convergence — new tab in ContextPanel)

### New tab `"review"` in existing ContextPanel

- `PanelTab` gains `"review"`; icon: `Inbox` from lucide
- Badge: total pending count (sum of `pendingCounts` values)
- The `"changes"` tab stays untouched — it's per-conversation
- Add `"review"` to `VALID` array (App.tsx:137) — or localStorage-restored tab breaks

### `ReviewInbox.tsx` (new component)

- Groups rows by `workstream_name` (group header = ws name pill + "→ jump" button)
- Each row: workstream label, diff preview (first N lines, expandable to full `DiffViewer`), Accept/Reject buttons
- `expandedDiffId` state: single-expand accordion
- Accept/reject: calls existing `acceptDiff(diffId)` / `rejectDiff(diffId)` (cross-workstream, already works)
- After success: optimistically remove from `inboxDiffs` + `refreshPendingCounts()` (sidebar pills update immediately)
- `DiffViewer.onSendComments` made optional — inbox passes no comments handler (prevents commenting on wrong conversation's diff)

### Polling strategy (3/3 convergence — gate on visibility + count-change)

1. Immediate fetch when review tab becomes visible (extend existing `[panelOpen, panelTab]` effect)
2. Inside poll loop: gated on `panelTab === "review"` + `lastInboxFetch >= 6s` — bounds cost to visible surface
3. After any accept/reject from inbox: immediate `refreshInbox()` + `refreshPendingCounts()`
4. When `Σ pending_counts == 0`: skip IPC, clear items locally
5. Zero daemon load when tab hidden; sidebar pills already give project-wide freshness

## Files to touch

| File | Change |
|---|---|
| `internal/store/diffs.go` | `PendingDiffRow` struct + `ListAllPendingDiffs` method |
| `internal/ipc/protocol.go` | `CmdListAllPendingDiffs`, `DiffInfoEx`, `Response.AllPendingDiffs` |
| `internal/ipc/server.go` | dispatch case + `handleListAllPendingDiffs` |
| `internal/store/store_test.go` | `TestListAllPendingDiffs` |
| `internal/ipc/server_test.go` | `TestListPendingReviews` + orphan-conversation + empty project |
| `gui/src-tauri/src/lib.rs` | `list_all_pending_diffs` command + `generate_handler` registration |
| `gui/src/types.ts` | `DiffInfoEx`, `ListAllPendingDiffsResponse` |
| `gui/src/api.ts` | `listAllPendingDiffs` wrapper |
| `gui/src/components/ContextPanel.tsx` | `"review"` tab, badge |
| `gui/src/components/ReviewInbox.tsx` | **NEW** — grouped row list with accept/reject |
| `gui/src/components/DiffViewer.tsx` | `onSendComments` made optional |
| `gui/src/App.tsx` | `inboxDiffs` state, `refreshInbox`, handlers, render branch, `VALID` update |
| `gui/src/dev/mock-invoke.ts` | `list_all_pending_diffs` mock case |
| `gui/e2e/review-inbox.spec.ts` | **NEW** — E2E scenarios |

## Hard rules

1. **Single SQL JOIN** — no Go-loop over workstreams.
2. **`w.status = 'active'` filter** in SQL.
3. **Read-only handler** — no journal writes, no mutex.
4. **Gate content fetch on tab visibility** — never poll unconditionally.
5. **Add `"review"` to `VALID`** at App.tsx:137.
6. **`DiffInfoEx` embeds `DiffInfo`** — JSON flattening gives the right shape.
7. **Reuse `DiffViewer`** for expanded rows — do not build a second diff renderer.
8. **Optimistic update + `refreshPendingCounts()`** after inbox accept/reject.
9. **No git add/commit.** Touch only files listed above.

## Test names

**Store** (`internal/store/store_test.go`):
- `TestListAllPendingDiffs` — 2 workstreams × pending + accepted + foreign project; assert scope, order, labels

**IPC** (`internal/ipc/server_test.go`):
- `TestListPendingReviews` — 2 workstreams with diffs on disk; assert content, labels; accept non-active ws diff by diffID without switching
- `TestListPendingReviewsIncludesOrphanConversationDiff` — pending diff on pre-distill conversation; inbox surfaces it
- `TestListPendingReviewsEmptyProject` — fresh rig → empty, ok:true

**E2E** (`gui/e2e/review-inbox.spec.ts`):
- Open review tab → rows for both workstreams grouped by name
- Accept a non-active ws diff → row removed, sidebar pill decrements, no ws switch
- Reject path mirrored
- Empty-state copy after final row resolves

## Verification

```bash
go test ./internal/store/ -run TestListAllPendingDiffs -v
go test ./internal/ipc/ -run 'TestListPendingReviews' -v
go build ./... && go vet ./...
cd gui && npx tsc --noEmit
cd gui/src-tauri && cargo check
cd gui && npx playwright test e2e/review-inbox.spec.ts
```
