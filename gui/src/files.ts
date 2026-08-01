// Small path helpers shared by the chat composer and bubbles. Attachment
// paths are plain strings (POSIX or Windows); we only ever display them.

// Final path segment for display ("src/lib/foo.py" → "foo.py").
export function basename(path: string): string {
  const seg = path.split(/[\\/]/).pop();
  return seg === undefined || seg === "" ? path : seg;
}
