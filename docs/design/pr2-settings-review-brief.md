# PR2 Settings Inspector Restructure — Tri-Model Review Brief

## 1. Task

Review commit `773df94` ("feat(gui): PR2 Settings inspector — left category sidebar + right detail panel").

This commit restructures the SettingsPanel from a single-column form into a two-column inspector layout (160px left category sidebar + right detail panel). It touches 2 files: `gui/src/components/SettingsPanel.tsx` (TSX restructure) and `gui/src/styles/app.css` (new CSS selectors + panel width change). All existing settings fields are preserved — no fields added or removed, only regrouped.

For each review criterion (RC1–RC8), independently verify against the live repo. Give **ACCEPT** or **REJECT** with evidence from your own inspection.

## 2. Key Diff (inline)

```diff
diff --git a/gui/src/components/SettingsPanel.tsx b/gui/src/components/SettingsPanel.tsx
index 7e3e44d..a8b2c3f 100644
--- a/gui/src/components/SettingsPanel.tsx
+++ b/gui/src/components/SettingsPanel.tsx
@@ -72,6 +72,16 @@ type Theme = "dark" | "light";
 
+// PR2: Settings inspector categories — left sidebar + right detail panel.
+type Category = "general" | "models" | "knowledge";
+
+const CATEGORIES: { id: Category; label: string }[] = [
+  { id: "general", label: "General" },
+  { id: "models", label: "Models" },
+  { id: "knowledge", label: "Knowledge" },
+];
+
 interface Props {
   onClose: () => void;
   onSaved?: () => void;
@@ -89,6 +99,8 @@ export default function SettingsPanel({ onClose, onSaved, projectRoot }: Props)
   const [savedToast, setSavedToast] = useState(false);
+  const [activeCategory, setActiveCategory] = useState<Category>("general");
   const [theme, setTheme] = useState<Theme>(() =>
@@ -162,18 +174,60 @@ export default function SettingsPanel({ onClose, onSaved, projectRoot }: Props)
     <div className="settings-overlay" onClick={onClose}>
       <div className="settings-panel" role="dialog" aria-modal="true" aria-label="Settings" ref={panelRef} tabIndex={-1} onClick={(e) => e.stopPropagation()}>
         <h2 className="settings-title">Settings</h2>
-        <div className="settings-field">
-          <span id="theme-label">Theme</span>
-          <div className="theme-toggle" role="group" aria-labelledby="theme-label">
-            <button type="button" className={theme === "dark" ? "active" : ""} aria-pressed={theme === "dark"} onClick={() => switchTheme("dark")}>Dark</button>
-            <button type="button" className={theme === "light" ? "active" : ""} aria-pressed={theme === "light"} onClick={() => switchTheme("light")}>Light</button>
-          </div>
-        </div>
-
         {loading && <LoadingInline />}
         {error && <div className="settings-error">{error}</div>}
 
         {settings && (
-          <form className="settings-form" onSubmit={handleSave}>
+          <form className="settings-inspector" onSubmit={handleSave}>
             <datalist id="sudo-models">
               {SUDO_MODELS.map((m) => (<option key={m} value={m} />))}
             </datalist>
+
+            {/* PR2: Left category sidebar */}
+            <nav className="settings-sidebar" aria-label="Settings categories">
+              {CATEGORIES.map((cat) => (
+                <button key={cat.id} type="button"
+                  className={activeCategory === cat.id ? "settings-nav-item active" : "settings-nav-item"}
+                  aria-pressed={activeCategory === cat.id}
+                  onClick={() => setActiveCategory(cat.id)}
+                >
+                  {cat.label}
+                </button>
+              ))}
+            </nav>
+
+            {/* PR2: Right detail panel */}
+            <div className="settings-detail">
+              {activeCategory === "general" && (
+                <>
+                  <div className="settings-field">
+                    <span id="theme-label">Theme</span>
+                    <div className="theme-toggle" role="group" aria-labelledby="theme-label">
+                      <button type="button" className={theme === "dark" ? "active" : ""} aria-pressed={theme === "dark"} onClick={() => switchTheme("dark")}>Dark</button>
+                      <button type="button" className={theme === "light" ? "active" : ""} aria-pressed={theme === "light"} onClick={() => switchTheme("light")}>Light</button>
+                    </div>
+                  </div>
+                  <label className="settings-field">
+                    <span>OMP timeout (seconds)</span>
+                    <input type="text" value={settings.omp_timeout} onChange={(e) => set("omp_timeout", e.target.value)} placeholder="e.g. 900" />
+                  </label>
+                  <label className="settings-field">
+                    <span>Max concurrent runs</span>
+                    <input type="number" min="1" max="16" value={settings.max_concurrent_runs} onChange={(e) => set("max_concurrent_runs", e.target.value)} />
+                  </label>
+                </>
+              )}
+
+              {activeCategory === "models" && (
+                <>
+                  <label className="settings-field">
+                    <span>Coding model</span>
+                    <input type="text" list="sudo-models" value={settings.coding_model} onChange={(e) => set("coding_model", e.target.value)} />
+                  </label>
+                  <label className="settings-field">
+                    <span>Orchestrator model</span>
+                    <input type="text" list="sudo-models" value={settings.orchestrator_model} onChange={(e) => set("orchestrator_model", e.target.value)} />
+                  </label>
+                  <label className="settings-field">
+                    <span>Review Models</span>
+                    <ReviewModelsInput value={settings.review_models} onChange={(v) => set("review_models", v)} />
+                  </label>
+                </>
+              )}
+
+              {activeCategory === "knowledge" && (
+                <>
+                  <label className="settings-field">
+                    <span>Auto-distill</span>
+                    <select value={settings.auto_distill} onChange={(e) => set("auto_distill", e.target.value)}>
+                      <option value="never">Never (manual)</option>
+                      <option value="on_idle">On idle (after N seconds)</option>
+                    </select>
+                  </label>
+                  <label className="settings-field">
+                    <span>Idle seconds</span>
+                    <input type="number" min="5" max="300" disabled={settings.auto_distill !== "on_idle"} value={settings.auto_distill_idle_seconds} onChange={(e) => set("auto_distill_idle_seconds", e.target.value)} />
+                  </label>
+                  <label className="settings-field">
+                    <span>Auto-curate after distill</span>
+                    <select value={settings.auto_curate_after_distill} onChange={(e) => set("auto_curate_after_distill", e.target.value)}>
+                      <option value="false">No (manual)</option>
+                      <option value="true">Yes (chain after distill)</option>
+                    </select>
+                  </label>
+                </>
+              )}
+            </div>
+
             <div className="settings-actions">
               <button type="submit" className="settings-save" disabled={saving}>{saving ? "Saving…" : "Save"}</button>
               <button type="button" className="settings-close" onClick={onClose}>Close</button>
             </div>
           </form>
         )}
         {savedToast && <div className="settings-toast">Settings saved</div>}
```

