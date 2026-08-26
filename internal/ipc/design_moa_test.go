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
