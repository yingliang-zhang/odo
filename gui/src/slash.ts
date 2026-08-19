// Advisory slash commands (/panel, /vision, /preview): the daemon answers
// them synchronously inside the send_message RPC — the /panel MoA fan-out
// (or a /preview capture + K3 call) can hold that call for minutes. The
// composer must not sit locked for the whole consult, so sends detected
// here detach: clear immediately, answers arrive via the poll loop.
//
// Detection mirrors handleSendMessage's routing in internal/ipc/server.go
// EXACTLY — TrimSpace, then TrimPrefix(cmd), then accept only when the
// remainder starts with a LITERAL space or is empty:
//
//   if rest := strings.TrimPrefix(strings.TrimSpace(req.Text), "/panel");
//      rest != strings.TrimSpace(req.Text) &&
//      (strings.HasPrefix(rest, " ") || rest == "") { … }
//
// So "/panelxyz", "/panel\t…", "/panel⏎…" and "/ panel …" are NOT advisory
// on either side and fall through to the normal send path; "/loop" spawns
// a daemon-side run, so it must never be detached here. If the daemon's
// delimiter ever widens, update this regex and slash.test.ts's mirrored
// case table together.
const ADVISORY_SLASH_RE = /^\/(panel|vision|preview)(?: |$)/;

export function isAdvisorySlash(text: string): boolean {
  return ADVISORY_SLASH_RE.test(text.trim());
}