```diff
diff --git a/gui/src/styles/app.css b/gui/src/styles/app.css
--- a/gui/src/styles/app.css
+++ b/gui/src/styles/app.css
@@ settings-panel width
-.settings-panel { width: 440px; ... }
+.settings-panel { width: 560px; ... }

@@ settings-form → settings-inspector
-.settings-form { display: flex; flex-direction: column; gap: 12px; }
+/* PR2: Settings inspector — 160px left category sidebar + right detail */
+.settings-inspector {
+  display: grid;
+  grid-template-columns: 160px 1fr;
+  gap: 0;
+  min-height: 280px;
+}
+
+.settings-sidebar {
+  display: flex;
+  flex-direction: column;
+  gap: 2px;
+  padding-right: 16px;
+  border-right: 1px solid var(--stroke-tertiary);
+}
+
+.settings-nav-item {
+  display: block;
+  text-align: left;
+  background: transparent;
+  border: none;
+  border-radius: 6px;
+  color: var(--text-dim);
+  font: inherit;
+  font-size: 13px;
+  padding: 6px 10px;
+  cursor: pointer;
+  transition: background 0.15s var(--ease-standard), color 0.15s var(--ease-standard);
+}
+
+.settings-nav-item:hover {
+  color: var(--text);
+  background: color-mix(in srgb, var(--bg-input) 50%, transparent);
+}
+
+.settings-nav-item.active {
+  color: var(--accent-user);
+  background: color-mix(in srgb, var(--accent-user) 12%, transparent);
+  font-weight: 500;
+}
+
+.settings-detail {
+  padding-left: 20px;
+  display: flex;
+  flex-direction: column;
+  gap: 12px;
+}

@@ settings-actions
 .settings-actions {
   display: flex;
   gap: 8px;
   margin-top: 6px;
+  grid-column: 1 / -1;
 }
```

## 3. Verification Evidence Already Collected

