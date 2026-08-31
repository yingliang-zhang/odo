package ipc

// /preview tests: the loopback allowlist (pre-journal refusal — nothing
// spawns for external hosts), the per-shot capture receipt (user_message
// image_sha16/image_bytes of exactly the wire bytes; preview_captured's
// full sha256 + wait_ms), the default UI-reviewer prompt, failure-class
// chat errors, and the hermes-Node npx preference. The playwright child is
// mocked via a PATH-installed npx shell script (the package's standard
// exec-handler test pattern — cf. omp_usage_test.go); no real browser
// launches in tests. The capture-time loopback guard (2026-08-24 finding
// 2) is covered hermetically against httptest targets (CONNECT tunneling,
// off-boundary denials, absolute-form forwarding, conn reaping), the
// child's proxy-env contract via a mock-npx env echo, and end-to-end by
// the opt-in ODO_PREVIEW_LIVE=1 live capture.

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"crypto/tls"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/yingliang-zhang/odo/internal/store"
)

// previewMoaRecorder captures what the K3 call put on the wire: routed
// model, the USER-text prompt, and the decoded image blocks. (Extents
// beyond slashctx_test's moaRecorder because /preview tests pin the
// default/substituted prompt, not just the system block.)
type previewMoaRecorder struct {
	Model  string
	Text   string
	Images []moaImageBlock
}

// previewMoaServer mocks the MoA gateway for /preview: fixed answer,
// records the last request's model, text content blocks, and image blocks.
func previewMoaServer(t *testing.T, text string, rec *previewMoaRecorder) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		var req struct {
			Model    string `json:"model"`
			Messages []struct {
				Content []struct {
					Type string `json:"type"`
					Text string `json:"text"`
				} `json:"content"`
			} `json:"messages"`
		}
		_ = json.Unmarshal(body, &req)
		rec.Model = req.Model
		rec.Text = ""
		for _, m := range req.Messages {
			for _, b := range m.Content {
				if b.Type == "text" {
					rec.Text += b.Text
				}
			}
		}
		rec.Images = probeImages(body)
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]interface{}{
			"content":     []map[string]string{{"type": "text", "text": text}},
			"stop_reason": "end_turn",
		})
	}))
	t.Cleanup(srv.Close)
	return srv
}

// previewFixturePNG is the byte content the mock npx "captures" — distinct
// bytes so sha receipts are provable.
var previewFixturePNG = []byte("\x89PNG\r\n\x1a\n" + strings.Repeat("odo-preview-fixture", 64))

// installMockNpx writes script as `npx` in a temp dir prepended to PATH
// and returns the PATH dir. The script runs with previewChildEnv
// (os.Environ + hermes-first PATH), so test env vars set before the send
// (MOCK_NPX_LOG, MOCK_PNG_FIXTURE) are visible to it. Each capture
// invokes it TWICE (2026-08-24 finding-2 split): the unguarded
// `--version` warm probe, then the guarded `screenshot` call whose env
// additionally carries the guard proxy vars — scripts branch on the
// final argv element (see mockNpxSuccess).
func installMockNpx(t *testing.T, script string) string {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "npx"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	return dir
}

// mockNpxSuccess is a playwright stand-in: it logs the invocation and
// copies the fixture PNG to the output path (the final argv element).
// Every mock handles the capture's TWO-call shape (2026-08-24 finding-2
// split): `--version` is the unguarded warm/resolve probe (succeed, echo
// nothing else), `screenshot` is the scenario under test.
const mockNpxSuccess = `#!/bin/sh
echo "path-npx:$*" >> "$MOCK_NPX_LOG"
last=""
for a in "$@"; do last="$a"; done
if [ "$last" = "--version" ]; then
  echo "Version 1.62.1"
  exit 0
fi
cp "$MOCK_PNG_FIXTURE" "$last"
`

// previewTargetServer is the probe-reachable loopback target (P1,
// 2026-08-24: resolvePreviewFinalURL must reach the URL BEFORE capture
// spawns, so tests can no longer point at dead ports like :1420). It
// answers 204 to every method, HEAD included.
func previewTargetServer(t *testing.T) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))
	t.Cleanup(srv.Close)
	return srv
}

// seedPreviewRig is the slash-test rig for /preview: repo, isolated HOME,
// stub agent wrapper, memory fixture, mocked gateway, mocked npx.
func seedPreviewRig(t *testing.T, rec *previewMoaRecorder) (*testRig, int64) {
	t.Helper()
	root := initRepo(t)
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("ODO_OMP_WRAPPER", writeStub(t, stubWrapper))
	seedSlashFixture(t, root, home)
	t.Setenv("MOA_BASE_URL", previewMoaServer(t, "preview-answer", rec).URL)
	t.Setenv("SUDO_CODING_KEY", "test-key")
	fixture := filepath.Join(t.TempDir(), "fixture.png")
	if err := os.WriteFile(fixture, previewFixturePNG, 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("MOCK_PNG_FIXTURE", fixture)
	log := filepath.Join(t.TempDir(), "npx.log")
	t.Setenv("MOCK_NPX_LOG", log)

	rig := startRig(t, root)
	t.Cleanup(func() { rig.stop(t) })
	boot := rig.call(t, Request{Cmd: CmdBootstrap, ProjectRoot: root})
	return rig, boot.Conversation.ID
}

func TestPreviewArgParsing(t *testing.T) {
	t.Parallel()
	if _, _, err := parsePreviewArgs(""); err == nil {
		t.Error("empty args: want error")
	}
	url, prompt, err := parsePreviewArgs("http://localhost:1420")
	if err != nil || url != "http://localhost:1420" || prompt != "" {
		t.Errorf("url only: got (%q, %q, %v)", url, prompt, err)
	}
	url, prompt, err = parsePreviewArgs("  http://localhost:1420/app   check the   header ")
	if err != nil || url != "http://localhost:1420/app" || prompt != "check the header" {
		t.Errorf("url+prompt: got (%q, %q, %v)", url, prompt, err)
	}
}

func TestPreviewHostAllowlist(t *testing.T) {
	t.Parallel()
	accepts := []string{
		"http://localhost:1420",
		"http://LOCALHOST/app",
		"http://127.0.0.1:8080/x?y=1",
		"http://[::1]:3000",
		"https://localhost",
	}
	for _, u := range accepts {
		if err := validatePreviewURL(u); err != nil {
			t.Errorf("accept %s: %v", u, err)
		}
	}
	rejects := []struct{ url, wantErr string }{
		{"https://example.com", "restricted to localhost"},
		{"http://localhost.evil.com", "restricted to localhost"},
		{"http://foo.localhost", "restricted to localhost"},
		{"http://localhost.", "restricted to localhost"},
		{"http://192.168.1.5", "restricted to localhost"},
		// Userinfo confused-deputy vectors: Hostname() must strip the
		// `userinfo@` prefix — a future url.Host regression would silently
		// re-allow these (review finding D7).
		{"http://localhost:3000@evil.com/x", "restricted to localhost"},
		{"http://***@evil.com/x", "restricted to localhost"},
		{"http://0x7f000001", "restricted to localhost"},
		{"ftp://localhost/x", "scheme must be http or https"},
		{"notaurl", "scheme must be http or https"},
	}
	for _, tc := range rejects {
		err := validatePreviewURL(tc.url)
		if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
			t.Errorf("reject %s: err = %v, want substring %q", tc.url, err, tc.wantErr)
		}
	}
}

