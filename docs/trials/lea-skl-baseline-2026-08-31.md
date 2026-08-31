# Odo self-learning trials baseline (2026-08-31, day 0)

Post quad-model architecture audit (8/8 legs, 4/4 convergent on both trials).
Parse fix 31198ff landed 2026-08-31 22:43; trials re-enabled on fresh evidence.

| Metric | Day-0 baseline |
|---|---|
| memory_propose events | 39 |
| learning_candidate events | 1 |
| skill-gate events | 0 |
| distill events | 97 |

Trial success criteria (evaluate after ~10 auto-distill epochs or 2-3 days):
- LEA-1: >=1 candidate reaching shadow stage (past replay gate) OR confirmed-zero
  with clean post-fix verdicts (either way the data is now trustworthy)
- SKL-1: skill_gate rows appear (proposals generated); landing rate measured from
  accept/discard splits; unanimous-reject auto-discard observed working
- Revert condition: pure spend with zero proposals after 10 epochs → back to off
