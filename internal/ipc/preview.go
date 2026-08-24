package ipc

// /preview: one headless loopback screenshot piped into the vision leg.
// Boundary posture:
//   - the URL allowlist (previewAllowedHosts) is checked pre-journal and
//     holds across the URL's 30x chain (P1, 2026-08-24, review finding 3):
//     the chain is walked hop-by-hop before capture and the FINAL URL is
//     what spawns, so a loopback URL 30x-ing to an external/intranet host
//     is refused at the hop it leaves the allowlist and the journaled
//     receipts name final_url — screened == captured;
//   - the capture itself runs behind a per-shot loopback-only filtering
//     proxy (previewGuard, P1 2026-08-24, review finding 2) injected into
//     the child BOTH via HTTP(S)_PROXY with an EMPTY no_proxy (no implicit
//     <-loopback> bypass) AND via playwright's explicit --proxy-server
//     argv (the channel guaranteed to reach chromium): after the
//     pre-capture probe the browser must re-fetch the same URL, and every
//     request the page issues during the capture — additional redirect
//     hops, JS/meta-refresh navigations, subresources — is
//     allowlist-checked BEFORE any dial; one blocked host refuses the
//     whole capture post-hoc (previewBoundaryRefusal);
//   - per-shot browser lifecycle (spawn → navigate → capture → exit under a
//     45s cap; the guard dies with it) — no persistent browser, no profile
//     reuse beyond the npx cache (npx's own registry resolution warms in a
//     separate unguarded spawn, then runs only-if-cached — tooling traffic
//     is not capture traffic);
//   - no auto-anything: no auto-retry, no auto screenshot after errors or
//     after auto-land.
// Residual v1 boundary, honestly remaining after the guard: in-page
// data:/blob: URLs never fetch (chromium resolves them internally — no
// request reaches or needs the guard), WebRTC UDP is not proxied (a page's
// own UDP chatter rides outside the guard but cannot place bytes INTO the
// capture), and DNS is irrelevant — only the three loopback names of
// previewAllowedHosts ever pass, and they resolve in-process.
// Explicit non-goals: agent tool channel (MCP server), any visible browser
// pane, CDP attach to the user's browser, a Process Dock.

import (
	"bufio"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/yingliang-zhang/odo/internal/moa"
	"github.com/yingliang-zhang/odo/internal/store"
)

const (
	// previewViewport / previewSettleMs are the npx playwright screenshot
	// arguments: a desktop-size viewport and a 3s settle for client-side
	// rendering before capture.
	previewViewport = "1440,900"
	previewSettleMs = "3000"

	// previewDefaultPrompt is the analysis prompt when the user gives none.
	previewDefaultPrompt = "Analyze this screenshot as a UI reviewer: list layout issues, style inconsistencies, overflow/misalignment. Be specific with locations."

	// previewInstallHint is the exact first-run prerequisite command shown
	// on a missing-browser failure.
	previewInstallHint = "PATH=~/.hermes/node/bin:$PATH npx playwright install chromium"
)

// previewChildTimeout bounds the whole per-shot subprocess (npx resolution,
// browser launch, navigation, capture). A var (not const) so the timeout
// test can shrink it — never touched in production paths.
var previewChildTimeout = 45 * time.Second

const (
	// previewResolveTimeout bounds the whole redirect-chain probe (P1,
	// 2026-08-24) — separate from and shorter than the 45s per-shot
	// capture cap, so a slow redirector surfaces its own error before a
	// browser spawns.
	previewResolveTimeout = 10 * time.Second

	// previewRedirectLimit caps the 30x hops the probe will follow (the
	// Go client's own default is also 10; pinned so the refusal text and
	// the loop test name one number).
	previewRedirectLimit = 10

	// previewDiscardProbeBytes bounds how much of a probe body is drained
	// on its way to io.Discard — a redirect probe must never become a
	// download.
	previewDiscardProbeBytes = 4096
)

// previewAllowedHosts is the v1 URL allowlist (url.Hostname() values,
// compared lower-cased): loopback only.
var previewAllowedHosts = map[string]bool{
	"localhost": true,
	"127.0.0.1": true,
	"::1":       true,
}

// parsePreviewArgs splits the /preview argument text: the first token is
// the URL (required), the rest is the optional analysis prompt.
func parsePreviewArgs(text string) (rawURL, prompt string, err error) {
	fields := strings.Fields(text)
	if len(fields) == 0 {
		return "", "", fmt.Errorf("/preview: URL is required (usage: /preview <url> [prompt])")
	}
	return fields[0], strings.Join(fields[1:], " "), nil
}

// validatePreviewURL enforces the v1 locked boundary: scheme http/https
// and the hostname exactly one of the loopback allowlist (localhost.
// subdomains, trailing dots, and external hosts all refuse).
func validatePreviewURL(raw string) error {
	u, err := url.Parse(raw)
	if err != nil {
		return fmt.Errorf("preview: invalid URL %q: %w", raw, err)
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return fmt.Errorf("preview: URL scheme must be http or https, got %q", u.Scheme)
	}
	if !previewAllowedHosts[strings.ToLower(u.Hostname())] {
		return fmt.Errorf("preview is restricted to localhost URLs (external URLs intentionally blocked in v1)")
	}
	return nil
}