// TestPreviewRouteFlow pins the full happy path: the capture's attachment
// receipt on the user_message, the preview_captured event, the K3 wire
// call (model, prompt text, one PNG block of exactly the fixture bytes),
// and the vision-shaped answer events.
func TestPreviewRouteFlow(t *testing.T) {
	rec := &previewMoaRecorder{}
	rig, convID := seedPreviewRig(t, rec)
	installMockNpx(t, mockNpxSuccess)
	srv := previewTargetServer(t)

	sent := rig.call(t, Request{Cmd: CmdSendMessage, ConversationID: convID, Text: "/preview " + srv.URL + " check the header layout"})

	// 2026-08-25 review P1: BOTH spawn legs name the pinned exact-version
	// spec — a mutable range here would auto-install new registry code
	// with daemon privileges on every screenshot.
	if log, err := os.ReadFile(os.Getenv("MOCK_NPX_LOG")); err != nil {
		t.Fatalf("npx never ran: %v", err)
	} else {
		for _, want := range []string{
			"-y " + previewPlaywrightSpec + " --version",
			"-y " + previewPlaywrightSpec + " screenshot",
		} {
			if !strings.Contains(string(log), want) {
				t.Errorf("npx log = %q, want %q", log, want)
			}
		}
	}
	var p struct {
		Attachments []string `json:"attachments"`
		ImageSha16  []string `json:"image_sha16"`
		ImageBytes  int      `json:"image_bytes"`
	}
	if err := json.Unmarshal(sent.Event.Payload, &p); err != nil {
		t.Fatalf("user_message payload: %v", err)
	}
	if len(p.Attachments) != 1 {
		t.Fatalf("attachments = %v, want exactly the captured PNG", p.Attachments)
	}
	png := p.Attachments[0]
	if dir := filepath.Dir(png); !strings.HasSuffix(dir, filepath.Join(".odo", "attachments")) {
		t.Errorf("capture path %q, want inside .odo/attachments", png)
	}
	if !regexp.MustCompile(`^preview-\d+-[0-9a-f]{8}\.png$`).MatchString(filepath.Base(png)) {
		t.Errorf("capture name %q, want preview-<unixts>-<sha8>.png", filepath.Base(png))
	}
	if p.ImageBytes != len(previewFixturePNG) {
		t.Errorf("image_bytes = %d, want %d (fixture bytes)", p.ImageBytes, len(previewFixturePNG))
	}
	if want := sha16(previewFixturePNG); len(p.ImageSha16) != 1 || p.ImageSha16[0] != want {
		t.Errorf("image_sha16 = %v, want [%s]", p.ImageSha16, want)
	}

	// preview_captured lands after its user_message with url, bytes, the
	// FULL sha256, and a non-negative capture wall time.
	events := rig.call(t, Request{Cmd: CmdPollEvents, ConversationID: convID, AfterSeq: 0}).Events
	var capEv *store.Event
	for i := range events {
		if events[i].Type == store.EventPreviewCaptured {
			capEv = &events[i]
		}
	}
	if capEv == nil {
		t.Fatal("no preview_captured event journaled")
	}
	if capEv.Seq <= sent.Event.Seq {
		t.Errorf("preview_captured seq %d must follow the user_message seq %d", capEv.Seq, sent.Event.Seq)
	}
	var cp struct {
		URL      string `json:"url"`
		FinalURL string `json:"final_url"`
		Bytes    int    `json:"bytes"`
		Sha256   string `json:"sha256"`
		WaitMs   int64  `json:"wait_ms"`
	}
	if err := json.Unmarshal(capEv.Payload, &cp); err != nil {
		t.Fatalf("preview_captured payload: %v", err)
	}
	sum := sha256.Sum256(previewFixturePNG)
	if cp.URL != srv.URL || cp.FinalURL != "" || cp.Bytes != len(previewFixturePNG) || cp.Sha256 != hex.EncodeToString(sum[:]) || cp.WaitMs < 0 {
		t.Errorf("preview_captured = %+v, want url=%s final_url absent (no redirect) bytes=%d sha256=%s wait_ms>=0",
			cp, srv.URL, len(previewFixturePNG), hex.EncodeToString(sum[:]))
	}

	// Wire end: one K3 call, the user's prompt as text, one PNG image
	// block decoding back to the journaled bytes.
	if rec.Model != "t9s/kimi-k3" {
		t.Errorf("/preview routed to %q, want t9s/kimi-k3", rec.Model)
	}
	if !strings.Contains(rec.Text, "check the header layout") {
		t.Errorf("wire text = %q, want the user's prompt", rec.Text)
	}
	if len(rec.Images) != 1 || rec.Images[0] != (moaImageBlock{len(previewFixturePNG), "image/png"}) {
		t.Errorf("wire image blocks = %+v, want [{%d image/png}]", rec.Images, len(previewFixturePNG))
	}

	// The answer enters chat with the vision rendering (the vision flag
	// also keeps fold-eligibility and slash-turn exclusions working).
	var sawAnswer, sawDone bool
	for _, ev := range events {
		if ev.Type == store.EventAgentText {
			var a struct {
				Text    string `json:"text"`
				Vision  bool   `json:"vision"`
				Preview bool   `json:"preview"`
			}
			if json.Unmarshal(ev.Payload, &a) == nil && a.Vision && a.Preview {
				if !strings.Contains(a.Text, "preview-answer") || !strings.HasPrefix(a.Text, "## t9s/kimi-k3") {
					t.Errorf("answer text = %q, want '## t9s/kimi-k3' header + body", a.Text)
				}
				sawAnswer = true
			}
		}
		if ev.Type == store.EventAgentDone {
			var d struct {
				Vision bool `json:"vision"`
			}
			if json.Unmarshal(ev.Payload, &d) == nil && d.Vision {
				sawDone = true
			}
		}
	}
	if !sawAnswer || !sawDone {
		t.Errorf("answer events: agent_text=%v agent_done=%v", sawAnswer, sawDone)
	}
}

// TestPreviewDefaultPrompt: with no prompt after the URL, the UI-reviewer
// default reaches the gateway instead.
func TestPreviewDefaultPrompt(t *testing.T) {
	rec := &previewMoaRecorder{}
	rig, convID := seedPreviewRig(t, rec)
	installMockNpx(t, mockNpxSuccess)

	rig.call(t, Request{Cmd: CmdSendMessage, ConversationID: convID, Text: "/preview " + previewTargetServer(t).URL})
	if !strings.Contains(rec.Text, "Analyze this screenshot as a UI reviewer") {
		t.Errorf("wire text = %q, want the default UI-reviewer prompt", rec.Text)
	}
}

