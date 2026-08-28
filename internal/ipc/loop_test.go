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
	"net"
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
//	tier0 — add internal/ipc/gatepolicy.go (D1 gate core gate)
//	tier1 — add internal/ipc/newgate.go (D1 Tier-1 panel-everywhere gate)
//	slow — sleep 5s (a mid-flight run a human send can interrupt)
//	none — produce no diff
//
// D3: $LOOP_STUB_USAGE_CTRL (a file path) arms the measured-cost
// receipt — when non-empty, the wrapper copies the file's JSONL content
// into the run's --session-dir as session.jsonl and TRUNCATES the ctrl
// (one-shot: exactly the next run gets a usage transcript; omit or
// leave empty for the usage_available:false fail-soft path).
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
  tier0) mkdir -p internal/ipc && printf 'package ipc\n\n// tier0 probe\n' > internal/ipc/gatepolicy.go ;;
  tier1) mkdir -p internal/ipc && printf 'package ipc\n\n// tier1 probe\n' > internal/ipc/newgate.go ;;
  slow) sleep 5 ;;
  none) : ;;
esac
usage_ctrl="${LOOP_STUB_USAGE_CTRL:-}"
if [ -n "$usage_ctrl" ] && [ -s "$usage_ctrl" ]; then
  sdir=""; prev=""
  for a in "$@"; do
    if [ "$prev" = "--session-dir" ]; then sdir="$a"; fi
    prev="$a"
  done
  if [ -n "$sdir" ]; then
    cp "$usage_ctrl" "$sdir/session.jsonl" && : > "$usage_ctrl"
  fi
fi
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

// TestFindingFingerprintV4 pins D5's identity change: file+symbol+category
// (+ optional rule) is the fingerprint; title/evidence wording is mutable
// description and must never fork a phantom finding.
func TestFindingFingerprintV4(t *testing.T) {
	base := finding{Severity: "P2", File: "a.go", Symbol: "f", Title: "races the resume", Category: "contract"}
	fp := findingFingerprint(base)
	// Same file/symbol/cat, different title wording ⇒ same FP (V3 hashed
	// the title; V4 must not).
	reworded := base
	reworded.Title = "completely different wording about the same race"
	if got := findingFingerprint(reworded); got != fp {
		t.Errorf("title rewording forked the FP: %s vs %s", got, fp)
	}
	// Severity never enters identity either (unchanged from V3).
	resev := base
	resev.Severity = "P0"
	if got := findingFingerprint(resev); got != fp {
		t.Errorf("severity forked the FP: %s vs %s", got, fp)
	}
	// Category IS identity: a different category splits the finding.
	diffcat := base
	diffcat.Category = "security"
	if got := findingFingerprint(diffcat); got == fp {
		t.Error("different category must fork the FP")
	}
	// Unknown/absent category normalizes to other.
	if got := findingFingerprint(finding{File: "a.go", Symbol: "f", Category: "madeup"}); got != findingFingerprint(finding{File: "a.go", Symbol: "f", Category: "other"}) {
		t.Error("unknown category must fold to other")
	}
	// The optional rule splits sightings when cited.
	ruled := base
	ruled.Rule = "C6"
	if got := findingFingerprint(ruled); got == fp {
		t.Error("cited rule must fork the FP")
	}
	// Rule whitespace edges don't fork.
	ruledLoose := base
	ruledLoose.Rule = " C6 "
	if got := findingFingerprint(ruledLoose); got != findingFingerprint(ruled) {
		t.Error("rule padding must not fork the FP")
	}
}

// TestUnionPerLegDedup pins D5's counting change: each leg is deduped by
// FP before leg support is counted (same-leg re-citations can't inflate
// Legs), and leg_ids names the supporting fan-out positions.
func TestUnionPerLegDedup(t *testing.T) {
	mk := func(sev, title string) finding {
		f := finding{Severity: sev, File: "a.go", Symbol: "f", Title: title, Category: "contract"}
		f.FP = findingFingerprint(f)
		return f
	}
	// One leg cites the same FP twice at different severities ⇒ ONE
	// supporter, kept at its most severe sighting.
	oneLeg := unionFindings([][]finding{{mk("P3", "mild wording"), mk("P2", "severe wording")}})
	if len(oneLeg) != 1 {
		t.Fatalf("same-leg duplicate escaped the union: n=%d want 1", len(oneLeg))
	}
	if oneLeg[0].Legs != 1 || fmt.Sprint(oneLeg[0].LegIDs) != "[0]" {
		t.Errorf("same-leg support = legs %d ids %v, want 1 leg [0]", oneLeg[0].Legs, oneLeg[0].LegIDs)
	}
	if oneLeg[0].Severity != "P2" || oneLeg[0].Title != "severe wording" {
		t.Errorf("per-leg dedup must keep the most severe sighting: %+v", oneLeg[0])
	}
	// Two legs ⇒ Legs=2 with both positions; max severity across legs wins.
	twoLegs := unionFindings([][]finding{{mk("P3", "mild wording")}, {mk("P1", "worst wording")}})
	if len(twoLegs) != 1 || twoLegs[0].Legs != 2 || fmt.Sprint(twoLegs[0].LegIDs) != "[0 1]" {
		t.Fatalf("two-leg support = %+v, want 1 finding legs 2 ids [0 1]", twoLegs[0])
	}
	if twoLegs[0].Severity != "P1" || twoLegs[0].Title != "worst wording" {
		t.Errorf("cross-leg max-wins: %+v", twoLegs[0])
	}
}

