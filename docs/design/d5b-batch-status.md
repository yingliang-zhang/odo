# D5b — Batch status.json contract (A2-4, LOCKED 2026-09-01)

The wire between batch scripts (writers) and the Odo daemon (reader).
Grounds: `docs/design/ux-batch-lock-amendment-a2.md` §A2-4,
`docs/reviews/2026-09-01-obs-arch/out.md` B4. Ownership split:

| side | owns | may never |
|---|---|---|
| batch scripts | WRITING status.json (atomic, heartbeating) | assume odo runs; assume a reader schema beyond v1 fields it set itself |
| odo daemon | READING (local mount first, pod cat fallback) | write anything under `k8s_batch_dir`; execute any pod verb but `cat`; journal batch data |

## Schema (schema_version: 1)

```json
{
  "schema_version": 1,
  "batch": "dsv-transcode",
  "stage": "transcode",
  "total": 250,
  "done": 180,
  "errs": 0,
  "rate_per_min": 5.3,
  "updated_unix": 1788271926,
  "status": "running"
}
```

| field | type | notes |
|---|---|---|
| schema_version | int | Gate. Writers pin 1. An old daemon + newer file ⇒ a visible `schema_mismatch` ROW, never garbage; a newer daemon + v1 file reads fine. |
| batch | string | Batch identity. Short stable slug (see vocabulary below). Falls back to the file name when empty. |
| stage | string | Current stage id. Vocabulary: `transcode` / `push` / `verify` style — short stable ids, NOT freeform prose (the GUI renders it verbatim). |
| total | int | Work items total. 0/absent ⇒ no progress bar (fraction unknown). |
| done | int | Completed items. May exceed total — the daemon clamps pct at 100. |
| errs | int | Error count; done rows surface it when > 0. |
| rate_per_min | float | Recent throughput. ≤ 0 ⇒ the GUI hides the ETA (a stalled rate inventing a time is a lie). |
| updated_unix | int64 | HEARTBEAT — refreshed every loop iteration or ≥ every 30s. Age > 90s ⇒ `stale:true` (a frozen file is UNKNOWN, never progress — B4 crash story). |
| status | string | `running` | `done` | `failed`. Terminal status distinguishes finished from abandoned; combined with a stale heartbeat a stale-"running" reads as crashed. |

## Write protocol (writer side)

```sh
tmp="$DIR/.status.$$.json"; printf '%s' "$JSON" > "$tmp" && mv "$tmp" "$DIR/status.json"
```

Atomic tmp+rename in the SAME directory (a reader either sees the old file
or the new file, never a torn write — mirrors prefs.md's persistence).
Heartbeat cadence: rewrite every loop iteration OR at least every 30s
whichever is sparser; the 90s staleness horizon assumes ≤30s cadence.

Reference pattern: the /cpfs/ylzhang dsv-transcode ledger loop (one status
file per batch, rewritten per ledger item) — this contract pins its wire,
the scripts themselves are NOT modified by odo.

## Read protocol (daemon side, `k8s_batch_status` IPC)

1. **Local mount first** (the designed path): `os.ReadFile` on every
   `*.json` directly under `k8s_batch_dir` (glob depth 1, no recursion) —
   the CPFS mount on the Mac, zero privilege.
2. **Pod fallback** (only when the local read fails AND k8s is
   configured): resolve the pod PER-READ via `kubectl get pods -l
   <k8s_job_selector>` across the configured namespaces (never a stored
   pod name — pods are ephemeral). 0 matches ⇒ `pod_not_found`; >1 ⇒
   `ambiguous_pod` (deterministic refusal, never a guess); empty selector
   ⇒ `no_pod_selector`. Exactly 1 ⇒ `kubectl exec <pod> -- cat
   <dir>/status.json` — **cat is the ONLY whitelisted exec verb**, which
   also bounds the fallback to the single canonical status file
   (directory listing inside a pod stays impossible by design; the
   multi-row enumeration is the CPFS mount's job).

Degradation contract (A2-1 at file granularity): a row may be missing its
DATA, its REASON may never be absent — `schema_mismatch`, `unparseable`,
`unreadable`, `pod_not_found`, `ambiguous_pod`, `no_pod_selector`. Rows
cap at 25 with `truncated:true`, sorted newest-first by heartbeat.

## RBAC honesty

`kubectl exec` (pods/exec) is a WRITE-CLASS RBAC verb even though our
invocation is a read-only `cat`: the same grant would allow `exec -- sh`.
Odo requests no new grants — it uses the operator's own kubeconfig. The
real threat model is accidental args on a single-user laptop, addressed
by argv whitelisting (argv-only exec, no shell; `exec <pod> -- cat <path>`
with the path derived from the settings value, never user-passed). The
local-mount-first priority means the exec path rarely fires in practice —
keep CPFS mounted on the Mac and kubectl never runs a pod verb at all.

## NEVER journaled

Batch states are external world state; the journal is replay context
(same containment as `k8s_status`). The GUI polls every 5s, visibility
gated, via the shared useK8sPoll fan.
