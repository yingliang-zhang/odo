package ipc

// D9-W4 (lock stage machine; K3 spec §2 frozen replay; GLM amendment:
// prevented-harm/friction/vacuity; Sol amendment: double-execution pin).
// LLM-free, zero model calls, fail-closed.
//
// WHAT FREEZES (K3 §2.1): a reference freeze, not a byte copy. Per active
// conversation: head = newest distill marker seq, tail = the marker seq
// 8 epochs back (lane head when younger). The manifest journals as
// review_action{action:"learning_freeze"} — bounds + input_sha256 (sha256
// over the canonical join of the bounds + the sha of every cohort snapshot
// consulted). Replay reads events with tail_seq < seq ≤ head_seq per lane.
// Missing consumed inputs (snapshot row absent, patch unreadable) ⇒
// verdict "unverifiable" = FAIL, journaled with the missing key — never
// interpolated.
//
// PROJECTION (K3 §2.2): for every send/run_prompt receipt hash h in the
// slice: live = snapshot(h) content; candidate = planMemoryApply(snapshot,
// delta) — the pure write-path planner, no second convention. The
// attribution join (rulesConvOutcomes → aggregateRules, verbatim) re-runs
// against the counterfactual cohort map (live ∪ counterfactual — live
// blocks answer only the window-eligibility question "was this rule ever
// absent"; outcomes join counterfactual hashes only, convIDs preserved so
// the reject-conversations leg of the harmful tuple stays honest).
//
// Epistemic honesty (locked): this is a hygiene + known-harm recall gate,
// not a behavior predictor. Everything gated here IS deterministic.
//
// PASS CRITERIA (all must pass; K3 §2.3 + GLM/Sol amendments):
//
//	a  no candidate-added rule's counterfactual row meets the harmful
//	   tuple (rules_audit.go:94-97 constants, verbatim join).
//	b  normalized add-text ∩ {texts retracted after a harmful flag} = ∅
//	   (flags: full journal history; retraction: applies inside the slice).
//	c  rotation projection EMPTY — no silent third-party eviction.
//	d  injected-block growth vs live ≤ +512B AND final ≤ memoryCap.
//	e  double-execution byte-identical (Sol non-determinism pin).
//	f  ≥1 prevented-harm (GLM anti-vacuity): human reject, weak reject,
//	   auto reject, or human revert covered by the candidate cohort.
//	g  friction ≤ 3 × prevented-harm (GLM): human/auto accepts covered
//	   (integer inequality — the rules_audit.go:553-556 float-trap
//	   precedent).
//	h  loosened == 0: every retract target carries harmful-flag evidence
//	   in journal history (a retraction without harmful evidence is a
//	   loosening — excluded from the conservative grammar; the W4
//	   auto path carries empty retracts, asserted anyway).
//
//	plus provenance: every source_seq resolves to a journaled row of the
//	claimed kind (memory_propose / memory_audit_flag).
//
// Never-score exclusions (lock §5, learning_scoring.go): canary-cohort
// outcomes and gate-source/C0-diff outcomes feed NO replay metric —
// mirrored from the rules audit's baseline rule, one shared predicate.

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"sort"
	"strconv"
	"strings"

	"github.com/yingliang-zhang/odo/internal/store"
)

// learningFreezeEpochSpan is K: tail = the distill marker seq K epochs
// back per lane (K3 §2.1).
const learningFreezeEpochSpan = 8

// learningReplayGrowthCap is replay-d's injected-block growth allowance
// (+512B vs live; the absolute memoryCap is asserted per projected block
// too — the prompt budget every send pays).
const learningReplayGrowthCap = 512

// learningFreezeBound is one lane's frozen slice bounds (tail exclusive,
// head inclusive).
type learningFreezeBound struct {
	Tail int `json:"tail_seq"`
	Head int `json:"head_seq"`
}

// learningFreezeManifest is the journaled learning_freeze row body.
type learningFreezeManifest struct {
	ArtifactHash string                         `json:"artifact_hash"`
	Bounds       map[string]learningFreezeBound `json:"bounds"` // key: conversation id (decimal)
	InputSHA256  string                         `json:"input_sha256"`
	SliceEvents  int                            `json:"slice_events"`
}

// learningReplayLane is one active conversation's gathered inputs.
type learningReplayLane struct {
	convID int64
	events []store.Event
	diffs  []store.Diff
}

