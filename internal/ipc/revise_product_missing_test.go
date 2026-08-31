package ipc

// revise_product_missing (2026-08-27, diffs #81/#83 of 2026-08-26): a
// revise-chain run that drains CLEAN with an empty diff used to fall
// through drainRun's default no-diff path — no diff row, no agent_error,
// the ladder waiting forever with the origin diff stuck pending behind a
// clean agent_done. The observed cause both times: the repair agent
// staged its work in a SIBLING worktree of the same conversation (the
// repair prompt embedded the origin diff but never named the run's own
// checkout as canonical). These tests pin the two-part fix: drainRun now
// journals the failure loudly (review_action{action:"revise_product_missing"}
// + a transcript advisory naming the operator recovery), and both ladder
// prompts carry the canonical worktree declaration (settleWorktreeSection).

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/yingliang-zhang/odo/internal/store"
	"github.com/yingliang-zhang/odo/internal/worktree"
)

// noProductWrapper mimics the #81/#83 agent-side signature: given a ladder
// prompt (repair/rebase — both open with "A previous implementation of the
// task below") the agent exits 0 with a text answer but stages NOTHING in
// its own checkout: text emitted, empty diff. (The real incidents staged
// in a sibling worktree; extracting from the run's OWN worktree sees the
// same empty diff either way — that equivalence is exactly why the failure
// was silent.) The 3s ladder-branch sleep leaves a steer window. Non-ladder
// prompts do the normal hello.txt work so the chain start still produces
// its diff.
const noProductWrapper = `#!/bin/sh
prompt_file="$2"
output_file="$3"
if grep -q "A previous implementation of the task below" "$prompt_file"; then
    sleep 3
    printf 'Reviewed the findings; still nothing to change.\n' > "$output_file"
else
    sleep 1
    cp "$prompt_file" hello.txt
    printf 'Created hello.txt as requested.\n' > "$output_file"
fi
exit 0
`

// crashProductWrapper: the ladder run exits 1 — the adapter drains it as a
// genuine agent_error, which must stay the sole truth (failStubWrapper's
// precedent): no revise_product_missing machinery may fire.
const crashProductWrapper = `#!/bin/sh
prompt_file="$2"
if grep -q "A previous implementation of the task below" "$prompt_file"; then
    echo "review harness crashed" >&2
    exit 1
fi
sleep 1
cp "$prompt_file" hello.txt
printf 'Created hello.txt as requested.\n' > "$3"
exit 0
`

// cleanNoChangeWrapper answers with text but changes nothing, for EVERY
// prompt — the plain non-revise clean no-diff shape.
const cleanNoChangeWrapper = `#!/bin/sh
sleep 1
printf 'Nothing to do.\n' > "$3"
exit 0
`

// silentThenCleanWrapper drives the false-stop-then-lost-product sequence:
// the FIRST ladder-shaped invocation emits nothing at all (the false-stop
// transport signature — zero texts, zero tool calls), the SECOND answers
// with text but again stages nothing. The retry re-runs the SAME repair
// prompt, so a counter file discriminates the two invocations.
func silentThenCleanWrapper(counter string) string {
	return fmt.Sprintf(`#!/bin/sh
prompt_file="$2"
output_file="$3"
if grep -q "A previous implementation of the task below" "$prompt_file"; then
    n=$(cat %q 2>/dev/null || printf 0)
    n=$((n + 1))
    echo "$n" > %q
    sleep 1
    if [ "$n" -eq 1 ]; then
        : > "$output_file"
    else
        printf 'Second look: still nothing staged.\n' > "$output_file"
    fi
else
    sleep 1
    cp "$prompt_file" hello.txt
    printf 'Created hello.txt as requested.\n' > "$output_file"
fi
exit 0
`, counter, counter)
}

// productMissingRows returns the journaled revise_product_missing payloads.
func productMissingRows(t *testing.T, rig *testRig, convID int64) []map[string]interface{} {
	t.Helper()
	return payloadsByAction(t, allEvents(t, rig, convID), "revise_product_missing")
}

