# PR1 CSS Polish — Tri-Model Review Brief

## 1. Task

Review commit `9aa7934` ("feat(gui): PR1 CSS polish — systemBlue + SF Pro + flat agent + motion tokens").

This is a **CSS-only** change to `gui/src/styles/app.css` (46 insertions, 11 deletions). No `.tsx` files were modified.

For each review criterion (RC1–RC11), independently verify against the live repo (read the CSS file, rebuild, run gates). Give **ACCEPT** or **REJECT** with evidence from your own inspection, not the submitter's. Then give an overall verdict.

## 2. Key Diff (inline)

```diff
diff --git a/gui/src/styles/app.css b/gui/src/styles/app.css
index ccdaacc..4c9d16d 100644
--- a/gui/src/styles/app.css
+++ b/gui/src/styles/app.css
@@ -11,7 +11,7 @@
   --border: var(--stroke-secondary); /* alias for backward compat */
   --text: #d8dce4;
   --text-dim: #7d8593;
-  --accent-user: #3b82f6;
+  --accent-user: #0A84FF;
   --accent-agent: #1e2228;
   --ok: #3fa35f;
   --err: #c34a4a;
@@ -69,10 +69,15 @@
   --shadow-soft: 0 2px 8px rgba(0, 0, 0, 0.25), 0 0 1px rgba(0, 0, 0, 0.15);
   --shadow-panel: 0 4px 16px rgba(0, 0, 0, 0.35), 0 0 1px rgba(0, 0, 0, 0.2);
-  /* Motion: Apple-style deceleration curve (cubic-bezier 0.22 1 0.36 1) for
+  /* Motion: Apple-style deceleration curve (cubic-bezier 0.32 0.72 0 1) for
       enter/slide, and a gentler standard curve for width/color tweens. */
-  --ease-out: cubic-bezier(0.22, 1, 0.36, 1);
+  --ease-out: cubic-bezier(0.32, 0.72, 0, 1);
   --ease-standard: cubic-bezier(0.4, 0, 0.2, 1);
+  /* Motion duration tokens + spring easing for bounce/overshoot. */
+  --dur-fast: 120ms;
+  --dur: 180ms;
+  --dur-slow: 280ms;
+  --ease-spring: cubic-bezier(0.34, 1.56, 0.64, 1);
 }

 /* E P2: thin scrollbars — macOS-native feel, consistent across themes */
@@ -105,7 +110,7 @@
   --border: var(--stroke-secondary); /* alias for backward compat */
   --text: #1a1a1a;
   --text-dim: #666;
-  --accent-user: #2563eb;
+  --accent-user: #007AFF;
   --accent-agent: #7c3aed;
   --err: #dc2626;
   --ok: #16a34a;
@@ -133,6 +138,16 @@
   --stroke-tertiary: #ebebeb;
 }

+/* Frosted vibrancy: translucent top/status bars need light-theme-aware
+   backgrounds so AA text contrast holds in both themes. */
+[data-theme="light"] .app-topbar {
+  background: rgba(255, 255, 255, 0.88);
+}
+
+[data-theme="light"] .app-statusbar {
+  background: rgba(255, 255, 255, 0.88);
+}
+
 /* In the light theme the agent message is a flat elevated surface, so
    links and blockquotes use the standard page-link/dim colors instead of
    the white-range values that served the old saturated-purple bubble. */
@@ -183,7 +198,7 @@ body {
   /* macOS "content text" is 15px. -apple-system resolves to SF Pro on
      Apple platforms; antialiasing keeps subpixel strokes crisp in the
      Tauri webview. */
-  font: 15px/1.55 -apple-system, BlinkMacSystemFont, "SF Pro Text", "Segoe UI", Helvetica, Arial, sans-serif;
+  font: 15px/1.55 -apple-system, BlinkMacSystemFont, "SF Pro Text", system-ui, sans-serif;
   -webkit-font-smoothing: antialiased;
   -moz-osx-font-smoothing: grayscale;
   text-rendering: optimizeLegibility;
@@ -227,7 +242,9 @@ body {
   height: var(--topbar-height);
   flex-shrink: 0;
   padding: 0 14px;
-  background: var(--bg-raised, var(--bg-input));
+  background: rgba(26, 29, 36, 0.88);
+  backdrop-filter: blur(12px) saturate(140%);
+  -webkit-backdrop-filter: blur(12px) saturate(140%);
   border-bottom: 1px solid var(--stroke-tertiary);
   font-size: var(--text-label);
 }
@@ -362,11 +379,14 @@ body {
   height: var(--statusbar-height);
   flex-shrink: 0;
   padding: 0 12px;
-  background: var(--bg-raised, var(--bg-input));
+  background: rgba(26, 29, 36, 0.88);
+  backdrop-filter: blur(12px) saturate(140%);
+  -webkit-backdrop-filter: blur(12px) saturate(140%);
   border-top: 1px solid var(--stroke-tertiary);
   font-size: var(--text-micro);
   color: var(--text-dim);
   font-family: var(--mono);
+  font-variant-numeric: tabular-nums;
 }

 .status-item {
@@ -1274,8 +1294,8 @@ details.sidebar-section[open] > .sidebar-section-header .caret {
 }

 .chat-input:focus-within {
-  border-color: color-mix(in srgb, var(--accent-user) 60%, transparent);
-  box-shadow: 0 0 0 3px color-mix(in srgb, var(--accent-user) 22%, transparent);
+  border-color: var(--accent-user);
+  box-shadow: 0 0 0 3px rgba(10, 132, 255, 0.25);
 }

 .composer-hint {
@@ -1373,6 +1393,7 @@ details.sidebar-section[open] > .sidebar-section-header .caret {
   color: var(--text);
   padding: 8px 0;
   font: inherit;
+  transition: box-shadow 0.12s ease;
 }

 .chat-input textarea {
@@ -1461,7 +1482,7 @@ details.sidebar-section[open] > .sidebar-section-header .caret {
   align-self: flex-end;
   background: var(--accent-user);
   color: #fff;
-  border-bottom-right-radius: 6px;
+  border-radius: 12px 12px 4px 12px;
   box-shadow: 0 1px 2px rgba(0, 0, 0, 0.25);
 }

@@ -1470,10 +1491,12 @@ details.sidebar-section[open] > .sidebar-section-header .caret {
 .bubble-agent {
   align-self: flex-start;
   width: 92%;
+  max-width: 720px;
+  margin: 0 auto;
   background: transparent;
   color: var(--agent-text);
   border: 1px solid var(--stroke-tertiary);
-  border-radius: var(--radius-md);
+  border-radius: 0;
 }

 /* M11 F2: agent thinking — collapsible, muted, no bubble bg */
@@ -2357,6 +2380,7 @@ mark {
   white-space: nowrap;
   overflow: hidden;
   text-overflow: ellipsis;
+  font-variant-numeric: tabular-nums;
 }

 /* ---------- Belt C: run grouping (spec §Fix 1) ---------- */
@@ -2380,6 +2404,7 @@ mark {
   border-top: 1px solid var(--stroke-tertiary);
   color: var(--text-dim);
   font-size: var(--text-caption);
+  font-variant-numeric: tabular-nums;
 }

 /* The bare .run-status class belongs to the live-run bar under the
@@ -3330,3 +3355,7 @@ mark {
   cursor: pointer;
   line-height: 1.4;
   white-space: nowrap;
+  font-variant-numeric: tabular-nums;
 }

 .status-badge:hover {
@@ -3778,3 +3804,12 @@ mark {
   opacity: 0.5;
   cursor: default;
 }
+
+@media (prefers-reduced-motion: reduce) {
+  *, *::before, *::after {
+    animation-duration: 0.01ms !important;
+    animation-iteration-count: 1 !important;
+    transition-duration: 0.01ms !important;
+    scroll-behavior: auto !important;
+  }
+}
```

