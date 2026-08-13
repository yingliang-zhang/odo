# DeepSeek Harness (repo `/tmp/harness-src-dsf/deepseek-harness`) — Web GUI Scout Report

**Repo shape**: TypeScript pnpm monorepo; a Cordis-plugin composition (`@deepseek-ai/cordis`, Koishi-family DI/plugin kernel — vendored, `vendor/cordis`). Client packages are `packages/client/*`, host packages `packages/host/*`, apps under `apps/`. One `dsh` binary dispatches everything (`apps/cli/src/bin.ts`). Web UI only — **no TUI** anywhere in the client tree.

---

## Q1 — Precise questions

### Q1.1 UI tech / framework
- **React 18** (`react: ^18.2.0`, `react-dom` via `createRoot` in `boot.tsx`), **TypeScript** strict-mode with `exactOptionalPropertyTypes`; CSS Modules (`*.module.css`); `use-sync-external-store@1.2.0` is the reactive bridge into host observables. Evidence: `packages/client/web-react/package.json`; `packages/client/web/src/boot.tsx` (`createRoot(this.el)`); `AppRoot.tsx` (`useSyncExternalStore`).
- **Not plain React architecture — it is a Cordova plugin tree in the browser.** The shell boots a cordis `Context`, mounts a vendored `Loader`, and every UI feature is a `dsh.client` plugin entry (`packages/client/web/src/boot.tsx`). The React glue (`packages/client/web-react/src/scoped-slots.tsx`) renders *declarative slots*: components register into `slot` keys (`root`, `sidebar`, `conversation`, `details`, `conversation.composer`, `conversation.input.dock`, …), `renderSlot()` (one ctx-level call; `packages/client/web/src/app.tsx` does `ctx.slots.renderSlot('root', {})`) composes them, entry scopes gate children, and injections wire services into components as props. No one monolithic React tree — a pluginized component architecture with an error boundary per entry (`SlotOwnershipError`, `StaleAuthorizationError`).

### Q1.2 How the UI process talks to the agent
- **Not same-process. The UI is a capability client over a defined RPC protocol; one `dsh` host process serves all sessions; many clients attach.**
- **Transport (Web):** unary-client-request / respond on **HTTP `POST /api/<method>`**; downlink **WebSocket** (one socket per stream: `/api/events.mux` → `MuxFrame`, `/api/events.host` → `HostFrame`). Evidence: `.agents/notes/implemented/architecture/2026-07-19-gui-layering-and-rpc-protocol.md`; `2026-08-04-websocket-downlink-carrier.md`; `packages/client/connection/src/client/web-api-client.ts` (`WebApiClient extends AbstractApiClient` ≪fetch uplink + WS downlinks); `api-path.ts` (`/api/events.mux`)… 
- **Session authority:** host-side `ctx.sessions` (`packages/client/web/src/app.tsx` reads `ctx.get('sessions')`, `bindSnapshotSelector(sessions.list)`). Sessions live in ONE host process (single `dsh web` server); each browser tab is a client attached to those streams. Multi-client is explicit: workspace delete “synchronizes other tabs” (`packages/client/runtime/src/client/sessions/manager.ts`).
- **Carrier independence:** the protocol is a closed 4-quadrant message model (`ClientRequest / ServerResponse / ServerRequest / ClientResponse`, all zod-validated, `RpcId` correlation). Transports: `InProcessApiClient` (isomorphic in-process fetch/SSE), `WebApiClient` (browser WebSocket), `FixtureApiClient` (`?fixture` serverless dev mode). Future Electron = doFetch-over-IPC subclass (documented; not implemented). Evidence: `apiproxy` `api/rpc.ts`, `api/rpc-map.ts`, layered note §Protocol.
- **Not over ACP for the primary UI.** ACP (`packages/acp`) is a separate acidic automation bridge where the protocol is exported for machine agents. UI is a real client-carrier consumer.

