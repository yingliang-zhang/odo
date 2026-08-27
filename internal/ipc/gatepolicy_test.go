package ipc

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/yingliang-zhang/odo/internal/git"
	"github.com/yingliang-zhang/odo/internal/store"
)

// D1 (2026-08-27 control-plane hardening DESIGN LOCK,
// docs/design/control-plane-hardening-lock.md): tests for the structural
// gate-source policy — Tier-0/Tier-1 boundary, manifest drift latch,
// human-only accept guard, Mode A reroute through the full panel path,
// and the fold attribution that follows it.

// TestIsGateSourcePathStructural pins the D1 boundary: every current Go
// file under internal/ipc/ is gate source; modelspec and gui are
// deliberately NOT; the whole match is case-folded. The locked test
// shape: walk internal/ipc/*.go ⇒ true; internal/modelspec, gui/src,
// root cmd_*.go ⇒ false; INTERNAL/IPC/x.go ⇒ true.
func TestIsGateSourcePathStructural(t *testing.T) {
	entries, err := os.ReadDir(".") // test cwd is internal/ipc itself
	if err != nil {
		t.Fatal(err)
	}
	sawGo := 0
	for _, e := range entries {
		if !strings.HasSuffix(e.Name(), ".go") {
			continue
		}
		sawGo++
		p := "internal/ipc/" + e.Name()
		if !isGateSourcePath(p) {
			t.Errorf("isGateSourcePath(%q) = false — the whole ipc tree is Tier-1", p)
		}
	}
	if sawGo == 0 {
		t.Fatal("no *.go files found in internal/ipc — test ran from the wrong directory")
	}
	cases := []struct {
		path string
		want bool
	}{
		// Tier-1 prefixes and the root CLI surface.
		{"internal/store/store.go", true},
		{"internal/git/git.go", true},
		{"internal/moa/client.go", true},
		{"internal/adapter/omp.go", true},
		{"main.go", true},
		{"cmd/subtool/main.go", true},
		// Case fold (macOS APFS bypass guard).
		{"INTERNAL/IPC/x.go", true},
		{"Internal/Store/Store.go", true},
		// Deliberately NOT protected.
		{"internal/modelspec/modelspec.go", false},
		{"gui/src/App.tsx", false},
		{"cmd_todo_test.go", false},
		{"cmd_unretract.go", false},
		{"src/main.go", false}, // subdir main.go, not the root CLI entry
		{"README.md", false},
		{"internal/ipcbackdoor/x.go", false}, // prefix must match a real directory boundary
	}
	for _, tc := range cases {
		if got := isGateSourcePath(tc.path); got != tc.want {
			t.Errorf("isGateSourcePath(%q) = %v, want %v", tc.path, got, tc.want)
		}
	}
	tier0 := []struct {
		path string
		want bool
	}{
		{"internal/ipc/gatepolicy.go", true},
		{"internal/ipc/gate_manifest.json", true},
		{"INTERNAL/IPC/GATEPOLICY.GO", true},
		{"internal/ipc/gatepolicy.go.bak", false},
		{"internal/ipc/autoland.go", false},
	}
	for _, tc := range tier0 {
		if got := isGateTier0Path(tc.path); got != tc.want {
			t.Errorf("isGateTier0Path(%q) = %v, want %v", tc.path, got, tc.want)
		}
	}
	// The compiled Tier-0 files exist in-tree (case-folded lookups above
	// must never hallucinate a policy that points at nothing), and the
	// tracked manifest parses with its own hash slot empty.
	for _, f := range gateTier0Files {
		if _, err := os.Stat(filepath.Base(f)); err != nil {
			t.Errorf("tier0 file %s unreadable from internal/ipc cwd: %v", f, err)
		}
	}
}

