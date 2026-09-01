Working...
Ground truth read: `internal/ipc/k8s.go`, `internal/adapter/settings.go` (read 140–290), `internal/ipc/protocol.go` (K8sStatus at 528–549), `internal/ipc/server.go` (Serve 675–689), `gui/src-tauri/src/lib.rs` (round_trip 156–166, k8s_status 674–679), `StatusBar.tsx` JobsChip (821–978), `jobs.ts`, `contrib.ts`, `types.ts:709–716`, lock + A2 + A3 docs. Note: `cache/d5b-handoff-prompt.md` does not exist anywhere in the repo (tracked, ignored, or `.odo/`) — the D5b contract below is grounded in A2-5/A3-3 + the shipped chip instead.

---

## D1 — Input model

**Verdict: ACCEPT-WITH-FIXES — A's storage shape + B's GUI surface, on ONE key. Reject B's second pref and its deprecated alias; reject C.**

A as stated ("zero GUI-input surface") fails the requirement's operative clause — "the GUI must let the user input namespace(s)"; B's `k8s_namespaces` array buys exactly nothing semantically (a 1-element list and a scalar are isomorphic) while creating a permanent two-key divergence hazard, and its "keep `k8s_namespace` as deprecated alias" violates the project's own learned rule (`.odo/learning/candidates.jsonl`, epoch-52: "never keep aliases, thin shells, or dead mounts"). Comma-separation in a scalar is unambiguous here because the RFC 1123 namespace charset (`k8sNamespacePattern`, k8s.go:35) excludes commas, and prefs.md already carries a comma-separated scalar precedent — `review: model@provider, model@provider` (settings.go:22). Hand-editing stays natural (`k8s_namespace: default,lab`) instead of JSON-in-markdown. C is two truths for one surface — chip scope ≠ tab scope is precisely the drift the lock's "one truth per surface" forbids.

**Recommendation:** `k8s_namespace` stays the single SSIT, now a comma-separated list. Daemon parse: split on `,`, trim, every element must match `k8sNamespacePattern`; any empty/invalid element → `bad_namespace` with detail naming the offending element (fail loud, never silently drop); dedupe; N>5 → `bad_namespace` with the cap in detail. SettingsPanel gains a k8s section: chip editor (add/remove, client-side charset + 5-cap pre-validation) writing the joined string through the existing `updateSettings` path. #140's write branch (`set("k8s_namespace", up.K8sNamespace)`, settings.go:257) persists `"default,lab"` verbatim — zero changes, cannot regress.

## D2 — kubectl strategy

**Verdict: ACCEPT per-namespace PARALLEL calls inside one `k8s_status`, one shared 10s deadline, N≤5. REJECT `--all-namespaces`; REJECT sequential.**

`-A` requires cluster-scope list RBAC: one denied grant blanks every namespace at once and conflates per-ns causes (one List cannot say "lab fine, default forbidden"), plus it drags every other tenant's jobs across the wire — strictly worse blast radius than the ns-scoped gets the design deliberately chose. Sequential worst-case is N×10s (30s at N=3), which exceeds the 5s tick and stacks overlapping in-flight calls; parallel with a shared deadline keeps the handler's wall-time profile identical to today's single-ns (≤10s) while typical 2-ns latency is ~0.8s, not 1.6s. The "daemon serialization concern" is a non-issue: `handleK8sStatus` holds no locks (no `s.mu` anywhere in k8s.go), and each GUI invoke dials a fresh connection served by its own goroutine (lib.rs:156–158, M11 P0) — the N forks share no state.

**Recommendation:** One handler, N concurrent `kubectl get jobs,pods -n nsX -o json` forks, all derived from ONE shared 10s `context.WithTimeout` (deadline kill classifies every unfinished ns as `timeout` via the existing `k8sClassify`; per-ns forbidden-sniffing needs no changes). Per-fork stderr stays LimitReader-capped at 1KB at capture. `k8s_context` and `k8s_job_selector` remain single values applied uniformly — the ns set is one context's namespaces by construction; don't smuggle multi-context in. Cap N at 5, enforced at validation, never at fetch (see D6).

## D3 — Chip semantics

**Verdict: ACCEPT-WITH-FIXES — summed face, per-ns popover sections, per-ns reason rows; partial failure is NOT a new state (A2-1 at namespace granularity), but the face needs an explicit degraded marker.**

