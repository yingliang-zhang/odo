# Odo Architecture Priority + Hermes Pattern Mining — Tri-Model Brief

## 1. Task

Two questions:
1. **Architecture priority**: Evaluate 4 architecture-level improvements for Odo. Which should be done NOW vs DEFER vs WONTFIX?
2. **Hermes pattern mining**: What skills/mechanisms/design patterns from Hermes Agent would be useful to port into Odo?

## 2. Architecture Candidates

### A1: Auto-approve boundaries (Hermes-style pre-auth/escalate)

**Current state**: Every diff requires manual Accept/Reject. There is no concept of "small changes auto-apply, large changes need approval."

**Hermes equivalent**: Hermes has `.hermes.md` with auto-approve/escalate boundaries:
- Auto-execute: code reads, edits, git operations on feature branches, running tests
- Escalate: kubectl apply, git merge to main, destructive operations

**Proposed for Odo**: 
- Config in `.odo/prefs.md` or settings: `auto_apply: false | minor | all`
- "minor" = diff < N lines changed, no new files, no deletions
- Auto-applied diffs still journal a `review_action{auto_accept}` event
- Large diffs still require manual Accept
- User can see auto-applied diffs in the panel and revert

**Questions**: Is this worth doing NOW (dogfooding efficiency)? What are the risks? What's the right threshold?

### A2: Mid-run steering via --resume (D11)

**Current state**: `steering.txt` is a dead path — the OMP wrapper doesn't read it. User follow-up messages during a running agent are journaled but never reach the OMP process.

**Proposed**: Use OMP's `--resume <UUID>` to implement true mid-run steering:
- When user sends a message while agent is running, queue it
- When the current OMP run completes, automatically start a new run with `--resume <session-uuid>` that includes the follow-up
- This gives the agent full conversation context across runs

**Questions**: Is this a real pain point for dogfooding? How complex is the implementation? Does OMP's --resume work reliably?

### A3: OMP session resume (cross-run memory)

**Current state**: Every OMP run starts a fresh session. OMP's hindsight (agent.db SQLite) accumulates across sessions but Odo can't control or see what it injects.

**Proposed**: Store OMP session UUIDs in the journal. On new runs in the same conversation, pass `--resume <last-uuid>` to give the agent continuity.

**Questions**: Does this overlap with A2? Is this premature? What's the interaction with Odo's own memory layers?

### A4: MoA aggregator layer

**Current state**: MoA is proposer-only — fan-out the same prompt to N OMP instances, journal each result independently. No aggregator synthesizes the N proposals into one.

**Proposed**: Add a lightweight aggregator in the Go daemon:
- After all N fan-out results land, call a model (e.g., GLM-5.2) via HTTP `/v1/chat/completions` with all results + a "synthesize" prompt
- Journal the aggregated result as a separate `review_action{aggregated}` event
- Display in the diff viewer as a synthesized verdict

**Questions**: Is this worth building? Does it add enough value over reading N independent verdicts manually?

## 3. Hermes Pattern Mining

Hermes Agent (https://hermes-agent.nousresearch.com/docs) is a desktop AI agent with a different architecture from Odo. Key areas to evaluate:

### Mechanisms to evaluate for porting:

1. **`.hermes.md` auto-approve boundaries** — layered permission model (auto-execute / escalate / ask). How should Odo adapt this for diff review?

2. **Tri-model MoA workflow** — Hermes dispatches 3 independent OMP runs (K3/GLM/DSF) for review/audit tasks, each with `--thinking max`, then the orchestrator consolidates. Odo's `/panel` does fan-out but no consolidation. Worth adding?

3. **Skill system** — Hermes has curated skills (procedural memory) with SKILL.md frontmatter + linked files. Odo has skills (M8) but no distillation→gate→promote pipeline. What's missing?

4. **Context compression** — Hermes compresses conversation context at thresholds to prevent token overflow. Odo has no context management. Is this needed for long conversations?

5. **Session persistence/resume** — Hermes persists sessions to SQLite and can resume by session ID. Odo has SQLite journal but no resume. How does this interact with A2/A3?

6. **Cron/background tasks** — Hermes has cron jobs and background processes. Odo is a desktop app (open/work/close model). Is this relevant?

7. **Delegate/subagent** — Hermes can spawn subagents for parallel work. Odo's OMP has its own subagent capability. How should Odo expose this?

8. **Memory partitioning** — Hermes has MEMORY.md + USER.md + Hindsight + Basic Memory (4 layers). Odo has 6 memory layers (journal → epoch notes → topics → memory.md → user.md → ledger.md). Are there Hermes memory patterns worth adopting?

9. **Planning files workflow** — Hermes has `planning-files-workflow` skill for durable agent planning files (progress.md, findings.md, experiment-ledger.md). Should Odo adopt something similar?

10. **Audit-review-loop** — Hermes has `audit-review-loop` skill for automated two-layer nested audit→review. Should Odo adopt this for its own tri-model reviews?

## 4. Constraints

- macOS only (Tauri webview)
- OMP CLI is the only model transport (no direct API, except /vision which uses HTTP)
- Single CSS file architecture
- Go daemon is the single source of truth (Invariant 1)
- Every feature must trace to a pain point
- Odo is a single-user desktop app, not a server
- Apple HIG design language

## 5. Questions for Reviewers

**Q1**: For each A1-A4, give NOW / DEFER / WONTFIX with reasoning.
**Q2**: For each Hermes pattern 1-10, give ADOPT (port to Odo) / ADAPT (modify for Odo) / SKIP (not relevant) with reasoning.
**Q3**: Are there Hermes patterns not listed that would be valuable for Odo?
**Q4**: What's the recommended implementation order for the NOW/ADOPT items?
**Q5**: Is auto-approve (A1) the highest-impact item for dogfooding efficiency, or is there something more urgent?

Read the Odo repo (`/Users/yingliangzhang/Projects/odo`) and the Hermes skills directory (`~/.hermes/profiles/orchestrator/skills/`) to ground your analysis. Write your complete analysis as text. Do NOT write files to the repository.