// TestParseBackwardCompat pins the mixed-window contract: old 4-field
// rows parse with cat=other, and the new optional cat/rule fields parse
// and normalize additively.
func TestParseBackwardCompat(t *testing.T) {
	old := "- sev: P2 | file: internal/ipc/loop.go | symbol: tickLoop | title: budget check races resume"
	f, ok := parseFindingsBlock(auditFindings(old))
	if !ok || len(f) != 1 {
		t.Fatalf("old row: ok=%v n=%d", ok, len(f))
	}
	if f[0].Category != "other" || f[0].Rule != "" {
		t.Errorf("old 4-field row must parse cat=other, no rule: %+v", f[0])
	}
	// New shape: cat + rule + status all ride.
	f, ok = parseFindingsBlock(auditFindings(
		"- sev: P1 | file: b.go | symbol: gate | cat: security | title: policy bypass | rule: SEC-7 | status: still_open",
	))
	if !ok || len(f) != 1 {
		t.Fatalf("new row: ok=%v n=%d", ok, len(f))
	}
	got := f[0]
	if got.Category != "security" || got.Rule != "SEC-7" || got.Status != "still_open" || got.Title != "policy bypass" {
		t.Errorf("new-shape parse: %+v", got)
	}
	// Unknown category folds to other; resolved closure still drops the row.
	f, ok = parseFindingsBlock(auditFindings(
		"- sev: P3 | file: c.go | symbol: nit | cat: madeup | title: x | status: resolved",
	))
	if !ok || len(f) != 0 {
		t.Errorf("resolved-with-cat must drop: ok=%v n=%d", ok, len(f))
	}
	f, ok = parseFindingsBlock(auditFindings(
		"- sev: P3 | file: c.go | symbol: nit | cat: madeup | title: x",
	))
	if !ok || len(f) != 1 || f[0].Category != "other" {
		t.Errorf("unknown cat must fold to other: ok=%v %+v", ok, f)
	}
}

