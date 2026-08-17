// Tri-model right sidebar gap (2/3 GLM+K3): inline file preview — read the
// file the diff/wikilink/chat reference WITHOUT leaving Odo. Content comes
// from the daemon's read_file IPC (same containment rule as open_path:
// canonicalize-then-prefix-check against the project root and ~/.odo).
// Syntax highlighting reuses the diff viewer's tokenizer. Esc / backdrop
// click closes; the overlay is registered in App.tsx's Esc gate so a bare
// Esc never cancels the agent.

import { Fragment, useEffect, useRef, useState } from "react";
import { X } from "lucide-react";
import { readFile, errorMessage } from "../api";
import { languageFromPath, tokenize } from "../highlight";
import { useFocusTrap } from "../focusTrap";

interface Props {
  path: string; // project-root-relative OR absolute — daemon resolves
  projectRoot: string | null;
  onClose: () => void;
}

export default function FilePreview({ path, projectRoot, onClose }: Props) {
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState<string | null>(null);
  const [content, setContent] = useState("");
  const [resolved, setResolved] = useState("");
  const [truncated, setTruncated] = useState(false);
  // Modal focus: the trap moves focus into the dialog on open (the close
  // button is the first focusable) and restores it to the trigger on close.
  const dialogRef = useRef<HTMLDivElement>(null);
  useFocusTrap(dialogRef);

  useEffect(() => {
    let alive = true;
    setLoading(true);
    setError(null);
    readFile(path, projectRoot)
      .then((resp) => {
        if (!alive) return;
        setContent(resp.file_content ?? "");
        setResolved(resp.file_resolved ?? "");
        setTruncated(resp.file_truncated ?? false);
        setLoading(false);
      })
      .catch((e) => {
        if (!alive) return;
        setError(errorMessage(e));
        setLoading(false);
      });
    return () => {
      alive = false;
    };
  }, [path, projectRoot]);

  // Esc closes (App.tsx's gate keeps this from reaching the agent cancel).
  useEffect(() => {
    const onKey = (e: KeyboardEvent) => {
      if (e.key === "Escape") {
        e.stopPropagation();
        onClose();
      }
    };
    window.addEventListener("keydown", onKey);
    return () => window.removeEventListener("keydown", onKey);
  }, [onClose]);

  const lang = languageFromPath(path);
  const lines = content.split("\n");

  return (
    <div
      className="file-preview-overlay"
      role="dialog"
      aria-modal="true"
      aria-label={`Preview of ${path}`}
      onClick={onClose}
    >
      <div
        className="file-preview-dialog"
        ref={dialogRef}
        tabIndex={-1}
        onClick={(e) => e.stopPropagation()}
      >
        <div className="file-preview-head">
          <span className="file-preview-path" title={resolved || path}>
            {path}
            {truncated && <span className="file-preview-trunc"> (truncated at 512KB)</span>}
          </span>
          <button
            type="button"
            className="file-preview-close"
            aria-label="Close preview"
            onClick={onClose}
          >
            <X size={14} />
          </button>
        </div>
        {loading ? (
          <div className="file-preview-body file-preview-status">Loading…</div>
        ) : error != null ? (
          <div className="file-preview-body file-preview-status file-preview-error">
            {error}
          </div>
        ) : (
          <pre className="file-preview-body file-preview-code">
            <code>
              {lines.map((line, li) => (
                <Fragment key={li}>
                  {li > 0 ? "\n" : ""}
                  {tokenize(line, lang).map((t, ti) =>
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
    </div>
  );
}