- `go build ./...` → PASS
- `npx tsc --noEmit` → PASS
- `npm run build` (vite) → PASS, CSS bundle 52.93 kB
- `npx playwright test` → **43/43 PASS**
- E2E selectors verified: `role="dialog"` + `aria-label="Settings"` unchanged (shortcuts.spec.ts:59), `.settings-save` class retained (skills-proposals.spec.ts:66,84)

## 4. Review Criteria (RC1–RC8)

**RC1: All existing fields preserved — no fields added or removed**
- Verify: the 8 settings fields (Theme, Coding model, Orchestrator model, OMP timeout, Review Models, Auto-distill, Idle seconds, Auto-curate after distill, Max concurrent runs) are ALL present in the new code
- Check: read `SettingsPanel.tsx` and count the fields across all 3 categories

**RC2: Field grouping is correct**
- General: Theme, OMP timeout, Max concurrent runs
- Models: Coding model, Orchestrator model, Review Models
- Knowledge: Auto-distill, Idle seconds, Auto-curate after distill
- Verify: each field appears in exactly one category, no field appears in multiple categories, no field is missing from all categories

**RC3: Grid layout correctness**
- `.settings-inspector` uses `display: grid; grid-template-columns: 160px 1fr`
- The `<nav>` (sidebar) is the first grid child → occupies 160px column
- `.settings-detail` is the second grid child → occupies 1fr column
- `.settings-actions` has `grid-column: 1 / -1` → spans full width
- Verify: read the CSS and confirm the grid structure matches the TSX element order

**RC4: E2E selector compatibility**
- `role="dialog"` + `aria-label="Settings"` — used by shortcuts.spec.ts:59 (`getByRole("dialog", { name: "Settings" })`)
- `.settings-save` class — used by skills-proposals.spec.ts:66,84
- `.settings-overlay` class — used by App.tsx:587
- Verify: all three selectors are still present in the new code

**RC5: Category navigation works**
- Clicking a category button sets `activeCategory` state
- Only the active category's fields are rendered (conditional rendering via `{activeCategory === "general" && (...)}`)
- `aria-pressed` reflects the active state
- Verify: the onClick handler, state management, and conditional rendering are correct

**RC6: Save still works**
- The form still has `onSubmit={handleSave}`
- The save button has `type="submit"` and `className="settings-save"`
- All field values are still wired to the `settings` state via `set()` calls
- Verify: save will capture all field values across all categories (not just the visible one) because they share the same `settings` state object

**RC7: CSS visual correctness**
- `.settings-nav-item` has hover and active states
- Active state uses `color-mix(in srgb, var(--accent-user) 12%, transparent)` — a subtle tint matching the accent color
- `.settings-sidebar` has a right border separating it from the detail panel
- Panel width increased from 440px to 560px — verify this is sufficient for the two-column layout without horizontal scrolling at minimum window width (700px per tauri.conf.json)
- Verify: at 700px window width, 560px panel + 48px margin = 608px < 700px → fits

**RC8: No dead code or stale selectors**
- The old `.settings-form` class is still defined in CSS but no longer used in TSX (changed to `.settings-inspector`)
- The old `.settings-section-title` class is still defined in CSS but no longer used in TSX (removed from the form)
- Verify: check if `.settings-form` and `.settings-section-title` have any remaining references; if not, they are dead CSS that should be cleaned up

## 5. Instructions to Reviewers

1. Read `gui/src/components/SettingsPanel.tsx` and `gui/src/styles/app.css` in the live repo.
2. For each RC1–RC8, independently verify against the source.
3. Check that all 9 settings fields are present across the 3 categories.
4. Verify the grid layout: sidebar (160px) + detail (1fr) + actions (full width).
5. Verify E2E compatibility: `role="dialog"`, `aria-label="Settings"`, `.settings-save`, `.settings-overlay` all present.
6. Check for dead CSS selectors from the old single-column layout.
7. Give ACCEPT or REJECT per criterion, then an overall verdict (ACCEPT / NEEDS_FIXES).

Write your complete analysis as text in your response. Do NOT write files to the repository.

## 6. Context

- Tauri 2 + React 18 + Go desktop app (macOS only)
- `gui/src/styles/app.css` is the single CSS file (~3850 lines)
- Settings fields map to the `Settings` interface in `gui/src/types.ts` (9 string fields)
- E2E tests: Playwright, 43/43 PASS. Key selectors: `role="dialog"` + `aria-label="Settings"`, `.settings-save`, `.settings-overlay`
- Apple HIG: content-first, natural sizing, subtle transitions
- DESIGN LOCK consensus: 160px left category bar + right detail panel, all existing fields preserved
