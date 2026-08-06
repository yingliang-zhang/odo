import { LoaderCircle } from "lucide-react";

// M11 F6: shared inline loading spinner — replaces "Loading…" text in
// panel empty states (WikiBrowser, MemoryPanel, LedgerPanel, SettingsPanel).
export default function LoadingInline({ label = "Loading" }: { label?: string }) {
  return (
    <div className="loading-inline">
      <LoaderCircle size={12} className="spin" />
      {label}…
    </div>
  );
}
