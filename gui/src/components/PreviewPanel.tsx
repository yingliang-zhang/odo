// P2.2/P2.3 (docs/design/adoption-lock.md): ContextPanel "Preview" tab body.
//
// File mode rides daemon read_file (containment-checked server-side) and
// re-reads on the keep-alive activation edge (active false→true) — the same
// activation-refetch contract MemoryPanel/WikiBrowser follow; content stays
// mounted while inactive. Image paths render inline when read_file supplies
// forward-compat bytes (file_data_base64, size-capped in preview.ts) and
// otherwise degrade to a chip + "Open in OS" (today's daemon rejects binary
// reads). Text files render syntax-highlighted via the shared tokenizer,
// reusing FilePreview's readFile→tokenize render structure.
//
// URL mode (P2.3, GUI-initiated only): http(s)://localhost[:*] /
// 127.0.0.1[:*] / [::1][:*] URLs mount a sandboxed iframe —
// sandbox="allow-scripts" with NO allow-same-origin ever, referrerPolicy
// "no-referrer". Every other URL renders the localhost-only lock note
// instead of loading. All failures render inline; the panel never throws.

import { Fragment, memo, useEffect, useRef, useState } from "react";
import { Check, Copy, ExternalLink } from "lucide-react";
import { errorMessage, openPath, readFile } from "../api";
import { languageFromPath, tokenize } from "../highlight";
import {
  imageDataUrl,
  isImagePath,
  isLocalPreviewUrl,
  previewTargetLabel,
  type PreviewTarget,
} from "../preview";
import { SLOT } from "../slots";
import { ZoomableImage } from "./Markdown";

interface Props {
  target: PreviewTarget | null;
  projectRoot: string | null;
  // Keep-alive activation signal: parent mounts this body once and flips
  // `active` on tab switches; file mode refetches on the false→true edge.
  active: boolean;
}

export default memo(function PreviewPanel({ target, projectRoot, active }: Props) {
  if (target === null) {
    return <div className="panel-empty">Nothing to preview — open a file or live URL to see it here.</div>;
  }
  return target.kind === "file" ? (
    <PreviewFilePane path={target.path} projectRoot={projectRoot} active={active} />
  ) : (
    <PreviewUrlPane url={target.url} />
  );
});

interface FileState {
  loading: boolean;
  error: string | null;
  content: string;
  resolved: string;
  truncated: boolean;
  dataUrl: string | null;
}

const EMPTY_FILE: FileState = {
  loading: true,
  error: null,
  content: "",
  resolved: "",
  truncated: false,
  dataUrl: null,
};