## 3. Verification Evidence Already Collected

- `go build ./...` → PASS (exit 0)
- `npx tsc --noEmit` → PASS (exit 0)
- `npm run build` → PASS (per handoff: 43/43 E2E PASS)
- No `.tsx` files modified (CSS-only change)

## 4. Review Criteria (RC1–RC11)

For each criterion, independently verify against the live repo. Read the actual CSS file at `gui/src/styles/app.css` to confirm the diff matches. Give ACCEPT or REJECT with evidence.

**RC1: Accent color — macOS systemBlue**
- Dark theme `--accent-user` changed from `#3b82f6` to `#0A84FF` (macOS systemBlue dark)
- Light theme `--accent-user` changed from `#2563eb` to `#007AFF` (macOS systemBlue light)
- Verify: both values are correct macOS systemBlue hex values
- Verify: no other references to the old accent values remain as hardcoded colors

**RC2: Font stack — SF Pro + system-ui**
- Body font shortened to `-apple-system, BlinkMacSystemFont, "SF Pro Text", system-ui, sans-serif`
- Verify: `system-ui` is a valid CSS keyword that resolves to the OS default UI font
- Verify: removing "Segoe UI", Helvetica, Arial does not break rendering on non-macOS platforms (Tauri webview on macOS uses WebKit; this is a macOS-only app)

