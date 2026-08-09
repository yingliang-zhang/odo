# Odo user.md Initial Content — Tri-Model Brainstorm Brief

## 1. Task

Draft the initial content for `~/.odo/user.md` — the global user preference file injected into every Odo prompt. This is Odo's equivalent of Hermes's `USER.md`.

## 2. What is user.md?

In Odo's architecture (ADR-0003):
- `~/.odo/user.md` — global user preferences, injected into every run's prompt (≤4KB)
- `.odo/memory.md` — project-level behavior rules (per-project)
- `.odo/pins.md` — user-authored "never forget" rules
- The user can distill conversations → learner proposes rules → user approves → rules are written to memory.md or user.md

`user.md` is the **highest-authority** memory layer — it's always injected, across all projects. It should contain stable, cross-project user preferences that won't change frequently.

## 3. User Profile

The user (Yingliang Zhang) is:
- Sr Algorithm Scientist @ Sudo Robotics (2026–), Real2Sim & algo R&D
- Background: CV/3DGS; prev CTO/Board @ DGene (2016–2025)
- Learning π0.5 since 2025-07; multi-device robotics (EGO+UMI)
- Site: yingliang-zhang.github.io
- Works across Mac Studio (primary dev) + planned Linux (Ubuntu/Pop-OS)
- Handles API key/credential config manually — wants a clear checklist, not auto-install of secrets

## 4. Hermes USER.md (for reference)

Hermes's USER.md contains these entries (each is a §-separated paragraph):
- Free/auth-free services preferred
- Output: compact Chinese; Markdown tables; no text fences
- Scope: practical guarded MVPs over speculative v1 rewrites; DELETE rather than keep as rarely-used option
- Research/study: read all relevant files first, report comprehensively; map technique↔evidence; rank EV/cost/risk; first-principles route audit for 2+ unproven P0s
- External repos: follow official setup exactly; ModelScope for gated HF deps
- Upstream contributions: fix locally, search existing issues/PRs, submit independent fix PRs; never leak paths/secrets
- Critical operational rules in always-injected surfaces; detailed procedures in skills
- Reuse templates over greenfielding
- Experiments: autonomous after node+auth; binary A/B preferred; never compare mixed metric sources
- Development: user directs, models implement; first-principles mandatory; verify framing/terminology BEFORE coding; pre-auth vs needs-approval; rejects narrow framings; compare against current baseline not ideal
- Test FAILs must be root-caused and fixed, never dismissed
- User expects visible progress during long quiet tool-call sequences
- Coverage & data safety: audit ALL similar areas; never touch source data

## 5. Odo-Specific Context

- Odo is a Tauri 2 + React 18 + Go daemon desktop app (macOS only)
- Uses OMP CLI as the coding adapter (not direct API)
- Single-user app, open/work/close model (no cron, no 24/7)
- Every commit traces to a pain point (no speculative features)
- Apple HIG design language
- MoA tri-model review: 3 independent models, ≥2/3 ACCEPT to close
- The user dogfoods Odo (using Odo to develop Odo)
- Go daemon is the single source of truth (Invariant 1)
- Hand-synced IPC types (Go ↔ TS), no codegen

## 6. Questions for Reviewers

**Q1**: What should go into Odo's initial user.md? Draft the actual content.
**Q2**: What should NOT go into user.md (too volatile, too project-specific, better as memory.md or pins)?
**Q3**: What's the right size? (≤4KB cap — how much of that to use?)
**Q4**: Should user.md contain model-specific instructions (e.g., "K3 responds better to X") or only user preferences?
**Q5**: Any patterns from Hermes USER.md that should be adapted (not copied verbatim) for Odo's context?

Write your draft user.md content as a complete markdown file. Do NOT write files to the repository — output the content as text.
