package ipc

// D9-W3 learning_status pins: the single daemon fold (episodes newest-
// first + capped, totals folded over ALL episodes beyond the cap, flags,
// candidate stage projection with invalid marking) and the IPC smoke that
// returns it. The fold is the ONLY state — these tests build the journal
// rows directly, no distill rig required.

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/yingliang-zhang/odo/internal/store"
)

// journalLearningRow appends one review_action row to the fixture
// conversation.
func journalLearningRow(t *testing.T, st *store.Store, convID int64, p map[string]interface{}) store.Event {
	t.Helper()
	ev, err := st.AppendEvent(context.Background(), convID, store.EventReviewAction, mustJSON(p))
	if err != nil {
		t.Fatalf("append: %v", err)
	}
	return ev
}

func TestComputeLearningStatus(t *testing.T) {
	t.Parallel()
	f := newAutonomyFixture(t)
	ctx := context.Background()

	// Two episode rows (older first), one flag, one forward stage row.
	epOld := journalLearningRow(t, f.st, f.c.ID, map[string]interface{}{
		"action": "learning_episode", "epoch": 2, "workstream": "main",
		"window":          map[string]int{"first_seq": 1, "last_seq": 5},
		"outcomes":        map[string]int{"accepted": 3, "auto_rejected": 1},
		"context":         map[string]int{"panel_infra": 1},
		"verify_ms_total": 4200, "distill_ms": 900,
	})
	epNew := journalLearningRow(t, f.st, f.c.ID, map[string]interface{}{
		"action": "learning_episode", "epoch": 3, "workstream": "main",
		"window":        map[string]int{"first_seq": 6, "last_seq": 9},
		"outcomes":      map[string]int{"accepted": 1, "rejected": 2},
		"context":       map[string]int{},
		"usage":         map[string]interface{}{"available": true, "input": 10, "output": 4},
		"flags_emitted": []int{42},
	})
	flagEv := journalLearningRow(t, f.st, f.c.ID, map[string]interface{}{
		"action": "memory_audit_flag", "rule": "Always run go vet", "verdict": "harmful",
		"injections": 12, "rejects": 4, "reject_conversations": 3,
	})

	cand := LearningCandidate{
		Version: 1, Scope: "project:memory", BaseSHA16: "ab12cd34ef56ab78", BaseSourceSeq: 411,
		Delta: LearningCandidateDelta{
			Add:     []LearningRuleAdd{{Rule: "Always run go vet before claiming done", Evidence: "main-epoch-16"}},
			Retract: []string{},
		},
		Content:   "- Prefer compact output\n",
		CreatedAt: "2026-08-30T01:12:44Z", CreatedSeq: 460,
	}
	written, appended, err := AppendLearningCandidate(f.dir, cand)
	if err != nil || !appended {
		t.Fatalf("candidate write: appended=%v err=%v", appended, err)
	}
	journalLearningRow(t, f.st, f.c.ID, map[string]interface{}{
		"action": "learning_stage", "artifact_hash": written.ArtifactHash, "from": "candidate", "to": "shadow",
	})
	journalLearningRow(t, f.st, f.c.ID, map[string]interface{}{
		"action": "learning_stage", "artifact_hash": "deadbeefdeadbeef", "from": "shadow", "to": "dropped",
	})

	rep, err := ComputeLearningStatus(ctx, f.st, f.p)
	if err != nil {
		t.Fatal(err)
	}
	if rep.EpisodeCount != 2 || len(rep.Episodes) != 2 {
		t.Fatalf("episodes = %d/%d, want 2/2", rep.EpisodeCount, len(rep.Episodes))
	}
	if rep.Episodes[0].Seq != epNew.Seq || rep.Episodes[1].Seq != epOld.Seq {
		t.Fatalf("episodes must be newest-first: %+v", rep.Episodes)
	}
	if rep.Episodes[0].Workstream != "main" || rep.Episodes[0].Window.FirstSeq != 6 {
		t.Fatalf("episode row decode: %+v", rep.Episodes[0])
	}
	// Totals fold ALL episodes (cap is display-only).
	if rep.EpisodeTotals["accepted"] != 4 || rep.EpisodeTotals["rejected"] != 2 || rep.EpisodeTotals["auto_rejected"] != 1 {
		t.Fatalf("episode_totals: %v", rep.EpisodeTotals)
	}
	for _, k := range learningEpisodeOutcomeKeys {
		if _, ok := rep.EpisodeTotals[k]; !ok {
			t.Fatalf("episode_totals missing fixed key %q", k)
		}
	}

	if len(rep.Flags) != 1 || rep.Flags[0].Seq != flagEv.Seq ||
		rep.Flags[0].Verdict != "harmful" || rep.Flags[0].Rule != "Always run go vet" ||
		rep.Flags[0].Injections != 12 || rep.Flags[0].RejectConversations != 3 {
		t.Fatalf("flags: %+v", rep.Flags)
	}
	if rep.FlagThresholds["min_injections"] != rulesFlagMinInjections {
		t.Fatalf("thresholds not surfaced: %v", rep.FlagThresholds)
	}

	if len(rep.Candidates) != 2 {
		t.Fatalf("candidates: %+v", rep.Candidates)
	}
	var sha, invalid LearningCandidateRow
	for _, c := range rep.Candidates {
		if c.ArtifactHash == written.ArtifactHash {
			sha = c
		} else {
			invalid = c
		}
	}
	if sha.Stage != "shadow" || sha.Scope != "project:memory" || sha.CreatedSeq != 460 || sha.Invalid {
		t.Fatalf("resolved candidate row: %+v", sha)
	}
	if invalid.Stage != "dropped" || !invalid.Invalid {
		t.Fatalf("unresolvable stage row must read invalid: %+v", invalid)
	}

	// Adversarial W3 state: NO episode/flag rows at all still answers.
	f2 := newAutonomyFixture(t)
	rep2, err := ComputeLearningStatus(ctx, f2.st, f2.p)
	if err != nil {
		t.Fatal(err)
	}
	if rep2.EpisodeCount != 0 || len(rep2.Episodes) != 0 || len(rep2.Flags) != 0 || len(rep2.Candidates) != 0 {
		t.Fatalf("empty project must fold to zero sections: %+v", rep2)
	}
	if rep2.EpisodeTotals == nil || rep2.Episodes == nil || rep2.Flags == nil || rep2.Candidates == nil {
		t.Fatal("sections must be non-nil (GUI renders without presence branches)")
	}
}