**RC3: Motion tokens — easing curve + duration + spring**
- `--ease-out` changed from `cubic-bezier(0.22, 1, 0.36, 1)` to `cubic-bezier(0.32, 0.72, 0, 1)`
- Added: `--dur-fast: 120ms`, `--dur: 180ms`, `--dur-slow: 280ms`, `--ease-spring: cubic-bezier(0.34, 1.56, 0.64, 1)`
- Verify: the new easing curve `cubic-bezier(0.32, 0.72, 0, 1)` is a valid Apple-style deceleration curve (should start fast, decelerate)
- Verify: `--ease-spring` with y1=1.56 produces overshoot (y > 1) — confirm this is intentional and appropriate for spring animations
- Verify: the duration tokens are actually used somewhere in the CSS (search for `var(--dur`), or are they dead tokens?

**RC4: Frosted vibrancy — topbar dark theme**
- `.app-topbar` background changed from `var(--bg-raised, var(--bg-input))` to `rgba(26, 29, 36, 0.88)`
- Added `backdrop-filter: blur(12px) saturate(140%)` + `-webkit-backdrop-filter` prefix
- Verify: `rgba(26, 29, 36, 0.88)` matches the dark theme `--bg` value (check the dark theme `:root` for `--bg`)
- Verify: the hardcoded RGBA replaces a CSS variable — does this break theme-agnosticism for any custom theme?

**RC5: Frosted vibrancy — statusbar dark theme**
- `.app-statusbar` same change as topbar (rgba bg + backdrop-filter)
- Verify same as RC4

**RC6: Frosted vibrancy — light theme overrides**
- `[data-theme="light"] .app-topbar` → `background: rgba(255, 255, 255, 0.88)`
- `[data-theme="light"] .app-statusbar` → `background: rgba(255, 255, 255, 0.88)`
- Verify: light theme overrides correctly override the dark-theme RGBA for topbar/statusbar
- Verify: the backdrop-filter is NOT overridden in light theme — it should still apply (blur works on both light/dark bg)

**RC7: Input focus ring**
- `.chat-input:focus-within` border-color changed from `color-mix(in srgb, var(--accent-user) 60%, transparent)` to `var(--accent-user)`
- box-shadow changed from `color-mix(in srgb, var(--accent-user) 22%, transparent)` to `rgba(10, 132, 255, 0.25)`
- Verify: `rgba(10, 132, 255, 0.25)` is the dark-theme systemBlue at 25% opacity — this is hardcoded and will NOT adapt to light theme's `#007AFF`. Is this a problem? Check if `.chat-input:focus-within` has a light-theme override.
- Verify: the `.chat-input` now has `transition: box-shadow 0.12s ease` — confirm this animates the focus ring appearance/disappearance smoothly

