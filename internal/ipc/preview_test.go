package ipc

// /preview tests: the loopback allowlist (pre-journal refusal — nothing
// spawns for external hosts), the per-shot capture receipt (user_message
// image_sha16/image_bytes of exactly the wire bytes; preview_captured's
// full sha256 + wait_ms), the default UI-reviewer prompt, failure-class
// chat errors, and the hermes-Node npx preference. The playwright child is
// mocked via a PATH-installed npx shell script (the package's standard
// exec-handler test pattern — cf. omp_usage_test.go); no real browser
// launches in tests.

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
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
// (MOCK_NPX_LOG, MOCK_PNG_FIXTURE) are visible to it.
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
const mockNpxSuccess = `#!/bin/sh
echo "path-npx:$*" >> "$MOCK_NPX_LOG"
last=""
for a in "$@"; do last="$a"; done
cp "$MOCK_PNG_FIXTURE" "$last"
`

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

	sent := rig.call(t, Request{Cmd: CmdSendMessage, ConversationID: convID, Text: "/preview http://localhost:1420 check the header layout"})
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
		URL    string `json:"url"`
		Bytes  int    `json:"bytes"`
		Sha256 string `json:"sha256"`
		WaitMs int64  `json:"wait_ms"`
	}
	if err := json.Unmarshal(capEv.Payload, &cp); err != nil {
		t.Fatalf("preview_captured payload: %v", err)
	}
	sum := sha256.Sum256(previewFixturePNG)
	if cp.URL != "http://localhost:1420" || cp.Bytes != len(previewFixturePNG) || cp.Sha256 != hex.EncodeToString(sum[:]) || cp.WaitMs < 0 {
		t.Errorf("preview_captured = %+v, want url=http://localhost:1420 bytes=%d sha256=%s wait_ms>=0",
			cp, len(previewFixturePNG), hex.EncodeToString(sum[:]))
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

	rig.call(t, Request{Cmd: CmdSendMessage, ConversationID: convID, Text: "/preview http://127.0.0.1:1420"})
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

// TestPreviewFailureClasses: playwright child failures journal the attempt
// (user_message WITHOUT attachment receipt — no bytes, no claim) and then
// refuse with a paired agent_error carrying the readable class text, never
// calling the gateway.
func TestPreviewFailureClasses(t *testing.T) {
	rec := &previewMoaRecorder{}
	rig, convID := seedPreviewRig(t, rec)

	for _, tc := range []struct{ name, script, wantErr string }{
		{"chromium missing (Executable)", `#!/bin/sh
echo "browserType.launch: Executable doesn't exist at /x/chrome" >&2
exit 1
`, "npx playwright install chromium"},
		{"chromium missing (browserType)", `#!/bin/sh
echo "browserType.launch: Target closed" >&2
exit 1
`, "npx playwright install chromium"},
		{"connection refused", `#!/bin/sh
echo "page.goto: net::ERR_CONNECTION_REFUSED at http://localhost:1420/" >&2
exit 1
`, "connection refused"},
		{"generic failure", `#!/bin/sh
echo "some playwright explosion" >&2
exit 1
`, "preview screenshot failed"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			installMockNpx(t, tc.script)
			resp := rig.callExpectErr(t, Request{Cmd: CmdSendMessage, ConversationID: convID, Text: "/preview http://localhost:1420"})
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
	if userMsgs != 4 || agentErrs != 4 {
		t.Errorf("journaled %d user_message + %d agent_error, want 4 each (attempt + refusal pair)", userMsgs, agentErrs)
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

	resp := rig.callExpectErr(t, Request{Cmd: CmdSendMessage, ConversationID: convID, Text: "/preview http://localhost:1420"})
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
cp "$MOCK_PNG_FIXTURE" "$last"
`
	if err := os.WriteFile(filepath.Join(hermes, "npx"), []byte(hermesScript), 0o755); err != nil {
		t.Fatal(err)
	}
	installMockNpx(t, mockNpxSuccess) // PATH fallback — must NOT run

	rig.call(t, Request{Cmd: CmdSendMessage, ConversationID: convID, Text: "/preview http://localhost:1420"})
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

// TestPreviewShotReceiptFields is the unit-level guard of the receipt
// naming the journalled wire-load: sha16 vs full-sha256 of the same bytes.
func TestPreviewShotReceiptFields(t *testing.T) {
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
sleep 30 &
echo $! > "$MOCK_CHILD_PID"
exec sleep 30
`)
	ctx := context.Background()
	_, err := runPreviewScreenshot(ctx, "http://localhost:1420", filepath.Join(t.TempDir(), "out.png"))
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