// TestGateCoreRefusedForActors pins the D1 human-only invariant: a diff
// touching a Tier-0 gate core file is a HARD error for every pipeline
// actor — unanimous panel attestation included (a panel judging a policy
// edit is downstream of the policy it judges: circular). The human Accept
// stays the unconditional escape. Tier-1 diffs keep panel attestation;
// autoLandCheck blocks Tier-0 pre-panel as gate_core_path.
func TestGateCoreRefusedForActors(t *testing.T) {
	root := initRepo(t)
	t.Setenv("HOME", t.TempDir())
	rig := startRig(t, root)
	defer rig.stop(t)

	boot := rig.call(t, Request{Cmd: CmdBootstrap, ProjectRoot: root})
	convID := boot.Conversation.ID
	ctx := context.Background()

	seed := func(name, patch string) store.Diff {
		t.Helper()
		p := filepath.Join(root, ".odo", "diffs", name)
		if err := os.WriteFile(p, []byte(patch), 0o644); err != nil {
			t.Fatal(err)
		}
		head, err := git.CurrentSHA(root)
		if err != nil {
			t.Fatalf("CurrentSHA: %v", err)
		}
		d, err := rig.store.InsertDiff(ctx, convID, p, head, "", "")
		if err != nil {
			t.Fatalf("InsertDiff: %v", err)
		}
		return d
	}

	corePatch := "diff --git a/internal/ipc/gatepolicy.go b/internal/ipc/gatepolicy.go\nnew file mode 100644\nindex 0000000..1111111\n--- /dev/null\n+++ b/internal/ipc/gatepolicy.go\n@@ -0,0 +1 @@\n+package generated\n"
	d := seed("tier0.diff", corePatch)

	// Pre-panel block: the check refuses before any panel spend.
	if reason, detail, _ := rig.server.autoLandCheck(d); reason != "gate_core_path" {
		t.Errorf("autoLandCheck = (%q, %q), want gate_core_path", reason, detail)
	}

	// Pipeline actor, no evidence: hard error naming the file.
	if _, err := rig.server.handleDiffAction(ctx, d.ID, "accept", autoActor, ""); err == nil ||
		!strings.Contains(err.Error(), "internal/ipc/gatepolicy.go") || !strings.Contains(err.Error(), "Tier-0") {
		t.Fatalf("auto accept of Tier-0 diff err = %v, want a refusal naming the file and Tier-0", err)
	}
	if got, err := rig.store.GetDiff(ctx, d.ID); err != nil || got.Status != store.DiffPending {
		t.Errorf("refused Tier-0 diff status = %v (%v), want pending", got.Status, err)
	}

	// An UNANIMOUS panel verdict bound to the exact bytes still refuses:
	// Tier-0 attestation is structurally impossible.
	if _, err := rig.store.AppendEvent(ctx, convID, store.EventReviewAction, mustJSON(map[string]interface{}{
		"action": "moa_review", "actor": autoActor, "diff_id": d.ID,
		"consensus_verdict": "accept", "patch_sha16": sha16([]byte(corePatch)),
	})); err != nil {
		t.Fatalf("journal unanimous verdict: %v", err)
	}
	if _, err := rig.server.handleDiffAction(ctx, d.ID, "accept", autoActor, ""); err == nil ||
		!strings.Contains(err.Error(), "internal/ipc/gatepolicy.go") {
		t.Fatalf("auto accept behind a unanimous verdict err = %v, want a Tier-0 refusal naming the file", err)
	}
	if got, err := rig.store.GetDiff(ctx, d.ID); err != nil || got.Status != store.DiffPending {
		t.Errorf("attested-but-Tier-0 diff status = %v (%v), want pending", got.Status, err)
	}

	// The human Accept is the unconditional escape and lands the bytes.
	resp := rig.call(t, Request{Cmd: CmdAcceptDiff, DiffID: d.ID})
	if !resp.Applied {
		t.Fatalf("human accept of Tier-0 diff: applied = false (resp %+v) — the escape path is broken", resp)
	}
	if got := readFileStr(t, filepath.Join(root, "internal", "ipc", "gatepolicy.go")); got != "package generated\n" {
		t.Errorf("gate core after human accept = %q, want the applied content", got)
	}
}

// gatePolicyRows collects the conversation's gate-policy journal rows for
// the drift test: gate_policy_check review rows and gate_source_drift
// memory_update rows, in journal order.
func gatePolicyRows(t *testing.T, st *store.Store, convID int64) (checks, drifts []map[string]interface{}) {
	t.Helper()
	events, err := st.ListEvents(context.Background(), convID, 0)
	if err != nil {
		t.Fatal(err)
	}
	for _, ev := range events {
		var p map[string]interface{}
		_ = json.Unmarshal(ev.Payload, &p)
		switch ev.Type {
		case store.EventReviewAction:
			if p["action"] == "gate_policy_check" {
				checks = append(checks, p)
			}
		case store.EventMemoryUpdate:
			if p["layer"] == "gate_policy" && p["cause"] == "gate_source_drift" {
				drifts = append(drifts, p)
			}
		}
	}
	return checks, drifts
}

