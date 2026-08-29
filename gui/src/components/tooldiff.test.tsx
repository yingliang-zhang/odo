import { describe, expect, it } from "vitest";
import { render } from "@testing-library/react";

// P1.4 (docs/design/adoption-lock.md): tool-result inline diffs — the
// unified-diff gate, the file-count helper behind the run-header chip, the
// compact ToolDiffView, and MessageBubble's swap from <pre> to hunks.

import { diffFilesChanged, looksLikeUnifiedDiff, ToolDiffView } from "./DiffViewer";
import MessageBubble from "./MessageBubble";
import type { OdoEvent } from "../types";

const TWO_FILE_DIFF = [
  "diff --git a/src/a.ts b/src/a.ts",
  "index 111..222 100644",
  "--- a/src/a.ts",
  "+++ b/src/a.ts",
  "@@ -1,3 +1,3 @@",
  " const a = 1;",
  "-export const oldName = a;",
  "+export const newName = a;",
  "diff --git a/src/b.ts b/src/b.ts",
  "index 333..444 100644",
  "--- a/src/b.ts",
  "+++ b/src/b.ts",
  "@@ -5,2 +5,3 @@",
  " rest();",
  "+again();",
].join("\n");

const NEW_FILE_DIFF = [
  "--- /dev/null",
  "+++ b/docs/new.md",
  "@@ -0,0 +1,2 @@",
  "+# New",
  "+body",
].join("\n");

describe("tool-result inline diffs (P1.4)", () => {
  it("looksLikeUnifiedDiff accepts git and bare-header forms", () => {
    expect(looksLikeUnifiedDiff(TWO_FILE_DIFF)).toBe(true);
    expect(looksLikeUnifiedDiff(NEW_FILE_DIFF)).toBe(true);
    expect(looksLikeUnifiedDiff(`prefix text\n${TWO_FILE_DIFF}`)).toBe(true);
  });

  it("looksLikeUnifiedDiff rejects prose and lone +/- lines", () => {
    expect(looksLikeUnifiedDiff("compiled fine — no diff")).toBe(false);
    expect(looksLikeUnifiedDiff("+added line\n-removed line")).toBe(false);
    expect(looksLikeUnifiedDiff('{"diff": "not a keyword match"}')).toBe(false);
  });

  it("diffFilesChanged lists every touched path per segment", () => {
    expect(diffFilesChanged(TWO_FILE_DIFF)).toEqual(["src/a.ts", "src/b.ts"]);
    expect(diffFilesChanged(NEW_FILE_DIFF)).toEqual(["docs/new.md"]);
  });

  it("ToolDiffView renders compact read-only hunks per file", () => {
    const { container } = render(<ToolDiffView text={TWO_FILE_DIFF} />);
    const files = container.querySelectorAll(".tool-diff-file");
    expect(files).toHaveLength(2);
    expect(container.textContent).toContain("src/a.ts");
    expect(container.textContent).toContain("+1 −1");
    expect(container.textContent).toContain("+1 −0");
    // Hunk markers + payload render; the git preamble never does.
    expect(container.textContent).toContain("@@ -1,3 +1,3 @@");
    expect(container.textContent).toContain("+again();");
    expect(container.textContent).not.toContain("diff --git");
    expect(container.textContent).not.toContain("index 111..222");
    // Read-only posture is explicit.
    expect(container.textContent).toContain("read-only");
    expect(container.querySelector(".diff-add")).not.toBeNull();
    expect(container.querySelector(".diff-del")).not.toBeNull();
  });

  it("ToolDiffView renders nothing for an unparseable body", () => {
    const { container } = render(<ToolDiffView text="just text" />);
    expect(container.querySelector(".tool-diff")).toBeNull();
  });

  it("MessageBubble swaps the result <pre> for hunk view on a diff result", () => {
    const ev: OdoEvent = {
      id: 1,
      conversation_id: 1,
      seq: 1,
      type: "agent_tool_result",
      payload: { tool: "run_command", result: TWO_FILE_DIFF },
      created_at: "2026-08-29T10:00:00Z",
    };
    const { container } = render(<MessageBubble event={ev} />);
    expect(container.querySelector(".tool-diff")).not.toBeNull();
    expect(container.querySelector("pre")).toBeNull();
    expect(container.textContent).toContain("run_command");
  });

  it("MessageBubble keeps the <pre> path for non-diff results", () => {
    const ev: OdoEvent = {
      id: 1,
      conversation_id: 1,
      seq: 1,
      type: "agent_tool_result",
      payload: { tool: "read_file", result: "280 lines of prose output" },
      created_at: "2026-08-29T10:00:00Z",
    };
    const { container } = render(<MessageBubble event={ev} />);
    expect(container.querySelector(".tool-diff")).toBeNull();
    expect(container.querySelector("pre")).not.toBeNull();
  });
});
