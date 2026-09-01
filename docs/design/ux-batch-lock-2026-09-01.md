# UX Batch Lock — TopBar alignment, Tasks tab, background tasks, K8s observability

Date: 2026-09-01. Quad-blind review: K3/GLM/DSF/Sol = 4/4 ACCEPT_WITH_FIXES,
all fixes merged. User requirements R1-R4 verbatim in the review brief
(cache/odo-gui-ux-review-brief.md); leg outputs archived in
docs/reviews/2026-09-01-ux-batch/ (copied from /tmp/odo-ux-review).

## D1 — R1 TopBar right alignment (S)
`.topbar-actions` `margin-left: 16px → auto` (app.css:436). NOT
`justify-content: space-between` (4/4 — it would spread the 7 header
children apart). Collateral (GLM-only find, P1): pin popover must flip
`.topbar-pin-popover { left:0 → right:0 }` + `.topbar-pin-error
{ left:8px → right:8px }` — cluster now parks at the right edge, a
left-anchored ~260px popover would overflow the viewport. e2e geometry:
no assertions break (boot/panel/sidebar specs assert visibility/labels
only).

## D2 — Tasks tab (M): journal SSOT, zero new IPC
- Read path (a) 4/4: `deriveTodoState(events)` — the SAME exported fn
  PlanChip uses. Lift the memo from ChatSurface.tsx:572 to App; one
  derive, two consumers (chat chip + panel tab). Zero new IPC; adoption
  lock "journal is the cache" holds (RunsPanel precedent).
- Registry entry (contrib.ts), inserted FIRST:
  `{ id: "tasks", title: "Tasks", icon: ListChecks, badge: i => positive(i.openTodos) }`
  — `PanelBadgeInput` gains `openTodos: number`.
- Default tab flips to "tasks" (App.tsx:349); persisted selections stay
  valid via PANEL_TAB_IDS derivation. Changes tab STAYS (accept/reject
  IPC flows through it; deprioritized by position only).
- Extract shared `TodoList` from PlanChip.tsx:62-188 (TodoGlyph + row +
  mutation runner) consumed by both chip and TasksPanel — one view
  convention, drift structurally impossible. TasksPanel adds what the
  chip truncates: full text, stale/swept sections, add ops.
- MANDATORY before merge: recalibrate gui/e2e/context-panel-tabs.spec.ts
  for the 10th tab (W3 bug class: 616px strip vs 579px clientWidth;
  DSF+Sol P1) + vitest contrib/tab tests.

## D3 — Real-time task progress: no change (4/4)
Poll transport is the M0 lock (App.tsx:77-79: "no SSE/WebSocket").
350ms running / 1500ms idle + pollNow poke on todo ops = real-time.
No push channel. Ever, under this lock.

## D4 — Background tasks (R3): scope decision (4/4)
(a) cross-workstream agent runs: DONE (StatusBar bgNotice chip +
popover + RunsPanel). (b) generic daemon exec'd commands: DEFER — no
user story requires it; daemon execs exactly one thing (omp wrapper,
adapter/omp.go:322). (c) Hermes-side: out of purview. No new bottom-bar
dock. All R3 budget → K8s (the user's actual pain).

## D5 — K8s observability (R4), staged (L)
Stage 0 (M): daemon `k8s_status` one-shot IPC — `kubectl get jobs,pods
-n <ns> -o json` per call, exec.Command direct (no shell, same posture
as omp.go:322), read-only get ONLY (apply/delete/logs are escalate-class,
arch-priority brief already says so). Namespace + label selector from
settings (`k8s_namespace:"lab"`, `k8s_job_selector:""`), validate
namespace charset daemon-side. Data path: polled IPC, NEVER journal
(OdoEvent requires conversation_id; journal is replay context — cluster
state would pollute both; pending_counts is the precedent surface).
Bridge: mirror pending_counts exactly (protocol.go cmd + server.go
handler + lib.rs tauri command + api.ts fn). GUI: StatusBar chip
"Jobs · N" + popover rows (name/phase/age/completions), poll 5s ONLY
while visible (refreshInbox visibility-gate precedent), kubectl-absent
degrades to hidden chip. MVP filter: our label-selected jobs only.
Stage 1 (M): panel tab `{ id:"jobs", title:"Jobs", icon: Boxes }` —
full table + row click → `kubectl logs --tail=200` (read-only).
Stage 2: `-w` watcher only if 5s cadence proves insufficient (never MVP).
Risk noted: KUBECONFIG must reach the GUI-spawned daemon (document).

## Sequencing
UX-1 (GUI-only, dispatch NOW): D1 + D2 (incl. tab-strip recalibration).
UX-2 (daemon+GUI): D5 Stage 0. UX-3: D5 Stage 1. D3/D4 need no code.

## Single most important thing (4/4 converged, verbatim theme)
One truth per surface: Tasks reads ONLY deriveTodoState(journal); K8s
reads ONLY the one new read-only IPC. External world state NEVER enters
the journal; agent state NEVER gets a second source. No push channels.
