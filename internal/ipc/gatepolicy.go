package ipc

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/yingliang-zhang/odo/internal/store"
)

// Gate-source policy (D1 of the 2026-08-27 control-plane hardening DESIGN
// LOCK, docs/design/control-plane-hardening-lock.md): the structural,
// fail-closed replacement for the 10-file hand-written protectedGateFiles
// map (deleted in the same diff). Two tiers:
//
//   - Tier-0 (human-only): editing these files IS an exemption grant; no
//     pipeline actor may land them, attestation included — a panel judging
//     a policy edit is downstream of the policy it judges (circular).
//   - Tier-1 (the control plane): directory-boundary rule — every current
//     and future file under the prefixes below is gate source. Tier-1
//     diffs route through the full auto-land panel and land only behind
//     panelVerdictAttestsDiff (the judged never rewrites its own judge
//     without its judges); the human Accept click stays the unconditional
//     escape.
//
// User ruling ① (boundary) recorded in the lock: the root entry is gate
// source too — main.go and any standalone cmd/ tree. internal/ipc/cmd_*.go
// stay Tier-1 via the ipc prefix (they are the human's control levers —
// attestable, not Tier-0). Deliberately NOT protected: internal/modelspec
// (timeouts/budgets only — misuse fails closed), gui/.
var gateTier0Files = []string{
	"internal/ipc/gatepolicy.go",
	"internal/ipc/gate_manifest.json",
}

// gateProtectedPrefixes is the Tier-1 directory boundary (lock D1
// Boundary, ruling ①). Every path under these prefixes — current and
// future — is gate source.
var gateProtectedPrefixes = []string{
	"internal/ipc/", "internal/store/", "internal/git/",
	"internal/moa/", "internal/adapter/",
}

const (
	// gatePolicyGoPath / gateManifestPath name the two Tier-0 files
	// project-relatively (journal rows and the drift check agree on the
	// spelling by construction).
	gatePolicyGoPath = "internal/ipc/gatepolicy.go"
	gateManifestPath = "internal/ipc/gate_manifest.json"
)

// isGateTier0Path reports Tier-0 membership (case-folded: macOS APFS/HFS+
// resolve case variants identically).
func isGateTier0Path(p string) bool {
	lp := strings.ToLower(p)
	for _, f := range gateTier0Files {
		if lp == f {
			return true
		}
	}
	return false
}

// gateTier0Hit returns the first Tier-0 path in paths, or ("", false).
func gateTier0Hit(paths []string) (string, bool) {
	for _, f := range paths {
		if isGateTier0Path(f) {
			return f, true
		}
	}
	return "", false
}

// isGateSourcePath reports whether p is gate source — a Tier-0 file or
// anything under the Tier-1 boundary. Case-folded; the check is a pure
// path predicate, never a file read, so it cannot be disabled by deleting
// or renaming files (deleting the manifest cannot widen the rule: Tier-0
// status is compiled in here, per the lock's Manifest section).
func isGateSourcePath(p string) bool {
	lp := strings.ToLower(p)
	if isGateTier0Path(lp) {
		return true
	}
	for _, prefix := range gateProtectedPrefixes {
		if strings.HasPrefix(lp, prefix) {
			return true
		}
	}
	// Ruling ①: the root CLI entry and any standalone cmd/ tree are gate
	// source. Root cmd_*.go files are NOT (the lock's structural test pins
	// cmd_*.go false) — odo's human levers live in internal/ipc/cmd_*.go,
	// already covered by the ipc prefix.
	if lp == "main.go" || strings.HasPrefix(lp, "cmd/") {
		return true
	}
	return false
}

// gateManifest is the on-disk pinned record (ruling ②): the human's
// re-acknowledgment of the current Tier-0 bytes. The manifest's own hash
// slot stays empty — self-reference; its Tier-0 status is compiled into
// gatepolicy.go, so the pin never depends on judging itself.
type gateManifest struct {
	Version           int               `json:"version"`
	ProtectedPrefixes []string          `json:"protected_prefixes"`
	Tier0Files        []string          `json:"tier0_files"`
	Tier0SHA16        map[string]string `json:"tier0_sha16"`
	PinnedAt          string            `json:"pinned_at"`
	PinnedBy          string            `json:"pinned_by"`
}

