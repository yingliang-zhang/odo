import type { EventPayload, OdoEvent } from "../types";
import { cn } from "../lib/utils";

// UX-4 / A3-2 (ux-batch-lock-amendment-a3, 2026-09-01): the review-receipt
// renderer folded out of the deleted Ledger tab into the Runs panel
// (design-lock A3-1 disposition: receipts → Runs section, ledger.md file →
// Preview). Both Reads fold the same conversation journal, newest first —
// RunsPanel derives run rows and receipts from one frozen events prop
// (App's runsEventsRef keep-alive freeze, ex ledgerEventsRef).
//
// The review-decision receipts: A-P0 #1 (Guardian risk taxonomy) journal
// rows — accept/reject/auto_land_blocked/moa_review/auto_revise_round/
// refresh_attempted — rendered as decision cells (codex
// history_cell/approvals.rs parity: who decided, what, outcome). Memory
// and queue bookkeeping rows (distill, curate, todo_merge, memory_propose,
// memory_apply, run_prompt, parked_goal_dropped) keep their own surfaces —
// Plan chip, MemoryPanel, transcript badges — and would only spam here.
const REVIEW_DECISION_ACTIONS: Record<string, true> = {
  accept: true,
  reject: true,
  auto_land_blocked: true,
  moa_review: true,
  auto_revise_round: true,
  refresh_attempted: true,
};

// Receipt-eligible actions (risk.go: the W5 write sites). A missing
// risk_class on one of these means a pre-W5 row — rendered honestly as
// "unrated"; refresh_attempted is a rebase, not a verdict, so it never
// rates and never shows the chip.
const RISK_ELIGIBLE_ACTIONS: Record<string, true> = {
  accept: true,
  reject: true,
  auto_land_blocked: true,
  moa_review: true,
  auto_revise_round: true,
};

// The receipts section's filter+sort: journal rows for the decision
// actions above, newest first — the same scan ComputeAutonomy does,
// rendered. RunsPanel memos this on the same frozen events prop.
export function reviewReceipts(events: OdoEvent[]): OdoEvent[] {
  return events
    .filter((e) => e.type === "review_action" && REVIEW_DECISION_ACTIONS[e.payload?.action ?? ""] === true)
    .sort((a, b) => b.seq - a.seq);
}

// Severity ramp → the five CSS stops. Order mirrors risk.go's
// leak-cost-ranked riskClassOrder: leaking a credential or a local file
// tops it; supply-chain touches rate lowest (observational wave). A class
// this table predates falls through to the neutral "clean" style — the
// forward-compat posture ComputeAutonomy also takes.
const RISK_LEVEL_STYLE: Record<string, string> = {
  credential_probe: "critical",
  data_exfil: "high",
  destructive: "high",
  security_weakening: "medium",
  supply_chain: "low",
  none: "clean",
};

// Outcome badges the transcript doesn't already style. The .badge base +
// badge-accept/badge-reject/badge-other rules stay in app.css for this
// renderer alone — MessageBubble/DiffViewer/MemoryPanel now use the shared
// CVA Badge (ui/badge.tsx). Delete the four rules when this migrates.
// Values translated 1:1 from the deleted rules.
const OUTCOME_BADGE_UTIL: Record<string, string> = {
  "badge-blocked": "bg-[rgba(204,167,66,0.14)] text-[var(--warn)] border border-[var(--warn)]",
  "badge-refresh":
    "bg-[color-mix(in_srgb,var(--link)_14%,transparent)] text-[var(--link)] border border-[var(--link)]",
};

// Risk-class badge geometry (old .risk-badge) + the severity ramp's stops
// (old .risk-critical … .risk-clean), translated 1:1 from the deleted rules.
// The light-theme retheme of .risk-high stays in app.css behind
// [data-theme="light"] — it overrides these inline values there.
// Sizes are px literals (11px ≡ var(--text-micro), 12px ≡ var(--text-caption) —
// single :root definitions, no theme override). twMerge would otherwise
// collapse a var-size and a var-color text utility into one class.
const RISK_BADGE_GEOMETRY =
  "inline-block rounded-[10px] px-2 py-0.5 text-[11px] leading-[1.3] whitespace-nowrap";
