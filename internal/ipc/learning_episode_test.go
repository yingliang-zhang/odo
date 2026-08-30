package ipc

// D9-W3 learning_episode fold tests (lock §1.1): a synthetic journal
// window carrying EVERY qualifying outcome type plus the non-outcome
// context cases (panel_infra, attribution boundaries), then determinism
// and fold-guard whitelist pins.

import (
	"encoding/json"
	"reflect"
	"testing"

	"github.com/yingliang-zhang/odo/internal/store"
)

func epEv(seq int, typ string, p map[string]interface{}, createdAt string) store.Event {
	return store.Event{Seq: seq, Type: typ, Payload: json.RawMessage(mustJSON(p)), CreatedAt: createdAt}
}

// TestFoldLearningEpisodeOutcomes folds one fixture window containing
// every §1.1 outcome class and asserts the full row — outcomes, context
// counts, cohort join, usage sum, verify_ms total, flags.
func TestFoldLearningEpisodeOutcomes(t *testing.T) {
	const t1 = "2026-08-30T00:00:03Z"
	const t2 = "2026-08-30T00:00:39Z"
	events := []store.Event{
		// Out-of-window rows: never counted regardless of class.
		epEv(99, store.EventReviewAction, map[string]interface{}{"action": "accept", "diff_id": 11}, t1),
		// In-window content.
		epEv(101, store.EventUserMessage, map[string]interface{}{
			"text": "do the thing", "receipt": map[string]string{rulesAuditMemoryReceipt: "shaAAA"}}, "2026-08-30T00:00:01Z"),
		// A pinned distill marker inside the window: bookkeeping, never
		// content (windowEvents doctrine) — pin that the fold skips it.
		epEv(103, store.EventReviewAction, map[string]interface{}{"action": "distill", "epoch": 6, "first_seq": 1, "last_seq": 99}, t1),
		epEv(105, store.EventAgentDone, map[string]interface{}{}, t1),
		epEv(110, store.EventReviewAction, map[string]interface{}{"action": "accept", "diff_id": 11}, t1),
		epEv(112, store.EventReviewAction, map[string]interface{}{"action": "accept", "diff_id": 22, "actor": autoActor}, t1),
		epEv(113, store.EventReviewAction, map[string]interface{}{"action": "reject", "diff_id": 33, "actor": autoActor}, t1),
		epEv(114, store.EventReviewAction, map[string]interface{}{"action": "reject", "diff_id": 44}, t1),
		epEv(115, store.EventReviewAction, map[string]interface{}{"action": "moa_review", "diff_id": 55, "consensus_verdict": "reject"}, t1),
		epEv(116, store.EventReviewAction, map[string]interface{}{"action": "moa_review", "diff_id": 66, "consensus_verdict": "reject", "verify_ms": 5000}, t1),
		epEv(117, store.EventReviewAction, map[string]interface{}{"action": "auto_revise_round", "round": 1, "diff_id": 70, "origin_diff_id": 71}, t1),
		epEv(118, store.EventReviewAction, map[string]interface{}{"action": "auto_revise_product", "product_diff_id": 77, "origin_diff_id": 71}, t1),
		epEv(119, store.EventReviewAction, map[string]interface{}{"action": "accept", "diff_id": 77, "actor": autoActor}, t1),
		epEv(120, store.EventReviewAction, map[string]interface{}{"action": "accept", "diff_id": 66}, t1),
		epEv(121, store.EventReviewAction, map[string]interface{}{"action": "auto_land_blocked", "reason": "verify_failed", "diff_id": 80, "verify_ms": 12000}, t1),
		epEv(122, store.EventReviewAction, map[string]interface{}{"action": "auto_land_blocked", "reason": "panel_mixed", "diff_id": 81}, t1),
		epEv(123, store.EventReviewAction, map[string]interface{}{"action": "auto_land_blocked", "reason": "panel_minority_reject", "diff_id": 82}, t1),
		epEv(124, store.EventReviewAction, map[string]interface{}{"action": "auto_land_blocked", "reason": "revise_no_progress", "diff_id": 83}, t1),
		epEv(125, store.EventReviewAction, map[string]interface{}{"action": "auto_land_blocked", "reason": "panel_infra", "diff_id": 84}, t1),
		epEv(126, store.EventReviewAction, map[string]interface{}{"action": "auto_land_blocked", "reason": "protected_path", "diff_id": 85}, t1),
		// The demotion pair: blocked row is SKIPPED in the review pass;
		// the memory_update ledger row counts — one suspension total.
		epEv(127, store.EventReviewAction, map[string]interface{}{"action": "auto_land_blocked", "reason": "ladder_suspended", "diff_id": 86}, t1),
		epEv(128, store.EventMemoryUpdate, map[string]interface{}{"layer": "auto_land", "cause": "ladder_suspended"}, t1),
		epEv(129, store.EventMemoryUpdate, map[string]interface{}{"layer": "run_verdict", "verdict": verdictFalseStop}, t1),
		epEv(130, store.EventMemoryUpdate, map[string]interface{}{"layer": "run_verdict", "verdict": verdictNoText}, t1),
		epEv(131, store.EventAgentError, map[string]interface{}{"error": "boom"}, t1),
		epEv(132, store.EventAgentError, map[string]interface{}{"error": "odo: transcript advisory", "odo": true}, t1),
		epEv(133, store.EventMemoryUpdate, map[string]interface{}{"layer": "apply", "cause": "revert"}, t1),
		epEv(134, store.EventMemoryUpdate, map[string]interface{}{
			"layer": "run_usage", "usage_available": true,
			"input_tokens": 100, "output_tokens": 50, "cache_read_tokens": 10, "cache_write_tokens": 5, "cost_usd": 0.25}, t1),
		epEv(135, store.EventMemoryUpdate, map[string]interface{}{
			"layer": "run_usage", "usage_available": false, "reason": "no session transcript"}, t1),
		epEv(136, store.EventLoopEvent, map[string]interface{}{
			"kind": loopKindRunUsage, "usage_available": true,
			"input_tokens": 200, "output_tokens": 60, "cache_write_tokens": 8, "cost_usd": 0.10}, t1),
		epEv(137, store.EventReviewAction, map[string]interface{}{"action": "memory_audit_flag", "rule": "x", "verdict": "harmful"}, t1),
		// Second terminal, claimed by no diff: an attribution boundary.
		epEv(138, store.EventAgentDone, map[string]interface{}{}, t2),
		// Out-of-window tail.
		epEv(201, store.EventReviewAction, map[string]interface{}{"action": "reject", "diff_id": 11}, t2),
	}
	// Only diff 11 exists as a store row: its send (101), terminal (105)
	// and accept (110) are all in-window → the single attributed cohort.
	// Every other outcome row names a diff with no in-window attribution
	// → counted raw, reconciled under context.attribution_lost.
	diffs := []store.Diff{{ID: 11, ConversationID: 1, PathOnDisk: "d11.diff", CreatedAt: "2026-08-30T00:00:04Z"}}

	row := foldLearningEpisode(events, diffs, learningEpisodeParams{
		epoch: 7, workstream: "main", firstSeq: 100, lastSeq: 200, distillMS: 98821,
	})

	if row["action"] != learningEpisodeAction || row["epoch"] != 7 || row["workstream"] != "main" {
		t.Fatalf("header wrong: %v %v %v", row["action"], row["epoch"], row["workstream"])
	}
	win := row["window"].(map[string]interface{})
	if win["first_seq"] != 100 || win["last_seq"] != 200 {
		t.Fatalf("window wrong: %v", win)
	}
	if row["distill_ms"] != int64(98821) {
		t.Fatalf("distill_ms wrong: %v", row["distill_ms"])
	}

	wantOut := map[string]int{
		"accepted": 2, "rejected": 1, "weak_rejected": 1,
		"auto_accepted": 2, "auto_rejected": 1,
		"verify_failed": 1, "panel_mixed": 1, "panel_minority_reject": 1,
		"revise_rounds_spawned": 1, "revise_landed": 1, "ladder_suspended": 1, "revise_no_progress": 1,
		"agent_errors": 1, "false_stops": 1, "no_texts": 1, "human_reverts": 1,
	}
	out := row["outcomes"].(map[string]int)
	if !reflect.DeepEqual(out, wantOut) {
		t.Fatalf("outcomes:\n got %v\nwant %v", out, wantOut)
	}

	ctxCounts := row["context"].(map[string]int)
	if ctxCounts["panel_infra"] != 1 || ctxCounts["blocked_other"] != 1 {
		t.Fatalf("blocked context wrong: %v", ctxCounts)
	}
	if ctxCounts["diff_less_terminals"] != 1 {
		t.Fatalf("diff_less_terminals: got %v want 1", ctxCounts["diff_less_terminals"])
	}
	// Raw human outcomes 4 (accepts 110+120, reject 114, weak 115) minus
	// the one attributed cohort outcome = 3 lost to the window boundary.
	if ctxCounts["attribution_lost"] != 3 {
		t.Fatalf("attribution_lost: got %v want 3", ctxCounts["attribution_lost"])
	}

	if row["verify_ms_total"] != int64(17000) {
		t.Fatalf("verify_ms_total: got %v want 17000", row["verify_ms_total"])
	}
	flags := row["flags_emitted"].([]int)
	if !reflect.DeepEqual(flags, []int{137}) {
		t.Fatalf("flags_emitted: got %v want [137]", flags)
	}

	usage := row["usage"].(map[string]interface{})
	if usage["available"] != true || usage["input"] != 300 || usage["output"] != 110 ||
		usage["cache_read"] != 10 || usage["cache_write"] != 13 {
		t.Fatalf("usage: %v", usage)
	}
	if usage["cost_usd"] != 0.25+0.10 {
		t.Fatalf("usage cost: %v", usage["cost_usd"])
	}

	cohorts := row["cohorts"].([]map[string]interface{})
	if len(cohorts) != 1 {
		t.Fatalf("cohorts: got %v want 1 row", cohorts)
	}
	c := cohorts[0]
	if c["sha16"] != "shaAAA" || c["outcomes"] != 1 || c["accepts"] != 1 || c["rejects"] != 0 || c["weak"] != 0 {
		t.Fatalf("cohort row: %v", c)
	}

	// Determinism pin: same inputs ⇒ byte-identical row (clock-free).
	again := foldLearningEpisode(events, diffs, learningEpisodeParams{
		epoch: 7, workstream: "main", firstSeq: 100, lastSeq: 200, distillMS: 98821,
	})
	b1, b2 := mustJSON(row), mustJSON(again)
	if string(b1) != string(b2) {
		t.Fatal("episode fold is not byte-deterministic")
	}
}

