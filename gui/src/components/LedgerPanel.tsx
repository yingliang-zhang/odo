import { useEffect, useState } from "react";
import { errorMessage, ledger } from "../api";

// M9 P3: the ledger view, lifted out of the memory review modal into the
// right panel's Ledger tab. The panel remounts on each tab visit, so every
// visit re-reads .odo/ledger.md (the daemon is the only writer) — same
// cadence as the modal's tab-activation refetch. conversationId is part of
// the panel-tab wiring contract; the ledger itself is project-global.
interface Props {
  conversationId: number;
  // M11 P1: the ledger read routes to this project's daemon; null =
  // bridge default. App remounts the panel on project switch.
  projectRoot?: string | null;
}

export default function LedgerPanel({ projectRoot }: Props) {
  const [content, setContent] = useState<string | null>(null);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState<string | null>(null);

  useEffect(() => {
    let cancelled = false;
    (async () => {
      try {
        const resp = await ledger(projectRoot ?? undefined);
        if (cancelled) return;
        setContent(resp.memory_content ?? "");
        setError(null);
      } catch (e) {
        if (!cancelled) setError(errorMessage(e));
      } finally {
        if (!cancelled) setLoading(false);
      }
    })();
    return () => {
      cancelled = true;
    };
  }, [projectRoot]);

  return (
    <div className="mem-body">
      {loading && <div className="wiki-hint">Loading…</div>}
      {error && <div className="wiki-hint">read failed: {error}</div>}
      {!loading && content !== null && (
        <>
          <div className="mem-section-title">ledger.md (daemon-written, verified metrics)</div>
          <pre className="wiki-content mem-file">{content || "(empty — distill to write the first section)"}</pre>
        </>
      )}
    </div>
  );
}