const RISK_SEVERITY_UTIL: Record<string, string> = {
  critical: "bg-[rgba(195,74,74,0.15)] text-[var(--err-text)] border border-[var(--err)]",
  high: "bg-[rgba(209,154,74,0.14)] text-[#d19a4a] border border-[#d19a4a]",
  medium: "bg-[rgba(204,167,66,0.14)] text-[var(--warn)] border border-[var(--warn)]",
  low: "bg-[color-mix(in_srgb,var(--link)_14%,transparent)] text-[var(--link)] border border-[var(--link)]",
  clean: "bg-[var(--bg-raised)] text-[var(--text-dim)] border border-[var(--border)]",
};

// Outcome badge for one row: label + an existing badge-*/new outcome class.
function actionBadge(p: EventPayload): { label: string; cls: string } {
  switch (p.action) {
    case "accept":
      return { label: "Accepted", cls: "badge-accept" };
    case "reject":
      return { label: "Rejected", cls: "badge-reject" };
    case "auto_land_blocked":
      return { label: "Blocked", cls: "badge-blocked" };
    case "refresh_attempted":
      return { label: `Refresh ${p.outcome ?? "?"}`, cls: "badge-refresh" };
    case "moa_review": {
      const verdict = p.consensus_verdict ?? "?";
      const cls =
        verdict === "accept" ? "badge-accept" : verdict === "reject" ? "badge-reject" : "badge-other";
      return { label: `Review · ${verdict}`, cls };
    }
    case "auto_revise_round":
      return { label: `Revise round ${p.round ?? "?"}`, cls: "badge-other" };
    // Forward-compat: a write site this renderer predates still lists, plainly.
    default:
      return { label: p.action ?? "review", cls: "badge-other" };
  }
}

