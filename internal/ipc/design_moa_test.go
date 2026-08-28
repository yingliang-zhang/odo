package ipc

// R-W4 Design-MoA tests: the consolidator plate + wire-exact receipts
// (TestDesignMoaConsolidator), the fail-closed dark-launch flag
// (TestDesignMoaPrefsOff), real leg concurrency (TestDesignMoaParallelLegs),
// and strict truncation in both positions — every leg, and the
// consolidator itself (TestDesignMoaTruncationFailClosed).

import (
	"encoding/json"
	"io"
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

// designMoaCall is one request the stub observed (model, last-turn prompt,
// raw wire body — the receipt reference the tests recompute sha16 over).
type designMoaCall struct {
	model  string
	prompt string
	body   []byte
}

// startDesignMoaStub installs a per-model moa-API stub: answers[model] is
// the end_turn text; truncated[model] pins that model to max_tokens
// forever (unterminated-JSON partial — the shape that must fail closed);
// hold delays every answer to expose leg concurrency. Requests are handled
// CONCURRENTLY by httptest, matching the fanout.
func startDesignMoaStub(t *testing.T, answers map[string]string, truncated map[string]bool, hold time.Duration) (*httptest.Server, func() []designMoaCall, func() int) {
	t.Helper()
	var mu sync.Mutex
	var calls []designMoaCall
	inFlight, maxInFlight := 0, 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		var req struct {
			Model    string `json:"model"`
			Messages []struct {
				Role    string          `json:"role"`
				Content json.RawMessage `json:"content"`
			} `json:"messages"`
		}
		json.Unmarshal(body, &req)
		var prompt string
		if n := len(req.Messages); n > 0 {
			// The initial user turn is plain string content (tool loops
			// never start here — the stub emits no tool_use).
			_ = json.Unmarshal(req.Messages[n-1].Content, &prompt)
		}
		mu.Lock()
		inFlight++
		if inFlight > maxInFlight {
			maxInFlight = inFlight
		}
		calls = append(calls, designMoaCall{model: req.Model, prompt: prompt, body: body})
		mu.Unlock()
		if hold > 0 {
			time.Sleep(hold)
		}
		mu.Lock()
		inFlight--
		mu.Unlock()
		text, stop := answers[req.Model], "end_turn"
		if truncated[req.Model] {
			stop, text = "max_tokens", `{"truncated":[`
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]interface{}{
			"content":     []map[string]string{{"type": "text", "text": text}},
			"stop_reason": stop,
			"usage":       map[string]int{"output_tokens": 321},
		})
	}))
	t.Cleanup(srv.Close)
	return srv,
		func() []designMoaCall { mu.Lock(); defer mu.Unlock(); return append([]designMoaCall(nil), calls...) },
		func() int { mu.Lock(); defer mu.Unlock(); return maxInFlight }
}

// designMoaPrefs is the standard R-W4 prefs line: flag on, three review
// legs, an orchestrator consolidator.
const designMoaPrefs = "design_via: moa\nreview: legA@test, legB@test, legC@test\norchestrator: orch-m3k@test\n"

// designLockEvent scans the conversation journal for the review_action
// {action:"design_lock"} row; absent is nil.
func designLockEvent(t *testing.T, rig *testRig, convID int64) map[string]interface{} {
	t.Helper()
	events := rig.call(t, Request{Cmd: CmdPollEvents, ConversationID: convID, AfterSeq: 0}).Events
	for _, ev := range events {
		if ev.Type != store.EventReviewAction {
			continue
		}
		var p map[string]interface{}
		if json.Unmarshal(ev.Payload, &p) == nil && p["action"] == "design_lock" {
			return p
		}
	}
	return nil
}