// redactPreviewURL returns the URL for journaling: a BASIC-auth password in
// userinfo is masked (url.Redacted). The raw form still spawns the capture —
// redaction only applies to the journaled record, never the wire args.
func redactPreviewURL(raw string) string {
	u, err := url.Parse(raw)
	if err != nil {
		return raw
	}
	return u.Redacted()
}

// resolvePreviewFinalURL walks rawURL's 30x chain and returns the final URL
// the capture must request. Every hop re-runs validatePreviewURL, so a
// redirect off the loopback allowlist (or to a non-http(s) scheme) is
// refused at the hop that leaves the boundary — the playwright CLI follows
// main-frame redirects silently and would otherwise smuggle an external
// host's screenshot into the vision leg past the initial check (security
// review 2026-08-24 finding 3). The probe is HEAD first (no body), GET on
// a 405 (servers that route GET-only); response bodies close after a
// bounded discard, never read in full. The caller treats any probe failure
// as a pre-journal refusal, same as validatePreviewURL.
func resolvePreviewFinalURL(ctx context.Context, rawURL string) (string, error) {
	client := &http.Client{
		Timeout: previewResolveTimeout,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if len(via) >= previewRedirectLimit {
				return fmt.Errorf("preview: URL redirect chain exceeded %d hops (redirect loop?)", previewRedirectLimit)
			}
			if err := validatePreviewURL(req.URL.String()); err != nil {
				return fmt.Errorf("preview: redirect hop %d leaves the boundary (%s): %w", len(via), redactPreviewURL(req.URL.String()), err)
			}
			return nil
		},
	}
	probe := func(method string) (string, bool, error) {
		req, err := http.NewRequestWithContext(ctx, method, rawURL, nil)
		if err != nil {
			return "", false, fmt.Errorf("preview: invalid URL %q: %w", rawURL, err)
		}
		resp, err := client.Do(req)
		if err != nil {
			// Drop the url.Error wrapper (method + echoed probe URL); the
			// cause — a refused hop, the chain limit, dial/timeout —
			// reads cleaner as the chat error on its own.
			var uerr *url.Error
			if errors.As(err, &uerr) {
				err = uerr.Err
			}
			return "", false, err
		}
		defer resp.Body.Close()
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, previewDiscardProbeBytes))
		return resp.Request.URL.String(), resp.StatusCode == http.StatusMethodNotAllowed, nil
	}
	finalURL, methodNotAllowed, err := probe(http.MethodHead)
	if err == nil && methodNotAllowed {
		// The endpoint won't answer HEAD: re-probe with GET, same bounds.
		finalURL, _, err = probe(http.MethodGet)
	}
	if err != nil {
		return "", err
	}
	return finalURL, nil
}

// attachmentDir returns <root>/.odo/attachments, creating it — the
// clipboard (save_attachment) and /preview captures share one store so
// chips, vision receipts, and audits resolve identically. The caller wraps
// the error with its own command prefix (save_attachment's wording predates
// the extraction and stays).
func attachmentDir(root string) (string, error) {
	dir := filepath.Join(root, ".odo", "attachments")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	return dir, nil
}

// hermesNodeBin is the project's pinned Node location — prepended FIRST to
// the screenshot child's PATH so playwright's npx shim (#!/usr/bin/env
// node) runs on hermes's Node and never on a newer system Node (the known
// Node 26 breakage); a .app-launched daemon's minimal PATH stays
// insufficient-only when hermes itself is absent.
func hermesNodeBin() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(home, ".hermes", "node", "bin")
}

// resolvePreviewNpx prefers the hermes-pinned npx, else falls back to
// LookPath (dev shells with node on PATH). Keeping resolution here (not
// cmd.Env) matters: exec.Command resolves the binary via the PARENT's
// PATH at construction, so extending only the child env would leave a
// .app-launched daemon unable to find npx at all.
func resolvePreviewNpx() (string, error) {
	if bin := hermesNodeBin(); bin != "" {
		cand := filepath.Join(bin, "npx")
		if info, err := os.Stat(cand); err == nil && !info.IsDir() && info.Mode()&0o111 != 0 {
			return cand, nil
		}
	}
	p, err := exec.LookPath("npx")
	if err != nil {
		return "", fmt.Errorf("preview needs node/npx — not found on PATH or at ~/.hermes/node/bin (install Node.js first)")
	}
	return p, nil
}

// previewChildEnv is os.Environ() with ~/.hermes/node/bin FIRST on PATH.
func previewChildEnv() []string {
	env := os.Environ()
	bin := hermesNodeBin()
	if bin == "" {
		return env
	}
	for i, e := range env {
		if strings.HasPrefix(e, "PATH=") {
			env[i] = "PATH=" + bin + string(filepath.ListSeparator) + e[len("PATH="):]
			return env
		}
	}
	return append(env, "PATH="+bin)
}