// learningReplaySlice is one lane's frozen event window.
type learningReplaySlice struct {
	lane   learningReplayLane
	events []store.Event
}

// learningReplayInput is the replay's gathered project state
// (deterministic ordering: lanes sorted by conversation id).
type learningReplayInput struct {
	lanes []learningReplayLane
}

// laneEvents flattens the lanes' event slices (stage-fold helpers consume
// this shape).
func (in learningReplayInput) laneEvents() [][]store.Event {
	out := make([][]store.Event, len(in.lanes))
	for i, ln := range in.lanes {
		out[i] = ln.events
	}
	return out
}

// gatherLearningReplayInput loads every active workstream's active
// conversation (events + diffs) for the project — the ComputeRulesAudit
// walk, single convention. Unreadable conversations are skipped (a
// half-readable conversation must not sink the gate; its slice simply
// contributes nothing).
func (s *Server) gatherLearningReplayInput(ctx context.Context, projectID int64) learningReplayInput {
	var in learningReplayInput
	wss, err := s.store.ListWorkstreams(ctx, projectID)
	if err != nil {
		return in
	}
	for _, w := range wss {
		c, cerr := s.store.GetActiveConversation(ctx, w.ID)
		if cerr != nil {
			continue
		}
		events, lerr := s.store.ListEvents(ctx, c.ID, 0)
		if lerr != nil {
			continue
		}
		diffs, derr := s.store.ListDiffs(ctx, c.ID)
		if derr != nil {
			continue
		}
		in.lanes = append(in.lanes, learningReplayLane{convID: c.ID, events: events, diffs: diffs})
	}
	sort.Slice(in.lanes, func(i, j int) bool { return in.lanes[i].convID < in.lanes[j].convID })
	return in
}

// learningFreezeBounds computes one lane's bound: head = newest distill
// marker seq; tail = the marker seq learningFreezeEpochSpan epochs back
// (0 = lane head when younger). ok=false when the lane has no markers —
// the lane contributes no slice (freeze is bounded by construction).
func learningFreezeBounds(events []store.Event) (learningFreezeBound, bool) {
	var markers []int
	for _, ev := range events {
		if isDistillMarkerEvent(ev) {
			markers = append(markers, ev.Seq)
		}
	}
	if len(markers) == 0 {
		return learningFreezeBound{}, false
	}
	head := markers[len(markers)-1]
	tail := 0
	if len(markers) >= learningFreezeEpochSpan {
		tail = markers[len(markers)-learningFreezeEpochSpan]
	}
	return learningFreezeBound{Tail: tail, Head: head}, true
}

// learningReplayReport is the replay gate's full deterministic result:
// the freeze manifest, the a–h + provenance verdicts, and the counters
// the checkpoint journals.
type learningReplayReport struct {
	Gate       string                 `json:"gate"`    // "replay"
	Verdict    string                 `json:"verdict"` // "pass" | "fail" | "unverifiable" (= FAIL)
	Violations []learningViolation    `json:"violations,omitempty"`
	Freeze     learningFreezeManifest `json:"freeze"`
	Checks     map[string]bool        `json:"checks"`
	// Counters (journaled into Detail for the gate row and into the
	// checkpoint metrics map):
	SliceSends      int `json:"slice_sends"`
	Outcomes        int `json:"outcomes"`
	MemoryFree      int `json:"memory_free_outcomes"`
	Cohorts         int `json:"cohorts"`
	PreventedHarm   int `json:"prevented_harm"`
	Friction        int `json:"friction"`
	Loosened        int `json:"loosened"`
	CanaryExcluded  int `json:"canary_excluded"`
	ScoringExcluded int `json:"scoring_excluded"`
	Reverts         int `json:"reverts"`
	GrowthMax       int `json:"growth_max_bytes"`
}

func (r learningReplayReport) passed() bool { return r.Verdict == "pass" }

// base folds the report into the generic gate-report shape the journal
// helper writes (freeze_seq links the manifest row).
func (r learningReplayReport) base(freezeSeq int) learningGateReport {
	return learningGateReport{
		Gate:       r.Gate,
		Verdict:    r.Verdict,
		Violations: r.Violations,
		Detail: map[string]interface{}{
			"freeze_seq":       freezeSeq,
			"bounds":           r.Freeze.Bounds,
			"input_sha256":     r.Freeze.InputSHA256,
			"slice_events":     r.Freeze.SliceEvents,
			"checks":           r.Checks,
			"slice_sends":      r.SliceSends,
			"outcomes":         r.Outcomes,
			"cohorts":          r.Cohorts,
			"prevented_harm":   r.PreventedHarm,
			"friction":         r.Friction,
			"loosened":         r.Loosened,
			"growth_max_bytes": r.GrowthMax,
			"canary_excluded":  r.CanaryExcluded,
			"scoring_excluded": r.ScoringExcluded,
		},
	}
}

