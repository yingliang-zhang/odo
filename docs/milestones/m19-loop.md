# M19 — `/loop`: Daemon-Driven Audit Fixpoint + Task Pipeline

```
Pain:      Quality fixpoint loops (audit → fix → re-audit until clean) and
           sequential task pipelines (design → implement → review per task)
           are driven BY HAND today — the user re-dispatches reviews,
           re-pastes findings, and babysits every round.
Demo:      (1) Land feature work in a workstream, type `/loop audit`
           → LoopChip advances audit/fix rounds → each fix lands as a
           commit → loop completes with a clean verdict → desktop
           notification fires.
           (2) Type `/loop tasks 1. … 2. …` → per task a DESIGN LOCK posts
           → approve → implement → auto-land pipeline (verify → panel →
           revise ladder) → next task → final audit over the accumulated
           diff → completes clean, notification fires.
           (3) Kill the daemon mid-loop, restart → loop either re-runs the
           (side-effect-free) audit round or suspends with
           `restart_mid_run`; `/loop resume` continues from the fold.
Not built: working-tree audit scope (`--tree`); cross-conversation loops;
           todo-store task source; auto-resume after stop/stall/protected
           path (`/loop resume` is always explicit); MCP/agent tool channel
           for audits; loop writes to memory layers; an
           accumulate-then-land-once Mode A (V6 locks land-each-round).
```

## Scope boundaries

In scope:

- **`/loop` daemon loop** per `docs/design/loop-design-lock.md` (v1.1, locked
  2026-08-18): pure journal fold, no supervisor, `loop_event{kind}` single
  discriminated event type, severity-gated fixpoint (P2+ blocking), fingerprint
  union without a consolidator model, BYOF verbatim findings, closure-pass
  prompt, stall detection, budget + 64KB subject byte breakers, one loop per
  conversation, human-send → suspend, restart recovery, land-each-round Mode A
  over `git diff base..HEAD`, Mode B verbatim `runDesignMoa` + `s.autoLand`.
- **Slash autocomplete UX**: typing `/` immediately opens the full command
  list (unfiltered), each entry with a one-line description, ↑/↓ + Tab/Enter
  selection, Esc dismissal, accepted command renders highlighted in the input.
- **`loop_notify_on_complete`**: GUI derives terminal loop kinds from the
  journal; first sighting fires a Tauri system notification; journaled
  `loop_notified` prevents repeats. Honest limit: notification requires the
  GUI open at completion time (daemon never talks to the OS notification
  service).
- **`loop_implementer` pref**: first-class override for loop-spawned
  fix/implement runs; default falls back to `coding:`.

Out of scope: see "Not built" above plus the non-goals list in the design
lock (persistent loop worktree, cross-conversation coordination, loop-fed
memory writes, GUI design-lock editor redesign).

## Review class

Journal + accept-path adjacency ⇒ independent fresh-context review before
landing. After landing, the repo dogfoods: run `/loop audit base=<m19_base>`
on the M19 diff itself to shake out the loop against its own code. The
self-audit is now possible — the squashed M19 impl commit measures 233,533B
of `git diff base..HEAD`, under the loop-owned 262,144B subject cap. The
dogfood pass criterion is that the loop audits its own code AT ALL — a fix
verdict driving convergence, or an honest subject/feed suspend with
actionable detail — NOT necessarily a clean verdict.
