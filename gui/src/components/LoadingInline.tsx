import { LoaderCircle } from "lucide-react";

// M11 F6: shared inline loading spinner — replaces "Loading…" text in
// panel empty states (WikiBrowser, MemoryPanel, LedgerPanel, SettingsPanel).
// Styles migrated to Tailwind utilities (P1-P4); class names survive as
// inert identity markers, `.spin` keeps its CSS rule (shared with
// StatusBar/ChatSurface) and drives the SVG rotation.
export default function LoadingInline({ label = "Loading" }: { label?: string }) {
  return (
    <div className="loading-inline inline-flex items-center gap-1.5 text-[var(--text-dim)] text-[12px]">
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
// The pulse keyframes stay in app.css (@keyframes skeleton-pulse); each bar
// references them via an arbitrary animation utility matching the old CSS
// (delay 0.1s, short bars 0.2s and 12px tall).
export function ChatSkeleton() {
  return (
    <div className="chat-skeleton p-4 flex flex-col gap-4" role="status" aria-label="Loading conversation">
      {[0, 1, 2].map((i) => (
        <div className="chat-skeleton-group flex flex-col gap-2" key={i}>
          <div className="chat-skeleton-header w-[120px] h-2.5 rounded bg-[var(--bg-raised)] [animation:skeleton-pulse_1.4s_ease-in-out_infinite]" />
          <div
            className="chat-skeleton-bubble h-4 rounded-md bg-[var(--bg-raised)] [animation:skeleton-pulse_1.4s_ease-in-out_infinite_0.1s]"
            style={{ width: `${60 + (i % 3) * 15}%` }}
          />
          <div
            className="chat-skeleton-bubble short h-3 rounded-md bg-[var(--bg-raised)] [animation:skeleton-pulse_1.4s_ease-in-out_infinite_0.2s]"
            style={{ width: `${40 + (i % 2) * 20}%` }}
          />
        </div>
      ))}
    </div>
  );
}