// TestGatePolicyDrift pins the D1 startup drift latch: pinned Tier-0
// bytes vs the on-disk files — a corruption latches gateDrift, journals
// the drift evidence, and every landing pipeline refuses with
// auto_land_blocked{reason:"gate_policy_drift"}; a human re-pin +
// re-check restores the ok posture. Fails closed in both directions.
func TestGatePolicyDrift(t *testing.T) {
	ctx := context.Background()
	root := initRepo(t)
	t.Setenv("HOME", t.TempDir())

	coreV1 := []byte("package ipc\n\n// drift latch fixture v1\n")
	if err := os.MkdirAll(filepath.Join(root, "internal", "ipc"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "internal", "ipc", "gatepolicy.go"), coreV1, 0o644); err != nil {
		t.Fatal(err)
	}
	m, err := repinGateManifest(root)
	if err != nil {
		t.Fatalf("initial re-pin: %v", err)
	}
	if m.Tier0SHA16["internal/ipc/gatepolicy.go"] != sha16(coreV1) {
		t.Errorf("manifest pin = %q, want sha16 of the fixture bytes %q",
			m.Tier0SHA16["internal/ipc/gatepolicy.go"], sha16(coreV1))
	}
	if m.Tier0SHA16["internal/ipc/gate_manifest.json"] != "" || m.PinnedBy != "human" || m.Version != 1 {
		t.Errorf("manifest shape wrong: %+v", m)
	}

	rig := startRig(t, root)
	defer rig.stop(t)
	boot := rig.call(t, Request{Cmd: CmdBootstrap, ProjectRoot: root})
	convID := boot.Conversation.ID

	// Baseline re-check: pinned == on-disk ⇒ latch stays off, one ok row.
	rig.server.checkGatePolicy(ctx)
	if rig.server.gateDrift {
		t.Fatal("gateDrift latched on matching pins — false positive")
	}
	checks, drifts := gatePolicyRows(t, rig.store, convID)
	if len(checks) != 1 || checks[0]["cause"] != "ok" || len(drifts) != 0 {
		t.Fatalf("baseline rows: checks=%v drifts=%v, want one ok check and zero drift rows", checks, drifts)
	}
	tier0, _ := checks[0]["tier0"].([]interface{})
	if len(tier0) != 2 {
		t.Fatalf("check row tier0 entries = %v, want 2", tier0)
	}
	for _, raw := range tier0 {
		row, _ := raw.(map[string]interface{})
		if row["ok"] != true {
			t.Errorf("baseline tier0 row not ok: %v", row)
		}
	}

	// Mutate the policy: the exact kind of edit only a human may grant.
	coreV2 := []byte("package ipc\n\n// drift latch fixture v2 — tampered\n")
	if err := os.WriteFile(filepath.Join(root, "internal", "ipc", "gatepolicy.go"), coreV2, 0o644); err != nil {
		t.Fatal(err)
	}
	rig.server.checkGatePolicy(ctx)
	if !rig.server.gateDrift {
		t.Fatal("gateDrift not latched after a Tier-0 mutation — the latch is the fail-closed spine")
	}
	checks, drifts = gatePolicyRows(t, rig.store, convID)
	if len(drifts) != 1 {
		t.Fatalf("drift rows = %v, want exactly one", drifts)
	}
	dr := drifts[0]
	if !strings.Contains(fmt.Sprint(dr["detail"]), "internal/ipc/gatepolicy.go") {
		t.Errorf("drift detail must name the file: %v", dr["detail"])
	}
	if fmt.Sprint(dr["expected_sha16"]) != sha16(coreV1) || fmt.Sprint(dr["actual_sha16"]) != sha16(coreV2) {
		t.Errorf("drift row sha pair = (%v, %v), want (%v, %v)",
			dr["expected_sha16"], dr["actual_sha16"], sha16(coreV1), sha16(coreV2))
	}
	if got := checks[len(checks)-1]["cause"]; got != "drift" {
		t.Errorf("latest check row cause = %v, want drift", got)
	}

	// While latched the landing pipeline refuses — even on an unarmed
	// project (the drift gate runs before the arming silent-return).
	benign := "diff --git a/src/a.go b/src/a.go\nnew file mode 100644\nindex 0000000..1111111\n--- /dev/null\n+++ b/src/a.go\n@@ -0,0 +1 @@\n+package src\n"
	seedDiff := func(name string) store.Diff {
		t.Helper()
		p := filepath.Join(root, ".odo", "diffs", name)
		if err := os.WriteFile(p, []byte(benign), 0o644); err != nil {
			t.Fatal(err)
		}
		head, err := git.CurrentSHA(root)
		if err != nil {
			t.Fatalf("CurrentSHA: %v", err)
		}
		d, err := rig.store.InsertDiff(ctx, convID, p, head, "", "")
		if err != nil {
			t.Fatalf("InsertDiff: %v", err)
		}
		return d
	}
	d := seedDiff("drift-era.diff")
	rig.server.autoLand(ctx, d, root, "goal", false, "")
	sc := scanLoop(t, rig.store, convID)
	blockedFor := func(diffID int64) []string {
		var reasons []string
		for _, r := range sc.review {
			if fmt.Sprint(r["diff_id"]) == fmt.Sprint(diffID) && r["action"] == "auto_land_blocked" {
				reasons = append(reasons, fmt.Sprint(r["reason"]))
			}
		}
		return reasons
	}
	if got := blockedFor(d.ID); len(got) != 1 || got[0] != "gate_policy_drift" {
		t.Errorf("blocked reasons while latched = %v, want [gate_policy_drift]", got)
	}
	if got, err := rig.store.GetDiff(ctx, d.ID); err != nil || got.Status != store.DiffPending {
		t.Errorf("latched diff status = %v (%v), want pending", got.Status, err)
	}

	// Human re-acknowledgment: re-pin (the CLI core) + re-check restores.
	if _, err := repinGateManifest(root); err != nil {
		t.Fatalf("re-pin: %v", err)
	}
	rig.server.checkGatePolicy(ctx)
	if rig.server.gateDrift {
		t.Fatal("gateDrift still latched after a correct re-pin — the recovery path is broken")
	}
	checks, drifts = gatePolicyRows(t, rig.store, convID)
	if len(drifts) != 1 {
		t.Errorf("drift rows after restore = %d, want still 1 (append-only evidence)", len(drifts))
	}
	if got := checks[len(checks)-1]["cause"]; got != "ok" {
		t.Errorf("post-repin check row cause = %v, want ok", got)
	}

	// Post-restore a pipeline attempt journals no further drift refusal
	// (this rig has no review prefs — the unarmed path is silent by
	// design; the assertion keys on the ABSENCE of gate_policy_drift).
	d2 := seedDiff("restored.diff")
	rig.server.autoLand(ctx, d2, root, "goal", false, "")
	if got := blockedFor(d2.ID); len(got) != 0 {
		t.Errorf("post-restore blocked reasons for a fresh diff = %v, want none", got)
	}
}