// TestHandleLearningStatusIPC: the daemon returns the same fold over the
// wire — GUI never re-folds.
func TestHandleLearningStatusIPC(t *testing.T) {
	root := initRepo(t)
	t.Setenv("HOME", t.TempDir())
	t.Setenv("ODO_OMP_WRAPPER", writeStub(t, stubWrapper))
	rig := startRig(t, root)
	defer rig.stop(t)

	boot := rig.call(t, Request{Cmd: CmdBootstrap, ProjectRoot: root})
	convID := boot.Conversation.ID
	journalLearningRow(t, rig.store, convID, map[string]interface{}{
		"action": "learning_episode", "epoch": 5, "workstream": "main",
		"window":   map[string]int{"first_seq": 1, "last_seq": 3},
		"outcomes": map[string]int{"accepted": 1},
	})

	resp := rig.call(t, Request{Cmd: CmdLearningStatus, ProjectRoot: root})
	if resp.Learning == nil {
		t.Fatal("learning_status: nil learning payload")
	}
	if resp.Learning.EpisodeCount != 1 || len(resp.Learning.Episodes) != 1 {
		t.Fatalf("episodes over IPC: %+v", resp.Learning)
	}
	row := resp.Learning.Episodes[0]
	if row.Epoch != 5 || row.ConversationID != convID || row.Outcomes["accepted"] != 1 {
		t.Fatalf("ipc episode row: %+v", row)
	}
	if len(resp.Learning.Candidates) != 0 {
		t.Fatalf("W3 candidates must be empty: %+v", resp.Learning.Candidates)
	}

	// Wire shape pin: the payload's JSON keys match the GUI contract.
	raw, err := json.Marshal(resp.Learning)
	if err != nil {
		t.Fatal(err)
	}
	var wire map[string]interface{}
	if err := json.Unmarshal(raw, &wire); err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"project_root", "journal", "episodes", "episode_count", "episode_totals", "flags", "flag_thresholds", "candidates"} {
		if _, ok := wire[key]; !ok {
			t.Fatalf("learning payload missing contract key %q: %v", key, wire)
		}
	}
}
