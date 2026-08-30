package ipc

// D9-W3 learning control plane (lock §W3): the SINGLE daemon-side fold
// behind both `learning_status` IPC and `odo learning status` — episodes
// (the learning_episode rows journaled at every distill), rules-audit
// flags (memory_audit_flag rows — the first flag surface ever; the
// self-improving wave-4 display was never dispatched), and the candidate
// stage list (candidates.jsonl + learning_stage rows — empty in W3, the
// writer is the W3 deliverable; W4 begins journaling stages).
//
// ONE fold implementation, two front ends: the GUI renders the IPC
// payload and NEVER re-folds (§7 pin — a TS fold would skew against this
// one); the CLI calls this same function against a store opened
// read-only (cmd_autonomy_audit precedent). Pure observability: nothing
// here feeds a decision path.

import (
	"context"
	"encoding/json"
	"path/filepath"
	"sort"

	"github.com/yingliang-zhang/odo/internal/store"
)

// learningStatusEpisodesCap bounds the episode list the payload carries
// (episode_totals still fold ALL episodes — the cap is display-only).
const learningStatusEpisodesCap = 50

// LearningEpisodeRow is one journaled learning_episode row, decoded.
type LearningEpisodeRow struct {
	Seq            int    `json:"seq"`
	ConversationID int64  `json:"conversation_id"`
	Workstream     string `json:"workstream"`
	Epoch          int    `json:"epoch"`
	Window         struct {
		FirstSeq int `json:"first_seq"`
		LastSeq  int `json:"last_seq"`
	} `json:"window"`
	Outcomes     map[string]int `json:"outcomes"`
	Context      map[string]int `json:"context"`
	FlagsEmitted []int          `json:"flags_emitted"`
	Usage        struct {
		Available  bool    `json:"available"`
		Input      int     `json:"input"`
		Output     int     `json:"output"`
		CacheRead  int     `json:"cache_read"`
		CacheWrite int     `json:"cache_write"`
		CostUSD    float64 `json:"cost_usd"`
	} `json:"usage"`
	VerifyMsTotal int64 `json:"verify_ms_total"`
	DistillMs     int64 `json:"distill_ms"`
}

// LearningFlagRow is one memory_audit_flag journal row (the rules audit's
// harmful/effective verdict, journaled per epoch on main).
type LearningFlagRow struct {
	Seq                 int    `json:"seq"`
	ConversationID      int64  `json:"conversation_id"`
	Rule                string `json:"rule"`
	Verdict             string `json:"verdict"` // "harmful" | "effective"
	Cites               string `json:"cites,omitempty"`
	Injections          int    `json:"injections"`
	Rejects             int    `json:"rejects"`
	RejectConversations int    `json:"reject_conversations"`
}

// LearningCandidateRow is one candidate.jsonl row projected with its
// folded stage (the fold is the only state, §1.3 — W3 has no stage rows
// yet, so every listed artifact reads stage "candidate").
type LearningCandidateRow struct {
	ArtifactHash string `json:"artifact_hash"`
	Version      int    `json:"version"`
	Scope        string `json:"scope"`
	Stage        string `json:"stage"`
	CreatedSeq   int    `json:"created_seq"`
	CreatedAt    string `json:"created_at"`
	// Invalid marks a stage row whose artifact_hash resolves to NO
	// candidates.jsonl row (tamper/drift — the fold surfaces it;
	// transitions W4+ will refuse it, fail-closed §7).
	Invalid bool `json:"invalid"`
}

// LearningStatusReport is the learning_status payload (and the CLI's
// --json shape).
type LearningStatusReport struct {
	ProjectRoot    string                 `json:"project_root"`
	Journal        string                 `json:"journal"`
	Episodes       []LearningEpisodeRow   `json:"episodes"` // newest first, capped at learningStatusEpisodesCap
	EpisodeCount   int                    `json:"episode_count"`
	EpisodeTotals  map[string]int         `json:"episode_totals"`
	Flags          []LearningFlagRow      `json:"flags"` // newest first
	FlagThresholds map[string]int         `json:"flag_thresholds"`
	Candidates     []LearningCandidateRow `json:"candidates"`
}

