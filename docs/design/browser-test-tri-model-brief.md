# Odo Browser Functional Test + Remaining Issues — Tri-Model Brief

## 1. Task

A browser-based functional test of Odo was conducted in dev mode (mock adapter). Review the test results + remaining issues and recommend what to fix before the user starts dogfooding.

## 2. Browser Test Results (dev mode, mock adapter)

| Feature | Status | Notes |
|---|---|---|
| App loads, UI renders | ✅ | Dark theme, 3-region layout (sidebar/chat/composer) |
| Sidebar: project list | ✅ | Shows odo + supersplat-hdr with workstreams |
| Sidebar: workstream tree | ✅ | Expand/collapse, "Idle"/"Pending review" status |
| Top bar: project/workstream label | ✅ | "Odo · main" |
| Top bar: Distill button + badge | ✅ | Shows badge "3" |
| Top bar: ⋯ overflow menu | ✅ | Present, not expanded |
| Top bar: Settings gear | ✅ | Opens settings dialog |
| Settings: General tab | ✅ | Theme (Dark/Light), OMP timeout (600), Max concurrent (3) |
| Settings: Models tab | ✅ | Coding=K3, Orchestrator=GLM-5.2, Review=3 models |
| Settings: Knowledge tab | ✅ | Present (not tested in detail) |
| ⌘K Command palette | ✅ | 9 commands listed (New Workstream, Distill, Curate, Pin, Wiki, Settings, Toggle Sidebar, Toggle Context Panel, Search Chat) |
| Slash command menu | ✅ | `/panel` and `/vision` shown on `/` input |
| Message input + Send | ✅ | Input enables Send button on text; mock agent responds with "Thinking…" |
| Chat history rendering | ✅ | Agent messages, tool call disclosure, green success chips, user bubbles |
| IME composition guard | ✅ (code) | `isComposing` check in both textarea and global ⌘+Enter handlers |
| Diff panel | ❓ | Not visible (no pending diff in this conversation) |

## 3. Remaining Issues Found During Real Use

### Issue 1: projects.json keeps getting repopulated
- User cleared projects.json to `[]` but odo project auto-registers on app launch
- Root cause: `default_project_root()` in lib.rs uses `CARGO_MANIFEST_DIR` (compile-time hardcoded path)
- This is BY DESIGN for dev convenience but the user wanted a fresh start
- **Impact**: cosmetic — user sees odo project they didn't add

### Issue 2: 401 auth error when sending messages from /Applications
- OMP's models.yml uses `SUDO_CODING_KEY` as literal env-var name in apiKey field
- When daemon launches from .app, it doesn't source ~/.zshrc, so SUDO_CODING_KEY is missing
- **Status**: FIXED in commit c5a573b — enrichedEnv() now reads ~/.zshrc for SUDO_CODING_KEY

### Issue 3: Raw JSONL stream shown in chat
- When OMP returns 401 (no content), the raw JSONL error events are displayed in the chat instead of a friendly error message
- Root cause: the adapter's event parser passes through unrecognized JSON as raw text
- **Status**: Root cause (401) is fixed; but the UI should still gracefully handle raw JSON in agent_text events

### Issue 4: Double-click from /Applications doesn't open
- Ad-hoc signed .app requires right-click → Open the first time
- This is macOS Gatekeeper behavior for unsigned apps, not an Odo bug
- After first right-click → Open, subsequent double-clicks work

### Issue 5: DMG bundling fails
- `bundle_dmg.sh` fails during `npm run tauri:build` (without `--bundles app`)
- **Impact**: no .dmg for distribution; .app works fine
- **Cause**: likely a tooling issue (hdiutil or create-dmg), not Odo code

## 4. Questions for Reviewers

**Q1**: The browser test shows UI is functional. Are there any critical features NOT tested that the user should verify before dogfooding?
**Q2**: Issue 3 (raw JSONL in chat) — should we add a UI-side filter that detects JSON-like content in agent_text and shows a friendly error instead? Or is this fixed by the 401 fix?
**Q3**: Issue 1 (auto-register project) — should we change the behavior so `default_project_root()` is not used when `projects.json` is empty? Or is this acceptable?
**Q4**: Are there any other .app-launch environment issues we haven't caught? (HOME is set, PATH is enriched, SUDO_CODING_KEY is injected — what else might be missing?)
**Q5**: What's the recommended pre-dogfooding checklist?

Read the Odo repo (`/Users/yingliangzhang/Projects/odo`) to ground your analysis. Write your complete analysis as text. Do NOT write files to the repository.
