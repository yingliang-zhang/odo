import { useCallback, useEffect, useState } from "react";
import { applyMemory, errorMessage, memoryProposals, readMemory, readPins } from "../api";
import type { MemoryProposal, PendingMemoryBatch, ReadMemoryResponse } from "../types";
import LoadingInline from "./LoadingInline";

// M4 memory review (spec §7): the learner proposes rules at distill time
// (journaled as one memory_propose batch per epoch); this panel is the human
// gate — nothing is written until Apply. The batch is fetched here, not
// threaded from App: App only tracks its size for the sidebar badge.
//
// M9 P3: what was the memory review modal now renders inside the right
// panel's Memory tab — Proposals and Current files. The ledger view moved
// to LedgerPanel (the panel's Ledger tab); closing is the panel's job (⌘J).

interface Props {
  conversationId: number;
  workstreamName?: string | null;
  // Initial sub-tab: "proposals" (default) or "files" (toast click-through).
  initialTab?: "proposals" | "files";
  // Fired after a successful apply so App can re-read the pending count.
  onApplied?: () => void;
  // M11 P1: all reads/writes route to this project's daemon; null = bridge
  // default. App remounts the panel on project switch.
  projectRoot?: string | null;
}

// Split the mixed proposals array into per-target sections while keeping
// the original indexes — apply_memory addresses proposals by their position
// in the full batch array, exactly as the daemon validates them.
function byTarget(batch: PendingMemoryBatch, target: MemoryProposal["target"]) {
  return batch.proposals.map((p, index) => ({ p, index })).filter(({ p }) => p.target === target);
}

// One proposal row: rule + provenance + Accept/Reject (Accept is the
// default, per spec §7; the daemon composes rejected indexes itself).
function ProposalRow({
  p,
  index,
  rejected,
  onDecision,
}: {
  p: MemoryProposal;
  index: number;
  rejected: boolean;
  onDecision: (index: number, accept: boolean) => void;
}) {
  return (
    <div className="mem-row">
      <div className="mem-row-main">
        <div className="mem-rule">{p.rule}</div>
        {p.evidence && <div className="mem-meta">cites {p.evidence}</div>}
        {p.contradicts && <div className="mem-meta mem-meta-warn">replaces: {p.contradicts}</div>}
        {p.projects != null && p.projects.length > 0 && (
          <div className="mem-meta">seen in: {p.projects.join(", ")}</div>
        )}
      </div>
      <div className="mem-decisions">
        <button
          type="button"
          className={`mem-decision accept${rejected ? "" : " selected"}`}
          onClick={() => onDecision(index, true)}
        >
          Accept
        </button>
        <button
          type="button"
          className={`mem-decision reject${rejected ? " selected" : ""}`}
          onClick={() => onDecision(index, false)}
        >
          Reject
        </button>
      </div>
    </div>
  );
}

