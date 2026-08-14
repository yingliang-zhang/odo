# DESIGN LOCK — Wave 5: Guardian risk taxonomy + structured review receipts

Tri-model design consolidated 2026-08-14 (K3/GLM/DSF). All three legs
independently reached: 5 classes (codex 4 + supply_chain), multi-label
array, pure-mechanical classifier, no single-model judge, observational
this wave (ratchet deferred), second audit table (not cross-tab).

Dissent recorded:
- DSF kebab-case class names — overruled 2/3 (odo convention is
  snake_case: `panel_mixed`, `base_stale`, `auto_revise_round`).
- DSF key `risk_classes` (plural) — overruled 2/3 (K3/GLM use
  `risk_class` as an array; the name is singular-of-concept, the value
  is an array — same pattern as `reason` which is one string).
- GLM "key absent = no risk" — overruled by K3's explicit `["none"]`
  which distinguishes rated-clean from pre-W5 unrated rows (the
  `unclassified`/`unreadable_diffs` honesty precedent in autonomy.go).
  Adopted 1/3+ (K3's explicit-none is the safer audit posture).

## Contract changes (all additive-optional-with-absence, ADR-0002 immune)

`review_action{accept|reject|moa_review|auto_land_blocked|auto_revise_round}`
gains three keys when a risk is classified:

```jsonc
{
  "risk_class":      ["credential_probe", "data_exfil"],  // []string, severity-ranked
  "risk_evidence":   {"credential_probe": "os.Getenv(\"AWS_SECRET_ACCESS_KEY\") @sse.go:18"},
                     // map[class]→one trigger artifact; omitted when ["none"] or patch unreadable
  "risk_classifier": "mechanical"                        // constant this wave
}
```

When the diff is clean: `"risk_class": ["none"]`, no `risk_evidence`,
`"risk_classifier": "mechanical"`. When the patch is unreadable: all
three keys omitted (patch_sha16 precedent). Pre-W5 rows: all three
absent (the `unclassified` audit bucket).

`AutonomyReport` gains:
```go
type RiskReport struct {
    Classes  []RiskClassReport `json:"classes"`
    Unrated  int               `json:"unrated"`  // pre-W5 rows (no risk_class key)
}
type RiskClassReport struct {
    Class        string `json:"class"`
    Description  string `json:"description"`
    Accepted     int    `json:"accepted"`       // human accept
    Rejected     int    `json:"rejected"`       // human reject
    AutoAccepted int    `json:"auto_accepted"`  // auto-panel accept
    AutoBlocked  int    `json:"auto_blocked"`   // auto_land_blocked (any reason)
}
```

## Risk class enum (snake_case, severity-ranked)

```
credential_probe     — reads secret-shaped material (env *_KEY/*_TOKEN/*_SECRET/*_PASSWORD,
                       ~/.ssh/id_*, .aws/credentials, .gnupg, keychain)
data_exfil           — co-adds local-source read + network-egress in one hunk
security_weakening   — added line weakens a control (InsecureSkipVerify, --insecure,
                       //nosec, chmod 777/666, CORS *, auth-disable). Added-lines only.
destructive         — DeletedFile in patch stats, or rm -rf/RemoveAll/rmtree/DROP TABLE/
                       git push --force/reset --hard in added lines
supply_chain         — touches a basename in autoLandSupplyChainFiles (SSOT, no second list)
none                 — no hit (explicit, distinguishes rated-clean from pre-W5 unrated)
```

Severity rank: `[credential_probe, data_exfil, destructive, security_weakening,
supply_chain, none]` (leak-cost order; element 0 is the primary class for
single-string consumers).

NOT added: `visual_only` (routing, not risk — human_gate_visual + VisualGateBlocks
already carry it); `behavior_change` (tautological — every non-docs diff changes
behavior; not mechanically detectable).

## Classification mechanism

New file `internal/ipc/risk.go`:
```go
func classifyRisk(diffText string) (classes []string, evidence map[string]string)
func riskReceipt(pathOnDisk string) map[string]interface{}
```
Pure: patch text in, classes + evidence out. Zero model spend. Added-lines
only for security_weakening (removing InsecureSkipVerify is improvement).
supply_chain reuses `autoLandSupplyChainFiles` as SSOT (no second list).

Write sites (4 + 1):
1. `journalAutoLandBlocked` (autoland.go) — all ~14 blocked reasons carry the receipt.
2. auto-land `moa_review{actor:"auto_panel"}` (autoland.go) — data already in hand.
3. manual `moa_review` (server.go handleReviewDiff) — content already in hand.
4. human/auto `accept`/`reject` (server.go handleDiffAction) — reads d.PathOnDisk.
5. `auto_revise_round` (settle.go) — the round's own diff gets its class (DSF adoption).

Relation to C0–C3: **orthogonal, not superclass, not redundant.** C-classes
measure automaticability (shape); risk classes measure hazard (content intent).
They cross-tab (a C3 small-in-scope diff reading `.env` = credential_probe).

## `odo autonomy audit` aggregation

Second table parallel to the C-class table (not a cross-tab — that needs a
diff×diff join and belongs with the ratchet wave). `ComputeAutonomy` scans
the same review_action rows; when `risk_class` is present, tally each class
into the appropriate bucket. Multi-label: a diff contributes ≤1 to each of
its classes (column sums may exceed Resolutions — printed honestly).

CLI: `risk breakdown:` block after the per-class streaks, before revert-detection.
`--json`: `risk` object. No new IPC command (`autonomy_status` returns the
enriched report — additive, GUI ignores the extra key this wave).

Ratchet: NOT this wave. Purely observational — M15 rung-0 instrument-before-gate
precedent. The future hook: promotion precondition "0 credential_probe/data_exfil
blocks in trailing N resolutions" = `report.Risk.Classes[i].AutoBlocked == 0`.

## Tests

`internal/ipc/risk_test.go` (new):
- `TestClassifyRiskPerClassTriggers` (table per class)
- `TestClassifyRiskSeverityOrder` (multi-hit → rank order)
- `TestClassifyRiskRemovedOnlyIsNotWeakening` (removing InsecureSkipVerify → ["none"])
- `TestClassifyRiskNoneForDocsDiff`
- `TestRiskReceiptUnreadablePatch` (empty map)
- `TestClassifyRiskSupplyChainMapSSOT`

`internal/ipc/autoland_test.go`:
- `TestAutoLandBlockedCarriesRiskReceipt`
- `TestAutoAcceptCarriesRiskReceipt`

`internal/ipc/server_test.go`:
- `TestHumanAcceptCarriesRiskReceipt`, `TestHumanRejectCarriesRiskReceipt`

Regression pins:
- `TestDistillRenderRiskKeysNeverFold` (byte-identical to pre-W5 whitelist)
- `TestLadderIgnoresRiskKeys` (suspension/round counts unchanged)
- `TestAutonomyRiskTalliesNeverMoveStreaks` (C0–C3 unchanged; Risk.Classes correct)
- `TestAutonomyAuditRiskTable` (CLI human + --json)

## Docs

- `docs/design/fix-int-w5-risk-taxonomy-lock.md` (this lock).
- No new ADR (ADR-0002 holds: additive payload keys only; W2 precedent).
- Header-comment updates: autoland.go journal list, settle.go contract,
  server.go resolution-payload comment.
- `docs/milestones/m18-settlement-ladder.md`: ~10-line appendix (W5 keys).
- README: one wave-history row.

## Hard rules

- No git add/commit. Touch: internal/ipc/risk.go (new), autoland.go,
  settle.go, server.go, autonomy.go, cmd_autonomy_audit.go + their tests +
  docs listed. No GUI. No new deps. No new IPC commands.
- If a locked step contradicts the code, STOP and report.
- Verify: `go build ./... && go vet ./internal/... && go test ./internal/ipc/ -count=1`