// TestReviseProductMissingFailsLoud (design-lock test 1): a revise-chain
// run ending clean with an empty diff journals the machine row (chain
// linkage + the run's own worktree path) AND the operator-recovery
// advisory, fires NO false-stop retry and NO steer continuation, and runs
// the unchanged default tail (steer dropped, no parked goal to take).
// The origin diff stays pending for the human.
func TestReviseProductMissingFailsLoud(t *testing.T) {
	rig := settleRigWrapper(t, func(call int64, model string) (int, string) {
		return 200, "NEEDS_FIXES\nplease revise" // zero rejects → revise round 1
	}, noProductWrapper)

	boot := rig.call(t, Request{Cmd: CmdBootstrap, ProjectRoot: rig.root})
	convID := boot.Conversation.ID
	rig.call(t, Request{Cmd: CmdSendMessage, ConversationID: convID, Text: "Add hello.txt with the greeting"})
	done := pollDone(t, rig, convID)
	if done.Diff == nil {
		t.Fatal("original run produced no diff")
	}
	d0 := done.Diff.ID

	// Revise round 1 spawns (marker + round row journal before the run
	// starts); the ladder-branch sleep leaves the steer a mid-run window.
	waitSettle(t, rig.store, convID, "revise round 1 spawn", func(sc settleScan) bool {
		return len(sc.markers) == 1 && len(sc.rounds) == 1
	})
	steer := rig.call(t, Request{Cmd: CmdSendMessage, ConversationID: convID, Text: "mid-revise note", Steer: true})
	seqS := steer.Event.Seq

	done2 := pollDone(t, rig, convID)
	// Response.Diff is the conversation's latest diff — still d0 (pending,
	// never resolved). A NEW row beyond it would be the revise run
	// registering a product, breaking the #81/#83 shape the test rides.
	if done2.Diff != nil && done2.Diff.ID != d0 {
		t.Fatalf("the no-product revise run registered diff %d — extraction must stay empty", done2.Diff.ID)
	}

	rows := productMissingRows(t, rig, convID)
	if len(rows) != 1 {
		t.Fatalf("revise_product_missing rows = %d, want exactly 1: %v", len(rows), rows)
	}
	row := rows[0]
	if row["actor"] != autoActor {
		t.Errorf("actor = %v, want %s (pipeline mechanics)", row["actor"], autoActor)
	}
	if row["origin_diff_id"] != float64(d0) {
		t.Errorf("origin_diff_id = %v, want the chain root diff %d", row["origin_diff_id"], d0)
	}
	runDir, _ := row["run_dir_id"].(string)
	if runDir == "" {
		t.Errorf("run_dir_id missing or empty: %v", row)
	}
	detail := fmt.Sprint(row["detail"])
	if !strings.Contains(detail, "/worktrees/") || !strings.Contains(detail, runDir) {
		t.Errorf("detail = %q, want the run's own worktree path (contains run_dir_id %q)", detail, runDir)
	}

	sc := scanSettle(t, rig.store, convID)
	if len(sc.advisories) != 1 ||
		!strings.Contains(sc.advisories[0], "no diff in its own checkout") ||
		!strings.Contains(sc.advisories[0], ".odo/worktrees/") ||
		!strings.Contains(sc.advisories[0], "diff --cached") {
		t.Errorf("advisories = %v, want one naming the sibling-worktree recovery", sc.advisories)
	}

	// NO false-stop retry and NO continuation: no run_prompt row at all,
	// and no run_verdict ledger rows (the run was clean — the failure is
	// carried by the machine row, not by a verdict).
	if got := len(payloadsByAction(t, allEvents(t, rig, convID), "run_prompt")); got != 0 {
		t.Errorf("run_prompt rows = %d, want 0 (no retry, no continuation)", got)
	}
	if got := verdictRows(t, rig.store, convID); len(got) != 0 {
		t.Errorf("run_verdict rows = %v, want none for a clean run", got)
	}

	// The default-tail steers ending is unchanged: the queued steer closes
	// as steer_dropped{cause:"run_terminal"} (a clean run with no
	// continuation slot), never silently continued.
	drops := payloadsByAction(t, allEvents(t, rig, convID), "steer_dropped")
	if len(drops) != 1 || drops[0]["cause"] != "run_terminal" {
		t.Fatalf("steer_dropped rows = %v, want one cause:run_terminal", drops)
	}
	seqs, _ := drops[0]["steer_seqs"].([]interface{})
	if len(seqs) != 1 || seqs[0] != float64(seqS) {
		t.Errorf("steer_dropped steer_seqs = %v, want [%d]", seqs, seqS)
	}

	// The ladder machinery itself is untouched: still exactly the one
	// round, still pending — the failure surfaced, the origin waits on
	// the human (never on the ladder).
	if len(sc.rounds) != 1 {
		t.Errorf("round rows = %d, want still exactly 1 (no silent re-round)", len(sc.rounds))
	}
	if d, err := rig.store.GetDiff(context.Background(), d0); err != nil || d.Status != store.DiffPending {
		t.Errorf("origin diff status = %v/%v, want pending", d.Status, err)
	}
}

