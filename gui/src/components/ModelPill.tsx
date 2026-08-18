import { useEffect, useState } from "react";
import { ChevronDown, Check } from "lucide-react";
import { updateSettings } from "../api";
import {
  DropdownMenu,
  DropdownMenuTrigger,
  DropdownMenuContent,
  DropdownMenuItem,
  DropdownMenuSeparator,
} from "./ui/dropdown-menu";
import { cn } from "../lib/utils";

/**
 * ModelPill — compact dropdown in the composer for switching the coding model.
 *
 * Migrated to Radix DropdownMenu (Phase 4):
 * - Portal positioning, viewport collision, keyboard nav — all Radix.
 * - Esc gate: onEscapeKeyDown stopPropagation in DropdownMenuContent.
 * - Old hand-rolled click-away/Esc/positioning code deleted.
 */

const COMMON_MODELS = [
  "sudo/t9s/kimi-k3",
  "sudo/glm-5.2",
  "sudo/t9s/deepseek-v4-flash",
  "sudo/t9s/claude-sonnet-4",
  "sudo/t9s/gpt-5",
  "sudo/t9s/gpt-5.6-sol",
  "sudo/t9s/kimi-k2.7-code",
];

interface Props {
  projectRoot?: string | null;
  currentModel?: string | null;
  onModelChanged?: () => void;
}

export default function ModelPill({ projectRoot, currentModel, onModelChanged }: Props) {
  const [model, setModel] = useState(currentModel ?? "");
  const [customMode, setCustomMode] = useState(false);
  const [customText, setCustomText] = useState("");

  // Sync from parent when settings reload.
  useEffect(() => {
    setModel(currentModel ?? "");
  }, [currentModel]);

  const selectModel = async (m: string) => {
    setModel(m);
    setCustomMode(false);
    try {
      await updateSettings({ coding_model: m }, projectRoot ?? undefined);
      onModelChanged?.();
    } catch {
      setModel(currentModel ?? "");
    }
  };

  // Short label: strip "sudo/" prefix for compactness.
  const shortLabel = model.replace(/^sudo\/[^/]+\//, "").replace(/^sudo\//, "") || "model";

  return (
    <div className="model-pill-wrap min-h-[38px] flex items-center">
      <DropdownMenu>
        <DropdownMenuTrigger asChild>
          <button
            type="button"
            className={cn(
              "model-pill min-h-[38px] flex items-center gap-1",
              "bg-[var(--bg-input)] border border-[var(--border)]",
              "rounded-[var(--radius-md)] text-[var(--text-dim)] text-xs",
              "px-2 py-1 cursor-pointer hover:text-[var(--text)] hover:border-[var(--accent)]",
              "data-[state=open]:text-[var(--text)] data-[state=open]:border-[var(--accent)]",
            )}
            title={`Coding model: ${model}`}
            aria-label={`Coding model: ${shortLabel}`}
          >
            <span className="model-pill-label max-w-[120px] truncate">{shortLabel}</span>
            <ChevronDown size={10} aria-hidden />
          </button>
        </DropdownMenuTrigger>
        <DropdownMenuContent align="end" side="top" sideOffset={4}>
          {COMMON_MODELS.map((m) => (
            <DropdownMenuItem
              key={m}
              onClick={() => void selectModel(m)}
              className={m === model ? "font-medium" : ""}
            >
              {m === model && <Check size={10} aria-hidden />}
              <span className={m === model ? "" : "ml-[14px]"}>{m}</span>
            </DropdownMenuItem>
          ))}
          <DropdownMenuSeparator />
          {customMode ? (
            <div className="px-2 py-1" onKeyDown={(e) => e.stopPropagation()}>
              <input
                type="text"
                value={customText}
                placeholder="model name"
                autoFocus
                aria-label="Custom coding model name"
                className={cn(
                  "w-full bg-transparent border-none text-xs text-[var(--text)]",
                  "outline-none px-1 py-1",
                )}
                onChange={(e) => setCustomText(e.target.value)}
                onKeyDown={(e) => {
                  if (e.key === "Enter") {
                    e.preventDefault();
                    e.stopPropagation();
                    const text = customText.trim();
                    if (text !== "") void selectModel(text);
                  }
                }}
              />
            </div>
          ) : (
            <DropdownMenuItem
              onSelect={(e) => {
                // Prevent Radix from auto-closing the menu on item select.
                e.preventDefault();
                setCustomMode(true);
                setCustomText(model);
              }}
            >
              <span className="ml-[14px]">Other…</span>
            </DropdownMenuItem>
          )}
        </DropdownMenuContent>
      </DropdownMenu>
    </div>
  );
}
