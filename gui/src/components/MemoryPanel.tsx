import { useCallback, useEffect, useRef, useState } from "react";
import { applyMemory, errorMessage, memoryProposals, readMemory, readPins } from "../api";
import type { MemoryProposal, PendingMemoryBatch, ReadMemoryResponse, ReviewResult } from "../types";
import LoadingInline from "./LoadingInline";
import { Badge } from "./ui/badge";
import { Button } from "./ui/button";
import { cn } from "../lib/utils";

// M4 memory review (spec §7): the learner proposes rules at distill time
// (journaled as one memory_propose batch per epoch); this panel is the human
// gate — nothing is written until Apply. The batch is fetched here, not
// threaded from App: App only tracks its size for the sidebar badge.
//
// M9 P3: what was the memory review modal now renders inside the right
// panel's Memory tab — Proposals and Current files. The ledger view moved
// to LedgerPanel (the panel's Ledger tab); closing is the panel's job (⌘J).
//
// P1-P4: styles migrated to Tailwind utilities; class names survive as
// inert identity markers (e2e hooks in skills-proposals.spec/ledger.spec).
// Verdict badges are served by ui/badge.tsx. Rules shared with other panels
// stay in app.css: mem-body/mem-section-title/mem-file (LedgerPanel),
// wiki-hint/wiki-content (WikiBrowser), settings-save/settings-error
// (SettingsPanel).

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
    <div className="mem-row flex items-start justify-between gap-3 px-3 py-2.5 border-b border-[var(--border)] bg-[var(--bg-raised)]">
      <div className="mem-row-main min-w-0">
        <div className="mem-rule whitespace-pre-wrap [word-break:break-word]">{p.rule}</div>
        {p.evidence && <div className="mem-meta mt-[3px] text-[11px] text-[var(--text-dim)]">cites {p.evidence}</div>}
        {p.contradicts && <div className="mem-meta mem-meta-warn mt-[3px] text-[11px] text-[var(--warn)]">replaces: {p.contradicts}</div>}
        {p.projects != null && p.projects.length > 0 && (
          <div className="mem-meta mt-[3px] text-[11px] text-[var(--text-dim)]">seen in: {p.projects.join(", ")}</div>
        )}
      </div>
      <div className="mem-decisions flex gap-1 shrink-0">
        <button
          type="button"
          className={cn(
            "mem-decision accept bg-[var(--bg-input)] border border-[var(--border)] rounded-md py-[3px] px-2.5 text-[12px] text-[var(--text-dim)] cursor-pointer",
            !rejected && "selected text-[var(--ok-text)] border-[var(--ok-text)] bg-[rgba(63,163,95,0.15)]",
          )}
          onClick={() => onDecision(index, true)}
        >
          Accept
        </button>
        <button
          type="button"
          className={cn(
            "mem-decision reject bg-[var(--bg-input)] border border-[var(--border)] rounded-md py-[3px] px-2.5 text-[12px] text-[var(--text-dim)] cursor-pointer",
            rejected && "selected text-[var(--err-text)] border-[var(--err-text)] bg-[rgba(195,74,74,0.12)]",
          )}
          onClick={() => onDecision(index, false)}
        >
          Reject
        </button>
      </div>
    </div>
  );
}

// M9: parse name + description from a SKILL.md rule's frontmatter for
// display in the skill proposal row. Minimal parser — no external YAML dep.
function parseSkillFrontmatter(rule: string): { name: string; description: string } {
  const m = rule.match(/^---\nname:\s*(.+)\ndescription:\s*(.+)/);
  if (!m) return { name: "", description: "" };
  return { name: m[1].trim(), description: m[2].trim() };
}