// TestReviseProductMissingSkipsErroredRun (design-lock test 2): an errored
// revise-chain run keeps agent_error as the sole truth — the new case is
// gated on !meta.errored and must not fire.
func TestReviseProductMissingSkipsErroredRun(t *testing.T) {
	rig := settleRigWrapper(t, func(call int64, model string) (int, string) {
		return 200, "NEEDS_FIXES\nplease revise"
	}, crashProductWrapper)

	boot := rig.call(t, Request{Cmd: CmdBootstrap, ProjectRoot: rig.root})
	convID := boot.Conversation.ID
	rig.call(t, Request{Cmd: CmdSendMessage, ConversationID: convID, Text: "Add hello.txt with the greeting"})
	done := pollDone(t, rig, convID)
	if done.Diff == nil {
		t.Fatal("original run produced no diff")
	}
	d0 := done.Diff.ID
	waitSettle(t, rig.store, convID, "revise round 1 spawn", func(sc settleScan) bool {
		return len(sc.markers) == 1 && len(sc.rounds) == 1
	})
	// Same latest-diff stance: the crashed run registers nothing new.
	if done2 := pollDone(t, rig, convID); done2.Diff != nil && done2.Diff.ID != d0 {
		t.Fatalf("the crashed revise run registered diff %d", done2.Diff.ID)
	}

	if rows := productMissingRows(t, rig, convID); len(rows) != 0 {
		t.Errorf("revise_product_missing rows = %v, want none for an errored run (agent_error is the truth)", rows)
	}
	// The adapter's genuine error reached the journal (errored truth), and
	// nothing pipeline-authored disguised it.
	var agentErrors int
	for _, ev := range allEvents(t, rig, convID) {
		if ev.Type == store.EventAgentError {
			agentErrors++
		}
	}
	if agentErrors == 0 {
		t.Error("no agent_error journaled for the crashed revise run")
	}
	if sc := scanSettle(t, rig.store, convID); len(sc.advisories) != 0 {
		t.Errorf("advisories = %v, want none (odo-authored advisories are for clean-drain failures)", sc.advisories)
	}
}

// TestReviseProductMissingSkipsNonReviseRun (design-lock test 3): a
// non-revise run (originDiffID == 0) ending clean with an empty diff is
// the ordinary no-diff shape — the new case must not fire.
func TestReviseProductMissingSkipsNonReviseRun(t *testing.T) {
	root := initRepo(t)
	t.Setenv("HOME", t.TempDir())
	t.Setenv("ODO_OMP_WRAPPER", writeStub(t, cleanNoChangeWrapper))
	rig := startRig(t, root)
	defer rig.stop(t)

	boot := rig.call(t, Request{Cmd: CmdBootstrap, ProjectRoot: root})
	convID := boot.Conversation.ID
	rig.call(t, Request{Cmd: CmdSendMessage, ConversationID: convID, Text: "take a look around"})
	if done := pollDone(t, rig, convID); done.Diff != nil {
		t.Fatalf("the no-change run registered diff %d", done.Diff.ID)
	}

	if rows := productMissingRows(t, rig, convID); len(rows) != 0 {
		t.Errorf("revise_product_missing rows = %v, want none for a non-revise run", rows)
	}
	if got := len(payloadsByAction(t, allEvents(t, rig, convID), "run_prompt")); got != 0 {
		t.Errorf("run_prompt rows = %d, want 0", got)
	}
	if sc := scanSettle(t, rig.store, convID); len(sc.markers) != 0 || len(sc.rounds) != 0 || len(sc.advisories) != 0 {
		t.Errorf("ladder noise on a plain run: markers=%v rounds=%v advisories=%v", sc.markers, sc.rounds, sc.advisories)
	}
}