// designFailureMarkers counts the memory_update{layer:"design",
// cause:"failed"} rows — the curate-precedent failure trace.
func designFailureMarkers(t *testing.T, rig *testRig, convID int64) int {
	t.Helper()
	events := rig.call(t, Request{Cmd: CmdPollEvents, ConversationID: convID, AfterSeq: 0}).Events
	n := 0
	for _, ev := range events {
		if ev.Type != store.EventMemoryUpdate {
			continue
		}
		var p struct {
			Layer string `json:"layer"`
			Cause string `json:"cause"`
		}
		if json.Unmarshal(ev.Payload, &p) == nil && p.Layer == "design" && p.Cause == "failed" {
			n++
		}
	}
	return n
}

// modelCalls filters the stub's observed requests by model.
func modelCalls(calls []designMoaCall, model string) []designMoaCall {
	var out []designMoaCall
	for _, c := range calls {
		if c.model == model {
			out = append(out, c)
		}
	}
	return out
}

// TestDesignMoaConsolidator: three blind legs propose; ONE orchestrator
// moa.Query synthesizes them; the design_lock row journals the lock plus
// every leg's receipts (R-W1.5 wire-exact, recomputed here over the
// captured bodies). The one-truncated-leg subtest pins the degrade rule:
// the leg drops, its receipts stay, the pipeline proceeds on the rest.
func TestDesignMoaConsolidator(t *testing.T) {
	setup := func(t *testing.T, truncated map[string]bool) (*testRig, int64, func() []designMoaCall, string) {
		root := initRepo(t)
		home := t.TempDir()
		t.Setenv("HOME", home)
		t.Setenv("ODO_OMP_WRAPPER", writeStub(t, stubWrapper))
		srv, calls, _ := startDesignMoaStub(t, map[string]string{
			"legA":     "PROPOSAL FROM LEG A",
			"legB":     "PROPOSAL FROM LEG B",
			"legC":     "PROPOSAL FROM LEG C",
			"orch-m3k": "# DESIGN LOCK\n\nThe consolidated direction.\n",
		}, truncated, 0)
		t.Setenv("MOA_BASE_URL", srv.URL)
		t.Setenv("SUDO_CODING_KEY", "test-key")
		writePrefs(t, home, designMoaPrefs)
		rig := startRig(t, root)
		boot := rig.call(t, Request{Cmd: CmdBootstrap, ProjectRoot: root})
		return rig, boot.Conversation.ID, calls, root
	}
	goal := "Design the consolidation pipeline."

	t.Run("three legs consolidate into a journaled design lock", func(t *testing.T) {
		rig, convID, calls, root := setup(t, nil)
		defer rig.stop(t)
		if err := os.WriteFile(filepath.Join(root, "ctx.md"), []byte("CTX-MARKER context body"), 0o644); err != nil {
			t.Fatal(err)
		}

		resp := rig.call(t, Request{
			Cmd: CmdDesignMoa, ProjectRoot: root, ConversationID: convID,
			Goal: goal, ContextFiles: []string{"ctx.md"},
		})
		if resp.DesignLock != "# DESIGN LOCK\n\nThe consolidated direction.\n" {
			t.Errorf("design_lock = %q, want the orchestrator's answer verbatim", resp.DesignLock)
		}
		if len(resp.DesignProposals) != 3 {
			t.Fatalf("proposals = %d, want 3", len(resp.DesignProposals))
		}

		got := calls()
		labels := []struct{ model, label, text string }{
			{"legA", "legA@test", "PROPOSAL FROM LEG A"},
			{"legB", "legB@test", "PROPOSAL FROM LEG B"},
			{"legC", "legC@test", "PROPOSAL FROM LEG C"},
		}
		for i, want := range labels {
			p := resp.DesignProposals[i]
			if p.Model != want.label || p.Text != want.text || p.Error != "" || p.Truncated {
				t.Errorf("proposal[%d] = %+v, want %s with its answer", i, p, want.label)
			}
			mc := modelCalls(got, want.model)
			if len(mc) != 1 {
				t.Fatalf("%s requests = %d, want 1 (single end_turn round)", want.model, len(mc))
			}
			// R-W1.5 wire-exact: the journaled leg receipt recomputes over
			// the captured wire body.
			if p.RequestSHA16 != sha16(mc[0].body) || p.RequestBytes != len(mc[0].body) {
				t.Errorf("%s receipt = %q/%d, want sha16+len of its wire body", want.model, p.RequestSHA16, p.RequestBytes)
			}
			// Blind + grounded: every leg saw the same goal and the
			// inlined context file.
			if !strings.Contains(mc[0].prompt, goal) || !strings.Contains(mc[0].prompt, "CTX-MARKER context body") {
				t.Errorf("%s prompt missing goal or context: %.200q", want.model, mc[0].prompt)
			}
		}

		// Consolidator: exactly one orchestrator request whose plate
		// carries the goal and all three proposals with stable labels.
		oc := modelCalls(got, "orch-m3k")
		if len(oc) != 1 {
			t.Fatalf("consolidator requests = %d, want 1", len(oc))
		}
		for _, want := range []string{goal, "PROPOSAL FROM LEG A", "PROPOSAL FROM LEG B", "PROPOSAL FROM LEG C", "Leg A (legA@test)", "Leg B (legB@test)", "Leg C (legC@test)"} {
			if !strings.Contains(oc[0].prompt, want) {
				t.Errorf("consolidator prompt missing %q", want)
			}
		}

		// Journal: one design_lock row; goal/design sha16, leg receipts,
		// and the consolidator receipt all recompute wire-exact.
		ev := designLockEvent(t, rig, convID)
		if ev == nil {
			t.Fatal("no design_lock review_action journaled")
		}
		if ev["goal_sha16"] != sha16([]byte(goal)) || ev["design_sha16"] != sha16([]byte(resp.DesignLock)) {
			t.Errorf("sha16 receipts = %v/%v", ev["goal_sha16"], ev["design_sha16"])
		}
		if ev["design_lock"] != resp.DesignLock {
			t.Error("journaled lock != response lock")
		}
		if cf, ok := ev["context_files"].([]interface{}); !ok || len(cf) != 1 || cf[0] != "ctx.md" {
			t.Errorf("context_files = %v", ev["context_files"])
		}
		props, ok := ev["proposals"].([]interface{})
		if !ok || len(props) != 3 {
			t.Fatalf("journaled proposals = %v", ev["proposals"])
		}
		for i, want := range labels {
			pr := props[i].(map[string]interface{})
			if pr["model"] != want.label || pr["text"] != want.text || pr["request_sha16"] != resp.DesignProposals[i].RequestSHA16 {
				t.Errorf("journaled proposal[%d] = %v", i, pr)
			}
		}
		cons, ok := ev["consolidator"].(map[string]interface{})
		if !ok || cons["model"] != "orch-m3k" {
			t.Fatalf("consolidator = %v", ev["consolidator"])
		}
		if cons["request_sha16"] != sha16(oc[0].body) || cons["request_bytes"] != float64(len(oc[0].body)) {
			t.Errorf("consolidator receipt = %v/%v, want wire-exact", cons["request_sha16"], cons["request_bytes"])
		}
		if _, dropped := ev["dropped_legs"]; dropped {
			t.Errorf("dropped_legs present on a clean run: %v", ev["dropped_legs"])
		}
		if designFailureMarkers(t, rig, convID) != 0 {
			t.Error("clean pass journaled a failure marker")
		}
	})

	t.Run("one truncated leg drops; the rest still consolidate", func(t *testing.T) {
		rig, convID, calls, root := setup(t, map[string]bool{"legB": true})
		defer rig.stop(t)

		resp := rig.call(t, Request{Cmd: CmdDesignMoa, ProjectRoot: root, ConversationID: convID, Goal: goal})
		if resp.DesignLock == "" {
			t.Fatal("design_lock empty — pipeline must proceed on the two surviving legs")
		}
		if len(resp.DesignProposals) != 3 {
			t.Fatalf("proposals = %d, want 3 (the failed leg stays as marked metadata)", len(resp.DesignProposals))
		}
		p := resp.DesignProposals[1]
		if p.Model != "legB@test" || !p.Truncated || p.Error == "" || p.Text != "" {
			t.Errorf("truncated leg = %+v, want marked, text dropped", p)
		}
		// The truncated leg keeps the receipt of its FINAL (escalated)
		// wire body — fallback spec escalates 16384 → 32768 then flags.
		bc := modelCalls(calls(), "legB")
		if len(bc) != 2 {
			t.Fatalf("legB requests = %d, want 2 (one escalation re-issue)", len(bc))
		}
		if p.RequestSHA16 != sha16(bc[1].body) || p.RequestBytes != len(bc[1].body) {
			t.Errorf("legB receipt = %q/%d, want sha16+len of the escalated body", p.RequestSHA16, p.RequestBytes)
		}

		oc := modelCalls(calls(), "orch-m3k")
		if len(oc) != 1 {
			t.Fatalf("consolidator requests = %d, want 1", len(oc))
		}
		for _, want := range []string{"PROPOSAL FROM LEG A", "PROPOSAL FROM LEG C"} {
			if !strings.Contains(oc[0].prompt, want) {
				t.Errorf("consolidator prompt missing surviving %q", want)
			}
		}
		// The dropped leg: never on the plate (no partial fragment), but
		// named in the dropped section — an invisible leg would be an
		// invisible vote.
		if strings.Contains(oc[0].prompt, "PROPOSAL FROM LEG B") || strings.Contains(oc[0].prompt, `{"truncated":[`) {
			t.Error("consolidator plate carries the dropped leg's text")
		}
		if !strings.Contains(oc[0].prompt, "legB@test") {
			t.Error("dropped section does not name legB@test")
		}

		ev := designLockEvent(t, rig, convID)
		if ev == nil {
			t.Fatal("no design_lock row — the surviving legs must still lock")
		}
		if ev["dropped_legs"] != float64(1) {
			t.Errorf("dropped_legs = %v, want 1", ev["dropped_legs"])
		}
	})
}