**RC8: User bubble asymmetric tail**
- `.bubble-user` `border-bottom-right-radius: 6px` replaced with `border-radius: 12px 12px 4px 12px`
- Verify: this creates an asymmetric tail effect (top-left=12px, top-right=12px, bottom-right=4px, bottom-left=12px — the "tail" is at bottom-right)
- Verify: the overall border-radius (12px) is consistent with the design system's radius scale

**RC9: Agent bubble flat design**
- `.bubble-agent` changes: `width: 92%` kept, added `max-width: 720px` + `margin: 0 auto`, `background: transparent`, `border: 1px solid var(--stroke-tertiary)`, `border-radius: 0`
- Verify: `border-radius: 0` makes the agent message completely flat (no rounded corners) — confirm this is the intended "flat card" design, not a regression
- Verify: `max-width: 720px` + `margin: 0 auto` centers the agent message — confirm this doesn't conflict with `align-self: flex-start` (flex-start aligns along cross axis; margin auto centers along inline axis)
- Verify: the hairline border (`1px solid var(--stroke-tertiary)`) is visible in both dark and light themes

**RC10: Tabular nums**
- Added `font-variant-numeric: tabular-nums` to: `.app-statusbar`, `.run-meta` (line ~2380), `.run-group-meta` (line ~2404), `.status-badge` (line ~3355)
- Verify: tabular-nums is appropriate for numeric displays (timestamps, counts, badges) to prevent width jitter
- Verify: no existing `font-variant-numeric` on these selectors was overridden

**RC11: Reduced motion guard**
- Added global `@media (prefers-reduced-motion: reduce)` block
- Verify: the guard sets animation-duration/transition-duration to 0.01ms and scroll-behavior to auto
- Verify: the `!important` flags are appropriate for a global accessibility override
- Verify: the guard does NOT disable transitions entirely (0.01ms, not 0ms) — this is the standard accessibility pattern (instant but still "fired" for any JS that listens)
- Verify: no existing `@media (prefers-reduced-motion)` rule was overridden by this addition

## 5. Instructions to Reviewers

1. Read `gui/src/styles/app.css` in the live repo to verify the diff matches the actual file.
2. For each RC1–RC11, independently verify against the source. Don't trust the submitter's evidence — re-inspect yourself.
3. For CSS values: confirm hex values are correct macOS systemBlue, confirm easing curves are valid, confirm rgba values match theme backgrounds.
4. For potential regressions: check if hardcoded RGBA values (topbar bg, focus ring shadow) break light-theme adaptation.
5. Check if motion tokens (`--dur-fast`, `--dur`, `--dur-slow`, `--ease-spring`) are actually used anywhere, or if they are dead tokens.
6. Check if `--ease-spring` with overshoot (y1=1.56) could cause visual glitches if used on layout properties (width, height) vs transform-only.
7. Give ACCEPT or REJECT per criterion, then an overall verdict (ACCEPT / NEEDS_FIXES).

Write your complete analysis as text in your response. Do NOT write files to the repository.

## 6. Context

- This is a Tauri 2 + React + Go desktop app (macOS only)
- `gui/src/styles/app.css` is the single CSS file (~2950 lines → now ~2990 lines)
- The app uses CSS variables for theming (dark `:root` + `[data-theme="light"]`)
- Apple HIG is the design reference: content-first, natural sizing, subtle transitions, SF typography
- E2E tests (Playwright, 43/43 PASS) assert TopBar button labels via `getByText` — CSS changes do not affect E2E
- Previous tri-model DESIGN LOCK consensus (3/3) approved all these changes conceptually; this review verifies the implementation