// TestReviseProductMissingAfterFalseStopRetry (design-lock test 4): a
// revise run that false-stops (zero output) keeps the existing one-shot
// retry — the new case is gated on verdict != false_stop. The retry
// re-runs the same repair prompt in a fresh worktree WITH the chain
// identity propagated; when IT again ends clean-and-empty, the
// revise_product_missing failure journals exactly once (for the retry).
func TestReviseProductMissingAfterFalseStopRetry(t *testing.T) {
	counter := filepath.Join(t.TempDir(), "ladder-invocations")
	rig := settleRigWrapper(t, func(call int64, model string) (int, string) {
		return 200, "NEEDS_FIXES\nplease revise"
	}, silentThenCleanWrapper(counter))

	boot := rig.call(t, Request{Cmd: CmdBootstrap, ProjectRoot: rig.root})
	convID := boot.Conversation.ID
	rig.call(t, Request{Cmd: CmdSendMessage, ConversationID: convID, Text: "Add hello.txt with the greeting"})
	done := pollDone(t, rig, convID)
	if done.Diff == nil {
		t.Fatal("original run produced no diff")
	}
	d0 := done.Diff.ID
	waitSettle(t, rig.store, convID, "revise round 1 spawn", func(sc settleScan) bool {
		return len(sc.markers) == 1 && len(sc.rounds) == 1
	})

	// Run 1 false-stops; its drain admits the retry SYNCHRONOUSLY, so one
	// pollDone consumes run 1's terminal drain AND the retry's whole
	// lifecycle (TestFalseStopRetryOnce's observation model).
	pollDone(t, rig, convID)

	// The revise prompt ran exactly twice: the false stop and the retry.
	// (The counter holds the VALUE of the last invocation — echo-overwrite,
	// not falseStopStub's append-tally.)
	data, rerr := os.ReadFile(counter)
	if rerr != nil || strings.TrimSpace(string(data)) != "2" {
		t.Fatalf("ladder-shaped invocations = %q (%v), want 2 (false stop + one retry)", strings.TrimSpace(string(data)), rerr)
	}

	// The retry fired: one run_prompt{origin:"retry"} row; no revise
	// failure for the false-stopping run itself (zero output keeps the
	// retry semantics — the prompt NOW carries the canonical path, so the
	// retry is the right first handler).
	prompts := payloadsByAction(t, allEvents(t, rig, convID), "run_prompt")
	if len(prompts) != 1 || prompts[0]["origin"] != "retry" {
		t.Fatalf("run_prompt rows = %v, want exactly one origin:retry (the false-stop retry)", prompts)
	}

	// The retry ended clean-and-empty: ITS drain journals the one
	// revise_product_missing row, chained to the round's origin.
	rows := productMissingRows(t, rig, convID)
	if len(rows) != 1 {
		t.Fatalf("revise_product_missing rows = %d, want exactly 1 (the clean-empty retry): %v", len(rows), rows)
	}
	if rows[0]["origin_diff_id"] != float64(d0) || rows[0]["actor"] != autoActor {
		t.Errorf("row = %v, want actor:%s origin_diff_id:%d", rows[0], autoActor, d0)
	}

	// Verdict ledger: run 1's false_stop with retry_fired=true; the retry
	// was clean → no second verdict row. The loop bound stays exactly 1.
	verdicts := verdictRows(t, rig.store, convID)
	if len(verdicts) != 1 || verdicts[0]["verdict"] != verdictFalseStop ||
		verdicts[0]["is_retry"] != false || verdicts[0]["retry_fired"] != true {
		t.Errorf("verdict rows = %v, want one false_stop fresh-run row with retry_fired=true", verdicts)
	}
	if sc := scanSettle(t, rig.store, convID); len(sc.advisories) != 1 || !strings.Contains(sc.advisories[0], "no diff in its own checkout") {
		t.Errorf("advisories = %v, want the one sibling-worktree recovery notice", sc.advisories)
	}
}

