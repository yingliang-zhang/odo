import { FormEvent, useEffect, useRef, useState } from "react";
import { errorMessage, getSettings, unwrap, updateSettings } from "../api";
import { useFocusTrap } from "../focusTrap";
import type { Settings } from "../types";

const SAVED_TOAST_MS = 3000;

// Belt D: the theme persists in localStorage and is applied to <html> —
// App reads the same key on mount, so the dialog never has to sync back.
type Theme = "dark" | "light";

interface Props {
  onClose: () => void;
  // M9 P4: fired after a successful save so App can re-read the adapter.
  onSaved?: () => void;
  // M11 P1: settings belong to this project's daemon; null = bridge default.
  projectRoot?: string | null;
}

// M2 settings modal: loads the daemon's project settings on mount, edits a
// curated subset (coding/orchestrator model, OMP timeout, default adapter,
// review models), and saves the full object back so untouched keys
// (providers) survive the round trip.
export default function SettingsPanel({ onClose, onSaved, projectRoot }: Props) {
  const [settings, setSettings] = useState<Settings | null>(null);
  const [loading, setLoading] = useState(true);
  const [saving, setSaving] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const [savedToast, setSavedToast] = useState(false);
  const [theme, setTheme] = useState<Theme>(() =>
    localStorage.getItem("odo-theme") === "light" ? "light" : "dark",
  );
  const switchTheme = (next: Theme) => {
    setTheme(next);
    localStorage.setItem("odo-theme", next);
    document.documentElement.dataset.theme = next;
  };
  const toastTimer = useRef<number | null>(null);
  // Belt D: modal focus trap (Tab cycles, focus restores on close).
  const panelRef = useRef<HTMLDivElement>(null);
  useFocusTrap(panelRef);

  useEffect(() => {
    let cancelled = false;
    (async () => {
      try {
        const resp = unwrap(await getSettings(projectRoot ?? undefined));
        if (!cancelled && resp.settings) {
          setSettings(resp.settings);
        }
      } catch (e) {
        if (!cancelled) setError(`load failed: ${errorMessage(e)}`);
      } finally {
        if (!cancelled) setLoading(false);
      }
    })();
    return () => {
      cancelled = true;
    };
  }, [projectRoot]);

  // Escape closes, like every other modal affordance.
  useEffect(() => {
    const onKey = (e: KeyboardEvent) => {
      if (e.key === "Escape") onClose();
    };
    window.addEventListener("keydown", onKey);
    return () => window.removeEventListener("keydown", onKey);
  }, [onClose]);

  useEffect(() => {
    return () => clearTimeout(toastTimer.current ?? undefined);
  }, []);

  const set = <K extends keyof Settings>(key: K, value: Settings[K]) => {
    setSettings((prev) => (prev ? { ...prev, [key]: value } : prev));
  };

  const handleSave = async (e: FormEvent) => {
    e.preventDefault();
    if (!settings || saving) return;
    setSaving(true);
    setError(null);
    try {
      unwrap(await updateSettings(settings, projectRoot ?? undefined));
      setSavedToast(true);
      clearTimeout(toastTimer.current ?? undefined);
      toastTimer.current = setTimeout(() => setSavedToast(false), SAVED_TOAST_MS);
      onSaved?.();
    } catch (err) {
      setError(`save failed: ${errorMessage(err)}`);
    } finally {
      setSaving(false);
    }
  };

  return (
    <div className="settings-overlay" onClick={onClose}>
      <div
        className="settings-panel"
        role="dialog"
        aria-modal="true"
        aria-label="Settings"
        ref={panelRef}
        tabIndex={-1}
        onClick={(e) => e.stopPropagation()}
      >
        <h2 className="settings-title">Settings</h2>

        <div className="settings-field">
          <span id="theme-label">Theme</span>
          <div className="theme-toggle" role="group" aria-labelledby="theme-label">
            <button
              type="button"
              className={theme === "dark" ? "active" : ""}
              aria-pressed={theme === "dark"}
              onClick={() => switchTheme("dark")}
            >
              Dark
            </button>
            <button
              type="button"
              className={theme === "light" ? "active" : ""}
              aria-pressed={theme === "light"}
              onClick={() => switchTheme("light")}
            >
              Light
            </button>
          </div>
        </div>

        {loading && <div className="settings-loading">Loading…</div>}
        {error && <div className="settings-error">{error}</div>}

        {settings && (
          <form className="settings-form" onSubmit={handleSave}>
            <label className="settings-field">
              <span>Coding model</span>
              <input
                type="text"
                value={settings.coding_model}
                onChange={(e) => set("coding_model", e.target.value)}
              />
            </label>
            <label className="settings-field">
              <span>Orchestrator model</span>
              <input
                type="text"
                value={settings.orchestrator_model}
                onChange={(e) => set("orchestrator_model", e.target.value)}
              />
            </label>
            <label className="settings-field">
              <span>OMP timeout (seconds)</span>
              <input
                type="text"
                value={settings.omp_timeout}
                onChange={(e) => set("omp_timeout", e.target.value)}
                placeholder="e.g. 900"
              />
            </label>
            <label className="settings-field">
              <span>Default adapter</span>
              <select
                value={settings.default_adapter}
                onChange={(e) => set("default_adapter", e.target.value)}
              >
                <option value="omp">OMP</option>
                <option value="pi">Pi</option>
              </select>
            </label>
            <label className="settings-field">
              <span>Review models</span>
              <input
                type="text"
                value={settings.review_models}
                onChange={(e) => set("review_models", e.target.value)}
                placeholder="comma-separated model@provider, e.g. glm-5.2@sudo, t9s/kimi-k3@sudo"
              />
            </label>

            {/* M10: Knowledge capture (auto-distill) */}
            <div className="settings-section-title">Knowledge capture</div>
            <label className="settings-row">
              <span>Auto-distill</span>
              <select
                value={settings.auto_distill}
                onChange={(e) => set("auto_distill", e.target.value)}
              >
                <option value="never">Never (manual)</option>
                <option value="on_idle">On idle (after N seconds)</option>
              </select>
            </label>
            <label className="settings-row">
              <span>Idle seconds</span>
              <input
                type="number"
                min="5"
                max="300"
                value={settings.auto_distill_idle_seconds}
                onChange={(e) => set("auto_distill_idle_seconds", e.target.value)}
              />
            </label>
            <label className="settings-row">
              <span>Auto-curate after distill</span>
              <select
                value={settings.auto_curate_after_distill}
                onChange={(e) => set("auto_curate_after_distill", e.target.value)}
              >
                <option value="false">No (manual)</option>
                <option value="true">Yes (chain after distill)</option>
              </select>
            </label>
            <label className="settings-row">
              <span>Max concurrent runs</span>
              <input
                type="number"
                min="1"
                max="16"
                value={settings.max_concurrent_runs}
                onChange={(e) => set("max_concurrent_runs", e.target.value)}
              />
            </label>

            <div className="settings-actions">
              <button type="submit" className="settings-save" disabled={saving}>
                {saving ? "Saving…" : "Save"}
              </button>
              <button type="button" className="settings-close" onClick={onClose}>
                Close
              </button>
            </div>
          </form>
        )}

        {savedToast && <div className="settings-toast">Settings saved</div>}
      </div>
    </div>
  );
}