// M9: SkillProposalRow — one proposed skill with tri-model review verdict
// badges and a collapsible full-content view. Skills are reject-by-default
// (stricter trust posture: skills inject into every prompt).
function SkillProposalRow({
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
  const { name, description } = parseSkillFrontmatter(p.rule);
  return (
    <div className="mem-row mem-row-skill flex items-start justify-between gap-3 px-3 py-2.5 border-b border-[var(--border)] bg-[var(--bg-raised)]">
      <div className="mem-row-main min-w-0">
        <div className="mem-skill-name font-semibold text-[13px] text-[var(--text)]">{name || p.name || "(unnamed skill)"}</div>
        {description && <div className="mem-meta mt-[3px] text-[11px] text-[var(--text-dim)]">{description}</div>}
        {p.evidence && <div className="mem-meta mt-[3px] text-[11px] text-[var(--text-dim)]">cites {p.evidence}</div>}
        {p.contradicts && <div className="mem-meta mem-meta-warn mt-[3px] text-[11px] text-[var(--warn)]">⚠ {p.contradicts}</div>}
        {p.reviews && p.reviews.length > 0 && (
          <div className="mem-verdicts flex flex-wrap gap-1 mt-1">
            {/* Verdict badges served by ui/badge.tsx variants (the .verdict-*
                rules in app.css are deleted by the DiffViewer migration);
                the className strings stay as e2e hooks. */}
            {p.reviews.map((r: ReviewResult, i: number) => (
              <Badge
                key={i}
                variant={r.verdict === "accept" || r.verdict === "reject" || r.verdict === "needs_fixes" ? (r.verdict as "accept" | "reject" | "needs_fixes") : "other"}
                className={cn("verdict-badge", `verdict-${r.verdict}`, "capitalize")}
              >
                {r.model}: {r.verdict}
              </Badge>
            ))}
          </div>
        )}
        <details className="mem-skill-details mt-1.5">
          <summary className="cursor-pointer text-[11px] text-[var(--text-dim)]">Full SKILL.md</summary>
          <pre className="wiki-content mem-file max-h-[200px] overflow-y-auto">{p.rule}</pre>
        </details>
      </div>
      <div className="mem-decisions flex gap-1 shrink-0">
        <button
          type="button"
          className={cn(
            "mem-decision accept bg-[var(--bg-input)] border border-[var(--border)] rounded-md py-[3px] px-2.5 text-[12px] text-[var(--text-dim)] cursor-pointer",
            !rejected && "selected text-[var(--ok-text)] border-[var(--ok-text)] bg-[rgba(63,163,95,0.15)]",
          )}
          onClick={() => onDecision(index, true)}
        >
          Accept
        </button>
        <button
          type="button"
          className={cn(
            "mem-decision reject bg-[var(--bg-input)] border border-[var(--border)] rounded-md py-[3px] px-2.5 text-[12px] text-[var(--text-dim)] cursor-pointer",
            rejected && "selected text-[var(--err-text)] border-[var(--err-text)] bg-[rgba(195,74,74,0.12)]",
          )}
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
  // Unmount guard: every async fetch below checks this before touching
  // state so a late resolution can't setState on a dead component.
  const mountedRef = useRef(true);
  // Reset in the setup so React StrictMode's dev double-invoke
  // (setup → cleanup → setup) leaves the ref true for the second pass;
  // a cleanup-only effect would stay false and starve every async load.
  useEffect(() => {
    mountedRef.current = true;
    return () => { mountedRef.current = false; };
  }, []);

  // (Re-)load the pending batch. Nothing pending (epoch absent/0 or no
  // proposals after the daemon's evidence veto) reads as the empty state —
  // a fresh distill supersedes an older unconsumed batch the same way.
  const refreshBatch = useCallback(async () => {
    try {
      const resp = await memoryProposals(conversationId, projectRoot ?? undefined);
      if (!mountedRef.current) return;
      if ((resp.epoch ?? 0) > 0 && (resp.proposals?.length ?? 0) > 0) {
        setBatch({
          epoch: resp.epoch ?? 0,
          seq: resp.seq ?? 0,
          proposals: resp.proposals ?? [],
          reaffirm: resp.reaffirm,
        });
        // M9: reject-by-default for skills — stricter trust posture because
        // skills inject into every prompt. User must actively accept.
        const skillRejects = new Set<number>();
        (resp.proposals ?? []).forEach((p, i) => {
          if (p.target === "skills") skillRejects.add(i);
        });
        setRejects(skillRejects);
      } else {
        setBatch(null);
        setRejects(new Set());
      }
      setError(null);
    } catch (e) {
      if (!mountedRef.current) return;
      setError(`memory proposals failed: ${errorMessage(e)}`);
      setBatch(null);
    } finally {
      if (mountedRef.current) setBatchLoading(false);
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
      if (!mountedRef.current) return;
      setFiles(mem);
      setPins(pinsResp.memory_content ?? "");
      setFilesError(null);
    } catch (e) {
      if (mountedRef.current) setFilesError(errorMessage(e));
    } finally {
      if (mountedRef.current) setFilesLoading(false);
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
      if (!mountedRef.current) return;
      const memCount = accepted.filter((a) => a.target === "memory.md").length;
      const skillCount = accepted.filter((a) => a.target === "skills").length;
      const userCount = accepted.length - memCount - skillCount;
      // The batch is now consumed; the refetch below lands on the empty
      // state and resets the decision set.
      await refreshBatch();
      if (accepted.length === 0) {
        setApplyResult("applied — all proposals rejected");
      } else {
        const summary: string[] = [];
        if (memCount > 0) summary.push(`${memCount} → memory.md`);
        if (userCount > 0) summary.push(`${userCount} → user.md`);
        if (skillCount > 0) summary.push(`${skillCount} → skills`);
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
      if (mountedRef.current) setError(`apply failed: ${errorMessage(e)}`);
    } finally {
      if (mountedRef.current) setApplyBusy(false);
    }
  };

  const memRows = batch ? byTarget(batch, "memory.md") : [];
  const userRows = batch ? byTarget(batch, "user.md") : [];
  const skillRows = batch ? byTarget(batch, "skills") : [];
  const acceptedCount = batch ? batch.proposals.length - rejects.size : 0;

  return (
    <div className="mem-panel h-full flex flex-col">
      <div className="mem-tabs flex gap-1.5 mb-3" role="tablist" aria-label="Memory sections">
        <button
          type="button"
          role="tab"
          aria-selected={tab === "proposals"}
          className={cn(
            "mem-tab bg-[var(--bg-input)] border border-[var(--border)] rounded-md text-[var(--text-dim)] px-3 py-[5px] text-[12px] cursor-pointer",
            tab === "proposals" && "active text-[var(--text)] border-[var(--accent-user)]",
          )}
          onClick={() => setTab("proposals")}
        >
          Proposals{batch ? ` (${batch.proposals.length})` : ""}
        </button>
        <button
          type="button"
          role="tab"
          aria-selected={tab === "files"}
          className={cn(
            "mem-tab bg-[var(--bg-input)] border border-[var(--border)] rounded-md text-[var(--text-dim)] px-3 py-[5px] text-[12px] cursor-pointer",
            tab === "files" && "active text-[var(--text)] border-[var(--accent-user)]",
          )}
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
                {skillRows.length > 0 && (
                  <>
                    <div className="mem-section-title">skills (proposed)</div>
                    {skillRows.map(({ p, index }) => (
                      <SkillProposalRow
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
                  <div className="mem-reaffirm px-3 py-2 text-[var(--text-dim)] text-[11px] italic">
                    The daemon will also reaffirm {batch.reaffirm?.length} existing rule(s) on
                    apply.
                  </div>
                )}
              </>
            )}
          </div>
          <div className="mem-foot flex items-center gap-3 mt-3">
            {batch && (
              <Button
                type="button"
                variant="default"
                // settings-save survives as an inert identity marker (e2e
                // skills-proposals.spec); its CSS is deleted in app.css.
                className="settings-save"
                disabled={applyBusy}
                title={`Accept ${acceptedCount}, reject ${rejects.size}`}
                onClick={() => void handleApply()}
              >
                {applyBusy ? "Applying…" : `Apply (${acceptedCount} accepted)`}
              </Button>
            )}
            {applyResult && <span className="mem-result text-[var(--ok-text)] text-[12px]">{applyResult}</span>}
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