// computeLearningReplay runs the whole gate TWICE and reports the first
// execution, with check e asserting byte-identity of the two marshaled
// runs (the Sol divergence pin — any time.Now/map-order leak flips e).
func computeLearningReplay(in learningReplayInput, cand LearningCandidate) learningReplayReport {
	first := computeLearningReplayOnce(in, cand)
	second := computeLearningReplayOnce(in, cand)
	b1, _ := json.Marshal(first)
	b2, _ := json.Marshal(second)
	eOK := string(b1) == string(b2)
	first.Checks["e"] = eOK
	if !eOK {
		first.Violations = append(first.Violations, learningViolation{
			Reason: "double execution diverged (clock/map-order nondeterminism)",
		})
		sort.Slice(first.Violations, func(i, j int) bool {
			if first.Violations[i].Rule != first.Violations[j].Rule {
				return first.Violations[i].Rule < first.Violations[j].Rule
			}
			return first.Violations[i].Reason < first.Violations[j].Reason
		})
		first.Verdict = "fail"
	}
	return first
}

// learningReplayOutcome is the slice-folded outcome view the counterfactual
// join needs: original kind + convID (the harmful tuple's conversation
// legs), the resolved cohort, and the producing diff for scoring exclusion.
type learningReplayOutcome struct {
	kind    string
	convID  int64
	memHash string
	diffID  int64
}