// TestFoldLearningEpisodeWeakAndBoundaries pins the two verbatim-machinery
// edges the headline fixture cannot isolate: a moa reject followed by the
// human's later accept on the same diff is NOT weak, and an outcome whose
// send predates the window is counted raw but never cohort-attributed.
func TestFoldLearningEpisodeWeakAndBoundaries(t *testing.T) {
	at := "2026-08-30T00:00:00Z"
	// Send at seq 50 is BEFORE the window: the accept inside the window
	// resolves to nothing in the cohort join (attribution boundary).
	events := []store.Event{
		epEv(50, store.EventUserMessage, map[string]interface{}{
			"text": "work", "receipt": map[string]string{rulesAuditMemoryReceipt: "shaPRE"}}, at),
		epEv(105, store.EventAgentDone, map[string]interface{}{}, at),
		epEv(110, store.EventReviewAction, map[string]interface{}{"action": "accept", "diff_id": 11}, at),
		epEv(120, store.EventReviewAction, map[string]interface{}{"action": "moa_review", "diff_id": 12, "consensus_verdict": "reject"}, at),
		epEv(125, store.EventReviewAction, map[string]interface{}{"action": "accept", "diff_id": 12}, at),
	}
	diffs := []store.Diff{
		{ID: 11, ConversationID: 1, CreatedAt: "2026-08-30T00:00:05Z"},
		{ID: 12, ConversationID: 1, CreatedAt: "2026-08-30T00:00:06Z"},
	}
	row := foldLearningEpisode(events, diffs, learningEpisodeParams{
		epoch: 1, workstream: "main", firstSeq: 100, lastSeq: 130,
	})
	out := row["outcomes"].(map[string]int)
	if out["weak_rejected"] != 0 {
		t.Fatalf("moa reject followed by the human accept is NOT weak: got %d", out["weak_rejected"])
	}
	if out["accepted"] != 2 {
		t.Fatalf("accepted: got %d want 2", out["accepted"])
	}
	if got := len(row["cohorts"].([]map[string]interface{})); got != 0 {
		t.Fatalf("pre-window send attribution must not fabricate a cohort: %v", got)
	}
	ctxCounts := row["context"].(map[string]int)
	if ctxCounts["attribution_lost"] != 2 {
		t.Fatalf("attribution_lost: got %v want 2", ctxCounts["attribution_lost"])
	}
}