// TestDesignMoaPrefsOff: the dark-launch flag refused closed — absent,
// explicit "omp", or garbled values all error before any moa call.
func TestDesignMoaPrefsOff(t *testing.T) {
	for _, prefs := range []string{"", "design_via: omp\n", "design_via: banana\n"} {
		root := initRepo(t)
		home := t.TempDir()
		t.Setenv("HOME", home)
		t.Setenv("ODO_OMP_WRAPPER", writeStub(t, stubWrapper))
		writePrefs(t, home, prefs+designMoaPrefs[len("design_via: moa\n"):]) // review legs + orchestrator, flag as cased
		rig := startRig(t, root)
		// Single-flight teardown: the flag-refusal loop stops each rig
		// explicitly at iteration end; a fatal abort mid-iteration still
		// gets torn down.
		defer rig.stopOnce(t)
		boot := rig.call(t, Request{Cmd: CmdBootstrap, ProjectRoot: root})

		resp := rig.callExpectErr(t, Request{
			Cmd: CmdDesignMoa, ProjectRoot: root, ConversationID: boot.Conversation.ID, Goal: "g",
		})
		if !strings.Contains(resp.Error, "design_moa requires design_via: moa in prefs") {
			t.Errorf("prefs %q: error = %q, want the flag refusal", prefs, resp.Error)
		}
		rig.stopOnce(t)
	}
}