// TestStartReviseRunWorktreeCreateFailure (design-lock test 6): after the
// startReviseRun reorder the worktree is created BEFORE anything is
// journaled, so a create failure leaves NO auto_revise user_message and
// NO auto_revise_round row behind — the caller's revise_spawn_failed
// ledger/blocked pair is the whole record of the round.
func TestStartReviseRunWorktreeCreateFailure(t *testing.T) {
	root := settleRigRepo(t)
	t.Setenv("HOME", t.TempDir())
	rig := startRig(t, root)
	defer rig.stop(t)

	boot := rig.call(t, Request{Cmd: CmdBootstrap, ProjectRoot: rig.root})
	convID := boot.Conversation.ID

	// A pending diff the ladder can spawn from (goal on the row anchors
	// the chain start exactly like a drain-registered product).
	patch := patchSrc("src/a.go", 1, 2, false)
	patchPath := filepath.Join(t.TempDir(), "p.diff")
	if err := os.WriteFile(patchPath, []byte(patch), 0o644); err != nil {
		t.Fatal(err)
	}
	d, err := rig.store.InsertDiff(context.Background(), convID, patchPath, "", "", "GOAL CHECK")
	if err != nil {
		t.Fatal(err)
	}

	// Break worktree creation: a manager rooted at a nonexistent repo
	// fails git worktree add — AFTER the admission gates, BEFORE the
	// prompt assembly, before any journal row.
	rig.server.mgr = worktree.NewManager(filepath.Join(t.TempDir(), "missing-repo"))
	defer func() { rig.server.mgr = worktree.NewManager(root) }()

	rig.server.ladderMu.Lock()
	rig.server.settleRevise(context.Background(), d, patch, []ReviewResult{
		{Model: "rm1@test", Verdict: "needs_fixes", Comments: "fix x"},
	})
	rig.server.ladderMu.Unlock()

	sc := scanSettle(t, rig.store, convID)
	if len(sc.markers) != 0 || len(sc.rounds) != 0 {
		t.Fatalf("journal rows leaked from a never-created run: markers=%v rounds=%v (the pre-reorder shape)", sc.markers, sc.rounds)
	}
	if got := sc.blockedReasons(); len(got) != 1 || got[0] != "revise_spawn_failed" {
		t.Errorf("blocked reasons = %v, want [revise_spawn_failed]", got)
	}
	if got := sc.memoryCauses(); len(got) != 1 || got[0] != "revise_spawn_failed" {
		t.Errorf("memory causes = %v, want [revise_spawn_failed] (the caller closes the ledger)", got)
	}
}

// TestSettleRebasePromptUnit pins the base_stale builder's canonical
// section: the same declaration repair prompts carry (both ladder prompts
// are wander-prone the same way), verbatim path, before the goal fence.
func TestSettleRebasePromptUnit(t *testing.T) {
	t.Parallel()
	prompt := settleRebasePrompt("THE ORIGINAL GOAL", "THE DIFF BODY", "MERGE DIAGNOSTICS", "/abs/rebase/worktree")
	wtAt := strings.Index(prompt, "/abs/rebase/worktree")
	goalAt := strings.Index(prompt, "THE ORIGINAL GOAL")
	if wtAt < 0 {
		t.Fatal("rebase prompt missing the canonical worktree path")
	}
	if wtAt > goalAt {
		t.Error("worktree section must precede the goal fence")
	}
	for _, want := range []string{
		"THE ORIGINAL GOAL", "THE DIFF BODY", "MERGE DIAGNOSTICS",
		"make ALL edits and stage ALL changes there",
		"earlier runs of this same task: treat them as read-only",
		"Your diff is extracted from your own checkout only",
	} {
		if !strings.Contains(prompt, want) {
			t.Errorf("rebase prompt missing %q", want)
		}
	}
}
