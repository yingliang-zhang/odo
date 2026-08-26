import { memo, useCallback, useEffect, useRef, useState } from "react";
import { applyMemory, errorMessage, memoryProposals, readMemory, readPins, resolveHealConflict } from "../api";
import type { AutoDistillCapResume, MemoryProposal, PendingMemoryBatch, ReadMemoryResponse, ReviewResult, StrandedOp } from "../types";
import LoadingInline from "./LoadingInline";
import { Badge } from "./ui/badge";
import { Button } from "./ui/button";
import { cn } from "../lib/utils";

// M4 memory review (spec §7) → panel-gated apply: the learner proposes
// rules at distill time (journaled as one memory_propose batch per epoch)
// and the review PANEL decides by default — the daemon auto-applies right
// after the fold commits. This tab is the outcome surface first (what the
// panel accepted/discarded, with per-model verdicts) and the human
// fallback second: an undecided batch (no review models configured, or a
// refused auto-apply left pending) still renders the Accept/Reject gate.
// The batch is fetched here, not threaded from App: App only tracks its
// actionable size for the sidebar badge.
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
  // Sub-tab deep link (toast click-through → "files", bare memory badge →
  // "proposals"), shaped like WikiBrowser's focus ({tab, n}): the counter
  // re-fires identical requests. It was an initialTab string when App
  // remounted this panel on every activation; with the panel's keep-alive
  // tabs (App mounts once, then CSS-hides) a constructor-time initial
  // value could never apply a later deep link, so this is a request
  // object consumed by the effect below, not just initial state.
  focus?: { tab: "proposals" | "files"; n: number } | null;
  // Fired after a successful apply so App can re-read the pending count.
  onApplied?: () => void;
  // M11 P1: all reads/writes route to this project's daemon; null = bridge
  // default. App remounts the panel on project switch.
  projectRoot?: string | null;
  // Keep-alive activation edge (2026-08-25 review P1): App mounts the
  // panel once and CSS-hides it; new distill epochs propose new batches,
  // the panel-gated auto-apply consumes them, and memory.md/pins.md move
  // — all while hidden. On inactive→active the batch and (when open) the
  // files re-fetch. Draft protection: an unchanged batch identity keeps
  // the user's in-flight Accept/Reject decisions (see refreshBatch).
  active: boolean;
  // Stranded crash-recoveries (2026-08-26 memory-replay doctrine):
  // strandedTotal and strandedOps BOTH ride pending_counts, project-wide
  // (round-3 FIX F — every counted conflict renders an actionable row,
  // Resolve restores the stranded body, Dismiss records the decision).
  // onResolved re-reads pending_counts after a decision so banner and
  // rows converge ahead of the poll cadence.
  strandedTotal?: number;
  strandedOps?: StrandedOp[];
  onResolved?: () => void;
  // Daily-cap suspension disclosure (2026-08-26 storm fix): the daemon's
  // earliest quota release, riding pending_counts; null while uncapped or
  // past the horizon.
  autoDistillCapResume?: AutoDistillCapResume | null;
  // FIX 3: the chip also gates on the auto-distill pref — App derives it
  // from the fetched settings ("never" hides the chip regardless of what
  // a stale poll carried).
  autoDistillEnabled?: boolean;
}

// Split the mixed proposals array into per-target sections while keeping
// the original indexes — apply_memory addresses proposals by their position
// in the full batch array, exactly as the daemon validates them.
function byTarget(batch: PendingMemoryBatch, target: MemoryProposal["target"]) {
  return batch.proposals.map((p, index) => ({ p, index })).filter(({ p }) => p.target === target);
}

// Per-model verdict badges for a gated proposal. Verdict badges are served
// by ui/badge.tsx variants (the .verdict-* rules in app.css are deleted by
// the DiffViewer migration); the className strings stay as e2e hooks.
function VerdictBadges({ reviews }: { reviews: ReviewResult[] }) {
  return (
    <div className="mem-verdicts flex flex-wrap gap-1 mt-1">
      {reviews.map((r: ReviewResult, i: number) => (
        <Badge
          key={i}
          variant={r.verdict === "accept" || r.verdict === "reject" || r.verdict === "needs_fixes" ? (r.verdict as "accept" | "reject" | "needs_fixes") : "other"}
          className={cn("verdict-badge", `verdict-${r.verdict}`, "capitalize")}
        >
          {r.model}: {r.verdict}
        </Badge>
      ))}
    </div>
  );
}