// TestFoldLearningEpisodeEmptyWindow: an empty pinned window yields the
// full zero row (stable shape — consumers never branch on key presence).
func TestFoldLearningEpisodeEmptyWindow(t *testing.T) {
	events := []store.Event{
		epEv(1, store.EventUserMessage, map[string]interface{}{"text": "before"}, "2026-08-30T00:00:00Z"),
	}
	row := foldLearningEpisode(events, nil, learningEpisodeParams{
		epoch: 3, workstream: "w2", firstSeq: 100, lastSeq: 99, // empty: last < first
	})
	out := row["outcomes"].(map[string]int)
	for _, k := range learningEpisodeOutcomeKeys {
		if out[k] != 0 {
			t.Fatalf("empty window: outcome %s = %d", k, out[k])
		}
	}
	if len(out) != 16 {
		t.Fatalf("outcome key set must be complete: got %d keys", len(out))
	}
	usage := row["usage"].(map[string]interface{})
	if usage["available"] != false {
		t.Fatalf("empty window usage must be unavailable: %v", usage)
	}
	if got := len(row["cohorts"].([]map[string]interface{})); got != 0 {
		t.Fatalf("cohorts on empty window: %d", got)
	}
	if got := len(row["flags_emitted"].([]int)); got != 0 {
		t.Fatalf("flags on empty window: %d", got)
	}
}