// previewBlockedReq records one request the guard refused: the method and
// the authority it named (host[:port] — usually the CONNECT target).
type previewBlockedReq struct {
	Method string
	Host   string
}

// previewGuard is the capture-time boundary enforcer (2026-08-24 review
// finding 2): a loopback-only filtering proxy living for exactly one
// capture. The pre-capture 30x probe (resolvePreviewFinalURL) screens the
// chain the DAEMON walks, but the playwright child then re-fetches the
// final URL in a second request — a page answering the probe with 204 and
// the browser GET with a 302 to an external/intranet host, or pulling
// external subresources / JS-driven navigations mid-shot, would otherwise
// screenshot external bytes straight into the vision leg. The child is
// spawned with BOTH the proxy env (EMPTY no_proxy — even loopback must
// detour through the guard) AND playwright's --proxy-server argv pointing
// here, so EVERY request the capture issues is allowlist-checked before
// any dial:
//
//   - allowed CONNECT: 200 Connection established, then a dumb
//     bidirectional byte tunnel (TLS bytes flow untouched);
//   - allowed absolute-URI plain HTTP: the first request is re-serialized
//     to origin-form, then a dumb byte tunnel — keep-alive follow-up
//     requests, chunked bodies, and 101 upgrades (ws://) all ride the
//     tunnel untranslated;
//   - anything off previewAllowedHosts: recorded in the blocked list,
//     answered 403, connection closed — the target is never dialed.
//
// Lifecycle: the guard starts immediately before the child spawns;
// close() (deferred by the caller until the capture file is read) shuts
// the listener AND every tracked connection, so no guard goroutine
// outlives the capture; accepted conns also carry a previewChildTimeout
// read deadline as a backstop.
type previewGuard struct {
	ln         net.Listener
	acceptDone chan struct{}
	wg         sync.WaitGroup
	mu         sync.Mutex
	closed     bool
	conns      map[net.Conn]struct{}
	blocked    []previewBlockedReq
	allowed    int
	dials      int
}

// previewGuardHook, when non-nil (tests only), receives each freshly
// started guard BEFORE the child spawns, so tests can pre-seed denials
// into its record or assert on its observations after the capture. Nil in
// production.
var previewGuardHook func(*previewGuard)

// startPreviewGuard listens on an ephemeral loopback port and launches the
// accept loop. One guard per capture; the caller defers close.
func startPreviewGuard() (*previewGuard, error) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return nil, err
	}
	g := &previewGuard{
		ln:         ln,
		acceptDone: make(chan struct{}),
		conns:      make(map[net.Conn]struct{}),
	}
	go g.acceptLoop()
	return g, nil
}

func (g *previewGuard) acceptLoop() {
	defer close(g.acceptDone)
	for {
		c, err := g.ln.Accept()
		if err != nil {
			return // listener closed: shutdown
		}
		// Backstop deadline: no guard read may outlive the capture bound
		// (close() actively kills tracked conns; this covers a conn that
		// idles between accept and close).
		_ = c.SetReadDeadline(time.Now().Add(previewChildTimeout))
		g.track(c)
		g.wg.Add(1)
		go func() {
			defer g.wg.Done()
			defer g.untrack(c)
			defer c.Close()
			g.serve(c)
		}()
	}
}

func (g *previewGuard) track(c net.Conn) {
	g.mu.Lock()
	g.conns[c] = struct{}{}
	g.mu.Unlock()
}

func (g *previewGuard) untrack(c net.Conn) {
	g.mu.Lock()
	delete(g.conns, c)
	g.mu.Unlock()
}

// close shuts the guard down in an order that can neither lose nor orphan
// a connection: the listener first, then the accept loop's exit is
// observed (every accepted conn is tracked by then), then all tracked
// conns close (unblocking every read/copy), then the handler wait.
func (g *previewGuard) close() {
	g.mu.Lock()
	if g.closed {
		g.mu.Unlock()
		return
	}
	g.closed = true
	g.mu.Unlock()
	_ = g.ln.Close()
	<-g.acceptDone
	g.mu.Lock()
	for c := range g.conns {
		_ = c.Close()
	}
	g.mu.Unlock()
	g.wg.Wait()
}

// addr is the 127.0.0.1:port the child's proxy env points at.
func (g *previewGuard) addr() string {
	return g.ln.Addr().String()
}

