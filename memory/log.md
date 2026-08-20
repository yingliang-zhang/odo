# Odo Development Log

Append-only. If it's not in the log, it didn't happen.

## Phase 1 — Foundational loop & GUI (M0–M7, 2026-08-01 → 08-03 ~ 08-08, folded)

Restarted from Ananke (95% divergence, killed). Built M0 visible loop (daemon+Tauri+worktree+accept), M1 multi-workstream/steering/distill, M2 MoA review fan-out, M3 recall+wiki browser+user.md, M4 learner (memory proposal → human apply, rotate/retract ledger), M5 curator (topics+index from epoch notes, generation-2 rule), M6 keyword recall+ledger+CLI+diff-guard, M7 live block-level streaming (--mode json byte-cursor tail). GUI belts A–D (markdown, palette, split diff, a11y) + real-OMP E2E uncovered+fixed obsolete-wrapper-flags bug (`4ca78f9`). Every milestone tri/dual-model reviewed (3/3 ACCEPT) and GUI-E2E'd via cua-driver. Era ends HEAD `273cafb`.
Detail: git history (pre-fold log) + wiki/main-epoch-1.md chain.

## Phase 2 — Memory machinery & pipeline hardening (M8–M12, 2026-08-03 → 08-10, folded)

M8 skills CRUD, M9 skill distillation (3-tier gating+MoA), M10 daemon auto-distill/curate (`auto_distill: on_idle`), M11 multi-project. GUI polish waves (sidebar rail, PR1 CSS/PR2 settings/PR3 topbar, diff line numbers, a11y, clipboard paste `6ecbac0`). Repo renamed odo→odo-agent then fully rolled back (`753d553`) — never rewrite pushed history. accept_diff #2 failure root-caused: stale odo/main worktree chain + path-less ApplyDiff sweeping user files; systemic fixes `46be84c` (path-scoped commit, detached HEAD fallback, unmerged-index refusal, no-diff retire = `TestNoDiffRunRetiresWorktree`). M12: slash/shared context block, replay budget prefs, CJK bigram tokenizer (`43ea5ac`); daemon auto-distill triggers+budgets (`ed769e8`); durable odo-todo journal layer (`f17ed12`); cross-workstream matched-only recall push + miss-audit CLI (`91da261`, dogfood miss 13.6%). Era ends HEAD `c0e1325`.
Detail: git history (pre-fold log) + wiki/main-epoch-{2..7}.md chain.

---

## Slash receipt parity + bridge duplicate root fix (2026-08-11)