// TestDesignMoaParallelLegs: three stub-held legs overlap — the max in-flight
// count proves the fanout is concurrent, not serial.
func TestDesignMoaParallelLegs(t *testing.T) {
	root := initRepo(t)
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("ODO_OMP_WRAPPER", writeStub(t, stubWrapper))
	srv, _, maxInFlight := startDesignMoaStub(t, map[string]string{
		"legA":     "A",
		"legB":     "B",
		"legC":     "C",
		"orch-m3k": "LOCK",
	}, nil, 300*time.Millisecond)
	t.Setenv("MOA_BASE_URL", srv.URL)
	t.Setenv("SUDO_CODING_KEY", "test-key")
	writePrefs(t, home, designMoaPrefs)
	rig := startRig(t, root)
	defer rig.stop(t)
	boot := rig.call(t, Request{Cmd: CmdBootstrap, ProjectRoot: root})

	resp := rig.call(t, Request{
		Cmd: CmdDesignMoa, ProjectRoot: root, ConversationID: boot.Conversation.ID, Goal: "g",
	})
	if resp.DesignLock != "LOCK" {
		t.Fatalf("design_lock = %q", resp.DesignLock)
	}
	// A serial fanout would peak at 2 (one leg + the consolidator never
	// overlap); 3 held legs at once is only possible when they ran
	// together.
	if got := maxInFlight(); got != 3 {
		t.Errorf("max in-flight requests = %d, want 3 (legs ran concurrently)", got)
	}
}