// TestUpgradeBoundaryNoFalseStall pins the v3→v4 migration guard: round
// 1's blocking_fps are v3 strings, round 2's are v4 strings for the SAME
// logical findings after a landed fix. The changed FP set must read as
// new findings for one round — never as an unchanged-set stall.
func TestUpgradeBoundaryNoFalseStall(t *testing.T) {
	st := &loopState{
		rounds: []loopRound{
			{seq: 2, round: 1, subjectSHA16: "s1", blockingFPS: []string{"v3fp-of-finding-a"}},
			{seq: 4, round: 2, subjectSHA16: "s2"},
		},
		boundDiffs: map[int64]bool{9: true},
	}
	events := []store.Event{
		{Seq: 3, Type: store.EventReviewAction, Payload: json.RawMessage(`{"action":"accept","actor":"auto_loop","diff_id":9}`)},
	}
	stall, why := (&Server{}).loopStallCheck(events, st, "s2", []string{"v4fp-of-finding-a"}, 5)
	if stall {
		t.Errorf("boundary FP change must not stall: %q", why)
	}
	// The armed comparator still works when the set truly is unchanged
	// (the upgrade only moves FPs, it does not disarm C5).
	prevV4 := append([]loopRound(nil), st.rounds...)
	prevV4[0].blockingFPS = []string{"v4fp-of-finding-a"}
	stall, _ = (&Server{}).loopStallCheck(events, &loopState{rounds: prevV4, boundDiffs: st.boundDiffs}, "s2", []string{"v4fp-of-finding-a"}, 5)
	if !stall {
		t.Error("unchanged blocking set across a landed fix must still stall (C5 intact)")
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
		mk(6, `{"kind":"loop_diff_bound","loop_id":1,"round":1,"diff_id":9,"spent_tokens":200}`),
		rev(7, `{"action":"accept","actor":"auto_loop","diff_id":9}`),
		mk(8, `{"kind":"loop_suspended","loop_id":1,"cause":"human_interleave","spent_tokens":200}`),
		mk(9, `{"kind":"loop_resumed","loop_id":1,"cause":"human_interleave","spent_tokens":200}`),
		mk(10, `{"kind":"loop_audit_round","loop_id":1,"round":2,"subject_sha16":"s2","legs":[{"model":"m","verdict":"complete"}],"spent_tokens":300}`),
		mk(11, `{"kind":"loop_verdict","loop_id":1,"round":2,"verdict":"clean","blocking_fps":[],"spent_tokens":300}`),
		mk(12, `{"kind":"loop_completed","loop_id":1,"rounds":2,"spent_tokens":300}`),
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

// TestFoldUsageCoversEstimate pins D3's estPending rule (the double-count
// killer): a spawn row's prompt_tokens_est stays in the cumulative ONLY
// until its covering loop_run_usage receipt lands — then spent = the
// MEASURED input+output+cacheWrite, never est+usage. cache_read rides
// the receipt journaled but is never budgeted. The writer stamps the
// same cumulative the fold derives (C1: fold is the truth).
func TestFoldUsageCoversEstimate(t *testing.T) {
	mk := func(seq int, payload string) store.Event {
		return store.Event{Seq: seq, Type: store.EventLoopEvent, Payload: json.RawMessage(payload)}
	}
	st := deriveLoopStates([]store.Event{
		mk(1, `{"kind":"loop_started","mode":"audit","base":"abc","max_rounds":10,"budget_tokens":2000000,"spent_tokens":0}`),
		mk(4, `{"kind":"loop_fix_spawn","loop_id":1,"round":1,"prompt_tokens_est":1000,"spent_tokens":1000}`),
		// Writer-side stamp: 1000 - 1000 + (4000+100+100) = 4200.
		mk(5, `{"kind":"loop_run_usage","loop_id":1,"round":1,"run_id":"r1","covers_spawn_seq":4,"usage_available":true,"input_tokens":4000,"output_tokens":100,"cache_read_tokens":9000,"cache_write_tokens":100,"cost_usd":0.05,"spent_tokens":4200}`),
	})[0]
	if st.spentTokens != 4200 {
		t.Errorf("covered estimate: spent = %d, want 4200 (not 5200 — the estimate must be RETIRED)", st.spentTokens)
	}
	if len(st.estPending) != 0 {
		t.Errorf("estPending = %v, want empty (the receipt covered it)", st.estPending)
	}

	// covers_spawn_seq 0 (unknown): the fold resolves the spawn by its
	// round key (task key for Mode B) — same replacement.
	st = deriveLoopStates([]store.Event{
		mk(1, `{"kind":"loop_started","mode":"audit","base":"abc","max_rounds":10,"budget_tokens":2000000,"spent_tokens":0}`),
		mk(4, `{"kind":"loop_fix_spawn","loop_id":1,"round":1,"prompt_tokens_est":1000,"spent_tokens":1000}`),
		mk(5, `{"kind":"loop_run_usage","loop_id":1,"round":1,"run_id":"r1","covers_spawn_seq":0,"usage_available":true,"input_tokens":4000,"output_tokens":100,"cache_write_tokens":100,"spent_tokens":4200}`),
	})[0]
	if st.spentTokens != 4200 {
		t.Errorf("round-fallback match: spent = %d, want 4200", st.spentTokens)
	}

	// usage_available:false (fail-soft): nothing folds, the estimate
	// stays pending ("usage pending" — a crash before the tail looks the
	// same).
	st = deriveLoopStates([]store.Event{
		mk(1, `{"kind":"loop_started","mode":"audit","base":"abc","max_rounds":10,"budget_tokens":2000000,"spent_tokens":0}`),
		mk(4, `{"kind":"loop_fix_spawn","loop_id":1,"round":1,"prompt_tokens_est":1000,"spent_tokens":1000}`),
		mk(5, `{"kind":"loop_run_usage","loop_id":1,"round":1,"run_id":"r1","covers_spawn_seq":4,"usage_available":false,"reason":"no session transcript","spent_tokens":1000}`),
	})[0]
	if st.spentTokens != 1000 || st.estPending[4] != 1000 {
		t.Errorf("fail-soft row: spent = %d pending = %v, want 1000 + pending estimate", st.spentTokens, st.estPending)
	}
}

// TestUsageRowIdempotent pins the duplicate-receipt rule: two usage rows
// covering the same spawn fold newest-wins — a journal re-fold (the
// bootstrap replay) yields the identical cumulative, not usage×2.
func TestUsageRowIdempotent(t *testing.T) {
	mk := func(seq int, payload string) store.Event {
		return store.Event{Seq: seq, Type: store.EventLoopEvent, Payload: json.RawMessage(payload)}
	}
	const usageRow = `{"kind":"loop_run_usage","loop_id":1,"round":1,"run_id":"r1","covers_spawn_seq":4,"usage_available":true,"input_tokens":4000,"output_tokens":100,"cache_write_tokens":100,"cost_usd":0.05,"spent_tokens":4200}`
	events := []store.Event{
		mk(1, `{"kind":"loop_started","mode":"audit","base":"abc","max_rounds":10,"budget_tokens":2000000,"spent_tokens":0}`),
		mk(4, `{"kind":"loop_fix_spawn","loop_id":1,"round":1,"prompt_tokens_est":1000,"spent_tokens":1000}`),
		mk(5, usageRow),
	}
	single := deriveLoopStates(events)[0].spentTokens
	double := deriveLoopStates(append(events, mk(6, usageRow)))[0].spentTokens
	if single != 4200 || double != single {
		t.Errorf("duplicate receipts: single = %d, double = %d, want equal 4200", single, double)
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

// subjectBlob renders a Go comment blob of at least size bytes — gate-clean
// ASCII whose only effect is crossing git-diff byte thresholds (the audit
// subject is base..HEAD's diff text, so each line costs one extra "+").
func subjectBlob(size int) []byte {
	line := "// " + strings.Repeat("x", 96) + "\n" // 100 B/line
	var b strings.Builder
	b.WriteString("package src\n\n")
	for b.Len() < size {
		b.WriteString(line)
	}
	return []byte(b.String())
}

// commitSubject commits a blob-sized work commit over the frozen base and
// starts the audit loop on it.
func commitSubject(t *testing.T, rig *testRig, convID int64, size int) {
	t.Helper()
	base := gitOut(t, rig.root, "rev-parse", "HEAD")
	if err := os.WriteFile(filepath.Join(rig.root, "src", "blob.go"), subjectBlob(size), 0o644); err != nil {
		t.Fatal(err)
	}
	gitIn(t, rig.root, "add", ".")
	gitIn(t, rig.root, "commit", "-m", "sized subject")
	rig.call(t, Request{Cmd: CmdSendMessage, ConversationID: convID, Text: "/loop audit base=" + base})
}

// TestLoopAuditSubjectCapAdmits200KB pins the v1.2 loop-owned cap: a ~192KB
// frozen subject (clear of the old 64KB settle cap, under the 256KB loop
// cap) is AUDITED end to end, not suspended — the round row journals
// subject_bytes inside the admission window.
func TestLoopAuditSubjectCapAdmits200KB(t *testing.T) {
	rig, _ := loopRig(t, func(kind string, n int, model string) (int, string, int) {
		if kind == "audit" {
			return 200, auditFindings(), 10 // clean leg: fenced block, zero rows
		}
		return 200, "", 0
	}, "")
	convID := loopBoot(t, rig)
	commitSubject(t, rig, convID, 190*1024)
	sc := waitLoop(t, rig.store, convID, "clean completion", func(sc loopScan) bool {
		return len(sc.ofKind(loopKindCompleted)) == 1
	})
	for _, c := range sc.causes() {
		if c == "subject_too_large" {
			t.Fatalf("~192KB subject must audit under the 256KB cap: causes=%v", sc.causes())
		}
	}
	rounds := sc.ofKind(loopKindAuditRound)
	if len(rounds) != 1 {
		t.Fatalf("round rows: %d, want 1", len(rounds))
	}
	sb, _ := rounds[0]["subject_bytes"].(float64)
	if sb <= float64(settleDiffCapBytes) || sb > float64(loopAuditSubjectCapBytes) {
		t.Errorf("subject_bytes = %v, want (%d, %d]", sb, settleDiffCapBytes, loopAuditSubjectCapBytes)
	}
}

// TestLoopAuditSubjectCapSuspends500KB pins the hard wall: a ~532KB frozen
// subject suspends subject_too_large before any leg fires, and the detail
// names the loop cap (262144).
func TestLoopAuditSubjectCapSuspends500KB(t *testing.T) {
	rig, _ := loopRig(t, nil, "")
	convID := loopBoot(t, rig)
	commitSubject(t, rig, convID, 520*1024)
	sc := waitLoop(t, rig.store, convID, "subject_too_large suspend", func(sc loopScan) bool {
		return len(sc.causes()) == 1 && sc.causes()[0] == "subject_too_large"
	})
	if got := len(sc.ofKind(loopKindAuditRound)); got != 0 {
		t.Errorf("breaker precedes fanout: round rows = %d, want 0", got)
	}
	susp := sc.ofKind(loopKindSuspended)
	if d := fmt.Sprint(susp[0]["detail"]); !strings.Contains(d, "262144") {
		t.Errorf("detail must name the loop cap: %q", d)
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
			return 200, "ACCEPT\nlooks correct", 10
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
	// Two journaled auto_panel lands (D1: loop fixes ride the full panel
	// path — auto_loop never lands a fix itself); fix prompts journaled
	// as marked user_messages with round identity.
	if sc.acceptsWithActor(autoActor) != 2 {
		t.Errorf("auto_panel accepts = %d, want 2", sc.acceptsWithActor(autoActor))
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

	// D3: per-leg/proposal/consolidator request_bytes/4 input estimates
	// fold beside the output receipts (floored per entry).
	if got := loopRowSpend(map[string]interface{}{"legs": []auditLegResult{
		{Model: "rm1@test", OutputTokens: 100, RequestBytes: 400},
		{Model: "rm2@test", OutputTokens: 54, RequestBytes: 8},
	}}); got != 256 { // 100+400/4 + 54+8/4
		t.Errorf("legs with request estimates: spend = %d, want 256", got)
	}
	if got := loopRowSpend(map[string]interface{}{
		"proposals":    []DesignProposal{{Model: "rm1@test", OutputTokens: 10, RequestBytes: 40}},
		"consolidator": map[string]interface{}{"output_tokens": 5, "request_bytes": 16},
	}); got != 29 { // 10+40/4 + 5+16/4
		t.Errorf("design row with request estimates: spend = %d, want 29", got)
	}

	// D3 usage receipt: measured input+output+cacheWrite folds (output
	// counted by the shared top-level reader — no double count);
	// cache_read is journaled but NEVER budgeted; a usage_available:false
	// fail-soft row contributes nothing.
	if got := loopRowSpend(map[string]interface{}{
		"kind": "loop_run_usage", "usage_available": true,
		"input_tokens": 4100, "output_tokens": 100, "cache_write_tokens": 100, "cache_read_tokens": 999,
	}); got != 4300 {
		t.Errorf("usage receipt: spend = %d, want 4300 (cacheRead excluded)", got)
	}
	if got := loopRowSpend(map[string]interface{}{
		"kind": "loop_run_usage", "usage_available": false, "reason": "no session transcript",
	}); got != 0 {
		t.Errorf("fail-soft usage row: spend = %d, want 0", got)
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
		name          string
		action        string
		wantCause     string
		wantDetailSub []string
	}{
		// wiki/ is memory content: since 2026-08-24 the run's diff is
		// refused at REGISTRATION (no diff row ever exists) and the loop
		// suspends run_tainted with the refusal reason — the legacy
		// risk:protected_path wedge advised "land manually", a dead end
		// the executor's every-actor refusal made impossible.
		{"protected memory path", "protect", "run_tainted", []string{"refused at registration", "wiki/x.md"}},
		{"supply chain", "supply", "risk:supply_chain", nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var ctrl string
			rig, ret := loopRig(t, func(kind string, n int, model string) (int, string, int) {
				switch kind {
				case "audit":
					_ = os.WriteFile(ctrl, []byte(tc.action), 0o644)
					return 200, auditFindings("- sev: P2 | file: src/a.go | symbol: a | title: missing guard"), 10
				case "review":
					return 200, "ACCEPT\nlooks correct", 10
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
			susp := sc.ofKind(loopKindSuspended)
			if len(susp) != 1 {
				t.Fatalf("suspended rows = %d, want 1", len(susp))
			}
			for _, sub := range tc.wantDetailSub {
				if d := fmt.Sprint(susp[0]["detail"]); !strings.Contains(d, sub) {
					t.Errorf("suspension detail = %q, want substring %q", d, sub)
				}
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
			return 200, "ACCEPT\nlooks correct", 10
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
	if sc.acceptsWithActor(autoActor) != 1 {
		t.Errorf("accepts = %d, want 1 (round 1's fix landed via the panel; the stall ride has no fix)", sc.acceptsWithActor(autoActor))
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
			return 200, "ACCEPT\nlooks correct", 10
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
	// ate every leg's receipt. D3 adds each leg's request_bytes/4 input
	// estimate on top (stub-body-dependent, so asserted as a floor here;
	// exact per-leg math lives in TestLoopRowSpendConcreteSlices).
	rounds := sc.ofKind(loopKindAuditRound)
	if len(rounds) != 1 {
		t.Fatalf("audit rounds = %d, want 1", len(rounds))
	}
	if got, _ := rounds[0]["spent_tokens"].(float64); int(got) < 3*legTokens {
		t.Errorf("round spent_tokens = %v, want >= %d (Σ leg output_tokens + request estimates)", rounds[0]["spent_tokens"], 3*legTokens)
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

// TestBudgetUsesExecutorSpend pins D3's enforcement end to end: the
// drain-side usage receipt (measured 90k, replacing the spawn's prompt
// estimate) on top of the panel's 30k crosses loop_budget_tokens — the
// loop suspends loop_budget_exceeded BEFORE autoLand spends verify+panel
// (fail-closed; zero moa_review/accept rows for the fix diff), and the
// journaled cumulative, the fold, and the receipt stamp all agree (C1).
func TestBudgetUsesExecutorSpend(t *testing.T) {
	// The stub wrapper plants this transcript into the fix run's session
	// dir (one-shot): spent = 89000+500+500 = 90000; cacheRead journaled
	// but never budgeted; cost rounds to 6dp (1.234568).
	transcript := `{"type":"message","message":{"role":"assistant","content":[{"type":"text","text":"fix"}],"usage":{"input":89000,"output":500,"cacheRead":7000,"cacheWrite":500,"totalTokens":97500,"cost":{"input":1.0,"output":0.2,"cacheRead":0.03,"cacheWrite":0.004567,"total":1.234567891}}}}` + "\n"
	usageCtrl := filepath.Join(t.TempDir(), "usage_ctrl")
	if err := os.WriteFile(usageCtrl, []byte(transcript), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("LOOP_STUB_USAGE_CTRL", usageCtrl)

	var ctrl string
	rig, ret := loopRig(t, func(kind string, n int, model string) (int, string, int) {
		switch kind {
		case "audit":
			_ = os.WriteFile(ctrl, []byte("fix1"), 0o644)                                                      // arm the fix run's diff action
			return 200, auditFindings("- sev: P2 | file: src/a.go | symbol: a | title: missing guard"), 10_000 // panel 3×10k = 30k
		case "review":
			return 200, "ACCEPT\nlooks correct", 10 // must NEVER be reached
		}
		return 200, "", 0
	}, "loop_budget_tokens: 100000\n")
	ctrl = ret
	convID := loopBoot(t, rig)
	base := gitOut(t, rig.root, "rev-parse", "HEAD")
	gitIn(t, rig.root, "commit", "--allow-empty", "-m", "work")
	rig.call(t, Request{Cmd: CmdSendMessage, ConversationID: convID, Text: "/loop audit base=" + base})

	// The fix run spawns off round 1's blocking finding; polls drive its
	// drain (the loop's terminal tail runs there).
	waitLoop(t, rig.store, convID, "fix spawn", func(sc loopScan) bool {
		return len(sc.ofKind(loopKindFixSpawn)) == 1
	})
	pollDone(t, rig, convID)
	sc := waitLoop(t, rig.store, convID, "budget breaker at drain", func(sc loopScan) bool {
		return len(sc.ofKind(loopKindBudgetExceeded)) == 1
	})
	// The trip happened at DRAIN, not spawn time: the fix run exists
	// (spawn admitted — est projection passed) and its usage receipt was
	// journaled FIRST in the same tail.
	spawns := sc.ofKind(loopKindFixSpawn)
	if len(spawns) != 1 {
		t.Fatalf("fix spawns = %d, want 1 (drain-time trip, not the spawn-time breaker)", len(spawns))
	}
	usageRows := sc.ofKind(loopKindRunUsage)
	if len(usageRows) != 1 {
		t.Fatalf("usage receipts = %d, want 1", len(usageRows))
	}
	u := usageRows[0]
	if u["usage_available"] != true || fmt.Sprint(u["kind_run"]) != "fix" {
		t.Errorf("receipt availability/kind_run = %v/%v", u["usage_available"], u["kind_run"])
	}
	for k, want := range map[string]float64{"input_tokens": 89000, "output_tokens": 500, "cache_write_tokens": 500, "cache_read_tokens": 7000} {
		if got, _ := u[k].(float64); got != want {
			t.Errorf("receipt %s = %v, want %v", k, u[k], want)
		}
	}
	if got, _ := u["cost_usd"].(float64); got != 1.234568 {
		t.Errorf("receipt cost_usd = %v, want 1.234568 (6dp)", u["cost_usd"])
	}
	if got, _ := u["covers_spawn_seq"].(float64); got <= 0 {
		t.Errorf("covers_spawn_seq = %v, want > 0 (pinned to the spawn row)", u["covers_spawn_seq"])
	}
	// C1 triple agreement: journaled receipt stamp == budget row's
	// projected == the fold's derived cumulative == spawn−est+measured.
	spawnSpent, _ := spawns[0]["spent_tokens"].(float64)
	spawnEst, _ := spawns[0]["prompt_tokens_est"].(float64)
	budgetRow := sc.ofKind(loopKindBudgetExceeded)[0]
	projected, _ := budgetRow["projected"].(float64)
	stamp, _ := u["spent_tokens"].(float64)
	folded := float64(sc.states[sc.loopID()].spentTokens)
	if want := spawnSpent - spawnEst + 90000; stamp != want || projected != want || folded != want {
		t.Errorf("C1 agreement: stamp=%v projected=%v fold=%v, want %v (spawn %v − est %v + measured 90000)",
			stamp, projected, folded, want, spawnSpent, spawnEst)
	}
	if st := sc.states[sc.loopID()]; st.status != "suspended" || st.cause != "budget_exceeded" {
		t.Errorf("fold = status %q cause %q, want suspended/budget_exceeded", st.status, st.cause)
	}
	// autoLand never entered: no panel, no land spend, no accept — the
	// fix diff sits pending for the human / a raised-budget resume.
	for _, r := range sc.review {
		if a := fmt.Sprint(r["action"]); a == "moa_review" || a == "auto_land_started" {
			t.Errorf("pipeline spend row %v present though the loop suspended before autoLand", a)
		}
	}
	if sc.acceptsWithActor(autoActor) != 0 {
		t.Errorf("accepts = %d, want 0 (nothing landed)", sc.acceptsWithActor(autoActor))
	}
	if got := sc.blockedReasonsLoop(); len(got) != 0 {
		t.Errorf("blocked rows = %v, want none (the loop suspend row carries the fact)", got)
	}
	// Journal-first ordering: the receipt precedes the breaker's row.
	kinds := sc.kinds()
	usageIdx, budgetIdx := -1, -1
	for i, k := range kinds {
		switch k {
		case loopKindRunUsage:
			if usageIdx < 0 {
				usageIdx = i
			}
		case loopKindBudgetExceeded:
			if budgetIdx < 0 {
				budgetIdx = i
			}
		}
	}
	if usageIdx < 0 || budgetIdx < 0 || usageIdx > budgetIdx {
		t.Errorf("row order: usage at %d, budget at %d — receipt must land first", usageIdx, budgetIdx)
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
	defer rig1.stopOnce(t)
	convID := loopBoot(t, rig1)
	base := gitOut(t, root, "rev-parse", "HEAD")
	gitIn(t, root, "commit", "--allow-empty", "-m", "work")
	rig1.call(t, Request{Cmd: CmdSendMessage, ConversationID: convID, Text: "/loop audit base=" + base})
	waitLoop(t, rig1.store, convID, "fix spawn", func(sc loopScan) bool {
		return len(sc.ofKind(loopKindFixSpawn)) == 1
	})
	rig1.stopOnce(t) // daemon killed mid fix run — no drain, no diff rows

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

// TestLoopAdmissionConcurrentSingleWinner pins C10's atomicity (P1 #6):
// N concurrent /loop admissions on one conversation settle exactly one
// started row — every loser refuses already-active. The pre-fix fold ran
// outside the critical section, so two admissions could both pass it and
// double-start one conversation's loops.
func TestLoopAdmissionConcurrentSingleWinner(t *testing.T) {
	rig, _ := loopRig(t, nil, "")
	convID := loopBoot(t, rig)

	const n = 4
	errs := make([]string, n)
	var wg sync.WaitGroup
	for i := range n {
		wg.Add(1)
		go func() {
			defer wg.Done()
			conn, err := net.Dial("unix", rig.sock)
			if err != nil {
				errs[i] = "dial: " + err.Error()
				return
			}
			defer conn.Close()
			if err := json.NewEncoder(conn).Encode(Request{Cmd: CmdSendMessage, ConversationID: convID,
				Text: "/loop tasks 1. probe admission"}); err != nil {
				errs[i] = "encode: " + err.Error()
				return
			}
			var resp Response
			if err := json.NewDecoder(conn).Decode(&resp); err != nil {
				errs[i] = "decode: " + err.Error()
				return
			}
			if !resp.OK {
				errs[i] = resp.Error
			}
		}()
	}
	wg.Wait()

	refusals := 0
	for i, e := range errs {
		if e == "" {
			continue
		}
		refusals++
		if !strings.Contains(e, "already") {
			t.Errorf("client %d: error = %q, want the already-active refusal", i, e)
		}
	}
	if refusals != n-1 {
		t.Fatalf("refusals = %d, want exactly %d — one admission must win, the rest refuse", refusals, n-1)
	}

	// Replies land after the appends, so the fold is settled at this
	// point: exactly one loop_started row and one active loop.
	sc := scanLoop(t, rig.store, convID)
	if started := sc.ofKind(loopKindStarted); len(started) != 1 {
		t.Fatalf("started rows = %d, want exactly 1", len(started))
	}

	// The winner keeps driving (design stage follows the spawn tick);
	// stop it and wait the stop out so teardown never closes the store
	// mid-journal.
	rig.call(t, Request{Cmd: CmdSendMessage, ConversationID: convID, Text: "/loop stop"})
	waitLoop(t, rig.store, convID, "stopped", func(sc loopScan) bool {
		return len(sc.ofKind(loopKindStopped)) == 1
	})
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

// --- P1 #13 (loop⇄diff attribution by binding, never wall-clock) ----------------

// seedLoopTask journals a tasks-mode loop with one spawned task and
// returns its loop id — the attribution drills' fixture shape.
func seedLoopTask(t *testing.T, rig *testRig, convID int64) int64 {
	t.Helper()
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
	return loopID
}

// journalReview appends one review_action row (payload literal) — the
// settle-row half of the attribution drills.
func journalReview(t *testing.T, rig *testRig, convID int64, payload string) {
	t.Helper()
	if _, err := rig.store.AppendEvent(context.Background(), convID, store.EventReviewAction, payload); err != nil {
		t.Fatal(err)
	}
}

// bindLoopDiff journals the loop_diff_bound row the drain writes for
// one task (the diff does not exist at spawn — the binding lands at
// drain).
func bindLoopDiff(t *testing.T, rig *testRig, convID, loopID int64, task int, diffID int64) {
	t.Helper()
	if _, err := rig.server.journalLoop(context.Background(), convID, loopID, loopModeTasks, loopKindDiffBound,
		map[string]interface{}{"task": task, "diff_id": diffID}, 0); err != nil {
		t.Fatal(err)
	}
}

// TestLoopAdjudicateIgnoresUnboundAccept pins P1 #13 (a): with a
// loop_diff_bound row on the journal, an accept of an UNRELATED diff —
// pipeline or human actor, later seq, same conversation — leaves the
// task open. Attribution keys on the binding, never on wall-clock.
func TestLoopAdjudicateIgnoresUnboundAccept(t *testing.T) {
	rig, _ := loopRig(t, nil, "")
	convID := loopBoot(t, rig)
	ctx := context.Background()
	loopID := seedLoopTask(t, rig, convID)
	bindLoopDiff(t, rig, convID, loopID, 1, 7)
	// The restart double-fire's churn: the recovery's re-landed inbox
	// diff and a human click on a foreign diff, both post-spawn.
	journalReview(t, rig, convID, `{"action":"accept","actor":"auto_panel","diff_id":9}`)
	journalReview(t, rig, convID, `{"action":"accept","diff_id":10}`)
	rig.server.loopAdjudicateTask(ctx, convID, loopID, 1)
	if done := scanLoop(t, rig.store, convID).ofKind(loopKindTaskDone); len(done) != 0 {
		t.Fatalf("task resolved on unbound accepts: %v", done)
	}
	// The task's own bound diff still resolves it afterwards (no wedge).
	journalReview(t, rig, convID, `{"action":"accept","actor":"auto_panel","diff_id":7}`)
	rig.server.loopAdjudicateTask(ctx, convID, loopID, 1)
	if done := scanLoop(t, rig.store, convID).ofKind(loopKindTaskDone); len(done) != 1 ||
		fmt.Sprint(done[0]["status"]) != loopTaskLanded || fmt.Sprint(done[0]["diff_id"]) != "7" {
		t.Fatalf("done rows = %v, want exactly one landed row carrying diff 7", done)
	}
}

// TestLoopAdjudicateBoundAcceptLands pins P1 #13 (b — the regression
// guard): an accept of the task's OWN bound diff closes the task as
// landed — including the revise ladder's product, whose settle rows
// arrive under the product id and chain to the bound diff via
// origin_diff_id (the Mode B ladder happy path).
func TestLoopAdjudicateBoundAcceptLands(t *testing.T) {
	rig, _ := loopRig(t, nil, "")
	convID := loopBoot(t, rig)
	ctx := context.Background()
	loopID := seedLoopTask(t, rig, convID)
	bindLoopDiff(t, rig, convID, loopID, 1, 7)
	journalReview(t, rig, convID, `{"action":"accept","actor":"auto_panel","diff_id":7}`)
	rig.server.loopAdjudicateTask(ctx, convID, loopID, 1)
	done := scanLoop(t, rig.store, convID).ofKind(loopKindTaskDone)
	if len(done) != 1 || fmt.Sprint(done[0]["status"]) != loopTaskLanded || fmt.Sprint(done[0]["diff_id"]) != "7" {
		t.Fatalf("done rows = %v, want one landed row carrying diff 7", done)
	}

	// The ladder: a repair round on the bound diff produces diff 8; the
	// pipeline lands the PRODUCT. A HUMAN accept of that product closes
	// the task too — the bound lane keys on the chain root, not the
	// actor.
	loopID = seedLoopTask(t, rig, convID)
	bindLoopDiff(t, rig, convID, loopID, 1, 7)
	journalReview(t, rig, convID, `{"action":"auto_revise_round","actor":"auto_panel","round":1,"diff_id":7,"origin_diff_id":7}`)
	journalReview(t, rig, convID, `{"action":"auto_revise_product","actor":"auto_panel","product_diff_id":8,"origin_diff_id":7}`)
	journalReview(t, rig, convID, `{"action":"accept","diff_id":8}`)
	rig.server.loopAdjudicateTask(ctx, convID, loopID, 1)
	done = scanLoop(t, rig.store, convID).ofKind(loopKindTaskDone)
	if len(done) != 2 || fmt.Sprint(done[1]["status"]) != loopTaskLanded || fmt.Sprint(done[1]["diff_id"]) != "8" {
		t.Fatalf("done rows = %v, want the ladder product's accept to land the task (diff_id 8)", done)
	}
	if fmt.Sprint(done[1]["loop_id"]) != fmt.Sprint(loopID) {
		t.Errorf("the ladder landing journaled loop_id %v, want %d", done[1]["loop_id"], loopID)
	}
}

// TestLoopAdjudicateBlockedAttribution pins P1 #13's blocked discipline:
// an auto_land_blocked row settles the task ONLY when it names a diff in
// the task's bound chain. An unrelated inbox diff's blocked row is
// inert; the chain-bound blocked row settles settle_blocked (task done
// + loop suspension), evidence preserved.
func TestLoopAdjudicateBlockedAttribution(t *testing.T) {
	rig, _ := loopRig(t, nil, "")
	convID := loopBoot(t, rig)
	ctx := context.Background()
	loopID := seedLoopTask(t, rig, convID)
	bindLoopDiff(t, rig, convID, loopID, 1, 7)
	journalReview(t, rig, convID, `{"action":"auto_land_blocked","actor":"auto_panel","reason":"panel_mixed","diff_id":9}`)
	rig.server.loopAdjudicateTask(ctx, convID, loopID, 1)
	sc := scanLoop(t, rig.store, convID)
	if done := sc.ofKind(loopKindTaskDone); len(done) != 0 {
		t.Fatalf("task settled on an unbound blocked row: %v", done)
	}
	// The ladder product's blocked row (chained to the bound diff) settles.
	journalReview(t, rig, convID, `{"action":"auto_revise_product","actor":"auto_panel","product_diff_id":8,"origin_diff_id":7}`)
	journalReview(t, rig, convID, `{"action":"auto_land_blocked","actor":"auto_panel","reason":"panel_mixed","diff_id":8}`)
	rig.server.loopAdjudicateTask(ctx, convID, loopID, 1)
	sc = scanLoop(t, rig.store, convID)
	done := sc.ofKind(loopKindTaskDone)
	if len(done) != 1 || fmt.Sprint(done[0]["status"]) != loopTaskSettleBlocked {
		t.Fatalf("done rows = %v, want one settle_blocked row for the chain-bound blocked diff", done)
	}
	if causes := sc.causes(); len(causes) == 0 || causes[len(causes)-1] != "settle_blocked" {
		t.Errorf("causes = %v, want the last cause settle_blocked", causes)
	}
}

// TestLoopDiffBoundFold pins P1 #13 (c): the loop_diff_bound rows fold
// into state — the same deriveLoopStates pass a restart rebuilds from.
// Task bindings map diff→task; a Mode A (round) binding never claims a
// task. The start row's seed_diffs stay folded for the recovery
// exclusion.
func TestLoopDiffBoundFold(t *testing.T) {
	mk := func(seq int, payload string) store.Event {
		return store.Event{Seq: seq, Type: store.EventLoopEvent, Payload: json.RawMessage(payload)}
	}
	events := []store.Event{
		mk(1, `{"kind":"loop_started","mode":"tasks","base":"abc","max_rounds":10,"budget_tokens":2000000,"hold_severity":"P2","tasks":["task one"],"seed_diffs":[3],"spent_tokens":0}`),
		mk(2, `{"kind":"loop_task_spawn","loop_id":1,"task":1,"spent_tokens":10}`),
		mk(4, `{"kind":"loop_diff_bound","loop_id":1,"task":1,"diff_id":7,"spent_tokens":10}`),
		mk(5, `{"kind":"loop_diff_bound","loop_id":1,"round":2,"diff_id":11,"spent_tokens":20}`),
	}
	st := deriveLoopStates(events)[0]
	if !st.boundDiffs[7] || !st.boundDiffs[11] {
		t.Errorf("boundDiffs = %v, want 7 and 11", st.boundDiffs)
	}
	if st.boundTasks[7] != 1 {
		t.Errorf("boundTasks = %v, want 7→1", st.boundTasks)
	}
	if _, ok := st.boundTasks[11]; ok {
		t.Errorf("a Mode A binding must not claim a task: boundTasks = %v", st.boundTasks)
	}
	if len(st.seedDiffs) != 1 || st.seedDiffs[0] != 3 {
		t.Errorf("seedDiffs = %v, want [3]", st.seedDiffs)
	}
}

// TestLoopOwnedSeedDiffIDs pins P1 #13 (d) — the recoverPendingDiffs
// exclusion set: exactly the pending diffs a NON-terminal loop owns via
// loop_diff_bound, plus the active loop's claimed seed_diffs. A
// completed loop's bound diff and an unbound inbox diff are NOT
// loop-owned (they return to the boot recovery's re-fire set).
func TestLoopOwnedSeedDiffIDs(t *testing.T) {
	rig, _ := loopRig(t, nil, "")
	convID := loopBoot(t, rig)
	ctx := context.Background()
	mkDiff := func(name string) int64 {
		path := filepath.Join(t.TempDir(), name)
		if err := os.WriteFile(path, []byte("diff --git a/a.go b/a.go\n--- a/a.go\n+++ b/a.go\n@@"), 0o644); err != nil {
			t.Fatal(err)
		}
		d, err := rig.store.InsertDiff(ctx, convID, path, "", "", "")
		if err != nil {
			t.Fatal(err)
		}
		return d.ID
	}
	boundDone := mkDiff("done.diff")     // bound to a completed loop — excluded
	boundActive := mkDiff("active.diff") // bound to an active loop — owned
	seedActive := mkDiff("seed.diff")    // the active loop's seed claim — owned
	_ = mkDiff("inbox.diff")             // a plain pending inbox diff — excluded

	// A completed loop with a bound pending diff.
	ev, err := rig.server.journalLoop(ctx, convID, 0, loopModeTasks, loopKindStarted, map[string]interface{}{
		"base": "abc", "max_rounds": 10, "budget_tokens": 2_000_000, "hold_severity": "P2",
		"tasks": []string{"done task"}, "task_source": "inline",
	}, 0)
	if err != nil {
		t.Fatal(err)
	}
	doneID := int64(ev.Seq)
	if _, err := rig.server.journalLoop(ctx, convID, doneID, loopModeTasks, loopKindDiffBound,
		map[string]interface{}{"task": 1, "diff_id": boundDone}, 0); err != nil {
		t.Fatal(err)
	}
	if _, err := rig.server.journalLoop(ctx, convID, doneID, loopModeTasks, loopKindCompleted,
		map[string]interface{}{"rounds": 0}, 0); err != nil {
		t.Fatal(err)
	}
	// An active loop with a drained binding AND a claimed seed.
	ev, err = rig.server.journalLoop(ctx, convID, 0, loopModeAudit, loopKindStarted, map[string]interface{}{
		"base": "abc", "max_rounds": 10, "budget_tokens": 2_000_000, "hold_severity": "P2",
		"seed_diffs": []int64{seedActive},
	}, 0)
	if err != nil {
		t.Fatal(err)
	}
	activeID := int64(ev.Seq)
	if _, err := rig.server.journalLoop(ctx, convID, activeID, loopModeAudit, loopKindDiffBound,
		map[string]interface{}{"round": 1, "diff_id": boundActive}, 0); err != nil {
		t.Fatal(err)
	}

	got, err := rig.server.loopOwnedSeedDiffIDs(ctx)
	if err != nil {
		t.Fatal(err)
	}
	want := map[int64]bool{boundActive: true, seedActive: true}
	if len(got) != len(want) {
		t.Fatalf("loopOwnedSeedDiffIDs = %v, want %v", got, want)
	}
	for id := range want {
		if !got[id] {
			t.Errorf("loopOwnedSeedDiffIDs = %v, missing %d (%v)", got, id, want)
		}
	}
}

// TestJournalLoopDiffBoundRow pins the drain-journaled binding row's
// shape: implement runs carry task, fix runs carry round, never both,
// and the row rides the loop's common keys (actor auto_loop).
func TestJournalLoopDiffBoundRow(t *testing.T) {
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
	rig.server.journalLoopDiffBound(ctx, &runMeta{conversationID: convID, loopID: loopID, loopKind: "implement", loopTask: 1}, 7)
	rig.server.journalLoopDiffBound(ctx, &runMeta{conversationID: convID, loopID: loopID, loopKind: "fix", loopRound: 2}, 11)
	rows := scanLoop(t, rig.store, convID).ofKind(loopKindDiffBound)
	if len(rows) != 2 {
		t.Fatalf("binding rows = %v, want 2", rows)
	}
	if fmt.Sprint(rows[0]["task"]) != "1" || fmt.Sprint(rows[0]["diff_id"]) != "7" {
		t.Errorf("implement binding = %v, want task 1 / diff 7", rows[0])
	}
	if _, has := rows[0]["round"]; has {
		t.Errorf("implement binding must not carry round: %v", rows[0])
	}
	if fmt.Sprint(rows[1]["round"]) != "2" || fmt.Sprint(rows[1]["diff_id"]) != "11" {
		t.Errorf("fix binding = %v, want round 2 / diff 11", rows[1])
	}
	if _, has := rows[1]["task"]; has {
		t.Errorf("fix binding must not carry task: %v", rows[1])
	}
	if fmt.Sprint(rows[0]["actor"]) != loopActor {
		t.Errorf("binding actor = %v, want %s", rows[0]["actor"], loopActor)
	}
}
