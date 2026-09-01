Working...
I inspected the ground truth before ruling: `internal/ipc/k8s.go` (single-ns handler, RFC1123 validation, 10s `CommandContext`, 1KB stderr cap, A2-3 row cap), `internal/adapter/settings.go:186-190` (raw string pass-through read) and `:254-265` (#140 write branch), plus both amendments (A2 degradation contract / settings shape, A3 strip budget). Verdicts:

## D1 — Input model

**ACCEPT Option A (with fixes).**

prefs.md is a flat line-based `key: value` format — there is no native array type, so Option B's "managed array pref" would have to be serialized into a string anyway, while adding a new key, new Settings rows, a Settings-chips UI, and a migration problem. Comma-separated solves all of it for free: RFC 1123 namespaces can never contain a comma, so splitting is unambiguous; existing `"lab"` reads as `["lab"]` and `""` stays off with zero migration; the #140 write branch (`settings.go:257-259`) is untouched because the key is unchanged. Split-brain (C) is worse than either — two scopes for one sensor invites drift.

**Rec:** Keep `k8s_namespace` as the sole SSOT. Daemon-side: split on `,`, trim, drop empty segments, validate each segment against the existing `k8sNamespacePattern` (one bad segment → `bad_namespace` for the whole set, fail loud before any exec), dedupe preserving order, cap at 3 (see D6). Update the handler comment at k8s.go:35 and A2-3 docs to say "comma-separated list; single value = unchanged behavior."

## D2 — kubectl strategy for N namespaces

**ACCEPT sequential per-ns calls, merged with per-namespace grouping. REJECT `--all-namespaces`. No parallelism.**

`--all-namespaces` requires cluster-scope list RBAC that today's per-ns user doesn't need, silently widens the blast radius when RBAC *is* granted, and turns a per-ns auth failure into a total failure — strictly worse on every axis for 2–3 namespaces. Parallel forks buy ~1s at N=2 and add concurrency to a daemon that is deliberately serial; not worth it. One real fix needed: today the 10s `CommandContext` bounds *one* call; N sequential calls must not become N×10s worst case. Put the existing `k8sTimeout` around the **whole fan-out** (overall deadline), not per call — worst case stays 10s regardless of N, and namespaces whose call didn't finish by the deadline return as `timeout`-class degraded rows while completed namespaces keep their data (partial results preserved, never discarded).

**Rec:** Loop the existing argv construction per namespace; one shared `context.WithTimeout(ctx, k8sTimeout)`; accumulate `[{ns, jobs, pods, truncated}, {ns, reason, detail}]`; per-ns failure never aborts the loop.

## D3 — Chip semantics across namespaces

**ACCEPT-WITH-FIXES: badge = summed active jobs; per-ns breakdown in popover; partial failure needs NO new state.**

The locked face is count-only, so the chip can only be a sum — there's nothing else it could honestly show. The A2-1 contract already covers partial failure without amendment: "data may be absent, the reason may never be absent" applies *per namespace*. A namespace with no data (RBAC-denied, timeout) renders in the popover as a degraded row carrying its cause class — exactly the same vocabulary as a fully-broken chip. All-namespaces-failed collapses to the existing `Jobs · unavailable` + reason state. Inventing a third chip state (e.g. "degraded") would add GUI surface for information the popover already carries.

**Rec:** Chip: `Jobs · N` where N = Σ active jobs across successfully fetched namespaces; if every namespace failed, N is unavailable and the chip dims with the aggregated reason. Popover: rows grouped by namespace, in configured order; each failed namespace gets a dimmed per-ns row `default — auth: …` with the capped stderr tail. No new Response schema state — the per-ns list *is* the grouping.

## D4 — Jobs tab display

**ACCEPT flat table with a namespace column, grouped-by-namespace default sort; transient in-tab filter; namespace discovery NOT required.**

A2-5 already locks the tab's two stacked sections; within the Jobs table, a namespace column with deterministic default grouping (configured order, then age within group) matches how this user thinks (dev vs GPU workloads) and costs nothing versus section headers. The distinction that matters: the Settings-set namespace list is the durable *scope* (what is fetched at all); an in-tab quick-switcher must be a client-side *filter within that set* — it must never trigger IPC or widen what the daemon fetches, or the "one truth per surface" rule gets a second source. Discovery (`kubectl get namespaces`) is a new read-only exec whose only value is saving the user from typing "default,lab" — they know their namespaces, validation errors surface honestly via `bad_namespace`, and it can be added later without breaking anything locked here. Keep it out.

**Rec:** Jobs table columns: name / namespace / phase / age / completions; default sort = configured-ns order then age; a lightweight namespace filter control (client-side only, defaults to all configured); batches section unchanged per A2-4/A2-5.

## D5 — Migration

**ACCEPT: no migration exists under Option A — and that is the decisive argument for A.**

Same key, same `""` = off sentinel, same validation site: an existing `k8s_namespace="lab"` is already a valid one-element list, and `""` is already off. Nothing in `settings.go:186-190` (read) or `:257-259` (#140 write branch) changes by even one character. Option B would have required an alias-read-then-migrate path threading through exactly the code #140 just landed — pure regression risk for zero user-visible gain. One caveat to document: a user who *semantically* wanted multi-ns but previously hand-edited the key to something the old code couldn't parse gains nothing broken — the new parse is a strict superset of the old accept-set (single valid label = single-element list).

**Rec:** Zero code in the migration dimension; add one line to the A2-3 docs: "`k8s_namespace` now accepts a comma-separated list; existing single-namespace values are unchanged and valid."

## D6 — Poll cadence and the N bound

**ACCEPT-WITH-FIXES: visibility-gated 5s stays; cap N at 3; enforce the cap by fail-loud rejection, not silent truncation.**

At ~0.8s per call, N=3 ≈ 2.4s inside a 5s tick with headroom, and the shared 10s deadline (D2) bounds the worst case. The cap itself must be honest: the codebase's posture is "fail loud, not exec" (k8s.go:31-35 comment) and the canonical rule forbids silently dropping data — "showing first 3" would make the chip's sum a lie (jobs in ns 4+ would be invisible with no reason anywhere). Truncating the *namespace set* is different in kind from the A2-3 row cap: rows are declared truncated, a silently-dropped namespace isn't. So N>3 → the whole sensor answers `unavailable` with reason `too_many_namespaces` — visible dimmed chip, reason in popover, zero execs. No popover-open fan-out needed: while the popover is open the chip is visible, so the 5s poll is already running; there's nothing a one-shot burst would add.

**Rec:** Cap = 3, enforced daemon-side before any exec with a distinct reason class; declare the cap in A2-3 docs and the popover copy. Optionally poke `pollNow` on popover open for freshness, but it's a nicety, not a lock condition.

---

## Design summary I would lock

`k8s_namespace` becomes a comma-separated list (single value = fully back-compatible, `""` = off, no migration, #140 write branch untouched); the daemon splits, validates each element against the existing RFC 1123 pattern, dedupes, and caps at 3 — over-cap or any invalid element fails loud with `too_many_namespaces` / `bad_namespace` before any exec. Fetch is N sequential per-namespace `kubectl get jobs,pods -n <ns>` calls under one shared 10s overall deadline, never `--all-namespaces`, never parallel; results merge into a per-namespace grouped response where a failed namespace carries its existing cause class and finished namespaces keep their data. The chip face stays a count-only sum of active jobs, dimming to `unavailable` only when *every* namespace fails; the popover shows per-namespace breakdown with dimmed reason rows for failed ones — no new chip state, the A2-1 contract holds per namespace unchanged. The D5b Jobs tab renders one flat table (name/namespace/phase/age/completions) grouped by configured namespace order plus the A2-5 batches section; any in-tab namespace switcher is a client-side filter within the Settings-defined scope, never a second fetch path; namespace discovery is explicitly deferred.