// TestDesignMoaTruncationFailClosed: strict truncation in both positions.
// Every leg truncated → the command errors with NO design_lock row and no
// consolidator call. Legs healthy but the consolidator truncated → same,
// after the legs paid out.
func TestDesignMoaTruncationFailClosed(t *testing.T) {
	setup := func(t *testing.T, truncated map[string]bool) (*testRig, int64, func() []designMoaCall, string) {
		root := initRepo(t)
		home := t.TempDir()
		t.Setenv("HOME", home)
		t.Setenv("ODO_OMP_WRAPPER", writeStub(t, stubWrapper))
		srv, calls, _ := startDesignMoaStub(t, map[string]string{
			"legA":     "PROPOSAL A",
			"legB":     "PROPOSAL B",
			"legC":     "PROPOSAL C",
			"orch-m3k": "LOCK",
		}, truncated, 0)
		t.Setenv("MOA_BASE_URL", srv.URL)
		t.Setenv("SUDO_CODING_KEY", "test-key")
		writePrefs(t, home, designMoaPrefs)
		rig := startRig(t, root)
		boot := rig.call(t, Request{Cmd: CmdBootstrap, ProjectRoot: root})
		return rig, boot.Conversation.ID, calls, root
	}

	t.Run("all legs truncated: error, no design lock, no consolidator", func(t *testing.T) {
		rig, convID, calls, root := setup(t, map[string]bool{"legA": true, "legB": true, "legC": true})
		defer rig.stop(t)
		resp := rig.callExpectErr(t, Request{Cmd: CmdDesignMoa, ProjectRoot: root, ConversationID: convID, Goal: "g"})
		if !strings.Contains(resp.Error, "every proposal leg failed") {
			t.Errorf("error = %q, want the all-legs failure", resp.Error)
		}
		if ev := designLockEvent(t, rig, convID); ev != nil {
			t.Errorf("design_lock journaled on a failed pass: %v", ev)
		}
		if designFailureMarkers(t, rig, convID) != 1 {
			t.Error("exactly one failed marker must remain")
		}
		if oc := modelCalls(calls(), "orch-m3k"); len(oc) != 0 {
			t.Errorf("consolidator ran with nothing to consolidate: %d requests", len(oc))
		}
	})

	t.Run("consolidator truncated: error, no design lock", func(t *testing.T) {
		rig, convID, _, root := setup(t, map[string]bool{"orch-m3k": true})
		defer rig.stop(t)
		resp := rig.callExpectErr(t, Request{Cmd: CmdDesignMoa, ProjectRoot: root, ConversationID: convID, Goal: "g"})
		if !strings.Contains(resp.Error, "consolidator") || !strings.Contains(resp.Error, "truncated") {
			t.Errorf("error = %q, want the consolidator truncation", resp.Error)
		}
		if ev := designLockEvent(t, rig, convID); ev != nil {
			t.Errorf("design_lock journaled despite a truncated consolidator: %v", ev)
		}
		if designFailureMarkers(t, rig, convID) != 1 {
			t.Error("failure marker missing for the consolidator truncation")
		}
	})
}

// --- D6 (design-MoA diversity gate) ------------------------------------------------