// repinGateManifest recomputes the sha16 of every compiled Tier-0 file
// under projectRoot and rewrites gate_manifest.json atomically (same-dir
// temp + rename). Callers: the human-only `odo gate re-pin` CLI (never
// commits — the human commits both files) and the drift test's restore
// leg. pinned_by is "human" by construction: no code path inside the
// daemon repins.
func repinGateManifest(projectRoot string) (gateManifest, error) {
	m := gateManifest{
		Version:           1,
		ProtectedPrefixes: append([]string(nil), gateProtectedPrefixes...),
		Tier0Files:        append([]string(nil), gateTier0Files...),
		Tier0SHA16:        make(map[string]string, len(gateTier0Files)),
		PinnedAt:          time.Now().UTC().Format(time.RFC3339),
		PinnedBy:          "human",
	}
	for _, f := range gateTier0Files {
		sha := ""
		if f != gateManifestPath {
			data, err := os.ReadFile(filepath.Join(projectRoot, f))
			if err != nil {
				return gateManifest{}, fmt.Errorf("re-pin: read %s: %w", f, err)
			}
			sha = sha16(data)
		}
		m.Tier0SHA16[f] = sha
	}
	data, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return gateManifest{}, fmt.Errorf("re-pin: encode manifest: %w", err)
	}
	target := filepath.Join(projectRoot, gateManifestPath)
	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		return gateManifest{}, fmt.Errorf("re-pin: mkdir: %w", err)
	}
	tmp := target + ".tmp"
	if err := os.WriteFile(tmp, append(data, '\n'), 0o644); err != nil {
		return gateManifest{}, fmt.Errorf("re-pin: write: %w", err)
	}
	if err := os.Rename(tmp, target); err != nil {
		return gateManifest{}, fmt.Errorf("re-pin: rename: %w", err)
	}
	return m, nil
}

// RepinGateManifest is the CLI-facing wrapper (package main's
// cmd_gate.go): recompute and rewrite the manifest, returning the
// resulting file→sha16 pin map for the human's confirmation print. It
// writes no journal rows and never commits — the human commits
// gatepolicy.go and gate_manifest.json together, then restarts the
// daemon (the drift latch clears only at boot).
func RepinGateManifest(projectRoot string) (map[string]string, error) {
	m, err := repinGateManifest(projectRoot)
	if err != nil {
		return nil, err
	}
	return m.Tier0SHA16, nil
}

// gateDriftRow is one Tier-0 drift finding, journaled as
// memory_update{layer:"gate_policy", cause:"gate_source_drift"}.
type gateDriftRow struct {
	detail   string
	expected string
	actual   string
}

