package ipc

// M19 (/loop) tests — the design lock's test plan (docs/design/
// loop-design-lock.md). Rig: a multiplexed moa stub (auditor / reviewer /
// design-leg / consolidator legs split by system prompt), settleRigRepo's
// worktree fixture shape with an env/file-controllable verify gate, and a
// file-scripted wrapper producing scripted diffs per run.

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/yingliang-zhang/odo/internal/store"
)

// --- multiplexed leg stub --------------------------------------------------------

// loopMux answers one moa request by leg class (system-prompt sniffed).
// n is the per-class call index (1-based).
type loopMux func(kind string, n int, model string) (status int, text string, outputTokens int)

// startLoopMuxStub installs the multiplexed gateway stub.
func startLoopMuxStub(t *testing.T, mux loopMux) {
	t.Helper()
	var mu sync.Mutex
	counts := map[string]int{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Model  string `json:"model"`
			System string `json:"system"`
		}
		_ = json.NewDecoder(r.Body).Decode(&req)
		kind := "review"
		switch {
		case strings.Contains(req.System, "design consolidator"):
			kind = "consolidator"
		case strings.Contains(req.System, "expert design reviewer"):
			kind = "design"
		case strings.Contains(req.System, "expert code auditor"):
			kind = "audit"
		}
		mu.Lock()
		counts[kind]++
		n := counts[kind]
		mu.Unlock()
		status, text, outTok := 200, "", 0
		if mux != nil {
			status, text, outTok = mux(kind, n, req.Model)
		}
		if status != http.StatusOK {
			w.WriteHeader(status)
			_ = json.NewEncoder(w).Encode(map[string]string{"error": "gateway boom"})
			return
		}
		stop := "end_turn"
		if strings.Contains(text, "\x00TRUNCATED\x00") {
			stop, text = "max_tokens", `{"truncated":[`
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]interface{}{
			"content":     []map[string]string{{"type": "text", "text": text}},
			"stop_reason": stop,
			"usage":       map[string]int{"output_tokens": outTok},
		})
	}))
	t.Cleanup(srv.Close)
	t.Setenv("MOA_BASE_URL", srv.URL)
	t.Setenv("SUDO_CODING_KEY", "test-key")
}

// auditFindings renders a leg answer with a fenced findings block.
func auditFindings(rows ...string) string {
	return "Findings:\n```findings\n" + strings.Join(rows, "\n") + "\n```\n"
}

// auditClean renders a leg answer with an empty findings block.
const auditClean = "No defects.\n```findings\n```\n"

// reviewVerdictText renders a panel leg verdict.
func reviewVerdictText(v string) string { return "VERDICT: " + v + "\n" }

// --- rig fixtures ------------------------------------------------------------------