// d6Mux answers design legs + the consolidator (the fixtures live in
// loop_test.go: kind sniffing keys on the system prompts).
func d6Mux(kind string, n int, model string) (int, string, int) {
	switch kind {
	case "design":
		return 200, "PROPOSAL FROM " + model, 10
	case "consolidator":
		return 200, "# DESIGN LOCK\n\nConsolidated by " + model + ".\n", 10
	}
	return 200, "", 0
}

// d6LoopRig boots a tasks-loop rig whose review line + design gate the
// test controls (loopRig's review line is fixed) with
// loop_design_gate: auto armed.
func d6LoopRig(t *testing.T, reviewLine string, mux loopMux) *testRig {
	t.Helper()
	root := loopRigRepo(t)
	home := t.TempDir()
	t.Setenv("HOME", home)
	writePrefs(t, home, reviewLine+"\nauto_apply: main\norchestrator: orch-m3k@test\nloop_design_gate: auto\n")
	startLoopMuxStub(t, mux)
	ctrlPath := filepath.Join(t.TempDir(), "loop_stub_ctrl")
	setLoopStubAction(t, ctrlPath, "none")
	t.Setenv("LOOP_STUB_CTRL", ctrlPath)
	t.Setenv("ODO_OMP_WRAPPER", writeStub(t, loopWrapper))
	rig := startRig(t, root)
	t.Cleanup(func() { rig.stop(t) })
	return rig
}

// d6Diversity decodes a journaled design_lock row's diversity block.
func d6Diversity(row map[string]interface{}) (legs, fams, eps int, single bool) {
	dv, _ := row["diversity"].(map[string]interface{})
	num := func(k string) int {
		f, _ := dv[k].(float64)
		return int(f)
	}
	return num("legs_successful"), num("distinct_families"), num("distinct_endpoints"), dv["single_endpoint"] == true
}

// d6PendingGate asserts the fold's exact human-gate state: a design lock
// is pending, the task is not spawned/done, and the loop is active.
func d6PendingGate(t *testing.T, sc loopScan) {
	t.Helper()
	st := sc.states[sc.loopID()]
	if st == nil || len(st.tasks) != 1 {
		t.Fatalf("fold = %+v, want one tasks-loop with one task", st)
	}
	task := st.tasks[0]
	if task.designLockSeq == 0 || task.spawned || task.done {
		t.Errorf("fold task = %+v, want a pending human design gate (designLockSeq set, not spawned, not done)", task)
	}
	if st.status != "active" {
		t.Errorf("loop status = %q, want active (a refusal parks, never suspends or skips)", st.status)
	}
}