// serve handles one client connection: read the first request, police its
// host against the allowlist, then deny or tunnel.
func (g *previewGuard) serve(c net.Conn) {
	br := bufio.NewReader(c)
	req, err := http.ReadRequest(br)
	if err != nil {
		return // pre-request garbage or an idle close: nothing to police
	}
	authority := req.URL.Host
	if req.Method == http.MethodConnect || authority == "" {
		authority = req.Host
	}
	if !previewAllowedHosts[previewAuthorityHost(authority)] {
		g.recordBlocked(req.Method, authority)
		g.writePlain(c, http.StatusForbidden,
			"preview guard: host is outside the loopback boundary\n")
		return
	}
	g.recordAllowed()
	if req.Method == http.MethodConnect {
		target, err := g.dial(previewDialAddr(authority, ""))
		if err != nil {
			g.writePlain(c, http.StatusBadGateway, "preview guard: dial failed\n")
			return
		}
		if _, err := io.WriteString(c, "HTTP/1.1 200 Connection established\r\n\r\n"); err != nil {
			_ = target.Close()
			return
		}
		previewGuardTunnel(br, c, target)
		return
	}
	// Absolute-form plain HTTP: re-serialize the first request in
	// origin-form, then tunnel raw bytes both ways.
	target, err := g.dial(previewDialAddr(authority, req.URL.Scheme))
	if err != nil {
		g.writePlain(c, http.StatusBadGateway, "preview guard: dial failed\n")
		return
	}
	if err := previewGuardForward(target, req); err != nil {
		_ = target.Close()
		return
	}
	previewGuardTunnel(br, c, target)
}

// dial bounds each dial attempt and counts guarded dials (a dial count of
// zero proves a denied request never touched the network).
func (g *previewGuard) dial(addr string) (net.Conn, error) {
	g.mu.Lock()
	g.dials++
	g.mu.Unlock()
	return net.DialTimeout("tcp", addr, 10*time.Second)
}

func (g *previewGuard) recordBlocked(method, host string) {
	g.mu.Lock()
	g.blocked = append(g.blocked, previewBlockedReq{Method: method, Host: host})
	g.mu.Unlock()
}

func (g *previewGuard) recordAllowed() {
	g.mu.Lock()
	g.allowed++
	g.mu.Unlock()
}

// blockedHosts returns the distinct denied authorities in first-seen
// order — the refusal message names these.
func (g *previewGuard) blockedHosts() []string {
	seen := map[string]bool{}
	var hosts []string
	g.mu.Lock()
	defer g.mu.Unlock()
	for _, b := range g.blocked {
		if !seen[b.Host] {
			seen[b.Host] = true
			hosts = append(hosts, b.Host)
		}
	}
	return hosts
}

// blockedRequests returns a snapshot of the full blocked record (method +
// host per refusal) for tests and audits.
func (g *previewGuard) blockedRequests() []previewBlockedReq {
	g.mu.Lock()
	defer g.mu.Unlock()
	return append([]previewBlockedReq(nil), g.blocked...)
}

func (g *previewGuard) allowedRequests() int {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.allowed
}

func (g *previewGuard) dialCount() int {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.dials
}

// writePlain answers a denied or failed request; the defer closes the
// conn and (for denials) the target was never dialed.
func (g *previewGuard) writePlain(c net.Conn, status int, body string) {
	_, _ = fmt.Fprintf(c, "HTTP/1.1 %d %s\r\nContent-Type: text/plain; charset=utf-8\r\nConnection: close\r\nContent-Length: %d\r\n\r\n%s",
		status, http.StatusText(status), len(body), body)
}

// previewAuthorityHost extracts the allowlist key from an authority
// (host[:port]); the same hostname-lower-cased semantics as
// validatePreviewURL.
func previewAuthorityHost(authority string) string {
	if h, _, err := net.SplitHostPort(authority); err == nil {
		return strings.ToLower(h)
	}
	// No port (or a bare IPv6 literal): strip any brackets, lowercase.
	return strings.ToLower(strings.Trim(authority, "[]"))
}

// previewDialAddr adds the scheme's default port when the authority has
// none (CONNECT always carries the port in practice; an absolute-form URL
// may elide it).
func previewDialAddr(authority, scheme string) string {
	if _, _, err := net.SplitHostPort(authority); err == nil {
		return authority
	}
	port := "443"
	if scheme == "http" {
		port = "80"
	}
	return net.JoinHostPort(strings.Trim(authority, "[]"), port)
}

// previewGuardDropHeaders is the set stripped when the first absolute-form
// request is re-serialized: the proxy-targeted fields. Framing headers
// (Content-Length, Transfer-Encoding, Connection, Upgrade) deliberately
// ride through — after the re-serialized request line the connection is a
// dumb byte tunnel where only byte-identical framing stays correct, and a
// ws:// 101 upgrade needs its headers intact to negotiate.
var previewGuardDropHeaders = map[string]bool{
	"Proxy-Connection":    true,
	"Proxy-Authorization": true,
	"Proxy-Authenticate":  true,
	"Keep-Alive":          true,
}

