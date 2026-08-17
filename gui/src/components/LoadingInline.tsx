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

// Skeleton: content-shaped placeholder shown while a conversation's
// event history loads (bootstrap/switch). Instead of a blank spinner,
// the user sees run-group-shaped gray bars that pulse — perceptually
// faster and structurally informative (tri-model gap analysis: Hermes
// has skeletons.tsx; Odo had only LoadingInline).
export function ChatSkeleton() {
  return (
    <div className="chat-skeleton" role="status" aria-label="Loading conversation">
      {[0, 1, 2].map((i) => (
        <div className="chat-skeleton-group" key={i}>
          <div className="chat-skeleton-header" />
          <div className="chat-skeleton-bubble" style={{ width: `${60 + (i % 3) * 15}%` }} />
          <div className="chat-skeleton-bubble short" style={{ width: `${40 + (i % 2) * 20}%` }} />
        </div>
      ))}
    </div>
  );
}
