import { describe, expect, it } from "vitest";

// P2.1–P2.3 (docs/design/adoption-lock.md): pure preview helpers. The
// isLocalPreviewUrl matrix pins the design-lock frame-src gate; imageDataUrl
// pins the 2 MiB cap + forward-compat field posture.

import {
  IMAGE_EXTENSIONS,
  PREVIEW_IMAGE_CAP,
  extractHttpUrls,
  findImageRefs,
  imageDataUrl,
  isImagePath,
  isLocalPreviewUrl,
  looksLikeFilePath,
  previewTargetLabel,
} from "./preview";

describe("isImagePath", () => {
  it("accepts the five raster types plus svg, case-insensitively", () => {
    for (const p of ["a.png", "b.jpg", "c.jpeg", "d.gif", "e.webp", "f.svg"]) {
      expect(isImagePath(p)).toBe(true);
    }
    expect(isImagePath("SHOT.PNG")).toBe(true);
    expect(isImagePath(".odo/attachments/Logo.SVG")).toBe(true);
  });

  it("rejects non-images", () => {
    for (const p of ["a.txt", "a.png.txt", "png", "a.png123", "a.svgz"]) {
      expect(isImagePath(p)).toBe(false);
    }
  });

  it("exposes the same extension list it consumes", () => {
    expect(IMAGE_EXTENSIONS).toContain(".svg");
    expect(IMAGE_EXTENSIONS).toHaveLength(6);
  });
});

describe("findImageRefs", () => {
  it("returns [] for empty and ref-free text", () => {
    expect(findImageRefs("")).toEqual([]);
    expect(findImageRefs("wrote report.md and notes.txt")).toEqual([]);
  });

  it("extracts POSIX paths", () => {
    expect(findImageRefs("Wrote .odo/attachments/shot_2026.png OK")).toEqual([".odo/attachments/shot_2026.png"]);
  });

  it("extracts Windows paths with drive letters and backslashes", () => {
    expect(findImageRefs("saved C:\\Users\\me\\shots\\A.PNG now")).toEqual(["C:\\Users\\me\\shots\\A.PNG"]);
    expect(findImageRefs("see src\\images\\logo.svg")).toEqual(["src\\images\\logo.svg"]);
  });

  it("dedupes repeats, keeping order of appearance", () => {
    const text = "b.jpg then a.png then b.jpg again";
    expect(findImageRefs(text)).toEqual(["b.jpg", "a.png"]);
  });

  it("strips trailing punctuation from refs", () => {
    expect(findImageRefs("see shot.png, then plot.svg.")).toEqual(["shot.png", "plot.svg"]);
  });

  it("skips URLs — those are live-preview territory, not byte loads", () => {
    expect(findImageRefs("open http://x.test/img.png and shot.png")).toEqual(["shot.png"]);
  });
});

describe("extractHttpUrls", () => {
  it("returns [] for empty and URL-free text", () => {
    expect(extractHttpUrls("")).toEqual([]);
    expect(extractHttpUrls("no links here")).toEqual([]);
  });

  it("extracts http and https URLs", () => {
    expect(extractHttpUrls("a http://localhost:3000/x b https://example.com")).toEqual([
      "http://localhost:3000/x",
      "https://example.com",
    ]);
  });

  it("strips trailing punctuation", () => {
    expect(extractHttpUrls("visit http://localhost:3000. (or https://127.0.0.1:8080/a)")).toEqual([
      "http://localhost:3000",
      "https://127.0.0.1:8080/a",
    ]);
    expect(extractHttpUrls("https://example.com/p?q=1, done")).toEqual(["https://example.com/p?q=1"]);
  });

  it("dedupes in order of appearance", () => {
    expect(extractHttpUrls("x http://a.test/ y http://a.test/")).toEqual(["http://a.test/"]);
  });
});