// TestPreviewRejectsBeforeSpawn: allowlist/scheme refusals are pre-journal
// — the IPC error carries the restriction text, no npx runs (the log and
// the .png stay absent), nothing journals.
func TestPreviewRejectsBeforeSpawn(t *testing.T) {
	rec := &previewMoaRecorder{}
	rig, convID := seedPreviewRig(t, rec)
	installMockNpx(t, mockNpxSuccess)

	for _, tc := range []struct{ text, wantErr string }{
		{"/preview https://example.com", "restricted to localhost"},
		{"/preview ftp://localhost/x", "scheme must be http or https"},
		{"/preview", "URL is required"},
	} {
		resp := rig.callExpectErr(t, Request{Cmd: CmdSendMessage, ConversationID: convID, Text: tc.text})
		if !strings.Contains(resp.Error, tc.wantErr) {
			t.Errorf("%s: error = %q, want %q", tc.text, resp.Error, tc.wantErr)
		}
	}
	if info, err := os.Stat(os.Getenv("MOCK_NPX_LOG")); err == nil && info.Size() > 0 {
		t.Errorf("npx spawned for a rejected URL: %s", os.Getenv("MOCK_NPX_LOG"))
	}
	events := rig.call(t, Request{Cmd: CmdPollEvents, ConversationID: convID, AfterSeq: 0}).Events
	for _, ev := range events {
		if ev.Type == store.EventPreviewCaptured || strings.Contains(string(ev.Payload), "/preview") {
			t.Errorf("refused /preview journaled event %s %s", ev.Type, ev.Payload)
		}
	}
	if rec.Model != "" {
		t.Error("gateway called for a rejected URL")
	}
}

// TestPreviewRedirectToExternalRefused is the P1 (2026-08-24, finding 3)
// regression: a loopback URL whose 30x chain leaves the allowlist (cloud
// metadata host stand-in) is refused at the hop it escapes — pre-journal,
// exactly like the initial-URL check: no npx spawn, no events, no gateway
// call.
func TestPreviewRedirectToExternalRefused(t *testing.T) {
	rec := &previewMoaRecorder{}
	rig, convID := seedPreviewRig(t, rec)
	installMockNpx(t, mockNpxSuccess)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "http://169.254.169.254/latest", http.StatusFound)
	}))
	t.Cleanup(srv.Close)

	resp := rig.callExpectErr(t, Request{Cmd: CmdSendMessage, ConversationID: convID, Text: "/preview " + srv.URL})
	if !strings.Contains(resp.Error, "redirect hop 1") || !strings.Contains(resp.Error, "restricted to localhost") {
		t.Errorf("error = %q, want the refused redirect hop 1 naming the allowlist", resp.Error)
	}
	if info, err := os.Stat(os.Getenv("MOCK_NPX_LOG")); err == nil && info.Size() > 0 {
		t.Errorf("npx spawned for a redirect-refused URL: %s", os.Getenv("MOCK_NPX_LOG"))
	}
	events := rig.call(t, Request{Cmd: CmdPollEvents, ConversationID: convID, AfterSeq: 0}).Events
	for _, ev := range events {
		if ev.Type == store.EventPreviewCaptured || strings.Contains(string(ev.Payload), "/preview") {
			t.Errorf("refused /preview journaled event %s %s", ev.Type, ev.Payload)
		}
	}
	if rec.Model != "" {
		t.Error("gateway called for a redirect-refused URL")
	}
}

// TestPreviewRedirectSelfCapturesFinalURL: a 30x chain that STAYS on the
// allowlist clears the probe; the mock npx argv carries the resolved FINAL
// URL (not the typed one) and both receipts journal final_url.
func TestPreviewRedirectSelfCapturesFinalURL(t *testing.T) {
	rec := &previewMoaRecorder{}
	rig, convID := seedPreviewRig(t, rec)
	installMockNpx(t, mockNpxSuccess)

	var srv *httptest.Server
	srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/" {
			http.Redirect(w, r, srv.URL+"/login", http.StatusFound)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	t.Cleanup(srv.Close)
	final := srv.URL + "/login"

	sent := rig.call(t, Request{Cmd: CmdSendMessage, ConversationID: convID, Text: "/preview " + srv.URL + " review the login page"})

	// Capture argv: `… --wait-for-timeout=3000 <url> <out>` — the navigated
	// target is the resolved final hop, not the typed URL.
	log, err := os.ReadFile(os.Getenv("MOCK_NPX_LOG"))
	if err != nil {
		t.Fatalf("npx never ran: %v", err)
	}
	if !strings.Contains(string(log), " "+final+" ") {
		t.Errorf("npx argv = %q, want the resolved final URL %q as the capture target", log, final)
	}

	// The user_message receipt names both the typed and the final URL.
	var p struct {
		FinalURL string `json:"final_url"`
	}
	if err := json.Unmarshal(sent.Event.Payload, &p); err != nil {
		t.Fatalf("user_message payload: %v", err)
	}
	if p.FinalURL != final {
		t.Errorf("user_message final_url = %q, want %q", p.FinalURL, final)
	}

	// preview_captured: url stays the typed one, final_url the redirect
	// target — the audit trail shows exactly what was screened and shot.
	events := rig.call(t, Request{Cmd: CmdPollEvents, ConversationID: convID, AfterSeq: 0}).Events
	var capEv *store.Event
	for i := range events {
		if events[i].Type == store.EventPreviewCaptured {
			capEv = &events[i]
		}
	}
	if capEv == nil {
		t.Fatal("no preview_captured event journaled")
	}
	var cp struct {
		URL      string `json:"url"`
		FinalURL string `json:"final_url"`
	}
	if err := json.Unmarshal(capEv.Payload, &cp); err != nil {
		t.Fatalf("preview_captured payload: %v", err)
	}
	if cp.URL != srv.URL || cp.FinalURL != final {
		t.Errorf("preview_captured = %+v, want url=%s final_url=%s", cp, srv.URL, final)
	}
	if rec.Model == "" {
		t.Error("no gateway call — the redirected capture chain broke")
	}
}

// TestPreviewRedirectLoop: a chain that never terminates trips the hop
// limit with a readable error — pre-journal, nothing spawns.
func TestPreviewRedirectLoop(t *testing.T) {
	rec := &previewMoaRecorder{}
	rig, convID := seedPreviewRig(t, rec)
	installMockNpx(t, mockNpxSuccess)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "/loop", http.StatusFound)
	}))
	t.Cleanup(srv.Close)

	resp := rig.callExpectErr(t, Request{Cmd: CmdSendMessage, ConversationID: convID, Text: "/preview " + srv.URL + "/loop"})
	if !strings.Contains(resp.Error, "redirect chain exceeded 10 hops") {
		t.Errorf("error = %q, want the redirect-chain hop-limit refusal", resp.Error)
	}
	if info, err := os.Stat(os.Getenv("MOCK_NPX_LOG")); err == nil && info.Size() > 0 {
		t.Errorf("npx spawned for a redirect loop: %s", os.Getenv("MOCK_NPX_LOG"))
	}
}

