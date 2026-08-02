import { FormEvent, useEffect, useRef, useState } from "react";
import { errorMessage, getSettings, unwrap, updateSettings } from "../api";
import type { Settings } from "../types";

const SAVED_TOAST_MS = 3000;

interface Props {
  onClose: () => void;
}

// M2 settings modal: loads the daemon's project settings on mount, edits a
// curated subset (coding/orchestrator model, OMP timeout, default adapter,
// review models), and saves the full object back so untouched keys
// (providers) survive the round trip.
export default function SettingsPanel({ onClose }: Props) {
  const [settings, setSettings] = useState<Settings | null>(null);
  const [loading, setLoading] = useState(true);
  const [saving, setSaving] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const [savedToast, setSavedToast] = useState(false);
  const toastTimer = useRef<number | null>(null);

  useEffect(() => {
    let cancelled = false;
    (async () => {
      try {
        const resp = unwrap(await getSettings());
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
  }, []);

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
      unwrap(await updateSettings(settings));
      setSavedToast(true);
      clearTimeout(toastTimer.current ?? undefined);
      toastTimer.current = setTimeout(() => setSavedToast(false), SAVED_TOAST_MS);
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
        onClick={(e) => e.stopPropagation()}
      >
        <h2 className="settings-title">Settings</h2>

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
                placeholder="comma-separated, e.g. gpt-4o, claude-sonnet"
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