// loopRigRepo builds the repo with the file-controlled verify gate: the
// committed .loop-verify-exit decides the gate's exit code in every
// worktree (the diff under review carries its own value along).
func loopRigRepo(t *testing.T) string {
	t.Helper()
	root := initRepo(t)
	if err := os.WriteFile(filepath.Join(root, verifyCmdFile),
		[]byte("echo PASS\nexit $(cat .loop-verify-exit 2>/dev/null || echo 0)\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, ".loop-verify-exit"), []byte("0\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(root, "src"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "src", "a.go"), []byte("package src\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	gitIn(t, root, "add", ".")
	gitIn(t, root, "commit", "-m", "loop fixtures")
	return root
}

// loopWrapper is the file-scripted wrapper: each run reads $LOOP_STUB_CTRL
// for its action name and applies it in the worktree. Actions append Go
// comments (gate-clean content) or write the verify-control file.
//
//	fix1/fix2/impl1/impl2 — edit src/a.go (test evidence via echo PASS)
//	vfail — set .loop-verify-exit so the diff's own verify fails
//	supply — add package.json (supply-chain gate)
//	protect — add wiki/ content (protected-path gate)
//	slow — sleep 5s (a mid-flight run a human send can interrupt)
//	none — produce no diff
const loopWrapper = `#!/bin/sh
output_file="$3"
ctrl="$(cat "$LOOP_STUB_CTRL" 2>/dev/null || echo none)"
case "$ctrl" in
  fix1) printf 'package src\n\n// fix one\n' > src/a.go ;;
  fix2) printf '\n// fix two\n' >> src/a.go ;;
  impl1) printf '\n// impl one\n' >> src/a.go ;;
  impl2) printf '\n// impl two\n' >> src/a.go ;;
  change) printf '\n// seeded work\n' >> src/a.go ;;
  vfail) printf '1\n' > .loop-verify-exit ;;
  supply) printf '{"name":"x"}\n' > package.json ;;
  protect) mkdir -p wiki && printf '# x\n' > wiki/x.md ;;
  slow) sleep 5 ;;
  none) : ;;
esac
printf 'did work\n' > "$output_file"
exit 0
`

// setLoopStubAction points the wrapper's next run at an action.
func setLoopStubAction(t *testing.T, ctrlPath, action string) {
	t.Helper()
	if err := os.WriteFile(ctrlPath, []byte(action), 0o644); err != nil {
		t.Fatal(err)
	}
}

// loopRig boots repo + mux stub + scripted wrapper + prefs (review line;
// extra prefs via prefsExtra).
func loopRig(t *testing.T, mux loopMux, prefsExtra string) (*testRig, string) {
	t.Helper()
	root := loopRigRepo(t)
	home := t.TempDir()
	t.Setenv("HOME", home)
	writePrefs(t, home, "review: rm1@test, rm2@test, rm3@test\nauto_apply: main\n"+prefsExtra)
	startLoopMuxStub(t, mux)
	ctrlPath := filepath.Join(t.TempDir(), "loop_stub_ctrl")
	setLoopStubAction(t, ctrlPath, "none")
	t.Setenv("LOOP_STUB_CTRL", ctrlPath)
	t.Setenv("ODO_OMP_WRAPPER", writeStub(t, loopWrapper))
	rig := startRig(t, root)
	t.Cleanup(func() { rig.stop(t) })
	return rig, ctrlPath
}

// --- journal fold helpers ------------------------------------------------------------

// loopScan decodes the conversation's loop journal rows.
type loopScan struct {
	rows      []map[string]interface{} // every loop_event payload, journal order
	review    []map[string]interface{} // every review_action payload
	userTexts []string                 // user_message texts (interleave evidence)
	errs      []string                 // agent_error texts
	states    map[int64]*loopState     // the fold
}

func scanLoop(t *testing.T, st *store.Store, convID int64) loopScan {
	t.Helper()
	events, err := st.ListEvents(context.Background(), convID, 0)
	if err != nil {
		t.Fatal(err)
	}
	out := loopScan{states: map[int64]*loopState{}}
	for _, st := range deriveLoopStates(events) {
		out.states[st.id] = st
	}
	for _, ev := range events {
		var p map[string]interface{}
		_ = json.Unmarshal(ev.Payload, &p)
		switch ev.Type {
		case store.EventLoopEvent:
			out.rows = append(out.rows, p)
		case store.EventReviewAction:
			out.review = append(out.review, p)
		case store.EventUserMessage:
			if text, _ := p["text"].(string); text != "" {
				out.userTexts = append(out.userTexts, text)
			}
		case store.EventAgentError:
			out.errs = append(out.errs, fmt.Sprint(p["error"]))
		}
	}
	return out
}

func (sc loopScan) kinds() []string {
	var out []string
	for _, r := range sc.rows {
		out = append(out, fmt.Sprint(r["kind"]))
	}
	return out
}

// ofKind filters rows by kind.
func (sc loopScan) ofKind(kind string) []map[string]interface{} {
	var out []map[string]interface{}
	for _, r := range sc.rows {
		if fmt.Sprint(r["kind"]) == kind {
			out = append(out, r)
		}
	}
	return out
}

// causes extracts suspended causes in order.
func (sc loopScan) causes() []string {
	var out []string
	for _, r := range sc.ofKind(loopKindSuspended) {
		out = append(out, fmt.Sprint(r["cause"]))
	}
	return out
}

// verdicts extracts loop_verdict verdicts in order.
func (sc loopScan) verdicts() []string {
	var out []string
	for _, r := range sc.ofKind(loopKindVerdict) {
		out = append(out, fmt.Sprint(r["verdict"]))
	}
	return out
}

// loopID returns the newest loop's id (0 when none).
func (sc loopScan) loopID() int64 {
	var id int64
	for k := range sc.states {
		if k > id {
			id = k
		}
	}
	return id
}

// acceptsWithActor counts review_action accept rows with the given actor.
func (sc loopScan) acceptsWithActor(actor string) int {
	n := 0
	for _, r := range sc.review {
		if fmt.Sprint(r["action"]) == "accept" && fmt.Sprint(r["actor"]) == actor {
			n++
		}
	}
	return n
}

// blockedReasonsOut lists auto_land_blocked reasons in order.
func (sc loopScan) blockedReasonsLoop() []string {
	var out []string
	for _, r := range sc.review {
		if fmt.Sprint(r["action"]) == "auto_land_blocked" {
			out = append(out, fmt.Sprint(r["reason"]))
		}
	}
	return out
}

// waitLoop polls the journal until match holds (30s deadline).
func waitLoop(t *testing.T, st *store.Store, convID int64, desc string, match func(loopScan) bool) loopScan {
	t.Helper()
	deadline := time.Now().Add(30 * time.Second)
	for {
		sc := scanLoop(t, st, convID)
		if match(sc) {
			return sc
		}
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s; kinds=%v verdicts=%v causes=%v blocked=%v",
				desc, sc.kinds(), sc.verdicts(), sc.causes(), sc.blockedReasonsLoop())
		}
		time.Sleep(50 * time.Millisecond)
	}
}

// loopQuiet asserts match never holds across d.
func loopQuiet(t *testing.T, st *store.Store, convID int64, d time.Duration, forbid string, match func(loopScan) bool) {
	t.Helper()
	deadline := time.Now().Add(d)
	for time.Now().Before(deadline) {
		if sc := scanLoop(t, st, convID); match(sc) {
			t.Fatalf("forbidden loop state appeared (%s): kinds=%v", forbid, sc.kinds())
		}
		time.Sleep(100 * time.Millisecond)
	}
}

// loopBoot bootstraps the rig's conversation.
func loopBoot(t *testing.T, rig *testRig) int64 {
	t.Helper()
	boot := rig.call(t, Request{Cmd: CmdBootstrap, ProjectRoot: rig.root})
	if boot.Conversation == nil {
		t.Fatal("bootstrap: missing conversation")
	}
	return boot.Conversation.ID
}

// --- pure-machine table tests ---------------------------------------------------------

func TestParseFindingsBlock(t *testing.T) {
	row := "- sev: P2 | file: internal/ipc/loop.go | symbol: tickLoop | title: budget check races resume"
	f, ok := parseFindingsBlock(auditFindings(row))
	if !ok || len(f) != 1 {
		t.Fatalf("single row: ok=%v n=%d", ok, len(f))
	}
	if f[0].Severity != "P2" || f[0].File != "internal/ipc/loop.go" || f[0].FP == "" {
		t.Errorf("parsed row wrong: %+v", f[0])
	}
	// No fence ⇒ parse_error (garbage contributes nothing).
	if _, ok := parseFindingsBlock("looks clean to me, nothing fenced"); ok {
		t.Error("missing fence must not parse")
	}
	// Garbage rows inside a valid block drop; the parseable ones survive.
	f, ok = parseFindingsBlock(auditFindings("this is not a row", row))
	if !ok || len(f) != 1 {
		t.Errorf("mixed block: ok=%v n=%d, want ok + 1 survivor", ok, len(f))
	}
	// Closure-pass resolved rows never enter the union.
	f, ok = parseFindingsBlock(auditFindings(
		row+" | status: resolved",
		"- sev: P1 | file: b.go | symbol: g | title: still broken",
	))
	if !ok || len(f) != 1 || f[0].Severity != "P1" {
		t.Errorf("resolved filtering: %+v", f)
	}
	// Empty block is a successful zero-finding parse.
	f, ok = parseFindingsBlock(auditClean)
	if !ok || len(f) != 0 {
		t.Errorf("empty block: ok=%v n=%d", ok, len(f))
	}
}

func TestUnionFindings(t *testing.T) {
	mk := func(sev, file, sym, title string) finding {
		f := finding{Severity: sev, File: file, Symbol: sym, Title: title}
		f.FP = findingFingerprint(f)
		return f
	}
	// Severity max-wins + leg counting on the same logical finding
	// (case/whitespace drift across legs must union to one entry).
	a := mk("P3", "a.go", "f", "x is wrong")
	b := mk("P1", "A.go ", " f", "  x   is  wrong ")
	union := unionFindings([][]finding{{a}, {b}})
	if len(union) != 1 {
		t.Fatalf("drifted duplicate legs: n=%d want 1", len(union))
	}
	if union[0].Severity != "P1" || union[0].Legs != 2 {
		t.Errorf("max-wins/legs: %+v", union[0])
	}
	// Distinct findings order deterministically by (file, symbol, title).
	c := mk("P0", "b.go", "g", "y breaks")
	union = unionFindings([][]finding{{a}, {c}})
	if len(union) != 2 || union[0].File != "a.go" || union[1].File != "b.go" {
		t.Errorf("order: %+v", union)
	}
}

func TestBlockingFindingsHoldGate(t *testing.T) {
	mk := func(sev string) finding {
		return finding{Severity: sev, File: "a.go", Symbol: "f", Title: "t"}
	}
	union := []finding{mk("P0"), mk("P1"), mk("P2"), mk("P3"), mk("unknown")}
	if got := len(blockingFindings(union, "P2")); got != 3 {
		t.Errorf("P2 hold: %d blocking, want 3 (P3/unknown never hold)", got)
	}
	if got := len(blockingFindings(union, "P1")); got != 2 {
		t.Errorf("P1 hold: %d blocking, want 2", got)
	}
}

func TestDeriveLoopStatesFold(t *testing.T) {
	mk := func(seq int, payload string) store.Event {
		return store.Event{Seq: seq, Type: store.EventLoopEvent, Payload: json.RawMessage(payload)}
	}
	rev := func(seq int, payload string) store.Event {
		return store.Event{Seq: seq, Type: store.EventReviewAction, Payload: json.RawMessage(payload)}
	}
	events := []store.Event{
		mk(1, `{"kind":"loop_started","mode":"audit","base":"abc","max_rounds":10,"budget_tokens":2000000,"hold_severity":"P2","spent_tokens":0}`),
		mk(3, `{"kind":"loop_audit_round","loop_id":1,"round":1,"subject_sha16":"s1","legs":[{"model":"m","verdict":"complete"}],"spent_tokens":100}`),
		mk(4, `{"kind":"loop_verdict","loop_id":1,"round":1,"verdict":"fix","blocking_fps":["f1"],"spent_tokens":100}`),
		mk(5, `{"kind":"loop_fix_spawn","loop_id":1,"round":1,"spent_tokens":200}`),
		rev(6, `{"action":"accept","actor":"auto_loop","diff_id":9}`),
		mk(7, `{"kind":"loop_suspended","loop_id":1,"cause":"human_interleave","spent_tokens":200}`),
		mk(8, `{"kind":"loop_resumed","loop_id":1,"cause":"human_interleave","spent_tokens":200}`),
		mk(9, `{"kind":"loop_audit_round","loop_id":1,"round":2,"subject_sha16":"s2","legs":[{"model":"m","verdict":"complete"}],"spent_tokens":300}`),
		mk(10, `{"kind":"loop_verdict","loop_id":1,"round":2,"verdict":"clean","blocking_fps":[],"spent_tokens":300}`),
		mk(11, `{"kind":"loop_completed","loop_id":1,"rounds":2,"spent_tokens":300}`),
	}
	states := deriveLoopStates(events)
	if len(states) != 1 {
		t.Fatalf("states: %d", len(states))
	}
	st := states[0]
	if st.id != 1 {
		t.Errorf("loop id from started seq: %d", st.id)
	}
	if st.status != "completed" || st.spentTokens != 300 || st.fixesLanded != 1 {
		t.Errorf("terminal fold: %+v", st)
	}
	if len(st.rounds) != 2 || st.rounds[0].verdict != "fix" || st.rounds[1].verdict != "clean" {
		t.Errorf("rounds: %+v", st.rounds)
	}
	// The resume after human_interleave resolved the interrupted fix to a
	// re-audit (fixOutcome unlanded), not a respawn.
	if st.awaitingFixSpawn || st.fixOpen {
		t.Errorf("fix phase must be closed: %+v", st)
	}
	// Budget raise + respawn class.
	events = []store.Event{
		mk(1, `{"kind":"loop_started","mode":"audit","base":"abc","max_rounds":10,"budget_tokens":100000,"hold_severity":"P2","spent_tokens":0}`),
		mk(2, `{"kind":"loop_audit_round","loop_id":1,"round":1,"subject_sha16":"s1","legs":[],"spent_tokens":99}`),
		mk(3, `{"kind":"loop_verdict","loop_id":1,"round":1,"verdict":"fix","blocking_fps":["f1"],"spent_tokens":99}`),
		mk(4, `{"kind":"loop_suspended","loop_id":1,"cause":"fix_no_diff","spent_tokens":99}`),
		mk(5, `{"kind":"loop_resumed","loop_id":1,"cause":"fix_no_diff","budget":250000,"spent_tokens":99}`),
	}
	st = deriveLoopStates(events)[0]
	if st.budgetTokens != 250000 {
		t.Errorf("budget raise: %d", st.budgetTokens)
	}
	if !st.awaitingFixSpawn {
		t.Error("fix_no_diff resume must re-arm the fix spawn (one automatic re-spawn)")
	}
	if st.status != "active" {
		t.Errorf("resume: status %q", st.status)
	}
}

// --- behavior drills ------------------------------------------------------------------

// TestLoopAuditRefusesEmpty pins the nothing_to_audit pre-journal error.
func TestLoopAuditRefusesEmpty(t *testing.T) {
	rig, _ := loopRig(t, nil, "")
	convID := loopBoot(t, rig)
	resp := rig.callExpectErr(t, Request{Cmd: CmdSendMessage, ConversationID: convID, Text: "/loop audit"})
	if !strings.Contains(resp.Error, "nothing_to_audit") {
		t.Fatalf("error = %q, want nothing_to_audit", resp.Error)
	}
	sc := scanLoop(t, rig.store, convID)
	if len(sc.rows) != 0 {
		t.Errorf("pre-journal refusal must journal no loop rows: %v", sc.kinds())
	}
}

// TestLoopSecondLoopRefused pins one-loop-per-conversation (C10).
func TestLoopSecondLoopRefused(t *testing.T) {
	// A blocking finding keeps the loop non-terminal at every observation
	// point (mid-audit, mid-fix, suspended) — a clean mux could complete
	// before the second call and mask the C10 refusal.
	rig, _ := loopRig(t, func(kind string, n int, model string) (int, string, int) {
		if kind == "audit" {
			return 200, auditFindings("- sev: P2 | file: src/a.go | symbol: a | title: never clean"), 0
		}
		return 200, "", 0
	}, "")
	convID := loopBoot(t, rig)
	// Give the loop a subject range: HEAD diverges from the frozen base.
	// Freeze the base BEFORE the work commit and pass it explicitly —
	// V6: base comes only from SEED pending diffs or an explicit base=
	// arg; the loop never auto-derives an audit base from history.
	base := gitOut(t, rig.root, "rev-parse", "HEAD")
	gitIn(t, rig.root, "commit", "--allow-empty", "-m", "work")
	rig.call(t, Request{Cmd: CmdSendMessage, ConversationID: convID, Text: "/loop audit base=" + base})
	waitLoop(t, rig.store, convID, "loop started", func(sc loopScan) bool { return len(sc.ofKind(loopKindStarted)) == 1 })
	resp := rig.callExpectErr(t, Request{Cmd: CmdSendMessage, ConversationID: convID, Text: "/loop audit"})
	if !strings.Contains(resp.Error, "already") {
		t.Fatalf("second loop: error = %q, want already-active refusal", resp.Error)
	}
}

// TestLoopFixpointClean is the fixpoint's happy path: three rounds — two
// blocking findings fixed and landed, round 3 clean, loop_completed with
// two auto_loop accepts and per-round subject growth.
func TestLoopFixpointClean(t *testing.T) {
	// The fix run starts WITH its spawn row, so arming the wrapper action
	// after the spawn is a lost race. The mux arms round R's action inside
	// round R's audit window instead: strictly after round R-1's run read
	// the file, strictly before round R's spawn. Idempotent across the
	// three concurrent legs (same value every write).
	var ctrl string
	rig, ret := loopRig(t, func(kind string, n int, model string) (int, string, int) {
		// Concurrent legs arrive in any order; round = the call window
		// (3 legs per round, sequential rounds).
		switch kind {
		case "audit":
			switch (n-1)/3 + 1 {
			case 1:
				_ = os.WriteFile(ctrl, []byte("fix1"), 0o644)
				return 200, auditFindings("- sev: P2 | file: src/a.go | symbol: a | title: missing doc"), 10
			case 2:
				_ = os.WriteFile(ctrl, []byte("fix2"), 0o644)
				return 200, auditFindings("- sev: P2 | file: src/a.go | symbol: b | title: wrong comment style"), 10
			default:
				return 200, auditClean, 10
			}
		case "review":
			return 200, reviewVerdictText("ACCEPT"), 10
		}
		return 200, "", 0
	}, "")
	ctrl = ret
	convID := loopBoot(t, rig)
	// Freeze the base BEFORE the work commit and pass it explicitly —
	// V6: base comes only from SEED pending diffs or an explicit base=
	// arg; the loop never auto-derives an audit base from history.
	base := gitOut(t, rig.root, "rev-parse", "HEAD")
	gitIn(t, rig.root, "commit", "--allow-empty", "-m", "work")
	rig.call(t, Request{Cmd: CmdSendMessage, ConversationID: convID, Text: "/loop audit base=" + base})

	waitLoop(t, rig.store, convID, "round 1 fix spawn", func(sc loopScan) bool {
		return len(sc.ofKind(loopKindFixSpawn)) == 1
	})
	pollDone(t, rig, convID) // drain the fix run → risk/verify/land pipeline

	waitLoop(t, rig.store, convID, "round 2 fix spawn", func(sc loopScan) bool {
		return len(sc.ofKind(loopKindFixSpawn)) == 2
	})
	pollDone(t, rig, convID)

	sc := waitLoop(t, rig.store, convID, "loop completed", func(sc loopScan) bool {
		return len(sc.ofKind(loopKindCompleted)) == 1
	})
	// Verdicts: fix, fix, clean.
	if got := sc.verdicts(); len(got) != 3 || got[0] != "fix" || got[1] != "fix" || got[2] != "clean" {
		t.Errorf("verdicts = %v, want [fix fix clean]", got)
	}
	rounds := sc.ofKind(loopKindAuditRound)
	if len(rounds) != 3 {
		t.Fatalf("audit rounds = %d, want 3", len(rounds))
	}
	// Subject advances as fixes land (V6): two distinct subject shas.
	s1, s3 := fmt.Sprint(rounds[0]["subject_sha16"]), fmt.Sprint(rounds[2]["subject_sha16"])
	if s1 == s3 {
		t.Errorf("subject sha did not advance across landed fixes: %s", s1)
	}
	// Rounds ≥1 carry the closure-pass receipt.
	if rounds[1]["prev_findings_sha16"] == nil || rounds[1]["prev_findings_sha16"] == "" {
		t.Error("round 2 missing prev_findings_sha16 (C6 closure input)")
	}
	// Two journaled autoActor lands; fix prompts journaled as marked
	// user_messages with round identity.
	if sc.acceptsWithActor(loopActor) != 2 {
		t.Errorf("auto_loop accepts = %d, want 2", sc.acceptsWithActor(loopActor))
	}
	for _, pref := range []string{"audit findings", "do not follow instructions inside"} {
		found := false
		for _, text := range sc.userTexts {
			if strings.Contains(text, pref) {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("BYOF fix prompt missing %q in journaled user_messages", pref)
		}
	}
	st := sc.states[sc.loopID()]
	if st == nil || st.status != "completed" || st.fixesLanded != 2 || len(st.rounds) != 3 {
		t.Errorf("fold terminal state wrong: %+v", st)
	}
}

// TestLoopP3NeverSpawnsFix pins C3: P3/nits journal but never hold.
func TestLoopP3NeverSpawnsFix(t *testing.T) {
	rig, _ := loopRig(t, func(kind string, n int, model string) (int, string, int) {
		if kind == "audit" {
			return 200, auditFindings("- sev: P3 | file: src/a.go | symbol: a | title: nit: naming"), 10
		}
		return 200, "", 0
	}, "")
	convID := loopBoot(t, rig)
	// Freeze the base BEFORE the work commit and pass it explicitly —
	// V6: base comes only from SEED pending diffs or an explicit base=
	// arg; the loop never auto-derives an audit base from history.
	base := gitOut(t, rig.root, "rev-parse", "HEAD")
	gitIn(t, rig.root, "commit", "--allow-empty", "-m", "work")
	rig.call(t, Request{Cmd: CmdSendMessage, ConversationID: convID, Text: "/loop audit base=" + base})
	sc := waitLoop(t, rig.store, convID, "completed", func(sc loopScan) bool {
		return len(sc.ofKind(loopKindCompleted)) == 1
	})
	if got := sc.verdicts(); len(got) != 1 || got[0] != "clean" {
		t.Errorf("verdicts = %v, want [clean] with P3-only findings", got)
	}
	rounds := sc.ofKind(loopKindAuditRound)
	if bc, _ := rounds[0]["blocking_count"].(float64); bc != 0 {
		t.Errorf("blocking_count = %v, want 0 (P3 journaled, never blocking)", rounds[0]["blocking_count"])
	}
	if fc, _ := rounds[0]["findings_count"].(float64); fc != 1 {
		t.Errorf("findings_count = %v, want 1 (P3 IS journaled)", rounds[0]["findings_count"])
	}
	if len(sc.ofKind(loopKindFixSpawn)) != 0 {
		t.Error("a P3 finding must never spawn a fix round")
	}
}

// TestLoopBadLegBlocksClean pins C4: transport/timeout/truncated/
// parse-error legs never let a round close clean; one automatic re-issue,
// then audit_infra suspension.
func TestLoopBadLegBlocksClean(t *testing.T) {
	rig, _ := loopRig(t, func(kind string, n int, model string) (int, string, int) {
		if kind != "audit" {
			return 200, "", 0
		}
		// The stub sees req.model = the wire model (m.model, no @provider).
		switch model {
		case "rm1":
			if (n-1)/3+1 == 1 {
				return 200, "no fence here — just prose", 10 // parse_error
			}
			return 500, "", 0 // infra on the re-issue
		case "rm2":
			return 200, "\x00TRUNCATED\x00", 10 // truncated every round
		default:
			return 200, auditClean, 10
		}
	}, "")
	convID := loopBoot(t, rig)
	// Freeze the base BEFORE the work commit and pass it explicitly —
	// V6: base comes only from SEED pending diffs or an explicit base=
	// arg; the loop never auto-derives an audit base from history.
	base := gitOut(t, rig.root, "rev-parse", "HEAD")
	gitIn(t, rig.root, "commit", "--allow-empty", "-m", "work")
	rig.call(t, Request{Cmd: CmdSendMessage, ConversationID: convID, Text: "/loop audit base=" + base})
	sc := waitLoop(t, rig.store, convID, "audit_infra suspend", func(sc loopScan) bool {
		return len(sc.causes()) == 1 && sc.causes()[0] == "audit_infra"
	})
	// Two invalid rounds (original + the ONE automatic re-issue), never
	// clean despite zero blocking findings from good legs.
	if got := sc.verdicts(); len(got) != 2 || got[0] != "audit_infra" || got[1] != "audit_infra" {
		t.Errorf("verdicts = %v, want [audit_infra audit_infra]", got)
	}
	if len(sc.ofKind(loopKindFixSpawn)) != 0 {
		t.Error("infra rounds never spawn fixes")
	}
	legs := sc.ofKind(loopKindAuditRound)[0]["legs"].([]interface{})
	var m map[string]interface{}
	_ = json.Unmarshal(mustJSON2(legs[0]), &m)
	if m["verdict"] == "complete" {
		t.Errorf("rm1's parse_error leg misreported complete: %v", legs)
	}
}

func mustJSON2(v interface{}) []byte {
	b, _ := json.Marshal(v)
	return b
}

// TestLoopHoldSeverityTightened pins the loop_hold_severity pref: with
// P1, a P2 finding never blocks.
func TestLoopHoldSeverityTightened(t *testing.T) {
	rig, _ := loopRig(t, func(kind string, n int, model string) (int, string, int) {
		if kind == "audit" {
			return 200, auditFindings("- sev: P2 | file: src/a.go | symbol: a | title: would block at P2 hold"), 10
		}
		return 200, "", 0
	}, "loop_hold_severity: P1\n")
	convID := loopBoot(t, rig)
	// Freeze the base BEFORE the work commit and pass it explicitly —
	// V6: base comes only from SEED pending diffs or an explicit base=
	// arg; the loop never auto-derives an audit base from history.
	base := gitOut(t, rig.root, "rev-parse", "HEAD")
	gitIn(t, rig.root, "commit", "--allow-empty", "-m", "work")
	rig.call(t, Request{Cmd: CmdSendMessage, ConversationID: convID, Text: "/loop audit base=" + base})
	sc := waitLoop(t, rig.store, convID, "completed", func(sc loopScan) bool {
		return len(sc.ofKind(loopKindCompleted)) == 1
	})
	if got := sc.verdicts(); len(got) != 1 || got[0] != "clean" {
		t.Errorf("verdicts = %v, want [clean] under loop_hold_severity: P1", got)
	}
	if len(sc.ofKind(loopKindFixSpawn)) != 0 {
		t.Error("P2 must not block under P1 hold")
	}
}

// --- P1/P2 review-fix pins ----------------------------------------------------

// TestLoopRowSpendConcreteSlices pins P1 (C12): journal-time payloads
// carry CONCRETE slices ([]auditLegResult, []DesignProposal) that never
// satisfy a .([]interface{}) assertion — leg output_tokens must
// accumulate through the JSON wire shape.
func TestLoopRowSpendConcreteSlices(t *testing.T) {
	legs := []auditLegResult{
		{Model: "rm1@test", Verdict: "complete", OutputTokens: 120},
		{Model: "rm2@test", Verdict: "infra"}, // zero-token leg contributes 0
		{Model: "rm3@test", Verdict: "complete", OutputTokens: 34},
	}
	if got := loopRowSpend(map[string]interface{}{"legs": legs}); got != 154 {
		t.Errorf("concrete legs slice: spend = %d, want 154", got)
	}
	proposals := []DesignProposal{
		{Model: "rm1@test", OutputTokens: 200},
		{Model: "rm2@test", Error: "boom"}, // failed legs carry no spend
	}
	payload := map[string]interface{}{
		"proposals":    proposals,
		"consolidator": map[string]interface{}{"model": "orc", "output_tokens": 55},
	}
	if got := loopRowSpend(payload); got != 255 {
		t.Errorf("design row: spend = %d, want 255", got)
	}
	// The pre-normalization shapes keep working: spawn estimates and
	// journal-decoded (already-wire-shaped) rows.
	if got := loopRowSpend(map[string]interface{}{"prompt_tokens_est": 500}); got != 500 {
		t.Errorf("spawn estimate: spend = %d, want 500", got)
	}
	var decoded map[string]interface{}
	if err := json.Unmarshal([]byte(`{"legs":[{"model":"m","output_tokens":10}]}`), &decoded); err != nil {
		t.Fatal(err)
	}
	if got := loopRowSpend(decoded); got != 10 {
		t.Errorf("journal-decoded row: spend = %d, want 10", got)
	}
}

// TestLoopImplementerAdapterPinned pins P1 (V12): with loop_implementer
// set, the override registers under the stable "loop" key exactly once,
// and the runMeta's adapter key resolves to the SAME instance the run
// started on (the orphan instance answered Events/Cancel "unknown run").
func TestLoopImplementerAdapterPinned(t *testing.T) {
	rig, _ := loopRig(t, nil, "loop_implementer: acme/pi-9@test\n")
	convID := loopBoot(t, rig)
	ctx := context.Background()
	ev, err := rig.server.journalLoop(ctx, convID, 0, loopModeAudit, loopKindStarted, map[string]interface{}{
		"base": "abc", "max_rounds": 3, "budget_tokens": 2_000_000, "hold_severity": "P2",
	}, 0)
	if err != nil {
		t.Fatal(err)
	}
	loopID := int64(ev.Seq)
	admitted, reason := rig.server.startLoopRunLocked(ctx, convID, loopID, "fix", 1, 0,
		"fix prompt", map[string]interface{}{"round": 1}, nil, 0)
	if !admitted {
		t.Fatalf("spawn refused: %s", reason)
	}
	reg, ok := rig.server.adapterNamed("loop")
	if !ok {
		t.Fatal("loop_implementer override not registered under the 'loop' key")
	}
	if reg == rig.server.adapterFor("") {
		t.Fatal("the override IS the default instance — pref not honored")
	}
	rig.server.mu.Lock()
	meta := rig.server.runs[rig.server.byConv[convID]]
	rig.server.mu.Unlock()
	if meta == nil {
		t.Fatal("no run registered for the conversation")
	}
	if meta.adapter != "loop" {
		t.Errorf("meta.adapter = %q, want \"loop\" (drain/cancel must resolve the override, not \"\")", meta.adapter)
	}
	if got := rig.server.adapterFor(meta.adapter); got != reg {
		t.Error("drain-time adapterFor(meta.adapter) resolves a DIFFERENT instance than the run started on")
	}
	// Register-once: a second resolution returns the same instance (a
	// mid-loop prefs edit never swaps instances under a live run). The
	// strict s.mu contract from spawn time is not needed here — the test
	// is single-threaded and adaptersMu guards the registry itself.
	ad2, key2 := rig.server.loopRunAdapterLocked()
	if ad2 != reg || key2 != "loop" {
		t.Errorf("re-resolution = (%v, %q), want the registered instance under \"loop\"", ad2, key2)
	}
	pollDone(t, rig, convID) // drain the stub run (ctrl=none → no diff)
}

// TestLoopHumanSendSuspendsMidLoopRun pins P1 (V8): a human send during
// a loop fix run is NEVER refused — it cancels the loop run, journals the
// message, then suspends the loop in that order.
func TestLoopHumanSendSuspendsMidLoopRun(t *testing.T) {
	rig, ctrl := loopRig(t, nil, "")
	convID := loopBoot(t, rig)
	ctx := context.Background()
	ev, err := rig.server.journalLoop(ctx, convID, 0, loopModeAudit, loopKindStarted, map[string]interface{}{
		"base": "abc", "max_rounds": 3, "budget_tokens": 2_000_000, "hold_severity": "P2",
	}, 0)
	if err != nil {
		t.Fatal(err)
	}
	loopID := int64(ev.Seq)
	setLoopStubAction(t, ctrl, "slow") // the fix run stays mid-flight for 5s
	admitted, reason := rig.server.startLoopRunLocked(ctx, convID, loopID, "fix", 1, 0,
		"fix prompt", map[string]interface{}{"round": 1}, nil, 0)
	if !admitted {
		t.Fatalf("spawn refused: %s", reason)
	}
	// Pre-fix behavior: this send returned "agent already running" and the
	// loop never suspended (the refusal pre-empted the V8 hook).
	resp := rig.call(t, Request{Cmd: CmdSendMessage, ConversationID: convID, Text: "human interrupt"})
	if resp.Error != "" {
		t.Fatalf("human send during a loop run must never be refused: %s", resp.Error)
	}
	sc := scanLoop(t, rig.store, convID)
	if causes := sc.causes(); len(causes) != 1 || causes[0] != "human_interleave" {
		t.Errorf("causes = %v, want exactly [human_interleave]", causes)
	}
	if st := sc.states[sc.loopID()]; st == nil || st.status != "suspended" {
		t.Errorf("fold status = %+v, want suspended", st)
	}
	// V8 ordering: the user_message lands BEFORE the suspension row
	// (the suspension always postdates the send).
	events, err := rig.store.ListEvents(ctx, convID, 0)
	if err != nil {
		t.Fatal(err)
	}
	var sendSeq, suspendSeq int
	for _, e := range events {
		switch e.Type {
		case store.EventUserMessage:
			if strings.Contains(string(e.Payload), "human interrupt") {
				sendSeq = e.Seq
			}
		case store.EventLoopEvent:
			if jsonStr(e.Payload, "kind") == loopKindSuspended {
				suspendSeq = e.Seq
			}
		}
	}
	if sendSeq == 0 || suspendSeq == 0 {
		t.Fatalf("journal missing rows: send=%d suspend=%d", sendSeq, suspendSeq)
	}
	if sendSeq > suspendSeq {
		t.Errorf("suspension (seq %d) predates the send (seq %d)", suspendSeq, sendSeq)
	}
	pollDone(t, rig, convID) // the human run continues normally (slow stub)
	// The cancelled fix run never re-suspends a different cause.
	if causes := scanLoop(t, rig.store, convID).causes(); len(causes) != 1 || causes[0] != "human_interleave" {
		t.Errorf("post-drain causes = %v, want [human_interleave]", causes)
	}
}

// TestLoopDrainSkipsInactiveFold pins the stale-drain guard: a loop run
// whose drain arrives after the fold moved on (suspended, stopped) must
// journal NOTHING — the cancelled run's taint row may never rewrite the
// standing cause, and a terminal loop never flips back to suspended.
func TestLoopDrainSkipsInactiveFold(t *testing.T) {
	rig, _ := loopRig(t, nil, "")
	convID := loopBoot(t, rig)
	ctx := context.Background()
	mkLoop := func() (int64, *runMeta) {
		ev, err := rig.server.journalLoop(ctx, convID, 0, loopModeAudit, loopKindStarted, map[string]interface{}{
			"base": "abc", "max_rounds": 3, "budget_tokens": 2_000_000, "hold_severity": "P2",
		}, 0)
		if err != nil {
			t.Fatal(err)
		}
		id := int64(ev.Seq)
		if _, err := rig.server.journalLoop(ctx, convID, id, loopModeAudit, loopKindFixSpawn, map[string]interface{}{"round": 1}, 0); err != nil {
			t.Fatal(err)
		}
		return id, &runMeta{conversationID: convID, loopID: id, loopKind: "fix", loopRound: 1}
	}
	// Control: active fold with the fix phase open — the taint row journals.
	_, meta1 := mkLoop()
	rig.server.loopNoDiffAfterRun(ctx, meta1, verdictNoText)
	if causes := scanLoop(t, rig.store, convID).causes(); len(causes) != 1 || causes[0] != "run_tainted" {
		t.Fatalf("active fold: causes = %v, want [run_tainted]", causes)
	}
	// Suspended loop: the same drain is inert — the standing cause wins.
	id2, meta2 := mkLoop()
	rig.server.journalLoopBestEffort(ctx, convID, id2, loopModeAudit, loopKindSuspended,
		map[string]interface{}{"cause": "human_interleave"}, 0)
	rig.server.loopNoDiffAfterRun(ctx, meta2, verdictNoText)
	// Stopped loop: the drain is equally inert (cancelLoopRun's contract).
	id3, meta3 := mkLoop()
	rig.server.journalLoopBestEffort(ctx, convID, id3, loopModeAudit, loopKindStopped, map[string]interface{}{"detail": "stopped"}, 0)
	rig.server.loopNoDiffAfterRun(ctx, meta3, verdictNoText)
	causes := scanLoop(t, rig.store, convID).causes()
	if len(causes) != 2 || causes[0] != "run_tainted" || causes[1] != "human_interleave" {
		t.Errorf("stale drains journaled: causes = %v, want exactly [run_tainted human_interleave]", causes)
	}
}

// TestLoopRiskGateSuspends pins the V5 security gates: a fix diff
// touching protected paths or rated supply_chain suspends the loop and
// NEVER lands (zero auto_loop accepts).
func TestLoopRiskGateSuspends(t *testing.T) {
	for _, tc := range []struct {
		name      string
		action    string
		wantCause string
	}{
		{"protected path", "protect", "risk:protected_path"},
		{"supply chain", "supply", "risk:supply_chain"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var ctrl string
			rig, ret := loopRig(t, func(kind string, n int, model string) (int, string, int) {
				switch kind {
				case "audit":
					_ = os.WriteFile(ctrl, []byte(tc.action), 0o644)
					return 200, auditFindings("- sev: P2 | file: src/a.go | symbol: a | title: missing guard"), 10
				case "review":
					return 200, reviewVerdictText("ACCEPT"), 10
				}
				return 200, "", 0
			}, "")
			ctrl = ret
			convID := loopBoot(t, rig)
			base := gitOut(t, rig.root, "rev-parse", "HEAD")
			gitIn(t, rig.root, "commit", "--allow-empty", "-m", "work")
			rig.call(t, Request{Cmd: CmdSendMessage, ConversationID: convID, Text: "/loop audit base=" + base})
			waitLoop(t, rig.store, convID, "fix spawn", func(sc loopScan) bool {
				return len(sc.ofKind(loopKindFixSpawn)) == 1
			})
			pollDone(t, rig, convID)
			sc := waitLoop(t, rig.store, convID, "risk suspension", func(sc loopScan) bool {
				causes := sc.causes()
				return len(causes) == 1
			})
			if got := sc.causes()[0]; got != tc.wantCause {
				t.Errorf("cause = %q, want %q", got, tc.wantCause)
			}
			if sc.acceptsWithActor(loopActor) != 0 {
				t.Error("a risk-gated fix must NEVER land (auto_loop accept found)")
			}
			if got := sc.verdicts(); len(got) != 1 || got[0] != "fix" {
				t.Errorf("verdicts = %v, want [fix]", got)
			}
			if st := sc.states[sc.loopID()]; st.status != "suspended" {
				t.Errorf("fold status = %q, want suspended", st.status)
			}
		})
	}
}

// TestLoopStallSuspends pins C5: the blocking fingerprint set unchanged
// across a landed fix is a stall — the loop suspends instead of burning
// rounds on a fix that provably doesn't move the findings.
func TestLoopStallSuspends(t *testing.T) {
	const sameFinding = "- sev: P2 | file: src/a.go | symbol: a | title: missing guard"
	var ctrl string
	rig, ret := loopRig(t, func(kind string, n int, model string) (int, string, int) {
		switch kind {
		case "audit":
			switch (n-1)/3 + 1 {
			case 1:
				_ = os.WriteFile(ctrl, []byte("fix1"), 0o644)
			default:
				_ = os.WriteFile(ctrl, []byte("fix2"), 0o644)
			}
			return 200, auditFindings(sameFinding), 10 // identical fp every round
		case "review":
			return 200, reviewVerdictText("ACCEPT"), 10
		}
		return 200, "", 0
	}, "")
	ctrl = ret
	convID := loopBoot(t, rig)
	base := gitOut(t, rig.root, "rev-parse", "HEAD")
	gitIn(t, rig.root, "commit", "--allow-empty", "-m", "work")
	rig.call(t, Request{Cmd: CmdSendMessage, ConversationID: convID, Text: "/loop audit base=" + base})
	waitLoop(t, rig.store, convID, "round 1 fix spawn", func(sc loopScan) bool {
		return len(sc.ofKind(loopKindFixSpawn)) == 1
	})
	pollDone(t, rig, convID) // round 1 fix LANDS — the stall comparator arms
	sc := waitLoop(t, rig.store, convID, "stall suspension", func(sc loopScan) bool {
		return len(sc.causes()) == 1
	})
	if got := sc.causes()[0]; got != "stall" {
		t.Errorf("cause = %q, want stall", got)
	}
	if got := sc.verdicts(); len(got) != 2 || got[0] != "fix" || got[1] != "stall" {
		t.Errorf("verdicts = %v, want [fix stall]", got)
	}
	if sc.acceptsWithActor(loopActor) != 1 {
		t.Errorf("accepts = %d, want 1 (round 1's fix landed; the stall ride has no fix)", sc.acceptsWithActor(loopActor))
	}
	if len(sc.ofKind(loopKindCompleted)) != 0 {
		t.Error("a stalled loop must not complete")
	}
}

// TestLoopBudgetExceededResume pins the C12 budget breaker (P1/C12 spend
// ledger end to end): round 1's Σ leg output_tokens crosses
// loop_budget_tokens, the loop suspends loop_budget_exceeded, and
// /loop resume budget=N raises the cap and runs to a clean completion.
func TestLoopBudgetExceededResume(t *testing.T) {
	const legTokens = 40_000 // 3 legs × 40k = 120k > 100k budget floor
	rig, _ := loopRig(t, func(kind string, n int, model string) (int, string, int) {
		switch kind {
		case "audit":
			if (n-1)/3+1 == 1 {
				return 200, auditFindings("- sev: P2 | file: src/a.go | symbol: a | title: missing guard"), legTokens
			}
			return 200, auditClean, 100
		case "review":
			return 200, reviewVerdictText("ACCEPT"), 10
		}
		return 200, "", 0
	}, "loop_budget_tokens: 100000\n")
	convID := loopBoot(t, rig)
	base := gitOut(t, rig.root, "rev-parse", "HEAD")
	gitIn(t, rig.root, "commit", "--allow-empty", "-m", "work")
	rig.call(t, Request{Cmd: CmdSendMessage, ConversationID: convID, Text: "/loop audit base=" + base})
	sc := waitLoop(t, rig.store, convID, "budget breaker", func(sc loopScan) bool {
		return len(sc.ofKind(loopKindBudgetExceeded)) == 1
	})
	// P1 (C12) end to end: the round row's spend includes Σ leg
	// output_tokens (120000) — zero meant the concrete-slice assertion
	// ate every leg's receipt.
	rounds := sc.ofKind(loopKindAuditRound)
	if len(rounds) != 1 {
		t.Fatalf("audit rounds = %d, want 1", len(rounds))
	}
	if got, _ := rounds[0]["spent_tokens"].(float64); int(got) != 3*legTokens {
		t.Errorf("round spent_tokens = %v, want %d (Σ leg output_tokens)", rounds[0]["spent_tokens"], 3*legTokens)
	}
	if st := sc.states[sc.loopID()]; st.status != "suspended" || st.cause != "budget_exceeded" {
		t.Fatalf("fold = %+v, want suspended budget_exceeded", st)
	}
	// Resume with a raised cap: the loop re-audits (the interrupted fix
	// resolves to a re-audit on a budget resume) and completes clean.
	resp := rig.call(t, Request{Cmd: CmdSendMessage, ConversationID: convID, Text: "/loop resume budget=500000"})
	if resp.Error != "" {
		t.Fatalf("resume: %s", resp.Error)
	}
	sc = waitLoop(t, rig.store, convID, "completion after resume", func(sc loopScan) bool {
		return len(sc.ofKind(loopKindCompleted)) == 1
	})
	if got := sc.verdicts(); len(got) != 2 || got[0] != "fix" || got[1] != "clean" {
		t.Errorf("verdicts = %v, want [fix clean]", got)
	}
	resumed := sc.ofKind(loopKindResumed)
	if len(resumed) != 1 {
		t.Fatalf("resumed rows = %d, want 1", len(resumed))
	}
	if got, _ := resumed[0]["budget"].(float64); int(got) != 500000 {
		t.Errorf("resume budget = %v, want 500000", resumed[0]["budget"])
	}
}

// TestLoopRestartMidRunSuspends pins V7: a daemon restart with a fix run
// in flight suspends restart_mid_run on recovery (the worktree may hold
// partial side effects; the human inspects and resumes).
func TestLoopRestartMidRunSuspends(t *testing.T) {
	root := loopRigRepo(t)
	home := t.TempDir()
	t.Setenv("HOME", home)
	writePrefs(t, home, "review: rm1@test, rm2@test, rm3@test\nauto_apply: main\n")
	ctrlPath := filepath.Join(t.TempDir(), "loop_stub_ctrl")
	setLoopStubAction(t, ctrlPath, "none")
	t.Setenv("LOOP_STUB_CTRL", ctrlPath)
	t.Setenv("ODO_OMP_WRAPPER", writeStub(t, loopWrapper))
	startLoopMuxStub(t, func(kind string, n int, model string) (int, string, int) {
		if kind == "audit" {
			_ = os.WriteFile(ctrlPath, []byte("slow"), 0o644) // the fix run sleeps through the kill
			return 200, auditFindings("- sev: P2 | file: src/a.go | symbol: a | title: missing guard"), 10
		}
		return 200, "", 0
	})
	rig1 := startRig(t, root)
	convID := loopBoot(t, rig1)
	base := gitOut(t, root, "rev-parse", "HEAD")
	gitIn(t, root, "commit", "--allow-empty", "-m", "work")
	rig1.call(t, Request{Cmd: CmdSendMessage, ConversationID: convID, Text: "/loop audit base=" + base})
	waitLoop(t, rig1.store, convID, "fix spawn", func(sc loopScan) bool {
		return len(sc.ofKind(loopKindFixSpawn)) == 1
	})
	rig1.stop(t) // daemon killed mid fix run — no drain, no diff rows

	rig2 := startRig(t, root) // NewServer → recoverLoops runs synchronously
	t.Cleanup(func() { rig2.stop(t) })
	sc := scanLoop(t, rig2.store, convID)
	if causes := sc.causes(); len(causes) != 1 || causes[0] != "restart_mid_run" {
		t.Errorf("post-restart causes = %v, want [restart_mid_run]", causes)
	}
	if st := sc.states[sc.loopID()]; st == nil || st.status != "suspended" {
		t.Errorf("post-restart fold = %+v, want suspended", st)
	}
}

// TestLoopLeadingFlagsOnly pins the P2 parser contract: flags parse from
// the LEADING run of k=v tokens only; a k=v-looking token inside task
// text is inert, and the body keeps everything from the first non-flag
// token on.
func TestLoopLeadingFlagsOnly(t *testing.T) {
	cases := []struct {
		args  string
		wantN int // expected flag count
		key   string
		val   string
		body  string
	}{
		{"base=abc rounds=2 budget=300000", 3, "rounds", "2", ""},
		{"rounds=2 budget=300000 1. set log warn=true", 2, "budget", "300000", "1. set log warn=true"},
		{"1. fix bounds n=64 rounds=9", 0, "", "", "1. fix bounds n=64 rounds=9"}, // flags trailing NEVER parse
		{"queue", 0, "", "", "queue"},
		{"rounds=2 file:docs/tasks.md", 1, "rounds", "2", "file:docs/tasks.md"},
		{"k= rounds=2 1. x", 0, "", "", "k= rounds=2 1. x"}, // empty value is not a flag; leading run broken
		{"", 0, "", "", ""},
	}
	for _, tc := range cases {
		flags, body := loopLeadingFlags(tc.args)
		if len(flags) != tc.wantN {
			t.Errorf("%q: flags = %v, want %d entries", tc.args, flags, tc.wantN)
		}
		if tc.key != "" && flags[tc.key] != tc.val {
			t.Errorf("%q: flags[%q] = %q, want %q", tc.args, tc.key, flags[tc.key], tc.val)
		}
		if body != tc.body {
			t.Errorf("%q: body = %q, want %q", tc.args, body, tc.body)
		}
	}
}

// TestLoopTasksFlagsLeadNotTaskText pins the behavioral half of P2:
// leading flags reach the started row while k=v text inside the task
// survives verbatim; trailing flags never leave task text.
func TestLoopTasksFlagsLeadNotTaskText(t *testing.T) {
	rig, _ := loopRig(t, nil, "")
	convID := loopBoot(t, rig)
	rig.call(t, Request{Cmd: CmdSendMessage, ConversationID: convID,
		Text: "/loop tasks rounds=2 1. set log warn=true"})
	// The design goroutine follows the spawn tick; wait it out so the
	// store doesn't close mid-journal.
	sc := waitLoop(t, rig.store, convID, "design stage done", func(sc loopScan) bool {
		return len(sc.rows) >= 2
	})
	started := sc.ofKind(loopKindStarted)
	if len(started) != 1 {
		t.Fatalf("started rows = %d", len(started))
	}
	if got, _ := started[0]["max_rounds"].(float64); int(got) != 2 {
		t.Errorf("max_rounds = %v, want 2 (leading flag parsed)", started[0]["max_rounds"])
	}
	tasks, _ := started[0]["tasks"].([]interface{})
	if len(tasks) != 1 || fmt.Sprint(tasks[0]) != "set log warn=true" {
		t.Errorf("tasks = %v, want [set log warn=true] — leading flags stripped, task text intact", tasks)
	}
}

// TestLoopAuditRequiresAutoApply pins the P2 preflight parity: Mode A
// refuses pre-journal when auto_apply is not main, exactly like Mode B.
func TestLoopAuditRequiresAutoApply(t *testing.T) {
	root := initRepo(t)
	home := t.TempDir()
	t.Setenv("HOME", home)
	writePrefs(t, home, "review: rm1@test\nauto_apply: off\n")
	rig := startRig(t, root)
	t.Cleanup(func() { rig.stop(t) })
	convID := loopBoot(t, rig)
	for _, text := range []string{"/loop audit", "/loop tasks 1. x"} {
		resp := rig.callExpectErr(t, Request{Cmd: CmdSendMessage, ConversationID: convID, Text: text})
		if !strings.Contains(resp.Error, "auto_apply: main") {
			t.Errorf("%q: error = %q, want the auto_apply: main refusal", text, resp.Error)
		}
	}
	if sc := scanLoop(t, rig.store, convID); len(sc.rows) != 0 {
		t.Errorf("preflight refusal must journal no loop rows: %v", sc.kinds())
	}
}

// TestLoopAdjudicateSkipsRejectedDiff pins P2-g (human reject after
// restart_mid_run resolves the task as skipped — never a dead end) and
// P2-d (re-adjudication never double-journals a terminal task row).
func TestLoopAdjudicateSkipsRejectedDiff(t *testing.T) {
	rig, _ := loopRig(t, nil, "")
	convID := loopBoot(t, rig)
	ctx := context.Background()
	ev, err := rig.server.journalLoop(ctx, convID, 0, loopModeTasks, loopKindStarted, map[string]interface{}{
		"base": "abc", "max_rounds": 10, "budget_tokens": 2_000_000, "hold_severity": "P2",
		"tasks": []string{"task one"}, "task_source": "inline",
	}, 0)
	if err != nil {
		t.Fatal(err)
	}
	loopID := int64(ev.Seq)
	if _, err := rig.server.journalLoop(ctx, convID, loopID, loopModeTasks, loopKindTaskSpawn,
		map[string]interface{}{"task": 1}, 0); err != nil {
		t.Fatal(err)
	}
	// The restart_mid_run window: no settle rows after the spawn, then
	// the human REJECTS the orphaned pending diff.
	if _, err := rig.store.AppendEvent(ctx, convID, store.EventReviewAction,
		`{"action":"reject","diff_id":7}`); err != nil {
		t.Fatal(err)
	}
	rig.server.loopAdjudicateTask(ctx, convID, loopID, 1)
	sc := scanLoop(t, rig.store, convID)
	done := sc.ofKind(loopKindTaskDone)
	if len(done) != 1 || fmt.Sprint(done[0]["status"]) != loopTaskSkipped {
		t.Fatalf("done rows = %v, want exactly one skipped row", done)
	}
	if len(sc.ofKind(loopKindSuspended)) != 0 {
		t.Errorf("a human rejection must un-block, not dead-end into restart_mid_run: %v", sc.causes())
	}
	// P2-d: a second adjudication (recovery/resume re-tick) is a no-op.
	rig.server.loopAdjudicateTask(ctx, convID, loopID, 1)
	if done := scanLoop(t, rig.store, convID).ofKind(loopKindTaskDone); len(done) != 1 {
		t.Errorf("re-adjudication double-journaled: done rows = %d, want 1", len(done))
	}
}