// TestPreviewResolveHead405FallsBackToGet pins the probe's method fallback:
// a GET-only endpoint (HEAD → 405) still resolves to its final URL via the
// bounded GET re-probe.
func TestPreviewResolveHead405FallsBackToGet(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodHead {
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	t.Cleanup(srv.Close)

	finalURL, err := resolvePreviewFinalURL(context.Background(), srv.URL)
	if err != nil || finalURL != srv.URL {
		t.Errorf("resolve = %q, err=%v; want %q", finalURL, err, srv.URL)
	}
}

// TestPreviewFailureClasses: playwright child failures journal the attempt
// (user_message WITHOUT attachment receipt — no bytes, no claim) and then
// refuse with a paired agent_error carrying the readable class text, never
// calling the gateway.
func TestPreviewFailureClasses(t *testing.T) {
	rec := &previewMoaRecorder{}
	rig, convID := seedPreviewRig(t, rec)
	srv := previewTargetServer(t)

	for _, tc := range []struct{ name, script, wantErr string }{
		{"chromium missing (Executable)", `#!/bin/sh
last=""
for a in "$@"; do last="$a"; done
if [ "$last" = "--version" ]; then
  exit 0
fi
echo "browserType.launch: Executable doesn't exist at /x/chrome" >&2
exit 1
`, "odo preview-setup"},
		{"chromium missing (browserType)", `#!/bin/sh
last=""
for a in "$@"; do last="$a"; done
if [ "$last" = "--version" ]; then
  exit 0
fi
echo "browserType.launch: Target closed" >&2
exit 1
`, "odo preview-setup"},
		{"cli not provisioned (ENOTCACHED at the offline provision check)", `#!/bin/sh
last=""
for a in "$@"; do last="$a"; done
if [ "$last" = "--version" ]; then
  echo "npm error code ENOTCACHED" >&2
  echo "npm error Can't find a cached version of playwright" >&2
  exit 1
fi
echo "the capture spawn must never run unprovisioned" >&2
exit 1
`, "not provisioned"},
		{"connection refused", `#!/bin/sh
last=""
for a in "$@"; do last="$a"; done
if [ "$last" = "--version" ]; then
  exit 0
fi
echo "page.goto: net::ERR_CONNECTION_REFUSED at http://localhost:1420/" >&2
exit 1
`, "connection refused"},
		{"generic failure", `#!/bin/sh
last=""
for a in "$@"; do last="$a"; done
if [ "$last" = "--version" ]; then
  exit 0
fi
echo "some playwright explosion" >&2
exit 1
`, "preview screenshot failed"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			installMockNpx(t, tc.script)
			resp := rig.callExpectErr(t, Request{Cmd: CmdSendMessage, ConversationID: convID, Text: "/preview " + srv.URL})
			if !strings.Contains(resp.Error, tc.wantErr) {
				t.Errorf("error = %q, want substring %q", resp.Error, tc.wantErr)
			}
		})
	}

	events := rig.call(t, Request{Cmd: CmdPollEvents, ConversationID: convID, AfterSeq: 0}).Events
	agentErrs := 0
	userMsgs := 0
	for _, ev := range events {
		switch ev.Type {
		case store.EventUserMessage:
			var p struct {
				Text        string   `json:"text"`
				Attachments []string `json:"attachments"`
				ImageBytes  *int     `json:"image_bytes"`
			}
			if json.Unmarshal(ev.Payload, &p) == nil && strings.HasPrefix(p.Text, "/preview") {
				userMsgs++
				if len(p.Attachments) != 0 || p.ImageBytes != nil {
					t.Errorf("failed capture journaled an attachment receipt: %s", ev.Payload)
				}
			}
		case store.EventAgentError:
			var a struct {
				Error string `json:"error"`
			}
			if json.Unmarshal(ev.Payload, &a) == nil && strings.Contains(a.Error, "preview") {
				agentErrs++
			}
		}
	}
	if userMsgs != 5 || agentErrs != 5 {
		t.Errorf("journaled %d user_message + %d agent_error, want 5 each (attempt + refusal pair)", userMsgs, agentErrs)
	}
	if rec.Model != "" {
		t.Error("gateway called after a capture failure")
	}
}

// TestPreviewNpxMissing: without npx anywhere (empty PATH dir, no hermes
// Node under the test HOME), the refusal names the missing prerequisite.
func TestPreviewNpxMissing(t *testing.T) {
	rec := &previewMoaRecorder{}
	rig, convID := seedPreviewRig(t, rec)
	// A PATH with no npx and (test HOME) no ~/.hermes/node/bin/npx — the
	// handler must fail readable before any spawn.
	t.Setenv("PATH", t.TempDir())

	resp := rig.callExpectErr(t, Request{Cmd: CmdSendMessage, ConversationID: convID, Text: "/preview " + previewTargetServer(t).URL})
	if !strings.Contains(resp.Error, "node/npx") {
		t.Errorf("error = %q, want the missing node/npx hint", resp.Error)
	}
	if rec.Model != "" {
		t.Error("gateway called without a capture")
	}
}

// TestPreviewPrefersHermesNpx: a hermes-pinned npx under
// ~/.hermes/node/bin wins over a PATH npx (the daemon's pinned-Node
// posture — resolvePreviewNpx checks it FIRST).
func TestPreviewPrefersHermesNpx(t *testing.T) {
	rec := &previewMoaRecorder{}
	rig, convID := seedPreviewRig(t, rec)
	hermes := filepath.Join(os.Getenv("HOME"), ".hermes", "node", "bin")
	if err := os.MkdirAll(hermes, 0o755); err != nil {
		t.Fatal(err)
	}
	hermesScript := `#!/bin/sh
echo "hermes-npx:$*" >> "$MOCK_NPX_LOG"
last=""
for a in "$@"; do last="$a"; done
if [ "$last" = "--version" ]; then
  exit 0
fi
cp "$MOCK_PNG_FIXTURE" "$last"
`
	if err := os.WriteFile(filepath.Join(hermes, "npx"), []byte(hermesScript), 0o755); err != nil {
		t.Fatal(err)
	}
	installMockNpx(t, mockNpxSuccess) // PATH fallback — must NOT run

	rig.call(t, Request{Cmd: CmdSendMessage, ConversationID: convID, Text: "/preview " + previewTargetServer(t).URL})
	log, err := os.ReadFile(os.Getenv("MOCK_NPX_LOG"))
	if err != nil {
		t.Fatalf("npx never ran: %v", err)
	}
	if !strings.Contains(string(log), "hermes-npx:") || strings.Contains(string(log), "path-npx:") {
		t.Errorf("npx log = %q, want the hermes npx only", log)
	}
	if rec.Model == "" {
		t.Error("no gateway call — capture chain broke")
	}
}