// previewGuardForward writes req to target as an origin-form request line
// plus the surviving headers — but NEVER the body: the byte tunnel
// starting right after carries body bytes (and every later request on this
// keep-alive conn) verbatim. Cloned to Path/RawQuery only, RequestURI
// gone, Proxy-* fields stripped.
func previewGuardForward(target net.Conn, req *http.Request) error {
	path := req.URL.Path
	if path == "" {
		path = "/"
	}
	if req.URL.RawQuery != "" {
		path += "?" + req.URL.RawQuery
	}
	var b strings.Builder
	fmt.Fprintf(&b, "%s %s HTTP/1.1\r\n", req.Method, path)
	if req.Host != "" {
		fmt.Fprintf(&b, "Host: %s\r\n", req.Host)
	}
	for name, values := range req.Header {
		if previewGuardDropHeaders[name] {
			continue
		}
		for _, v := range values {
			fmt.Fprintf(&b, "%s: %s\r\n", name, v)
		}
	}
	b.WriteString("\r\n")
	_, err := io.WriteString(target, b.String())
	return err
}

// previewGuardTunnel dumb-tunnels bytes between the (bufio-wrapped) client
// conn and target until either direction ends, then closes both. br — not
// the raw conn — is the client read side: http.ReadRequest may already
// have buffered bytes beyond the first request's headers. Duplex shutdown
// is intentionally crude (full close on first EOF): keep-alive, streaming
// bodies, and upgraded (101) protocols all end by closing, and the guard's
// close() kills both ends regardless.
func previewGuardTunnel(br *bufio.Reader, client, target net.Conn) {
	var once sync.Once
	kill := func() {
		once.Do(func() {
			_ = client.Close()
			_ = target.Close()
		})
	}
	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		_, _ = io.Copy(target, br)
		kill()
		wg.Done()
	}()
	go func() {
		_, _ = io.Copy(client, target)
		kill()
		wg.Done()
	}()
	wg.Wait()
}

// previewGuardEnv builds the capture child's environment: base with
// managed proxy keys STRIPPED (an operator shell's exported http_proxy
// must not shadow the guard — duplicate keys in exec environ read
// ambiguously), then the guard endpoint on all four case variants and an
// explicitly EMPTY no_proxy/NO_PROXY so no implicit <-loopback> bypass
// keeps loopback traffic away from the guard (uniform routing: every
// request incl. loopback hits the guard). npm_config_offline is pinned
// true: npm honors the generic proxy vars too, and the guard exists to
// police the CAPTURE (browser traffic) — npx's own registry resolution is
// daemon-side tooling the warm spawn (warmPreviewCLI) already completed
// over npm's pre-guard direct path, so the guarded spawn runs cache-only
// (only-if-cached: zero egress attempts possible, and none can die against
// the guard as a bogus boundary refusal — ODO_PREVIEW_LIVE caught exactly
// that, blocked registry.npmjs.org:443 from npx itself, before this
// split).
func previewGuardEnv(base []string, proxyURL string) []string {
	env := make([]string, 0, len(base)+7)
	for _, e := range base {
		key := e
		if i := strings.IndexByte(e, '='); i >= 0 {
			key = e[:i]
		}
		switch strings.ToLower(key) {
		case "http_proxy", "https_proxy", "no_proxy", "npm_config_offline":
			continue
		}
		env = append(env, e)
	}
	return append(env,
		"HTTP_PROXY="+proxyURL,
		"HTTPS_PROXY="+proxyURL,
		"http_proxy="+proxyURL,
		"https_proxy="+proxyURL,
		"NO_PROXY=",
		"no_proxy=",
		"npm_config_offline=true",
	)
}

// previewBoundaryRefusal is the post-capture refusal (2026-08-24 review
// finding 2): the guard blocked at least one request naming a host outside
// the loopback boundary, so the captured PNG may smear external bytes into
// the vision leg — the file is dropped and the whole capture refuses.
// Accept-refusal family shape (dirtyPatchRefusal/extraEditsRefusal): the
// first 3 hosts are spelled out, the rest summarized "… (+N more)". The
// refusal surfaces through the same shotErr path as
// classifyPreviewFailure's classes — the handler journals the attempt
// without attachments and pairs an agent_error.
func previewBoundaryRefusal(hosts []string) error {
	shown := hosts
	if len(shown) > 3 {
		shown = append(shown[:3:3], fmt.Sprintf("… (+%d more)", len(hosts)-3))
	}
	return fmt.Errorf("preview refused the capture: the page tried to leave the loopback boundary (blocked: %s)", strings.Join(shown, ", "))
}

// previewShot is one captured screenshot and its receipt facts: the bytes
// the gateway request will carry (read ONCE — the user_message then
// journals sha16/bytes of exactly this preimage, ADR-0003) plus the
// preview_captured event's full-sha256 and capture wall time.
type previewShot struct {
	Path   string
	Data   []byte
	Sha16  string
	Sha256 string // full hex — the capture event's audit hash
	WaitMs int64
}

