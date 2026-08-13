# Frozen Brief — Tri-Model Comparative Audit: deepseek-harness vs grok-build vs openai/codex (for Odo feature mining)

## 1. Context

You are one of three independent analysts comparing three open-source coding-agent
harnesses. Your output will be consolidated with two other models' analyses by an
orchestrator. Be evidence-grounded: every claim must cite a file path, doc section,
or GitHub signal you actually inspected.

**The three repos (metadata verified via GitHub API on 2026-08-13):**

| Repo | Lang | License | Stars | Created | Description |
|---|---|---|---|---|---|
| `deepseek-ai/deepseek-harness` | TypeScript | MIT | ~17.8k | 2026-08-13 (TODAY) | (none — empty description) |
| `xai-org/grok-build` | Rust | Apache-2.0 | ~25k | 2026-07-14 | "SpaceXAI's coding agent harness and TUI. Fullscreen, mouse interactive, extensible." |
| `openai/codex` | Rust | Apache-2.0 | ~105k | 2025-04-13 | "Lightweight coding agent that runs in your terminal" |

**Why:** we build **Odo** (`~/Projects/odo`), a personal Research Coding OS —
Tauri 2 WebView + React GUI + single Go daemon (no Electron). Pillars: memory
(per-project wiki + journal), skills (CRUD + distillation + three-tier gating),
orchestrator (MoA fan-out, diff review with Accept/Reject lanes, settlement ladder,
auto-revise). Models are a commodity; MoA comparison is default-on. Odo's vision is
long uninterrupted autonomy: a gating ladder replaces human diff review, and
asynchronous "parked" reviews replace synchronous gates. M0–M15+ complete
(~29.6K Go + ~14K TS/CSS/Rust).

We want to know what these three harnesses do differently, how open they really
are, and which features are worth borrowing into Odo.

## 2. Current State (Odo anchor points — skim these to ground "worth learning")

- `~/Projects/odo/README.md` — milestone table M0–M15+, pain points, status.
- `~/Projects/odo/docs/` — `adr/`, `design/`, `milestones/`, `compare/` (prior external comparisons live here).
- `~/Projects/odo/internal/` — Go daemon (journal, recall, skills, adapters).
- `~/Projects/odo/gui/` — React frontend.

Skim enough of Odo to know what already exists (MoA fan-out, diff lanes, journal,
memory distiller, skills panel, settlement ladder). Do NOT re-audit Odo deeply —
use it only to make Q3 recommendations concrete (e.g., "X maps to Odo's adapter
layer at internal/adapter/…" with honest cost).

## 3. Repos to Audit (clone shallow, do not run code)

Clone all three with `git clone --depth 1` into a unique scratch dir under
`/tmp/harness-src-<your-identifier>/` (pick a unique identifier; dirs may be
created by parallel analysts):

- https://github.com/deepseek-ai/deepseek-harness
- https://github.com/xai-org/grok-build
- https://github.com/openai/codex

You may also use the GitHub API (`https://api.github.com/repos/<org>/<repo>/…`)
for governance signals: contributors, commit activity, issue/PR counts + response
velocity, whether discussions are enabled, org membership hints, recent commit
authors and their affiliations. Do NOT modify anything; this is read-only.

## 4. The Questions

### Q1 — Technical differences (per repo, evidence-cited)
Compare along these axes (where the repo has them):
- **Architecture**: language/runtime split, process model (daemon? single proc?),
  TUI/GUI tech, headless mode (non-interactive `-p`/`-exec`), structured output.
- **Agent loop**: how turns are driven, tool-call protocol, parallel tool calls,
  reasoning/effort controls, subagents/delegation if any.
- **Tool system**: built-in tools, sandboxing (seatbelt/landlock/docker?), file-edit
  mechanism (diff/AST/full-rewrite), shell execution model.
- **Permission/approval model**: auto-approve tiers, policy config, human gates.
- **Context & memory**: compaction, pinning, project docs loading, memory files.
- **Session management**: resume/fork, persistence format, multi-session.
- **Extensibility**: plugins/hooks/MCP/skills/slash commands; config surface.
- **Model neutrality**: first-party only, or multi-provider? BYOK?

Produce a comparison table with one column per repo, plus short prose on the
2–3 *most consequential* architectural divergences.

### Q2 — Openness assessment (per repo, evidence-cited)
- License + any extra terms (trademark policy, CLA check).
- Governance signals who actually controls direction: commit author distribution
  (org members vs external), top external contributors, PR merge pattern
  (rubber-stamp vs real review), how decisions get made (RFCs? discussions?).
- Contribution surface: CONTRIBUTING.md quality, good-first-issues, plugin API
  stability promises.
- Model openness: does the harness work with third-party providers, or is it a
  funnel to the vendor's own model/API? Rate each: **Open / Open-ish / Funnel**.
- Red flags: e.g. `deepseek-harness` has NO description and was created TODAY —
  is it a real engineering artifact or a snapshot dump? `grok-build` says
  "SpaceXAI" — check whether commit authors/org membership look official.

### Q3 — Features worth borrowing for Odo (ranked, honest about fit)
For each candidate feature (aim 6–12 total across all three repos):
1. **Feature name** — concise label.
2. **What it does** — 1–2 sentences.
3. **Evidence** — `<repo>:<path>` (and commit/line if useful).
4. **Which repo(s) have it** — and who does it best.
5. **Map to Odo** — which Odo layer it touches (Go daemon / gui / adapter /
   skills / memory) with the concrete anchor you skimmed in §2.
6. **Adoption cost** — S (<1 day) / M (1–3 days) / L (>3 days), one-line why.
7. **Fit risk** — why it might NOT fit Odo's philosophy (single researcher,
   lightweight, long autonomy, MoA default-on). Be adversarial here.
8. **Priority & confidence** — P0/P1/P2 + High/Med/Low confidence.

## 5. Constraints

- **Analysis only.** Do NOT implement anything in Odo. Do NOT modify any file
  under `~/Projects/odo`.
- Read-only on the cloned repos; do not run their installers or agents.
- Evidence standard: every factual claim about a repo cites a path you read or an
  API field you fetched. If the evidence is absent (e.g., no CONTRIBUTING.md),
  say "absent" explicitly — never infer or fill gaps.
- If a clone fails or a repo is unexpectedly tiny/empty, report that as a finding.
- Keep prose tight. Use tables over walls of text.

## 6. Output Format (required structure)

```
## A. Executive summary (≤10 lines)
## B. Q1 comparison table + key architectural divergences
## C. Q2 openness verdicts (per repo: Open / Open-ish / Funnel + evidence)
## D. Q3 ranked feature-borrow list for Odo (8 fields per item, per §4 Q3)
## E. Surprises / red flags
## F. What Odo already does BETTER than all three (honest, 2–5 items)
```

## FINAL-MESSAGE CONTRACT (flash variant)

Your reply may take many tool calls — that is expected. But the session's FINAL
assistant message must contain the COMPLETE structured deliverable (sections A–F).
Do NOT stop after dumping intermediate evidence notes; if you finish gathering,
keep writing inside the same turn until the full report is in the final message.
The final message must start with "## A. Executive summary".