// TestLoopFixRoutesGateSourceThroughPanel pins the D1 Mode A reroute: a
// loop fix touching Tier-1 gate source rides the FULL auto-land path —
// verify + panel + the attestation executor. The moa_review row lands
// BEFORE the accept (no judged rewrite without its judges), and with no
// valid verdict the fix never lands.
func TestLoopFixRoutesGateSourceThroughPanel(t *testing.T) {
	t.Run("panel_lands", func(t *testing.T) {
		var ctrl string
		rig, ret := loopRig(t, func(kind string, n int, model string) (int, string, int) {
			switch kind {
			case "audit":
				switch (n - 1) / 3 {
				case 0:
					// Arm the Tier-1 fix inside round 1's audit window
					// (the fix run's spawn reads it later — lost-race safe).
					_ = os.WriteFile(ctrl, []byte("tier1"), 0o644)
					return 200, auditFindings("- sev: P2 | file: internal/ipc/newgate.go | symbol: x | title: new gate file"), 10
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
		base := gitOut(t, rig.root, "rev-parse", "HEAD")
		rig.call(t, Request{Cmd: CmdSendMessage, ConversationID: convID, Text: "/loop audit base=" + base})

		waitLoop(t, rig.store, convID, "fix spawn", func(sc loopScan) bool {
			return len(sc.ofKind(loopKindFixSpawn)) == 1
		})
		pollDone(t, rig, convID) // drain the fix run → loopFixPipeline (D1 reroute)

		sc := waitLoop(t, rig.store, convID, "gate-source fix through the panel", func(sc loopScan) bool {
			return len(sc.ofKind(loopKindCompleted)) == 1
		})
		// The fix landed as the PANEL (auto_panel), never as auto_loop.
		if sc.acceptsWithActor(autoActor) != 1 || sc.acceptsWithActor(loopActor) != 0 {
			t.Errorf("accepts: auto_panel=%d auto_loop=%d, want [1 0]",
				sc.acceptsWithActor(autoActor), sc.acceptsWithActor(loopActor))
		}
		// Order proof: the moa_review verdict row precedes the accept row
		// for the same diff.
		moaIdx, acceptIdx := -1, -1
		for i, r := range sc.review {
			switch {
			case r["action"] == "moa_review" && moaIdx < 0:
				moaIdx = i
			case r["action"] == "accept" && r["actor"] == autoActor && acceptIdx < 0:
				acceptIdx = i
			}
		}
		if moaIdx < 0 || acceptIdx < 0 || moaIdx >= acceptIdx {
			t.Errorf("review rows: moa idx %d, accept idx %d — the verdict must precede the land", moaIdx, acceptIdx)
		} else if fmt.Sprint(sc.review[moaIdx]["diff_id"]) != fmt.Sprint(sc.review[acceptIdx]["diff_id"]) {
			t.Errorf("moa row diff %v ≠ accept row diff %v", sc.review[moaIdx]["diff_id"], sc.review[acceptIdx]["diff_id"])
		}
		st := sc.states[sc.loopID()]
		if st == nil || st.fixesLanded != 1 || st.fixOutcome != "landed" {
			t.Errorf("fold state wrong after panel-landed fix: %+v", st)
		}
	})

	t.Run("no_verdict_no_land", func(t *testing.T) {
		var ctrl string
		rig, ret := loopRig(t, func(kind string, n int, model string) (int, string, int) {
			switch kind {
			case "audit":
				switch (n - 1) / 3 {
				case 0:
					_ = os.WriteFile(ctrl, []byte("tier1"), 0o644)
					return 200, auditFindings("- sev: P2 | file: internal/ipc/newgate.go | symbol: x | title: new gate file"), 10
				default:
					return 200, auditClean, 10
				}
			case "review":
				return 500, "", 0 // every leg infra — no valid verdict exists
			}
			return 200, "", 0
		}, "")
		ctrl = ret
		convID := loopBoot(t, rig)
		base := gitOut(t, rig.root, "rev-parse", "HEAD")
		rig.call(t, Request{Cmd: CmdSendMessage, ConversationID: convID, Text: "/loop audit base=" + base})

		waitLoop(t, rig.store, convID, "fix spawn", func(sc loopScan) bool {
			return len(sc.ofKind(loopKindFixSpawn)) == 1
		})
		pollDone(t, rig, convID) // drain the fix run → panel infra blocked

		sc := waitLoop(t, rig.store, convID, "infra panel keeps the fix pending", func(sc loopScan) bool {
			return len(sc.ofKind(loopKindCompleted)) == 1
		})
		if n := sc.acceptsWithActor(autoActor) + sc.acceptsWithActor(loopActor); n != 0 {
			t.Errorf("accepts = %d, want 0 — no valid verdict may ever land a gate fix", n)
		}
		var fixDiff int64 = -1
		for _, r := range sc.review {
			if r["action"] == "auto_land_blocked" && fmt.Sprint(r["reason"]) == "panel_infra" {
				fmt.Sscanf(fmt.Sprint(r["diff_id"]), "%d", &fixDiff)
			}
		}
		if fixDiff < 0 {
			t.Fatal("no panel_infra blocked row for the fix — the pipeline lost the evidence")
		}
		if got, err := rig.store.GetDiff(context.Background(), fixDiff); err != nil || got.Status != store.DiffPending {
			t.Errorf("infra-blocked fix diff status = %v (%v), want pending", got.Status, err)
		}
		st := sc.states[sc.loopID()]
		if st == nil || st.fixesLanded != 0 || st.fixOutcome != "unlanded" {
			t.Errorf("fold state wrong after infra-blocked fix: %+v", st)
		}
	})
}

// TestLoopFixSuspendTier0 pins the D1 Mode A Tier-0 classification: a
// loop fix touching the gate core suspends risk:gate_core — human-only
// territory, no pipeline landing path exists — and the fix never lands.
func TestLoopFixSuspendTier0(t *testing.T) {
	var ctrl string
	rig, ret := loopRig(t, func(kind string, n int, model string) (int, string, int) {
		switch kind {
		case "audit":
			switch (n - 1) / 3 {
			case 0:
				_ = os.WriteFile(ctrl, []byte("tier0"), 0o644)
				return 200, auditFindings("- sev: P2 | file: internal/ipc/gatepolicy.go | symbol: x | title: gate core edit"), 10
			default:
				return 200, auditClean, 10
			}
		case "review":
			return 200, "ACCEPT\nshould never be consulted", 10
		}
		return 200, "", 0
	}, "")
	ctrl = ret
	convID := loopBoot(t, rig)
	base := gitOut(t, rig.root, "rev-parse", "HEAD")
	rig.call(t, Request{Cmd: CmdSendMessage, ConversationID: convID, Text: "/loop audit base=" + base})

	waitLoop(t, rig.store, convID, "fix spawn", func(sc loopScan) bool {
		return len(sc.ofKind(loopKindFixSpawn)) == 1
	})
	pollDone(t, rig, convID) // drain the fix run → risk:gate_core suspend

	sc := waitLoop(t, rig.store, convID, "tier0 suspend", func(sc loopScan) bool {
		return len(sc.ofKind(loopKindSuspended)) == 1
	})
	if got := sc.causes(); len(got) != 1 || got[0] != "risk:gate_core" {
		t.Errorf("suspend causes = %v, want [risk:gate_core]", got)
	}
	if n := sc.acceptsWithActor(autoActor) + sc.acceptsWithActor(loopActor); n != 0 {
		t.Errorf("accepts = %d, want 0 — a Tier-0 fix must NEVER land through any pipeline actor", n)
	}
	for _, r := range sc.review {
		if r["action"] == "moa_review" {
			t.Errorf("moa_review row present (%v) — the panel must never judge a Tier-0 fix", r)
		}
	}
}

// TestLoopFoldAttributesPanelLandedFix pins the D1 attribution rule on
// the raw fold (no rig): accept/blocked rows resolve the open Mode A fix
// phase IFF the actor is a pipeline actor (auto_loop or auto_panel) AND a
// loop_diff_bound{round} row names the diff. No binding ⇒ no attribution
// (fail-closed) — a human accept of an unrelated inbox diff stays a
// fact, never a loop outcome.
func TestLoopFoldAttributesPanelLandedFix(t *testing.T) {
	mk := func(seq int, payload string) store.Event {
		return store.Event{Seq: seq, Type: store.EventLoopEvent, Payload: json.RawMessage(payload)}
	}
	rev := func(seq int, payload string) store.Event {
		return store.Event{Seq: seq, Type: store.EventReviewAction, Payload: json.RawMessage(payload)}
	}
	phase := func(withBound bool, tail store.Event) []store.Event {
		rows := []store.Event{
			mk(1, `{"kind":"loop_started","mode":"audit","base":"abc","max_rounds":10,"budget_tokens":1000,"hold_severity":"P2"}`),
			mk(2, `{"kind":"loop_audit_round","loop_id":1,"round":1,"subject_sha16":"s1","legs":[{"model":"m","verdict":"complete"}]}`),
			mk(3, `{"kind":"loop_verdict","loop_id":1,"round":1,"verdict":"fix"}`),
			mk(4, `{"kind":"loop_fix_spawn","loop_id":1,"round":1}`),
		}
		if withBound {
			rows = append(rows, mk(5, `{"kind":"loop_diff_bound","loop_id":1,"round":1,"diff_id":9}`))
		}
		return append(rows, store.Event{Seq: 6, Type: tail.Type, Payload: tail.Payload})
	}

	t.Run("bound_panel_accept_attributes", func(t *testing.T) {
		st := deriveLoopStates(phase(true, rev(0, `{"action":"accept","actor":"auto_panel","diff_id":9}`)))[0]
		if st.fixesLanded != 1 || st.fixOpen || st.fixOutcome != "landed" {
			t.Errorf("bound panel accept must close the fix landed: %+v", st)
		}
	})
	t.Run("unbound_panel_accept_does_not", func(t *testing.T) {
		st := deriveLoopStates(phase(false, rev(0, `{"action":"accept","actor":"auto_panel","diff_id":9}`)))[0]
		if st.fixesLanded != 0 || !st.fixOpen || st.fixOutcome != "" {
			t.Errorf("unbound accept must NOT attribute (fail-closed): %+v", st)
		}
	})
	t.Run("bound_panel_blocked_attributes", func(t *testing.T) {
		st := deriveLoopStates(phase(true, rev(0, `{"action":"auto_land_blocked","actor":"auto_panel","diff_id":9,"reason":"panel_infra"}`)))[0]
		if st.fixOpen || st.fixOutcome != "unlanded" {
			t.Errorf("bound blocked row must resolve the fix unlanded: %+v", st)
		}
	})
	t.Run("bound_human_accept_does_not", func(t *testing.T) {
		st := deriveLoopStates(phase(true, rev(0, `{"action":"accept","actor":"","diff_id":9}`)))[0]
		if st.fixesLanded != 0 || !st.fixOpen || st.fixOutcome != "" {
			t.Errorf("a human accept of a bound diff is not a pipeline outcome: %+v", st)
		}
	})
	t.Run("bound_loop_actor_still_attributes", func(t *testing.T) {
		st := deriveLoopStates(phase(true, rev(0, `{"action":"accept","actor":"auto_loop","diff_id":9}`)))[0]
		if st.fixesLanded != 1 || st.fixOutcome != "landed" {
			t.Errorf("legacy auto_loop rows keep attributing when bound: %+v", st)
		}
	})
	t.Run("task_binding_is_not_a_fix", func(t *testing.T) {
		rows := []store.Event{
			mk(1, `{"kind":"loop_started","mode":"tasks","base":"abc","budget_tokens":1000,"tasks":["t1"]}`),
			mk(2, `{"kind":"loop_task_spawn","loop_id":1,"task":1}`),
			mk(3, `{"kind":"loop_diff_bound","loop_id":1,"task":1,"diff_id":9}`),
			rev(4, `{"action":"accept","actor":"auto_panel","diff_id":9}`),
		}
		st := deriveLoopStates(rows)[0]
		if st.fixesLanded != 0 {
			t.Errorf("a task-bound accept must not count as a Mode A fix land: %+v", st)
		}
	})
}

// TestClassifyDiffC0Purity pins the D1 guard (lock's Migration + guards
// section): the autonomy ladder's C0 is MEMORY-PREFIX-ONLY. Under the
// structural Tier-1 boundary nearly every daemon diff touches gate
// source — folding gate-source hits into C0 would drown the ladder
// stats, so classifyDiff must treat them as ordinary paths (they get
// their own gates: Tier annotations + panelVerdictAttestsDiff).
func TestClassifyDiffC0Purity(t *testing.T) {
	gate := func(path string) git.PatchStat {
		return git.PatchStat{Files: []git.FileStat{fs(path, 5, 1)}, Added: 5, Removed: 1}
	}
	for _, p := range []string{
		"internal/ipc/server.go",
		"internal/ipc/autoland.go", // a legacy protectedGateFiles member — freed from C0 by D1
		"internal/ipc/gatepolicy.go",
		"internal/store/store.go",
		"main.go",
	} {
		if got := classifyDiff(gate(p), false, nil); got == "C0" {
			t.Errorf("classifyDiff(%s) = C0 — gate-source hits must NOT widen C0", p)
		}
	}
	for _, p := range []string{".odo/memory.md", "wiki/guide.md"} {
		if got := classifyDiff(gate(p), false, nil); got != "C0" {
			t.Errorf("classifyDiff(%s) = %s, want C0 (memory prefixes keep the class)", p, got)
		}
	}
}