The locked contract already expresses partial failure — "data may be absent, the reason may never be absent" — as a visible per-ns degraded row (reason class + capped stderr tail, exactly today's broken-body shape). But a countable face with a silently missing namespace is the lie-of-omission shape the amendment's closing line names: the user reading `Jobs · 4` (default only) must be told lab is dark without clicking anything. No third chip state is needed for that — it's one dimmed suffix on the existing countable face, the same canonical rule applied one level down; only all-namespaces-broken collapses to today's `Jobs · unavailable`.

**Recommendation:** Face = `Jobs · Σtotal` over up-namespaces, `+` if ANY ns truncated, plus a dimmed `· Nns down` marker when any ns is broken; title attr names the broken ns + cause class. Popover: dim per-ns headers (`default · 3`, `lab · 5`), broken ns rendering its reason + detail row in place of rows. Badge/overflow summary = summed `activeJobCount` (truncation: any-ns OR). Write the partial-availability clause into this amendment verbatim so D5b inherits it: "a narrowed face without a marker is a silent sensor."

## D4 — Jobs tab display

**Verdict: ACCEPT-WITH-FIXES — flat table + namespace column (amends A2-5's locked column set); Settings-set is the ONLY durable scope; in-tab filter is client-only; discovery is an on-demand pick-list over free-text, never a hard dependency.**

Per-ns section headers inside a sortable table fight the sort; a leading ns column with default sort (ns ASC, creationTimestamp DESC) gives grouping visually at any N and keeps one table — A2-5's two stacked sections (Jobs table + Batches) survive intact, since `k8s_batch_dir` status is not ns-keyed. A transient in-tab ns filter never touches fetch scope — it filters the already-returned snapshot, zero IPC, the one-fetch-many-views posture (deriveTodoState precedent); making it durable would create a second scope truth. Namespace cardinality never multiplies surfaces: one chip, one tab, regardless of N (the A3-3 conditional 10th is unaffected). Discovery: `kubectl get namespaces -o json` is read-only-get-compatible, normally granted via system:discovery, and kills the typo class — but it is RBAC-gated, so it must degrade to free-text with a reason, never block input.

**Recommendation:** Columns: ns (dim, first) · name · phase · age · completions — one-line amendment to A2-5. Default sort ns ASC then newest-first, clickable headers. Scope edits live only in the Settings chip editor (D1); the tab's ns filter chips are unpersisted view state. Add a refresh button in the Settings editor firing a one-shot `kubectl get namespaces` discovery IPC (never polled, same exec posture, fail-loud to free-text on auth/unreachable). Free-text remains the always-works floor; daemon `bad_namespace` remains the typo backstop.

## D5 — Migration

**Verdict: ACCEPT — with D1's shape there is NO migration.**

`k8s_namespace: "lab"` parses to `["lab"]` and `""` stays off — every existing user's behavior is byte-identical by construction, no fallback branch, no alias, no rewrite. #140's write branch persists values verbatim (`settings.go:254–265`), so `"default,lab"` round-trips through `updateSettings` untouched — the migration can't regress what it never touches. Only the read side changes: the validation loop and the fan-out fetch. `jobs.ts:90`'s "k8s_namespace pref rejected" copy stays accurate; extend its detail to name the offending element.

**Recommendation:** No new key, no `RemovePrefsKey`, no read-fallback. Amendment ships one sentence: "k8s_namespace is a comma-separated namespace list; single-element values are the legacy single-ns config, unchanged." Any reviewer proposing a second key must show one behavior a scalar cannot express — none exists here.

## D6 — Poll cadence

**Verdict: ACCEPT-WITH-FIXES — keep visibility-gated 5s with N≤5; add an in-flight guard; REJECT silent "showing first N".**

Five parallel forks per 5s tick is ~1 query/sec against the apiserver and trivial Mac-side fork cost; the existing gate (`!off && !fold.hidden && docVisible`, StatusBar.tsx:881) already zeroes execs unseen — the epoch-51 subprocess-per-tick gating rule holds unchanged. The real hazard is overlap: the handler's 10s worst case already exceeds the 5s tick and `setInterval(() => void fetchStatus())` (StatusBar.tsx:885) has no in-flight guard, so a slow cluster stacks concurrent `k8s_status` calls, each now fanning N forks. Silent "showing first N namespaces" is the exact lie-of-omission A2-1 outlaws — a narrowed set with no reason; the cap belongs at validation (`bad_namespace`, visible), not at fetch.

**Recommendation:** Keep `K8S_POLL_INTERVAL = 5_000` and the visibility gate. Add an in-flight boolean to `fetchStatus` (skip the tick while a fetch is unresolved — kills overlap in one line). Shared 10s deadline (D2) bounds worst-case wall time. D5b's tab-open pokes one immediate refresh (the pollNow-on-todo-ops precedent) so the table is never 5s-stale on open; chip and tab share one fetch cycle — one truth per fetch, two surfaces. No popover-open fan-out beyond the normal tick: popover open adds nothing the visibility gate doesn't already cover.

---

## Design summary I would lock

`k8s_namespace` remains the single managed pref, now a comma-separated list (the `review:` precedent; charset excludes commas, so the parse is total), edited only through a new SettingsPanel chip editor with client-side RFC 1123 + 5-element validation and an optional one-shot `kubectl get namespaces` discovery button that degrades to free-text with a reason — daemon-side, `k8s_status` splits/validates the list fail-loud (bad element or >5 named in `bad_namespace` detail), then fans N concurrent argv-only `kubectl get jobs,pods -n X -o json` forks under one shared 10s deadline with per-fork 1KB stderr caps, answering a per-namespace array (namespace/reason/detail/jobs/pods/truncated each; wire shape changes once, chip and D5b tab consume the same payload); the chip face shows summed totals with `+` on any-ns truncation and a dimmed `· Nns down` marker when any namespace is broken, the popover renders per-ns sections with in-place reason rows (A2-1 applied at namespace granularity — no new top-level state, all-broken collapses to today's `Jobs · unavailable`), the D5b Jobs tab is one flat table (ns · name · phase · age · completions, default sort ns ASC then newest) above the global Batches section with an unpersisted client-side ns filter that never touches the Settings-owned scope; polling stays visibility-gated 5s with a new in-flight guard and a tab-open poke; existing `"lab"`/`""` configs are byte-identical single-element/off cases — no migration, no alias, no second key, and #140's write branch is untouched.