// The decision side of a proposal row: Accept/Reject buttons while the
// batch is actionable (human fallback), or the recorded outcome chip once
// consumed (panel or human decided — read-only history).
function DecisionArea({
  index,
  rejected,
  outcome,
  onDecision,
}: {
  index: number;
  rejected: boolean;
  outcome?: "accepted" | "discarded";
  onDecision: (index: number, accept: boolean) => void;
}) {
  if (outcome != null) {
    return (
      <div className="mem-decisions flex gap-1 shrink-0">
        <Badge
          variant={outcome === "accepted" ? "accept" : "reject"}
          className={cn("mem-outcome", `mem-outcome-${outcome}`, "capitalize")}
        >
          {outcome}
        </Badge>
      </div>
    );
  }
  return (
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
  );
}

// One proposal row: rule + provenance + panel verdicts + decision area
// (Accept is the default while actionable, per spec §7; the daemon
// composes rejected indexes itself).
function ProposalRow({
  p,
  index,
  rejected,
  outcome,
  onDecision,
}: {
  p: MemoryProposal;
  index: number;
  rejected: boolean;
  outcome?: "accepted" | "discarded";
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
        {p.reviews != null && p.reviews.length > 0 && <VerdictBadges reviews={p.reviews} />}
      </div>
      <DecisionArea index={index} rejected={rejected} outcome={outcome} onDecision={onDecision} />
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

// M9: SkillProposalRow — one proposed skill with panel review verdict
// badges and a collapsible full-content view. While the batch is
// actionable, skills are reject-by-default (stricter trust posture: skills
// inject into every prompt); once consumed the row is read-only history
// like every other target.
function SkillProposalRow({
  p,
  index,
  rejected,
  outcome,
  onDecision,
}: {
  p: MemoryProposal;
  index: number;
  rejected: boolean;
  outcome?: "accepted" | "discarded";
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
        {p.reviews != null && p.reviews.length > 0 && <VerdictBadges reviews={p.reviews} />}
        <details className="mem-skill-details mt-1.5">
          <summary className="cursor-pointer text-[11px] text-[var(--text-dim)]">Full SKILL.md</summary>
          <pre className="wiki-content mem-file max-h-[200px] overflow-y-auto">{p.rule}</pre>
        </details>
      </div>
      <DecisionArea index={index} rejected={rejected} outcome={outcome} onDecision={onDecision} />
    </div>
  );
}

function MemoryPanel({ conversationId, workstreamName, focus, onApplied, projectRoot, active, strandedTotal, strandedOps, onResolved, autoDistillCapResume, autoDistillEnabled = true }: Props) {
  const [tab, setTab] = useState<"proposals" | "files">(focus?.tab ?? "proposals");
  // External sub-tab requests. This effect is what keeps deep links
  // working under the panel's keep-alive tabs: the panel now survives
  // tab switches, so a request arriving after first mount could never
  // reach the useState initial value — the effect (and the n nonce,
  // which re-fires repeated same-target requests) applies it instead.
  // WikiBrowser's focus is the same pattern.
  useEffect(() => {
    if (focus) setTab(focus.tab);
  }, [focus]);
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
  // Per-op resolve in-flight guard (keyed `conv:layer:receiptSeq`).
  const [strandBusy, setStrandBusy] = useState<string | null>(null);
  const ops = strandedOps ?? [];
  const total = strandedTotal ?? 0;
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

  // Mirror of the batch state for refresh-time identity comparison (the
  // async callback below can't read the closure's stale `batch`).
  const batchRef = useRef<PendingMemoryBatch | null>(null);

  // (Re-)load the latest batch: actionable when unconsumed (the human
  // fallback), read-only outcome view once consumed (the panel-gated path
  // decides inside the distill; a human apply counts too). No batch for
  // the latest epoch reads as the empty state.
  const refreshBatch = useCallback(async () => {
    try {
      const resp = await memoryProposals(conversationId, projectRoot ?? undefined);
      if (!mountedRef.current) return;
      if ((resp.epoch ?? 0) > 0 && (resp.proposals?.length ?? 0) > 0) {
        const consumed = resp.consumed ?? false;
        const next = {
          epoch: resp.epoch ?? 0,
          seq: resp.seq ?? 0,
          proposals: resp.proposals ?? [],
          reaffirm: resp.reaffirm,
          consumed,
          applyActor: resp.apply_actor,
          accepted: resp.accepted ?? [],
          rejected: resp.rejected ?? [],
        };
        // Draft protection (2026-08-25 review P1): an activation-driven
        // refresh that returns the SAME batch identity must not wipe the
        // user's in-flight Accept/Reject toggles — only a genuinely new
        // or newly-consumed batch resets decisions to the default.
        const prev = batchRef.current;
        const sameBatch =
          prev != null &&
          prev.epoch === next.epoch &&
          prev.seq === next.seq &&
          prev.consumed === next.consumed &&
          prev.proposals.length === next.proposals.length;
        batchRef.current = next;
        setBatch(next);
        // M9: reject-by-default for skills — stricter trust posture because
        // skills inject into every prompt. User must actively accept. A
        // consumed batch is read-only; the decision set is irrelevant.
        const skillRejects = new Set<number>();
        if (!consumed) {
          (resp.proposals ?? []).forEach((p, i) => {
            if (p.target === "skills") skillRejects.add(i);
          });
        }
        setRejects((cur) => (sameBatch ? cur : skillRejects));
      } else {
        batchRef.current = null;
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

  // Activation edge (the Props contract): refetch the batch, and the
  // files when that sub-tab is open. refreshBatch keeps in-flight
  // decisions for an unchanged batch identity.
  const wasActiveRef = useRef(active);
  useEffect(() => {
    if (!wasActiveRef.current && active) {
      void refreshBatch();
      if (tab === "files") void loadFiles();
    }
    wasActiveRef.current = active;
  }, [active, refreshBatch, loadFiles, tab]);

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

  // Resolve/Dismiss one journaled heal_conflict (2026-08-26 doctrine):
  // the daemon restores from the row's embedded stranded body (never the
  // client), journals heal_resolved on the row's OWNING conversation
  // (round-3 FIX F — the request routes by it even when the human acts
  // from a different lane's Memory tab), and the refreshed pending_counts
  // drops the row and the banner count ahead of the poll cadence.
  const handleResolve = async (op: StrandedOp, dismissed: boolean) => {
    const key = `${op.strandedConversation}:${op.layer}:${op.receiptSeq}`;
    if (strandBusy === key) return;
    setStrandBusy(key);
    setError(null);
    try {
      const resp = await resolveHealConflict(
        { conversationId: op.strandedConversation, layer: op.layer, receiptSeq: op.receiptSeq, strandedConversation: op.strandedConversation, dismissed },
        projectRoot ?? undefined,
      );
      if (!resp.applied) throw new Error("daemon did not confirm the resolution");
      if (!mountedRef.current) return;
      if (tab === "files") void loadFiles();
      onResolved?.();
    } catch (e) {
      if (mountedRef.current) setError(`resolve failed: ${errorMessage(e)}`);
    } finally {
      if (mountedRef.current) setStrandBusy(null);
    }
  };

  const memRows = batch ? byTarget(batch, "memory.md") : [];
  const userRows = batch ? byTarget(batch, "user.md") : [];
  const skillRows = batch ? byTarget(batch, "skills") : [];
  const acceptedCount = batch ? batch.proposals.length - rejects.size : 0;
  // Consumed batch → per-row outcome from the apply row's accepted refs
  // (daemon-computed; everything else was rejected). Dynamic membership —
  // Set by convention.
  const acceptedIdx = new Set<number>((batch?.accepted ?? []).map((a) => a.index));
  const outcomeFor = (index: number): "accepted" | "discarded" | undefined =>
    batch?.consumed ? (acceptedIdx.has(index) ? "accepted" : "discarded") : undefined;

  return (
    <div className="mem-panel h-full flex flex-col">
      {(total > 0 || ops.length > 0) && (
        <div className="mem-stranded mb-3 rounded-md border border-[var(--warn)] bg-[rgba(211,156,53,0.08)] px-3 py-2">
          <div className="mem-stranded-copy text-[12px] text-[var(--warn)]">
            {total > 0 ? `${total} stranded crash-recoveries — open to review` : "Stranded crash-recoveries — open to review"}
          </div>
          {ops.map((op) => {
            const key = `${op.strandedConversation}:${op.layer}:${op.receiptSeq}`;
            return (
              <div key={key} className="mem-stranded-row mt-2 flex items-center justify-between gap-2">
                <div className="min-w-0 text-[11px] text-[var(--text-dim)]">
                  <span className="font-medium text-[var(--text)]">{op.layer}</span>
                  {op.detail != null && <span className="mem-stranded-detail"> · {op.detail}</span>}
                </div>
                <div className="flex shrink-0 gap-1">
                  <button
                    type="button"
                    className="mem-stranded-resolve bg-[var(--bg-input)] border border-[var(--border)] rounded-md py-[3px] px-2.5 text-[12px] text-[var(--ok-text)] cursor-pointer disabled:opacity-50"
                    disabled={strandBusy === key}
                    onClick={() => void handleResolve(op, false)}
                  >
                    Resolve
                  </button>
                  <button
                    type="button"
                    className="mem-stranded-dismiss bg-[var(--bg-input)] border border-[var(--border)] rounded-md py-[3px] px-2.5 text-[12px] text-[var(--text-dim)] cursor-pointer disabled:opacity-50"
                    disabled={strandBusy === key}
                    onClick={() => void handleResolve(op, true)}
                  >
                    Dismiss
                  </button>
                </div>
              </div>
            );
          })}
        </div>
      )}
      {/* Daily-cap suspension chip (2026-08-26 storm fix): the daemon
          discloses the earliest quota release on pending_counts. FIX 3:
          hidden while auto-distill is disabled, and never rendered past
          the horizon even if a stale poll value lingers. */}
      {autoDistillEnabled &&
        autoDistillCapResume != null &&
        autoDistillCapResume.resume_at_unix * 1000 > Date.now() && (
          <div
            className="mem-cap-notice mb-3 rounded-md border border-[var(--warn)] bg-[rgba(211,156,53,0.08)] px-3 py-2"
            data-computed={autoDistillCapResume.computed || undefined}
          >
            <div className="text-[12px] text-[var(--warn)]">
              {`今日额度已用完 · 预计恢复 ${new Date(autoDistillCapResume.resume_at_unix * 1000).toLocaleTimeString([], { hour: "2-digit", minute: "2-digit" })}`}
            </div>
          </div>
        )}
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
                <div className="mem-batch-status px-3 py-2 text-[11px] text-[var(--text-dim)]">
                  {batch.consumed
                    ? `epoch ${batch.epoch} batch — ${
                        batch.applyActor === "auto_panel"
                          ? "decided by the review panel"
                          : "applied by hand"
                      } (${(batch.accepted ?? []).length} accepted, ${(batch.rejected ?? []).length} discarded)`
                    : `epoch ${batch.epoch} batch — awaiting decision (no panel decision recorded)`}
                </div>
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
                    outcome={outcomeFor(index)}
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
                        outcome={outcomeFor(index)}
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
                        outcome={outcomeFor(index)}
                        onDecision={handleDecision}
                      />
                    ))}
                  </>
                )}
                {(batch.reaffirm?.length ?? 0) > 0 && (
                  <div className="mem-reaffirm px-3 py-2 text-[var(--text-dim)] text-[11px] italic">
                    {batch.consumed
                      ? `The daemon also reaffirmed ${batch.reaffirm?.length} existing rule(s) on apply.`
                      : `The daemon will also reaffirm ${batch.reaffirm?.length} existing rule(s) on apply.`}
                  </div>
                )}
              </>
            )}
          </div>
          <div className="mem-foot flex items-center gap-3 mt-3">
            {batch && !batch.consumed && (
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

// Keep-alive panel (tri-review P2 #5, 2026-08-24): App keeps this
// component mounted under the ContextPanel `hidden` tabs and hands it
// only referentially stable props (useCallback handlers + diff_stable
// prev-bails), so the default shallow compare skips re-rendering the
// hidden subtree on quiet poll ticks. Same convention as
// memo(ChatSurface) — no custom comparator.
export default memo(MemoryPanel);