// ComputeLearningStatus folds the project's journals + candidates.jsonl
// into the status report. Deterministic and LLM-free (ADR-0003 inv 4).
func ComputeLearningStatus(ctx context.Context, st *store.Store, p store.Project) (LearningStatusReport, error) {
	rep := LearningStatusReport{
		ProjectRoot:    p.RootPath,
		Journal:        filepath.Join(p.RootPath, ".odo", "journal.sqlite"),
		Episodes:       []LearningEpisodeRow{},
		EpisodeTotals:  map[string]int{},
		Flags:          []LearningFlagRow{},
		FlagThresholds: RulesAuditThresholds(),
		Candidates:     []LearningCandidateRow{},
	}
	for _, k := range learningEpisodeOutcomeKeys {
		rep.EpisodeTotals[k] = 0
	}

	convs, err := st.ListActiveConversations(ctx, p.ID)
	if err != nil {
		return rep, err
	}
	// Workstream names are display attribution for the episode rows.
	wsNames := map[int64]string{}
	if wss, werr := st.ListWorkstreams(ctx, p.ID); werr == nil {
		for _, ws := range wss {
			wsNames[ws.ID] = ws.Name
		}
	}
	var episodes []LearningEpisodeRow
	// Stage fold (W4 begins journaling these): the LATEST "to" per
	// artifact_hash is the stage — the fold is the only state (§1.3).
	stage := map[string]string{}
	stageSeq := map[string]int{}
	for _, c := range convs {
		events, lerr := st.ListEvents(ctx, c.ID, 0)
		if lerr != nil {
			return rep, lerr
		}
		for _, ev := range events {
			if ev.Type != store.EventReviewAction {
				continue
			}
			var head struct {
				Action string `json:"action"`
			}
			if json.Unmarshal(ev.Payload, &head) != nil {
				continue
			}
			switch head.Action {
			case learningEpisodeAction:
				var row LearningEpisodeRow
				if json.Unmarshal(ev.Payload, &row) != nil {
					continue // torn row never kills the fold (observability posture)
				}
				row.Seq = ev.Seq
				row.ConversationID = c.ID
				if row.Workstream == "" {
					row.Workstream = wsNames[c.WorkstreamID]
				}
				episodes = append(episodes, row)
				for _, k := range learningEpisodeOutcomeKeys {
					rep.EpisodeTotals[k] += row.Outcomes[k]
				}
			case rulesAuditFlagAction:
				var p struct {
					Rule                string `json:"rule"`
					Verdict             string `json:"verdict"`
					Cites               string `json:"cites"`
					Injections          int    `json:"injections"`
					Rejects             int    `json:"rejects"`
					RejectConversations int    `json:"reject_conversations"`
				}
				if json.Unmarshal(ev.Payload, &p) != nil || p.Rule == "" {
					continue
				}
				rep.Flags = append(rep.Flags, LearningFlagRow{
					Seq: ev.Seq, ConversationID: c.ID,
					Rule: p.Rule, Verdict: p.Verdict, Cites: p.Cites,
					Injections: p.Injections, Rejects: p.Rejects,
					RejectConversations: p.RejectConversations,
				})
			case "learning_stage":
				var p struct {
					Hash string `json:"artifact_hash"`
					To   string `json:"to"`
				}
				if json.Unmarshal(ev.Payload, &p) != nil || p.Hash == "" || p.To == "" {
					continue
				}
				if ev.Seq > stageSeq[p.Hash] {
					stageSeq[p.Hash] = ev.Seq
					stage[p.Hash] = p.To
				}
			}
		}
	}
	sort.Slice(episodes, func(i, j int) bool { return episodes[i].Seq > episodes[j].Seq })
	sort.Slice(rep.Flags, func(i, j int) bool { return rep.Flags[i].Seq > rep.Flags[j].Seq })
	rep.EpisodeCount = len(episodes)
	for i, er := range episodes {
		if i >= learningStatusEpisodesCap {
			break
		}
		rep.Episodes = append(rep.Episodes, er)
	}

	// Candidate stages (W3: candidates.jsonl is the writer deliverable —
	// nothing appends yet; learning_stage rows start in W4. The fold is
	// complete for both: last stage table per hash, refs to a hash absent
	// from candidates.jsonl read invalid (fail-closed surface, §7).
	cands, cerr := ReadLearningCandidates(p.RootPath)
	if cerr != nil {
		return rep, cerr
	}
	known := map[string]bool{}
	for _, c := range cands {
		known[c.ArtifactHash] = true
		stg := "candidate"
		if s, ok := stage[c.ArtifactHash]; ok {
			stg = s
			delete(stage, c.ArtifactHash)
		}
		rep.Candidates = append(rep.Candidates, LearningCandidateRow{
			ArtifactHash: c.ArtifactHash, Version: c.Version, Scope: c.Scope,
			Stage: stg, CreatedSeq: c.CreatedSeq, CreatedAt: c.CreatedAt,
		})
	}
	for hash, stg := range stage { // stage rows without a resolvable artifact
		rep.Candidates = append(rep.Candidates, LearningCandidateRow{
			ArtifactHash: hash, Stage: stg, Invalid: true,
		})
	}
	sort.Slice(rep.Candidates, func(i, j int) bool {
		if rep.Candidates[i].CreatedSeq != rep.Candidates[j].CreatedSeq {
			return rep.Candidates[i].CreatedSeq < rep.Candidates[j].CreatedSeq
		}
		return rep.Candidates[i].ArtifactHash < rep.Candidates[j].ArtifactHash
	})
	return rep, nil
}

// handleLearningStatus implements learning_status: the daemon-side fold
// for the Memory panel's Learning tab (GUI never re-folds).
func (s *Server) handleLearningStatus(ctx context.Context, req Request) (Response, error) {
	p, err := s.store.GetProjectByRoot(ctx, s.projectRoot)
	if err != nil {
		return Response{}, err
	}
	rep, err := ComputeLearningStatus(ctx, s.store, p)
	if err != nil {
		return Response{}, err
	}
	return Response{Learning: &rep}, nil
}
