import { useEffect, useRef, useState } from "react";
import { ChevronDown, Check } from "lucide-react";
import { updateSettings } from "../api";

// Model pill: a compact dropdown in the composer that shows the
// current coding model and lets the user switch it per-message.
// Reads/writes the daemon's coding_model setting via IPC.
//
// The model list is a curated set of common sudo models plus whatever
// the daemon reports as the current value. The user can also type a
// custom model name (matching the Settings panel's free-text input).

const COMMON_MODELS = [
  "sudo/t9s/kimi-k3",
  "sudo/glm-5.2",
  "sudo/t9s/deepseek-v4-flash",
  "sudo/t9s/claude-sonnet-4",
  "sudo/t9s/gpt-5",
];

interface Props {
  projectRoot?: string | null;
  currentModel?: string | null;
  onModelChanged?: () => void;
}

export default function ModelPill({ projectRoot, currentModel, onModelChanged }: Props) {
  const [open, setOpen] = useState(false);
  const [model, setModel] = useState(currentModel ?? "");
  const [customMode, setCustomMode] = useState(false);
  const [customText, setCustomText] = useState("");
  const rootRef = useRef<HTMLDivElement>(null);

  // Sync from parent when settings reload.
  useEffect(() => {
    setModel(currentModel ?? "");
  }, [currentModel]);

  // Click-away closes.
  useEffect(() => {
    if (!open) return;
    const onDocDown = (e: MouseEvent) => {
      if (rootRef.current && !rootRef.current.contains(e.target as Node)) {
        setOpen(false);
        setCustomMode(false);
      }
    };
    document.addEventListener("mousedown", onDocDown);
    return () => document.removeEventListener("mousedown", onDocDown);
  }, [open]);

  const selectModel = async (m: string) => {
    setModel(m);
    setOpen(false);
    setCustomMode(false);
    try {
      await updateSettings({ coding_model: m }, projectRoot ?? undefined);
      onModelChanged?.();
    } catch {
      // Revert on failure — the next settings poll will correct it.
    }
  };

  const submitCustom = (e: React.FormEvent) => {
    e.preventDefault();
    const text = customText.trim();
    if (text === "") return;
    void selectModel(text);
  };

  // Short label: strip "sudo/" prefix for compactness.
  const shortLabel = model.replace(/^sudo\/[^/]+\//, "").replace(/^sudo\//, "") || "model";

  return (
    <div className="model-pill-wrap" ref={rootRef}>
      <button
        type="button"
        className={`model-pill${open ? " open" : ""}`}
        title={`Coding model: ${model}`}
        onClick={() => setOpen((v) => !v)}
      >
        <span className="model-pill-label">{shortLabel}</span>
        <ChevronDown size={10} aria-hidden />
      </button>
      {open && (
        <div className="model-pill-menu" role="menu">
          {COMMON_MODELS.map((m) => (
            <button
              key={m}
              type="button"
              className={`model-pill-item${m === model ? " active" : ""}`}
              onClick={() => void selectModel(m)}
            >
              {m === model && <Check size={10} aria-hidden />}
              <span>{m}</span>
            </button>
          ))}
          {customMode ? (
            <form className="model-pill-custom" onSubmit={submitCustom}>
              <input
                type="text"
                value={customText}
                placeholder="model name"
                autoFocus
                onChange={(e) => setCustomText(e.target.value)}
                onKeyDown={(e) => {
                  if (e.key === "Escape") {
                    setCustomMode(false);
                    setCustomText("");
                  }
                }}
              />
            </form>
          ) : (
            <button
              type="button"
              className="model-pill-item model-pill-other"
              onClick={() => {
                setCustomMode(true);
                setCustomText(model);
              }}
            >
              Other…
            </button>
          )}
        </div>
      )}
    </div>
  );
}
