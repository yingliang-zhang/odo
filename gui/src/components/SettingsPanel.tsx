import { FormEvent, useEffect, useRef, useState } from "react";
import { errorMessage, getSettings, unwrap, updateSettings } from "../api";
import { SUDO_MODELS, SUDO_PROVIDER, type Settings } from "../types";
import LoadingInline from "./LoadingInline";
import { Button } from "./ui/button";
import { Dialog, DialogContent, DialogTitle } from "./ui/dialog";

const SAVED_TOAST_MS = 3000;

// P1-1: Chip/tag input for comma-separated model@provider review models.
// Enter/comma commits a chip; each chip auto-appends @sudo if no provider is
// given. Chips render as removable tags; the underlying string stays in wire
// format (comma-joined) so save needs no conversion.
function ReviewModelsInput({
  value,
  onChange,
}: {
  value: string;
  onChange: (next: string) => void;
}) {
  const chips = value
    .split(",")
    .map((c) => c.trim())
    .filter((c) => c !== "");
  const [draft, setDraft] = useState("");

  const commit = (next: string[]) => onChange(next.join(","));
  const addChip = () => {
    const raw = draft.trim();
    if (raw === "") return;
    const chip = raw.includes("@") ? raw : `${raw}@${SUDO_PROVIDER}`;
    if (!chips.includes(chip)) commit([...chips, chip]);
    setDraft("");
  };

  return (
    <div className="model-chips">
      {chips.map((c, i) => (
        <span key={`${c}-${i}`} className="model-chip">
          {c}
          <button
            type="button"
            className="model-chip-remove"
            aria-label={`Remove ${c}`}
            onClick={(e) => {
              e.stopPropagation();
              e.preventDefault();
              commit(chips.filter((x) => x !== c));
            }}
          >
            ×
          </button>
        </span>
      ))}
      <input
        type="text"
        list="sudo-models"
        className="model-chip-input"
        value={draft}
        onChange={(e) => setDraft(e.target.value)}
        onKeyDown={(e) => {
          if (e.key === "Enter" || e.key === ",") {
            e.preventDefault();
            addChip();
          }
        }}
        onBlur={addChip}
        placeholder="Add model…"
      />
    </div>
  );
}

// Belt D: the theme persists in localStorage and is applied to <html> —
// App reads the same key on mount, so the dialog never has to sync back.
type Theme = "dark" | "light";

// PR2: Settings inspector categories — left sidebar + right detail panel.
type Category = "general" | "models" | "knowledge";

const CATEGORIES: { id: Category; label: string }[] = [
  { id: "general", label: "General" },
  { id: "models", label: "Models" },
  { id: "knowledge", label: "Knowledge" },
];

interface Props {
  onClose: () => void;
  // M9 P4: fired after a successful save so App can react.
  onSaved?: () => void;
  // M11 P1: settings belong to this project's daemon; null = bridge default.
  projectRoot?: string | null;
}