// computeLearningReplayOnce is the single execution. Deterministic: every
// iteration is sorted or insertion-ordered by construction; no clock
// reads.
func computeLearningReplayOnce(in learningReplayInput, cand LearningCandidate) learningReplayReport {
	rep := learningReplayReport{
		Gate:    learningGateReplay,
		Verdict: "pass",
		Freeze: learningFreezeManifest{
			ArtifactHash: cand.ArtifactHash,
			Bounds:       map[string]learningFreezeBound{},
		},
		Checks: map[string]bool{},
	}

	// --- slice extraction + project-wide snapshot tables -----------------
	var slices []learningReplaySlice
	snapshots := map[string]string{}     // sha -> content, layer:memory
	canarySnapshots := map[string]bool{} // sha set, layer:learning_canary
	for _, lane := range in.lanes {
		for _, ev := range lane.events {
			if ev.Type != store.EventMemoryUpdate {
				continue
			}
			var p struct {
				Layer   string `json:"layer"`
				Cause   string `json:"cause"`
				Content string `json:"content"`
				Sha     string `json:"sha"`
			}
			if !jsonUnmarshalOK(ev.Payload, &p) || p.Cause != "snapshot" || p.Sha == "" {
				continue
			}
			switch p.Layer {
			case "memory":
				if _, seen := snapshots[p.Sha]; !seen {
					snapshots[p.Sha] = p.Content
				}
			case "learning_canary":
				canarySnapshots[p.Sha] = true
			}
		}
		bound, ok := learningFreezeBounds(lane.events)
		if !ok {
			continue // no marker ⇒ no slice (bounded by construction)
		}
		rep.Freeze.Bounds[strconv.FormatInt(lane.convID, 10)] = bound
		var evs []store.Event
		for _, ev := range lane.events {
			if ev.Seq > bound.Tail && ev.Seq <= bound.Head {
				evs = append(evs, ev)
			}
		}
		slices = append(slices, learningReplaySlice{lane: lane, events: evs})
		rep.Freeze.SliceEvents += len(evs)
	}

	// --- outcome join per lane + never-score exclusions --------------------
	var covered []learningReplayOutcome
	var missingShas []string
	seenMissing := map[string]bool{}
	for _, sl := range slices {
		excludedDiffs := map[int64]bool{}
		for _, d := range sl.lane.diffs {
			if ex, _ := learningScoringClassify(d.PathOnDisk); ex {
				excludedDiffs[d.ID] = true
			}
		}
		for _, o := range rulesConvOutcomes(rulesScanConversation(sl.events), sl.lane.diffs, sl.lane.convID) {
			switch o.kind {
			case "accept", "reject", "weak_reject", "auto_accept", "auto_reject":
			default:
				continue
			}
			rep.Outcomes++
			switch {
			case o.memHash != "" && canarySnapshots[o.memHash]:
				rep.CanaryExcluded++
				continue
			case excludedDiffs[o.diffID]:
				rep.ScoringExcluded++
				continue
			}
			covered = append(covered, learningReplayOutcome{kind: o.kind, convID: sl.lane.convID, memHash: o.memHash, diffID: o.diffID})
		}
	}
	rep.SliceSends = 0
	for _, sl := range slices {
		rep.SliceSends += len(rulesScanConversation(sl.events).sends)
	}

	// Cohort resolution: a covered outcome whose receipt hash resolves to
	// NO snapshot row is a missing consumed input ⇒ unverifiable (FAIL),
	// the missing key journaled. Memory-free outcomes carry no cohort.
	consulted := map[string]bool{}
	for _, o := range covered {
		if o.memHash == "" {
			rep.MemoryFree++
			continue
		}
		if _, ok := snapshots[o.memHash]; ok {
			consulted[o.memHash] = true
			continue
		}
		if !seenMissing[o.memHash] {
			seenMissing[o.memHash] = true
			missingShas = append(missingShas, o.memHash)
		}
	}
	sort.Strings(missingShas)
	if len(missingShas) > 0 {
		rep.Verdict = "unverifiable"
		for _, h := range missingShas {
			rep.Violations = append(rep.Violations, learningViolation{
				Reason: "snapshot row absent for cohort " + h + " (fail-closed; rotated-away inputs are never interpolated)",
			})
		}
		for _, name := range []string{"a", "b", "c", "d", "f", "g", "h", "provenance"} {
			rep.Checks[name] = false
		}
		rep.finFreezeInput(consulted)
		return rep
	}

	// Human-revert evidence inside the slice (memory_update{layer:"apply",
	// cause:"revert"} rows — GLM's "human-reverted" harm class, counted,
	// not cohort-joined).
	for _, sl := range slices {
		for _, ev := range sl.events {
			if ev.Type != store.EventMemoryUpdate {
				continue
			}
			var p struct {
				Layer string `json:"layer"`
				Cause string `json:"cause"`
			}
			if jsonUnmarshalOK(ev.Payload, &p) && p.Layer == "apply" && p.Cause == "revert" {
				rep.Reverts++
			}
		}
	}

	// --- counterfactual projection -----------------------------------------
	// Per distinct live cohort: candidate block = planMemoryApply(snapshot,
	// delta). Retract entries ride the retraction-with-record arm of the
	// same pure planner (rule "" + contradicts = the verbatim target) —
	// with rule "" the archive prefix is empty, projection content just
	// removes the target line.
	project := make([]acceptedRule, 0, len(cand.Delta.Add)+len(cand.Delta.Retract))
	for _, a := range cand.Delta.Add {
		project = append(project, acceptedRule{rule: a.Rule, evidence: a.Evidence})
	}
	for _, t := range cand.Delta.Retract {
		project = append(project, acceptedRule{rule: "", contradicts: t})
	}
	hashList := make([]string, 0, len(consulted))
	for h := range consulted {
		hashList = append(hashList, h)
	}
	sort.Strings(hashList)
	cfCohorts := map[string]map[string]bool{}
	cfOf := map[string]string{}
	var rotatedOut []string
	rotSeen := map[string]bool{}
	capBreach := false
	for _, h := range hashList {
		content := snapshots[h]
		plan := planMemoryApply(content, project, nil, 0)
		for _, r := range plan.rotated {
			if !rotSeen[r] {
				rotSeen[r] = true
				rotatedOut = append(rotatedOut, r)
			}
		}
		if g := len(plan.content) - len(content); g > rep.GrowthMax {
			rep.GrowthMax = g
		}
		if len(plan.content) > memoryCap {
			capBreach = true
		}
		cfHash := sha16([]byte(plan.content))
		cfOf[h] = cfHash
		if _, seen := cfCohorts[cfHash]; !seen {
			cfCohorts[cfHash] = rulesOfContent(plan.content)
		}
		rep.Cohorts++
	}

	// Counterfactual join (verbatim machinery): outcomes re-keyed to their
	// counterfactual cohort with convID preserved; cohort map =
	// counterfactual ∪ live (live blocks only answer window eligibility,
	// never earn an outcome).
	var cfOutcomes []rulesOutcome
	for _, o := range covered {
		if o.memHash == "" {
			continue
		}
		cfOutcomes = append(cfOutcomes, rulesOutcome{
			convID: o.convID, resolveSeq: 0, kind: o.kind, memHash: cfOf[o.memHash],
		})
	}
	var currentRules []memoryRule
	for _, r := range parseMemoryLines(cand.Content) {
		if !r.opaque && r.text != "" {
			currentRules = append(currentRules, r)
		}
	}
	allCohorts := map[string]map[string]bool{}
	for h := range consulted {
		allCohorts[h] = rulesOfContent(snapshots[h])
	}
	for h, set := range cfCohorts {
		allCohorts[h] = set
	}
	rows, _, _, _, _, _ := aggregateRules(cfOutcomes, allCohorts, currentRules)

	// --- check a: no counterfactual harmful tuple on candidate adds -------
	addNorm := map[string]string{}
	for _, a := range cand.Delta.Add {
		addNorm[normalizeRule(a.Rule)] = a.Rule
	}
	aFail := ""
	for _, row := range rows {
		if row.Flag != "harmful" {
			continue
		}
		if orig, ok := addNorm[normalizeRule(row.Rule)]; ok {
			aFail = orig
			break
		}
	}
	rep.Checks["a"] = aFail == ""
	if aFail != "" {
		rep.Violations = append(rep.Violations, learningViolation{
			Rule:   aFail,
			Reason: "counterfactual row meets the harmful tuple (injections ≥ 10, rejects ≥ 3 in ≥ 3 conversations, rate ≥ 2× baseline)",
		})
	}

	// --- check b: retracted-after-harmful re-add ----------------------------
	// Flags: full journal history per lane (a rule once flagged harmful
	// stays evidence). Retractions: contradicts-apply pairs whose apply
	// lands inside the slice (propose rows may pre-date the slice — they
	// resolve from the full lane history too).
	harmfulFlags := map[string]bool{} // normalized rule text
	for _, lane := range in.lanes {
		for _, ev := range lane.events {
			if ev.Type != store.EventReviewAction {
				continue
			}
			var p struct {
				Action  string `json:"action"`
				Verdict string `json:"verdict"`
				Rule    string `json:"rule"`
			}
			if jsonUnmarshalOK(ev.Payload, &p) && p.Action == rulesAuditFlagAction && p.Verdict == "harmful" && p.Rule != "" {
				harmfulFlags[normalizeRule(p.Rule)] = true
			}
		}
	}
	retractedTexts := map[string]bool{} // normalized
	for _, lane := range in.lanes {
		bound, ok := rep.Freeze.Bounds[strconv.FormatInt(lane.convID, 10)]
		if !ok {
			continue
		}
		proposeByEpoch := map[int]proposePayload{}
		for _, ev := range lane.events {
			if ev.Type != store.EventReviewAction {
				continue
			}
			var head struct {
				Action string `json:"action"`
				Epoch  int    `json:"epoch"`
			}
			if !jsonUnmarshalOK(ev.Payload, &head) {
				continue
			}
			switch head.Action {
			case "memory_propose":
				var pp proposePayload
				if jsonUnmarshalOK(ev.Payload, &pp) {
					proposeByEpoch[pp.Epoch] = pp
				}
			case "memory_apply":
				if ev.Seq <= bound.Tail || ev.Seq > bound.Head {
					continue // retractions count only inside the frozen slice
				}
				var ap struct {
					Epoch    int            `json:"epoch"`
					Accepted []MemoryAccept `json:"accepted"`
				}
				if !jsonUnmarshalOK(ev.Payload, &ap) {
					continue
				}
				pp, ok := proposeByEpoch[ap.Epoch]
				if !ok {
					continue
				}
				for _, ref := range ap.Accepted {
					if ref.Target != "memory.md" || ref.Index < 0 || ref.Index >= len(pp.Proposals) {
						continue
					}
					prop := pp.Proposals[ref.Index]
					if prop.Intent == "retract" {
						continue
					}
					if nc := normalizeRule(prop.Contradicts); nc != "" && harmfulFlags[nc] {
						retractedTexts[nc] = true
					}
				}
			}
		}
	}
	bFail := ""
	for _, a := range cand.Delta.Add {
		if retractedTexts[normalizeRule(a.Rule)] {
			bFail = a.Rule
			break
		}
	}
	rep.Checks["b"] = bFail == ""
	if bFail != "" {
		rep.Violations = append(rep.Violations, learningViolation{
			Rule:   bFail,
			Reason: "add text was retracted after a harmful flag in slice history",
		})
	}

	// --- check c: rotation projection empty ---------------------------------
	rep.Checks["c"] = len(rotatedOut) == 0
	sort.Strings(rotatedOut)
	for _, r := range rotatedOut {
		rep.Violations = append(rep.Violations, learningViolation{
			Rule:   r,
			Reason: "projection evicts a live rule (third-party rotation outside the delta)",
		})
	}

	// --- check d: byte budget ------------------------------------------------
	rep.Checks["d"] = !capBreach && rep.GrowthMax <= learningReplayGrowthCap
	if rep.GrowthMax > learningReplayGrowthCap {
		rep.Violations = append(rep.Violations, learningViolation{
			Reason: "injected-block growth exceeds +512B budget (max " + strconv.Itoa(rep.GrowthMax) + "B)",
		})
	}
	if capBreach {
		rep.Violations = append(rep.Violations, learningViolation{
			Reason: "projected block exceeds memoryCap",
		})
	}

	// --- checks f/g: GLM anti-vacuity ----------------------------------------
	for _, o := range covered {
		if o.memHash == "" {
			continue
		}
		switch o.kind {
		case "reject", "weak_reject", "auto_reject":
			rep.PreventedHarm++
		case "accept", "auto_accept":
			rep.Friction++
		}
	}
	rep.PreventedHarm += rep.Reverts
	rep.Checks["f"] = rep.PreventedHarm >= 1
	rep.Checks["g"] = rep.Friction <= 3*rep.PreventedHarm
	if !rep.Checks["f"] {
		rep.Violations = append(rep.Violations, learningViolation{
			Reason: "replay_fail: no prevented harm (vacuous candidate — zero reject/revert evidence in the covered slice)",
		})
	}
	if !rep.Checks["g"] {
		rep.Violations = append(rep.Violations, learningViolation{
			Reason: "friction " + strconv.Itoa(rep.Friction) + " exceeds 3× prevented harm (" + strconv.Itoa(3*rep.PreventedHarm) + ")",
		})
	}

	// --- check h: conservative-only -------------------------------------------
	var loosened []string
	for _, t := range cand.Delta.Retract {
		if !harmfulFlags[normalizeRule(t)] {
			loosened = append(loosened, t)
		}
	}
	sort.Strings(loosened)
	rep.Loosened = len(loosened)
	rep.Checks["h"] = rep.Loosened == 0
	for _, t := range loosened {
		rep.Violations = append(rep.Violations, learningViolation{
			Rule:   t,
			Reason: "retract without harmful-flag evidence (loosening is outside the conservative grammar)",
		})
	}

	// --- provenance: source_seqs resolve to claimed row kinds -----------------
	provViolations := learningProvenanceViolations(in, cand, rep.Freeze.Bounds)
	rep.Checks["provenance"] = len(provViolations) == 0
	rep.Violations = append(rep.Violations, provViolations...)

	for _, ok := range rep.Checks {
		if !ok {
			rep.Verdict = "fail"
			break
		}
	}
	sort.Slice(rep.Violations, func(i, j int) bool {
		if rep.Violations[i].Rule != rep.Violations[j].Rule {
			return rep.Violations[i].Rule < rep.Violations[j].Rule
		}
		return rep.Violations[i].Reason < rep.Violations[j].Reason
	})
	rep.finFreezeInput(consulted)
	return rep
}

