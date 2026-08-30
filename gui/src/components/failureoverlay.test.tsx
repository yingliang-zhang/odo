import { cleanup, fireEvent, render, screen } from "@testing-library/react";
import { afterEach, describe, expect, it, vi } from "vitest";

// P2.5 (docs/design/adoption-lock.md): the typed failure overlay. Pins the
// leaf contract the parent mounts: data-slot + role="alert", title + cause
// per spec, exactly the ONE leading action button the taxonomy's
// spec.action names (the other two must be ABSENT, not disabled), the ×
// dismiss, and the raw error string riding the cause line's title
// attribute only — never visible text. Classification itself lives in
// errors.test.ts; these tests exercise the taxonomy's real specs so the
// pair of files pins the full class → render pipeline.

import FailureOverlay, { type FailureOverlayProps } from "./FailureOverlay";
import { FAILURE_TAXONOMY, type FailureClass, type FailureSpec } from "../errors";
import { SLOT } from "../slots";

const RAW = "poll failed: connect /tmp/proj/.odo/odo.sock: connection refused (os error 61)";

type ActionCallback = "onReconnect" | "onCopyDiagnostics" | "onOpenJournal";

const ACTION_LABELS: { action: FailureSpec["action"]; label: string; cb: ActionCallback }[] = [
  { action: "reconnect", label: "Reconnect", cb: "onReconnect" },
  { action: "copy_diagnostics", label: "Copy diagnostics", cb: "onCopyDiagnostics" },
  { action: "open_journal", label: "Open journal", cb: "onOpenJournal" },
];

const ALL_CLASSES: FailureClass[] = [
  "socket_closed",
  "heartbeat_timeout",
  "version_mismatch",
  "verify_infra",
  "panel_infra",
];

function setup(spec: FailureSpec) {
  const props: FailureOverlayProps = {
    spec,
    raw: RAW,
    onReconnect: vi.fn(),
    onCopyDiagnostics: vi.fn(),
    onOpenJournal: vi.fn(),
    onDismiss: vi.fn(),
  };
  render(<FailureOverlay {...props} />);
  return props;
}

function setupReload(spec: FailureSpec) {
  const props: FailureOverlayProps = {
    spec,
    raw: RAW,
    onReconnect: vi.fn(),
    onCopyDiagnostics: vi.fn(),
    onOpenJournal: vi.fn(),
    onDismiss: vi.fn(),
    onReload: vi.fn(),
  };
  render(<FailureOverlay {...props} />);
  return props;
}

afterEach(cleanup);

describe("FailureOverlay (P2.5)", () => {
  it.each(ALL_CLASSES)("renders title + one-line cause for %s, with slot and alert semantics", (cls) => {
    const spec = FAILURE_TAXONOMY[cls];
    setup(spec);
    const root = document.querySelector(`[data-slot="${SLOT.failureOverlay}"]`);
    expect(root).not.toBeNull();
    expect(root?.getAttribute("role")).toBe("alert");
    expect(root?.className).toContain(`failure-overlay-${cls}`);
    expect(screen.getByText(spec.title)).toBeInTheDocument();
    const cause = screen.getByText(spec.cause);
    expect(cause).toBeInTheDocument();
  });

  it.each(ALL_CLASSES)("%s renders ONLY its leading action button, which fires its callback", (cls) => {
    const spec = FAILURE_TAXONOMY[cls];
    const spies = setup(spec);
    const leading = ACTION_LABELS.find((a) => a.action === spec.action)!;

    for (const a of ACTION_LABELS) {
      const btn = screen.queryByRole("button", { name: a.label });
      if (a === leading) expect(btn).not.toBeNull();
      else expect(btn).toBeNull();
    }

    fireEvent.click(screen.getByRole("button", { name: leading.label }));
    expect(vi.mocked(spies[leading.cb])).toHaveBeenCalledTimes(1);
    for (const a of ACTION_LABELS) {
      if (a !== leading) expect(vi.mocked(spies[a.cb])).not.toHaveBeenCalled();
    }
  });

  it("dismiss × fires onDismiss", () => {
    const spies = setup(FAILURE_TAXONOMY.socket_closed);
    fireEvent.click(screen.getByRole("button", { name: "Dismiss failure overlay" }));
    expect(vi.mocked(spies.onDismiss)).toHaveBeenCalledTimes(1);
  });

  // F1 (grounded revise R2): past POLL_FAIL_RESTART_THRESHOLD the App hands
  // onReload and the classified lane grows the same reload escape hatch the
  // legacy banner has; without it (below threshold), nothing renders.
  it("onReload grows a Restart daemon affordance that fires it", () => {
    const spies = setupReload(FAILURE_TAXONOMY.socket_closed);
    const btn = screen.getByRole("button", { name: "Restart daemon" });
    expect(btn.getAttribute("data-slot")).toBe(SLOT.failureReload);
    expect(screen.getByRole("button", { name: "Reconnect" })).toBeInTheDocument();
    fireEvent.click(btn);
    expect(vi.mocked(spies.onReload!)).toHaveBeenCalledTimes(1);
    expect(vi.mocked(spies.onReconnect)).not.toHaveBeenCalled();
  });

  it("without onReload no restart affordance renders", () => {
    setup(FAILURE_TAXONOMY.socket_closed);
    expect(screen.queryByRole("button", { name: "Restart daemon" })).toBeNull();
    expect(document.querySelector(`[data-slot="${SLOT.failureReload}"]`)).toBeNull();
  });

  it("raw rides the cause line's title attribute, never visible text", () => {
    setup(FAILURE_TAXONOMY.heartbeat_timeout);
    expect(screen.queryByText(RAW)).toBeNull();
    const cause = screen.getByText(FAILURE_TAXONOMY.heartbeat_timeout.cause);
    expect(cause.getAttribute("title")).toBe(RAW);
  });

  it("reconnect classes map to the same Reconnect affordance; skew/infra classes diverge", () => {
    // The taxonomy's locked mapping, observed through the DOM: the two
    // transport classes both offer Reconnect, version skew offers Copy
    // diagnostics, and both pipeline-infra classes offer Open journal.
    for (const [cls, label] of [
      ["socket_closed", "Reconnect"],
      ["heartbeat_timeout", "Reconnect"],
      ["version_mismatch", "Copy diagnostics"],
      ["verify_infra", "Open journal"],
      ["panel_infra", "Open journal"],
    ] as const) {
      cleanup();
      setup(FAILURE_TAXONOMY[cls]);
      expect(screen.getByRole("button", { name: label })).toBeInTheDocument();
    }
  });
});