export default function MemoryPanel({ conversationId, workstreamName, initialTab, onApplied, projectRoot }: Props) {
  const [tab, setTab] = useState<"proposals" | "files">(initialTab ?? "proposals");
  const [batch, setBatch] = useState<PendingMemoryBatch | null>(null);
  const [batchLoading, setBatchLoading] = useState(true);
  const [error, setError] = useState<string | null>(null);
  // Rejected proposal indexes; everything not rejected is accepted.
  const [rejects, setRejects] = useState<Set<number>>(new Set());
  const [applyBusy, setApplyBusy] = useState(false);
  const [applyResult, setApplyResult] = useState<string | null>(null);
  const [files, setFiles] = useState<ReadMemoryResponse | null>(null);
  // M5: pins.md reads come from read_pins (separate daemon command) so the
  // files tab can show them beside memory.md and user.md.
  const [pins, setPins] = useState<string | null>(null);
  const [filesLoading, setFilesLoading] = useState(false);
  const [filesError, setFilesError] = useState<string | null>(null);

  // (Re-)load the pending batch. Nothing pending (epoch absent/0 or no
  // proposals after the daemon's evidence veto) reads as the empty state —
  // a fresh distill supersedes an older unconsumed batch the same way.
  const refreshBatch = useCallback(async () => {
    try {
      const resp = await memoryProposals(conversationId, projectRoot ?? undefined);
      if ((resp.epoch ?? 0) > 0 && (resp.proposals?.length ?? 0) > 0) {
        setBatch({
          epoch: resp.epoch ?? 0,
          seq: resp.seq ?? 0,
          proposals: resp.proposals ?? [],
          reaffirm: resp.reaffirm,
        });
      } else {
        setBatch(null);
      }
      setRejects(new Set());
      setError(null);
    } catch (e) {
      setError(`memory proposals failed: ${errorMessage(e)}`);
      setBatch(null);
    } finally {
      setBatchLoading(false);
    }
  }, [conversationId, projectRoot]);

  useEffect(() => {
    void refreshBatch();
  }, [refreshBatch]);

  // Reader tab: the daemon constructs all canonical paths itself
  // (read_memory/read_pins take no user-supplied path). Refetched on
  // activation and after an apply.
  const loadFiles = useCallback(async () => {
    setFilesLoading(true);
    try {
      const [mem, pinsResp] = await Promise.all([
        readMemory(projectRoot ?? undefined),
        readPins(projectRoot ?? undefined),
      ]);
      setFiles(mem);
      setPins(pinsResp.memory_content ?? "");
      setFilesError(null);
    } catch (e) {
      setFilesError(errorMessage(e));
    } finally {
      setFilesLoading(false);
    }
  }, [projectRoot]);

  useEffect(() => {
    if (tab === "files") void loadFiles();
  }, [tab, loadFiles]);

  const handleDecision = (index: number, accept: boolean) => {
    setRejects((prev) => {
      const next = new Set(prev);
      if (accept) {
        next.delete(index);
      } else {
        next.add(index);
      }
      return next;
    });
  };

  const handleApply = async () => {
    if (!batch || applyBusy) return;
    const accepted = batch.proposals.flatMap((p, index) =>
      rejects.has(index) ? [] : [{ target: p.target, index }],
    );
    setApplyBusy(true);
    setApplyResult(null);
    setError(null);
    try {
      const resp = await applyMemory(
        { conversationId, epoch: batch.epoch, accepted },
        projectRoot ?? undefined,
      );
      if (!resp.applied) throw new Error("daemon did not confirm the apply");
      const memCount = accepted.filter((a) => a.target === "memory.md").length;
      const userCount = accepted.length - memCount;
      // The batch is now consumed; the refetch below lands on the empty
      // state and resets the decision set.
      await refreshBatch();
      if (accepted.length === 0) {
        setApplyResult("applied — all proposals rejected");
      } else {
        const summary: string[] = [];
        if (memCount > 0) summary.push(`${memCount} → memory.md`);
        if (userCount > 0) summary.push(`${userCount} → user.md`);
        setApplyResult(
          `applied — ${accepted.length} rule${accepted.length === 1 ? "" : "s"}${
            summary.length > 0 ? ` (${summary.join(", ")})` : ""
          }`,
        );
      }
      if (tab === "files") void loadFiles();
      onApplied?.();
    } catch (e) {
      // A refusal (e.g. user.md would overflow) leaves the batch pending —
      // the rows stay editable for a retry.
      setError(`apply failed: ${errorMessage(e)}`);
    } finally {
      setApplyBusy(false);
    }
  };

  const memRows = batch ? byTarget(batch, "memory.md") : [];
  const userRows = batch ? byTarget(batch, "user.md") : [];
  const acceptedCount = batch ? batch.proposals.length - rejects.size : 0;

  return (
    <div className="mem-panel">
      <div className="mem-tabs">
        <button
          type="button"
          className={`mem-tab${tab === "proposals" ? " active" : ""}`}
          onClick={() => setTab("proposals")}
        >
          Proposals{batch ? ` (${batch.proposals.length})` : ""}
        </button>
        <button
          type="button"
          className={`mem-tab${tab === "files" ? " active" : ""}`}
          onClick={() => setTab("files")}
        >
          Current files
        </button>
      </div>

      {error && <div className="settings-error">{error}</div>}

      {tab === "proposals" && (
        <>
          <div className="mem-body">
            {batchLoading && <LoadingInline />}
            {!batchLoading && !batch && (
              <div className="wiki-hint">
                No pending memory proposals{workstreamName ? ` for ${workstreamName}` : ""}.
                Distill this conversation to let the learner propose rules from the new epoch note.
              </div>
            )}
            {!batchLoading && batch && (
              <>
                <div className="mem-section-title">memory.md (project)</div>
                {memRows.length === 0 && (
                  <div className="wiki-hint">No project rules in this batch.</div>
                )}
                {memRows.map(({ p, index }) => (
                  <ProposalRow
                    key={index}
                    p={p}
                    index={index}
                    rejected={rejects.has(index)}
                    onDecision={handleDecision}
                  />
                ))}
                {userRows.length > 0 && (
                  <>
                    <div className="mem-section-title">user.md (global)</div>
                    {userRows.map(({ p, index }) => (
                      <ProposalRow
                        key={index}
                        p={p}
                        index={index}
                        rejected={rejects.has(index)}
                        onDecision={handleDecision}
                      />
                    ))}
                  </>
                )}
                {(batch.reaffirm?.length ?? 0) > 0 && (
                  <div className="mem-reaffirm">
                    The daemon will also reaffirm {batch.reaffirm?.length} existing rule(s) on
                    apply.
                  </div>
                )}
              </>
            )}
          </div>
          <div className="mem-foot">
            {batch && (
              <button
                type="button"
                className="settings-save"
                disabled={applyBusy}
                title={`Accept ${acceptedCount}, reject ${rejects.size}`}
                onClick={() => void handleApply()}
              >
                {applyBusy ? "Applying…" : `Apply (${acceptedCount} accepted)`}
              </button>
            )}
            {applyResult && <span className="mem-result">{applyResult}</span>}
          </div>
        </>
      )}

      {tab === "files" && (
        <div className="mem-body">
          {filesLoading && <LoadingInline />}
          {filesError && <div className="wiki-hint">read failed: {filesError}</div>}
          {files && !filesLoading && (
            <>
              <div className="mem-section-title">memory.md (current)</div>
              <pre className="wiki-content mem-file">{files.memory_content || "(empty)"}</pre>
              <div className="mem-section-title">memory-archive.md (append-only)</div>
              <pre className="wiki-content mem-file">{files.archive_content || "(empty)"}</pre>
              <div className="mem-section-title">user.md (global)</div>
              <pre className="wiki-content mem-file">{files.user_content || "(empty)"}</pre>
              <div className="mem-section-title">pins.md (user-authored, verbatim)</div>
              <pre className="wiki-content mem-file">{pins || "(empty)"}</pre>
            </>
          )}
        </div>
      )}
    </div>
  );
}