// finFreezeInput computes input_sha256 over the canonical join of bounds +
// the consulted snapshot shas (K3 §2.1 falsifiability anchor), sorted and
// line-joined.
func (rep *learningReplayReport) finFreezeInput(consulted map[string]bool) {
	var sb strings.Builder
	keys := make([]string, 0, len(rep.Freeze.Bounds))
	for k := range rep.Freeze.Bounds {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		b := rep.Freeze.Bounds[k]
		sb.WriteString(k + ":" + strconv.Itoa(b.Tail) + ":" + strconv.Itoa(b.Head) + "\n")
	}
	shas := make([]string, 0, len(consulted))
	for h := range consulted {
		shas = append(shas, h)
	}
	sort.Strings(shas)
	for _, h := range shas {
		sb.WriteString(h + "\n")
	}
	sum := sha256.Sum256([]byte(sb.String()))
	rep.Freeze.InputSHA256 = hex.EncodeToString(sum[:])
}

// learningProvenanceViolations verifies every creation-provenance seq:
// each Provenance.SourceSeq must resolve to a memory_propose row inside
// the slice; each add's FlagSeq to a memory_audit_flag row (full lane
// history — flags are evidence, they never expire).
func learningProvenanceViolations(in learningReplayInput, cand LearningCandidate, bounds map[string]learningFreezeBound) []learningViolation {
	var out []learningViolation
	inSlice := func(convID int64, seq int) bool {
		b, ok := bounds[strconv.FormatInt(convID, 10)]
		return ok && seq > b.Tail && seq <= b.Head
	}
	for _, want := range cand.Provenance.SourceSeq {
		found := false
		for _, lane := range in.lanes {
			if !inSlice(lane.convID, want) {
				continue
			}
			for _, ev := range lane.events {
				if ev.Seq != want || ev.Type != store.EventReviewAction {
					continue
				}
				var p struct {
					Action string `json:"action"`
				}
				if jsonUnmarshalOK(ev.Payload, &p) && p.Action == "memory_propose" {
					found = true
					break
				}
			}
			if found {
				break
			}
		}
		if !found {
			out = append(out, learningViolation{
				Reason: "provenance source_seq " + strconv.Itoa(want) + " does not resolve to a memory_propose row inside the freeze slice",
			})
		}
	}
	for _, a := range cand.Delta.Add {
		if a.FlagSeq == 0 {
			continue
		}
		found := false
		for _, lane := range in.lanes {
			for _, ev := range lane.events {
				if ev.Seq != a.FlagSeq || ev.Type != store.EventReviewAction {
					continue
				}
				var p struct {
					Action string `json:"action"`
				}
				if jsonUnmarshalOK(ev.Payload, &p) && p.Action == rulesAuditFlagAction {
					found = true
					break
				}
			}
			if found {
				break
			}
		}
		if !found {
			out = append(out, learningViolation{
				Rule:   a.Rule,
				Reason: "flag_seq " + strconv.Itoa(a.FlagSeq) + " does not resolve to a memory_audit_flag row",
			})
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Rule != out[j].Rule {
			return out[i].Rule < out[j].Rule
		}
		return out[i].Reason < out[j].Reason
	})
	return out
}

