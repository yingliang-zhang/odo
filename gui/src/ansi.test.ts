// Odo DX wave (Feature 4): renderAnsi contract — passthrough fast path,
// basic/bright fg+bg, bold+dim, reset closure, 256-cube/gray/truecolor,
// HTML entity escaping at the injection seam, and silent stripping of
// unsupported sequences.
import { describe, expect, it } from "vitest";
import { renderAnsi } from "./ansi";

describe("renderAnsi", () => {
  it("passes plain text through unchanged", () => {
    const plain = "no escapes here — 100% <fast> path";
    expect(renderAnsi(plain)).toBe(plain); // identical string, no work done
  });

  it("renders a basic fg color and closes on reset", () => {
    expect(renderAnsi("\x1b[31mred\x1b[0m done")).toBe(
      '<span style="color:#cc0000">red</span> done',
    );
  });

  it("combines weight with color in one param list", () => {
    expect(renderAnsi("\x1b[1;32mok\x1b[m")).toBe(
      '<span style="font-weight:600;color:#4e9a06">ok</span>',
    );
  });

  it("treats an empty param list as reset (\x1b[m)", () => {
    expect(renderAnsi("\x1b[33mx\x1b[m y")).toBe(
      '<span style="color:#c4a000">x</span> y',
    );
  });

  it("renders dim as opacity and bright fg colors", () => {
    expect(renderAnsi("\x1b[2;93mfaint\x1b[0m")).toBe(
      '<span style="opacity:0.6;color:#fce94f">faint</span>',
    );
  });

  it("renders background colors (40–47)", () => {
    expect(renderAnsi("\x1b[41malert\x1b[0m")).toBe(
      '<span style="background-color:#cc0000">alert</span>',
    );
  });

  it("renders 256-color from the cube, bright range, and gray ramp", () => {
    // 196 = cube (5,0,0) → #ff0000; 9 → bright red; 245 → gray 8+13*10=138.
    expect(renderAnsi("\x1b[38;5;196mh\x1b[0m")).toBe('<span style="color:#ff0000">h</span>');
    expect(renderAnsi("\x1b[38;5;9mh\x1b[0m")).toBe('<span style="color:#ef2929">h</span>');
    expect(renderAnsi("\x1b[38;5;245mh\x1b[0m")).toBe('<span style="color:#8a8a8a">h</span>');
  });

  it("renders 256-color backgrounds and truecolor", () => {
    expect(renderAnsi("\x1b[48;5;21mbg\x1b[0m")).toBe(
      '<span style="background-color:#0000ff">bg</span>',
    );
    expect(renderAnsi("\x1b[38;2;10;20;30mrgb\x1b[0m")).toBe(
      '<span style="color:#0a141e">rgb</span>',
    );
  });

  it("escapes HTML entities inside colored and plain text", () => {
    expect(renderAnsi("\x1b[31m<b> & \"q\"\x1b[0m")).toBe(
      '<span style="color:#cc0000">&lt;b&gt; &amp; "q"</span>',
    );
  });

  it("silently strips unsupported sequences", () => {
    expect(renderAnsi("\x1b[4;5;999mweird\x1b[7m done")).toBe("weird done");
    // Malformed extended descriptor (truncated params) leaves no markup.
    expect(renderAnsi("\x1b[38;5mbreak\x1b[m")).toBe("break");
  });

  it("handles adjacent colors as two distinct runs", () => {
    expect(renderAnsi("pre \x1b[31ma\x1b[32mb\x1b[0m post")).toBe(
      'pre <span style="color:#cc0000">a</span><span style="color:#4e9a06">b</span> post',
    );
  });

  it("supports default-fg/bg resets without closing other style", () => {
    expect(renderAnsi("\x1b[1;31mbold\x1b[39mplain\x1b[0m")).toBe(
      '<span style="font-weight:600;color:#cc0000">bold</span><span style="font-weight:600">plain</span>',
    );
  });

  it("emits no empty span when the first code resets", () => {
    expect(renderAnsi("\x1b[0mplain")).toBe("plain");
  });
});