// TestPreviewSetup: the explicit provisioning phase (2026-08-25 review
// P1) resolves the PINNED CLI and installs chromium in two npx calls with
// the network ALLOWED (no offline pin — the capture legs carry that), and
// propagates a failing step as a non-zero exit.
func TestPreviewSetup(t *testing.T) {
	t.Run("provisions the pinned CLI plus chromium", func(t *testing.T) {
		t.Setenv("HOME", t.TempDir()) // hermes npx must not win over the mock
		log := filepath.Join(t.TempDir(), "npx.log")
		t.Setenv("MOCK_NPX_LOG", log)
		installMockNpx(t, `#!/bin/sh
echo "setup-npx:$*" >> "$MOCK_NPX_LOG"
if [ "$npm_config_offline" = "true" ]; then
  echo "offline pin leaked into the network-allowed phase" >> "$MOCK_NPX_LOG"
fi
exit 0
`)
		var out, errb bytes.Buffer
		if code := PreviewSetup(&out, &errb); code != 0 {
			t.Fatalf("PreviewSetup = %d (%s)", code, errb.String())
		}
		raw, err := os.ReadFile(log)
		if err != nil {
			t.Fatalf("npx never ran: %v", err)
		}
		text := string(raw)
		if strings.Contains(text, "offline pin leaked") {
			t.Errorf("setup env must stay network-allowed (explicit install phase): %q", text)
		}
		for _, want := range []string{
			"-y " + previewPlaywrightSpec + " --version",
			previewPlaywrightSpec + " install chromium",
		} {
			if !strings.Contains(text, want) {
				t.Errorf("setup log = %q, want %q", text, want)
			}
		}
	})

	t.Run("a failing step fails the command", func(t *testing.T) {
		t.Setenv("HOME", t.TempDir())
		t.Setenv("MOCK_NPX_LOG", filepath.Join(t.TempDir(), "npx.log"))
		installMockNpx(t, "#!/bin/sh\nexit 3\n")
		var out, errb bytes.Buffer
		if code := PreviewSetup(&out, &errb); code == 0 {
			t.Error("failing npx exited 0 — provisioning failure must propagate")
		}
	})
}

// TestPreviewShotReceiptFields is the unit-level guard of the receipt
// naming the journalled wire-load: sha16 vs full-sha256 of the same bytes.
func TestPreviewShotReceiptFields(t *testing.T) {
	t.Parallel()
	sum := sha256.Sum256(previewFixturePNG)
	if got := sha16(previewFixturePNG); got != hex.EncodeToString(sum[:8]) {
		t.Errorf("sha16 = %q, want first-8-bytes hex of sha256", got)
	}
	if full := hex.EncodeToString(sum[:]); len(full) != 64 {
		t.Errorf("full sha256 hex = %d chars, want 64", len(full))
	}
}

// TestPreviewURLRedaction pins the journal-side BASIC-auth masking:
// passwords never persist verbatim in the preview_captured record; the
// username and the host survive unchanged.
func TestPreviewURLRedaction(t *testing.T) {
	t.Parallel()
	if got := redactPreviewURL("http://user:secret@localhost:3000/x"); got != "http://user:xxxxx@localhost:3000/x" {
		t.Errorf("redact = %q", got)
	}
	if got := redactPreviewURL("http://localhost:1420"); got != "http://localhost:1420" {
		t.Errorf("honest URL must pass through verbatim: %q", got)
	}
}

// TestPreviewDeadlineKillsProcessGroup pins the per-shot lifecycle
// guarantee (lock item 2, review finding D3): when the timeout fires, the
// whole process GROUP dies — npx's node/chromium descendants must NOT be
// orphaned. A hanging mock npx spawns a grandchild sleeper; the deadline
// error must surface, and the grandchild must be gone.
func TestPreviewDeadlineKillsProcessGroup(t *testing.T) {
	old := previewChildTimeout
	previewChildTimeout = 1500 * time.Millisecond
	t.Cleanup(func() { previewChildTimeout = old })

	// Isolate HOME so hermesNodeBin() finds no real npx and resolution
	// falls back to the PATH mock (same isolation as seedPreviewRig).
	t.Setenv("HOME", t.TempDir())

	pidFile := filepath.Join(t.TempDir(), "child.pid")
	t.Setenv("MOCK_CHILD_PID", pidFile)
	installMockNpx(t, `#!/bin/sh
last=""
for a in "$@"; do last="$a"; done
if [ "$last" = "--version" ]; then
  exit 0
fi
sleep 30 &
echo $! > "$MOCK_CHILD_PID"
exec sleep 30
`)
	ctx := context.Background()
	_, err := runPreviewScreenshot(ctx, t.TempDir(), "http://localhost:1420", filepath.Join(t.TempDir(), "out.png"))
	if err == nil || !strings.Contains(err.Error(), "timed out after") {
		t.Fatalf("err = %v, want the deadline-class error", err)
	}

	pidRaw, rerr := os.ReadFile(pidFile)
	if rerr != nil {
		t.Fatalf("mock never recorded its grandchild pid: %v", rerr)
	}
	pid, cerr := strconv.Atoi(strings.TrimSpace(string(pidRaw)))
	if cerr != nil {
		t.Fatalf("child pid: %v", cerr)
	}
	// Group kill is expected immediate; allow a short settle anyway.
	deadline := time.Now().Add(4 * time.Second)
	for {
		if kerr := syscall.Kill(pid, 0); kerr == syscall.ESRCH {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("grandchild pid %d survived the deadline — process group not killed", pid)
		}
		time.Sleep(100 * time.Millisecond)
	}
}

// ---------------------------------------------------------------------------
// Capture-time loopback guard (2026-08-24 tri-review P1, review finding 2)
// ---------------------------------------------------------------------------