// computeLearningReplayGate is the creation-path driver: gather, compute,
// journal the freeze manifest on the creating lane, return the report and
// the manifest row's seq (0 when the journal append failed — the gap is
// named on the gate row).
func computeLearningReplayGate(ctx context.Context, s *Server, convID int64, cand LearningCandidate) (learningReplayReport, int) {
	p, err := s.store.GetProjectByRoot(ctx, s.projectRoot)
	if err != nil {
		return learningReplayReport{
			Gate:       learningGateReplay,
			Verdict:    "unverifiable",
			Violations: []learningViolation{{Reason: "project lookup: " + err.Error()}},
			Freeze:     learningFreezeManifest{ArtifactHash: cand.ArtifactHash, Bounds: map[string]learningFreezeBound{}},
			Checks:     map[string]bool{},
		}, 0
	}
	in := s.gatherLearningReplayInput(ctx, p.ID)
	rep := computeLearningReplay(in, cand)
	return rep, s.journalLearningFreeze(ctx, convID, rep)
}

// journalLearningFreeze journals the freeze manifest as
// review_action{action:"learning_freeze"}. Best-effort, seq returned (0
// on failure — the gate row names the gap).
func (s *Server) journalLearningFreeze(ctx context.Context, convID int64, rep learningReplayReport) int {
	ev, err := s.store.AppendEvent(ctx, convID, store.EventReviewAction, mustJSON(map[string]interface{}{
		"action":        "learning_freeze",
		"artifact_hash": rep.Freeze.ArtifactHash,
		"bounds":        rep.Freeze.Bounds,
		"input_sha256":  rep.Freeze.InputSHA256,
		"slice_events":  rep.Freeze.SliceEvents,
	}))
	if err != nil {
		return 0
	}
	return ev.Seq
}