- Session-restore note: the prior session was killed mid-gate with its edits unwritten (worktree was clean at `ddb0f86`); all work below re-landed from the journaled evidence chain and passed gates.
- **Bridge duplicate /panel ROOT FIX** (`gui/src-tauri/src/lib.rs`): `send_to_daemon` retried on ANY round-trip error; a /panel running past `REVIEW_READ_TIMEOUT` (330s) was blindly re-sent while still executing → second full fan-out, second journaled user_message + answers (the "glm-5.2 twice" symptom; second user_message's created_at = first +330s exactly). Now retry fires ONLY when the request provably never reached the daemon (connect/write stage failure) — read-stage failures (timeout/closed/undecodable) surface the error instead of duplicating a non-idempotent command. Pure-rust unit test `retry_only_before_dispatch`; cargo test green.
- Slash dropped-seq symmetric journaling (A1): `slashContextBlock`→`slashUserMessagePayload` now thread the conversation tail's receipt → slash user_message payloads carry `replay{after_seq/first_seq/last_seq/bytes/dropped_seqs}` in the send path's exact shape; the omission marker the model saw names the journaled window (pinned by Test{Panel,Vision}DroppedSeqsReceipt).
- Vision image bytes in receipt (A2): moa gains `Image`+`ReadImage` (pre-read API; media type from extension, PNG default); `QueryWithImages` takes pre-read images. `handleVisionQuery` pre-reads BEFORE journaling (missing file fails the command with nothing journaled) and records `attachments` + decoded `image_bytes`. TestVisionImagesReceiptAndBlocks pins gateway blocks (base64==file bytes) + receipt + no-journal-on-missing.
- recallQuery seed rune-safe (A2b): `seed[:turnCap]` → `runeSafeCut` (CJK byte-cut leaked invalid UTF-8 into the recall term set; TestRecallQuerySeedRuneSafe). Same-class audit found one more site: fs grep line cap (`fstools.go`) — same fix (no direct fstools test scaffold exists; semantics identical to the pinned recallQuery case).
- gofmt hygiene (A3): 6 dirty files cleaned (adapter/omp.go, git/git_test.go, ipc/concurrent_test.go, ipc/skills_test.go, store/events.go, store/store.go) — diffs verified alignment-only before `-w`; tree now `gofmt -l` clean.
- Gates: go build+vet clean; full `go test ./...` green (ipc 217.8s); cargo test green. Daemon+bundle binaries NOT yet rebuilt — bridge fix needs an app rebuild to take effect live.

HEAD: worktree 6a7abd3c (pending accept)

## M16 auto-land + consensus fail-open fix (2026-08-11)

- Design panel (K3/GLM/DSF, grounded prompt): **3/3 DROP_SIZE_KEEP_DIR** — all size gates dropped (300-line cliff = fake precision @ 350K ctx; 3/8 real journal diffs exceeded it), new-top-dir gate kept. Three catches beyond the brief: consensus fail-open, test-weakening tripwire, supply-chain paths never auto.
- **Fail-open fix:** `consensusVerdict` accept now requires unanimity (was ceil-majority → 2 ACCEPT + 1 NEEDS_FIXES landed). Display-layer 2/3 semantics unchanged.
- `internal/ipc/autoland.go`: pref gate (`auto_apply: main`; off = silent, zero journal rows), run posture, mechanical gates (protected paths, supply-chain/manifests/lockfiles/`.odo-verify` itself, new top-level dir vs base tree, net `_test.go` assertion loss via `git.TestAssertionDelta`), mandatory `.odo-verify` re-run at worktree root (fail-closed on missing), 87K-token prompt cost breaker, grounded adversarial prompt (journaled trigger text — never agent self-report — + verify tail), unanimity, land via handleDiffAction's original path with `actor:"auto_panel"` (excluded from autonomy streaks). Every stop journals `auto_land_blocked{reason,...}`; `panel_disagreed` carries the verdicts. `branch`/`all` remain unconsumed.
- Docs: m15 O-1 supersession note, README M16 row + A1 trim, `m16-auto-land.md`, autoland header cite fix (was wrongly citing ADR-0003; the deferral contract lives in m15 O-1/README A1).
- Gates: `go vet` clean; `go test ./internal/git ./internal/ipc -count=1` green at accepted diffs #8–#10 (ipc 232s).
- Pending: diff #11 (`.odo-verify` + gate tests + `TestPanelLive` harness) awaits GUI accept → M16 panel review fires via the `ODO_PANEL_LIVE` harness. Live activation additionally needs daemon rebuild + `prefs.md auto_apply: main`.

HEAD: worktree 6a7b24cc (docs pending accept)

## M16 panel review + fix round (2026-08-11)

- Panel harness spin diagnosed: models paged files in 400-line windows and hit the 16-round tool cap with zero output (3/3 loop-exceeded). Fix = hybrid prompt: gate stack inlined verbatim with line numbers + explicit 7-read batched READ PLAN + tool-economics warning; stale worktree path in the first assembly also corrected. Rerun: 3/3 completed in 244s, 8–10K output tokens each, none truncated.
- Grounded adversarial review of M16 at HEAD 50e3e32: **3/3 NEEDS_FIXES** (spine endorsed, targeted holes). Fix round landed in one diff:
  1. `parseVerdict` FINAL verdict-line wins (first-match let a truncated/early ACCEPT override a concluding NEEDS_FIXES); truncated review now forced `needs_fixes` (`reviewVerdict`) — hardens the manual `review_diff` path too; auto-land prompt fences the diff body as data.
  2. `runVerify` allowlisted child env (`verifyEnviron`: PATH/HOME/TMPDIR + GO*/GIT_*/CGO_* passthrough) — the gate executes unreviewed agent code; daemon API keys no longer visible to it (kimi P0).
  3. `base_stale` gate: main HEAD must equal diff base_sha before any verify/panel spend — verify had attested base+diff while land applied onto current HEAD (kimi P0 / deepseek P1).
  4. `TestAssertionDelta` comment-aware: `+// assert.X` no longer nets zero against a removed live assertion (kimi's one-char evasion).
  5. `.odo-verify` doc wording: text config, first non-comment line runs — no chmod.
- New pins: TestParseVerdict (9 cases incl. early-ACCEPT regression), TestReviewVerdictTruncation, TestVerifyEnviron, base_stale blocked-path, 3 comment-evasion cases. Gates: build+vet clean; targeted tests green; full git+ipc suites (see HEAD line).
- Deferred (owner decision, recorded in m16 doc): verify-timeout process-group sweep, min panel-size policy (N=1 unanimity trivial), merge-preview verify throughput upgrade, repeated-block GUI surface, stubbed-reviewer land-path integration test.
- Restart still required for live activation: daemon binary predates all of M16 (built 08-10); journal.sqlite* reset window = quit → rm → rebuild → relaunch → `auto_apply: main` in prefs.md.

HEAD: worktree 6a7b2938 (pending accept)

## M16 activation (2026-08-11)

- main 1fb31d6 (diffs #8–#13: unanimity fix, autoland gate stack, verifyEnviron, base_stale, comment-aware TestAssertionDelta) **pushed** → origin/main current; epoch-7's "unpushed 1de583c" was already on origin, stale bullet dropped.
- `~/.odo/prefs.md`: `auto_apply: main` set (key was absent → fail-closed "off"). All other prefs untouched (`auto_distill: never` stays).
- Daemon rebuilt from committed HEAD (`go build ./... && go vet ./...` clean) → repo binary `~/Projects/odo/odo` replaced atomically (mv). Restart = SIGTERM to old daemon; GUI `send_to_daemon` → `ensure_daemon_running` auto-respawns from the new binary on next command (no manual nohup, double-spawn safe: loser fails to bind socket and exits).
- `.odo-verify` at repo root runs `go build ./... && go vet ./... && go test ./...`; its own land path is human-gated by the supply-chain gate.
- Arming verified pre-restart: rebuilt `odo autonomy audit` prints `auto-apply: main`.
- NOT done: journal.sqlite* reset (needs full Odo.app quit — the restart above is daemon-only; optional, window stays open). `odo/main` at b926226 — AdvanceBranch fast-forwards on next accept.

HEAD: origin/main post-activation (this commit)
