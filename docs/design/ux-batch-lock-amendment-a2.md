# UX Batch Lock Amendment A2 — UX-2/D5b architecture fixes + UX-3 observability batch (2026-09-01)

Quad-blind review: K3/GLM/DSF/Sol = 4/4 ACCEPT_WITH_FIXES, all fixes
below merged. Leg outputs archived in docs/reviews/2026-09-01-obs-arch/
(copied from /tmp/odo-obs-review). Amends docs/design/ux-batch-lock-2026-09-01.md §D5.

## A2-1 — Degradation contract (4/4, supersedes D5's "hidden chip")
A configured sensor never fails silently. Two states, distinct behaviors:
- off-by-config (k8s_namespace empty) → NO chip, no tab, no polling.
- on-but-broken (kubectl missing / KUBECONFIG unreachable / cluster
  error) → VISIBLE dimmed `Jobs · unavailable` chip + reason in popover
  (cause class: ENOENT / timeout / auth / unreachable). Precedent:
  OmpUsageChip (StatusBar.tsx:673-693) — the lock's original "hide on
  kubectl-absent" clause is REPEALED.
Canonical rule (verbatim from 4 legs, applies to ALL future sensors):
data may be absent, the reason may never be absent.

## A2-2 — Env chain (K3+GLM+Sol)
- kubectl exec uses EnrichedEnv() (omp.go:139-176) so homebrew PATH
  reaches Finder-launched daemons — do not regress.
- Add KUBECONFIG extraction to enrichDaemonEnv (main.go:288-317 pattern,
  same as SUDO_CODING_KEY scrape) — 3 lines, zero new mechanism.
- Exec posture mirrors runOmpJSON (omp_usage.go:31-48): 10s
  CommandContext, argv-only (no shell), 1KB stderr cap, errors array
  soft-degrade, NEVER journaled. THE precedent for k8s_status (the lock
  mistakenly cited pending_counts — corrected).

## A2-3 — Settings shape (4/4: settings-gated, default OFF)
- k8s_namespace: "" (default; empty = feature off — B3 verdict)
- k8s_context: "" (empty = current-context; when set pass --context
  explicitly — prevents terminal context-switches silently re-aiming
  the chip)
- k8s_job_selector: "" (empty = all jobs in ns with a hard row cap 50 +
  declared truncation; non-empty = argv --selector passthrough)
- Settings are GLOBAL today (settings.go:64-70; prefs.md single file;
  zero per-project readers — verified). One ADR line: per-project
  override explicitly deferred as a non-goal.

## A2-4 — D5b batch bridge contract (amended per K3 B4 + Sol)
- status.json schema: {batch, stage, total, done, errs, rate_per_min,
  updated_unix (heartbeat, atomic tmp+rename), status:
  running|done|failed, schema_version:1}
- Read path PRIORITY: (1) CPFS/local mount read (os.ReadFile, zero
  privilege) when k8s_batch_dir is a local path; (2) fallback kubectl
  exec -- cat, argv-whitelisted path. Pod selection: never hardcoded;
  multi-match ⇒ deterministic refusal with reason.
- Staleness = heartbeat age >90s ⇒ grey "stale — last update T".
  Terminal status field distinguishes done from abandoned.
- RBAC honesty: pods/exec is write-class; the docs must say so and
  state the real threat model (accidental args on a single-user laptop,
  addressed by argv whitelisting).
- Path learned via settings key k8s_batch_dir (glob under it), owned by
  settings; schema contract pinned in docs/design/d5b-batch-status.md.

## A2-5 — GUI shape (K3 B5)
- ONE "Jobs" tab (id: jobs, icon: Container — NOT Boxes, which
  PanelChip already uses), two stacked sections: Jobs table (name/
  phase/age/completions) + Batches (true N/M progress bar + rate +
  ETA as derived annotation). Badge = active jobs + active batches.
- StatusBar chip stays count-only (`Jobs · N [+ n batches]`); progress
  bars NEVER in chip face (24px bar + fold-to-hidden = useless).
- Chip overflow rank 3 (actionable tier).

## A2-6 — UX-3 observability batch (from Part A, P1s + cheap P2s)
UX-3a (P1): background run failure notification — notifyRunDone fires
on agent_done only (App.tsx:430-433); extend to agent_error with a
distinct failure notification + bgNotice finished-flash must
distinguish error from ok (App.tsx:733-742 reads running-set
transitions only).
UX-3b (P1): journalRunAdvisory reuses agent_error type with odo:true
flag (runverdict.go:54-58) that NO gui/src reader consumes — daemon
advisories render as red agent errors. Fix: GUI reads the odo flag →
advisory styling (amber, not red); or daemon migrates to a distinct
type. Pick the smaller diff; add a regression test.
UX-3c (P2, ride-alongs): curator/auto_distill backoff line in
MemoryPanel (next_eligible_at already journaled); daemon_lifecycle
journal event (restart + boot-sweep summary) → transcript receipt.

## Sequencing (unchanged + UX-3 inserted)
UX-1 ✅ (cfb40fb). UX-2 = A2-1..A2-5 + A2-6a notification fix (same
App.tsx neighborhood). UX-3 = A2-6b/6c. D5b after UX-2 lands (needs
k8s_status infra).

## Single most important thing (4/4 verbatim convergence)
One truth per surface extends to failure: hidden-only-when-off,
visible-with-reason-when-broken. A sensor that can silently vanish is
a lie of omission.