// TestRunDesignMoaDiversityGate pins the D6 auto gate over the /loop
// tasks path: same model family under two provider labels is ONE
// correlation class (refused); two families pass; a single leg never
// auto-implements.
func TestRunDesignMoaDiversityGate(t *testing.T) {
	// refusedFlow drives the loop to its design row and pins: the
	// auto_gate refusal rides the lock row, the diversity block matches,
	// no implement spawns, and the fold parks at the human gate.
	refusedFlow := func(t *testing.T, reviewLine string, wantLegs, wantFams int, wantFamilies []string) {
		rig := d6LoopRig(t, reviewLine, d6Mux)
		convID := loopBoot(t, rig)
		rig.call(t, Request{Cmd: CmdSendMessage, ConversationID: convID,
			Text: "/loop tasks 1. design the widget"})
		sc := waitLoop(t, rig.store, convID, "design lock journaled", func(sc loopScan) bool {
			return len(sc.ofKind(loopKindDesignLock)) == 1
		})
		lock := sc.ofKind(loopKindDesignLock)[0]
		if lock["auto_gate"] != "refused_diversity" {
			t.Errorf("auto_gate = %v, want refused_diversity", lock["auto_gate"])
		}
		legs, fams, eps, single := d6Diversity(lock)
		if legs != wantLegs || fams != wantFams || eps != 1 || !single {
			t.Errorf("diversity = {%d legs, %d fams, %d eps, single:%v}, want {%d, %d, 1, true}",
				legs, fams, eps, single, wantLegs, wantFams)
		}
		props, _ := lock["proposals"].([]interface{})
		if len(props) != wantLegs {
			t.Fatalf("journaled proposals = %d, want %d", len(props), wantLegs)
		}
		for i, wantFam := range wantFamilies {
			pr, _ := props[i].(map[string]interface{})
			if pr["model_family"] != wantFam {
				t.Errorf("proposal[%d] model_family = %v, want %q", i, pr["model_family"], wantFam)
			}
			if ep, _ := pr["endpoint"].(string); !strings.HasPrefix(ep, "http://127.0.0.1:") {
				t.Errorf("proposal[%d] endpoint = %v, want the scrubbed stub gateway", i, pr["endpoint"])
			}
		}
		d6PendingGate(t, sc)
		loopQuiet(t, rig.store, convID, 500*time.Millisecond, "implement spawned despite the diversity refusal",
			func(sc loopScan) bool { return len(sc.ofKind(loopKindTaskSpawn)) != 0 })
		// Clean teardown: stop the parked loop.
		rig.call(t, Request{Cmd: CmdSendMessage, ConversationID: convID, Text: "/loop stop"})
		waitLoop(t, rig.store, convID, "stopped", func(sc loopScan) bool {
			return len(sc.ofKind(loopKindStopped)) == 1
		})
	}

	t.Run("two legs one family: refused (label diversity is not model diversity)", func(t *testing.T) {
		refusedFlow(t, "review: kimi-k3@t9s, kimi-9x@other", 2, 1, []string{"kimi", "kimi"})
	})

	t.Run("one leg: refused (a single success is no consensus)", func(t *testing.T) {
		refusedFlow(t, "review: kimi-k3@t9s", 1, 1, []string{"kimi"})
	})

	t.Run("two legs two families: admitted and auto-implemented", func(t *testing.T) {
		rig := d6LoopRig(t, "review: kimi-k3@t9s, deepseek-v4-flash@t9s", d6Mux)
		convID := loopBoot(t, rig)
		rig.call(t, Request{Cmd: CmdSendMessage, ConversationID: convID,
			Text: "/loop tasks 1. design the widget"})
		sc := waitLoop(t, rig.store, convID, "implement spawn", func(sc loopScan) bool {
			return len(sc.ofKind(loopKindTaskSpawn)) == 1
		})
		lock := sc.ofKind(loopKindDesignLock)[0]
		if _, refused := lock["auto_gate"]; refused {
			t.Errorf("auto_gate = %v, want NO auto_gate key on an admitted lock", lock["auto_gate"])
		}
		legs, fams, eps, single := d6Diversity(lock)
		if legs != 2 || fams != 2 || eps != 1 || !single {
			t.Errorf("diversity = {%d, %d, %d, single:%v}, want {2, 2, 1, true}", legs, fams, eps, single)
		}
		// The implement run follows the spawn; the IPC poll drives the
		// drain (ctrl none → no diff → fix_no_diff suspension) so
		// teardown never closes the store mid-journal.
		pollDone(t, rig, convID)
		waitLoop(t, rig.store, convID, "post-drain adjudication", func(sc loopScan) bool {
			return len(sc.causes()) == 1 && sc.causes()[0] == "fix_no_diff"
		})
	})
}