// checkGatePolicy is the startup drift latch (lock D1, ruling ②): rerun
// the Tier-0 hashes against the pinned manifest BEFORE any recovery below
// can fire a landing pipeline; a mismatch (or a missing Tier-0 file /
// missing or unparseable manifest) journals the drift and latches
// s.gateDrift. While latched, autoLand, loopFixPipeline, and the settle
// ladder (reachable only through autoLand — no settle.go edit needed)
// refuse with auto_land_blocked{reason:"gate_policy_drift"}: fail-closed
// until the human re-pins (odo gate re-pin) and restarts the daemon.
//
// Project-discovery rule: odo serves arbitrary user repos, so the check
// applies only when the project carries the policy (gatepolicy.go or the
// manifest present). A manifest alone (policy file deleted) IS drift;
// deleting the manifest cannot widen the rule. Both absent ⇒ not a
// gate-managed project: skip silently.
//
// Journaling (lock shapes, additive): per drifted file one
// memory_update{layer:"gate_policy", cause:"gate_source_drift", detail,
// expected_sha16, actual_sha16}, then one
// review_action{action:"gate_policy_check", cause:"ok"|"drift",
// tier0:[{file,sha16,ok}], actor:"daemon"} — once per start, on every
// active conversation. In-memory truth does not depend on journal
// success: the latch engages even when every append fails (fail-closed).
func (s *Server) checkGatePolicy(ctx context.Context) {
	type tierRow struct {
		File  string `json:"file"`
		SHA16 string `json:"sha16"`
		OK    bool   `json:"ok"`
	}

	manifestBytes, manifestErr := os.ReadFile(filepath.Join(s.projectRoot, gateManifestPath))
	var manifest gateManifest
	manifestParseErr := error(nil)
	if manifestErr == nil {
		manifestParseErr = json.Unmarshal(manifestBytes, &manifest)
	}
	_, coreStatErr := os.Stat(filepath.Join(s.projectRoot, gatePolicyGoPath))
	if os.IsNotExist(manifestErr) && os.IsNotExist(coreStatErr) {
		return // arbitrary user repo: no gate policy pinned here
	}

	var tier0 []tierRow
	var drifts []gateDriftRow
	switch {
	case manifestErr != nil:
		drifts = append(drifts, gateDriftRow{detail: gateManifestPath + " missing — the gate policy is unpinned (odo gate re-pin; commit gatepolicy.go + gate_manifest.json; restart the daemon)"})
	case manifestParseErr != nil:
		drifts = append(drifts, gateDriftRow{detail: gateManifestPath + " unparseable: " + manifestParseErr.Error()})
	}
	for _, f := range gateTier0Files {
		actual, ok := "", false
		data, err := os.ReadFile(filepath.Join(s.projectRoot, f))
		switch {
		case err != nil:
			drifts = append(drifts, gateDriftRow{detail: f + " missing/unreadable: " + err.Error()})
		default:
			actual = sha16(data)
			if f == gateManifestPath {
				// Self-reference: the manifest's own slot is empty by
				// construction; its presence was established above.
				ok = true
			} else {
				expected, pinned := "", false
				if manifestParseErr == nil {
					expected, pinned = manifest.Tier0SHA16[f]
				}
				switch {
				case !pinned:
					drifts = append(drifts, gateDriftRow{
						detail: f + " has no pinned hash in the manifest", actual: actual})
				case expected != actual:
					drifts = append(drifts, gateDriftRow{
						detail:   f + " sha16 drift: pinned " + expected + " but on-disk " + actual,
						expected: expected, actual: actual})
				default:
					ok = true
				}
			}
		}
		tier0 = append(tier0, tierRow{File: f, SHA16: actual, OK: ok})
	}

	s.gateDrift = len(drifts) > 0

	// Rows land on every active conversation (the orphan-sweep precedent):
	// the wedge is project-wide, so the evidence is readable from any
	// lane. No conversations yet (first boot of a fresh project) ⇒ no
	// rows; the latch still engages.
	convs := s.activeConversationIDs(ctx)
	for _, convID := range convs {
		for _, dr := range drifts {
			if _, err := s.store.AppendEvent(ctx, convID, store.EventMemoryUpdate, mustJSON(map[string]interface{}{
				"layer":          "gate_policy",
				"cause":          "gate_source_drift",
				"detail":         dr.detail,
				"expected_sha16": dr.expected,
				"actual_sha16":   dr.actual,
			})); err != nil {
				log.Printf("gate policy: journal drift row (conv %d): %v", convID, err)
			}
		}
		cause := "ok"
		if s.gateDrift {
			cause = "drift"
		}
		if _, err := s.store.AppendEvent(ctx, convID, store.EventReviewAction, mustJSON(map[string]interface{}{
			"action": "gate_policy_check",
			"cause":  cause,
			"tier0":  tier0,
			"actor":  "daemon",
		})); err != nil {
			log.Printf("gate policy: journal check row (conv %d): %v", convID, err)
		}
	}
}

// activeConversationIDs enumerates the project's active conversations
// (the recoverParkedGoals enumeration precedent): GetProjectByRoot →
// ListWorkstreams → GetActiveConversation per workstream. An unregistered
// project and workstreams without a conversation are silent — boot-time
// rows are best-effort, never daemon-blocking.
func (s *Server) activeConversationIDs(ctx context.Context) []int64 {
	p, err := s.store.GetProjectByRoot(ctx, s.projectRoot)
	if err != nil {
		return nil
	}
	wss, err := s.store.ListWorkstreams(ctx, p.ID)
	if err != nil {
		log.Printf("gate policy: startup scan: %v", err)
		return nil
	}
	var out []int64
	for _, w := range wss {
		if c, err := s.store.GetActiveConversation(ctx, w.ID); err == nil {
			out = append(out, c.ID)
		}
	}
	return out
}