// P2.2 file mode: fetch on mount, on path/root identity change, and on the
// activation edge — never on the true→false hide edge (keep-alive keeps the
// last content visible when the user returns).
function PreviewFilePane({ path, projectRoot, active }: { path: string; projectRoot: string | null; active: boolean }) {
  const [state, setState] = useState<FileState>(EMPTY_FILE);
  // StrictMode-safe fetch key. The discarded first pass of dev StrictMode's
  // double-invoke cancels its fetch (alive=false) before the promise can
  // resolve, so a "seen identity" ref written in that pass would starve the
  // retried pass — the pane sat in "Loading…" forever (diff #104 e2e).
  // Track the key whose content has LANDED instead (a cancelled fetch left
  // nothing, so the retried pass still loads) — the same discipline
  // MemoryPanel's mountedRef comment names for the double-invoke.
  const loadedKeyRef = useRef<string | null>(null);
  const prevActiveRef = useRef(active);
  const fetchKey = `${path}\n${projectRoot ?? ""}`;

  useEffect(() => {
    const reactivated = !prevActiveRef.current && active;
    prevActiveRef.current = active;
    if (loadedKeyRef.current === fetchKey && !reactivated) return;
    let alive = true;
    setState(EMPTY_FILE);
    readFile(path, projectRoot)
      .then((resp) => {
        if (!alive) return;
        loadedKeyRef.current = fetchKey;
        setState({
          loading: false,
          error: null,
          content: resp.file_content ?? "",
          resolved: resp.file_resolved ?? "",
          truncated: resp.file_truncated ?? false,
          dataUrl: imageDataUrl(resp, path),
        });
      })
      .catch((e) => {
        if (!alive) return;
        // Nothing landed — leave no loaded key so re-entry retries.
        loadedKeyRef.current = null;
        setState({ ...EMPTY_FILE, loading: false, error: errorMessage(e) });
      });
    return () => {
      alive = false;
    };
  }, [path, projectRoot, active, fetchKey]);

  const image = isImagePath(path);
  const label = previewTargetLabel({ kind: "file", path });
  return (
    <div className="preview-panel flex h-full min-h-0 flex-col overflow-hidden">
      <div className="preview-head flex items-center gap-2 border-b border-border px-2.5 py-1.5 text-caption text-text-dim">
        <span className="preview-head-label shrink-0 font-medium text-text">{label}</span>
        <span className="preview-head-path min-w-0 overflow-hidden text-ellipsis whitespace-nowrap" title={state.resolved || path}>
          {state.resolved || path}
        </span>
        {state.truncated && <span className="file-preview-trunc shrink-0">(truncated at 512KB)</span>}
      </div>
      {state.loading ? (
        <div className="preview-status flex-1 px-3 py-4 text-caption text-text-dim">Loading…</div>
      ) : image ? (
        state.dataUrl !== null ? (
          // ZoomableImage owns the click-to-lightbox; the wrapper carries
          // the P2.1 probe slot (Markdown.tsx is locked to the export-only
          // change, so the slot cannot ride the img tag itself).
          <div className="preview-body flex-1 overflow-auto p-3" data-slot={SLOT.previewImage}>
            <ZoomableImage src={state.dataUrl} alt={label} />
          </div>
        ) : (
          <div className="preview-body flex-1 overflow-auto p-3">
            <div className="attachment-chips flex flex-wrap gap-1.5">
              <span
                className="attachment-chip inline-flex items-center gap-1.5 rounded-[12px] border border-border bg-bg-input px-2 py-0.5 font-mono text-caption"
                data-slot={SLOT.previewChip}
                title={state.resolved || path}
              >
                <code>{label}</code>
                <button
                  type="button"
                  className="attachment-open inline-flex cursor-pointer items-center gap-0.5 rounded border border-border bg-transparent px-1 py-px text-[10px] text-text-dim hover:border-accent hover:text-text"
                  onClick={() => {
                    openPath(path, false, projectRoot).catch(() => {});
                  }}
                >
                  <ExternalLink size={10} aria-hidden /> Open in OS
                </button>
              </span>
            </div>
            <div className="preview-note mt-2 text-caption text-text-dim">
              Inline bytes unavailable — the open-path fallback is the safe default until read_file serves image content.
              {state.error !== null && <span className="file-preview-error block">{state.error}</span>}
            </div>
          </div>
        )
      ) : state.error !== null ? (
        <div className="preview-status preview-error flex-1 px-3 py-4 text-caption file-preview-error">{state.error}</div>
      ) : (
        <pre className="preview-body preview-code file-preview-code flex-1 overflow-auto">
          <code>
            {state.content.split("\n").map((line, li) => (
              <Fragment key={li}>
                {li > 0 ? "\n" : ""}
                {tokenize(line, languageFromPath(path)).map((t, ti) =>
                  t.cls !== null ? (
                    <span key={ti} className={t.cls}>{t.text}</span>
                  ) : (
                    <Fragment key={ti}>{t.text}</Fragment>
                  ),
                )}
              </Fragment>
            ))}
          </code>
        </pre>
      )}
    </div>
  );
}

// P2.3 live mode: the localhost gate runs before ANY mount — a non-local
// URL never reaches an iframe src.
function PreviewUrlPane({ url }: { url: string }) {
  const local = isLocalPreviewUrl(url);
  return (
    <div className="preview-panel flex h-full min-h-0 flex-col overflow-hidden">
      <div className="preview-head flex items-center gap-2 border-b border-border px-2.5 py-1.5 text-caption text-text-dim">
        <span className="preview-head-path min-w-0 flex-1 overflow-hidden text-ellipsis whitespace-nowrap" title={url}>
          {url}
        </span>
        <CopyUrlButton url={url} />
      </div>
      {local ? (
        <iframe
          data-slot={SLOT.previewFrame}
          className="preview-frame min-h-0 w-full flex-1 border-0 bg-white"
          sandbox="allow-scripts"
          referrerPolicy="no-referrer"
          title={url}
          src={url}
        />
      ) : (
        <div className="preview-body flex-1 overflow-auto p-3">
          <div className="preview-blocked max-w-[52ch] rounded-md border border-border p-3 text-caption text-text-dim">
            Blocked by the preview security lock: live mode only loads localhost URLs
            (http(s)://localhost[:port], 127.0.0.1[:port], or [::1][:port]). This URL was never requested.
          </div>
        </div>
      )}
    </div>
  );
}

// Header copy affordance — same feedback-flip pattern as CopyBubbleButton.
function CopyUrlButton({ url }: { url: string }) {
  const [copied, setCopied] = useState(false);
  return (
    <button
      type="button"
      className="preview-copy-url inline-flex shrink-0 cursor-pointer items-center gap-1 rounded border border-border bg-transparent px-1.5 py-px text-[10px] text-text-dim hover:border-accent hover:text-text"
      title={copied ? "Copied" : "Copy URL"}
      aria-label={copied ? "Copied" : "Copy URL"}
      onClick={() => {
        navigator.clipboard?.writeText(url)?.then(() => {
          setCopied(true);
          setTimeout(() => setCopied(false), 2000);
        })?.catch(() => {});
      }}
    >
      {copied ? <Check size={10} aria-hidden /> : <Copy size={10} aria-hidden />} Copy
    </button>
  );
}