// runPreviewScreenshot performs one per-shot capture under
// previewChildTimeout, in two spawns (2026-08-24, finding-2 fallout):
// warmPreviewCLI resolves/warms the playwright CLI over npm's unguarded
// direct path, then the guarded screenshot spawn runs behind the
// capture-time previewGuard (argv + env proxy, npm pinned offline) —
// navigate → capture → exit, and one blocked request refuses the whole
// capture post-hoc. Failures map to readable chat errors (missing
// node/npx, missing chromium with the install command, navigation timeout,
// refused host).
func runPreviewScreenshot(ctx context.Context, rawURL, out string) (previewShot, error) {
	npx, err := resolvePreviewNpx()
	if err != nil {
		return previewShot{}, err
	}
	cctx, cancel := context.WithTimeout(ctx, previewChildTimeout)
	defer cancel()
	// Phase 1 (of 2): resolve/warm the CLI WITHOUT the guard — npm's
	// stale-cache registry fetch keeps the direct-network path it had
	// before the guard existed (tooling traffic is not capture traffic),
	// so the guarded phase-2 spawn can pin npm offline.
	if err := warmPreviewCLI(cctx, npx); err != nil {
		return previewShot{}, err
	}
	// Finding 2: the loopback-only capture guard starts BEFORE the capture
	// child (so its argv can name the address) and dies only after the
	// output file is read — no guard conn or goroutine outlives the
	// capture.
	guard, err := startPreviewGuard()
	if err != nil {
		return previewShot{}, fmt.Errorf("preview: start the capture-time guard: %w", err)
	}
	defer guard.close()
	if previewGuardHook != nil { // test-only seam
		previewGuardHook(guard)
	}
	cmd := exec.CommandContext(cctx, npx,
		"-y", "playwright@^1", "screenshot",
		"--viewport-size="+previewViewport,
		"--wait-for-timeout="+previewSettleMs,
		// Finding 2: the guard rides the argv too, not just the env (the ODO_PREVIEW_LIVE
		// empirical check, 2026-08-24): env vars reach env-honoring children (and
		// chromium on Linux), but playwright's explicit --proxy-server flag is the
		// channel GUARANTEED to reach its chromium on every platform; with no
		// --proxy-bypass, loopback has no special pass — every request hits the guard.
		"--proxy-server=http://"+guard.addr(),
		rawURL, out)
	// Per-shot lifecycle guarantee (lock item 2): on the deadline, kill the
	// whole process GROUP, not just npx — npx spawns node, which spawns the
	// headless chromium; killing only the top PID orphans the subtree
	// (review finding D3). Same pattern as internal/adapter/omp.go.
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error {
		return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
	}
	cmd.Env = previewGuardEnv(previewChildEnv(), "http://"+guard.addr())
	start := time.Now()
	output, err := cmd.CombinedOutput()
	waitMs := time.Since(start).Milliseconds()
	// Post-capture refusal (2026-08-24 review finding 2): ≥1 blocked host
	// means the PNG may smear boundary-external bytes into the vision leg
	// — drop the file and refuse the whole capture. This outranks the
	// child's own failure class: a denied main-frame hop usually fails the
	// child TOO (proxy 403 → navigation error), and the refusal names the
	// actual cause.
	if blocked := guard.blockedHosts(); len(blocked) > 0 {
		_ = os.Remove(out)
		return previewShot{}, previewBoundaryRefusal(blocked)
	}
	if err != nil {
		return previewShot{}, classifyPreviewFailure(cctx, err, string(output))
	}
	data, rerr := os.ReadFile(out)
	if rerr != nil {
		return previewShot{}, fmt.Errorf("preview: read captured screenshot: %w", rerr)
	}
	sum := sha256.Sum256(data)
	return previewShot{
		Path:   out,
		Data:   data,
		Sha16:  sha16(data),
		Sha256: hex.EncodeToString(sum[:]),
		WaitMs: waitMs,
	}, nil
}

// classifyPreviewFailure maps a playwright child failure to a readable
// chat error, ordered most-actionable first. output is the child's
// combined stdout+stderr.
func classifyPreviewFailure(ctx context.Context, err error, output string) error {
	tail := lastLines(output, 3)
	switch {
	case ctx.Err() == context.DeadlineExceeded:
		return fmt.Errorf("preview screenshot timed out after %ds (navigation timeout, hung page, or the first run still downloading playwright — a warm-cache retry usually succeeds) — is the dev server serving and responsive?", int(previewChildTimeout/time.Second))
	case strings.Contains(output, "Executable doesn't exist") || strings.Contains(output, "browserType.launch"):
		return fmt.Errorf("preview's headless chromium is not installed — run: %s", previewInstallHint)
	case strings.Contains(output, "net::ERR_CONNECTION_REFUSED"):
		return fmt.Errorf("preview could not reach the URL (connection refused) — is the dev server running?")
	case strings.Contains(output, "net::ERR_NAME_NOT_RESOLVED"):
		return fmt.Errorf("preview could not resolve the URL host")
	case exitCode(err) == 127:
		return fmt.Errorf("preview needs node/npx — the child failed to launch (exit 127): %s", tail)
	default:
		return fmt.Errorf("preview screenshot failed: %s", tail)
	}
}