### Q1.3 Surfaces
- **Web app** (`apps/web`, `dsh-web-frontend`): browser UI served by `dsh web` (webserver + static dist).
- **apps/cli** (`@deepseek-ai/dsh`): mode dispatcher; `dsh web` = host + webserver + web UI dist; `dsh --profile headless` = **direct core Agent/Session entry point with zero Host/HTTP/ports** (headless surface; goes straight to core seams).
- **TUI: absent** (workspace has no terminal UI package; DiffBlock comments reference “the TUI's exact changed-row comparison” only as a legacy front end note).
- **Desktop/IDE: absent** (Electron reused but unimplemented; `dsh-client-plugin` tree is browser-only; no electron shell exists in repo).
- All of these attach to **one session authority** (the host `ctx.sessions`/`ctx.agents` service in the `dsh` process). Headless attaches to the agent directly (`Agent`/`Session` public seams), not over the UI RPC carrier.

### Q1.4 Headless vs UI parity
- **Headless-only capabilities:** run without any HTTP/client; orchestrates agents through public Agent/Session seams; can set approval policy `never` (deterministic rejects) — CI posture. Headless has no `?fixture` mode, no live frame replays.
- **UI-only:** visual token/message streaming (page), approval/question answer cards, plan review, diff cards, image/attachment pickup, background job dashboard, commands/search UI. UI can answer approvals; headless's `never` policy cannot.
- **Same protocol for the same live session log:** RPC streams are just a carrier for the same `session/event`s; the clock host implements the same contract regardless of surface.
- **UI can't** backup policy state the headless can (the UI only sets `ask`, headless may set `never`); and UI has no multi-process guarantee (client carrier is per-process; cross-process write sets don't exist — `feedback.md` documents that).

### Q1.5 State derivation
- **Nearly all UI *content* state is derived from the durable session log.** History = event replay over `session.history` pages (messages never split across pages). Live increments `session/event`s flow on the WS stream, folded client-side into the same transcript shape. Evidence: `packages/session-query/session-query` (exact reads); `session-projection.md` — per-session `ProjectionDefinition` units fold every committed event server-side; history tail pages carry `session/projection` push frames; persisted `session_projcache` + `coldStateSnapshot` for list views.
- **Ephemeral UI state** rides live frames, not the log: `pending` approvals/questions (`approval/requested`…`resolved`), the queue (queued user prompts), `pendingSteering`, job status (`jobsBySession`), input draft/undo, session title. `assistant/chunk` is the token stream — a live frame, folded into the same surface projection; compaction summary pages are *log events* that stay in the log.
- **Projector:** `useProjection(key)` reads host-computed projection values (`plan`, `goal`, `todos`, `permissions`, `contextPressure`, `contextBreakdown`, `imageLimits`) from per-session projection faces (Whole per-stream values, change feed on `assistant/chunk`…). UI-on-UI finalizes: `ui-layout`, `ui-sidebar`, `ui-conversation`, `trajectory` all derive from these projections — no client-side replay of the underlying events.

---

## Q2 — The 12 categories

### 1. Streaming rendering
**Verdict: Present (strong).** Evidence: `assistant/chunk` = token stream (apg-menu RPC note §frames); `ui-conversation/skeleton/chat/AssistantNodeView` (`data.status === 'running'`), `AssistantMarkdown` (receives `streaming:boolean`), `ReasoningRow` (`data-state='running'`, live `summary` follows end; `ChatView` `TurnStatus` live elapsed clock `role=status`; `ui-universal` `bot`-running shows `visual`. Interrupted turns render `interrupted` marker (`assistant.status: 'interrupted'`; `message.stopped` label).
- **HW:** stream chunks fold into the JSON chain `assistant/assistant` — clicks come as whole messages; the client subscribes to a `assistant/chunk` frame per push, cumulative `blocks` — WA state machine per turn; `status: running→settled→interrupted`.

### 2. Approval / permission UX **Present** — several planes:
- Composer-takeover **ApprovalPanel** (`ui-conversation/skeleton/ApprovalPanel.ts`): amber strip **“Waiting for approval”**, reason headline, paired executed command (from the running tool-call's args), `拒绝/Allow once` (one-shot buttons; disables after first click; removal frame-driven by `approval/resolved`). Dropdown selector routes `conversation.composer` entry to `ApprovalPanel` when a `PendingApproval` wait is active.
- **PermissionSelect** chip in composer bar: menu of permission presets with shield glyphs; selecting `danger-full-access` triggers a `RiskConfirmation` checkbox-gated modal first. Presets (write/read/never) submitted as `/permission <preset>` callback. Evidence: `ui-conversation/skeleton/PermissionSelect.tsx`. Tell also `ui-primitives/RiskConfirmation.tsx` (generic checkbox+confirm modal) reused by `PopupSelectView` and caes.
- **CordisPanel** (`extensions/ui-cordis`): plugin-run approval with `approveOnce` / `DoubleCheck approve all` / decline.

### 3. Interrupt & steering
- **Verdict: Present.** Cancel: input `stop()` → `conversation.cancel()` → `session.cancel` unary (`dots`, `inputActions.stop`); the UI has a stop action (the 