// TestPreviewGuardConnectTunnel: an allowed CONNECT names a loopback TLS
// target and the guard tunnels real bytes both ways (a Go HTTPS client
// through the guard as proxy, against an httptest TLS server).
func TestPreviewGuardConnectTunnel(t *testing.T) {
	target := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("tls-through-tunnel:" + r.URL.Path))
	}))
	t.Cleanup(target.Close)
	g, err := startPreviewGuard()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(g.close)

	client := &http.Client{
		Transport: &http.Transport{
			Proxy:           http.ProxyURL(&url.URL{Scheme: "http", Host: g.addr()}),
			TLSClientConfig: &tls.Config{InsecureSkipVerify: true}, //nolint:gosec — hermetic httptest cert
		},
		Timeout: 10 * time.Second,
	}
	resp, err := client.Get(target.URL + "/secure")
	if err != nil {
		t.Fatalf("GET through the CONNECT tunnel: %v", err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != 200 || string(body) != "tls-through-tunnel:/secure" {
		t.Errorf("GET = %d %q, want 200 %q", resp.StatusCode, body, "tls-through-tunnel:/secure")
	}
	if g.allowedRequests() < 1 || g.dialCount() < 1 {
		t.Errorf("guard observed allowed=%d dials=%d, want >=1 each", g.allowedRequests(), g.dialCount())
	}
	if len(g.blockedRequests()) != 0 {
		t.Errorf("blocked = %v, want none", g.blockedRequests())
	}
}

// TestPreviewGuardBlocksOffBoundary: a denied host — in BOTH wire shapes
// (CONNECT authority-form and absolute-URI) — is answered 403, recorded
// with method + host, and NEVER dialed (the TEST-NET targets are
// unroutable; the dial counter is the programmatic proof, and dedupe keeps
// the refusal list readable).
func TestPreviewGuardBlocksOffBoundary(t *testing.T) {
	g, err := startPreviewGuard()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(g.close)

	deny := func(request string) int {
		t.Helper()
		conn, err := net.Dial("tcp", g.addr())
		if err != nil {
			t.Fatal(err)
		}
		defer conn.Close()
		if _, err := io.WriteString(conn, request); err != nil {
			t.Fatal(err)
		}
		resp, err := http.ReadResponse(bufio.NewReader(conn), nil)
		if err != nil {
			t.Fatalf("reading denial response: %v", err)
		}
		defer resp.Body.Close()
		return resp.StatusCode
	}

	if s := deny("CONNECT 192.0.2.1:1 HTTP/1.1\r\nHost: 192.0.2.1:1\r\n\r\n"); s != http.StatusForbidden {
		t.Errorf("CONNECT denial = %d, want 403", s)
	}
	if s := deny("GET http://198.51.100.7:8080/x.png HTTP/1.1\r\nHost: 198.51.100.7:8080\r\n\r\n"); s != http.StatusForbidden {
		t.Errorf("absolute-form denial = %d, want 403", s)
	}
	// A repeat of the first authority: recorded, but deduped for the
	// refusal message.
	if s := deny("CONNECT 192.0.2.1:1 HTTP/1.1\r\nHost: 192.0.2.1:1\r\n\r\n"); s != http.StatusForbidden {
		t.Errorf("repeat CONNECT denial = %d, want 403", s)
	}

	blocked := g.blockedRequests()
	want := []previewBlockedReq{
		{Method: "CONNECT", Host: "192.0.2.1:1"},
		{Method: "GET", Host: "198.51.100.7:8080"},
		{Method: "CONNECT", Host: "192.0.2.1:1"},
	}
	if fmt.Sprint(blocked) != fmt.Sprint(want) {
		t.Errorf("blocked record = %v, want %v", blocked, want)
	}
	hosts := g.blockedHosts()
	if len(hosts) != 2 || hosts[0] != "192.0.2.1:1" || hosts[1] != "198.51.100.7:8080" {
		t.Errorf("blockedHosts = %v, want [192.0.2.1:1 198.51.100.7:8080] (deduped, first-seen order)", hosts)
	}
	if g.dialCount() != 0 || g.allowedRequests() != 0 {
		t.Errorf("denials dialed (%d) or counted allowed (%d) — the target must never be dialed", g.dialCount(), g.allowedRequests())
	}
}

// TestPreviewGuardForwardsAbsoluteForm: an allowed absolute-URI request is
// re-serialized to origin-form and answered, and the conn then acts as a
// dumb byte tunnel — a keep-alive second request and a POST body (the
// Content-Length framing must survive) both land on the target.
func TestPreviewGuardForwardsAbsoluteForm(t *testing.T) {
	var hits []string
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(io.LimitReader(r.Body, 4096))
		hits = append(hits, r.Method+" "+r.URL.RequestURI())
		fmt.Fprintf(w, "served:%s|body:%s", r.URL.RequestURI(), body)
	}))
	t.Cleanup(target.Close)
	g, err := startPreviewGuard()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(g.close)

	client := &http.Client{
		Transport: &http.Transport{Proxy: http.ProxyURL(&url.URL{Scheme: "http", Host: g.addr()})},
		Timeout:   10 * time.Second,
	}
	get := func(path, want string) {
		t.Helper()
		resp, err := client.Get(target.URL + path)
		if err != nil {
			t.Fatalf("GET %s via guard: %v", path, err)
		}
		body, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		if resp.StatusCode != 200 || string(body) != want {
			t.Errorf("GET %s = %d %q, want 200 %q", path, resp.StatusCode, body, want)
		}
	}
	get("/one?x=1", "served:/one?x=1|body:")
	get("/two", "served:/two|body:") // keep-alive reuse through the tunnel

	resp, err := client.Post(target.URL+"/submit", "text/plain", strings.NewReader("payload-body"))
	if err != nil {
		t.Fatalf("POST via guard: %v", err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != 200 || string(body) != "served:/submit|body:payload-body" {
		t.Errorf("POST = %d %q, want the tunneled body intact", resp.StatusCode, body)
	}

	want := []string{"GET /one?x=1", "GET /two", "POST /submit"}
	if fmt.Sprint(hits) != fmt.Sprint(want) {
		t.Errorf("target hits = %v, want %v", hits, want)
	}
	if g.allowedRequests() < 1 {
		t.Errorf("allowed = %d, want >=1", g.allowedRequests())
	}
	if len(g.blockedRequests()) != 0 {
		t.Errorf("blocked = %v, want none", g.blockedRequests())
	}
}

// TestPreviewGuardCloseReapsConns: close() ends the listener AND every
// tracked connection, so a held-open tunnel dies with the capture instead
// of leaking past it.
func TestPreviewGuardCloseReapsConns(t *testing.T) {
	target := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))
	t.Cleanup(target.Close)
	g, err := startPreviewGuard()
	if err != nil {
		t.Fatal(err)
	}

	conn, err := net.Dial("tcp", g.addr())
	if err != nil {
		t.Fatal(err)
	}
	host := strings.TrimPrefix(target.URL, "https://")
	if _, err := fmt.Fprintf(conn, "CONNECT %s HTTP/1.1\r\nHost: %s\r\n\r\n", host, host); err != nil {
		t.Fatal(err)
	}
	br := bufio.NewReader(conn)
	resp, err := http.ReadResponse(br, nil)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatalf("CONNECT = %d, want 200 Connection established", resp.StatusCode)
	}

	g.close()

	// The held tunnel must be dead — prompt read failure, not a hang.
	_ = conn.SetReadDeadline(time.Now().Add(2 * time.Second))
	if _, err := br.ReadByte(); err == nil {
		t.Error("held tunnel still readable after close — a guard conn outlived the capture")
	}
	// And the listener is gone.
	if c2, err := net.Dial("tcp", g.addr()); err == nil {
		c2.Close()
		t.Error("listener still accepting after close")
	}
}

// TestPreviewGuardEnv pins the child-env contract: pre-existing proxy keys
// are STRIPPED (an operator shell's exported proxy must not shadow the
// guard), the guard endpoint lands on all four case variants, and
// no_proxy/NO_PROXY are present and EMPTY (no loopback bypass).
func TestPreviewGuardEnv(t *testing.T) {
	base := []string{
		"PATH=/bin", "HOME=/h",
		"http_proxy=http://preexisting:9", "HTTPS_PROXY=http://preexisting:9",
		"Http_Proxy=http://preexisting:8", "NO_PROXY=.corp", "no_proxy=localhost",
		"UNRELATED=keep",
	}
	out := previewGuardEnv(base, "http://127.0.0.1:5555")
	counts := map[string]int{}
	vals := map[string]string{}
	for _, e := range out {
		k, v, _ := strings.Cut(e, "=")
		switch strings.ToLower(k) {
		case "http_proxy", "https_proxy", "no_proxy":
			counts[strings.ToLower(k)]++
			vals[k] = v
			if v != "" && v != "http://127.0.0.1:5555" {
				t.Errorf("pre-existing proxy leaked through as %q", e)
			}
		}
	}
	for _, lk := range []string{"http_proxy", "https_proxy", "no_proxy"} {
		if counts[lk] != 2 {
			t.Errorf("%s: %d entries (want exactly upper+lower 2); env=%q", lk, counts[lk], out)
		}
	}
	for _, k := range []string{"HTTP_PROXY", "HTTPS_PROXY", "http_proxy", "https_proxy"} {
		if vals[k] != "http://127.0.0.1:5555" {
			t.Errorf("%s = %q, want the guard endpoint", k, vals[k])
		}
	}
	if v, ok := vals["NO_PROXY"]; !ok || v != "" {
		t.Errorf("NO_PROXY = %q (present=%v), want present and empty", v, ok)
	}
	if v, ok := vals["no_proxy"]; !ok || v != "" {
		t.Errorf("no_proxy = %q (present=%v), want present and empty", v, ok)
	}
	for _, keep := range []string{"PATH=/bin", "HOME=/h", "UNRELATED=keep"} {
		found := false
		for _, e := range out {
			if e == keep {
				found = true
			}
		}
		if !found {
			t.Errorf("non-proxy entry %q was dropped", keep)
		}
	}
}