// TestAutoGateFallsBackToHuman pins the refusal's end-to-end contract:
// the parked state is EXACTLY the loop_design_gate: human state (the
// human gate consumes it via loop_ctl approve_design and only then does
// the implement run spawn), and the manual /design_moa handler is
// unchanged — one leg still emits a lock, diversity block journaled for
// visibility only.
func TestAutoGateFallsBackToHuman(t *testing.T) {
	t.Run("refused diversity: pending gate consumed by the human gate", func(t *testing.T) {
		rig := d6LoopRig(t, "review: kimi-k3@t9s", d6Mux)
		convID := loopBoot(t, rig)
		rig.call(t, Request{Cmd: CmdSendMessage, ConversationID: convID,
			Text: "/loop tasks 1. design the widget"})
		sc := waitLoop(t, rig.store, convID, "design lock journaled", func(sc loopScan) bool {
			return len(sc.ofKind(loopKindDesignLock)) == 1
		})
		if lock := sc.ofKind(loopKindDesignLock)[0]; lock["auto_gate"] != "refused_diversity" {
			t.Fatalf("auto_gate = %v, want refused_diversity", lock["auto_gate"])
		}
		d6PendingGate(t, sc)
		if len(sc.ofKind(loopKindTaskSpawn)) != 0 {
			t.Fatal("implement spawned despite the refusal")
		}

		// The human gate (loop_ctl approve_design) consumes exactly the
		// pending designLockSeq — the state must be indistinguishable
		// from loop_design_gate: human.
		rig.call(t, Request{Cmd: CmdLoopCtl, ConversationID: convID, Action: "approve_design"})
		sc = waitLoop(t, rig.store, convID, "implement spawn after approval", func(sc loopScan) bool {
			return len(sc.ofKind(loopKindTaskSpawn)) == 1
		})
		st := sc.states[sc.loopID()]
		if st == nil || len(st.tasks) != 1 || !st.tasks[0].spawned || st.tasks[0].designLockSeq != 0 {
			t.Errorf("post-approval fold = %+v, want spawned with the gate cleared", st)
		}
		// The IPC poll drives the drain (ctrl none → no diff →
		// fix_no_diff) so teardown never closes the store mid-journal.
		pollDone(t, rig, convID)
		waitLoop(t, rig.store, convID, "post-drain adjudication", func(sc loopScan) bool {
			return len(sc.causes()) == 1 && sc.causes()[0] == "fix_no_diff"
		})
	})

	t.Run("manual design_moa with one leg still locks (visibility only)", func(t *testing.T) {
		root := initRepo(t)
		home := t.TempDir()
		t.Setenv("HOME", home)
		t.Setenv("ODO_OMP_WRAPPER", writeStub(t, stubWrapper))
		srv, _, _ := startDesignMoaStub(t, map[string]string{
			"kimi-k3":  "PROPOSAL FROM THE LONE LEG",
			"orch-m3k": "# DESIGN LOCK\n\nThe lone leg's design.\n",
		}, nil, 0)
		t.Setenv("MOA_BASE_URL", srv.URL)
		t.Setenv("SUDO_CODING_KEY", "test-key")
		writePrefs(t, home, "design_via: moa\nreview: kimi-k3@t9s\norchestrator: orch-m3k@test\n")
		rig := startRig(t, root)
		defer rig.stop(t)
		boot := rig.call(t, Request{Cmd: CmdBootstrap, ProjectRoot: root})

		resp := rig.call(t, Request{
			Cmd: CmdDesignMoa, ProjectRoot: root, ConversationID: boot.Conversation.ID, Goal: "Design it.",
		})
		if resp.DesignLock != "# DESIGN LOCK\n\nThe lone leg's design.\n" {
			t.Fatalf("manual path must still lock on one leg; design_lock = %q", resp.DesignLock)
		}
		if len(resp.DesignProposals) != 1 {
			t.Fatalf("proposals = %d, want 1", len(resp.DesignProposals))
		}
		p := resp.DesignProposals[0]
		if p.ModelFamily != "kimi" || p.Endpoint != srv.URL {
			t.Errorf("proposal = family %q endpoint %q, want kimi @ %s", p.ModelFamily, p.Endpoint, srv.URL)
		}
		ev := designLockEvent(t, rig, boot.Conversation.ID)
		if ev == nil {
			t.Fatal("no design_lock row journaled")
		}
		legs, fams, eps, single := d6Diversity(ev)
		if legs != 1 || fams != 1 || eps != 1 || !single {
			t.Errorf("diversity = {%d, %d, %d, single:%v}, want {1, 1, 1, true}", legs, fams, eps, single)
		}
		if _, gated := ev["auto_gate"]; gated {
			t.Errorf("auto_gate = %v present on the manual path — the gate only exists under loop_design_gate: auto", ev["auto_gate"])
		}
	})
}
