# UX Lock Amendment A4 — Multi-namespace K8s Jobs (user-ruled 2026-09-01)

Amends: ux-batch-lock-2026-09-01.md §D5 + A2 (a6b129e). Supersedes the
single-namespace scope of `k8s_namespace`. Status: LOCKED (quad 4/4 on
D1/D3/D4/D5, user tiebreak on D2; originals in docs/reviews/2026-09-01-multins/).

## Requirement

User's clusters run jobs in `default` (long-lived dev pods) AND `lab`
(GPU jobs). GUI must accept a namespace set and show jobs across all
of them at once.

## D1 — Input model: Option A + B's surface (4/4)

- ONE key: `k8s_namespace` stays a single prefs line; the VALUE becomes
  a comma-separated list (`default,lab`). `"lab"` ≡ `["lab"]` (one
  element), `""` = feature off. No new key, no array pref, no
  `k8s_namespaces` alias, no migration (D5: zero-migration is decisive —
  Option B's empty-array-can't-clear would trap the #140 write branch).
- Settings GUI renders it as an add/remove chips input that round-trips
  to the comma string (B's input surface, A's storage).
- Validation: each element RFC-1123 via existing `k8sNamespacePattern`;
  whole-value fail-loud (`bad_namespace` + visible reason), never
  silent drop of an invalid element. Cap N ≤ 5, enforced at the same
  validation point (fail-loud, not silent truncation).

## D2 — Execution: parallel per-ns forks, one shared 10s deadline (user ruling ②)

- `handleK8sStatus` fans N concurrent `kubectl get jobs,pods -n <ns>
  -o json` forks from ONE shared `context.WithTimeout(10s)`. Deadline
  kill classifies every unfinished ns as `timeout` via existing
  `k8sClassify`.
- Ground truth enabling this (GLM, verified in code): k8s.go holds no
  locks; each GUI invoke dials a fresh connection served by its own
  goroutine — no daemon serialization.
- `--all-namespaces` is REJECTED (4/4): cluster-scope list RBAC blast
  radius, drags every tenant's JSON, collapses failure granularity.

## D3 — Chip semantics: no third state (4/4)

- Badge = summed active jobs across ANSWERING namespaces.
- Popover carries per-namespace rows; a failed/RBAC-denied ns shows as
  a visible degraded row with its reason (partial availability is a
  healthy chip + degraded rows — the locked off/on-broken contract
  expressed per-namespace, not a new state).
- Total unavailability (all ns down / kubectl broken) → existing
  broken chip with reason.

## D4 — Jobs tab: flat table + namespace column (4/4)

- A2-5's locked two stacked sections stay (Jobs table + Batches); the
  Jobs table gains a leading `namespace` column, NO per-ns section
  headers (they break cross-ns sort).
- Default sort: configured namespace order, then age within group.
- In-tab quick-switcher = client-side FILTER within the configured set
  only; it never triggers IPC or widens what the daemon fetches.
- Namespace discovery (pick-list from live cluster): deferred, not in
  D5b. Free-text/chips input only.

## D5 — Migration: none (4/4, automatic under Option A)

`"lab"` parses to `["lab"]`; `""` stays off. #140's UpdateSettings
write branch unchanged.

## D6 — Polling: visibility-gated 5s + REQUIRED in-flight guard (4/4 + GLM catch)

- `K8S_POLL_INTERVAL = 5_000` unchanged; visibility gate
  (`!off && !fold.hidden && docVisible`) unchanged.
- REQUIRED FIX (real defect, pre-existing): StatusBar's
  `setInterval(() => void fetchStatus())` has no in-flight guard — a
  slow cluster (>5s handler, now possible at N× hung forks ≤10s) stacks
  concurrent `k8s_status` calls. D5b must add skip-if-in-flight
  (and a pollNow refetch on popover open, DSF).

## Batch-bridge interplay

A2-4 batch rows inherit the same namespace grouping in the popover;
batch status files remain path-keyed (not ns-keyed) — unchanged.

## D5b scope delta

The D5b handoff (cache/d5b-handoff-prompt.md) is updated to implement
A4 together with the Jobs tab: multi-ns fan-out handler + chips input +
popover per-ns rows + in-flight guard + tab namespace column.