// TestPreviewBoundaryRefusalShape pins the refusal-family shape: ≤3 hosts
// spelled out, the rest summarized "… (+N more)".
func TestPreviewBoundaryRefusalShape(t *testing.T) {
	msg := previewBoundaryRefusal([]string{"a.test:1", "b.test:2", "c.test:3"}).Error()
	if !strings.Contains(msg, "preview refused the capture: the page tried to leave the loopback boundary (blocked: a.test:1, b.test:2, c.test:3)") {
		t.Errorf("3-host refusal = %q", msg)
	}
	if strings.Contains(msg, "more") {
		t.Errorf("3-host refusal wrongly summarizes: %q", msg)
	}
	msg5 := previewBoundaryRefusal([]string{"a:1", "b:2", "c:3", "d:4", "e:5"}).Error()
	if !strings.Contains(msg5, "a:1, b:2, c:3, … (+2 more)") || strings.Contains(msg5, "d:4") {
		t.Errorf("5-host refusal = %q, want first 3 + \"… (+2 more)\"", msg5)
	}
}

// TestPreviewBlockedCaptureDropsFile is the run-level pin of finding 2:
// the child exits 0 having WRITTEN a capture, but the guard recorded an
// off-boundary attempt — the file must be REMOVED and the refusal must
// name the blocked host.
func TestPreviewBlockedCaptureDropsFile(t *testing.T) {
	t.Setenv("HOME", t.TempDir()) // no hermes npx: fall back to the PATH mock
	fixture := filepath.Join(t.TempDir(), "fixture.png")
	if err := os.WriteFile(fixture, previewFixturePNG, 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("MOCK_PNG_FIXTURE", fixture)
	log := filepath.Join(t.TempDir(), "npx.log")
	t.Setenv("MOCK_NPX_LOG", log)
	installMockNpx(t, mockNpxSuccess)

	old := previewGuardHook
	previewGuardHook = func(g *previewGuard) { g.recordBlocked("GET", "192.0.2.1:1") }
	t.Cleanup(func() { previewGuardHook = old })

	out := filepath.Join(t.TempDir(), "out.png")
	_, err := runPreviewScreenshot(context.Background(), t.TempDir(), "http://localhost:1420", out)
	if err == nil || !strings.Contains(err.Error(), "preview refused the capture") || !strings.Contains(err.Error(), "192.0.2.1:1") {
		t.Fatalf("err = %v, want the boundary refusal naming 192.0.2.1:1", err)
	}
	if _, serr := os.Stat(out); !os.IsNotExist(serr) {
		t.Errorf("capture file survived the refusal (stat err = %v)", serr)
	}
	if b, rerr := os.ReadFile(log); rerr != nil || !strings.Contains(string(b), "path-npx:") {
		t.Errorf("mock npx never ran (%v, %q) — removal must follow a WRITTEN capture", rerr, b)
	}
}

// TestPreviewBlockedAttemptRefusesRoute is the finding-2 route pin: the
// page attempts an off-boundary CONNECT through the freshly started guard
// (issued by the test via the hook seam — the mock npx child is a shell
// script that cannot reach it), the capture "succeeds", and the route
// refuses with the boundary text, journaled exactly like every shotErr —
// the attempt's user_message WITHOUT attachments pairs with an
// agent_error, no preview_captured lands, the gateway stays untouched.
func TestPreviewBlockedAttemptRefusesRoute(t *testing.T) {
	rec := &previewMoaRecorder{}
	rig, convID := seedPreviewRig(t, rec)
	installMockNpx(t, mockNpxSuccess)
	srv := previewTargetServer(t)

	old := previewGuardHook
	previewGuardHook = func(g *previewGuard) {
		conn, err := net.Dial("tcp", g.addr())
		if err != nil {
			t.Errorf("test could not reach the guard: %v", err)
			return
		}
		defer conn.Close()
		fmt.Fprintf(conn, "CONNECT 169.254.169.254:80 HTTP/1.1\r\nHost: 169.254.169.254:80\r\n\r\n")
		resp, err := http.ReadResponse(bufio.NewReader(conn), nil)
		if err != nil {
			t.Errorf("guard denial unreadable: %v", err)
			return
		}
		resp.Body.Close()
		if resp.StatusCode != http.StatusForbidden {
			t.Errorf("guard answered %d to an off-boundary CONNECT, want 403", resp.StatusCode)
		}
	}
	t.Cleanup(func() { previewGuardHook = old })

	resp := rig.callExpectErr(t, Request{Cmd: CmdSendMessage, ConversationID: convID, Text: "/preview " + srv.URL})
	if !strings.Contains(resp.Error, "preview refused the capture: the page tried to leave the loopback boundary") ||
		!strings.Contains(resp.Error, "169.254.169.254:80") {
		t.Errorf("error = %q, want the boundary refusal naming the blocked host", resp.Error)
	}

	var userMsgs, agentErrs, captured int
	for _, ev := range rig.call(t, Request{Cmd: CmdPollEvents, ConversationID: convID, AfterSeq: 0}).Events {
		switch ev.Type {
		case store.EventUserMessage:
			var p struct {
				Text        string   `json:"text"`
				Attachments []string `json:"attachments"`
				ImageBytes  *int     `json:"image_bytes"`
			}
			if json.Unmarshal(ev.Payload, &p) == nil && strings.HasPrefix(p.Text, "/preview") {
				userMsgs++
				if len(p.Attachments) != 0 || p.ImageBytes != nil {
					t.Errorf("refused capture journaled an attachment receipt: %s", ev.Payload)
				}
			}
		case store.EventAgentError:
			var a struct {
				Error string `json:"error"`
			}
			if json.Unmarshal(ev.Payload, &a) == nil && strings.Contains(a.Error, "loopback boundary") {
				agentErrs++
			}
		case store.EventPreviewCaptured:
			captured++
		}
	}
	if userMsgs != 1 || agentErrs != 1 || captured != 0 {
		t.Errorf("journaled %d user_message + %d agent_error + %d preview_captured, want 1/1/0", userMsgs, agentErrs, captured)
	}
	if rec.Model != "" {
		t.Error("gateway called after a boundary refusal")
	}
}

// TestPreviewChildGetsGuardProxyEnv is the (b) env-contract pin: the
// capture child's spawn env now carries the guard proxy on all four case
// variants with NO_PROXY/no_proxy explicitly EMPTY — and a pre-existing
// proxy config poisoned into the daemon's OWN env must not leak through
// (strip-then-set). The mock npx echoes its env to the log.
func TestPreviewChildGetsGuardProxyEnv(t *testing.T) {
	rec := &previewMoaRecorder{}
	rig, convID := seedPreviewRig(t, rec)
	t.Setenv("http_proxy", "http://preexisting-corp:9")
	t.Setenv("NO_PROXY", "internal.corp")
	installMockNpx(t, `#!/bin/sh
last=""
for a in "$@"; do last="$a"; done
if [ "$last" = "--version" ]; then
  exit 0
fi
{
  echo "env:HTTP_PROXY=$HTTP_PROXY"
  echo "env:HTTPS_PROXY=$HTTPS_PROXY"
  echo "env:http_proxy=$http_proxy"
  echo "env:https_proxy=$https_proxy"
  echo "env:NO_PROXY=$NO_PROXY"
  echo "env:no_proxy=$no_proxy"
} >> "$MOCK_NPX_LOG"
cp "$MOCK_PNG_FIXTURE" "$last"
`)

	rig.call(t, Request{Cmd: CmdSendMessage, ConversationID: convID, Text: "/preview " + previewTargetServer(t).URL})
	log, err := os.ReadFile(os.Getenv("MOCK_NPX_LOG"))
	if err != nil {
		t.Fatalf("npx never ran: %v", err)
	}
	proxies := map[string]string{}
	for _, line := range strings.Split(string(log), "\n") {
		if k, v, ok := strings.Cut(line, "="); ok && strings.HasPrefix(k, "env:") {
			proxies[strings.TrimPrefix(k, "env:")] = v
		}
	}
	proxyRe := regexp.MustCompile(`^http://127\.0\.0\.1:\d+$`)
	endpoints := map[string]bool{}
	for _, k := range []string{"HTTP_PROXY", "HTTPS_PROXY", "http_proxy", "https_proxy"} {
		v, ok := proxies[k]
		if !ok || !proxyRe.MatchString(v) {
			t.Errorf("child %s = %q, want http://127.0.0.1:<guard port>", k, v)
			continue
		}
		endpoints[v] = true
	}
	if len(endpoints) > 1 {
		t.Errorf("proxy endpoints differ across case variants: %v", proxies)
	}
	if proxies["NO_PROXY"] != "" || proxies["no_proxy"] != "" {
		t.Errorf("child NO_PROXY=%q no_proxy=%q, want both EMPTY (no implicit loopback bypass)",
			proxies["NO_PROXY"], proxies["no_proxy"])
	}
	for k, v := range proxies {
		if strings.Contains(v, "preexisting-corp") || strings.Contains(v, "internal.corp") {
			t.Errorf("child %s leaked the daemon's pre-existing proxy config %q", k, v)
		}
	}
}

// TestPreviewLiveGuardedCapture is the load-bearing empirical check
// (2026-08-24 finding 2): does the *_PROXY env contract truly reach
// playwright's headless chromium? Both subtests assert the guard OBSERVED
// traffic — a capture that bypassed the proxy (e.g. chromium's implicit
// <-loopback> bypass winning over the empty no_proxy) is detected, not
// misread as success. Offline-safe: the off-boundary target is TEST-NET-1
// and the guard blocks before any dial. Opt-in: run with
// ODO_PREVIEW_LIVE=1 (real npx + the ms-playwright chromium cache + the
// real ~/.hermes node — no HOME isolation here).
func TestPreviewLiveGuardedCapture(t *testing.T) {
	if os.Getenv("ODO_PREVIEW_LIVE") != "1" {
		t.Skip("opt-in live test — set ODO_PREVIEW_LIVE=1 to run the REAL playwright capture through the guard")
	}
	var guard *previewGuard
	old := previewGuardHook
	previewGuardHook = func(g *previewGuard) { guard = g }
	t.Cleanup(func() { previewGuardHook = old })

	t.Run("refuses a page reaching off-boundary", func(t *testing.T) {
		guard = nil
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", "text/html")
			// Port 8080, not :1 — chromium refuses :1 LOCALLY as an
			// "unsafe port" (ERR_UNSAFE_PORT) before anything reaches the
			// proxy; the guard must see this request to block it.
			_, _ = io.WriteString(w, `<!doctype html><html><body><h1>guard live test</h1><img src="http://192.0.2.1:8080/x.png"></body></html>`)
		}))
		t.Cleanup(srv.Close)

		out := filepath.Join(t.TempDir(), "live-out.png")
		_, err := runPreviewScreenshot(context.Background(), t.TempDir(), srv.URL+"/", out)
		if err == nil || !strings.Contains(err.Error(), "preview refused the capture") {
			t.Fatalf("err = %v, want the boundary refusal", err)
		}
		if !strings.Contains(err.Error(), "192.0.2.1:8080") {
			t.Errorf("refusal = %q, want the blocked TEST-NET host named", err)
		}
		if _, serr := os.Stat(out); !os.IsNotExist(serr) {
			t.Errorf("capture file survived the refusal (stat err = %v)", serr)
		}
		if guard == nil || guard.allowedRequests() == 0 {
			t.Fatal("guard saw no requests — the *_PROXY env did not reach chromium")
		}
		found := false
		for _, h := range guard.blockedHosts() {
			if h == "192.0.2.1:8080" {
				found = true
			}
		}
		if !found {
			t.Errorf("blockedHosts = %v, want 192.0.2.1:8080 recorded", guard.blockedHosts())
		}
	})

	t.Run("loopback control captures through the proxy", func(t *testing.T) {
		guard = nil
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path == "/x.png" {
				w.Header().Set("Content-Type", "image/png")
				_, _ = w.Write(previewFixturePNG)
				return
			}
			w.Header().Set("Content-Type", "text/html")
			_, _ = io.WriteString(w, `<!doctype html><html><body><h1>loopback ok</h1><img src="/x.png"></body></html>`)
		}))
		t.Cleanup(srv.Close)

		// The read-back is containment-checked against the passed root —
		// point it at the tempdir holding the capture.
		root := t.TempDir()
		out := filepath.Join(root, "live-ok.png")
		shot, err := runPreviewScreenshot(context.Background(), root, srv.URL, out)
		if err != nil {
			t.Fatalf("loopback capture failed: %v", err)
		}
		if len(shot.Data) < 8 || string(shot.Data[:4]) != "\x89PNG" {
			t.Errorf("capture bytes look wrong (%d bytes)", len(shot.Data))
		}
		if guard == nil || guard.allowedRequests() == 0 {
			t.Fatal("guard observed no loopback requests — the capture bypassed the proxy")
		}
		if b := guard.blockedRequests(); len(b) != 0 {
			t.Errorf("pure-loopback page recorded blocked requests: %v", b)
		}
	})
}