// warmPreviewCLI is phase 1 of the capture's two-spawn shape: a plain
// `npx -y playwright@^1 --version` with the DAEMON-style child env (no
// guard proxy vars). It exists so the guarded phase-2 spawn can pin
// npm_config_offline=true: npm honors generic proxy env vars, and an
// npm registry fetch routing through the guard would 403-die and read as
// a bogus loopback-boundary refusal (the guard polices CAPTURE traffic;
// npx resolution is daemon tooling that went direct before the guard
// existed). Phase 1 warms/resolves exactly like the pre-guard single
// spawn did — including the cold first-run download path the timeout
// class already names — and phase 2 then resolves only-if-cached with
// zero egress attempts possible (ODO_PREVIEW_LIVE-driven, 2026-08-24).
// Failures reuse the capture's classify family — the messages (missing
// node/npx, first-run-download timeout) describe this spawn precisely.
func warmPreviewCLI(ctx context.Context, npx string) error {
	cmd := exec.CommandContext(ctx, npx, "-y", "playwright@^1", "--version")
	// Same per-shot lifecycle guarantee as the capture spawn (finding D3):
	// kill the whole process GROUP on the deadline.
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error {
		return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
	}
	cmd.Env = previewChildEnv()
	output, err := cmd.CombinedOutput()
	if err != nil {
		return classifyPreviewFailure(ctx, err, string(output))
	}
	return nil
}

// exitCode extracts an *exec.ExitError's code (-1 when the error carries
// none: a signal kill or the deadline's Kill).
func exitCode(err error) int {
	var ee *exec.ExitError
	if errors.As(err, &ee) {
		return ee.ExitCode()
	}
	return -1
}

// lastLines keeps the last n non-empty lines of child output for a chat
// error (playwright's banner is long; the cause is at the end).
func lastLines(s string, n int) string {
	var lines []string
	for _, l := range strings.Split(strings.TrimSpace(s), "\n") {
		if l = strings.TrimSpace(l); l != "" {
			lines = append(lines, l)
		}
	}
	if len(lines) == 0 {
		return "(no output)"
	}
	if len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	return strings.Join(lines, " | ")
}