// One receipt row: seq anchor, outcome badge, diff ref, actor chip, Guardian
// risk-class badges (honest "unrated" for pre-W5 rows), timed-out chip,
// blocked-reason/refresh-phase detail, and the collapsed run-output log.
// The .ledger-review-* classes survive as inert identity markers (e2e hooks
// in ledger.spec.ts — MemoryPanel P1-P4 convention).
export function ReviewRow({ event }: { event: OdoEvent }) {
  const p = event.payload ?? {};
  const { label, cls } = actionBadge(p);
  // The colored tail of one row: outcome evidence. Blocked rows name their
  // reason; refresh rows name their phase; everything else stays silent.
  const detail =
    p.action === "auto_land_blocked" && p.reason
      ? p.reason
      : p.action === "refresh_attempted" && p.phase
        ? p.phase
        : null;
  // Read-only run/verify log: full verify output where journaled — blocked
  // rows carry it in `detail` (capped), landed moa_review rows in
  // verify_cmd/verify_tail. Rendered collapsed so the section stays
  // scannable and the run log stays one click away.
  const runLog: { cmd?: string; output: string } | null =
    p.action === "auto_land_blocked" && typeof p.detail === "string" && p.detail !== ""
      ? { output: p.detail }
      : p.action === "moa_review" && typeof p.verify_tail === "string" && p.verify_tail !== ""
        ? { cmd: typeof p.verify_cmd === "string" ? p.verify_cmd : undefined, output: p.verify_tail }
        : null;
  // auto_panel → "Auto"; the human click path journals no actor (the
  // handleDiffAction contract) and revise rounds may carry actor:"human" —
  // both render "Human".
  const auto = p.actor === "auto_panel";
  return (
    <div
      className={cn(
        "ledger-review-row",
        "flex items-start flex-wrap gap-1.5 px-3 py-2",
        "border-b border-[var(--border)] bg-[var(--bg-raised)]",
      )}
      data-action={p.action}
      data-seq={event.seq}
    >
      <span
        className={cn(
          "ledger-review-seq mono",
          "min-w-[40px] pt-0.5 text-[var(--text-dim)] text-[11px]",
        )}
      >
        #{event.seq}
      </span>
      <span className={cn("badge", cls, OUTCOME_BADGE_UTIL[cls])}>{label}</span>
      {p.diff_id != null && (
        <span
          className={cn(
            "ledger-review-diff",
            "pt-0.5 text-[var(--text-dim)] text-[12px]",
          )}
        >
          diff #{p.diff_id}
        </span>
      )}
      <span
        className={cn(
          "badge",
          auto ? "badge-actor-auto" : "badge-actor-human",
          auto
            ? "bg-[color-mix(in_srgb,var(--bg-run)_18%,transparent)] text-[var(--bg-run)] border border-[var(--bg-run)]"
            : "bg-[var(--bg-raised)] text-[var(--text-dim)] border border-[var(--border)]",
        )}
        title={p.actor || "human review (no actor journaled)"}
      >
        {auto ? "Auto" : "Human"}
      </span>
      {p.risk_class != null ? (
        p.risk_class.map((riskCls) => (
          <span
            key={riskCls}
            className={cn(
              "risk-badge",
              `risk-${RISK_LEVEL_STYLE[riskCls] ?? "clean"}`,
              RISK_BADGE_GEOMETRY,
              RISK_SEVERITY_UTIL[RISK_LEVEL_STYLE[riskCls] ?? "clean"],
            )}
            title={p.risk_evidence?.[riskCls] ?? (riskCls === "none" ? "rated clean" : "no evidence journaled")}
          >
            {riskCls === "none" ? "clean" : riskCls}
          </span>
        ))
      ) : (
        // Pre-W5 row (or an unreadable patch): absence is attested less,
        // never a false "clean". Only receipt-eligible verdicts flag this.
        RISK_ELIGIBLE_ACTIONS[p.action ?? ""] === true && (
          <span
            className={cn(
              "risk-badge risk-unrated",
              RISK_BADGE_GEOMETRY,
              "bg-transparent text-[var(--text-dim)] border border-dashed border-[var(--border)]",
            )}
            title="pre-W5 row — no risk receipt journaled"
          >
            unrated
          </span>
        )
      )}
      {p.timed_out === true && (
        <span
          className={cn(
            "risk-badge risk-timeout",
            RISK_BADGE_GEOMETRY,
            "bg-[rgba(204,167,66,0.22)] text-[var(--warn)] border border-[var(--warn)]",
          )}
          title="the review timed out"
        >
          timed out
        </span>
      )}
      {detail !== null && (
        <span
          className={cn(
            "ledger-review-detail",
            "pt-0.5 text-[var(--text-dim)] text-[12px] wrap-anywhere",
          )}
        >
          {detail}
        </span>
      )}
      {runLog !== null && (
        <details className={cn("ledger-run-log", "basis-full mt-1")}>
          <summary className="cursor-pointer select-none text-[var(--text-dim)] text-[12px] hover:text-[var(--text)]">
            run output
            {runLog.cmd != null && (
              <code
                className={cn(
                  "ledger-run-cmd",
                  "ml-1.5 px-1.5 py-[1px] text-[11px] text-[var(--text-dim)]",
                  "bg-[var(--code-chip-bg)] rounded-[var(--radius-sm)]",
                )}
              >
                {runLog.cmd}
              </code>
            )}
          </summary>
          <pre
            className={cn(
              "ledger-run-log-pre",
              "mt-1.5 mb-0.5 max-h-[260px] overflow-auto px-2.5 py-2",
              "bg-[var(--bg)] border border-[var(--border)] rounded-[var(--radius-sm)]",
              "text-[11.5px] leading-[1.45] whitespace-pre-wrap wrap-anywhere",
            )}
          >
            {runLog.output}
          </pre>
        </details>
      )}
    </div>
  );
}