// TestLearningEpisodeFoldWhitelist pins the D9-W3 attribution clauses of
// unownedFoldGrowth: an episode row above the pinned window is fold-
// authored bookkeeping (episode-only window ⇒ "nothing new"), a later
// waves' learning_* row inherits the same attribution, a run_usage
// receipt landing mid-fold never aborts the fold, and any UNRELATED
// action above the pin still trips the guard (fail-closed direction).
func TestLearningEpisodeFoldWhitelist(t *testing.T) {
	base := []store.Event{
		epEv(10, store.EventUserMessage, map[string]interface{}{"text": "content"}, "2026-08-30T00:00:00Z"),
	}
	over := func(ev store.Event) []store.Event { return append(append([]store.Event{}, base...), ev) }

	episodeRow := epEv(20, store.EventReviewAction, map[string]interface{}{
		"action": "learning_episode", "epoch": 1, "window": map[string]int{"first_seq": 1, "last_seq": 10}}, "2026-08-30T00:00:01Z")
	if unownedFoldGrowth(over(episodeRow), 10) {
		t.Error("an episode-only tail must read as nothing new (fold guard poisoned)")
	}
	stageRow := epEv(21, store.EventReviewAction, map[string]interface{}{"action": "learning_stage", "artifact_hash": "x"}, "2026-08-30T00:00:02Z")
	if unownedFoldGrowth(append(over(episodeRow), stageRow), 10) {
		t.Error("later waves' learning_* rows must inherit the episode's attribution")
	}
	usageRow := epEv(22, store.EventMemoryUpdate, map[string]interface{}{
		"layer": "run_usage", "usage_available": true, "input_tokens": 1}, "2026-08-30T00:00:03Z")
	if unownedFoldGrowth(over(usageRow), 10) {
		t.Error("a run_usage receipt landing mid-fold is bookkeeping, not growth")
	}
	for _, action := range []string{"accept", "foo_gate", "learningX"} {
		row := epEv(23, store.EventReviewAction, map[string]interface{}{"action": action}, "2026-08-30T00:00:04Z")
		if !unownedFoldGrowth(over(row), 10) {
			t.Errorf("action %q above the pin must still trip the guard (fail-closed)", action)
		}
	}
}