// handlePreviewQuery routes /preview through one headless capture and then
// the /vision pipeline. Mirrors handleVisionQuery's structure — gates and
// slash-slot registration (M12, one critical section), the slashModeVision
// context block BEFORE the user_message journals, the fail-closed receipt
// closure, then the K3 direct-API call — with the loopback allowlist as a
// pre-journal refusal and the capture facts journaled as preview_captured
// right after its user_message (so the badge lands inside the run it
// belongs to).
func (s *Server) handlePreviewQuery(ctx context.Context, c *store.Conversation, text string) (Response, error) {
	rawURL, prompt, err := parsePreviewArgs(text)
	if err != nil {
		return Response{}, err
	}
	// Locked boundary 1: pre-journal refusal — nothing is spawned, nothing
	// is journaled; the IPC error renders as the chat error surface.
	if err := validatePreviewURL(rawURL); err != nil {
		return Response{}, err
	}
	// Locked boundary 1b (P1, 2026-08-24): the initial check alone was
	// bypassable — the playwright CLI follows main-frame 30x redirects to
	// ANY host. Walk the chain (each hop revalidated) and capture the
	// FINAL URL, keeping screened == captured.
	finalURL, err := resolvePreviewFinalURL(ctx, rawURL)
	if err != nil {
		return Response{}, err
	}
	if prompt == "" {
		prompt = previewDefaultPrompt
	}

	// M12: same gates + slash-slot registration as /panel and /vision (one
	// critical section — see handlePanelQuery).
	s.mu.Lock()
	if runID, ok := s.byConv[c.ID]; ok {
		if meta := s.runs[runID]; meta != nil && !meta.finished {
			s.mu.Unlock()
			return Response{}, fmt.Errorf("send_message: agent already running for conversation %d", c.ID)
		}
	}
	if err := s.gateAutoDistillForSendLocked(ctx, c.ID); err != nil {
		s.mu.Unlock()
		return Response{}, err
	}
	s.slashing[c.ID]++
	s.mu.Unlock()
	defer s.releaseSlashSlot(ctx, c.ID)

	w, err := s.store.GetWorkstream(ctx, c.WorkstreamID)
	if err != nil {
		return Response{}, err
	}

	// Vision's lean contract (slashModeVision): the block is assembled
	// before the /preview user_message journals, so it never contains the
	// preview question itself. One events fetch feeds the conversation tail.
	var events []store.Event
	if evs, lerr := s.store.ListEvents(ctx, c.ID, 0); lerr == nil {
		events = evs
	}
	scope := resolvePanelContextScope()
	system := "You are a vision-capable coding assistant. Analyze the image or screenshot described in the prompt. Identify visual issues, layout problems, or design suggestions."
	block, receipt, convBlock, conv := s.slashContextBlock(ctx, w.Name, c.ID, prompt, events, scope, slashModeVision)
	if block != "" {
		system += "\n\n---\n\n" + block
	}

	// Capture BEFORE journaling the user_message (the /vision pre-read
	// ordering): the receipt then covers exactly the PNG bytes the gateway
	// request carries. A capture failure still journals the attempt — with
	// the paths absent (no attachment was ever produced; ADR-0003 inv 5: a
	// receipt never claims bytes that were not read) — and refuses with a
	// paired agent_error BEFORE any moa call (the send path's
	// evidence-first ordering; failRun precedent at the receipt breach).
	attachDir, err := attachmentDir(s.projectRoot)
	if err != nil {
		return Response{}, fmt.Errorf("preview: mkdir attachments: %w", err)
	}
	urlSum := sha256.Sum256([]byte(rawURL))
	out := filepath.Join(attachDir, fmt.Sprintf("preview-%d-%s.png", time.Now().Unix(), hex.EncodeToString(urlSum[:4])))
	shot, shotErr := runPreviewScreenshot(ctx, finalURL, out)

	msgPayload := slashUserMessagePayload("/preview", text, receipt, scope, len(system)+len(prompt), conv)
	// P1 (2026-08-24): when the 30x chain moved, the wire URL differs from
	// the typed one — the receipts name both (passwords redacted like url).
	if finalURL != rawURL {
		msgPayload["final_url"] = redactPreviewURL(finalURL)
	}
	if shotErr == nil {
		msgPayload["attachments"] = []string{shot.Path}
		msgPayload["image_sha16"] = []string{shot.Sha16}
		msgPayload["image_bytes"] = len(shot.Data)
	}
	ev, err := s.store.AppendEvent(ctx, c.ID, store.EventUserMessage, mustJSON(msgPayload))
	if err != nil {
		return Response{}, err
	}
	if shotErr != nil {
		return Response{}, s.failRun(ctx, c.ID, shotErr)
	}

	// The capture receipt: full-sha256 (audit-grade, the user_message's
	// sha16 covers the wire bytes), size, and the per-shot wall time. The
	// URL is journaled redacted — a loopback BASIC-auth password must not
	// persist verbatim (review finding: userinfo leaks into the journal).
	capReceipt := map[string]interface{}{
		"url":     redactPreviewURL(rawURL),
		"bytes":   len(shot.Data),
		"sha256":  shot.Sha256,
		"wait_ms": shot.WaitMs,
	}
	if finalURL != rawURL {
		capReceipt["final_url"] = redactPreviewURL(finalURL)
	}
	if _, err := s.store.AppendEvent(ctx, c.ID, store.EventPreviewCaptured, mustJSON(capReceipt)); err != nil {
		return Response{}, err
	}

	// W2 item 4: fail closed BEFORE any moa call — the attempt (above) and
	// the breach (agent_error) both stay on record.
	if aerr := s.assertSlashReceipts(block, receipt, convBlock); aerr != nil {
		return Response{}, s.failRun(ctx, c.ID, fmt.Errorf("slash receipt assertion failed: %w", aerr))
	}

	client := s.sharedMoa() // P1 #10: shared cap, no per-leg semaphore
	// P1 #9: same outer deadline as the /panel leg (one worst-case
	// attempt chain) — a hung K3 leg must not hold the consult.
	vctx, cancel := context.WithTimeout(ctx, s.legTimeout(slashVisionModel))
	defer cancel()
	res, qerr := client.QueryWithImages(vctx, slashVisionModel, system, prompt, []moa.VisionImage{
		{Path: shot.Path, MediaType: moa.ImageMediaType(shot.Path), Data: shot.Data},
	})

	var resultText string
	if qerr != nil {
		resultText = "(vision error: " + qerr.Error() + ")"
	} else {
		resultText = "## " + slashVisionModel + "\n\n" + res.Text
		if res.Truncated {
			resultText += truncationMarker(res.Budget, len(res.Escalations))
		}
	}

	// vision:true (plus preview:true provenance) keeps the fold-eligibility
	// and slash-turn exclusions the vision flag already buys (auto.go's
	// measureWindow, collectSlashTurns); the GUI renders it with the
	// existing vision event rendering.
	agentPayload := map[string]interface{}{
		"text":    resultText,
		"vision":  true,
		"preview": true,
	}
	if qerr == nil && res.Truncated {
		agentPayload["truncated"] = true
		agentPayload["output_tokens"] = res.OutputTokens
	}
	if _, err := s.store.AppendEvent(ctx, c.ID, store.EventAgentText, mustJSON(agentPayload)); err != nil {
		return Response{}, err
	}
	if _, err := s.store.AppendEvent(ctx, c.ID, store.EventAgentDone, mustJSON(map[string]interface{}{
		"vision":  true,
		"preview": true,
	})); err != nil {
		return Response{}, err
	}

	return Response{Event: &ev}, nil
}
