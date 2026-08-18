// Tri-model right sidebar gap (2/3 GLM+K3): inline file preview — read the
// file the diff/wikilink/chat reference WITHOUT leaving Odo. Content comes
// from the daemon's read_file IPC (same containment rule as open_path:
// canonicalize-then-prefix-check against the project root and ~/.odo).
// Syntax highlighting reuses the diff viewer's tokenizer. Esc / backdrop
// click closes via Radix Dialog (Phase 5) — its Esc gate keeps a bare Esc
// from reaching App's agent-cancel handler.

import { Fragment, useEffect, useState } from "react";
import { X } from "lucide-react";
import { readFile, errorMessage } from "../api";
import { languageFromPath, tokenize } from "../highlight";
import { Dialog, DialogContent } from "./ui/dialog";

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

  const lang = languageFromPath(path);
  const lines = content.split("\n");

  return (
    <Dialog open onOpenChange={(v) => !v && onClose()}>
      <DialogContent
        aria-label={`Preview of ${path}`}
        className="flex w-[min(860px,92vw)] max-h-[82vh] flex-col overflow-hidden p-0"
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
      </DialogContent>
    </Dialog>
  );
}