// M2 settings modal: loads the daemon's project settings on mount, edits a
// curated subset (coding/orchestrator model, OMP timeout,
// review models), and saves the full object back so untouched keys
// (providers) survive the round trip.
//
// PR2: Restructured from a single-column form into an inspector layout —
// 160px left category sidebar + right detail panel. All existing fields
// preserved, only regrouped under General / Models / Knowledge.
export default function SettingsPanel({ onClose, onSaved, projectRoot }: Props) {
  const [settings, setSettings] = useState<Settings | null>(null);
  const [loading, setLoading] = useState(true);
  const [saving, setSaving] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const [savedToast, setSavedToast] = useState(false);
  const [activeCategory, setActiveCategory] = useState<Category>("general");
  const [theme, setTheme] = useState<Theme>(() =>
    localStorage.getItem("odo-theme") === "light" ? "light" : "dark",
  );
  const switchTheme = (next: Theme) => {
    setTheme(next);
    localStorage.setItem("odo-theme", next);
    document.documentElement.dataset.theme = next;
  };
  const toastTimer = useRef<number | null>(null);
  // Focus trap + Esc + overlay close are owned by Radix Dialog (Phase 5).

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
    <Dialog open onOpenChange={(v) => !v && onClose()}>
      <DialogContent
        aria-label="Settings"
        className="settings-panel w-[480px] max-w-[calc(100vw-48px)] px-7 py-6"
        onInteractOutside={(e) => e.preventDefault()}
      >
        <DialogTitle className="settings-title">Settings</DialogTitle>

        {loading && <LoadingInline />}
        {error && <div className="settings-error">{error}</div>}

        {settings && (
          <form className="settings-inspector" onSubmit={handleSave}>
            <datalist id="sudo-models">
              {SUDO_MODELS.map((m) => (
                <option key={m} value={m} />
              ))}
            </datalist>

            {/* PR2: Left category sidebar */}
            <nav className="settings-sidebar" aria-label="Settings categories">
              {CATEGORIES.map((cat) => (
                <button
                  key={cat.id}
                  type="button"
                  className={
                    activeCategory === cat.id
                      ? "settings-nav-item active"
                      : "settings-nav-item"
                  }
                  aria-pressed={activeCategory === cat.id}
                  onClick={() => setActiveCategory(cat.id)}
                >
                  {cat.label}
                </button>
              ))}
            </nav>

            {/* PR2: Right detail panel */}
            <div className="settings-detail">
              {activeCategory === "general" && (
                <>
                  <div className="settings-field">
                    <span id="theme-label">Theme</span>
                    <div
                      className="theme-toggle"
                      role="group"
                      aria-labelledby="theme-label"
                    >
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
                    <span>Max concurrent runs</span>
                    <input
                      type="number"
                      min="1"
                      max="16"
                      value={settings.max_concurrent_runs}
                      onChange={(e) =>
                        set("max_concurrent_runs", e.target.value)
                      }
                    />
                  </label>
                </>
              )}

              {activeCategory === "models" && (
                <>
                  <label className="settings-field">
                    <span>Coding model</span>
                    <input
                      type="text"
                      list="sudo-models"
                      value={settings.coding_model}
                      onChange={(e) => set("coding_model", e.target.value)}
                    />
                  </label>
                  <label className="settings-field">
                    <span>Orchestrator model</span>
                    <input
                      type="text"
                      list="sudo-models"
                      value={settings.orchestrator_model}
                      onChange={(e) =>
                        set("orchestrator_model", e.target.value)
                      }
                    />
                  </label>
                  <label className="settings-field">
                    <span>Review Models</span>
                    <ReviewModelsInput
                      value={settings.review_models}
                      onChange={(v) => set("review_models", v)}
                    />
                  </label>
                </>
              )}

              {activeCategory === "knowledge" && (
                <>
                  <label className="settings-field">
                    <span>Auto-distill</span>
                    <select
                      value={settings.auto_distill}
                      onChange={(e) => set("auto_distill", e.target.value)}
                    >
                      <option value="never">Never (manual)</option>
                      <option value="on_idle">
                        On idle (after N seconds)
                      </option>
                    </select>
                  </label>
                  <label className="settings-field">
                    <span>Idle seconds</span>
                    <input
                      type="number"
                      min="15"
                      max="600"
                      disabled={settings.auto_distill !== "on_idle"}
                      value={settings.auto_distill_idle_seconds}
                      onChange={(e) =>
                        set("auto_distill_idle_seconds", e.target.value)
                      }
                    />
                  </label>
                </>
              )}
            </div>

            <div className="settings-actions">
              <Button type="submit" variant="default" disabled={saving}>
                {saving ? "Saving…" : "Save"}
              </Button>
              <Button type="button" variant="secondary" onClick={onClose}>
                Close
              </Button>
            </div>
          </form>
        )}

        {savedToast && <div className="settings-toast">Settings saved</div>}
      </DialogContent>
    </Dialog>
  );
}
