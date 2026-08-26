package ipc

// Test pins for the 2026-08-25 deep-audit fixes (1 P0, 4 P1, 5 P2). Each
// test names the audit finding it covers; the fix sites cite the same date.

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/yingliang-zhang/odo/internal/store"
)

// raceResp ferries one concurrent RPC outcome back to the test goroutine.
type raceResp struct {
	resp Response
	err  error
}

// callRace fires one request on its own connection (the rig.call pattern
// would serialize the race away).
func callRace(rig *testRig, req Request) chan raceResp {
	done := make(chan raceResp, 1)
	go func() {
		resp, err := rig.roundTrip(req)
		done <- raceResp{resp: resp, err: err}
	}()
	return done
}

// --- P0: project skill root symlink escape --------------------------------

// A symlinked .odo/skills (or .odo itself) made every REGULAR file inside an
// external directory scan clean and ride into the prompt — the per-file
// Lstat never saw the directory link. The project scope now resolves
// through guardedBase and degrades to absent; the global scope (dotfiles
// links are legitimate) stays unguarded.
func TestScanSkillsSkipsSymlinkedProjectRoot(t *testing.T) {
	t.Setenv("HOME", t.TempDir()) // no global skills interfere
	writeExternal := func() string {
		external := t.TempDir()
		if err := os.WriteFile(filepath.Join(external, "ext.md"), []byte("---\nname: evilskill\n---\nexternal body\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		return external
	}
	names := func(entries []skillEntry) []string {
		var out []string
		for _, e := range entries {
			out = append(out, e.info.Name)
		}
		return out
	}

	// Case 1: .odo/skills IS the link.
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, ".odo"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(writeExternal(), filepath.Join(root, ".odo", "skills")); err != nil {
		t.Skipf("symlink not supported: %v", err)
	}
	for _, n := range names(scanSkills(root)) {
		if n == "evilskill" {
			t.Errorf("symlinked .odo/skills leaked external skill %q into the scan", n)
		}
	}

	// Case 2: .odo itself IS the link (skills dir real inside the target).
	root2 := t.TempDir()
	external2 := t.TempDir()
	if err := os.MkdirAll(filepath.Join(external2, "skills"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(external2, "skills", "ext.md"), []byte("---\nname: evilskill\n---\nexternal body\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(external2, filepath.Join(root2, ".odo")); err != nil {
		t.Skipf("symlink not supported: %v", err)
	}
	for _, n := range names(scanSkills(root2)) {
		if n == "evilskill" {
			t.Errorf("symlinked .odo leaked external skill %q into the scan", n)
		}
	}

	// Control: a real project skills dir still scans.
	root3 := t.TempDir()
	skillsDir := filepath.Join(root3, ".odo", "skills")
	if err := os.MkdirAll(skillsDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(skillsDir, "ok.md"), []byte("---\nname: okskill\n---\nreal body\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	found := false
	for _, n := range names(scanSkills(root3)) {
		if n == "okskill" {
			found = true
		}
	}
	if !found {
		t.Error("real .odo/skills skill vanished — the guard must only refuse links")
	}
}

// P2 (same scan): one oversized skill file must not OOM the scan (bounded
// read) and must not block smaller matched skills from injecting
// (whole-file skip at scan, continue-not-break at format).
func TestScanSkillsSkipsOversizedFile(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	root := t.TempDir()
	skillsDir := filepath.Join(root, ".odo", "skills")
	if err := os.MkdirAll(skillsDir, 0o755); err != nil {
		t.Fatal(err)
	}
	big := "---\nname: bigskill\n---\n" + strings.Repeat("b ", skillMaxFileBytes) // > cap with frontmatter
	if err := os.WriteFile(filepath.Join(skillsDir, "big.md"), []byte(big), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(skillsDir, "small.md"), []byte("---\nname: smallskill\n---\nsmall body\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	byName := map[string]bool{}
	for _, e := range scanSkills(root) {
		byName[e.info.Name] = true
	}
	if byName["bigskill"] {
		t.Error("oversized skill file scanned — want whole-file skip over the cap")
	}
	if !byName["smallskill"] {
		t.Error("small skill lost to the oversized sibling")
	}
}

func TestFormatSkillsSkipsOversizedKeepsSmaller(t *testing.T) {
	entries := []skillEntry{
		{info: SkillInfo{Name: "big", Path: "big.md"}, body: strings.Repeat("x", 9000)},
		{info: SkillInfo{Name: "small", Path: "small.md"}, body: "tiny procedure"},
	}
	block, receipts := formatSkillsForInjection(entries, skillsInjectionCap)
	if len(receipts) != 1 || receipts[0].path != "small.md" {
		t.Fatalf("receipts = %+v, want exactly the small skill", receipts)
	}
	if strings.Contains(block, "big") && strings.Contains(block, strings.Repeat("x", 64)) {
		t.Errorf("oversized skill body leaked into the injection block:\n%.200s", block)
	}
	if !strings.Contains(block, "tiny procedure") {
		t.Errorf("small skill missing from the injection block:\n%s", block)
	}
}

// delete_skill (GUI path) on a project scope whose .odo/skills chain is
// symlinked must refuse — os.Remove would otherwise unlink an EXTERNAL
// file. update_skill already refused; delete was the hole.
func TestDeleteSkillRefusesSymlinkedSkillsDir(t *testing.T) {
	root := initRepo(t)
	t.Setenv("HOME", t.TempDir())
	t.Setenv("ODO_OMP_WRAPPER", writeStub(t, stubWrapper))
	rig := startRig(t, root)
	defer rig.stop(t)

	external := t.TempDir()
	victim := filepath.Join(external, "victim.md")
	if err := os.WriteFile(victim, []byte("external skill body\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	odoSkills := filepath.Join(root, ".odo", "skills")
	os.RemoveAll(odoSkills)
	if err := os.Symlink(external, odoSkills); err != nil {
		t.Skipf("symlink not supported: %v", err)
	}
	resp := rig.callExpectErr(t, Request{Cmd: CmdDeleteSkill, Name: "victim", Scope: "project"})
	if !strings.Contains(resp.Error, "symlinked component") {
		t.Errorf("delete_skill error = %q, want the symlinked-component refusal", resp.Error)
	}
	if data, err := os.ReadFile(victim); err != nil || string(data) != "external skill body\n" {
		t.Errorf("external victim file = %q, %v — delete escaped the project", data, err)
	}
}

// --- P1: memory single-writer ----------------------------------------------

// Two concurrent applies of the SAME batch: without the memMu in-lock
// re-check both probe "pending" unlocked and both consume — archive,
// reaffirm bumps, and the apply marker double up. Exactly ONE apply row
// may land.
func TestApplyMemorySingleConsumerRace(t *testing.T) {
	root := initRepo(t)
	t.Setenv("HOME", t.TempDir())
	t.Setenv("ODO_OMP_WRAPPER", writeStub(t, stubWrapper))
	rig := startRig(t, root)
	defer rig.stop(t)
	convID := bootstrapConv(t, rig, root)
	rule := "Concurrent applies converge to one."
	seedProposeBatch(t, rig, convID, 1, []MemoryProposal{
		{Target: "memory.md", Rule: rule, Evidence: "main-epoch-1"},
	}, nil)

	c1 := callRace(rig, Request{
		Cmd:            CmdApplyMemory,
		ConversationID: convID,
		Epoch:          1,
		Accepted:       []MemoryAccept{{Target: "memory.md", Index: 0}},
	})
	c2 := callRace(rig, Request{
		Cmd:            CmdApplyMemory,
		ConversationID: convID,
		Epoch:          1,
		Accepted:       []MemoryAccept{{Target: "memory.md", Index: 0}},
	})
	r1, r2 := <-c1, <-c2
	oks := 0
	for _, r := range []raceResp{r1, r2} {
		if r.err != nil {
			t.Fatalf("apply transport: %v", r.err)
		}
		if r.resp.OK {
			oks++
			continue
		}
		if !strings.Contains(r.resp.Error, "already applied") {
			t.Errorf("losing apply error = %q, want the single-consumer refusal", r.resp.Error)
		}
	}
	if oks != 1 {
		t.Fatalf("concurrent applies: ok count = %d, want exactly 1", oks)
	}
	events := rig.call(t, Request{Cmd: CmdPollEvents, ConversationID: convID, AfterSeq: 0}).Events
	if applies := payloadsByAction(t, events, "memory_apply"); len(applies) != 1 {
		t.Fatalf("memory_apply rows = %d, want 1", len(applies))
	}
	if got := strings.Count(readFileStr(t, filepath.Join(root, ".odo", "memory.md")), rule); got != 1 {
		t.Errorf("rule occurrences in memory.md = %d, want 1", got)
	}
}

// Two concurrent pins both survive: the RMW used to read the same old
// content and last-rename-wins dropped one pin.
func TestConcurrentPinsKeepBoth(t *testing.T) {
	root := initRepo(t)
	t.Setenv("HOME", t.TempDir())
	t.Setenv("ODO_OMP_WRAPPER", writeStub(t, stubWrapper))
	rig := startRig(t, root)
	defer rig.stop(t)
	convID := bootstrapConv(t, rig, root)

	alpha := callRace(rig, Request{Cmd: CmdPin, ConversationID: convID, Text: "pin alpha audit"})
	beta := callRace(rig, Request{Cmd: CmdPin, ConversationID: convID, Text: "pin beta audit"})
	if r := <-alpha; r.err != nil || !r.resp.OK {
		t.Fatalf("pin alpha: err=%v resp=%+v", r.err, r.resp)
	}
	if r := <-beta; r.err != nil || !r.resp.OK {
		t.Fatalf("pin beta: err=%v resp=%+v", r.err, r.resp)
	}
	got := readFileStr(t, filepath.Join(root, ".odo", "pins.md"))
	for _, want := range []string{"- pin alpha audit", "- pin beta audit"} {
		if !strings.Contains(got, want) {
			t.Errorf("pins.md = %q, want both pins (%q lost to the RMW race)", got, want)
		}
	}
}

// --- P1: workstream delete guard -------------------------------------------

// A workstream with live conversation work (run / distill / panel / loop /
// scheduled distill) must refuse delete: the next produced diff would
// strand on a soft-deleted workstream the Review inbox (active-only)
// never lists. The store's pending-diff check alone could not see this.
func TestDeleteWorkstreamRefusesActiveWork(t *testing.T) {
	root := initRepo(t)
	t.Setenv("HOME", t.TempDir())
	t.Setenv("ODO_OMP_WRAPPER", writeStub(t, stubWrapper))
	rig := startRig(t, root)
	defer rig.stop(t)
	bootstrapConv(t, rig, root)

	created := rig.call(t, Request{Cmd: CmdCreateWorkstream, ProjectRoot: root, Name: "busy-lane"})
	wsID := created.Workstream.ID
	boot2 := rig.call(t, Request{Cmd: CmdBootstrap, ProjectRoot: root, WorkstreamID: wsID})
	conv2 := boot2.Conversation.ID

	cases := []struct {
		name  string
		arm   func()
		clear func()
		want  string
	}{
		{"agent run", func() { rig.server.byConv[conv2] = "rX"; rig.server.runs["rX"] = &runMeta{} },
			func() { delete(rig.server.byConv, conv2); delete(rig.server.runs, "rX") }, "agent run"},
		{"manual distill", func() { rig.server.distilling[conv2] = struct{}{} },
			func() { delete(rig.server.distilling, conv2) }, "distill"},
		{"auto distill", func() { rig.server.distillKind[conv2] = "idle" },
			func() { delete(rig.server.distillKind, conv2) }, "distill"},
		{"slash consult", func() { rig.server.slashing[conv2] = 1 },
			func() { delete(rig.server.slashing, conv2) }, "slash consult"},
		{"panel consult", func() { rig.server.panelProg[conv2] = []*PanelProgress{{Total: 2}} },
			func() { delete(rig.server.panelProg, conv2) }, "panel consult"},
		{"loop", func() { rig.server.loops[conv2] = struct{}{} },
			func() { delete(rig.server.loops, conv2) }, "loop"},
		{"scheduled distill", func() { rig.server.autoPending[conv2] = &autoPendingEntry{} },
			func() { delete(rig.server.autoPending, conv2) }, "scheduled distill"},
	}
	for _, tc := range cases {
		rig.server.mu.Lock()
		tc.arm()
		rig.server.mu.Unlock()
		resp := rig.callExpectErr(t, Request{Cmd: CmdDeleteWorkstream, WorkstreamID: wsID})
		if !strings.Contains(resp.Error, tc.want) {
			t.Errorf("%s: delete error = %q, want the %q refusal", tc.name, resp.Error, tc.want)
		}
		rig.server.mu.Lock()
		tc.clear()
		rig.server.mu.Unlock()
		if got, err := rig.server.store.GetWorkstream(t.Context(), wsID); err != nil || got.Status != "active" {
			t.Fatalf("%s: workstream vanished under the refusal: %v %+v", tc.name, err, got)
		}
	}

	// Nothing in flight: delete proceeds and the row soft-deletes.
	del := rig.call(t, Request{Cmd: CmdDeleteWorkstream, WorkstreamID: wsID})
	if !del.OK {
		t.Fatalf("quiet delete: %+v", del)
	}
	if got, err := rig.server.store.GetWorkstream(t.Context(), wsID); err != nil || got.Status == "active" {
		t.Errorf("after quiet delete: %+v %v, want soft-deleted", got, err)
	}
}

// --- P1: /loop tasks file containment --------------------------------------

func TestReadLoopTaskFileContainment(t *testing.T) {
	root := t.TempDir()
	s := &Server{projectRoot: root}

	// A checked-in symlink tasks file must not read outside the project
	// (the old bare os.ReadFile followed it to ~/.ssh/config's cousins).
	secret := filepath.Join(t.TempDir(), "secret.txt")
	if err := os.WriteFile(secret, []byte("ssh-key-bytes"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(secret, filepath.Join(root, "tasks.md")); err != nil {
		t.Skipf("symlink not supported: %v", err)
	}
	if _, err := s.readLoopTaskFile("tasks.md"); err == nil || !strings.Contains(err.Error(), "symlink escapes") {
		t.Errorf("symlinked tasks file: err = %v, want the symlink-escape refusal", err)
	}

	// Textual escape still refused without touching the filesystem.
	if _, err := s.readLoopTaskFile("../outside.txt"); err == nil || !strings.Contains(err.Error(), "escapes") {
		t.Errorf("../ escape: err = %v, want the textual refusal", err)
	}

	// Real project file still reads.
	if err := os.WriteFile(filepath.Join(root, "real.md"), []byte("1. do a thing\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if got, err := s.readLoopTaskFile("real.md"); err != nil || got != "1. do a thing\n" {
		t.Errorf("real tasks file = %q, %v", got, err)
	}

	// Over-cap file is refused (pre-read stat) without loading the bytes.
	fat := filepath.Join(root, "fat.md")
	if err := os.WriteFile(fat, []byte(strings.Repeat("x", settleDiffCapBytes+1)), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := s.readLoopTaskFile("fat.md"); err == nil || !strings.Contains(err.Error(), "over the") {
		t.Errorf("over-cap tasks file: err = %v, want the cap refusal", err)
	}
}

// A file measured UNDER the cap by the stat pre-check but grown past it
// before the read lands (2026-08-26 audit P2 TOCTOU window) must hit the
// same "over the cap" refusal — via the capped read, with the grown
// bytes never allocated. The cappedReadPreOpenHook seam fires between
// the loop's stat and the read, deterministically (no sleeps).
func TestReadLoopTaskFileGrowthPastCap(t *testing.T) {
	root := t.TempDir()
	s := &Server{projectRoot: root}
	tasks := filepath.Join(root, "tasks.md")
	if err := os.WriteFile(tasks, []byte("1. small\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	armed := true
	cappedReadPreOpenHook = func(path string) {
		if !armed || path != tasks {
			return
		}
		armed = false // one-shot: fires exactly inside this read's window
		if err := os.WriteFile(tasks, []byte(strings.Repeat("x", 2*settleDiffCapBytes)), 0o644); err != nil {
			t.Errorf("grow fixture: %v", err)
		}
	}
	defer func() { cappedReadPreOpenHook = nil }()

	if _, err := s.readLoopTaskFile("tasks.md"); err == nil || !strings.Contains(err.Error(), "over the") {
		t.Errorf("grew-past-stat tasks file: err = %v, want the cap refusal identical to the stat gate", err)
	}
	if armed {
		t.Error("seam never fired — the drill tested nothing")
	}
}

// --- P2: loop spill integrity ----------------------------------------------

// A spilled artifact (findings union, design lock) is journaled as
// path+sha16; recovery must re-check BOTH the containment and the hash —
// replaced bytes previously steered the next round as if attested.
func TestLoopArtifactBodyIntegrity(t *testing.T) {
	root := t.TempDir()
	s := &Server{projectRoot: root}

	body := `["finding-alpha","finding-beta"]`
	rel, sha, err := s.loopSpillBody(7, "findings-1.json", body)
	if err != nil {
		t.Fatalf("spill: %v", err)
	}
	payload := func(p, h string) []byte {
		return []byte(mustJSON(map[string]interface{}{"findings_path": p, "findings_sha16": h}))
	}

	// Intact: exact bytes back.
	if got := s.loopArtifactBody(payload(rel, sha), "findings_path"); string(got) != body {
		t.Errorf("intact spill = %q, want %q", got, body)
	}
	// Tampered after journaling: refused, not read as the attested union.
	if err := os.WriteFile(filepath.Join(root, rel), []byte(`["attacker-finding"]`), 0o644); err != nil {
		t.Fatal(err)
	}
	if got := s.loopArtifactBody(payload(rel, sha), "findings_path"); got != nil {
		t.Errorf("tampered spill = %q, want nil refusal", got)
	}
	// Path escaping the loop tree: refused even with the hash "matching".
	outside := strings.Repeat("x", 32)
	escapeRel := "../" + strings.TrimPrefix(rel, ".odo/") // resolves next to .odo
	if got := s.loopArtifactBody(payload(escapeRel, sha16([]byte(outside))), "findings_path"); got != nil {
		t.Errorf("escaping spill path = %q, want nil refusal", got)
	}
	// The daemon-owned loop dir replaced by a symlink: refused as a whole.
	loopDir := filepath.Join(root, ".odo", "loop")
	os.RemoveAll(loopDir)
	extDir := t.TempDir()
	if err := os.Symlink(extDir, loopDir); err != nil {
		t.Skipf("symlink not supported: %v", err)
	}
	if got := s.loopArtifactBody(payload(rel, sha), "findings_path"); got != nil {
		t.Errorf("symlinked loop dir spill = %q, want nil refusal", got)
	}
}

// --- P2: concurrent /panel tally -------------------------------------------

// Two overlapping consults share no tally state: the first to finish
// removes ONLY its batch — the survivor still reads {done:0,total:N} with
// its own legs, never Done > Total with a mixed list (the shared-tally
// corruption).
func TestPanelProgressConcurrentBatches(t *testing.T) {
	root := initRepo(t)
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("ODO_OMP_WRAPPER", writeStub(t, stubWrapper))
	seedSlashFixture(t, root, home)
	writePrefs(t, home, "review: pm1@test, pm2@test\n")

	releaseA := make(chan struct{})
	releaseB := make(chan struct{})
	var calls atomic.Int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		bodyBytes, _ := io.ReadAll(r.Body)
		bodyText := string(bodyBytes)
		calls.Add(1)
		switch {
		case strings.Contains(bodyText, "AAAA"):
			<-releaseA
		case strings.Contains(bodyText, "BBBB"):
			<-releaseB
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]interface{}{
			"content":     []map[string]string{{"type": "text", "text": "panel-answer"}},
			"stop_reason": "end_turn",
		})
	}))
	t.Cleanup(srv.Close)
	t.Setenv("MOA_BASE_URL", srv.URL)
	t.Setenv("SUDO_CODING_KEY", "test-key")

	rig := startRig(t, root)
	defer rig.stop(t)
	convID := bootstrapConv(t, rig, root)

	panelCall := func(text string) chan Response {
		done := make(chan Response, 1)
		go func() {
			conn, err := net.Dial("unix", rig.sock)
			if err != nil {
				done <- Response{Error: err.Error()}
				return
			}
			defer conn.Close()
			if err := json.NewEncoder(conn).Encode(Request{Cmd: CmdSendMessage, ConversationID: convID, Text: text}); err != nil {
				done <- Response{Error: err.Error()}
				return
			}
			var resp Response
			if err := json.NewDecoder(conn).Decode(&resp); err != nil {
				done <- Response{Error: err.Error()}
				return
			}
			done <- resp
		}()
		return done
	}
	doneA := panelCall("/panel probe AAAA")
	doneB := panelCall("/panel probe BBBB")

	// All four legs (2 panels x 2 models) gate parked on their channels.
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) && calls.Load() < 4 {
		time.Sleep(20 * time.Millisecond)
	}
	if calls.Load() < 4 {
		t.Fatalf("moa calls = %d, want 4 legs parked", calls.Load())
	}
	// Mid-flight: merged tally sums both batches.
	var merged *PanelProgress
	for time.Now().Before(deadline) {
		if resp := rig.call(t, Request{Cmd: CmdPollEvents, ConversationID: convID}); resp.PanelProgress != nil && resp.PanelProgress.Total == 4 {
			merged = resp.PanelProgress
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if merged == nil || merged.Done != 0 || len(merged.Legs) != 4 {
		t.Fatalf("mid-flight merged tally = %+v, want {done:0 total:4 legs:4}", merged)
	}

	// A 完成（B 仍在停）。A 的批组必须独立消失：合计回到 B 的 {0,2}，
	// 且两条腿均未应答 —— 共享实现里这一步会露出 Done(2) >= Total(2)
	// 并把 A 的腿混进 B 的列表。
	close(releaseA)
	select {
	case resp := <-doneA:
		if !resp.OK {
			t.Fatalf("panel A: %s", resp.Error)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("panel A never returned after release")
	}
	var survivor *PanelProgress
	for time.Now().Before(deadline) {
		if resp := rig.call(t, Request{Cmd: CmdPollEvents, ConversationID: convID}); resp.PanelProgress != nil && resp.PanelProgress.Total == 2 {
			survivor = resp.PanelProgress
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if survivor == nil {
		t.Fatal("surviving panel's tally vanished when its sibling finished")
	}
	if survivor.Done != 0 {
		t.Errorf("surviving tally Done = %d, want 0 (finished sibling took its count along)", survivor.Done)
	}
	if len(survivor.Legs) != 2 {
		t.Errorf("surviving legs = %d, want the sibling's batch removed entirely", len(survivor.Legs))
	}
	for _, leg := range survivor.Legs {
		if leg.Done {
			t.Errorf("surviving leg %s marked done though B is still parked", leg.Model)
		}
	}

	close(releaseB)
	select {
	case resp := <-doneB:
		if !resp.OK {
			t.Fatalf("panel B: %s", resp.Error)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("panel B never returned after release")
	}
	if resp := rig.call(t, Request{Cmd: CmdPollEvents, ConversationID: convID}); resp.PanelProgress != nil {
		t.Errorf("panel_progress after both consults = %+v, want absent", resp.PanelProgress)
	}
}

// --- Review follow-up: crash-window recovery (P1, marker/receipt-first) ----

// TestApplyMemoryCrashWindowHeals drills the marker-first apply protocol end
// to end: the consumption marker journals BEFORE the file writes, so the
// crash leaves the batch consumed (never applied twice) with the FILES
// lagging; the boot replayer then restores the layers from the marker's
// recovery block — exactly, once, and a re-apply onto the healed file is
// refused (the reviewer's double-reaffirm scenario).
func TestApplyMemoryCrashWindowHeals(t *testing.T) {
	root := initRepo(t)
	t.Setenv("HOME", t.TempDir())
	t.Setenv("ODO_OMP_WRAPPER", writeStub(t, stubWrapper))
	rig := startRig(t, root)
	defer rig.stopOnce(t)
	convID := bootstrapConv(t, rig, root)

	// Existing rule for the reaffirm half; the batch reaffirms it and adds
	// one rule at epoch 2.
	memPath := filepath.Join(root, ".odo", "memory.md")
	if err := os.MkdirAll(filepath.Dir(memPath), 0o755); err != nil {
		t.Fatal(err)
	}
	const existing = "- Keep the lane discipline. — cites: main-epoch-1; reaffirmed: 1\n"
	if err := os.WriteFile(memPath, []byte(existing), 0o644); err != nil {
		t.Fatal(err)
	}
	const newRule = "Crash windows close marker-first."
	seedProposeBatch(t, rig, convID, 2, []MemoryProposal{
		{Target: "memory.md", Rule: newRule, Evidence: "main-epoch-2"},
	}, []string{"Keep the lane discipline."})
	applyReq := Request{Cmd: CmdApplyMemory, ConversationID: convID, Epoch: 2,
		Accepted: []MemoryAccept{{Target: "memory.md", Index: 0}}}

	// Crash drill: marker journaled, no file writes, handler returns dead.
	rig.server.failApplyAfterMarker = errors.New("crash drill")
	if resp := rig.callExpectErr(t, applyReq); !strings.Contains(resp.Error, "crash drill") {
		t.Fatalf("crash-drill apply error = %q, want the failpoint", resp.Error)
	}
	if got := readFileStr(t, memPath); got != existing {
		t.Fatalf("memory.md after crash = %q, want untouched %q — the file must lag the journal now", got, existing)
	}
	pre := allEvents(t, rig, convID)
	if applies := payloadsByAction(t, pre, "memory_apply"); len(applies) != 1 {
		t.Fatalf("memory_apply rows = %d, want exactly the crashed marker", len(applies))
	}
	if cur := findPendingBatch(pre); !cur.exists || !cur.consumed || cur.epoch != 2 {
		t.Fatalf("pending batch after crash = %+v, want epoch 2 CONSUMED (journal-authoritative)", cur)
	}

	// Recovery (the restart): a fresh daemon folds the project journal at
	// boot, finds the stranded layers at before_sha, and replays the
	// recorded bodies (2026-08-26 doctrine: the replayer owns this now,
	// not a bootstrap-time lane scan).
	rig = restartRig(t, rig)
	defer rig.stopOnce(t)

	const wantOld = "- Keep the lane discipline. — cites: main-epoch-1; reaffirmed: 2"
	const wantNew = "- " + newRule + " — cites: main-epoch-2; reaffirmed: 2"
	got := readFileStr(t, memPath)
	for _, want := range []string{wantOld, wantNew} {
		if c := strings.Count(got, want); c != 1 {
			t.Errorf("memory.md = %q, want %q exactly once (healed, not lost, not doubled)", got, want)
		}
	}
	post := allEvents(t, rig, convID)
	if recovers := memoryUpdatesByCause(t, post, "recover"); len(recovers) != 1 {
		t.Errorf("recover receipts = %d, want exactly 1 for the healed crash", len(recovers))
	}

	// Idempotent + single-consumption: a second boot pass replays nothing
	// more, and the reviewer's re-apply-onto-changed-file stays refused.
	rig.server.replayMemoryJournal(context.Background())

	if resp := rig.callExpectErr(t, applyReq); !strings.Contains(resp.Error, "already applied") {
		t.Errorf("re-apply after heal error = %q, want the consumed refusal", resp.Error)
	}
	if got := readFileStr(t, memPath); strings.Count(got, wantOld) != 1 || strings.Count(got, wantNew) != 1 {
		t.Errorf("memory.md after refusal = %q — the refusal must never touch the healed file", got)
	}
	if recovers := memoryUpdatesByCause(t, allEvents(t, rig, convID), "recover"); len(recovers) != 1 {
		t.Errorf("recover receipts after the second boot pass = %d, want 1 (replay is a no-op on landed layers)", len(recovers))
	}
}

// TestPinCrashWindowHeals drills the journal-first pin protocol: the
// receipt (with before/after sha + body) lands BEFORE the file write, so
// a crashed pin is restored from the journal — and the NEXT pin's
// read-modify-write basis carries the recovered line, never dropping it.
func TestPinCrashWindowHeals(t *testing.T) {
	root := initRepo(t)
	t.Setenv("HOME", t.TempDir())
	t.Setenv("ODO_OMP_WRAPPER", writeStub(t, stubWrapper))
	rig := startRig(t, root)
	defer rig.stopOnce(t)
	convID := bootstrapConv(t, rig, root)
	pinsFile := filepath.Join(root, ".odo", "pins.md")

	rig.server.failPinAfterReceipt = errors.New("crash drill")
	if resp := rig.callExpectErr(t, Request{Cmd: CmdPin, ConversationID: convID, Text: "pin alpha crash"}); !strings.Contains(resp.Error, "crash drill") {
		t.Fatalf("crash-drill pin error = %q, want the failpoint", resp.Error)
	}
	if data, err := os.ReadFile(pinsFile); err == nil && strings.Contains(string(data), "pin alpha crash") {
		t.Fatalf("pins.md = %q — the crashed pin must lag the receipt (journal-first)", data)
	}
	pinReceipts := memoryUpdatesByCause(t, allEvents(t, rig, convID), "pin")
	if len(pinReceipts) != 1 || pinReceipts[0]["after_sha"] == nil || pinReceipts[0]["body"] == nil {
		t.Fatalf("pin receipts = %+v, want the crashed pin journaled with its recovery fields", pinReceipts)
	}

	// Recovery via the replayer: a fresh daemon restores the receipted
	// body at boot (2026-08-26 doctrine).
	rig = restartRig(t, rig)
	defer rig.stopOnce(t)
	if got := readFileStr(t, pinsFile); got != "- pin alpha crash\n" {
		t.Fatalf("pins.md after boot replay = %q, want exactly the recovered pin", got)
	}
	// The next pin's RMW basis includes the recovered line — both survive.
	rig.call(t, Request{Cmd: CmdPin, ConversationID: convID, Text: "pin beta after heal"})
	got := readFileStr(t, pinsFile)
	for _, want := range []string{"- pin alpha crash", "- pin beta after heal"} {
		if c := strings.Count(got, want); c != 1 {
			t.Errorf("pins.md = %q, want %q exactly once (recovered + appended)", got, want)
		}
	}
	if recovers := memoryUpdatesByCause(t, allEvents(t, rig, convID), "recover"); len(recovers) != 1 {
		t.Errorf("pin recover receipts = %d, want exactly 1", len(recovers))
	}
}

// --- Review follow-up: delete-gate atomicity (P1) ---------------------------

// TestDeleteWorkstreamBarsRacingStarts pins the atomic start bar: once the
// idle proof passed and the mid-delete flag is up, EVERY liveness-bearing
// start refuses — the previous diff released s.mu before the SQL delete,
// and a start sliding into that window stranded its diff on a lane the
// Review inbox had just stopped listing. The flag also single-flights
// concurrent deletes, and post-commit starts refuse on the deleted status.
func TestDeleteWorkstreamBarsRacingStarts(t *testing.T) {
	root := initRepo(t)
	t.Setenv("HOME", t.TempDir())
	t.Setenv("ODO_OMP_WRAPPER", writeStub(t, stubWrapper))
	rig := startRig(t, root)
	defer rig.stop(t)
	bootstrapConv(t, rig, root)

	created := rig.call(t, Request{Cmd: CmdCreateWorkstream, ProjectRoot: root, Name: "doomed"})
	wsID := created.Workstream.ID
	boot := rig.call(t, Request{Cmd: CmdBootstrap, ProjectRoot: root, WorkstreamID: wsID})
	conv2 := boot.Conversation.ID

	// Stage the exact mid-delete state: idle proof passed, flag up, the
	// SQL commit still pending.
	rig.server.mu.Lock()
	rig.server.deletingWs[wsID] = struct{}{}
	rig.server.mu.Unlock()

	for _, req := range []Request{
		{Cmd: CmdSendMessage, ConversationID: conv2, Text: "start racing work"},
		{Cmd: CmdDistill, ConversationID: conv2},
	} {
		if resp := rig.callExpectErr(t, req); !strings.Contains(resp.Error, "being deleted") {
			t.Errorf("%s during mid-delete: error = %q, want the being-deleted refusal", req.Cmd, resp.Error)
		}
	}
	// The barred starts registered NOTHING — the delete's busy check must
	// still see the quiet lane it proved.
	rig.server.mu.Lock()
	_, runLive := rig.server.byConv[conv2]
	_, disLive := rig.server.distilling[conv2]
	rig.server.mu.Unlock()
	if runLive || disLive {
		t.Fatalf("barred starts leaked liveness (run=%v distill=%v)", runLive, disLive)
	}
	// Concurrent deletes single-flight on the flag.
	if resp := rig.callExpectErr(t, Request{Cmd: CmdDeleteWorkstream, WorkstreamID: wsID}); !strings.Contains(resp.Error, "already in flight") {
		t.Errorf("second delete error = %q, want the in-flight refusal", resp.Error)
	}

	// Commit window over (flag cleared): the quiet delete proceeds, and
	// post-commit starts refuse on the deleted STATUS — bootstrap included,
	// so no fresh conversation sprouts on the dead lane either.
	rig.server.mu.Lock()
	delete(rig.server.deletingWs, wsID)
	rig.server.mu.Unlock()
	if del := rig.call(t, Request{Cmd: CmdDeleteWorkstream, WorkstreamID: wsID}); !del.OK {
		t.Fatalf("post-flag delete: %+v", del)
	}
	if resp := rig.callExpectErr(t, Request{Cmd: CmdSendMessage, ConversationID: conv2, Text: "after the fact"}); !strings.Contains(resp.Error, "deleted") {
		t.Errorf("send to deleted lane: error = %q, want the deleted refusal", resp.Error)
	}
	if resp := rig.callExpectErr(t, Request{Cmd: CmdBootstrap, ProjectRoot: root, WorkstreamID: wsID}); !strings.Contains(resp.Error, "deleted") {
		t.Errorf("bootstrap of deleted lane: error = %q, want the deleted refusal", resp.Error)
	}
}

// TestConversationlessDeleteBootstrapRace drills the REAL
// guard-passed-but-create-pending window the delete atomicity fix closes
// (2026-08-25 panel direction): hand-staging deletingWs can never reach
// it — a bootstrap whose create carries no critical section would PASS a
// staged-flag test — so the bootstrapPreCreateGateForTest seam parks a
// REAL bootstrap between its resolve reads and its guarded create, a
// REAL delete runs start to finish inside that window, and the released
// bootstrap must refuse on the deleted status with SQL showing NO active
// conversation stranded on the lane.
func TestConversationlessDeleteBootstrapRace(t *testing.T) {
	root := initRepo(t)
	t.Setenv("HOME", t.TempDir())
	t.Setenv("ODO_OMP_WRAPPER", writeStub(t, stubWrapper))
	rig := startRig(t, root)
	defer rig.stop(t)
	bootstrapConv(t, rig, root) // registers the project resolveProject needs

	created := rig.call(t, Request{Cmd: CmdCreateWorkstream, ProjectRoot: root, Name: "doomed"})
	wsID := created.Workstream.ID

	gate := make(chan struct{})
	rig.server.bootstrapPreCreateGateForTest = gate
	done := callRace(rig, Request{Cmd: CmdBootstrap, ProjectRoot: root, WorkstreamID: wsID})
	// The bootstrap must BE parked in the window before the delete
	// starts, or the drill tests nothing.
	select {
	case <-gate:
	case <-time.After(10 * time.Second):
		t.Fatal("bootstrap never reached the pre-create gate")
	}
	defer func() {
		// Free a still-parked bootstrap on any failure exit so teardown
		// never waits on it.
		select {
		case gate <- struct{}{}:
		default:
		}
	}()

	// The ENTIRE delete (flag raise → SQL commit → flag clear) inside
	// the window the parked bootstrap is holding open.
	if del := rig.call(t, Request{Cmd: CmdDeleteWorkstream, ProjectRoot: root, WorkstreamID: wsID}); !del.OK {
		t.Fatalf("delete during parked bootstrap: %+v", del)
	}

	gate <- struct{}{} // release the bootstrap into its critical section
	select {
	case out := <-done:
		if out.err != nil {
			t.Fatalf("bootstrap transport: %v", out.err)
		}
		if out.resp.OK {
			t.Errorf("bootstrap succeeded on a deleted lane: %+v", out.resp)
		} else if !strings.Contains(out.resp.Error, "deleted") {
			t.Errorf("bootstrap error = %q, want the being-deleted/deleted refusal", out.resp.Error)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("bootstrap never returned after the gate released")
	}

	// SQL truth: no active conversation may exist under the deleted lane.
	if _, err := rig.store.GetActiveConversation(context.Background(), wsID); err == nil {
		t.Error("active conversation stranded on the deleted workstream")
	}
	// And every later bootstrap refuses at the door (deleted status).
	if resp := rig.callExpectErr(t, Request{Cmd: CmdBootstrap, ProjectRoot: root, WorkstreamID: wsID}); !strings.Contains(resp.Error, "deleted") {
		t.Errorf("post-delete bootstrap: error = %q, want the deleted refusal", resp.Error)
	}
}

// TestDeleteRetriesWhenBootstrapCommitsFirst drills the OTHER half of
// the ordering argument (2026-08-25 panel follow-up): the bootstrap
// wins the race — its conversation commits between the delete's
// idle-proof read (taken OUTSIDE s.mu, hence stale) and the delete's
// flag raise. The flag can never refuse a create that already
// committed, so the delete's commit-time re-read must make the DELETE
// lose: refuse mid-delete, leave the lane active, keep the
// bootstrap's conversation, and behave byte-identical to baseline on
// a clean retry. The deleteIdleProofGateForTest seam parks a REAL
// delete inside that exact sliver; omitting the re-read fails this
// test on its first assertion.
func TestDeleteRetriesWhenBootstrapCommitsFirst(t *testing.T) {
	root := initRepo(t)
	t.Setenv("HOME", t.TempDir())
	t.Setenv("ODO_OMP_WRAPPER", writeStub(t, stubWrapper))
	rig := startRig(t, root)
	defer rig.stop(t)
	bootstrapConv(t, rig, root) // registers the project resolveProject needs

	created := rig.call(t, Request{Cmd: CmdCreateWorkstream, ProjectRoot: root, Name: "raced"})
	wsID := created.Workstream.ID

	gate := make(chan struct{})
	rig.server.deleteIdleProofGateForTest = gate
	done := callRace(rig, Request{Cmd: CmdDeleteWorkstream, ProjectRoot: root, WorkstreamID: wsID})
	// The delete's idle-proof read must be COMPLETE (stale: no
	// conversation) before the bootstrap commits, or the drill tests
	// nothing.
	select {
	case <-gate:
	case <-time.After(10 * time.Second):
		t.Fatal("delete never reached the idle-proof gate")
	}
	defer func() {
		// Free a still-parked delete on any failure exit so teardown
		// never waits on it.
		select {
		case gate <- struct{}{}:
		default:
		}
	}()

	// The bootstrap wins: its guarded create commits while the delete
	// is parked between its stale read and its flag raise.
	if boot := rig.call(t, Request{Cmd: CmdBootstrap, ProjectRoot: root, WorkstreamID: wsID}); !boot.OK {
		t.Fatalf("bootstrap: %+v", boot)
	}

	gate <- struct{}{} // release the delete into its flag raise + re-read
	select {
	case out := <-done:
		if out.err != nil {
			t.Fatalf("delete transport: %v", out.err)
		}
		if out.resp.OK {
			t.Errorf("delete succeeded over a conversation committed mid-delete: %+v", out.resp)
		} else if !strings.Contains(out.resp.Error, "mid-delete") {
			t.Errorf("delete error = %q, want the mid-delete retry refusal", out.resp.Error)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("delete never returned after the gate released")
	}
	// One-shot seam: disarm before the parity retry below, or its
	// delete re-parks at the gate nobody is left to release.
	rig.server.deleteIdleProofGateForTest = nil

	// The delete lost, so the lane must be fully live (SQL truth):
	// active status, and the bootstrap's conversation present on it.
	ws, err := rig.store.GetWorkstream(context.Background(), wsID)
	if err != nil {
		t.Fatalf("GetWorkstream: %v", err)
	}
	if ws.Status != store.WorkstreamActive {
		t.Errorf("workstream status = %q, want active — the losing delete still committed", ws.Status)
	}
	if _, err := rig.store.GetActiveConversation(context.Background(), wsID); err != nil {
		t.Errorf("bootstrap's conversation missing after winning the race: %v", err)
	}

	// Parity proof: the mid-delete refusal is race-scoped, not a
	// behavior flip — a clean retry deletes the idle
	// conversation-bearing lane exactly as before the fix.
	if retry := rig.call(t, Request{Cmd: CmdDeleteWorkstream, ProjectRoot: root, WorkstreamID: wsID}); !retry.OK {
		t.Errorf("clean retry delete: %+v", retry)
	}
}