describe("isLocalPreviewUrl (design-lock frame-src gate)", () => {
  it("rejects non-http(s) schemes hostile to the sandbox", () => {
    expect(isLocalPreviewUrl("file:///etc/passwd")).toBe(false);
    expect(isLocalPreviewUrl("javascript:alert(1)")).toBe(false);
    expect(isLocalPreviewUrl("data:text/html,<b>")).toBe(false);
  });

  it("rejects remote hosts and localhost-suffix tricks", () => {
    expect(isLocalPreviewUrl("https://example.com")).toBe(false);
    expect(isLocalPreviewUrl("http://localhost.evil.com")).toBe(false);
    expect(isLocalPreviewUrl("http://0.0.0.0:3000")).toBe(false);
    expect(isLocalPreviewUrl("localhost:3000")).toBe(false); // missing scheme
  });

  it("accepts exact localhost / 127.0.0.1 / [::1] on any port or none", () => {
    expect(isLocalPreviewUrl("http://localhost:3000/x")).toBe(true);
    expect(isLocalPreviewUrl("http://localhost")).toBe(true);
    expect(isLocalPreviewUrl("https://127.0.0.1")).toBe(true);
    expect(isLocalPreviewUrl("http://127.0.0.1:4321/a?b=1")).toBe(true);
    expect(isLocalPreviewUrl("http://[::1]:8080")).toBe(true);
  });

  it("normalizes scheme/host case like the URL spec does", () => {
    expect(isLocalPreviewUrl("HTTP://LOCALHOST:3000")).toBe(true);
  });
});

describe("imageDataUrl", () => {
  it("returns null when the forward-compat field is absent or empty", () => {
    expect(imageDataUrl({}, "a.png")).toBeNull();
    expect(imageDataUrl({ file_data_base64: "" }, "a.png")).toBeNull();
  });

  it("prefers the daemon's file_mime over the extension guess", () => {
    expect(imageDataUrl({ file_data_base64: "aGk=", file_mime: "image/webp" }, "a.png")).toBe(
      "data:image/webp;base64,aGk=",
    );
  });

  it("derives MIME from the extension when file_mime is absent", () => {
    expect(imageDataUrl({ file_data_base64: "aGk=" }, "a.jpg")).toBe("data:image/jpeg;base64,aGk=");
    expect(imageDataUrl({ file_data_base64: "aGk=" }, "a.svg")).toBe("data:image/svg+xml;base64,aGk=");
    expect(imageDataUrl({ file_data_base64: "aGk=" }, "a.webp")).toBe("data:image/webp;base64,aGk=");
  });

  it("rejects payloads whose decoded estimate exceeds the 2 MiB cap", () => {
    // decoded estimate = floor(b64len * 3 / 4) vs cap 2,097,152:
    // len 2,796,203 estimates exactly AT the cap (allowed), 2,796,204 is one over.
    expect(imageDataUrl({ file_data_base64: "A".repeat(2_796_203) }, "a.png")).toMatch(/^data:image\/png;base64,A+$/);
    expect(imageDataUrl({ file_data_base64: "A".repeat(2_796_204) }, "a.png")).toBeNull();
    expect(PREVIEW_IMAGE_CAP).toBe(2 * 1024 * 1024);
  });
});

describe("looksLikeFilePath (chat→panel arg gate)", () => {
  it("accepts slash-bearing values on either separator", () => {
    expect(looksLikeFilePath("src/main.go")).toBe(true);
    expect(looksLikeFilePath("./a")).toBe(true);
    expect(looksLikeFilePath("src/")).toBe(true);
    expect(looksLikeFilePath("src\\pkg\\x")).toBe(true);
    expect(looksLikeFilePath("**/*.ts")).toBe(true);
  });

  it("accepts bare filenames with a known text extension, incl. :line refs", () => {
    expect(looksLikeFilePath("README.md")).toBe(true);
    expect(looksLikeFilePath("go.mod")).toBe(true);
    expect(looksLikeFilePath("a.go:12")).toBe(true);
  });

  it("rejects URLs, whitespace strings, bare words, and non-strings", () => {
    expect(looksLikeFilePath("https://example.com/x.go")).toBe(false);
    expect(looksLikeFilePath("hello world.png")).toBe(false);
    expect(looksLikeFilePath("justtext")).toBe(false);
    expect(looksLikeFilePath("Makefile")).toBe(false);
    expect(looksLikeFilePath("localhost:3000")).toBe(false);
    expect(looksLikeFilePath(42)).toBe(false);
    expect(looksLikeFilePath(null)).toBe(false);
  });
});

describe("previewTargetLabel", () => {
  it("labels files by basename, urls verbatim", () => {
    expect(previewTargetLabel({ kind: "file", path: "/x/y/shot.png" })).toBe("shot.png");
    expect(previewTargetLabel({ kind: "file", path: "C:\\shots\\b.webp" })).toBe("b.webp");
    expect(previewTargetLabel({ kind: "url", url: "http://localhost:3000" })).toBe("http://localhost:3000");
  });
});
