package ipc

import (
	"context"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
	"unicode"

	"github.com/yingliang-zhang/odo/internal/adapter"
	"github.com/yingliang-zhang/odo/internal/git"
	"github.com/yingliang-zhang/odo/internal/moa"
	"github.com/yingliang-zhang/odo/internal/store"
	"github.com/yingliang-zhang/odo/internal/worktree"
)

// queuedSteer is one steer-queued message: the seq of its
// user_message{steer:true} journal row (the consumption identity, same
// precedent as parkedGoal.seq) plus the verbatim text. The seq lets the
// ledger close on every journaled steer: a later run_prompt{steer_seqs}
// consumes it, a steer_dropped row abandons it.
type queuedSteer struct {
	seq  int64
	text string
}

// steerTexts flattens a drained queue to its verbatim texts — the
// continuation/retry prompt assembly input. Nil-safe (nil in, nil out).
func steerTexts(queued []queuedSteer) []string {
	if len(queued) == 0 {
		return nil
	}
	texts := make([]string, len(queued))
	for i, q := range queued {
		texts[i] = q.text
	}
	return texts
}

// steerSeqs flattens a drained queue to its journal seqs — the
// consumption/drop linkage. Nil-safe (nil in, nil out).
func steerSeqs(queued []queuedSteer) []int64 {
	if len(queued) == 0 {
		return nil
	}
	seqs := make([]int64, len(queued))
	for i, q := range queued {
		seqs[i] = q.seq
	}
	return seqs
}

// runMeta tracks one in-flight (or recently finished) agent run in memory.
// Run bookkeeping is intentionally NOT journaled: after a daemon restart no
// agent process is alive, and pending diffs are reviewed from the journal +
// diff file alone.
type runMeta struct {
	runID          string // adapter-assigned ID
	runDirID       string // daemon-generated ID naming worktree + diff file
	adapter        string // adapter name that owns the run ("" = default)
	conversationID int64
	workstreamID   int64
	worktreePath   string
	consumed       int  // adapter events already journaled
	finished       bool // terminal adapter event (done/error) journaled
	errored        bool // terminal adapter event was agent_error
	// cancelled marks a user-killed run (the cancel op): the kill surfaces
	// in drainRun as an adapter agent_error, indistinguishable from a
	// genuine agent error without this flag — steer_dropped's cause split
	// ("cancelled" vs "errored") hinges on the distinction.
	cancelled bool
	// M7: the run's transient streaming preview (adapter event with
	// partial:true), rebuilt by each drainRun while the run is live. Never
	// journaled; handlePollEvents passes it through verbatim.
	previewEvent *adapter.AgentEvent
	// A2-lite: queued steering messages collected during the run. On run
	// completion, drainRun triggers a continuation run with these as the
	// prompt (verbatim, never LLM-summarized — Hermes verify-handoff rule).
	queuedSteers []queuedSteer
	// M16: the verbatim trigger text of this run (the send's text, or the
	// queued steers for a continuation) — the SAME string journaled as the
	// user_message payload. Copied, not re-read: the auto-land review
	// prompt grounds the panel in the user's words, never the agent's
	// self-report.
	goal string
	// M18: revise-chain runs set this to the chain's ORIGIN goal. The
	// repair brief (goal field) is the run's truthful trigger, but the
	// auto-land panel must judge the revision against the USER's words,
	// not the meta-prompt the ladder synthesized (P0 review GLM — the
	// panel otherwise sees up to 44KB of toolchain labeled "the user's
	// original instruction, verbatim"). Empty on every non-revise run.
	reviewGoal string
	// originDiffID: non-zero for revise-chain runs — the chain root diff
	// ID. Used by drainRun to journal an auto_revise_product event linking
	// the product diff back to its chain (Fix B1).
	originDiffID int64
	// run_verdict (epoch-8, outstanding #1): mechanical output tallies for
	// the terminal post-mortem. An exit-0 run is not proof of work — the
	// kimi-k3 false stop produced exactly this shape (OMP exit 0, zero
	// output, doneSummary falling back to "agent completed"). Counted in
	// drainRun's journal loop; read once at the terminal event.
	texts     int
	toolCalls int
	thinkings int
	isRetry   bool // false-stop retry run — immune to a further auto-retry
	// landPinned: bindRunLocked's landWG lifetime pin — true from the
	// registration's s.mu hold until unpinRunLandLocked runs (terminal
	// drain, retire, or Wait's sweep). The pin keeps the landWG counter
	// non-zero for the run's whole life, which makes every drainRun
	// tail Add provably precede landWG.Wait regardless of the drain's
	// call context.
	landPinned bool
	// M19 (/loop): loop provenance. Non-zero loopID marks a loop-spawned
	// run ("fix" for Mode A audits, "implement" for Mode B tasks);
	// drainRun's terminal tail skips maybeAutoLand for these and drives
	// the loop's own pipeline instead (C1).
	loopID    int64
	loopKind  string
	loopRound int
	loopTask  int
	// refusalDetail: drainRun's registration fail-fast records WHY the
	// run's diff was refused instead of registered (today only: protected
	// memory paths). loopNoDiffAfterRun upgrades its cause/detail to
	// run_tainted carrying this reason, so the suspension is actionable
	// (unlike the legacy "land manually" advice, which the executor's
	// every-actor refusal makes impossible).
	refusalDetail string
}

// Server dispatches IPC commands against the store, adapters, and worktree
// manager for one project.
type Server struct {
	store        *store.Store
	projectRoot  string
	resolvedRoot string // projectRoot after EvalSymlinks (registry exclusion compares resolved forms)
	mgr          *worktree.Manager
	// adaptersMu guards the adapter registry: writes happen pre-serve
	// (NewServer/RegisterAdapter) AND post-serve (M19 registers the
	// loop_implementer override under "loop" at first spawn) — every read
	// must go through adapterFor/adapterNamed; direct map reads would race
	// the post-serve write (Go maps: concurrent read+write is fatal).
	adaptersMu     sync.RWMutex
	adapters       map[string]adapter.Adapter // "" and "omp" = default adapter
	distillAdapter adapter.Adapter            // uses orchestrator model from prefs.md

	// mu (M11 P0) guards every piece of in-memory run bookkeeping below it:
	// runs, byConv, distilling, curating, designing, and each runMeta's
	// consumed/previewEvent/finished/errored fields. Handlers doing only
	// store/filesystem work don't take it; distill and curate explicitly
	// drop it around their multi-minute agent runs. wg tracks handleConn
	// goroutines for graceful shutdown (Wait).
	mu     sync.Mutex
	runs   map[string]*runMeta // adapter runID -> meta
	byConv map[int64]string    // conversationID -> adapter runID (active run)

	// W6 (ADR-0005): the parked-goal FIFOs, conversationID -> seq-ordered
	// goals. The journal is the authority (user_message{park:true} minus
	// run_prompt{goal_seqs}/parked_goal_dropped consumption); this is the
	// hot cache, seeded at boot by recoverParkedGoals.
	parked map[int64][]parkedGoal

	distilling map[int64]struct{} // conversations with an in-flight distill (M11 P0)
	curating   bool               // a curate pass is in flight (M11 P0)
	designing  bool               // a design_moa pass is in flight (R-W4; the curating precedent)
	wg         sync.WaitGroup     // active handleConn goroutines (M11 P0)
	curateWG   sync.WaitGroup     // detached auto-curates (M17: drained at Wait/teardown)
	// P1 (2026-08-25): fired auto-distill goroutines drove distillCore
	// (journal/wiki/git writes, multi-minute) with zero lifecycle
	// registration — a timer-fired distill outlived shutdown and kept
	// writing into a closing store. distillWG joins them in Wait
	// against an OPEN store; recoverWG joins the boot-time
	// recoverPendingDiffs read/fan-out. Both follow the curateWG drain
	// precedent (Add-before-go, joined at Wait/teardown).
	distillWG sync.WaitGroup
	recoverWG sync.WaitGroup
	// P1 (2026-08-26, the #63 verify-flake class): the recover fan-out
	// and the drainRun tail spawned the auto-land pipeline detached —
	// its accept tail (rescue snapshot's worktree git reads, land and
	// supersede journal appends, run-verdict rows) outlived the
	// returning handler and wrote into a closing store/worktree
	// (TestRecoverReFireAnchorsStoredGoal: git still writing
	// .git/objects at TempDir RemoveAll, appends landing on a closed
	// store). landWG joins every maybeAutoLand continuation in Wait,
	// after recoverWG (whose fan-out performs its Adds first) —
	// joining converts the pipeline's restart-interruptible posture
	// ONLY at shutdown; no in-flight pipeline is aborted.
	//
	// Add-vs-Wait is fenced STRUCTURALLY by the run-lifetime pin
	// (2026-08-26 repair #66, supersedes the argued-by-comment
	// "exactly two drainRun contexts" construction): bindRunLocked
	// performs landWG.Add(1) under the same s.mu hold that registers
	// the run at its byConv site, and the pin is released exactly
	// once (terminal drain, retire, or Wait's sweep). While a run
	// lives the counter never reaches zero, so a drainRun tail's Add
	// can never execute against a zero counter concurrently with
	// Wait — for every present AND future drainRun call-site
	// context. Admissions from inside a landWG pipeline (revise,
	// continuation) Add while the parent's unit still holds the
	// counter — the same zero-counter impossibility. The group also
	// joins drainRun's steer-continuation admissions
	// (startContinuationRun spawns: run_prompt/steer_dropped journal
	// rows, follow-up worktree creation) — the same flake class as
	// the accept tail.
	//
	// Drills: TestLandWGDrainPinFencesWait (a tail held provably
	// pre-Add is still joined by Wait, in order, under -race) and
	// TestManualAcceptTailJoinedByWait (the manual-accept surface).
	landWG sync.WaitGroup
	// landSealed closes run admissions (2026-08-26, the late-bind hole
	// found in repair #66's first pass). sealLandAndReleasePins sets it
	// under the SAME s.mu hold that drops the still-registered runs'
	// lifetime pins: an in-flight landWG unit (revise spawn, steer
	// continuation, ladder loop tick) reaching bindRunLocked after that
	// point is REFUSED, never pinned — an unchecked late pin would
	// outlive the sweep and hang landWG.Wait forever (no drain-capable
	// context is left to release it, and the run could never drain).
	// Refusal keeps the restart-interruptible posture: the diff stays
	// pending and the boot recovery re-pipelines it next run. Drilled by
	// TestLandSealRefusesLateAdmission.
	landSealed bool
	// M19 (/loop): loops is the liveness-only claim that a tick chain or
	// driver goroutine is driving a conversation's loop (the designing
	// precedent; the journal fold is the state). loopWG keeps blocking
	// audit/design MoA fan-outs off the IPC thread, drained at Wait()
	// like curateWG.
	loops  map[int64]struct{}
	loopWG sync.WaitGroup

	// acceptMu serializes the accept critical section (Q6 #6, previously
	// unadjudicated): two concurrent accepts — human + auto-land, or two
	// humans — share one main checkout, so apply/stage/commit/rollback on
	// interleaved patch paths would sweep each other's files. Reject takes
	// the same lock (uniform handler posture); ordering vs s.mu is
	// acceptMu → mu and never reversed.
	acceptMu sync.Mutex

	// M16 (O-1 v2): auto-land. Pipelines run CONCURRENTLY (isolated
	// verify worktrees, stateless panel HTTP); only the accept critical
	// section and the settle-revise ladder decision are serialized — a
	// racing pipeline re-adjudicates base freshness under acceptMu
	// (clean refresh or base_stale_at_land), never double-applies.
	// Lock ordering: acceptMu → mu and ladderMu → mu, each never
	// reversed; ladderMu and acceptMu may be held together (majority-accept
	// valve calls handleDiffAction while holding ladderMu), but always in
	// this order (ladderMu → acceptMu), never reversed — no deadlock.
	// ladderMu serializes the settle-revise ladder decision daemon-wide —
	// concurrent pipelines cannot fork the rounds chain
	// (settle.go: settleRevise's read-decide-spawn).
	// autoLandDone is the tests-only completion signal (nil in production).
	ladderMu     sync.Mutex
	autoLandDone chan struct{}
	// memMu is the project-wide single-writer lock for the memory file
	// family (memory.md, pins.md, archive, a batch's skill files)
	// (2026-08-25 audit P1): cross-workstream read-modify-write — a pin
	// racing a panel-gated auto-apply — lost one side to a
	// last-rename-wins race, and one batch could be consumed twice when
	// a manual apply raced the distill-side sweep. Leaf lock: holders
	// touch files and the store only, never s.mu/acceptMu/ladderMu.
	memMu sync.Mutex

	// verifyAdvised keys project roots that already got the one-time
	// .odo-verify setup advisory (verify_advisory.go) — one transcript row
	// per project per daemon boot, never per diff; a failed journal
	// append releases the key so the next blocked diff retries.
	// verifyAdviseMu serializes claim+journal+release: autoLand
	// pipelines run concurrently, and an unguarded claim would let a
	// racing second diff observe a key its owner's failed append is
	// about to release — both diffs passing without a row (panel
	// finding). The lock is held across the journal append; the path is
	// once-per-project-per-boot, so the bounded hold is noise.
	verifyAdviseMu sync.Mutex
	verifyAdvised  map[string]struct{}

	// M12 (D-auto): daemon-side auto-distill state. All guarded by mu.
	// distillKind replaces the bare distilling bit where the kind matters:
	// "manual" keeps let-finish + send refusal; auto triggers ("idle",
	// "startup", "urgent") carry a cancel-before-note handle in
	// autoInFlight that a send/steer/slash fires to abort pre-note.
	distillKind  map[int64]string            // conversationID -> kind of in-flight distill
	autoPending  map[int64]*autoPendingEntry // conversationID -> scheduled (not yet fired) auto-distill
	autoInFlight map[int64]*autoInFlight     // conversationID -> firing/fired auto-distill cancel handle
	// autoCap is the daily-cap suspension registry, keyed by PROJECT (the
	// cap is project-wide, so the storm-fix suspension is too): one entry =
	// at most one cap_suspended_until journal row per window + at most one
	// resume timer. installAutoCapLocked owns the check→journal→arm
	// critical section; gateAutoCapLocked silences activity while an entry
	// lives; stopAutoDistill tears the timers down with the rest.
	autoCap map[int64]*autoCapEntry // projectID -> daily-cap suspension
	// autoStopped closes the auto subsystem against NEW arms once
	// shutdown begins (stopAutoDistill, called from Wait/rig teardown):
	// armAutoLocked turns them away so no fresh timer can outlive the
	// drain. In-flight distills are NOT aborted — they complete and
	// drain via distillWG (joining is the fix, not cancelling).
	autoStopped bool
	slashing    map[int64]int // conversationID -> live /panel+//vision queries (fold-integrity gate)
	// Live /panel leg tally per conversation: one slice entry per
	// IN-FLIGHT consult (2026-08-25 audit P2 — a shared tally let a
	// finishing panel decrement Total under another panel's legs,
	// yielding Done > Total and a mixed leg list). Registered at fan-out,
	// removed by its own consult when done. Poll-side heartbeat only
	// (never journaled — the previewEvent precedent); the poll snapshot
	// merges the batches.
	panelProg map[int64][]*PanelProgress

	// deletingWs keys workstreams whose delete is between the idle proof
	// and the SQL commit (2026-08-25 review follow-up, closing the audit
	// P1's residual window). The flag is raised under the SAME s.mu hold
	// that proved the lane idle; every liveness-bearing start (run,
	// distill, slash/panel, loop admission, scheduled auto) checks it
	// under s.mu via guardLiveWorkstreamLocked, so a start racing the
	// commit either already registered (the delete refuses busy) or hits
	// the flag (the start refuses). Deleted-after-commit starts refuse on
	// the status half of that guard. Conversation-less lanes rise the
	// flag too (unconditionally — no idle proof exists to fold it into):
	// handleBootstrap's create runs its own guard+INSERT under one s.mu
	// hold, and a create that already beat the flag is caught by the
	// delete's commit-time re-read instead of stranding.
	deletingWs map[int64]struct{}

	// Test seams (zero in production): autoIdle overrides the prefs-resolved
	// idle (bypasses the 15s daemon floor); autoJitter caps the T2 startup
	// jitter (0.1–60s by default via rand; tests set ≤1ms); autoCurateAge
	// overrides the auto-curate age threshold (time travel without SQL);
	// autoDisabled dark-launches the whole auto subsystem for the
	// pre-M12 tests whose journals must stay byte-stable (production never
	// sets it — the M12 default is ON).
	autoDisabled  bool
	autoIdle      time.Duration
	autoJitter    time.Duration
	autoCurateAge time.Duration
	// autoCapRetry overrides the FIX-2 resume retry cadence (zero →
	// autoCapRetryDelay); tests set seconds, never the 1m default.
	autoCapRetry time.Duration
	// C11 (2026-08-22 P0): daemon-side liveness drain. drainRun used to be
	// reachable only from pollLocked — i.e. only while the GUI kept
	// polling — so a closed GUI wedged every in-flight run mid-conversation:
	// no terminal event, and for /loop fix/implement runs no
	// loopPipelineAfterRun/fireLoopTick (the loop died silently).
	// runLivenessDrain is the daemon-side counterpart of the GUI poll
	// loop: it ticks at livenessInterval (0 → livenessDrainInterval) and
	// advances every unfinished run one drain step. Atomics, not plain
	// fields, because the goroutine reads them continuously while tests
	// assign post-construction (the autoDisabled seam, but -race-clean):
	// disabled dark-launches the drain for byte-stable pre-C11 rigs —
	// production never sets it (the M12 default-ON posture).
	livenessDisabled atomic.Bool
	livenessInterval atomic.Int64 // nanoseconds; 0 → livenessDrainInterval
	// Shutdown: Wait (and rig teardown) close livenessStop once BEFORE
	// draining wg — a tick journals under s.mu and must not race the store
	// close. livenessWG joins the goroutine so teardown is deterministic.
	livenessStop chan struct{}
	livenessOnce sync.Once
	livenessWG   sync.WaitGroup
	// drainTailGate (test-only; production never sets it): drainRun
	// invokes it on each terminal finish — the diff path immediately
	// BEFORE the pipeline/tick/continuation switch, the no-diff path
	// AFTER the run's retire unregistered it and BEFORE the retry/
	// continuation/parked-goal tail. It runs with the caller's s.mu
	// hold and may RELEASE s.mu while parked (re-acquiring before
	// return): the landWG pin drill parks a finish here so the run's
	// lifetime pin — never the mutex or an s.wg frame — is what
	// provably fences Wait.
	drainTailGate func()
	// diffActionGate (test-only; production never sets it): invoked at
	// the END of handleDiffAction's success tail — after the apply/commit
	// pair, the rescue snapshot, supersedeChain, the resolution journal,
	// the run retire, and the ladder resume, before the response — with
	// s.mu not held. The manual-accept drill parks the handler here so
	// Wait provably begins while the accept frame is still mid-flight:
	// "the response was already serialized" can no longer impersonate
	// "s.wg joined the handler".
	diffActionGate func()
	// P1 #8 (2026-08-22 panel review): an N=1 "unanimous" panel is a
	// single judge with no dissent channel, so auto-land stays UNARMED at
	// one review model. The first attempt journals ONE
	// auto_land_blocked{single_judge_panel} advisory per daemon lifetime —
	// an unconfigured panel is the ordinary silent state (M20), but the
	// DEGRADED panel must not be invisible. Advisory surfaces only
	// (/panel, review_diff) stay N-unrestricted.
	singleJudgeAdvised atomic.Bool
	// P1 #9: moa legs (/panel tool loop, reviewWithModel) carry an outer
	// deadline — moa.TimeoutForModel in production. Tests override through
	// here to drill a hanging leg in wall-clock time (receiptBreachForTest
	// seam precedent: production never sets it).
	legTimeoutForTest time.Duration
	// P1 #10: ONE shared MoA client per Server. Per-leg NewClientFromEnv
	// gave every leg a FRESH client, so the client's in-flight semaphore
	// (moa defaultMaxInFlight=5) never contended — a skills/distill gate
	// batch (N proposals × M review legs) fired N×M concurrent requests
	// at the gateway. The shared client's sem is the daemon-wide cap on
	// review-lane concurrency. Lazy: built at first use so tests keep
	// per-Server env control (MOA_BASE_URL set before any moa traffic).
	moaOnce   sync.Once
	moaShared *moa.Client
	// M18 W2 item 4 fail-closed drill (autoCurateAge seam precedent):
	// non-nil ONLY in tests — assembleRunPrompt calls it between layer
	// assembly and buildPrompt to simulate a receipt diverging from the
	// injected content, proving the send/continuation/revise paths refuse
	// before any adapter start. Production never sets it.
	receiptBreachForTest func(ml *memoryLayers)
	// The slash-side counterpart of the seam above (slashctx.go): non-nil
	// ONLY in tests — slashContextBlock calls it after assembling the
	// block+receipt so /panel and /vision tests diverge the journaled
	// receipt from the injected content and drill assertSlashReceipts'
	// fail-closed refusal before any moa call. Production never sets it.
	slashReceiptBreachForTest func(receipt map[string]string)
	// Crash-window failpoints (2026-08-25 review follow-up; the
	// receiptBreachForTest seam precedent): non-nil ONLY in tests.
	// failApplyAfterMarker returns from applyResolvedBatch as if the daemon
	// died right after the (marker-first) consumption marker journaled —
	// no file written — staging the exact post-crash journal state.
	// failPinAfterReceipt does the same after the pin's journal-first
	// receipt. Both let the replay paths be drilled end to end.
	failApplyAfterMarker error
	failPinAfterReceipt  error
	// replayJournalPageSizeForTest (same test-only posture; production
	// never sets it): non-zero forces the boot replayer's journal page
	// size — the paging-equivalence drill folds an identical synthetic
	// journal in 2-row pages and requires byte-identical outcomes.
	replayJournalPageSizeForTest int
	// bootstrapPreCreateGateForTest parks handleBootstrap after its
	// resolve reads and before the guarded create, so a drill can run a
	// REAL delete through the guard-passed-but-create-pending window
	// (send = arrival, receive = release). Nil in production — the
	// failApplyAfterMarker seam precedent.
	bootstrapPreCreateGateForTest chan struct{}
	// deleteIdleProofGateForTest parks handleDeleteWorkstream with its
	// idle-proof read done (conversation state captured, possibly stale)
	// and BEFORE s.mu is taken, so a drill can commit a bootstrap's
	// conversation inside that exact sliver — the bootstrap-wins half
	// of the ordering argument the commit-time re-read closes. Same
	// handshake convention, nil in production.
	deleteIdleProofGateForTest chan struct{}
}

// NewServer builds a Server bound to one project root. ad becomes the default
// adapter ("omp"). Binding a project also registers it in the global
// ~/.odo/projects.json registry (best-effort) so the learner can find sibling
// projects for user.md recurrence checks (M4 §1).
func NewServer(st *store.Store, projectRoot string, ad adapter.Adapter, mgr *worktree.Manager) *Server {
	resolved, err := filepath.EvalSymlinks(projectRoot)
	if err != nil {
		resolved = projectRoot
	}
	s := &Server{
		store:        st,
		projectRoot:  projectRoot,
		resolvedRoot: resolved,
		adapters:     make(map[string]adapter.Adapter),
		mgr:          mgr,
		runs:         make(map[string]*runMeta),
		byConv:       make(map[int64]string),
		parked:       make(map[int64][]parkedGoal),
		distilling:   make(map[int64]struct{}),
		distillKind:  make(map[int64]string),
		autoPending:  make(map[int64]*autoPendingEntry),
		autoInFlight: make(map[int64]*autoInFlight),
		autoCap:      make(map[int64]*autoCapEntry),
		slashing:     make(map[int64]int),
		panelProg:    make(map[int64][]*PanelProgress),
		deletingWs:   make(map[int64]struct{}),
		loops:        make(map[int64]struct{}),
		autoJitter:   autoStartupJitterMax,
		livenessStop: make(chan struct{}),
	}
	s.adapters[""] = ad
	s.adapters["omp"] = ad
	ensureProjectRegistered(projectRoot)
	// Orphaned asks: a restart (crash or stray kill) strands every in-flight
	// run/slash consult — the user_message is journaled, the terminal
	// agent_done/agent_error never landed, and the GUI shows the question
	// with zero signal (2026-08-19 SIGQUIT incident). Close each with one
	// agent_error{cause:daemon_restart}. This folds the OLD journal only,
	// so it runs FIRST — before the recoveries below can journal fresh
	// expectation rows for the work they resume.
	s.recoverOrphanedRequests(context.Background())
	// Memory/pins intents (2026-08-26 memory-replay doctrine): the boot
	// replayer — project-wide, total-order, newest-receipt-per-layer —
	// restores or surfaces every replayable intent that crashed between
	// journal and file write. After the orphan sweep (which folds the OLD
	// journal only, so its form stays one row per ask), before any
	// parked-goal dequeue or loop recovery resumes runs — prompts then
	// read the repaired projection from the first resumed run. Runs once
	// per boot, single-threaded by construction; the runtime resolve IPC
	// and the apply-path retry convergence take memMu instead.
	s.replayMemoryJournal(context.Background())
	// W6: recover the durable parked-goal queues from the journal and
	// dequeue for free conversations — at daemon startup, after the store
	// is open and before serving (NewServer is the only touchable init
	// hook; main.go's wiring stays untouched). Dequeue journals a
	// run_prompt receipt, never a fresh user_message (fix-int-w6 lock), so
	// nothing here re-opens the orphan fold above.
	s.recoverParkedGoals(context.Background())
	// Steer queue: the pre-restart open steers' owning runs died with the
	// old process (memory-only queue) — close the ledger once per
	// conversation so the GUI never repopulates undeletable ghost rows.
	s.recoverOpenSteers(context.Background())
	// M19 (V7): restart recovery for /loop — mid-audit/design loops
	// (side-effect-free) re-run; mid-run loops suspend restart_mid_run.
	s.recoverLoops(context.Background())
	// Deploy witness (P0-4): the daemon once ran a stale binary ~22h
	// behind HEAD with zero signal (wiki/main-epoch-12.md). Compare the
	// binary mtime against the project repo's HEAD commit time and log a
	// prominent WARNING when stale — log-only, never fatal, and silent
	// when projectRoot is not a git repo or git fails.
	s.logDeployWitness()
	// Re-trigger auto-land for pending diffs that were stranded by a
	// daemon restart (run drained before the pipeline fired, or a restart
	// mid-pipeline). Diffs whose outcomes are already journaled are NOT
	// re-fired — the dedup filter lives with the pipeline (autoland.go,
	// recoverPendingDiffs/strandedPendingDiffs).
	s.recoverWG.Add(1)
	go func() {
		defer s.recoverWG.Done()
		s.recoverPendingDiffs(context.Background())
	}()
	// C11 ("GUI-closed loops continue"): the daemon-side counterpart of
	// the GUI's 350ms poll loop — with zero GUI traffic, runs must still
	// drain to their terminal event or /loop wedged permanently (2026-08
	// panel P0). The goroutine is joined by stopLiveness (Wait) at
	// shutdown.
	s.livenessWG.Add(1)
	go s.runLivenessDrain()
	return s
}

// deployStaleGrace is the drift the deploy witness tolerates before
// warning — rebuild jitter and clock skew below this stay silent.
const deployStaleGrace = 5 * time.Minute

// deployStaleness reports how far a daemon binary's mtime lags the project
// repo's HEAD commit time. Zero when the binary is at or after HEAD minus
// the grace — only a binary OLDER than HEAD by more than deployStaleGrace
// is stale. Pure comparison so the unit test can drive it with fake times.
func deployStaleness(binaryMtime, headCommit time.Time) time.Duration {
	if d := headCommit.Sub(binaryMtime); d > deployStaleGrace {
		return d
	}
	return 0
}

// logDeployWitness is the deploy witness body: compare os.Executable()'s
// mtime against the HEAD commit time of the project repo and log a
// prominent WARNING when the binary predates HEAD (P0-4 — a stale daemon
// serving a newer checkout is otherwise invisible; ~22h of drift went
// unnoticed, wiki/main-epoch-12.md). Every step is best-effort: a
// non-git project root, a git failure, or an unresolvable binary logs
// nothing, and no failure is ever fatal at startup.
func (s *Server) logDeployWitness() {
	exe, err := os.Executable()
	if err != nil {
		return
	}
	fi, err := os.Stat(exe)
	if err != nil {
		return
	}
	head, err := git.HeadCommitTime(s.projectRoot)
	if err != nil {
		return
	}
	if drift := deployStaleness(fi.ModTime(), head); drift > 0 {
		log.Printf("WARNING: daemon binary stale (binary mtime < HEAD commit time): %s mtime %s is %s older than HEAD commit %s of %s — rebuild/redeploy the daemon",
			exe, fi.ModTime().UTC().Format(time.RFC3339), drift.Round(time.Second),
			head.UTC().Format(time.RFC3339), s.projectRoot)
	}
}

// RegisterAdapter makes ad selectable via the send_message "adapter" field
// under the given name (e.g. "omp-alt" in tests).
func (s *Server) RegisterAdapter(name string, ad adapter.Adapter) {
	s.adaptersMu.Lock()
	s.adapters[name] = ad
	s.adaptersMu.Unlock()
}

// SetDistillAdapter sets the adapter used for distill runs (uses the
// orchestrator model from prefs.md instead of the coding model).
func (s *Server) SetDistillAdapter(ad adapter.Adapter) {
	s.distillAdapter = ad
}

// adapterFor resolves a run/request adapter name to its Adapter. Unknown
// names fall back to the default adapter.
func (s *Server) adapterFor(name string) adapter.Adapter {
	ad, ok := s.adapterNamed(name)
	if ok {
		return ad
	}
	s.adaptersMu.RLock()
	defer s.adaptersMu.RUnlock()
	return s.adapters[""]
}

// adapterNamed resolves a registry key, reporting existence (the
// send_message unknown-adapter check and M19's register-once probe —
// adapterFor's default fallback cannot express absence).
func (s *Server) adapterNamed(name string) (adapter.Adapter, bool) {
	s.adaptersMu.RLock()
	defer s.adaptersMu.RUnlock()
	ad, ok := s.adapters[name]
	return ad, ok
}

// Serve accepts connections and handles each on its own goroutine (since
// M11 P0; M0 serialized connections). Shared run bookkeeping is guarded by
// s.mu. Serve returns when the listener is closed (net.ErrClosed) or on a
// fatal accept error; in-flight handler goroutines keep running — call Wait
// to drain them during shutdown.
func (s *Server) Serve(listener net.Listener) error {
	for {
		conn, err := listener.Accept()
		if err != nil {
			if errors.Is(err, net.ErrClosed) {
				return nil
			}
			return err
		}
		s.wg.Add(1)
		go func() {
			defer s.wg.Done()
			s.handleConn(conn)
		}()
	}
}

// Wait blocks until every accepted connection's handler goroutine has
// returned — and every detached auto-curate (M17) has journaled its
// outcome. Call after Serve returns (the listener is closed) to drain
// in-flight requests — e.g. a distill still inside its 10-minute agent run —
// before shutdown cleanup kills agents and closes the journal.
// Also drains (P1): the boot-time stranded-diff recovery, every spawned
// auto-land pipeline (recover fan-out and drainRun tails), and every fired
// auto-distill, after closing the auto subsystem against new fires/arms.
func (s *Server) Wait() {
	// C11: stop the liveness drain FIRST — its tick takes s.mu and
	// journals; joined here (bounded by one in-flight tick) so no tick is
	// alive when shutdown cleanup kills agents and closes the journal.
	s.stopLiveness()
	s.wg.Wait()
	s.curateWG.Wait()
	// M19: loop driver goroutines (audit/design MoA fan-outs) drain too.
	s.loopWG.Wait()
	// P1: join the boot-time stranded-diff recovery — it reads the store
	// (and spawns the pipeline per-diff); it must not outlive the close.
	s.recoverWG.Wait()
	// Seal run admissions AND drop the lifetime pins of still-registered
	// runs in one critical section: every drain-capable context (liveness
	// tick, poll handlers, loop drivers, boot recovery) is joined above,
	// so no drain can still register a tail for these runs; an in-flight
	// pipeline's late revise/continuation spawn is refused instead of
	// pinning past the sweep (which would hang landWG.Wait forever). An
	// in-flight RUN never blocks shutdown (in-flight LAND pipelines
	// below do; the restart-interruptible posture is preserved for runs).
	s.sealLandAndReleasePins()
	// P1 (#63 verify-flake class): join every auto-land pipeline
	// spawned by the recovery fan-out or a drainRun tail — their
	// accept tails (git rescue reads, journal appends) must complete
	// against an OPEN store. The Add-vs-Wait fencing is the
	// run-lifetime pin (every drainRun tail Adds while its run's pin
	// holds the counter non-zero), never an ordering convention — see
	// bindRunLocked. Joined, never cancelled — an in-flight pipeline
	// finishes.
	s.landWG.Wait()
	// P1: close the auto-distill SUBSYSTEM first — no new fires/arms can
	// land after this (armAutoLocked turns away re-arms from in-flight
	// runs; pending timers are stopped) — then join every FIRED distill.
	// Placed last, before the caller's store teardown, so a multi-minute
	// distill still completes against an OPEN store: joining is the fix,
	// never aborting (a send/steer/slash cancel keeps current semantics).
	s.stopAutoDistill()
	s.distillWG.Wait()
}

// handleConn processes requests on a connection until EOF. Requests and
// responses are line-delimited JSON via json.Decoder/Encoder.
func (s *Server) handleConn(conn net.Conn) {
	defer conn.Close()
	dec := json.NewDecoder(conn)
	enc := json.NewEncoder(conn)
	for {
		var req Request
		err := dec.Decode(&req)
		if err != nil {
			if err != io.EOF {
				log.Printf("ipc: decode: %v", err)
			}
			return
		}
		resp := s.dispatch(context.Background(), req)
		if err := enc.Encode(resp); err != nil {
			log.Printf("ipc: encode: %v", err)
			return
		}
	}
}

func (s *Server) dispatch(ctx context.Context, req Request) Response {
	var resp Response
	var err error
	switch req.Cmd {
	case CmdBootstrap:
		resp, err = s.handleBootstrap(ctx, req)
	case CmdCreateWorkstream:
		resp, err = s.handleCreateWorkstream(ctx, req)
	case CmdListWorkstreams:
		resp, err = s.handleListWorkstreams(ctx, req)
	case CmdRenameWorkstream:
		resp, err = s.handleRenameWorkstream(ctx, req)
	case CmdDeleteWorkstream:
		resp, err = s.handleDeleteWorkstream(ctx, req)
	case CmdSendMessage:
		resp, err = s.handleSendMessage(ctx, req)
	case CmdCancel:
		resp, err = s.handleCancel(ctx, req)
	case CmdResumeParkedGoal:
		resp, err = s.handleResumeParkedGoal(ctx, req)
	case CmdDropParkedGoal:
		resp, err = s.handleDropParkedGoal(ctx, req)
	case CmdDropQueuedSteer:
		resp, err = s.handleDropQueuedSteer(ctx, req)
	case CmdPollEvents:
		resp, err = s.handlePollEvents(ctx, req)
	case CmdAcceptDiff:
		resp, err = s.handleDiffAction(ctx, req.DiffID, "accept", "", req.CommitMessage)
	case CmdRejectDiff:
		resp, err = s.handleDiffAction(ctx, req.DiffID, "reject", "", "")
	case CmdReviewDiff:
		resp, err = s.handleReviewDiff(ctx, req)
	case CmdAutonomyStatus:
		resp, err = s.handleAutonomyStatus(ctx, req)
	case CmdGetSettings:
		resp, err = s.handleGetSettings(ctx, req)
	case CmdUpdateSettings:
		resp, err = s.handleUpdateSettings(ctx, req)
	case CmdDistill:
		resp, err = s.handleDistill(ctx, req)
		if err != nil {
			// Distill failures otherwise leave no durable trace: the GUI
			// shows a 10s toast and daemon.log stays silent (the
			// 2026-08-09 E2BIG failure took a journal dig to diagnose).
			log.Printf("distill: conversation %d: %v", req.ConversationID, err)
		}
	case CmdListWiki:
		resp, err = s.handleListWiki(ctx, req)
	case CmdPendingCounts:
		resp, err = s.handlePendingCounts(ctx, req)
	case CmdListAllPendingDiffs:
		resp, err = s.handleListAllPendingDiffs(ctx, req)
	case CmdDesignMoa:
		resp, err = s.handleDesignMoa(ctx, req)
	case CmdLoopCtl:
		resp, err = s.handleLoopCtl(ctx, req)
	case CmdReadWiki:
		resp, err = s.handleReadWiki(ctx, req)
	case CmdReadMemory:
		resp, err = s.handleReadMemory(ctx, req)
	case CmdMemoryProposals:
		resp, err = s.handleMemoryProposals(ctx, req)
	case CmdApplyMemory:
		resp, err = s.handleApplyMemory(ctx, req)
	case CmdResolveHealConflict:
		resp, err = s.handleResolveHealConflict(ctx, req)
	case CmdCurate:
		resp, err = s.handleCurate(ctx, req)
	case CmdPin:
		resp, err = s.handlePin(ctx, req)
	case CmdReadPins:
		resp, err = s.handleReadPins(ctx, req)
	case CmdAutoDistillCtl:
		resp, err = s.handleAutoDistillCtl(ctx, req)
	case CmdTodoUpdate:
		resp, err = s.handleTodoUpdate(ctx, req)
	case CmdListTopics:
		resp, err = s.handleListTopics(ctx, req)
	case CmdListSkills:
		resp, err = s.handleListSkills(ctx, req)
	case CmdReadSkill:
		resp, err = s.handleReadSkill(ctx, req)
	case CmdUpdateSkill:
		resp, err = s.handleUpdateSkill(ctx, req)
	case CmdDeleteSkill:
		resp, err = s.handleDeleteSkill(ctx, req)
	case CmdLedger:
		resp, err = s.handleLedger(ctx, req)
	case CmdContradictions:
		resp, err = s.handleContradictions(ctx, req)
	case CmdSearchEvents:
		resp, err = s.handleSearchEvents(ctx, req)
	case CmdSaveAttachment:
		resp, err = s.handleSaveAttachment(ctx, req)
	case CmdOmpUsage:
		resp, err = s.handleOmpUsage(ctx, req)
	case CmdReadFile:
		resp, err = s.handleReadFile(ctx, req)
	default:
		err = fmt.Errorf("unknown command %q", req.Cmd)
	}
	if err != nil {
		return Response{OK: false, Error: err.Error()}
	}
	resp.OK = true
	return resp
}

// resolveProject resolves (creating as needed) the project row for a
// request's project root, defaulting to the daemon's bound root and rejecting
// any other path. reqRoot may be empty.
func (s *Server) resolveProject(ctx context.Context, reqRoot string) (store.Project, error) {
	root := reqRoot
	if root == "" {
		root = s.projectRoot
	}
	abs, err := filepath.Abs(root)
	if err != nil {
		return store.Project{}, fmt.Errorf("resolve project root: %w", err)
	}
	if abs != s.projectRoot {
		return store.Project{}, fmt.Errorf("daemon is bound to %s, not %s", s.projectRoot, abs)
	}
	return s.store.CreateOrGetProject(ctx, abs, filepath.Base(abs))
}

// handleBootstrap resolves (creating as needed) project + workstream +
// active conversation, and returns their IDs plus full event history and the
// latest diff — everything a client needs to restore a session. Without a
// workstream_id it targets the default "main" workstream (creating it); with
// one it targets that workstream, which must belong to the project.
func (s *Server) handleBootstrap(ctx context.Context, req Request) (Response, error) {
	p, err := s.resolveProject(ctx, req.ProjectRoot)
	if err != nil {
		return Response{}, fmt.Errorf("bootstrap: %w", err)
	}
	var w store.Workstream
	if req.WorkstreamID != 0 {
		w, err = s.store.GetWorkstream(ctx, req.WorkstreamID)
		if err != nil {
			return Response{}, fmt.Errorf("bootstrap: %w", err)
		}
		if w.ProjectID != p.ID {
			return Response{}, fmt.Errorf("bootstrap: workstream %d belongs to another project", req.WorkstreamID)
		}
		// Deleted-lane refusal (2026-08-25 review follow-up): a bootstrap
		// creates the conversation every start site keys on, so a
		// soft-deleted lane must turn callers away at the door, not after
		// a fresh idle conversation exists on it.
		if w.Status != store.WorkstreamActive {
			return Response{}, fmt.Errorf("bootstrap: workstream %d is %s", req.WorkstreamID, w.Status)
		}
	} else {
		w, err = s.store.CreateOrGetWorkstream(ctx, p.ID, "main")
		if err != nil {
			return Response{}, err
		}
	}
	c, err := s.store.GetActiveConversation(ctx, w.ID)
	if errors.Is(err, sql.ErrNoRows) {
		// Base SHA anchors stale-diff detection later; a repo with zero
		// commits simply stores NULL. The spawn runs BEFORE the critical
		// section: once s.mu is taken, nothing slow may sit between the
		// guard and the INSERT.
		baseSHA, _ := git.CurrentSHA(s.projectRoot)
		if ch := s.bootstrapPreCreateGateForTest; ch != nil {
			// Test seam (nil in production): park inside the exact
			// guard-passed-but-create-pending window so a drill can
			// slide a real delete through it.
			ch <- struct{}{}
			<-ch
		}
		// Guarded create (2026-08-25 panel finding): every read above is
		// stale the instant it returns — a delete can raise its flag,
		// commit, and clear while this goroutine spawns git. So the lane
		// is re-read and re-proven against deletingWs under the SAME
		// s.mu hold as the INSERT: either the delete won first (its
		// flag/status refuses below) or this conversation commits first
		// and the delete's commit-time re-read loses to it. The hold
		// carries one store read plus the INSERT — sub-ms SQLite, the
		// guardLiveConversationLocked precedent.
		s.mu.Lock()
		live, lerr := s.store.GetWorkstream(ctx, w.ID)
		if lerr == nil {
			lerr = s.guardLiveWorkstreamLocked(live)
		}
		if lerr != nil {
			s.mu.Unlock()
			return Response{}, fmt.Errorf("bootstrap: %w", lerr)
		}
		c, err = s.store.CreateConversation(ctx, live.ID, baseSHA)
		s.mu.Unlock()
	}
	if err != nil {
		return Response{}, err
	}
	// Repeat-switch hint: the GUI holds a cached full journal through
	// req.AfterSeq for req.ConversationID. When the hint still resolves to
	// the ACTIVE conversation, replay only the tail — switching back and
	// forth no longer resends a months-long journal on every click. A stale
	// hint (epoch fold replaced the conversation since, or plain garbage)
	// falls back to the full replay; the GUI's merge is seq-keyed so an
	// over-broad replay is deduped, never doubled.
	after := 0
	if req.AfterSeq > 0 && req.ConversationID != 0 && req.ConversationID == c.ID {
		after = req.AfterSeq
	}
	events, err := s.store.ListEvents(ctx, c.ID, after)
	if err != nil {
		return Response{}, err
	}
	// Crash-window recovery moved to the boot replayer (2026-08-26
	// memory-replay doctrine): NewServer replays the project-wide journal
	// before serving (the daemon that could strand a projection is dead by
	// definition), and a LIVE write failure converges through the apply
	// retry's engine pass. A per-bootstrap lane scan duplicated that
	// engine — removed, not replaced.

	// D8: Generate AGENTS.md so OMP reads Odo's project rules as its system
	// prompt. Odo owns the prompt prefix (memory/pins/wiki/skills); AGENTS.md
	// is the bridge that tells OMP to treat Odo's injection as authoritative.
	s.generateAgentsMD()
	return Response{
		Project:      &p,
		Workstream:   &w,
		Conversation: &c,
		Events:       events,
		AgentRunning: new(false),
		Diff:         s.latestDiffInfo(ctx, c.ID),
	}, nil
}

// generateAgentsMD writes an AGENTS.md file in .odo/ so OMP reads Odo's
// protocol rules as its system prompt. Content is the STABLE protocol
// only (the prompt-authority/journal-pull rule and the odo-todo write
// contract) — memory.md and pins.md are NOT copied here (2026-08-25
// audit P1): they already ride every prompt through the receipted
// injection layer, and the second copy refreshed only at bootstrap
// drifted behind mid-session apply/retract/pin, handing OMP rules the
// receipts don't cover. Regenerated on every bootstrap; rewritten only
// when the content actually changed.
//
// Containment (2026-08-24 tri-review P0): the write side refuses a
// symlinked AGENTS.md outright: the daemon owns the file, no legitimate
// link exists, and os.WriteFile would otherwise follow one onto an
// external file.
func (s *Server) generateAgentsMD() {
	var b strings.Builder
	b.WriteString("# AGENTS.md\n\n")
	b.WriteString("This file is auto-generated by the Odo daemon on every bootstrap.\n")
	b.WriteString("Do not edit manually — edit .odo/memory.md and .odo/pins.md instead.\n\n")
	b.WriteString("## Project Rules\n\n")
	b.WriteString("Odo injects current user memory, project rules, pins, wiki, and recalled\n")
	b.WriteString("notes in the prompt prefix. Treat them as authoritative. If OMP's\n")
	b.WriteString("hindsight memory conflicts with the prompt, follow the prompt. Older turns\n")
	b.WriteString("folded by distills are not injected verbatim — when a summary or the replay\n")
	b.WriteString("lacks a detail, run `odo journal folded|range|tail` (read-only), or\n")
	b.WriteString("`odo journal search <terms>` (keyword over every active workstream) to\n")
	b.WriteString("locate the window first, before concluding it is lost.\n\n")
	// M12 (D-todo): the agent-facing write contract for the durable plan
	// layer — discovered here because AGENTS.md is the system-prompt bridge.
	b.WriteString("## Plan todos (odo-todo)\n\n")
	b.WriteString("Maintain a durable plan for the user by emitting a fenced block inside\n")
	b.WriteString("your normal reply — the daemon parses it mechanically and journals the\n")
	b.WriteString("merge; your message text itself is never modified:\n\n")
	b.WriteString("```odo-todo\n")
	b.WriteString("[{\"op\":\"add\",\"text\":\"verify accept loop e2e\"},{\"op\":\"done\",\"id\":\"t7\"},{\"op\":\"strike\",\"id\":\"t4\"},{\"op\":\"reword\",\"id\":\"t3\",\"text\":\"…\"}]\n")
	b.WriteString("```\n\n")
	b.WriteString("Ops: `add` (new open item — single line, ≤240 bytes), `done` and `strike`\n")
	b.WriteString("(close an open item; strike = retract with record, never deletion),\n")
	b.WriteString("`reword` (fix an open item's text). Item ids are daemon-assigned (`t1`,\n")
	b.WriteString("`t2`, …): added items get their ids from the `## Current plan` section of\n")
	b.WriteString("your NEXT prompt, and only journaled ids are valid — never invent one.\n")
	b.WriteString("Open items survive epoch folds; done/struck items stay visible for the\n")
	b.WriteString("rest of the current epoch. Read-only view of the plan: `odo todo`.\n")
	b.WriteString("Never quote this block inside explanations (docs, examples, echoes) —\n")
	b.WriteString("the merge is mechanical; emit it only to change the plan.\n\n")
	odoDir := filepath.Join(s.projectRoot, ".odo")
	agentsPath := filepath.Join(odoDir, "AGENTS.md")
	// Bootstrap runs on every project/workstream switch; skip the rewrite
	// (and the mtime churn it triggers in file watchers — OMP included)
	// when the derived content is identical.
	out := b.String()
	// Write-side twin of the read guards (2026-08-24 tri-review P0; walk
	// extended 2026-08-25 review P0): the daemon owns AGENTS.md, so no
	// legitimate symlink exists at the path — and none at any component
	// below the project root either, or a checked-in .odo -> /external
	// link would have os.WriteFile follow it onto an external file.
	// Refuse and log only.
	if gerr := guardProjectWritePath(s.projectRoot, agentsPath); gerr != nil {
		log.Printf("ipc: generate AGENTS.md: %v", gerr)
		return
	}
	if existing, err := os.ReadFile(agentsPath); err == nil && string(existing) == out {
		return
	}
	if err := os.WriteFile(agentsPath, []byte(out), 0o644); err != nil {
		log.Printf("ipc: generate AGENTS.md: %v", err)
	}
}

// handleCreateWorkstream creates (or returns) the named workstream for the
// project. The name is sanitized into a git-safe branch name; the sanitized
// form is also the workstream's stored name. An empty name is an error.
func (s *Server) handleCreateWorkstream(ctx context.Context, req Request) (Response, error) {
	name := sanitizeBranchName(req.Name)
	if name == "" {
		return Response{}, fmt.Errorf("create_workstream: a usable name is required")
	}
	p, err := s.resolveProject(ctx, req.ProjectRoot)
	if err != nil {
		return Response{}, fmt.Errorf("create_workstream: %w", err)
	}
	w, err := s.store.CreateOrGetWorkstream(ctx, p.ID, name)
	if err != nil {
		return Response{}, err
	}
	return Response{Project: &p, Workstream: &w}, nil
}

// handleListWorkstreams returns every workstream for the project, oldest
// first.
func (s *Server) handleListWorkstreams(ctx context.Context, req Request) (Response, error) {
	p, err := s.resolveProject(ctx, req.ProjectRoot)
	if err != nil {
		return Response{}, fmt.Errorf("list_workstreams: %w", err)
	}
	ws, err := s.store.ListWorkstreams(ctx, p.ID)
	if err != nil {
		return Response{}, err
	}
	return Response{Project: &p, Workstreams: ws}, nil
}

// handleRenameWorkstream renames a workstream. The new name is sanitized
// to a git-safe branch name. Returns the updated workstream.
func (s *Server) handleRenameWorkstream(ctx context.Context, req Request) (Response, error) {
	name := sanitizeBranchName(req.Name)
	if name == "" {
		return Response{}, fmt.Errorf("rename_workstream: a usable name is required")
	}
	if _, err := s.resolveProject(ctx, req.ProjectRoot); err != nil {
		return Response{}, fmt.Errorf("rename_workstream: %w", err)
	}
	if err := s.store.RenameWorkstream(ctx, req.WorkstreamID, name); err != nil {
		return Response{}, fmt.Errorf("rename_workstream: %w", err)
	}
	w, err := s.store.GetWorkstream(ctx, req.WorkstreamID)
	if err != nil {
		return Response{}, fmt.Errorf("rename_workstream: refetch: %w", err)
	}
	return Response{Workstream: &w}, nil
}

// handleDeleteWorkstream soft-deletes a workstream. Refuses if the
// workstream has pending diffs. Returns the updated workstream list.
func (s *Server) handleDeleteWorkstream(ctx context.Context, req Request) (Response, error) {
	p, err := s.resolveProject(ctx, req.ProjectRoot)
	if err != nil {
		return Response{}, fmt.Errorf("delete_workstream: %w", err)
	}
	// Active-run guard (2026-08-25 audit P1): the store check below covers
	// pending diffs only — deleting a workstream whose conversation still
	// has a live run/distill/panel/loop strands the NEXT produced diff on
	// a soft-deleted workstream the Review inbox (active-only listing)
	// never shows. Conversation liveness is daemon memory, so it is
	// checked here, before the SQL layer gets a say. A store ERROR from
	// GetActiveConversation behaves as "no active conversation" — the
	// lane just gets no busy proof.
	c, cerr := s.store.GetActiveConversation(ctx, req.WorkstreamID)
	if ch := s.deleteIdleProofGateForTest; ch != nil {
		// Test seam (nil in production): park with the idle proof in
		// hand so a drill can slide a bootstrap commit between this
		// read and the flag raise below.
		ch <- struct{}{}
		<-ch
	}
	s.mu.Lock()
	if cerr == nil {
		busy := ""
		if runID, ok := s.byConv[c.ID]; ok {
			if meta := s.runs[runID]; meta != nil && !meta.finished {
				busy = "an agent run"
			}
		}
		if _, ok := s.distilling[c.ID]; ok && busy == "" {
			busy = "a distill"
		}
		if kind, ok := s.distillKind[c.ID]; ok && busy == "" {
			busy = "a " + kind + " distill"
		}
		if n := s.slashing[c.ID]; n > 0 && busy == "" {
			busy = "a slash consult"
		}
		if len(s.panelProg[c.ID]) > 0 && busy == "" {
			busy = "a panel consult"
		}
		if _, ok := s.loops[c.ID]; ok && busy == "" {
			busy = "a loop"
		}
		if _, ok := s.autoPending[c.ID]; ok && busy == "" {
			busy = "a scheduled distill"
		}
		if busy != "" {
			s.mu.Unlock()
			return Response{}, fmt.Errorf("delete_workstream: workstream %d has %s in flight — let it finish or cancel it first", req.WorkstreamID, busy)
		}
	}
	// Atomic bar on NEW starts (2026-08-25 review follow-up): the old code
	// dropped the lock here and ran the SQL delete unlocked — a start
	// sliding into that gap keyed live work onto a lane the SQL then
	// soft-deleted, and its diff stranded off the active-only Review
	// inbox. The flag rises under the SAME hold that proved the lane idle
	// (or, for a conversation-less lane, unconditionally — there is no
	// idle proof to fold it into), so start sites
	// (guardLiveWorkstreamLocked, handleBootstrap's guarded create) see
	// either their own registration (busy wins) or this flag.
	if _, ok := s.deletingWs[req.WorkstreamID]; ok {
		s.mu.Unlock()
		return Response{}, fmt.Errorf("delete_workstream: workstream %d delete already in flight", req.WorkstreamID)
	}
	s.deletingWs[req.WorkstreamID] = struct{}{}
	s.mu.Unlock()
	defer func() {
		s.mu.Lock()
		delete(s.deletingWs, req.WorkstreamID)
		s.mu.Unlock()
	}()
	if cerr == nil {
		// Journal-derived loop liveness: a daemon restart drops the
		// in-memory liveness bit, but the journal's fold still knows.
		if st, _, lerr := s.loopActiveState(ctx, c.ID); lerr == nil && st != nil {
			return Response{}, fmt.Errorf("delete_workstream: workstream %d has an active loop — stop it first", req.WorkstreamID)
		}
	} else {
		// Conversation-less lane, commit-time re-read (structurally
		// REQUIRED, not a behavior flip): the idle proof above was read
		// OUTSIDE s.mu, so a bootstrap can commit its conversation
		// between that read and the flag raise — a create the flag can
		// never refuse because it already landed. Without this re-read
		// the delete would soft-delete a lane under a live, just-born
		// conversation: the exact strand the bootstrap-side critical
		// section exists to prevent. The re-read hands the win to the
		// bootstrap and loses the DELETE instead. Parity holds
		// everywhere outside that sliver: the refusal fires only when a
		// conversation materialized mid-delete, a clean retry deletes
		// the lane exactly as before (drilled by
		// TestDeleteRetriesWhenBootstrapCommitsFirst), and a store
		// ERROR here degrades to "no conversation" — the same parity
		// the outer read keeps.
		if _, rerr := s.store.GetActiveConversation(ctx, req.WorkstreamID); rerr == nil {
			return Response{}, fmt.Errorf("delete_workstream: workstream %d gained an active conversation mid-delete — retry", req.WorkstreamID)
		}
	}
	if err := s.store.DeleteWorkstream(ctx, req.WorkstreamID); err != nil {
		return Response{}, fmt.Errorf("delete_workstream: %w", err)
	}
	ws, err := s.store.ListWorkstreams(ctx, p.ID)
	if err != nil {
		return Response{}, err
	}
	return Response{Project: &p, Workstreams: ws}, nil
}

// sanitizeBranchName maps a user-typed workstream name to a git-safe branch
// name: letters, digits, and ._- pass through, everything else becomes "-",
// runs of dashes collapse, and leading/trailing edge characters are trimmed.
// It returns "" when nothing usable remains.
func sanitizeBranchName(name string) string {
	var b strings.Builder
	lastDash := false
	for _, r := range name {
		if unicode.IsLetter(r) || unicode.IsDigit(r) || r == '.' || r == '_' {
			b.WriteRune(r)
			lastDash = false
			continue
		}
		if !lastDash {
			b.WriteByte('-')
			lastDash = true
		}
	}
	return strings.Trim(b.String(), ".-")
}

// handleSendMessage journals the user message, creates a run worktree, and
// starts the agent in it.
func (s *Server) handleSendMessage(ctx context.Context, req Request) (Response, error) {
	if req.Text == "" {
		return Response{}, fmt.Errorf("send_message: text is required")
	}
	// /panel slash command: route to MoA thinking (3 models via direct API).
	// Must be outside s.mu — the fan-out blocks for up to N×HTTP_TIMEOUT.
	// The run/distill gates live inside the handler (M12): the gate and the
	// slash-slot registration must be one critical section, or a distill
	// starting between the two folds the slash answer into last_seq unseen.
	// cmd_recall_audit.go's auditSlashCommands mirrors the four slash routes
	// below (/panel, /vision, /preview, /loop) — keep them in sync: slash
	// user_messages journal no recall key, so the audit excludes them from
	// the miss class.
	if rest := strings.TrimPrefix(strings.TrimSpace(req.Text), "/panel"); rest != strings.TrimSpace(req.Text) && (strings.HasPrefix(rest, " ") || rest == "") {
		c, err := s.checkConversation(ctx, req.ConversationID)
		if err != nil {
			return Response{}, err
		}
		return s.handlePanelQuery(ctx, &c, strings.TrimSpace(rest))
	}
	// /vision slash command: route to K3 (vision-capable) via direct API.
	// Same routing as /panel but single model (K3 only) for image analysis.
	if rest := strings.TrimPrefix(strings.TrimSpace(req.Text), "/vision"); rest != strings.TrimSpace(req.Text) && (strings.HasPrefix(rest, " ") || rest == "") {
		c, err := s.checkConversation(ctx, req.ConversationID)
		if err != nil {
			return Response{}, err
		}
		return s.handleVisionQuery(ctx, &c, strings.TrimSpace(rest), req.Attachments)
	}
	// /preview slash command: headless-chromium screenshot of a localhost
	// URL, analyzed by the SAME /vision pipeline (K3 direct API). Third
	// member of the auditSlashCommands / rulesAuditSlashCommands mirror —
	// keep all three in sync.
	if rest := strings.TrimPrefix(strings.TrimSpace(req.Text), "/preview"); rest != strings.TrimSpace(req.Text) && (strings.HasPrefix(rest, " ") || rest == "") {
		c, err := s.checkConversation(ctx, req.ConversationID)
		if err != nil {
			return Response{}, err
		}
		return s.handlePreviewQuery(ctx, &c, strings.TrimSpace(rest))
	}
	// /loop slash command: daemon-driven audit fixpoint + task pipeline
	// (M19). Fourth member of the auditSlashCommands /
	// rulesAuditSlashCommands mirror — keep all four in sync.
	if rest := strings.TrimPrefix(strings.TrimSpace(req.Text), "/loop"); rest != strings.TrimSpace(req.Text) && (strings.HasPrefix(rest, " ") || rest == "") {
		c, err := s.checkConversation(ctx, req.ConversationID)
		if err != nil {
			return Response{}, err
		}
		return s.handleLoop(ctx, &c, strings.TrimSpace(rest))
	}
	// Held for the entire handler (M11 P0): the byConv check and
	// the run-table insert must be one critical section, and adapter.Start is
	// non-blocking so the hold stays short (~200ms).
	s.mu.Lock()
	defer s.mu.Unlock()
	c, err := s.checkConversation(ctx, req.ConversationID)
	if err != nil {
		return Response{}, err
	}
	// W6: steer and park are mutually exclusive — refused pre-journal, and
	// before any gate side effects (a refused input disarms nothing).
	if req.Steer && req.Park {
		return Response{}, fmt.Errorf("send_message: steer and park are mutually exclusive")
	}
	// M12 (D-auto): user input — a send OR a steer — disarms a scheduled
	// auto-distill and cancels an in-flight AUTO distill before the note is
	// written (cancel-before-note; journaled, then the input proceeds
	// normally). A MANUAL distill keeps let-finish + refusal. This gate
	// must sit above the steer branch: steers used to bypass the distill
	// gate entirely and could journal into a folding window.
	if err := s.gateAutoDistillForSendLocked(ctx, c.ID); err != nil {
		return Response{}, err
	}
	// M8: steering is handled by handleSteering when req.Steer is set.
	if req.Steer {
		return s.handleSteering(ctx, c, req)
	}
	// W6: parking enqueues the goal (durable journal row + runtime queue)
	// instead of starting a run; a free conversation dequeues immediately.
	if req.Park {
		return s.handleParkGoal(ctx, c, req)
	}
	adName := req.Adapter
	if adName == "" {
		adName = "omp"
	}
	ad, ok := s.adapterNamed(adName)
	if !ok {
		if req.Adapter != "" {
			return Response{}, fmt.Errorf("send_message: unknown adapter %q", req.Adapter)
		}
		// Should not happen — "omp" is always registered — but fall back
		// to the default adapter for safety.
		adName, ad = "", s.adapterFor("")
	}
	var loopRunID string
	var loopRunMeta *runMeta
	if runID, ok := s.byConv[c.ID]; ok {
		if meta := s.runs[runID]; meta != nil && !meta.finished {
			// M19 (V8): a human send during a LOOP fix/implement run is
			// never refused (P1 — the refusal here pre-empted the
			// human-interleave suspension below and swallowed the send).
			// Defer the cancel until this send clears the concurrency
			// cap, then fall through: the user_message journals, then
			// suspendLoopOnHumanSendLocked suspends the loop (the
			// suspension postdates the send, per the design ordering),
			// then the new run starts normally. The cancelled run's
			// drain is inert — loopDrainActive's fold check.
			if meta.loopID != 0 {
				loopRunID, loopRunMeta = runID, meta
			} else {
				return Response{}, fmt.Errorf("send_message: agent already running for conversation %d", c.ID)
			}
		}
	}
	// M11 P3: parallelism cap — reject when too many concurrent runs.
	if cap := resolveMaxConcurrent(); s.activeRunCount() >= cap {
		return Response{}, fmt.Errorf("send_message: %d concurrent runs (cap %d)", s.activeRunCount(), cap)
	}
	if loopRunMeta != nil {
		s.cancelLoopRunLocked(loopRunID, loopRunMeta)
	}

	w, err := s.store.GetWorkstream(ctx, c.WorkstreamID)
	if err != nil {
		return Response{}, err
	}
	// Mid-delete/deleted lane bar (2026-08-25 review follow-up): without it
	// this run's diff would strand on a lane the Review inbox stopped
	// listing.
	if err := s.guardLiveWorkstreamLocked(w); err != nil {
		return Response{}, fmt.Errorf("send_message: %w", err)
	}
	// R1+R4: replay (and its cold-start complement, the resume card) are
	// assembled BEFORE journaling this message, so the replay excludes the
	// message itself (its text lands verbatim at the prompt's end).
	// assembleRunPrompt (M18 W2 item 4) additionally checks the
	// model-visible ⇔ logged closure; on a breach the payload is still
	// returned so the user_message attempt lands BEFORE the refusal.
	prompt, receiptPayload, assertErr := s.assembleRunPrompt(ctx, w.Name, c.ID, req.Text, req.Attachments...)

	// Journal the user message with attachments (spec item 5) and the
	// unified receipt closure (W2 item 4: receipt, recall, replay
	// sub-receipt, total_prompt_bytes, prompt_sha16).
	msgPayload := map[string]interface{}{"text": req.Text}
	if len(req.Attachments) > 0 {
		msgPayload["attachments"] = req.Attachments
	}
	for k, v := range receiptPayload {
		msgPayload[k] = v
	}
	ev, err := s.store.AppendEvent(ctx, c.ID, store.EventUserMessage, mustJSON(msgPayload))
	if err != nil {
		return Response{}, err
	}
	// M19 (V8): a human send without loop provenance suspends an active
	// loop (deterministic — the conversation never refuses the send).
	s.suspendLoopOnHumanSendLocked(ctx, c.ID)

	// M18 W2 item 4: fail closed BEFORE any adapter start — the attempt
	// (user_message above) and the breach (agent_error below) both stay on
	// record.
	if assertErr != nil {
		return Response{}, s.failRun(ctx, c.ID, fmt.Errorf("prompt receipt assertion failed: %w", assertErr))
	}

	// Setup failures after this point revoke the run with a journaled
	// agent_error so the chat history stays truthful.
	runDirID := worktree.NewRunID()
	wtPath, err := s.mgr.Create(runDirID)
	if err != nil {
		return Response{}, s.failRun(ctx, c.ID, fmt.Errorf("create worktree: %w", err))
	}

	runID, err := ad.Start(ctx, wtPath, prompt)
	if err != nil {
		_ = s.mgr.Remove(wtPath) // nothing to review; don't orphan a worktree
		return Response{}, s.failRun(ctx, c.ID, fmt.Errorf("start agent: %w", err))
	}

	meta := &runMeta{
		runID:          runID,
		runDirID:       runDirID,
		adapter:        adName,
		conversationID: c.ID,
		workstreamID:   c.WorkstreamID,
		worktreePath:   wtPath,
		goal:           req.Text,
	}
	// Registration and the landWG lifetime pin in one s.mu hold — the
	// structural Add-vs-Wait fence the drainRun tails rely on. The seal
	// is unreachable through Serve (Wait joins every handler connection
	// before sealing); the branch is the atomic backstop for direct
	// callers and mirrors the agent-start cleanup.
	if !s.bindRunLocked(c.ID, runID, meta) {
		_ = ad.Cancel(ctx, runID)
		_ = s.mgr.Remove(wtPath)
		return Response{}, s.failRun(ctx, c.ID, fmt.Errorf("send_message: land admissions sealed (shutting down)"))
	}
	return Response{Event: &ev}, nil
}

// memoryLayers bundles everything a prompt-building send path needs: the
// injected layer bodies, the recall path list, and the injection receipt
// (ADR-0003 inv 5: content hashes of exactly what was injected).
type memoryLayers struct {
	user           string             // ~/.odo/user.md (global principles)
	project        string             // .odo/memory.md (project behavior rules)
	pins           string             // .odo/pins.md (M5: user-authored, verbatim)
	skills         string             // M8: matched skill procedures (keyword-selected)
	skillReceipts  []skillReceiptItem // M8: per-skill path + block hash for receipt
	index          string             // wiki/index.md (M5: always-injected)
	wiki           string             // recalled epoch notes block
	cross          string             // M12 Batch 3a (D-cross): matched-only cross-workstream block
	crossItems     []crossSource      // D-cross: per-source path/origin/matched terms + chunk sha
	memoryMap      string             // R2: pull-based read-back hints (wiki/ + ledger absolute paths)
	resume         string             // R4: cold-start open-loops handoff (injected only when the replay window is empty)
	todo           string             // M12 (D-todo): durable plan block (journaled todo state, durable across folds)
	replay         string             // R1: current-epoch journal replay block
	replayFirst    int                // R1 receipt: first replayed journal seq
	replayLast     int                // R1 receipt: last replayed journal seq
	replayAfter    int                // R1 receipt: fold boundary the window starts after
	replayDropped  []int              // R1 receipt: [first,last] dropped seq window (nil without drops)
	recall         []recallItem       // M6: was []string, now per-note with matched terms
	receipt        map[string]string
	recallHeldBack int // M18 W2 item 4: notes the recall cap dropped (journaled as recall_held_back when >0)
}

// memoryLayers reads the current memory layers for the workstream and builds
// the recall items plus the sha16 receipt for every non-empty layer
// (per-note hashes cover the exact injected block, header and separator
// included). The query is the user's message text (M6 keyword recall);
// retracted notes (the journal's note-layer retraction set) are excluded.
// Layers absent/empty appear in neither.
func (s *Server) memoryLayers(ctx context.Context, wsName string, conversationID int64, query string) memoryLayers {
	pins := readPins(s.projectRoot)
	sk, skReceipts := loadSkillsForPrompt(s.projectRoot, query)
	ml := memoryLayers{
		user:          readUserMemory(),
		project:       readProjectMemory(s.projectRoot),
		pins:          pins,
		skills:        sk,
		skillReceipts: skReceipts,
		index:         readIndex(s.projectRoot),
		receipt:       map[string]string{},
	}
	retracted := s.retractedNotes(ctx, conversationID)
	m, items, noteBytes, heldBack := recallWikiNotesCapped(s.projectRoot, wsName, query, retracted, recallMemoryCap)
	ml.wiki = m
	ml.recallHeldBack = heldBack
	// M12 Batch 3a (D-cross): matched-only cross-workstream push — empty
	// unless the query earned it (no fallback tier leaks cross-ws). The
	// store threads through to the sibling retraction gate (a note
	// retracted in its own workstream is never pushed).
	if block, sources := crossWsBlock(ctx, s.store, s.projectRoot, wsName, query); block != "" {
		ml.cross = block
		ml.crossItems = sources
	}
	ml.memoryMap = memoryMapBlock(s.projectRoot)
	if ml.user != "" {
		ml.receipt["~/.odo/user.md"] = sha16([]byte(ml.user))
	}
	if ml.project != "" {
		ml.receipt[".odo/memory.md"] = sha16([]byte(ml.project))
	}
	if ml.pins != "" {
		ml.receipt[".odo/pins.md"] = sha16([]byte(ml.pins))
	}
	// M8: per-skill receipt entries (ADR-0003 inv 5).
	for _, sr := range ml.skillReceipts {
		ml.receipt[sr.path] = sr.blockHash
	}
	if ml.index != "" {
		ml.receipt["wiki/index.md"] = sha16([]byte(ml.index))
	}
	// M18 W2 item 4: the R2 read-back map is model-visible, so it must be
	// logged (synthetic key, journal#todo precedent) — without this entry
	// the pre-send closure would fail closed on a legitimate layer.
	if ml.memoryMap != "" {
		ml.receipt["odo#memory-map"] = sha16([]byte(ml.memoryMap))
	}
	for i, it := range items {
		ml.receipt[it.path] = sha16(noteBytes[i])
	}
	ml.recall = items
	// M12 Batch 3a (D-cross): per-source receipt entries — real wiki
	// paths → sha16 of the section chunk each source contributed.
	for _, src := range ml.crossItems {
		ml.receipt[src.path] = src.sha
	}
	return ml
}

// runMemoryLayers assembles the full layer stack for a run prompt in one
// pass (M6.1/R1/R4): the events list drives the recall query — the user's
// text UNION the last few current-epoch turns, so short or CJK messages
// still hit the notes the thread is about — then the memory layers, the R1
// replay, and (only when that replay window is empty) the R4 resume card
// with its receipt entry. The events list happens BEFORE the caller
// journals the new message, so the replay never contains the message whose
// text lands verbatim at the prompt's end.
// A journal READ failure is returned, never swallowed: the previous shape
// degraded to a blind prompt (no replay, no recall, no rule snapshots)
// with zero trace — the one silent hole on the fail-closed chain. Every
// caller funnels through assembleRunPrompt, which refuses the run on this
// error with a journaled agent_error.
func (s *Server) runMemoryLayers(ctx context.Context, wsName string, conversationID int64, text string) (memoryLayers, error) {
	events, lerr := s.store.ListEvents(ctx, conversationID, 0)
	if lerr != nil {
		return memoryLayers{}, fmt.Errorf("memory layers: list journal events: %w", lerr)
	}
	ml := s.memoryLayers(ctx, wsName, conversationID, recallQuery(text, events))
	// W2 item 3: materialize changed rule files. The send/continuation/
	// retry/revise callers all funnel through here BEFORE journaling the
	// user_message the prompt serves, so the snapshot rows land ahead of
	// it; a nil-but-fresh event list (zero rows) still snapshots.
	s.journalRuleSnapshots(ctx, conversationID, events)
	if events == nil {
		return ml, nil
	}
	ml.replay, ml.replayFirst, ml.replayLast, ml.replayAfter, ml.replayDropped = buildReplay(events)
	if ml.replay == "" {
		if card, notePath := buildResumeCard(s.projectRoot, wsName, events); card != "" {
			ml.resume = card
			ml.receipt[notePath+"#open-loops"] = sha16([]byte(card))
		}
	}
	// M12 (D-todo): the durable plan block renders between the resume card
	// and the replay. Empty state renders neither block nor receipt entry
	// (zero-noise default); its sha rides the synthetic journal#todo key.
	if block := renderTodoBlock(TodoStateFromEvents(events)); block != "" {
		ml.todo = block
		ml.receipt["journal#todo"] = sha16([]byte(block))
	}
	return ml, nil
}

// journalRecall serializes the recall payload for the user_message event
// (M6): fixed-marker layers first in daemon order as {"path": …} objects
// (matched_terms omitted — they are always-injected, not keyword-selected),
// then the recalled notes with optional matched_terms. M6 shape change:
// []string → []object (payload-key extension, ADR-0002 preserved).
func (ml *memoryLayers) journalRecall() []interface{} {
	var out []interface{}
	add := func(path string) {
		out = append(out, map[string]interface{}{"path": path})
	}
	if ml.user != "" {
		add("~/.odo/user.md")
	}
	if ml.project != "" {
		add(".odo/memory.md")
	}
	if ml.pins != "" {
		add(".odo/pins.md")
	}
	// M8: skill paths injected between pins and wiki index (matching buildPrompt order).
	for _, sr := range ml.skillReceipts {
		add(sr.path)
	}
	if ml.index != "" {
		add("wiki/index.md")
	}
	for _, it := range ml.recall {
		item := map[string]interface{}{"path": it.path}
		if len(it.matchedTerms) > 0 {
			item["matched_terms"] = it.matchedTerms
		}
		out = append(out, item)
	}
	// M12 Batch 3a (D-cross): cross-workstream sources carry origin +
	// matched_terms (optional-field style, ADR-0002 preserved).
	for _, src := range ml.crossItems {
		item := map[string]interface{}{"path": src.path, "origin": src.origin}
		if len(src.matchedTerms) > 0 {
			item["matched_terms"] = src.matchedTerms
		}
		out = append(out, item)
	}
	return out
}

// buildPrompt renders the agent prompt. Layers inject in ADR-0003's stable
// order (inv 6 extended, M5): user (global, durable user principles),
// project (.odo/memory.md behavior rules), pins (.odo/pins.md, verbatim),
// skills, index (wiki/index.md, always-injected), then recalled wiki notes,
// the M12 Batch 3a matched-only cross-workstream block (D-cross), the R2
// read-back map, the R4 resume card (cold start only), the M12 durable plan
// block, the R1 journal replay, attachment hints, and the user's text last
// (cache-friendly stable prefix).
// isImageFile reports whether a path has an image file extension.
func isImageFile(path string) bool {
	p := strings.ToLower(path)
	return strings.HasSuffix(p, ".png") || strings.HasSuffix(p, ".jpg") ||
		strings.HasSuffix(p, ".jpeg") || strings.HasSuffix(p, ".webp") ||
		strings.HasSuffix(p, ".gif")
}

func buildPrompt(text string, attachments []string, ml memoryLayers) string {
	var b strings.Builder
	if ml.user != "" {
		b.WriteString("## User memory (durable cross-project principles)\n\n")
		b.WriteString(ml.user)
		b.WriteString("\n\n---\n\n")
	}
	if ml.project != "" {
		b.WriteString("## Project memory (behavior rules)\n\n")
		b.WriteString(ml.project)
		b.WriteString("\n\n---\n\n")
	}
	if ml.pins != "" {
		b.WriteString("## Pins (user-authored, verbatim)\n\n")
		b.WriteString(ml.pins)
		b.WriteString("\n\n---\n\n")
	}
	if ml.skills != "" {
		b.WriteString("## Relevant skills (procedures)\n\n")
		b.WriteString(ml.skills)
		b.WriteString("\n\n---\n\n")
	}
	if ml.index != "" {
		b.WriteString("## Wiki index\n\n")
		b.WriteString(ml.index)
		b.WriteString("\n\n---\n\n")
	}
	if ml.wiki != "" {
		b.WriteString("## Prior notes (recalled)\n\n")
		b.WriteString(ml.wiki)
		b.WriteString("\n\n---\n\n")
	}
	if ml.cross != "" {
		// M12 Batch 3a (D-cross): matched-only cross-workstream push sits
		// after the home-workstream notes and before the memory map —
		// earned context, never a fallback tier.
		b.WriteString(ml.cross)
		b.WriteString("\n\n---\n\n")
	}
	if ml.memoryMap != "" {
		b.WriteString(ml.memoryMap)
		b.WriteString("\n\n---\n\n")
	}
	if ml.resume != "" {
		b.WriteString(ml.resume)
		b.WriteString("\n\n---\n\n")
	}
	if ml.todo != "" {
		// M12 (D-todo): plan state sits after the resume card and before
		// the replay — replay is the newest/churniest layer and stays last
		// (inv 6 cache-friendliness).
		b.WriteString(ml.todo)
		b.WriteString("\n\n---\n\n")
	}
	if ml.replay != "" {
		b.WriteString(ml.replay)
		b.WriteString("\n\n---\n\n")
	}
	if len(attachments) > 0 {
		// P1: image attachments use @path so OMP injects them as vision
		// content blocks (agent can see pasted screenshots/diagrams).
		// Non-image files stay as text mentions (agent reads via read tool).
		var imagePaths, otherPaths []string
		for _, p := range attachments {
			if isImageFile(p) {
				imagePaths = append(imagePaths, "@"+p)
			} else {
				otherPaths = append(otherPaths, p)
			}
		}
		if len(otherPaths) > 0 {
			fmt.Fprintf(&b, "Attached files: %s. Read them before proceeding.\n\n",
				strings.Join(otherPaths, ", "))
		}
		if len(imagePaths) > 0 {
			fmt.Fprintf(&b, "Attached images: %s\n\n",
				strings.Join(imagePaths, "\n"))
		}
	}
	b.WriteString(text)
	return b.String()
}

// promptReceiptPayload builds the unified model-visible ⇔ logged closure
// (M18 W2 item 4) that run-starting paths journal: the ADR-0003 injection
// receipt, the recall list, the held-back recall count (optional-when-absent),
// the replay structural sub-receipt (window + dropped_seqs on omission), and
// the assembled prompt's own total_prompt_bytes + prompt_sha16. The send path
// merges the map into its user_message payload; the revise user_message gains
// it intact (auto_revise marker preserved); continuation/retry anchor it on a
// review_action{action:"run_prompt"} row instead (chat surface discipline:
// the steers are already journaled, no user_message duplicate).
func promptReceiptPayload(ml memoryLayers, prompt string) map[string]interface{} {
	p := map[string]interface{}{
		"total_prompt_bytes": len(prompt),
		"prompt_sha16":       sha16([]byte(prompt)),
	}
	if jr := ml.journalRecall(); len(jr) > 0 {
		p["recall"] = jr
	}
	if ml.recallHeldBack > 0 {
		p["recall_held_back"] = ml.recallHeldBack
	}
	if len(ml.receipt) > 0 {
		p["receipt"] = ml.receipt
	}
	if ml.replay != "" {
		// R1 receipt: the covered seq range + the fold boundary it follows,
		// + the dropped window (first,last) when the cap cut older turns.
		rp := map[string]interface{}{
			"after_seq": ml.replayAfter,
			"first_seq": ml.replayFirst,
			"last_seq":  ml.replayLast,
			"bytes":     len(ml.replay),
		}
		if len(ml.replayDropped) == 2 {
			rp["dropped_seqs"] = ml.replayDropped
		}
		p["replay"] = rp
	}
	return p
}

// assertPromptReceipts is the M18 W2 item-4 fail-closed gate: model-visible
// ⇔ logged. ml is the layer stack about to be injected, prompt the exact
// bytes the adapter will receive, payload the journal-bound unified closure
// (promptReceiptPayload output — passed in because the caller journals
// exactly that map; recomputing it here would check a copy, not the record).
// It refuses (non-nil error) when:
//
//   - a non-empty layer field lacks its receipt entry;
//   - a recomputable hash disagrees — content-hash convention for file-body
//     layers (user ~/.odo/user.md, project .odo/memory.md, pins .odo/pins.md,
//     index wiki/index.md: sha16 of the body verbatim); block-hash convention
//     for rendered blocks whose bodies ARE retained on memoryLayers
//     (<note>#open-loops resume card, journal#todo plan block, odo#memory-map
//     read-back map: sha16 of the injected block). The received-vs-injected
//     drift these catch is the production gap the memory-map fix above
//     closed;
//   - presence-only layers lack their per-item keys. Wiki epoch-note items,
//     skill blocks and cross-workstream chunks are the honest local bound:
//     the note bytes are not retained on memoryLayers after the recap
//     build, the per-skill block hash is sealed by loadSkillsForPrompt and
//     the per-source chunk sha by crossWsBlock, so this gate can only
//     require each key to exist (the value was attested at injection time,
//     one code path away);
//   - the journaled totals drift: total_prompt_bytes == len(prompt) and
//     prompt_sha16 == sha16(prompt) must hold byte-exactly.
//
// The replay is EXEMPT by construction: its structural sub-receipt
// (first_seq/last_seq/after_seq/bytes/dropped_seqs) pins the journaled
// window, which is the receipt — content bytes add nothing.
//
// Exemption ledger — model-visible content NOT covered by this gate, with
// the attestation that covers it instead:
//   - distill prompt bodies: pure f(journal events), and Item 2's
//     omitted_count/omitted_first_seq/omitted_last_seq keys journal the one
//     lossy cut (the cap) with the fact. On the moa route (R-W2) the fold
//     marker additionally carries prompt_sha16 of the exact wire body —
//     closing this exemption whenever `distill_via: moa` is active; on the
//     OMP route the by-derivation attestation above remains the bound;
//   - learner rows: Item 3's memory_update{cause:"snapshot"} rows carry the
//     injected rule-file content + sha explicitly;
//   - curator: topic-page writes are journaled artifacts (the journal holds
//     the before/after);
//   - review legs: every moa_review row attests patch_sha16 of the diff at
//     hand (the panel's view is reconstructible);
//   - panel tool results: derived — recomputable from the journaled tool
//     call (same input ⇒ same output).
func assertPromptReceipts(ml memoryLayers, prompt string, payload map[string]interface{}) error {
	// Content-hash layers: key = source path, value = sha16(verbatim body).
	for _, layer := range []struct{ key, body string }{
		{"~/.odo/user.md", ml.user},
		{".odo/memory.md", ml.project},
		{".odo/pins.md", ml.pins},
		{"wiki/index.md", ml.index},
	} {
		if layer.body == "" {
			continue
		}
		got, ok := ml.receipt[layer.key]
		if !ok {
			return fmt.Errorf("prompt receipt: missing entry for %q (injected but not logged)", layer.key)
		}
		if want := sha16([]byte(layer.body)); got != want {
			return fmt.Errorf("prompt receipt: hash mismatch for %q: logged %s != injected %s", layer.key, got, want)
		}
	}
	// Block-hash layers recomputable from the retained field.
	for _, layer := range []struct{ key, body string }{
		{"odo#memory-map", ml.memoryMap},
		{"journal#todo", ml.todo},
	} {
		if layer.body == "" {
			continue
		}
		got, ok := ml.receipt[layer.key]
		if !ok {
			return fmt.Errorf("prompt receipt: missing entry for %q (injected but not logged)", layer.key)
		}
		if want := sha16([]byte(layer.body)); got != want {
			return fmt.Errorf("prompt receipt: hash mismatch for %q: logged %s != injected %s", layer.key, got, want)
		}
	}
	if ml.resume != "" {
		// The open-loops key carries its note path (<path>#open-loops);
		// scan the suffix and require exactly one, hash-matching the card.
		want := sha16([]byte(ml.resume))
		found := false
		for k, v := range ml.receipt {
			if !strings.HasSuffix(k, "#open-loops") {
				continue
			}
			found = true
			if v != want {
				return fmt.Errorf("prompt receipt: hash mismatch for %q: logged %s != injected %s", k, v, want)
			}
		}
		if !found {
			return fmt.Errorf("prompt receipt: missing entry for %q (resume card injected but not logged)", "<note>#open-loops")
		}
	}
	// Presence-only (the honest local bound — hashes sealed at injection).
	if ml.wiki != "" {
		for _, it := range ml.recall {
			if _, ok := ml.receipt[it.path]; !ok {
				return fmt.Errorf("prompt receipt: missing entry for wiki note %q (injected but not logged)", it.path)
			}
		}
	}
	for _, sr := range ml.skillReceipts {
		if _, ok := ml.receipt[sr.path]; !ok {
			return fmt.Errorf("prompt receipt: missing entry for skill block %q (injected but not logged)", sr.path)
		}
	}
	for _, src := range ml.crossItems {
		if _, ok := ml.receipt[src.path]; !ok {
			return fmt.Errorf("prompt receipt: missing entry for cross-workstream chunk %q (injected but not logged)", src.path)
		}
	}
	// The journaled totals must byte-match the adapter-bound prompt.
	if got, want := payload["total_prompt_bytes"], len(prompt); got != want {
		return fmt.Errorf("prompt receipt: total_prompt_bytes %v != prompt length %d", got, want)
	}
	if got, want := payload["prompt_sha16"], sha16([]byte(prompt)); got != want {
		return fmt.Errorf("prompt receipt: prompt_sha16 %v != sha16(prompt) %s", got, want)
	}
	return nil
}

// assembleRunPrompt is the one run-prompt assembly (M18 W2 item 4):
// runMemoryLayers + buildPrompt + the journaled closure + the fail-closed
// assertion. Every run-starting path funnels through it so the receipt the
// journal records is the prompt the adapter receives. On assertion failure
// the prompt and payload are still returned (both derived from the same ml)
// so the caller can journal the attempt before refusing — evidence-first.
// attachments ride the send path only (revise/continuation pass none).
func (s *Server) assembleRunPrompt(ctx context.Context, wsName string, conversationID int64, text string, attachments ...string) (prompt string, payload map[string]interface{}, err error) {
	ml, lerr := s.runMemoryLayers(ctx, wsName, conversationID, text)
	if lerr != nil {
		// Fail closed on a journal read failure: no blind prompt assembles,
		// and callers' existing assertErr handling journals the refusal.
		return "", nil, fmt.Errorf("journal read failed — refusing a blind prompt: %w", lerr)
	}
	if s.receiptBreachForTest != nil {
		// Test seam (autoCurateAge precedent): simulates a layer-assembly
		// bug — the receipt diverging from the injected content — to drill
		// the fail-closed gate below. nil in production.
		s.receiptBreachForTest(&ml)
	}
	prompt = buildPrompt(text, attachments, ml)
	payload = promptReceiptPayload(ml, prompt)
	if aerr := assertPromptReceipts(ml, prompt, payload); aerr != nil {
		return prompt, payload, aerr
	}
	return prompt, payload, nil
}

// memoryMapBlock is the R2 "memory map": ~6 lines telling the agent that the
// injected extracts are a keyword-selected slice and the full distilled
// knowledge base is pull-readable. Absolute MAIN-CHECKOUT paths are named
// because the agent runs in a run worktree whose checkout only carries
// tracked files — wiki/ notes and .odo/ledger.md live outside it. Returns ""
// when the project has neither a wiki dir nor a ledger yet (fresh project:
// nothing to pull, no noise).
func memoryMapBlock(projectRoot string) string {
	wikiDir := filepath.Join(projectRoot, "wiki")
	ledger := ledgerPath(projectRoot)
	wikiOK := false
	if st, err := os.Stat(wikiDir); err == nil && st.IsDir() {
		wikiOK = true
	}
	ledgerOK := false
	if st, err := os.Stat(ledger); err == nil && !st.IsDir() {
		ledgerOK = true
	}
	if !wikiOK && !ledgerOK {
		return ""
	}
	var b strings.Builder
	b.WriteString("## Memory read-back (pull-based recall)\n\n")
	b.WriteString("The extracts above are a keyword-selected slice. The full distilled knowledge base lives in the MAIN project checkout (this worktree only has tracked files):\n")
	if wikiOK {
		fmt.Fprintf(&b, "- Wiki notes, topic pages, index: `%s` (plain markdown, read directly)\n", wikiDir)
	}
	if ledgerOK {
		fmt.Fprintf(&b, "- Per-epoch metrics ledger: `%s`\n", ledger)
	}
	b.WriteString("From the main checkout you can also run `odo wiki read <name>` (e.g. `odo wiki read index`, `odo wiki read ledger`).\n\n")
	b.WriteString("Folded-out journal turns are NOT injected above, and every summary layer is lossy: when a summary or the replay lacks a detail, query the journal first (read-only; works from this worktree) — `odo journal search <terms>` (keyword over every active workstream, to locate the seq window), then `odo journal folded`, `odo journal range A B`, or `odo journal tail N` — instead of concluding it is lost.")
	return b.String()
}

// handleSteering journals a steering message onto the conversation's
// ACTIVE run. The text is queued in the run's meta; when the run
// completes, drainRun auto-starts a continuation run with the queued
// texts as the prompt (A2-lite: queue-continuation). Previously this
// called adapter.Send which wrote to steering.txt — a dead path the
// wrapper never reads. The UI showed "delivered" but the agent never saw
// the message. Now the message is honestly queued and delivered via a
// fresh run on completion.
//
// Fail-closed (Hermes steer queue): with no live run there is no queue
// to receive the steer — a journal-only steer would orphan (nothing ever
// consumes or drops it), and a meta whose run already finished would
// strand the text in a dead struct. Both are refused pre-journal, and
// the refusal is NOT a human send (suspendLoopOnHumanSendLocked stays
// out of it).
// Caller holds s.mu (called from handleSendMessage).
func (s *Server) handleSteering(ctx context.Context, c store.Conversation, req Request) (Response, error) {
	runID, ok := s.byConv[c.ID]
	meta := s.runs[runID]
	if !ok || meta == nil || meta.finished {
		return Response{}, fmt.Errorf("steer: no active run for conversation %d", c.ID)
	}
	msgPayload := map[string]interface{}{"text": req.Text, "steer": true}
	if len(req.Attachments) > 0 {
		msgPayload["attachments"] = req.Attachments
	}
	ev, err := s.store.AppendEvent(ctx, c.ID, store.EventUserMessage, mustJSON(msgPayload))
	if err != nil {
		return Response{}, err
	}
	// M19 (V8): a steer is a human send — it suspends an active loop.
	s.suspendLoopOnHumanSendLocked(ctx, c.ID)
	// A2-lite: queue the steering text for the continuation run, keyed by
	// its journal seq so the ledger can close on it (run_prompt{steer_seqs}
	// consumption, steer_dropped abandonment). The agent will see it as
	// the prompt of the next run, not mid-run.
	meta.queuedSteers = append(meta.queuedSteers, queuedSteer{seq: int64(ev.Seq), text: req.Text})
	return Response{Event: &ev}, nil
}

// handleDropQueuedSteer is the manual steer-queue drop — drop_parked_goal's
// twin for the transient steer queue: the human's decision is journaled as
// review_action{action:"steer_dropped", steer_seq} (no actor, no cause —
// the parked_goal_dropped shape) and the entry leaves the active run's
// queue, so the drain's continuation never sees it. An absent seq means
// the steer was already consumed or dropped — a reconciled state (the
// poll reflects it), so the refusal journals nothing.
func (s *Server) handleDropQueuedSteer(ctx context.Context, req Request) (Response, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	c, err := s.checkConversation(ctx, req.ConversationID)
	if err != nil {
		return Response{}, err
	}
	runID, ok := s.byConv[c.ID]
	meta := s.runs[runID]
	if ok && meta != nil && !meta.finished {
		for i, q := range meta.queuedSteers {
			if q.seq != req.SteerSeq {
				continue
			}
			if _, err := s.store.AppendEvent(ctx, c.ID, store.EventReviewAction, mustJSON(map[string]interface{}{
				"action":    "steer_dropped",
				"steer_seq": req.SteerSeq,
			})); err != nil {
				return Response{}, err
			}
			meta.queuedSteers = append(meta.queuedSteers[:i], meta.queuedSteers[i+1:]...)
			return Response{}, nil
		}
	}
	return Response{}, fmt.Errorf("no queued steer with seq %d", req.SteerSeq)
}

// handleCancel SIGKILLs the conversation's active run through its adapter
// and journals agent_error{cancelled by user} so the chat history records
// the user's stop. The run is deliberately left unfinished: the normal
// drain path observes the dead process on the next poll, journals the
// adapter's own terminal event, and extracts whatever partial diff exists
// (ADR-0001: partial changes stay reviewable).
func (s *Server) handleCancel(ctx context.Context, req Request) (Response, error) {
	// Held for the entire handler (M11 P0): run-table reads, adapter.Cancel,
	// and the journal write stay consistent against concurrent drains.
	s.mu.Lock()
	defer s.mu.Unlock()
	c, err := s.checkConversation(ctx, req.ConversationID)
	if err != nil {
		return Response{}, err
	}
	// Primary single run.
	runID, ok := s.byConv[c.ID]
	meta := s.runs[runID]
	if ok && meta != nil && !meta.finished {
		if err := s.adapterFor(meta.adapter).Cancel(ctx, runID); err != nil {
			return Response{}, fmt.Errorf("cancel: %w", err)
		}
		// Noted before the kill's drain lands: the adapter's terminal event
		// also arrives as agent_error, so without this mark the drain could
		// not tell a user kill from a genuine agent error (steer_dropped's
		// cause reads it).
		meta.cancelled = true
		if _, err := s.store.AppendEvent(ctx, c.ID, store.EventAgentError,
			mustJSON(map[string]interface{}{"error": "cancelled by user"})); err != nil {
			return Response{}, err
		}
		return Response{}, nil
	}
	return Response{}, fmt.Errorf("cancel: no active run for conversation %d", c.ID)
}

// handlePollEvents drains finished-run adapter events into the journal,
// extracts each run's diff once, then returns journal events after afterSeq.
// The two-phase shape keeps s.mu off the trailing diff reads: latestDiffInfo
// and pendingDiffInfos hit only the store and diff FILES (handleBootstrap
// already calls both without s.mu), and at the 350ms running cadence every
// poll re-read every pending diff from disk under the global lock, stalling
// drains, admissions, and switches daemon-wide.
func (s *Server) handlePollEvents(ctx context.Context, req Request) (Response, error) {
	base, convID, err := s.pollLocked(ctx, req)
	if err != nil {
		return Response{}, err
	}
	base.Diff = s.latestDiffInfo(ctx, convID)
	base.Diffs = s.pendingDiffInfos(ctx, convID)
	return base, nil
}

// pollLocked is the s.mu-critical core of handlePollEvents (M11 P0):
// drainRun advances the consumed cursor and sets finished, so concurrent
// pollers of the same run must serialize — without this two polls journal
// the same adapter events. It returns the Response minus the diff reads,
// plus the validated conversation id for the caller's unlocked tail.
func (s *Server) pollLocked(ctx context.Context, req Request) (Response, int64, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	c, err := s.checkConversation(ctx, req.ConversationID)
	if err != nil {
		return Response{}, 0, err
	}

	if runID, ok := s.byConv[c.ID]; ok {
		if meta := s.runs[runID]; meta != nil && !meta.finished {
			if err := s.drainRun(ctx, meta); err != nil {
				return Response{}, 0, err
			}
		}
	}

	events, err := s.store.ListEvents(ctx, c.ID, req.AfterSeq)
	if err != nil {
		return Response{}, 0, err
	}
	agentRunning := false
	if runID, ok := s.byConv[c.ID]; ok {
		if meta := s.runs[runID]; meta != nil && !meta.finished {
			agentRunning = true
		}
	}
	var preview *adapter.AgentEvent
	if runID, ok := s.byConv[c.ID]; ok {
		if meta := s.runs[runID]; meta != nil && !meta.finished {
			preview = meta.previewEvent
		}
	}
	// /panel heartbeat: hand the poller a MERGED copy of every in-flight
	// consult's tally — legs bump Done under s.mu and the response encodes
	// after this handler drops the lock, so shared state would race.
	var panelProg *PanelProgress
	if batches := s.panelProg[c.ID]; len(batches) > 0 {
		merged := &PanelProgress{}
		for _, b := range batches {
			merged.Done += b.Done
			merged.Total += b.Total
			merged.Legs = append(merged.Legs, b.Legs...)
		}
		panelProg = merged
	}
	return Response{
		Events:        events,
		AgentRunning:  new(agentRunning),
		Preview:       preview,
		Streaming:     preview != nil,
		PanelProgress: panelProg,
	}, c.ID, nil
}

// startContinuationRun (A2-lite) starts a fresh run for a conversation
// after the previous run completed with queued steering messages. The
// queued texts are joined as the prompt (verbatim, never LLM-summarized).
// Memory layers are re-read fresh (ADR-0003: each run gets current state).
// Runs in a goroutine because drainRun holds s.mu; this function takes
// its own lock.
func (s *Server) startContinuationRun(conversationID, workstreamID int64, queued []queuedSteer) {
	s.startFollowupRun(conversationID, workstreamID, steerTexts(queued), steerSeqs(queued), false)
}

// startRetryRun is the run_verdict retry entry (epoch-8): same machinery
// as a steer-continuation, but the new run is marked isRetry so its own
// false stop cannot chain another (loop bound = exactly one retry). The
// retry spawn used to live here as startRetryRun; round-2 panel moved
// false-stop retries into drainRun's synchronous admission below, so only
// the continuation path rides the goroutine entry now.
//
// startFollowupRun is the goroutine entry (continuation path): admission
// failure still closes the ledger — steerSeqs reaching a non-admitted run
// are journaled steer_dropped inside startFollowupRunLocked.
func (s *Server) startFollowupRun(conversationID, workstreamID int64, queuedTexts []string, steerSeqs []int64, isRetry bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.startFollowupRunLocked(conversationID, workstreamID, queuedTexts, steerSeqs, isRetry)
}

// startFollowupRunLocked performs the admission + start with the caller
// holding s.mu and returns whether the run was admitted and, if not, the
// reason. drainRun's false-stop retry calls this SYNCHRONOUSLY (round-2
// panel: goroutine admission let a user send win s.mu in the retire→retry
// window and silently veto the retry, while the ledger had already
// journaled retry_fired=true — a ledger lie AND a journal-silent wedge).
// Under the drain's own lock a send cannot interleave, so the verdict row
// reflects the true admission outcome.
//
// steerSeqs carries the drained steers' journal seqs (nil for a steerless
// retry). Admission consumes them — the run_prompt row links steer_seqs —
// and every refusal BEFORE that row lands closes the ledger with a
// steer_dropped row HERE (this function is the last place both the seqs
// and the refusal reason coexist — a goroutine entry would otherwise lose
// them silently). A refusal AFTER the receipt closes nothing: consumption
// is the steers' exactly-one ending and a drop row would contradict it.
func (s *Server) startFollowupRunLocked(conversationID, workstreamID int64, queuedTexts []string, steerSeqs []int64, isRetry bool) (admitted bool, dropReason string) {
	ctx := context.Background()
	prompt := strings.Join(queuedTexts, "\n\n")

	// Ledger-close closure: a non-admitted run abandons its steers; the
	// drop is journaled with the refusal as cause (a no-op for steerless
	// calls, so every return below routes through it uniformly).
	drop := func(reason string) (bool, string) {
		s.journalSteersDropped(ctx, conversationID, steerSeqs, reason)
		return false, reason
	}

	// Land-seal first (the second #66 repair): the daemon is draining
	// (Wait / rig teardown). A late continuation racing shutdown from an
	// in-flight pipeline must refuse before ANY side effect — journal,
	// worktree, agent, pin. The drained steers close as dropped with the
	// seal as cause (a ledger truth, like every other refusal).
	if s.landSealed {
		return drop("land_sealed")
	}
	// Re-check: don't start if a run is already active for this conversation
	// (user may have sent a normal message in the window between drain and
	// this goroutine).
	if runID, ok := s.byConv[conversationID]; ok {
		if meta := s.runs[runID]; meta != nil && !meta.finished {
			return drop("active_run") // a new run already started; drop the continuation
		}
	}
	// Respect the concurrency cap.
	if cap := resolveMaxConcurrent(); s.activeRunCount() >= cap {
		log.Printf("a2-continuation: skipping — concurrency cap %d reached", cap)
		return drop("concurrency_cap")
	}

	// M11 P0: don't start a continuation during distill — same guard as
	// handleSendMessage (3 places). The distill's unlocked window would let
	// a continuation run journal events into the epoch the distill is rolling.
	if _, ok := s.distilling[conversationID]; ok {
		log.Printf("a2-continuation: skipping — distill in progress for conversation %d", conversationID)
		return drop("distill_active")
	}

	w, err := s.store.GetWorkstream(ctx, workstreamID)
	if err != nil {
		log.Printf("a2-continuation: get workstream: %v", err)
		return drop("workstream_lookup")
	}
	// Mid-delete/deleted lane bar (2026-08-25 review follow-up).
	if err := s.guardLiveWorkstreamLocked(w); err != nil {
		log.Printf("a2-continuation: %v", err)
		return drop("workstream_deleted")
	}

	// R1: continuation runs replay the current epoch too — the previous
	// run's agent_text is in the journal and the steering agent must see it.
	fullPrompt, receiptPayload, assertErr := s.assembleRunPrompt(ctx, w.Name, conversationID, prompt)
	if assertErr != nil {
		// M18 W2 item 4: fail closed, no silent drop — the breach is a
		// journaled agent_error, the adapter never starts.
		_ = s.failRun(ctx, conversationID, fmt.Errorf("prompt receipt assertion failed: %w", assertErr))
		return drop("receipt_assert_failed")
	}

	// M18 W2 item 4: the continuation/retry anchors the same unified
	// receipt closure on a review_action{action:"run_prompt"} row — the
	// steers are already journaled, so NO user_message duplicate is
	// written (chat surface discipline). actor:auto_panel marks it
	// pipeline mechanics: the fold render excludes it (Item 1 whitelist).
	// steer_seqs is the consumption linkage: admitted steers are claimed
	// by this run (a retry's own goal has no seq — only its steers do).
	// The key is omitted when empty: a steerless row stays byte-identical.
	origin := "continuation"
	if isRetry {
		origin = "retry"
	}
	row := map[string]interface{}{"action": "run_prompt", "actor": autoActor, "origin": origin}
	if len(steerSeqs) > 0 {
		row["steer_seqs"] = steerSeqs
	}
	for k, v := range receiptPayload {
		row[k] = v
	}
	if _, err := s.store.AppendEvent(ctx, conversationID, store.EventReviewAction, mustJSON(row)); err != nil {
		// Journal-first: starting unlogged would break evidence-before-action.
		log.Printf("a2-continuation: journal run_prompt: %v", err)
		return drop("journal_run_prompt")
	}

	// Past this point the receipt has CONSUMED the steers in the ledger: a
	// failure here must return WITHOUT journalSteersDropped — closing them
	// again would mark one steer both consumed (run_prompt{steer_seqs}) and
	// abandoned (steer_dropped), breaking the exactly-one-ending invariant
	// (panel diff #9). The pre-receipt precedent (HEAD: plain returns).
	runDirID := worktree.NewRunID()
	wtPath, err := s.mgr.Create(runDirID)
	if err != nil {
		log.Printf("a2-continuation: create worktree: %v", err)
		return false, "worktree_create"
	}

	ad := s.adapterFor("") // default adapter
	runID, err := ad.Start(ctx, wtPath, fullPrompt)
	if err != nil {
		_ = s.mgr.Remove(wtPath) // nothing to review; don't orphan a worktree
		log.Printf("a2-continuation: start agent: %v", err)
		return false, "agent_start"
	}

	// Registration and the landWG lifetime pin in one s.mu hold
	// (bindRunLocked) — the admission runs inside a poll drain or a
	// landWG pipeline; the pin fences the drain tails either way. The
	// refusal is unreachable (the early seal gate above and this bind
	// share one s.mu hold with sealLandAndReleasePins) — it is the
	// atomic backstop, and post-receipt the steers stay CONSUMED (no
	// drop row: the exactly-one-ending invariant).
	if !s.bindRunLocked(conversationID, runID, &runMeta{
		runID:          runID,
		runDirID:       runDirID,
		adapter:        "",
		conversationID: conversationID,
		workstreamID:   workstreamID,
		worktreePath:   wtPath,
		goal:           prompt, // the joined queued steers, verbatim
		isRetry:        isRetry,
	}) {
		_ = ad.Cancel(ctx, runID)
		_ = s.mgr.Remove(wtPath)
		return false, "land_sealed"
	}
	return true, ""
}

// journalSteersDropped closes the steer ledger on an abandoned batch:
// review_action{action:"steer_dropped", steer_seqs, cause} with actor
// autoPanel (pipeline mechanics, like run_prompt — the drain and the
// followup admission gates journal it, never the human). A journaled
// steer must end exactly one of two ways — consumed by a run_prompt's
// steer_seqs or explicitly dropped here — so the GUI's journal-derived
// queue never shows a ghost. An empty seqs slice no-ops: paths shared by
// steerless retries route through this uniformly. Journal failures log
// but never wedge the drain (journalAuto's posture).
func (s *Server) journalSteersDropped(ctx context.Context, conversationID int64, seqs []int64, cause string) {
	if len(seqs) == 0 {
		return
	}
	if _, err := s.store.AppendEvent(ctx, conversationID, store.EventReviewAction, mustJSON(map[string]interface{}{
		"action":     "steer_dropped",
		"actor":      autoActor,
		"steer_seqs": seqs,
		"cause":      cause,
	})); err != nil {
		log.Printf("steer-queue: journal steer_dropped for conversation %d: %v", conversationID, err)
	}
}

// deriveOpenSteers folds one conversation's seq-ascending journal
// (ListEvents(_, _, 0)) to the steers no row ever closed:
// user_message{steer:true, non-empty text} minus run_prompt{steer_seqs}
// consumption and steer_dropped{steer_seq | steer_seqs} closure. The Go
// mirror of the GUI's journal fold (gui/src/steer_queue.ts
// deriveSteerQueue) — the two MUST read the same journal the same way.
// Its only daemon consumer is the startup sweep below.
func deriveOpenSteers(events []store.Event) []int64 {
	var open []int64
	consumed := map[int64]bool{}
	for _, ev := range events {
		switch ev.Type {
		case store.EventUserMessage:
			var p struct {
				Text  string `json:"text"`
				Steer bool   `json:"steer"`
			}
			if json.Unmarshal(ev.Payload, &p) != nil || !p.Steer || strings.TrimSpace(p.Text) == "" {
				continue
			}
			open = append(open, int64(ev.Seq))
		case store.EventReviewAction:
			var p struct {
				Action    string  `json:"action"`
				SteerSeq  *int64  `json:"steer_seq"`
				SteerSeqs []int64 `json:"steer_seqs"`
			}
			if json.Unmarshal(ev.Payload, &p) != nil {
				continue
			}
			switch p.Action {
			case "run_prompt":
				for _, s := range p.SteerSeqs {
					consumed[s] = true
				}
			case "steer_dropped":
				if p.SteerSeq != nil {
					consumed[*p.SteerSeq] = true
				}
				for _, s := range p.SteerSeqs {
					consumed[s] = true
				}
			}
		}
	}
	var out []int64
	for _, s := range open {
		if !consumed[s] {
			out = append(out, s)
		}
	}
	return out
}

// recoverOpenSteers runs at NewServer (the recoverParkedGoals precedent):
// a daemon restart strands every queued steer — runMeta.queuedSteers is
// memory-only by design, so the runs owning the journal's open steers
// died with the old process and no drain will ever close them. Without
// this sweep the GUI's journal-derived queue repopulates those rows as
// immortal, undeletable entries (handleDropQueuedSteer refuses: no active
// run owns them — panel diff #9 finding 1). The sweep closes the ledger
// once per conversation as a batched steer_dropped{cause:"daemon_restart"},
// so the rows die with a visible reason; a second boot folds the closure
// rows and no-ops (idempotent). Best-effort: failures log per
// conversation and never stop the daemon from serving.
func (s *Server) recoverOpenSteers(ctx context.Context) {
	p, err := s.store.GetProjectByRoot(ctx, s.projectRoot)
	if err != nil {
		if !errors.Is(err, sql.ErrNoRows) {
			// Unregistered project (fresh repo) — nothing to close, no noise.
			log.Printf("steer-queue: startup scan: %v", err)
		}
		return
	}
	convs, err := s.store.ListActiveConversations(ctx, p.ID)
	if err != nil {
		log.Printf("steer-queue: startup scan: %v", err)
		return
	}
	for _, c := range convs {
		events, err := s.store.ListEvents(ctx, c.ID, 0)
		if err != nil {
			log.Printf("steer-queue: startup scan conversation %d: %v", c.ID, err)
			continue
		}
		if open := deriveOpenSteers(events); len(open) > 0 {
			s.journalSteersDropped(ctx, c.ID, open, "daemon_restart")
		}
	}
}

// deriveOrphanedRequest folds one conversation's journal for an unanswered
// ask: a user_message that created an expectation (a run or slash consult
// whose terminal agent_done/agent_error was due) with no terminal row
// behind it — the crash/kill shape. The expectation-carrier test is
// field-keyed (the settle.go originGoal precedent — never text-keyed):
// every user_message expects a terminal EXCEPT steers (closed by
// run_prompt/steer_dropped rows), parked goals (closed by run_prompt
// receipts; the park decision carries "park":true), and /loop control
// rows (context_scope "/loop", closed by loop event rows). Parse failures
// skip the row (recoverOpenSteers precedent).
func deriveOrphanedRequest(events []store.Event) (pending bool) {
	for _, ev := range events {
		switch ev.Type {
		case store.EventUserMessage:
			var p struct {
				Steer bool   `json:"steer"`
				Park  bool   `json:"park"`
				Scope string `json:"context_scope"`
			}
			if json.Unmarshal(ev.Payload, &p) != nil {
				continue
			}
			if p.Steer || p.Park || p.Scope == "/loop" {
				continue
			}
			pending = true
		case store.EventAgentDone, store.EventAgentError:
			pending = false
		}
	}
	return pending
}

// recoverOrphanedRequests runs at NewServer (the recoverOpenSteers
// precedent) BEFORE the other boot recoveries: a daemon restart strands
// every in-flight ask — the journaled user_message never got its terminal
// agent_done/agent_error and the GUI shows the question with zero signal
// (2026-08-19: a stray SIGQUIT killed the daemon mid-/panel; WAL showed
// the question, nothing after). The sweep closes each stranded ask with
// one agent_error{cause:"daemon_restart"} so the GUI renders a failure
// bubble instead of silence; the closure row clears the fold on the next
// boot (idempotent). Best-effort like the sibling sweeps: failures log
// per conversation and never stop the daemon from serving.
func (s *Server) recoverOrphanedRequests(ctx context.Context) {
	p, err := s.store.GetProjectByRoot(ctx, s.projectRoot)
	if err != nil {
		if !errors.Is(err, sql.ErrNoRows) {
			// Unregistered project (fresh repo) — nothing to close, no noise.
			log.Printf("orphan-requests: startup scan: %v", err)
		}
		return
	}
	convs, err := s.store.ListActiveConversations(ctx, p.ID)
	if err != nil {
		log.Printf("orphan-requests: startup scan: %v", err)
		return
	}
	for _, c := range convs {
		events, err := s.store.ListEvents(ctx, c.ID, 0)
		if err != nil {
			log.Printf("orphan-requests: startup scan conversation %d: %v", c.ID, err)
			continue
		}
		if !deriveOrphanedRequest(events) {
			continue
		}
		if _, err := s.store.AppendEvent(ctx, c.ID, store.EventAgentError, mustJSON(map[string]interface{}{
			"error": "daemon restarted while this request was in flight — no reply was recorded; resend the message",
			"cause": "daemon_restart",
		})); err != nil {
			log.Printf("orphan-requests: close conversation %d: %v", c.ID, err)
		}
	}
}

// steerDropCause names why a drained queue could not continue from this
// run's terminal state: a user kill (cancel), an agent-reported error
// (errored), or a clean finish that never gets a continuation slot
// (run_terminal — loop takeovers and diff-machinery failures).
func steerDropCause(meta *runMeta) string {
	if meta.cancelled {
		return "cancelled"
	}
	if meta.errored {
		return "errored"
	}
	return "run_terminal"
}

// activeRunCount returns the number of non-finished runs across all
// conversations — the daemon-wide concurrency level used by the cap.
// Caller must hold s.mu.
func (s *Server) activeRunCount() int {
	n := 0
	for _, meta := range s.runs {
		if !meta.finished {
			n++
		}
	}
	return n
}

// maxConcurrentDefault is used when prefs.md has no max_concurrent_runs line.
const maxConcurrentDefault = 4

// resolveMaxConcurrent reads the cap from prefs.md, falling back to the
// default when absent or unparseable.
func resolveMaxConcurrent() int {
	v := adapter.LoadPrefsRaw("max_concurrent_runs")
	if v == "" {
		return maxConcurrentDefault
	}
	n, err := strconv.Atoi(v)
	if err != nil || n < 1 {
		return maxConcurrentDefault
	}
	return n
}

// livenessDrainInterval is the production cadence of the daemon-side
// liveness drain (C11, 2026-08-22 P0). Deliberately slow next to the GUI's
// 350ms poll cadence: the poll drain is the interactive path, this tick is
// only the no-GUI floor — it takes the same s.mu as pollLocked, so each
// tick can stall admissions/drains by one drain step and must not be
// chatty. Tests shrink it through the livenessInterval atomic.
const livenessDrainInterval = 2 * time.Second

// runLivenessDrain is the daemon-side counterpart of the GUI's poll loop
// (C11, "GUI-closed loops continue"): every tick advances every unfinished
// run one drain step, so a run reaches its terminal event — and a /loop
// fix/implement run fires loopPipelineAfterRun/fireLoopTick — even with
// zero GUI traffic. Started in NewServer, stopped by stopLiveness (Wait /
// rig teardown). The interval re-resolves per tick so the test seam takes
// effect immediately; the disabled check sits INSIDE the loop so a rig
// flipping the switch needs no restart.
func (s *Server) runLivenessDrain() {
	defer s.livenessWG.Done()
	for {
		interval := time.Duration(s.livenessInterval.Load())
		if interval <= 0 {
			interval = livenessDrainInterval
		}
		t := time.NewTimer(interval)
		select {
		case <-s.livenessStop:
			t.Stop()
			return
		case <-t.C:
		}
		if s.livenessDisabled.Load() {
			continue // dark-launched (test rigs): ticks fire, the body skips
		}
		s.drainActiveRuns()
	}
}

// drainActiveRuns is one liveness tick body: advance every unfinished run
// exactly as pollLocked would (same s.mu discipline — drainRun's
// consumed/finished cursor must never advance concurrently or the same
// events journal twice). Errors are logged and the tick moves on: one
// failing run must never wedge the others, and this goroutine must never
// die — it IS the liveness contract, so a panic is swallowed with a log
// line rather than propagated (same fail-closed posture as the orphaned-
// request recovery: a dead tick regresses to the pre-C11 GUI-only wedge,
// invisibly).
func (s *Server) drainActiveRuns() {
	defer func() {
		if r := recover(); r != nil {
			log.Printf("ipc: liveness drain: recovered panic: %v (tick continues next interval)", r)
		}
	}()
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, meta := range s.runs {
		if meta.finished {
			continue
		}
		if err := s.drainRun(context.Background(), meta); err != nil {
			log.Printf("ipc: liveness drain: run %s: %v (retrying next tick)", meta.runID, err)
		}
	}
}

// stopLiveness closes the drain's stop channel exactly once and blocks
// until the goroutine exits. Wait calls it before draining wg (shutdown),
// rig teardown calls it before closing the store — a tick in flight
// journals under s.mu and must not outlive the journal.
func (s *Server) stopLiveness() {
	s.livenessOnce.Do(func() { close(s.livenessStop) })
	s.livenessWG.Wait()
}

// stopAutoDistill closes the auto-distill subsystem against further
// fires and arms (P1, the stopLiveness mirror): every pending timer is
// stopped and forgotten — an already-fired callback's identity check
// no-ops against the cleared slot — and autoStopped bars armAutoLocked
// re-arms (backoff/supersession) by in-flight distills themselves.
// Idempotent; Wait calls it before distillWG.Wait, rig teardown calls
// it before joining distillWG and closing the store. In-flight runs
// are joined, never cancelled.
func (s *Server) stopAutoDistill() {
	s.mu.Lock()
	if !s.autoStopped {
		s.autoStopped = true
		for id, entry := range s.autoPending {
			entry.timer.Stop()
			delete(s.autoPending, id)
		}
		// Daily-cap resume timers join the same teardown: an already-fired
		// callback's identity check no-ops against the dropped registry,
		// and armAutoCapLocked's autoStopped bar covers re-arms.
		for id, entry := range s.autoCap {
			dropAutoCapEntry(entry)
			delete(s.autoCap, id)
		}
	}
	s.mu.Unlock()
}

// bindRunLocked registers meta as the conversation's live run AND pins
// landWG for the run's WHOLE LIFETIME (2026-08-26 repair #66 — the
// structural Add-vs-Wait fence replacing the argued-by-comment
// drainRun-contexts list). The Add happens under the same s.mu hold as
// the registration, so it provably precedes any Wait that could reach
// landWG: every admission context (send/poll handlers, the liveness
// tick, the loop drivers, boot recovery) is joined ahead of
// landWG.Wait, and an admission from inside a landWG pipeline (revise,
// continuation) Adds while the parent's own unit still holds the
// counter. While the run lives the counter never reaches zero, so a
// later drainRun tail Add can never execute against a zero counter
// concurrently with Wait — REGARDLESS of the drain's call context,
// including a future call site outside pollLocked/drainActiveRuns.
// Returns false when land admissions are sealed (the second #66
// repair): sealLandAndReleasePins seals and sweeps under one s.mu
// hold, so the refusal observed here is atomic with the pin drop — a
// late pipeline admission (revise spawn, steer continuation, loop
// tick racing shutdown) can NEVER register a run whose pin no sweep
// will release. The caller unwinds its just-started agent/worktree
// exactly like an agent-start failure; the diff stays pending for
// the next boot's recovery. Caller holds s.mu.
func (s *Server) bindRunLocked(conversationID int64, runID string, meta *runMeta) bool {
	if s.landSealed {
		return false
	}
	s.landWG.Add(1)
	meta.landPinned = true
	s.runs[runID] = meta
	s.byConv[conversationID] = runID
	return true
}

// unpinRunLandLocked releases bindRunLocked's lifetime pin exactly
// once. Release points: drainRun's terminal drain (deferred, so it
// follows every land-tail Add that drain made), retireRun's map delete,
// and sealLandAndReleasePins at Wait/teardown. The flag makes the
// release idempotent — a finished run ALSO swept at Wait cannot take
// the counter negative. Caller holds s.mu.
func (s *Server) unpinRunLandLocked(meta *runMeta) {
	if meta.landPinned {
		meta.landPinned = false
		s.landWG.Done()
	}
}

// sealLandAndReleasePins is the last ordering step before landWG.Wait,
// and its two halves live in ONE s.mu critical section by necessity:
//
//  1. SEAL admissions — after this, bindRunLocked only refuses. Without
//     the seal, an in-flight landWG unit's late revise/continuation/
//     loop-tick spawn would register a run whose lifetime pin no sweep
//     ever releases (every drain-capable context is already joined), and
//     landWG.Wait below would hang FOREVER — observed as a stuck daemon
//     shutdown. With the seal, that late admission unwinds like an
//     agent-start failure and the pending diff waits for the next boot's
//     recovery (restart-interruptible posture preserved).
//  2. SWEEP the lifetime pins of every still-registered run — an
//     in-flight RUN never blocks shutdown (in-flight LAND pipelines do;
//     the distinction is the design). The seal must precede the sweep
//     under the same hold: interleaved, a bind could slip a pinned run
//     past the sweep and reintroduce the hang above.
//
// At this point every drain-capable context (liveness tick, poll
// handlers, loop drivers, boot recovery) is already joined (Wait's own
// order; rig teardown mirrors it), so no drain can still register a
// tail for the swept runs.
func (s *Server) sealLandAndReleasePins() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.landSealed = true
	for _, meta := range s.runs {
		s.unpinRunLandLocked(meta)
	}
}

// drainRun pulls new adapter events into the journal once. When the
// terminal event arrives it extracts the worktree diff exactly once
// and records it. Caller holds s.mu (called from handlePollEvents —
// and, since C11, from the liveness drain's drainActiveRuns):
// consumed/finished must not advance concurrently or the same
// events journal twice.
//
// The terminal tail's land-Adds are fenced by the run's lifetime
// pin (bindRunLocked), NOT by a census of this function's call
// sites: the deferred release below runs at function end — after
// every tail Add, on every finish path — so the counter stays
// non-zero from the run's registration until its last tail is
// registered.
func (s *Server) drainRun(ctx context.Context, meta *runMeta) error {
	// Release the run's landWG lifetime pin when the drain reaches a
	// terminal state, however the function exits. The defer
	// evaluates AFTER the body's tail registrations on every path,
	// so the pin always outlives the run's own Adds. It is the SOLE
	// release on drain-internal finishes — retireRunInDrain
	// deliberately skips its own release so a mid-branch retire can
	// never drop the pin ahead of the retry/continuation/parked-goal
	// registrations (#68 K3 finding 1).
	defer func() {
		if meta.finished {
			s.unpinRunLandLocked(meta)
		}
	}()
	evs, err := s.adapterFor(meta.adapter).Events(ctx, meta.runID, meta.consumed)
	if err != nil {
		return err
	}
	// M7: a trailing partial event is the adapter's transient preview —
	// strip it before journaling and stash it for this poll's response. It
	// never advances consumed: the next Events call re-sends the completed
	// block it was previewing.
	meta.previewEvent = nil
	if n := len(evs); n > 0 && evs[n-1].Payload["partial"] == true {
		preview := evs[n-1]
		meta.previewEvent = &preview
		evs = evs[:n-1]
	}
	for _, ev := range evs {
		appended, err := s.store.AppendEvent(ctx, meta.conversationID, ev.Type, mustJSON(ev.Payload))
		if err != nil {
			return err
		}
		meta.consumed++ // advance per successfully journaled event
		// run_verdict tallies (epoch-8): feed the terminal post-mortem.
		switch appended.Type {
		case store.EventAgentText:
			// Only a non-empty text counts toward the verdict — doneSummary
			// treats "" as "no text" the same way, and the false-stop
			// signature must not be fooled by a blank block.
			var tp struct {
				Text string `json:"text"`
			}
			if jsonUnmarshalOK(appended.Payload, &tp) && strings.TrimSpace(tp.Text) != "" {
				meta.texts++
			}
			// M12 (D-todo): agent_text ingest is the todo write path — the
			// daemon scans the journaled text for odo-todo blocks and merges
			// them mechanically (the event itself is never modified).
			s.mergeAgentTodo(ctx, meta.conversationID, appended)
		case store.EventAgentToolCall:
			meta.toolCalls++
		case store.EventAgentThinking:
			meta.thinkings++
		}
	}
	if len(evs) == 0 {
		return nil // still running
	}
	if t := evs[len(evs)-1].Type; t != store.EventAgentDone && t != store.EventAgentError {
		return nil // more events to come (not reached in M0: terminal batch is atomic)
	}
	meta.errored = evs[len(evs)-1].Type == store.EventAgentError

	// run_verdict (epoch-8, outstanding #1): mechanical post-mortem of an
	// exit-0 run. The false-stop signature (OMP exits 0 with zero output —
	// thinking-replay loss through the transport chain is the observed
	// cause) is journaled as a ledger row, never a forged agent_error, and
	// drives exactly one automatic retry; no_text (tools moved, the answer
	// never came back) hard-blocks auto-land downstream. Errored runs keep
	// their agent_error as the sole truth.
	verdict := verdictNone
	if !meta.errored {
		switch {
		case meta.texts == 0 && meta.toolCalls == 0:
			verdict = verdictFalseStop
		case meta.texts == 0:
			verdict = verdictNoText
		}
	}

	// A2-lite: defer the continuation trigger to a single tail after all
	// finished paths. We collect the queue here and fire after diff
	// extraction (or skip on error). This covers the no-diff success path
	// (diffPath == "") that previously dropped queued steers.
	queuedSteers := meta.queuedSteers
	if len(queuedSteers) > 0 {
		meta.queuedSteers = nil // consume the queue regardless of outcome
	}

	// The diff is extracted whether the run succeeded or failed: partial
	// changes are reviewable, and the human decides. (ADR-0001.)
	//
	// base_sha is the worktree's REAL HEAD here (I9) — the exact commit the
	// staged diff was generated against. Copying conversations.base_commit_sha
	// (the OLD path) mislabeled every run from the second one onward: the
	// conversation's base froze at creation while accepts moved main forward,
	// which also made auto-land's base_stale gate block truthful fresh diffs.
	baseSHA := ""
	if sha, err := git.CurrentSHA(meta.worktreePath); err == nil {
		baseSHA = sha
	} else {
		log.Printf("ipc: drainRun: read worktree base sha: %v (diff gets NULL base)", err)
	}
	diffPath, err := s.mgr.ExtractDiff(meta.worktreePath, meta.runDirID)
	if err != nil {
		// Every classification journals (runverdict.go): the early error
		// return is not an exemption — telemetry stays complete. No retry
		// here: the worktree/diff machinery is itself erroring, so a fresh
		// run would land in the same hole; the human sees the agent_error.
		if verdict != verdictNone {
			s.journalRunVerdict(ctx, meta, verdict, false)
		}
		_, _ = s.store.AppendEvent(ctx, meta.conversationID, store.EventAgentError,
			mustJSON(map[string]interface{}{"error": fmt.Sprintf("extract diff: %v", err)}))
		meta.finished = true // mark finished so polling stops even on diff failure
		// A broken diff path admits no continuation: the drained steers
		// close out as dropped instead of vanishing journal-silent.
		s.journalSteersDropped(ctx, meta.conversationID, steerSeqs(queuedSteers), steerDropCause(meta))
		// M12: the run ended — the window grew, so evaluate auto-distill too.
		s.maybeAutoAfterActivityLocked(ctx, meta.conversationID)
		return nil
	}
	// Registration-time memory-path fail-fast (2026-08-24): a diff touching
	// daemon-owned memory (.odo/, wiki/) can NEVER land — pre-panel blocks
	// it as protected_path and the executor's rejectMemoryPaths refuses
	// those bytes for EVERY actor, human Accept included — so registering
	// the row only burns a verify+panel cycle on its way to a terminal
	// block, wedging pending forever. Refuse here instead, shaped exactly
	// like the no-diff outcome (retire now; loops get the failure matrix),
	// and say so in the transcript with the correct route: wiki/ notes land
	// through the daemon's own distill/wiki-commit pipeline, so the
	// producer strips the memory hunk and resends the rest. The extracted
	// .diff stays in .odo/diffs/ as the salvage record. An unparseable
	// patch passes through (the accept-time guard is the backstop there).
	if diffPath != "" {
		if paths, perr := git.PatchPaths(diffPath); perr != nil {
			log.Printf("ipc: drainRun: memory-path guard: parse %s: %v (leaving to accept-time backstop)", diffPath, perr)
		} else if merr := rejectMemoryPaths(paths); merr != nil {
			reason := merr.Error() + "; wiki/ notes land through the daemon's own distill/wiki-commit pipeline — strip the memory-path hunk and resend the remaining work"
			meta.refusalDetail = "the fix run's diff was refused at registration (" + reason + ") — /loop resume after the hunk is stripped"
			s.journalRunAdvisory(ctx, meta.conversationID,
				"this run's diff was NOT registered: "+reason+fmt.Sprintf(" (full patch kept at %s; the run's worktree was retired)", diffPath))
			if verdict != verdictNone {
				s.journalRunVerdict(ctx, meta, verdict, false)
			}
			meta.finished = true
			s.retireRunInDrain(ctx, meta.conversationID)
			if meta.loopID != 0 {
				s.loopNoDiffAfterRun(ctx, meta, verdict)
				s.journalSteersDropped(ctx, meta.conversationID, steerSeqs(queuedSteers), steerDropCause(meta))
				s.maybeAutoAfterActivityLocked(ctx, meta.conversationID)
				return nil
			}
			s.journalSteersDropped(ctx, meta.conversationID, steerSeqs(queuedSteers), steerDropCause(meta))
			if !s.dequeueParkedGoalOnRunDoneLocked(ctx, meta) {
				s.maybeAutoAfterActivityLocked(ctx, meta.conversationID)
			}
			return nil
		}
	}
	if diffPath == "" {
		meta.finished = true // agent changed nothing; run is complete
		// Nothing to review, so retire immediately: previously only a review
		// action retired runs, and every no-diff run leaked its worktree
		// forever (P1). Retire BEFORE firing a continuation — and WITHOUT
		// releasing the lifetime pin: the defer at drainRun's top owns the
		// release so it provably follows every registration below (retry
		// bind, steer continuation, parked-goal activation). A mid-branch
		// release here fenced those Adds only by the s.mu happenstance of
		// the drain's call context — the #68 K3 finding-1 window.
		s.retireRunInDrain(ctx, meta.conversationID)
		// Test seam (landWG pin drill): hold the tail provably AFTER the
		// retire unregistered the run (Wait's pin sweep can no longer
		// reach it) and BEFORE this path's registrations, while the
		// counter still rides the kept pin. A gate that drops s.mu while
		// parked isolates the fencing to landWG alone.
		if s.drainTailGate != nil {
			s.drainTailGate()
		}
		if meta.loopID != 0 {
			// M19: a loop fix/implement run produced no diff — failure
			// matrix fix_no_diff / run_tainted (no continuations, no
			// false-stop retry: the loop resumes explicitly). The loop's
			// no-continuation shape also owns the steers' ending: dropped,
			// journaled, never continued.
			s.loopNoDiffAfterRun(ctx, meta, verdict)
			s.journalSteersDropped(ctx, meta.conversationID, steerSeqs(queuedSteers), steerDropCause(meta))
			s.maybeAutoAfterActivityLocked(ctx, meta.conversationID)
			return nil
		}
		switch {
		case verdict == verdictFalseStop && !meta.isRetry:
			// One automatic retry, verbatim goal plus any queued steers —
			// the steers were typed against work the dead run never did.
			// The retry run is marked isRetry, so a second false stop
			// cannot chain another (loop bound = exactly 1).
			//
			// Round-2 panel fix: admission is SYNCHRONOUS under the drain's
			// own s.mu — no user send can interleave between this drain and
			// the retry's registration, and the verdict row journals the
			// TRUE admission outcome (a goroutine-shaped fire used to
			// journal retry_fired=true and then get silently dropped by the
			// active-run/cap/distill re-checks: a ledger lie plus a
			// journal-silent wedge).
			log.Printf("run-verdict: retrying false-stop run for conversation %d", meta.conversationID)
			texts := make([]string, 0, 1+len(queuedSteers))
			texts = append(append(texts, meta.goal), steerTexts(queuedSteers)...)
			// The steers ride the retry as its prompt suffix: admitted is
			// consumption (the run_prompt row links their seqs — the goal
			// itself has none); refused is a journaled steer_dropped inside
			// startFollowupRunLocked. Either way the ledger closes.
			admitted, dropReason := s.startFollowupRunLocked(meta.conversationID, meta.workstreamID, texts, steerSeqs(queuedSteers), true)
			s.journalRunVerdict(ctx, meta, verdict, admitted)
			if !admitted {
				log.Printf("run-verdict: retry for conversation %d not admitted: %s", meta.conversationID, dropReason)
				s.journalRunAdvisory(ctx, meta.conversationID,
					"a silent run was detected but the automatic retry could not start ("+
						dropReason+"). Nothing was produced; resend manually.")
				s.maybeAutoAfterActivityLocked(ctx, meta.conversationID)
			}
		default:
			// Errored runs fire no continuation (agent_error is the truth);
			// a clean no-diff run continues from queued steers as before.
			// The retry's own false stop journals retry_fired=false — the
			// loop bound is visible in the ledger.
			if verdict != verdictNone {
				s.journalRunVerdict(ctx, meta, verdict, false)
			}
			if verdict == verdictFalseStop && meta.isRetry {
				// Two consecutive false stops on one goal = the transport
				// chain is broken, not a transient — stop retrying and SAY
				// SO in the transcript (panel 2026-08-12: the human-wait
				// fall-through must be visible, not ledger-only). No
				// further retry fires.
				s.journalRunAdvisory(ctx, meta.conversationID,
					"the automatic retry after a silent run also returned empty — "+
						"the model/gateway is likely stalled. Nothing was produced; resend manually.")
			}
			if len(queuedSteers) > 0 && !meta.errored {
				// landWG, like the land tails below: the admission
				// journals run_prompt/steer_dropped rows and creates the
				// follow-up worktree — detached it shared the #63
				// teardown-flake class (journal appends landing on a
				// closing store, mkdir inside a reclaimed tempdir).
				s.landWG.Add(1)
				go func() {
					defer s.landWG.Done()
					s.startContinuationRun(meta.conversationID, meta.workstreamID, queuedSteers)
				}()
			} else {
				// An errored (or cancelled) run continues nothing: the
				// drained steers must not vanish journal-silent — close
				// the ledger on them (a no-op when the queue was empty).
				s.journalSteersDropped(ctx, meta.conversationID, steerSeqs(queuedSteers), steerDropCause(meta))
				if !s.dequeueParkedGoalOnRunDoneLocked(ctx, meta) {
					// M12 (T1/T3): run-finished is an auto-distill evaluation
					// point — arm the idle timer or fire urgently.
					s.maybeAutoAfterActivityLocked(ctx, meta.conversationID)
				}
			}
		}
		return nil
	}
	// The per-run worktree binding rides the diff row (schema v2): retire
	// and the sweeper aim at exactly this run's worktree, never whatever the
	// workstream's single-slot column happened to point at last (Q6 bug 1).
	// The review objective rides the row too (schema v3): every later
	// review of this diff — gate, recoverPendingDiffs re-fire, manual
	// review_diff, loop seed drive — anchors to the PRODUCING run's goal,
	// never the conversation's newest human message (the #34 false
	// objective-mismatch rejection, 2026-08-22).
	// M16 (O-1 v2)/M18 ladder: meta.reviewGoal overrides meta.goal on
	// revise-chain runs — the panel judges against the chain's origin
	// goal, and a product diff stores that same anchor byte-exactly.
	reviewGoal := meta.goal
	if meta.reviewGoal != "" {
		reviewGoal = meta.reviewGoal
	}
	newDiff, derr := s.store.InsertDiff(ctx, meta.conversationID, diffPath, baseSHA, meta.worktreePath, reviewGoal)
	if derr != nil {
		log.Printf("ipc: drainRun: InsertDiff failed: %v", derr)
		s.store.AppendEvent(ctx, meta.conversationID, store.EventAgentError, mustJSON(map[string]interface{}{
			"error": "diff save failed: " + derr.Error(),
		}))
		meta.finished = true
		// Same ledger-close as the extract path: no diff row, no
		// continuation, so the drained steers drop on the record.
		s.journalSteersDropped(ctx, meta.conversationID, steerSeqs(queuedSteers), steerDropCause(meta))
		// M12: the run ended — the window grew, so evaluate auto-distill too.
		s.maybeAutoAfterActivityLocked(ctx, meta.conversationID)
		return nil
	}
	meta.finished = true // mark finished only after the diff row exists

	// Fix B1: link product diff to its revise chain. When this run is a
	// revise-chain run (originDiffID > 0), journal an auto_revise_product
	// event so supersedeChain can find the product diff when it lands.
	if meta.originDiffID > 0 {
		if _, err := s.store.AppendEvent(ctx, meta.conversationID, store.EventReviewAction, mustJSON(map[string]interface{}{
			"action":          "auto_revise_product",
			"actor":           autoActor,
			"product_diff_id": newDiff.ID,
			"origin_diff_id":  meta.originDiffID,
		})); err != nil {
			log.Printf("drainRun: auto_revise_product journal failed for diff %d: %v (supersedeChain may not find this product)", newDiff.ID, err)
		}
	}

	// The diff-bearing path journals its verdict too (no_text here means the
	// work is real but the answer died — the pipeline treats it as tainted).
	if verdict != verdictNone {
		s.journalRunVerdict(ctx, meta, verdict, false)
	}
	if verdict == verdictFalseStop && meta.isRetry {
		// Advisory parity (round-2 panel #2): a RETRY that false-stops earns
		// the transcript-visible surface on every finished path, diff or
		// not — here the phantom side effect is reviewable and the human
		// must know why the answer is missing and auto-land blocked.
		s.journalRunAdvisory(ctx, meta.conversationID,
			"the automatic retry after a silent run returned empty again, but it left an "+
				"unreviewed diff — likely model/gateway stall. Auto-land is blocked; review the diff manually.")
	}

	// Test seam (landWG pin drill): hold the tail provably BEFORE any
	// of the registrations below (loop tick, land pipeline,
	// continuation) — the lifetime pin keeps the counter non-zero
	// throughout the hold, and Wait must not pass until the released
	// tail runs to completion.
	if s.drainTailGate != nil {
		s.drainTailGate()
	}

	// M16 (O-1 v2): the pending diff spawns the auto-land pipeline
	// (pref-gated inside; goroutine, no locks held — the continuation
	// trigger's shape). meta's fields are copied as arguments.
	//
	// M19 (C1): a loop-provenance run SKIPS maybeAutoLand — the loop's
	// own pipeline drives its diff (Mode A: risk gate → verify → land;
	// Mode B: s.autoLand verbatim). A ladder repair run (originDiffID,
	// no loop marker) auto-lands as usual, then ticks any waiting loop.
	// reviewGoal is computed above, at InsertDiff.
	if meta.loopID != 0 {
		s.loopWG.Add(1)
		go func() {
			defer s.loopWG.Done()
			s.loopPipelineAfterRun(meta, newDiff, verdict)
		}()
	} else if meta.originDiffID != 0 {
		// Ladder repair run: its product re-enters the full pipeline; the
		// tick a tasks loop waits on fires AFTER settle's rows land.
		s.landWG.Add(1)
		go func() {
			defer s.landWG.Done()
			s.maybeAutoLand(newDiff, meta.worktreePath, reviewGoal, meta.errored, verdict)
			s.fireLoopTick(meta.conversationID)
		}()
	} else {
		s.landWG.Add(1)
		go func() {
			defer s.landWG.Done()
			s.maybeAutoLand(newDiff, meta.worktreePath, reviewGoal, meta.errored, verdict)
		}()
	}

	// A2-lite: if steering messages were queued during this run, auto-start
	// a continuation run with the queued texts as the prompt. This replaces
	// the dead steering.txt path — the agent sees the follow-up in a fresh
	// run with full memory-layer injection, not a silent file it never reads.
	if len(queuedSteers) > 0 && !meta.errored {
		// landWG (same class as the no-diff continuation above): the
		// admission journals and creates a worktree — join, never abort.
		s.landWG.Add(1)
		go func() {
			defer s.landWG.Done()
			s.startContinuationRun(meta.conversationID, meta.workstreamID, queuedSteers)
		}()
	} else {
		// An errored (or cancelled) run continues nothing: close the
		// ledger on the drained steers (a no-op when the queue was empty).
		s.journalSteersDropped(ctx, meta.conversationID, steerSeqs(queuedSteers), steerDropCause(meta))
		if !s.dequeueParkedGoalOnRunDoneLocked(ctx, meta) {
			// M12 (T1/T3): idle/urgent auto-distill evaluation at run finish.
			// Skipped when the parked-goal queue took this drain's one
			// successor slot (W6: steer continuations outrank parked goals;
			// at most one continuation OR activation per finished run).
			s.maybeAutoAfterActivityLocked(ctx, meta.conversationID)
		}
	}
	return nil
}

// errBaseStale is the base-freshness refusal's sentinel: checkAndRefreshBase
// wraps it when a stale base fails every automatic resolution — the M20
// already-landed roundtrip AND the P0a refresh (conflict OR error) — so the
// auto-land caller (the land step in autoland.go) can errors.Is-distinguish
// "main HEAD drifted mid-pipeline" from an apply failure — drift journals
// base_stale_at_land with the completed panel riding the blocked row as
// evidence, then hands the diff to a base_stale revise round on current
// HEAD, while a non-sentinel refusal (protected path, conflicted index,
// apply error) stays log-only.
var errBaseStale = errors.New("base stale")

// dirtyPatchRefusal builds the pre-apply refusal shared by the fresh- and
// stale-base accept paths (tri-review P0, 2026-08-24): the main checkout
// carries uncommitted user work on the patch's own paths, and applying
// over it is lossy in BOTH directions — a failed --3way rolls back to
// HEAD bytes over content the pipeline never touched; a clean merge
// sweeps the user's edits into the accept commit. The diff stays pending
// and the error names the paths, so committing or stashing them makes
// the accept retryable. Unlike a stale-base failure this is NOT wrapped
// in errBaseStale: the auto-land caller's errors.Is branch would fire an
// auto-revise whose regenerated patch hits the same refusal — user dirt
// needs a human, not another ~8-minute verify round.
func dirtyPatchRefusal(dirty []string) error {
	shown := dirty
	if len(dirty) > 5 {
		shown = append(shown[:5:5], fmt.Sprintf("… (+%d more)", len(dirty)-5))
	}
	return fmt.Errorf("accept_diff: main checkout has uncommitted changes on the patch's own paths (%s); commit or stash them, then retry the accept — the pipeline refuses to apply over them", strings.Join(shown, ", "))
}

// stagedEditsRefusal builds the pre-adjudication accept refusal
// (tri-review P1, 2026-08-24): the patch's own paths carry STAGED index
// content diverging from HEAD (IndexEditsBeyondHEAD), and the accept's
// stage+commit pair rewrites index entries wholesale — a staged edit
// whose worktree matches the post-image, or any staged content under an
// already-landed or refresh accept, is overwritten with no record. The
// byte- and porcelain-level guards miss the shape by design: the dirty
// check runs only on the fresh-apply path, and ExtraEditsBeyondPatch
// never reads the index. Same refusal family shape as dirtyPatchRefusal
// at this site — the diff stays pending, the error names the paths,
// nothing is journaled here, never errBaseStale; unstaging the edits
// makes the accept retryable.
func stagedEditsRefusal(staged []string) error {
	shown := staged
	if len(staged) > 5 {
		shown = append(shown[:5:5], fmt.Sprintf("… (+%d more)", len(staged)-5))
	}
	return fmt.Errorf("accept_diff: main checkout has staged changes on the patch's own paths (%s) — the accept stages and commits those paths wholesale and would overwrite the staged content; unstage the edits, then retry the accept", strings.Join(shown, ", "))
}

// extraEditsRefusal builds the ALREADY-LANDED accept refusal (tri-review
// P1, 2026-08-24): the M20 reverse-apply probe sees the patch at hunk
// granularity, so uncommitted user edits on the patch's own paths BEYOND
// the hunks read as landed — and CommitPaths records whole working-tree
// files, so the bookkeeping no-op-land commit would sweep them in. Where
// dirtyPatchRefusal's whole-file porcelain check runs before ANY apply,
// this runs byte-exact against the patch's reconstructed post-image
// (ExtraEditsBeyondPatch): identical landed content passes, trailing or
// inline extras refuse. Same refusal family shape — the diff stays
// pending named-paths-first and retryable, never errBaseStale (user dirt
// needs a human, not an auto-revise round).
func extraEditsRefusal(extra []string) error {
	shown := extra
	if len(shown) > 5 {
		shown = append(shown[:5:5], fmt.Sprintf("… (+%d more)", len(extra)-5))
	}
	return fmt.Errorf("accept_diff: already-landed diff's own paths carry uncommitted content beyond the patch bytes (%s) — the accept commit would sweep them in; commit or stash those edits, then retry the accept (or reject the diff)", strings.Join(shown, ", "))
}

// baseAdjudication is checkAndRefreshBase's tri-state verdict (M20: the
// already-landed resolution joined fresh/refreshed).
type baseAdjudication int

const (
	// baseFresh: HEAD == base (or nil/empty base) — the caller applies
	// the diff and commits normally.
	baseFresh baseAdjudication = iota
	// baseRefreshed: a clean --3way rebase already applied the diff to
	// main and moved its base pointer to HEAD — the caller skips its own
	// baseline/apply and only commits.
	baseRefreshed
	// baseAlreadyLanded: the diff's post-image is already in main's tree
	// (it landed through a path the daemon never applied — manual merge,
	// cherry-pick). Nothing was applied by this call; the caller skips
	// apply, commits only if uncommitted post-image content remains on
	// the patch paths, and marks the accept row already_landed.
	baseAlreadyLanded
)

// checkAndRefreshBase is the FINAL, authoritative base-freshness
// adjudication (P0a; supersedes fix-INT's checkBaseFresh, same D1/D3/D4
// invariants): it runs where the refusal used to — under acceptMu, after
// the unmerged-index refusal and before the caller's apply — so the
// check-to-apply window stays zero for daemon writers (a concurrent
// accept's CommitPaths holds this same mutex). HEAD drift triggers (in
// order):
//
//  1. the ALREADY-LANDED roundtrip (M20): ProbeAlreadyLanded reverse-
//     checks the patch against main's tree — read-only, before any
//     apply attempt. A diff whose content is fully present (landed via
//     manual merge/cherry-pick) is a bookkeeping resolution, not a
//     conflict: base pointer moves to HEAD, refresh_attempted
//     {outcome:"already_landed"} journals, and the caller's accept
//     proceeds as a no-op land. This is the reconcile that closes the
//     "content in main, diff pending forever" zombie class at its root.
//  2. the P0a REBASE: the diff embeds its base blobs, so git apply
//     --3way merges it onto current HEAD as a real 3-way merge, using
//     the same baseline/apply/rollback trio the caller's fresh path
//     runs.
//
// Returns:
//
//	(baseFresh, nil) — base fresh, or nil/empty (fix-INT D4
//	grandfathering: the auto path already refuses a missing base
//	upstream as base_unresolvable, so the skip re-opens no hole); the
//	caller applies normally.
//	(baseRefreshed, nil) — refresh CLEAN: the diff already applied to
//	main and its base pointer moved to HEAD (UpdateDiffBaseSHA); the
//	caller skips its baseline/apply and proceeds to CommitPaths.
//	(baseAlreadyLanded, nil) — content roundtrip: fully present, pointer
//	moved; the caller skips apply and commits only if patch paths still
//	differ from HEAD.
//	(baseFresh, err) — refresh failed: main rolled back to pre-attempt
//	state, the diff stays pending (NOT conflict — DiffConflict is
//	reserved for fresh-base apply failures), and err wraps errBaseStale
//	for BOTH the conflict and the error outcome (the lock's contract
//	treats them the same: fail closed), so the auto-land caller's
//	errors.Is branch journals base_stale_at_land and hands the diff to
//	the rebase revise round.
//
// Every attempt journals refresh_attempted{phase:"accept_apply"} BEFORE
// returning — journal-first, so the rebase's evidence precedes whichever
// resolution/blocked row follows (hard rule 6). A HEAD read error fails
// closed without touching the tree; an unparseable patch or missing
// rollback baseline refuses the attempt — refreshing main without a way
// back is not on the table. One attempt per gate encounter (hard rule 8):
// if HEAD moves again after a clean refresh, the next gate's check starts
// the adjudication over, it never loops inside one call.
func (s *Server) checkAndRefreshBase(ctx context.Context, d *store.Diff) (baseAdjudication, error) {
	if d.BaseSHA == nil || *d.BaseSHA == "" {
		return baseFresh, nil
	}
	head, err := git.CurrentSHA(s.projectRoot)
	if err != nil {
		return baseFresh, fmt.Errorf("accept_diff: read main HEAD for base freshness: %w", err)
	}
	if head == *d.BaseSHA {
		return baseFresh, nil
	}
	base := *d.BaseSHA
	// M20 reconcile, BEFORE any apply attempt (read-only): a stale base
	// whose content is already in main is the diff-20 zombie class — the
	// pipeline's only ingest path is git apply, so manual merges and
	// cherry-picks are invisible to it without this check. A partial
	// landing or a tree with drifted content reverse-checks dirty and
	// falls through to the rebase — conservative, never a false accept.
	if landed, _, lerr := git.ProbeAlreadyLanded(s.projectRoot, d.PathOnDisk); lerr == nil && landed {
		if uerr := s.store.UpdateDiffBaseSHA(ctx, d.ID, head); uerr != nil {
			return baseFresh, fmt.Errorf("accept_diff: record already-landed base: %w", uerr)
		}
		*d.BaseSHA = head
		s.journalRefreshAttempt(ctx, *d, "accept_apply", "already_landed", base, head, nil)
		return baseAlreadyLanded, nil
	}
	patchPaths, gerr := git.PatchPaths(d.PathOnDisk)
	if gerr != nil {
		return baseFresh, fmt.Errorf("accept_diff: parse patch paths for refresh: %w", gerr)
	}
	// P0 pre-apply refusal: never refresh over the user's uncommitted work
	// (see dirtyPatchRefusal). The already-landed probe above rescued the
	// identical-content case; whatever is dirty here genuinely differs.
	// Not an apply attempt, but the trail must say WHY a stale base did
	// not refresh (hard rule 6) — journal the refusal as its own outcome.
	if dirty, derr := git.DirtyPaths(s.projectRoot, patchPaths); derr != nil {
		return baseFresh, fmt.Errorf("accept_diff: check dirty patch paths for refresh: %w", derr)
	} else if len(dirty) > 0 {
		s.journalRefreshAttempt(ctx, *d, "accept_apply", "dirty_refusal", base, head, nil)
		return baseFresh, dirtyPatchRefusal(dirty)
	}
	baseHEAD, baseDisk, berr := git.CapturePatchBaseline(s.projectRoot, patchPaths)
	if berr != nil {
		return baseFresh, fmt.Errorf("accept_diff: capture rollback baseline: %w", berr)
	}
	if applyErr := git.ApplyDiff(s.projectRoot, d.PathOnDisk); applyErr == nil {
		// Clean rebase onto current HEAD — move the base pointer before
		// journaling so the trail records the store state as it stands.
		// Fail closed (hard rule 6): if the pointer move fails, roll main
		// back to the pre-attempt tree rather than leave an applied diff
		// whose store row still claims the old base.
		if uerr := s.store.UpdateDiffBaseSHA(ctx, d.ID, head); uerr != nil {
			if rbErr := git.RollbackPatchApply(s.projectRoot, patchPaths, baseHEAD, baseDisk); rbErr != nil {
				log.Printf("accept_diff: refresh rollback after UpdateDiffBaseSHA failure for diff %d: %v (inspect the main checkout)", d.ID, rbErr)
			}
			return baseFresh, fmt.Errorf("accept_diff: record refreshed base: %w", uerr)
		}
		*d.BaseSHA = head
		s.journalRefreshAttempt(ctx, *d, "accept_apply", "clean", base, head, nil)
		return baseRefreshed, nil
	} else {
		// Classify BEFORE the rollback erases the evidence: a failed --3way
		// that left unmerged index entries is a merge conflict; anything
		// else (missing blobs, unreadable patch) is a git error.
		outcome := "error"
		if conflicts, cerr := git.HasUnmergedEntries(s.projectRoot); cerr == nil && conflicts {
			outcome = "conflict"
		}
		if rbErr := git.RollbackPatchApply(s.projectRoot, patchPaths, baseHEAD, baseDisk); rbErr != nil {
			log.Printf("accept_diff: refresh rollback for diff %d: %v (inspect the main checkout)", d.ID, rbErr)
		}
		s.journalRefreshAttempt(ctx, *d, "accept_apply", outcome, base, head, applyErr)
		return baseFresh, fmt.Errorf("accept_diff: base stale (%s→%s) and automatic refresh %s: %v — the content conflicts with current main; the pipeline regenerates the task on current HEAD (auto-revise): %w", base, head, outcome, applyErr, errBaseStale)
	}
}

// journalRefreshAttempt records one stale-base rebase attempt as a
// refresh_attempted row — an additive action value on EventReviewAction
// (ADR-0002 immune: ComputeAutonomy and friends iterate payloads
// generically). base_sha is the diff's ORIGINAL base, target_sha the HEAD
// the rebase aimed at; detail carries the git diagnostics on
// conflict/error only, capped at 200 chars. The pre_spend_probe phase
// only runs inside the auto pipeline, so its rows carry
// actor:"auto_panel"; accept_apply rows leave provenance to the
// resolution/blocked row that follows (the accept row itself carries the
// actor). Best-effort like the pipeline's other journal helpers: the
// subsequent resolution row is journaled with an error return, so a lost
// breadcrumb never leaves an unrecorded RESOLUTION.
func (s *Server) journalRefreshAttempt(ctx context.Context, d store.Diff, phase, outcome, baseSHA, targetSHA string, applyErr error) {
	payload := map[string]interface{}{
		"action":     "refresh_attempted",
		"diff_id":    d.ID,
		"base_sha":   baseSHA,
		"target_sha": targetSHA,
		"outcome":    outcome,
		"phase":      phase,
	}
	if applyErr != nil {
		detail := applyErr.Error()
		if len(detail) > 200 {
			detail = detail[:200] + "…"
		}
		payload["detail"] = detail
	}
	if phase == "pre_spend_probe" {
		payload["actor"] = autoActor
	}
	if _, err := s.store.AppendEvent(ctx, d.ConversationID, store.EventReviewAction, mustJSON(payload)); err != nil {
		log.Printf("accept_diff: journal refresh attempt (%s/%s) for diff %d: %v", phase, outcome, d.ID, err)
	}
}

// handleDiffAction implements accept_diff and reject_diff. Accept applies the
// diff to the user's working tree with git apply (the visible loop closes
// here). Both journal a review_action and retire the run's worktree.
// actor is "" for the human click path; the auto-land pipeline passes
// autoActor so the journaled resolution carries its provenance (and stays
// out of the human streaks — ComputeAutonomy).
//
// Concurrency contract (2026-08-26 repair #66, landWG audit): this
// function MUST stay fully synchronous, top to tail — the guard
// checks, the apply/commit pair, the rescueResolvedWorktree snapshot
// (the git diff --cached HEAD read), supersedeChain, the resolution
// journal, and the maybeLadderResume tail. Its only callers are
// already-registered frames: dispatch inside handleConn for the
// manual accept/reject (s.wg, Add at Serve's accept loop and Done on
// handler return; Wait joins it before the store closes) and the
// maybeAutoLand settle pipeline for the auto accept (a landWG unit —
// autoland.go's recover fan-out wrapper and drainRun's registered
// tails; landWG.Wait joins it last). Any future async continuation
// here MUST register a landWG unit itself, or the #63
// write-into-closed-store class returns. The -race pair
// TestLandWGDrainPinFencesWait + TestManualAcceptTailJoinedByWait
// drills both surfaces mid-flight against a closing server.
func (s *Server) handleDiffAction(ctx context.Context, diffID int64, action, actor, commitMessage string) (Response, error) {
	if diffID == 0 {
		return Response{}, fmt.Errorf("%s_diff: diff_id is required", action)
	}
	d, err := s.store.GetDiff(ctx, diffID)
	if err != nil {
		return Response{}, err
	}

	// Q6 #6: one accept (or reject) at a time daemon-wide — see acceptMu.
	s.acceptMu.Lock()
	defer s.acceptMu.Unlock()

	// Fix B2: re-read status under acceptMu to prevent TOCTOU race.
	// Two concurrent callers (human vs pipeline, or recover-pending-diffs
	// fan-out) can both read "pending" before either takes acceptMu; the
	// loser must re-read the current status under the lock.
	d, err = s.store.GetDiff(ctx, diffID)
	if err != nil {
		return Response{}, err
	}
	if d.Status != store.DiffPending {
		return Response{}, fmt.Errorf("%s_diff: diff %d already %s", action, diffID, d.Status)
	}

	applied := false
	// headSHA (fix-INT D5) is the main HEAD the action operated on,
	// journaled on every resolution row — the accept path fills it with the
	// freshness head, the reject path with a best-effort read below.
	var headSHA string
	// refreshedFromSHA (P0a) rides the accept resolution row when a
	// stale-base refresh moved the diff's base to land it: the diff (and
	// any panel) was judged against ORIGINAL_base+diff, but the land
	// attests current_HEAD+diff, so the row must carry both — base_sha is
	// the post-refresh base (== head_sha), refreshed_from_sha the original.
	var refreshedFromSHA string
	// alreadyLanded (M20) marks the content-roundtrip resolution: the
	// diff's post-image was already in main, so the accept applied nothing
	// (bookkeeping no-op land, accept row carries already_landed:true).
	// The probe is hunk-granular — before the bookkeeping commit stages
	// anything, the P1 extra-edits refusal below (tri-review, 2026-08-24)
	// byte-compares each patch path against the reconstructed post-image so
	// user edits beyond the hunks never ride the accept commit.
	var alreadyLanded bool
	if action == "accept" {
		// M6 (§8b): explicit guarded-path check — gitignore is not the
		// enforcement point (wiki/ is NOT gitignored, so this daemon-side
		// guard is the sole protection for daemon-owned content). The diff
		// stays pending on a violation; the user can still reject it.
		// Both patch sides are guarded: renaming a file out of wiki/ is a
		// memory write too, not just writing into it.
		//
		// Two protection layers (2026-08-20 user doctrine — "review
		// everything automatically"; supersedes the 2026-08-19 restore):
		//   (1) memory paths (.odo/, wiki/) — invariant 1, refused for
		//       EVERY actor, human Accept included (the click still lands
		//       an agent-produced patch into daemon-owned content).
		//   (2) protectedGateFiles — a non-human actor (autoActor, loopActor,
		//       …) may land them ONLY behind panel evidence:
		//       panelVerdictAttestsDiff requires a journaled UNANIMOUS
		//       verdict row (consensus "accept"; the settle ladder's
		//       majority verdict lost attestation power in the 2026-08-22
		//       security cut) whose patch_sha16 matches the exact bytes
		//       being landed — the judged never modifies its own judge
		//       without its judges.
		//       The human Accept click stays the unconditional escape.
		patchPaths, gerr := git.PatchPaths(d.PathOnDisk)
		if gerr == nil {
			perr := rejectMemoryPaths(patchPaths)
			if perr == nil && actor != "" {
				if g, hit := gateSourceHit(patchPaths); hit && !s.panelVerdictAttestsDiff(ctx, d) {
					perr = fmt.Errorf("gate source %q may auto-land only behind a journaled unanimous panel verdict on these exact patch bytes (moa_review consensus accept with matching patch_sha16); a human Accept is the alternative", g)
				}
			}
			if perr != nil {
				_, _ = s.store.AppendEvent(ctx, d.ConversationID, store.EventAgentError,
					mustJSON(map[string]interface{}{"error": "accept_diff: " + perr.Error()}))
				return Response{}, perr
			}
		}
		// P1 retry guardrail: refuse to apply onto an in-progress conflict.
		// A previous --3way attempt that hit conflicts leaves unmerged index
		// entries behind; re-applying there mixes two merges, and the
		// path-scoped stage would record half-resolved files. Stay pending —
		// the user resolves or resets the conflict in the main checkout and
		// retries the accept.
		if conflicts, cerr := git.HasUnmergedEntries(s.projectRoot); cerr != nil {
			return Response{}, fmt.Errorf("accept_diff: check index: %w", cerr)
		} else if conflicts {
			return Response{}, errors.New("accept_diff: main checkout has unresolved merge conflicts; resolve or reset them, then retry the accept")
		}
		// P1 pre-adjudication staged-edit refusal (see stagedEditsRefusal):
		// index content on the patch's own paths diverging from HEAD can
		// pass every worktree-level guard below when the worktree happens
		// to hold the post-image — the already-landed extra-edits check
		// compares worktree BYTES only, and the porcelain dirty check
		// runs only on the fresh-apply path — while the stage+commit pair
		// on every accept branch rewrites those index entries wholesale,
		// losing the staged bytes. The gate must precede base
		// adjudication: the refresh path applies and commits too, so
		// refreshes, fresh applies, and already-landed accepts all share
		// this one refusal.
		if gerr == nil {
			if staged, serr := git.IndexEditsBeyondHEAD(s.projectRoot, patchPaths); serr != nil {
				return Response{}, fmt.Errorf("accept_diff: check staged patch paths: %w", serr)
			} else if len(staged) > 0 {
				return Response{}, stagedEditsRefusal(staged)
			}
		}
		// Final base-freshness adjudication (P0a + M20; see
		// checkAndRefreshBase): a stale base is adjudicated right here
		// instead of the old hard refusal — already-landed roundtrip first,
		// then a --3way rebase. A clean refresh ALREADY applied the diff to
		// main, so the fresh-path baseline+apply below is skipped and only
		// CommitPaths remains; already-landed applies nothing and commits
		// only leftover uncommitted post-image content; a conflict/error
		// keeps the diff pending, journals refresh_attempted, and returns
		// errBaseStale-wrapped — the auto caller's errors.Is branch owns
		// base_stale_at_land (blocked row + rebase revise round) on top.
		originalBase := ""
		if d.BaseSHA != nil {
			originalBase = *d.BaseSHA
		}
		adj, err := s.checkAndRefreshBase(ctx, &d)
		if err != nil {
			return Response{}, err
		}
		if adj == baseRefreshed {
			refreshedFromSHA = originalBase
		}
		if adj == baseAlreadyLanded {
			alreadyLanded = true
			refreshedFromSHA = originalBase
		}
		// The freshness head for the journaled row: a set BaseSHA that
		// survived the check IS the current head under acceptMu — after a
		// clean refresh the base pointer itself was moved to it — no
		// re-read (tri-review N2); nil-base legacy rows read it once for
		// additive evidence ("" on failure).
		if d.BaseSHA != nil && *d.BaseSHA != "" {
			headSHA = *d.BaseSHA
		} else {
			headSHA, _ = git.CurrentSHA(s.projectRoot)
		}
		// I7 baseline BEFORE the apply attempt: on failure the daemon rolls
		// the main checkout back to pre-accept state limited to these patch
		// paths (no self-produced unmerged entries, no half-applied files,
		// no damage outside the patch). When our parser can't enumerate the
		// paths, falls back to no-baseline — git apply stays the authority.
		// Skipped entirely when the adjudication resolved the base (a
		// clean refresh ran its own baseline/apply; already-landed applies
		// nothing).
		if adj == baseFresh {
			// M20 fresh-base roundtrip, BEFORE any apply (read-only): the
			// post-image is already in the tree when an identical
			// UNCOMMITTED edit sits on the patch paths, or the base
			// commit itself carries the content. A forward apply there
			// fails, and the I7 rollback restores HEAD bytes — it would
			// destroy the identical edit before any check could see it.
			// Rescued diffs land as bookkeeping; when the content is
			// uncommitted, CommitPaths below records it as the accept
			// commit (the landing must be recorded either way).
			if landed, _, lerr := git.ProbeAlreadyLanded(s.projectRoot, d.PathOnDisk); lerr == nil && landed {
				alreadyLanded = true
			}
		}
		if adj == baseFresh && !alreadyLanded {
			// P0 pre-apply refusal (see dirtyPatchRefusal): never apply over
			// the user's uncommitted work on the patch's own paths. The M20
			// identical-content rescue above already ran, so whatever is
			// dirty here genuinely differs from the post-image. The diff
			// stays pending (NOT conflict — nothing was attempted).
			if gerr == nil {
				if dirty, derr := git.DirtyPaths(s.projectRoot, patchPaths); derr != nil {
					return Response{}, fmt.Errorf("accept_diff: check dirty patch paths: %w", derr)
				} else if len(dirty) > 0 {
					return Response{}, dirtyPatchRefusal(dirty)
				}
			}
			var baseHEAD, baseDisk map[string]bool
			if gerr == nil {
				var berr error
				baseHEAD, baseDisk, berr = git.CapturePatchBaseline(s.projectRoot, patchPaths)
				if berr != nil {
					return Response{}, fmt.Errorf("accept_diff: capture rollback baseline: %w", berr)
				}
			}
			if err := git.ApplyDiff(s.projectRoot, d.PathOnDisk); err != nil {
				applyErr := err
				// I7: roll back. A failed --3way can leave per-file conflict
				// markers and unmerged index entries; the old "stay pending"
				// comment assumed nothing was written, which was false — the
				// conflict stuck the diff forever while main carried damage.
				var rbErr error
				if baseHEAD != nil {
					rbErr = git.RollbackPatchApply(s.projectRoot, patchPaths, baseHEAD, baseDisk)
				}
				// Terminal conflict state: the diff leaves the review
				// queue, the journal records the outcome, and the
				// message tells the human exactly where main stands.
				if uerr := s.store.UpdateDiffStatus(ctx, diffID, store.DiffConflict); uerr != nil {
					log.Printf("accept_diff: mark diff %d conflict: %v", diffID, uerr)
				}
				payload := map[string]interface{}{
					"action":      "conflict",
					"diff_id":     d.ID,
					"error":       applyErr.Error(),
					"rolled_back": rbErr == nil && baseHEAD != nil,
				}
				if actor != "" {
					payload["actor"] = actor
				}
				if _, aerr := s.store.AppendEvent(ctx, d.ConversationID, store.EventReviewAction, mustJSON(payload)); aerr != nil {
					log.Printf("accept_diff: journal conflict for diff %d: %v", diffID, aerr)
				}
				if rbErr != nil {
					return Response{}, fmt.Errorf("accept_diff: apply: %w (ROLLBACK INCOMPLETE: %v — inspect the main checkout)", applyErr, rbErr)
				}
				if baseHEAD == nil {
					return Response{}, fmt.Errorf("accept_diff: apply: %w (no rollback baseline — patch paths unparseable, inspect the main checkout)", applyErr)
				}
				return Response{}, fmt.Errorf("accept_diff: apply failed, main checkout rolled back to pre-accept state (diff marked conflict): %w", applyErr)
			}
		}
		// Commit the applied diff — only its own paths (P0) — so the next
		// worktree (created from HEAD) includes all previously accepted
		// files. Without this, files applied via `git apply` but never
		// committed don't appear in new worktrees, and the agent can't
		// modify them in isolation. Unrelated changes the user staged or
		// left dirty in the main checkout are never swept in.
		//
		// M20: the already-landed resolution applies nothing, so the paths
		// usually match HEAD exactly (content arrived committed) — an
		// empty path-scoped commit is a git error, skip it. Uncommitted
		// identical content (the fresh-base rescue) still gets the accept
		// commit — the landing must be recorded. That commit records whole
		// working-tree FILES, so it must only see post-image bytes: the
		// extra-edits refusal below (tri-review P1, 2026-08-24) guarantees
		// byte-equality before staging.
		skipCommit := false
		if alreadyLanded {
			// P1 extra-edits refusal (tri-review P1, 2026-08-24): the M20
			// reverse-apply probe matches hunk context only — uncommitted
			// user edits on the patch's own paths BEYOND the hunks survive
			// it, and CommitPaths below records whole files, sweeping
			// them into the accept commit. ExtraEditsBeyondPatch compares
			// bytes against the patch's reconstructed post-image instead;
			// identical landed content stays acceptable, anything diverging
			// refuses with the diff pending and a journaled agent_error
			// (same trail shape as the guarded-path refusal above). Runs
			// BEFORE any staging so the refusal is side-effect free, and
			// before the !skipCommit determination — a skipCommit (worktree
			// byte-equal to HEAD) compares clean here by construction.
			if gerr == nil {
				if extra, xerr := git.ExtraEditsBeyondPatch(s.projectRoot, d.PathOnDisk); xerr != nil {
					return Response{}, fmt.Errorf("accept_diff: check already-landed extra edits: %w", xerr)
				} else if len(extra) > 0 {
					rerr := extraEditsRefusal(extra)
					_, _ = s.store.AppendEvent(ctx, d.ConversationID, store.EventAgentError,
						mustJSON(map[string]interface{}{"error": rerr.Error()}))
					return Response{}, rerr
				}
			}
			// The rescue applied nothing, so no apply staged the paths —
			// stage FIRST: an untracked post-image file must form an index
			// entry before anything downstream, because git-diff-vs-HEAD
			// is blind to untracked files (it would report "no
			// difference" and skip the very commit that must record the
			// file) and git commit -- paths refuses untracked pathspecs.
			if err := git.StagePaths(s.projectRoot, patchPaths); err != nil {
				log.Printf("accept_diff: already-landed stage for diff %d (non-fatal): %v", diffID, err)
			}
			differs, derr := git.PathsDifferFromHEAD(s.projectRoot, patchPaths)
			switch {
			case derr != nil:
				log.Printf("accept_diff: already-landed commit-necessity check for diff %d: %v (attempting commit)", diffID, derr)
			case !differs:
				skipCommit = true
			}
		}
		if !skipCommit {
			commitMsg := fmt.Sprintf("odo: accept diff #%d", diffID)
			if commitMessage != "" {
				commitMsg = commitMessage
			}
			if err := git.CommitPaths(s.projectRoot, commitMsg, patchPaths); err != nil {
				// Non-fatal: the file is already applied to the working tree.
				// The commit just ensures worktree freshness for future runs.
				log.Printf("accept_diff: auto-commit failed (non-fatal): %v", err)
			}
		}
		applied = true
	}

	status := store.DiffAccepted
	if action == "reject" {
		status = store.DiffRejected
	}
	// Update diff status first: if the event journal fails, the diff is at
	// least correctly marked and a retry returns "already accepted/rejected"
	// instead of re-running git apply on an already-applied patch.
	if err := s.store.UpdateDiffStatus(ctx, diffID, status); err != nil {
		return Response{}, err
	}
	// Rescue archive (#49, the #47 incident): retireRunForDiff below
	// deletes the reviewed diff's worktree, destroying any bytes newer
	// than the archived .diff — rejecting #47 left the fix's only copy in
	// a stale backup, recoverable solely by hand-applying it. Snapshot
	// the worktree's current full delta FIRST; divergent bytes land in
	// .odo/diffs/<run>-rescue.diff (sweeper-exempt like every diff
	// archive) and their receipt rides the resolution row. Best-effort:
	// a failed snapshot never delays the resolution.
	rescue := s.rescueResolvedWorktree(d)
	// Supersede older pending diffs in the same revise chain (Fix 2,
	// zero-manual-accept): when this diff lands, mark all older pending
	// diffs in the same chain as superseded so they stop blocking the
	// pending queue. The chain is derived from auto_revise_round rows.
	if action == "accept" {
		s.supersedeChain(ctx, d)
	}
	// fix-INT D5 (additive): the adjudicated tree rides every resolution
	// row — base_sha is the diff's stored base ("" = grandfathered pre-v2
	// row), head_sha the main HEAD at the action. Consumers ignore unknown
	// keys (ComputeAutonomy/ledger/audit iterate generically).
	baseSHA := ""
	if d.BaseSHA != nil {
		baseSHA = *d.BaseSHA
	}
	if action == "reject" {
		// Reject writes nothing to the tree, so freshness never adjudicates
		// it; the head is journaled best-effort anyway ("" on read failure).
		headSHA, _ = git.CurrentSHA(s.projectRoot)
	}
	payload := map[string]interface{}{
		"action":   action,
		"diff_id":  d.ID,
		"base_sha": baseSHA,
		"head_sha": headSHA,
	}
	// P0a (additive): a clean-refreshed accept attests current_HEAD+diff
	// while the diff (and any panel) was judged against the ORIGINAL base
	// — the row must name both, on top of the refresh_attempted row.
	if refreshedFromSHA != "" {
		payload["refreshed_from_sha"] = refreshedFromSHA
	}
	// M20 (additive): the diff's post-image was found already present in
	// main (it landed outside the pipeline's apply path); the accept is a
	// bookkeeping no-op land, not a fresh application.
	if alreadyLanded {
		payload["already_landed"] = true
	}
	if actor != "" {
		payload["actor"] = actor
	}
	// fix-INT W5 (additive): every human/auto accept + reject carries the
	// Guardian risk receipt for the diff it resolved (risk.go:
	// risk_class/risk_evidence/risk_classifier; an unreadable patch omits
	// all three). The ratchet wave reads these via ComputeAutonomy's risk
	// table; today's consumers ignore them (ADR-0002).
	mountRiskReceipt(payload, riskReceipt(d.PathOnDisk))
	for k, v := range rescue {
		payload[k] = v
	}
	if _, err := s.store.AppendEvent(ctx, d.ConversationID, store.EventReviewAction, mustJSON(payload)); err != nil {
		return Response{}, err
	}

	// Retire the reviewed run under the mutex (M11 P0): map deletes,
	// adapter.Close, and worktree removal must not interleave with
	// concurrent poll drains of the same run.
	s.mu.Lock()
	s.retireRunForDiff(ctx, d)
	s.mu.Unlock()

	// M18 → M20: ANY landing is the ladder's un-suspension reset —
	// "the panel converged again" is the same evidence class a human
	// accept was, and a pipeline that can demote itself but never recover
	// permanently wedges every conversation that hits one bad chain.
	// Journaled only at the transition, derived — never an in-memory flag.
	if applied {
		s.maybeLadderResume(ctx, d.ConversationID, diffID, actor)
	}

	// Test seam (the landWG manual-accept drill; production never sets
	// it): park the handler AFTER the full accept tail — apply/commit,
	// rescue snapshot, supersedeChain, resolution journal, retire,
	// ladder resume — but BEFORE the response, with s.mu not held, so
	// the drill proves Wait's s.wg join fences shutdown around a
	// mid-flight manual accept rather than observing a response that
	// was merely serialized early.
	if s.diffActionGate != nil {
		s.diffActionGate()
	}

	return Response{DiffID: diffID, Applied: applied}, nil
}

// rescueResolvedWorktree archives the RESOLVED diff's worktree's current
// full delta vs its HEAD, before retireRun deletes the dir (see the call
// site in handleDiffAction for the incident). The judged archive
// (d.PathOnDisk) is NEVER mutated — patch_sha16 lineage stays intact;
// divergent bytes get a -rescue sibling. Receipt values on the row:
//
//	matches_archived  worktree delta == archived patch (nothing newer)
//	clean             worktree matches its HEAD (delta is empty)
//	archived          rescue_path/rescue_sha16 name the divergent bytes
//	unavailable       worktree gone or snapshot failed — the archived
//	                  patch stays the sole record (the pre-#49 contract)
//	no_worktree       legacy pre-v2 row with no bound worktree
func (s *Server) rescueResolvedWorktree(d store.Diff) map[string]interface{} {
	if d.WorktreePath == nil || *d.WorktreePath == "" {
		return map[string]interface{}{"rescue": "no_worktree"}
	}
	diff, err := git.ExtractDiff(*d.WorktreePath)
	if err != nil {
		log.Printf("review: rescue snapshot for diff %d: %v (archived patch stays the record)", d.ID, err)
		return map[string]interface{}{"rescue": "unavailable"}
	}
	if diff == "" {
		return map[string]interface{}{"rescue": "clean"}
	}
	if archived, rerr := os.ReadFile(d.PathOnDisk); rerr == nil && string(archived) == diff {
		return map[string]interface{}{"rescue": "matches_archived"}
	}
	path := s.mgr.DiffPath(strings.TrimSuffix(filepath.Base(d.PathOnDisk), ".diff") + "-rescue")
	if err := os.WriteFile(path, []byte(diff), 0o644); err != nil {
		log.Printf("review: rescue write for diff %d: %v", d.ID, err)
		return map[string]interface{}{"rescue": "unavailable"}
	}
	log.Printf("review: rescue archived for diff %d: %s", d.ID, path)
	return map[string]interface{}{
		"rescue":       "archived",
		"rescue_path":  path,
		"rescue_sha16": sha16([]byte(diff)),
	}
}

// retireRunForDiff releases resources after a diff review: the worktree
// retired is exactly the reviewed diff's OWN worktree (d.WorktreePath,
// schema v2 per-run binding) — never a binding the workstream happened to
// point at last (the Q6 cardinality bug targeted the wrong dir under
// concurrent runs). Caller holds s.mu (handleDiffAction's locked section).
func (s *Server) retireRunForDiff(ctx context.Context, d store.Diff) {
	fallbackWT := ""
	if d.WorktreePath != nil {
		fallbackWT = *d.WorktreePath
	}
	s.retireRun(ctx, d.ConversationID, fallbackWT)
}

// retireRun closes the adapter run and removes the worktree for a concluded
// review. fallbackWT is the reviewed diff's recorded worktree ("" for the
// no-diff retire out of drainRun). Removal failures are logged, not fatal —
// the review already happened and the startup sweeper converges orphans.
// Caller holds s.mu (via retireRunForDiff — drainRun's own finishes go
// through retireRunInDrain below).
func (s *Server) retireRun(ctx context.Context, conversationID int64, fallbackWT string) {
	s.retireRunCore(ctx, conversationID, fallbackWT, true)
}

// retireRunInDrain is drainRun's retire entry for its no-diff and
// memory-refusal finishes: the identical slot retire, WITHOUT the
// lifetime-pin release. drainRun's deferred unpin owns the release so it
// provably follows every tail registration of the drain — retry bind,
// steer continuation, parked-goal activation, loop resume. Releasing here
// would drop the pin mid-branch and fence those Adds only by the drain's
// call-context happenstance (#68 K3 finding 1).
func (s *Server) retireRunInDrain(ctx context.Context, conversationID int64) {
	s.retireRunCore(ctx, conversationID, "", false)
}

// retireRunCore implements both entries; releaseLandPin selects whether
// the lifetime pin drops with the slot retire (review path) or defers to
// drainRun's function-end release (drain path).
//
// Target selection (tri-review P1, 2026-08-24): the run retired is the one
// whose worktreePath IS the reviewed diff's own — never whichever run the
// conversation's byConv binding happens to point at last. Under back-to-back
// runs that binding is a DIFFERENT finished run, and the old byConv-first
// selection closed it and deleted ITS worktree — mid auto-land verify, if
// that run's own diff was still in the pipeline — while the reviewed diff's
// worktree was orphaned. The byConv binding selects only when there is no
// worktree to match (drainRun's no-diff retire, legacy pre-v2 diff rows).
func (s *Server) retireRunCore(ctx context.Context, conversationID int64, fallbackWT string, releaseLandPin bool) {
	var wtPath, liveWT, closedRunID string

	targetID := ""
	if fallbackWT != "" {
		for id, meta := range s.runs {
			if meta != nil && meta.worktreePath == fallbackWT {
				targetID = id
				break
			}
		}
	} else if runID, ok := s.byConv[conversationID]; ok {
		targetID = runID
	}
	if meta := s.runs[targetID]; meta != nil {
		if !meta.finished {
			// The target is a LIVE run — closing it would kill the
			// in-flight agent mid-write (accept/reject interrupting a
			// running agent). Leave run and maps alone.
			liveWT = meta.worktreePath
		} else {
			wtPath = meta.worktreePath
			if releaseLandPin {
				// Release the lifetime pin ahead of the delete —
				// finished runs already released at their terminal drain
				// (this no-ops), so the call is the balance point's belt
				// AND suspenders.
				s.unpinRunLandLocked(meta)
			}
			_ = s.adapterFor(meta.adapter).Close(ctx, targetID)
			delete(s.runs, targetID)
			closedRunID = targetID
		}
	}
	// Binding hygiene: reap the conversation's binding only when it points
	// at a run this call closed, or at one already gone from the map. A
	// binding to a DIFFERENT run — live or finished — survives: that run
	// retires with its own diff's review.
	if boundID, ok := s.byConv[conversationID]; ok {
		if boundID == closedRunID || s.runs[boundID] == nil {
			delete(s.byConv, conversationID)
		}
	}

	if wtPath == "" {
		wtPath = fallbackWT
	}
	// A live run's worktree is never removed here; a STALE finished worktree
	// (fallbackWT from an older reviewed diff) IS removed even while another
	// run is live — distinct per-run dirs make that finally safe (schema v2).
	if wtPath != "" && wtPath != liveWT {
		if err := s.mgr.Remove(wtPath); err != nil {
			log.Printf("ipc: retire run: remove worktree %s: %v", wtPath, err)
		}
	}

	// The run's transcript dir retires WITH it: everything of record was
	// journaled before this point (the event stream per turn), and orphaned
	// session dirs otherwise accumulate without bound within one daemon
	// lifetime. The prompt capture deliberately STAYS until the startup
	// sweeper — it is the codebase's post-completion audit surface for
	// "what the agent was actually shown" (tests read it as ground truth),
	// so a daemon lifetime of prompts is kept for forensics and aged out
	// at boot. Only a run THIS call closed is collectible: a live run is
	// mid-write, and a fallbackWT diff's adapter run id is unrecoverable
	// from its worktree name (the startup sweeper reaps those orphans by
	// age instead).
	if closedRunID != "" {
		sessionDir := filepath.Join(s.mgr.StateDir(), "sessions", closedRunID)
		if err := os.RemoveAll(sessionDir); err != nil {
			log.Printf("ipc: retire run: remove session dir %s: %v", sessionDir, err)
		}
	}
}

// reviewModel is one parsed entry of the prefs.md `review:` line.
type reviewModel struct {
	model    string
	provider string
}

// handleReviewDiff implements review_diff: the diff is sent to every
// model on the prefs.md `review:` line via direct HTTP API (moa.Query),
// in parallel. The call blocks until all finish — but only its own
// connection: M11 made the daemon goroutine-per-connection and this
// handler holds neither s.mu nor any per-conversation lock, so an
// in-flight tri-model review never blocks other requests, including a
// new send in the same conversation (it serializes only on the single
// sqlite connection inside the store).
// Results are journaled as a review_action event with action "moa_review".
//
// M18 batch B: the prompt is the SAME builder the auto-land gate uses
// (review.go — the manual panel stops losing information the auto path
// had): the original goal from the journal verbatim, the mechanical
// facts block, and the adversarial instruction. Verify honestly reads
// "not run" — the manual path has no worktree-verified receipt; a
// tainted producing run's run_verdict tallies ride the facts when the
// ledger has them.
func (s *Server) handleReviewDiff(ctx context.Context, req Request) (Response, error) {
	if req.DiffID == 0 {
		return Response{}, fmt.Errorf("review_diff: diff_id is required")
	}
	d, err := s.store.GetDiff(ctx, req.DiffID)
	if err != nil {
		return Response{}, err
	}
	// N5 fix: don't run a panel on non-pending diffs — wastes spend and
	// adds dead evidence rows.
	if d.Status != store.DiffPending {
		return Response{}, fmt.Errorf("review_diff: diff %d is %s (only pending diffs can be reviewed)", req.DiffID, d.Status)
	}
	content, err := os.ReadFile(d.PathOnDisk)
	if err != nil {
		return Response{}, fmt.Errorf("review_diff: read diff: %w", err)
	}
	models := parseReviewModels(adapter.LoadPrefsRaw("review"))
	if len(models) == 0 {
		return Response{}, errors.New("No review models configured.")
	}

	prompt := buildReviewPrompt(reviewPromptInput{
		mode:       reviewPromptAdvisory,
		goal:       s.diffGoal(ctx, d),
		diffPath:   d.PathOnDisk,
		diffText:   string(content),
		verifyNote: "not run — manual review_diff has no verify gate; the auto-land pipeline is the verified path",
		runFacts:   s.latestRunVerdictFacts(ctx, d.ConversationID),
	})
	reviews := s.reviewFanout(ctx, models, prompt)

	cv := consensusVerdict(reviews)
	// patch_sha16 (M18 W2 item 4): attests the EXACT diff bytes the panel
	// judged (content rode the prompt fenced above — the handler already
	// refused when the diff was unreadable), so a verdict stays falsifiable
	// against the artifact even after the diff file rotates.
	moaPayload := map[string]interface{}{
		"action":            "moa_review",
		"diff_id":           d.ID,
		"reviews":           reviews,
		"consensus_verdict": cv,
		"patch_sha16":       sha16(content),
	}
	// W5: the manual panel's row carries the same risk receipt as the
	// auto path's (same bytes, same mechanical classifier, risk.go).
	mountRiskReceipt(moaPayload, riskReceiptKeys(string(content)))
	if _, err := s.store.AppendEvent(ctx, d.ConversationID, store.EventReviewAction, mustJSON(moaPayload)); err != nil {
		return Response{}, err
	}
	return Response{Reviews: reviews, Consensus: cv}, nil
}

// consensusVerdict computes a deterministic tally over review results.
// Returns "accept" only when EVERY reviewer accepts — a single
// needs_fixes (explicit dissent, a parse failure, or a degraded
// truncated/errored review) blocks the accept. Returns "reject" if any
// verdict is "reject", otherwise "needs_fixes". This replaces the former
// 2/3 threshold, which was fail-open at N=3: 2 ACCEPT + 1 NEEDS_FIXES
// read as "accept", letting a dissented diff present (and later land)
// as panel-approved. One tally now serves both the review badge and the
// auto-land gate. Still no 4th model call (Hermes consolidator rule).
func consensusVerdict(reviews []ReviewResult) string {
	if len(reviews) == 0 {
		return "needs_fixes"
	}
	accepts := 0
	for _, r := range reviews {
		switch r.Verdict {
		case "accept":
			accepts++
		case "reject":
			return "reject" // any reject dominates
		}
	}
	if accepts == len(reviews) {
		return "accept"
	}
	return "needs_fixes"
}

// reviewWithModel runs a review prompt through one model via the direct
// HTTP API client (moa.Query) and parses the verdict. The prompt is
// pre-built by the caller (handleReviewDiff and the auto-land gate both
// build with buildReviewPrompt; gateSkillProposals wraps with
// skillReviewPrompt). A failed run degrades to needs_fixes with the
// error as comments: a review that never happened must not read as an
// accept.
//
// legTimeout bounds one moa leg's outer deadline (P1 #9): production is
// moa.TimeoutForModel — one worst-case attempt chain at the model's hard
// output cap, the designLeg/auditLeg precedent. The legTimeoutForTest
// seam shrinks it for wall-clock deadline drills (production never sets
// it, the receiptBreachForTest precedent).
func (s *Server) legTimeout(model string) time.Duration {
	if s.legTimeoutForTest > 0 {
		return s.legTimeoutForTest
	}
	return moa.TimeoutForModel(model)
}

// sharedMoa returns the Server's single MoA client (P1 #10) — see the
// moaShared field for why every review/advisory/design leg must ride it.
func (s *Server) sharedMoa() *moa.Client {
	s.moaOnce.Do(func() { s.moaShared = moa.NewClientFromEnv("", "") })
	return s.moaShared
}

// M18 batch B additions (journal surfaces only — the verdict contract is
// byte-identical): every leg carries base_url, the scrubbed endpoint it
// truly hit (provider honesty: prefs' model@provider is a label, the one
// moa gateway is the route); and a non-accept leg carries thinking_md,
// the leg's reasoning text capped at 4KB — the gateway's real thinking
// blocks when present, else the leg's full response text (the documented
// approximation for models with no separate reasoning channel). ACCEPT
// legs journal no thinking (noise discipline).
func (s *Server) reviewWithModel(ctx context.Context, m reviewModel, prompt string) ReviewResult {
	label := m.model + "@" + m.provider
	// P1 #10: the SHARED client — a per-leg one would give every leg a
	// private semaphore (see moaShared).
	client := s.sharedMoa()
	baseURL := scrubBaseURL(client.BaseURL)
	system := "You are a code reviewer. Review the following diff and provide your verdict."
	// P1 #9: same outer-deadline class as the /panel leg — an unbounded
	// gateway stall would otherwise wedge the auto-land pipeline (its
	// Background ctx carries no deadline) and every skills/distill gate
	// batch. The leg's Infra result fails the round closed, never an
	// accidental accept.
	lctx, cancel := context.WithTimeout(ctx, s.legTimeout(m.model))
	defer cancel()
	res, err := client.Query(lctx, m.model, system, prompt)
	if err != nil {
		// M18: the leg never reached a verdict — transport/auth/timeout
		// is INFRA, not dissent. The Verdict stays needs_fixes (the M16
		// degrade: a review that never happened must not read as an
		// accept) but carries Infra so the settlement ladder fails the
		// whole round closed as panel_infra instead of feeding an error
		// string to the repair prompt. base_url still records where the
		// leg TRIED to go.
		return ReviewResult{Model: label, Verdict: "needs_fixes", Comments: "review failed: " + err.Error(), Infra: true, BaseURL: baseURL}
	}
	rr := reviewVerdict(label, res.Text, res.Truncated)
	rr.BaseURL = baseURL
	// R-W1.5: the client's wire receipt rides every journaled moa_review /
	// memory_propose row — sha16 of the exact request body whose verdict
	// shipped. Infra legs return above the receipt, carrying none.
	rr.RequestSHA16 = res.RequestSHA16
	rr.RequestBytes = res.RequestBytes
	if rr.Verdict != "accept" {
		if res.Thinking != "" {
			rr.ThinkingMD = capDetail(res.Thinking)
		} else {
			// No reasoning channel exposed by the client for this model —
			// journal the full response text (approximation, locked in the
			// m18 doc) so the non-accept reasoning is never lost.
			rr.ThinkingMD = capDetail(res.Text)
		}
	}
	return rr
}

// reviewVerdict folds one model response into a ReviewResult. Truncation
// forces needs_fixes even when the partial text parses clean: a cut-off
// stream cannot prove the model's final position, and honoring the
// truncated verdict would count a review the model never finished (M16
// panel finding — the truncated/early-ACCEPT unanimity bypass).
func reviewVerdict(label, text string, truncated bool) ReviewResult {
	rr := parseVerdict(label, text)
	if truncated {
		rr.Verdict = "needs_fixes"
		rr.Truncated = true
		rr.Comments = "[review truncated at the model's hard output cap — verdict forced fail-closed] " + rr.Comments
	}
	return rr
}

// parseReviewModels parses the comma-separated `review:` prefs value into
// model/provider pairs, skipping blank and malformed entries.
func parseReviewModels(raw string) []reviewModel {
	var out []reviewModel
	for entry := range strings.SplitSeq(raw, ",") {
		entry = strings.TrimSpace(entry)
		if entry == "" {
			continue
		}
		model, provider := adapter.ParseModelProvider(entry)
		if model == "" {
			continue
		}
		out = append(out, reviewModel{model: model, provider: provider})
	}
	return out
}

// handlePanelQuery routes a /panel prompt to N MoA models via the direct API
// client. It journals the user message and all model replies as events, then
// returns the combined replies. No worktree, no diff — read-only thinking.
func (s *Server) handlePanelQuery(ctx context.Context, c *store.Conversation, text string) (Response, error) {
	if text == "" {
		return Response{}, fmt.Errorf("/panel: prompt text is required after /panel")
	}
	// M12: the gates run against the slash-slot registration in one critical
	// section. Distill side: manual distills refuse (same error text as
	// send, which is where this check lived before); an in-flight AUTO
	// distill is cancelled pre-note and the query proceeds. Run side:
	// unchanged refusal. The slot itself makes the reciprocal race
	// impossible — a distill (manual refused, auto skipped+journaled) can no
	// longer start mid-/panel and fold this answer into last_seq unseen.
	s.mu.Lock()
	if runID, ok := s.byConv[c.ID]; ok {
		if meta := s.runs[runID]; meta != nil && !meta.finished {
			s.mu.Unlock()
			return Response{}, fmt.Errorf("send_message: agent already running for conversation %d", c.ID)
		}
	}
	if err := s.gateAutoDistillForSendLocked(ctx, c.ID); err != nil {
		s.mu.Unlock()
		return Response{}, err
	}
	s.slashing[c.ID]++
	s.mu.Unlock()
	defer s.releaseSlashSlot(ctx, c.ID)

	models := parseReviewModels(adapter.LoadPrefsRaw("review"))
	if len(models) == 0 {
		return Response{}, errors.New("No review models configured for /panel. Set the 'review:' line in prefs.md.")
	}
	w, err := s.store.GetWorkstream(ctx, c.WorkstreamID)
	if err != nil {
		return Response{}, err
	}

	// Assemble the shared slash context BEFORE journaling the /panel
	// user_message (mirroring the send path's runMemoryLayers ordering), so
	// the block never contains the panel question itself. The recall query
	// reuses the send-path shape: slash text UNION the last current-epoch
	// turns. One events fetch feeds both that query and the block's
	// conversation tail.
	var events []store.Event
	if evs, lerr := s.store.ListEvents(ctx, c.ID, 0); lerr == nil {
		events = evs
	}
	// Resolve the scope once — the context block's layer gate and the
	// journaled receipt must agree.
	scope := resolvePanelContextScope()

	// Fan out to N models via direct API (parallel, no OMP process) on the
	// shared client (P1 #10) so panel legs contend for the daemon-wide cap
	// instead of each holding a private semaphore.
	client := s.sharedMoa()
	// E1: read-only, home-scoped FS tools (read_file/grep/glob) let panel
	// models ground answers in real files instead of hand-pasted context.
	// Round 0 → moa default cap. Every executed call is journaled below.
	exec := newFSToolExecutor()
	tools := moaFSTools()
	system := "You are an expert advisor. Provide a thorough, independent analysis." +
		"\n\nYou have read-only tools over the user's files: read_file, grep, glob. " +
		exec.describeScope() +
		" Use them to ground your answer in the actual files whenever the question touches code or documents — do not ask the user to paste content. Every read is journaled."
	block, receipt, convBlock, conv := s.slashContextBlock(ctx, w.Name, c.ID, recallQuery(text, events), events, scope, slashModePanel)
	if block != "" {
		system += "\n\n---\n\n" + block
	}

	// Journal the user message with the injection receipt, the effective
	// context scope, the assembled prompt size (item D), and the
	// conversation tail's replay receipt (dropped_seqs on omission —
	// symmetric with the send path).
	ev, err := s.store.AppendEvent(ctx, c.ID, store.EventUserMessage, mustJSON(
		slashUserMessagePayload("/panel", text, receipt, scope, len(system)+len(text), conv)))
	if err != nil {
		return Response{}, err
	}

	// M18 W2 item 4: fail closed BEFORE any moa call — the attempt
	// (user_message above) and the breach (agent_error below) both stay on
	// record (the send path's evidence-first ordering).
	if aerr := s.assertSlashReceipts(block, receipt, convBlock); aerr != nil {
		return Response{}, s.failRun(ctx, c.ID, fmt.Errorf("slash receipt assertion failed: %w", aerr))
	}
	// Fan-out progress heartbeat (poll-side, never journaled): the tally
	// registered here flows to poll_events so the GUI's spinner row can
	// count legs during multi-minute consults. Concurrent panels on one
	// conversation each hold their OWN batch entry (2026-08-25 audit P2 —
	// a shared entry corrupted a surviving panel's tally); the poll
	// snapshot sums batches, and a finishing panel removes only its own.
	// Every answering leg (answer or error) bumps its own batch's Done.
	s.mu.Lock()
	// Mid-delete/deleted lane bar (2026-08-25 review follow-up): the tally
	// registration IS the panel's liveness claim — w loads inside this
	// hold so the check races neither the delete's flag nor its commit.
	w, werr := s.store.GetWorkstream(ctx, c.WorkstreamID)
	if werr == nil {
		werr = s.guardLiveWorkstreamLocked(w)
	}
	if werr != nil {
		s.mu.Unlock()
		return Response{}, fmt.Errorf("panel: %w", werr)
	}
	prog := &PanelProgress{}
	for _, m := range models {
		prog.Legs = append(prog.Legs, PanelLeg{Model: m.model + "@" + m.provider})
	}
	prog.Total = len(models)
	s.panelProg[c.ID] = append(s.panelProg[c.ID], prog)
	s.mu.Unlock()
	defer func() {
		// Runs after wg.Wait(): every leg of THIS batch has answered, so
		// removing it can no longer race its own goroutines; other
		// in-flight consults keep their batches and leg indices untouched.
		s.mu.Lock()
		batches := s.panelProg[c.ID]
		for i, b := range batches {
			if b == prog {
				s.panelProg[c.ID] = append(batches[:i:i], batches[i+1:]...)
				break
			}
		}
		if len(s.panelProg[c.ID]) == 0 {
			delete(s.panelProg, c.ID)
		}
		s.mu.Unlock()
	}()
	results := make([]PanelResult, len(models))
	var wg sync.WaitGroup
	for i, m := range models {
		wg.Add(1)
		go func(legIdx int) {
			defer wg.Done()
			label := m.model + "@" + m.provider
			// P1 #9: per-leg outer deadline (the designLeg/auditLeg
			// precedent) — one worst-case attempt chain; a hung gateway
			// dies a typed timeout and the panel answers on the surviving
			// legs instead of holding the consult for hours (16 tool
			// rounds × 1173s worst).
			lctx, cancel := context.WithTimeout(ctx, s.legTimeout(m.model))
			defer cancel()
			resp, calls, err := client.QueryWithTools(lctx, m.model, system, text, tools, exec.Execute, 0)
			s.mu.Lock()
			prog.Done++
			prog.Legs[legIdx].Done = true
			prog.Legs[legIdx].Error = err != nil
			s.mu.Unlock()
			if err != nil {
				results[i] = PanelResult{Model: label, Error: err.Error(), ToolCalls: calls}
				return
			}
			results[i] = PanelResult{
				Model: label, Text: resp.Text, ToolCalls: calls,
				Truncated: resp.Truncated, Budget: resp.Budget,
				OutputTokens: resp.OutputTokens, Escalations: resp.Escalations,
				RequestSHA16: resp.RequestSHA16, RequestBytes: resp.RequestBytes,
			}
		}(i)
	}
	wg.Wait()

	// Journal the panel responses as an agent_text event.
	if _, err := s.store.AppendEvent(ctx, c.ID, store.EventAgentText, mustJSON(map[string]interface{}{
		"text":   formatPanelResults(results),
		"panel":  true,
		"models": results,
	})); err != nil {
		return Response{}, err
	}
	// Journal agent_done so the GUI knows the run is complete.
	if _, err := s.store.AppendEvent(ctx, c.ID, store.EventAgentDone, mustJSON(map[string]interface{}{
		"panel": true,
	})); err != nil {
		return Response{}, err
	}

	return Response{Event: &ev}, nil
}

// PanelResult is one model's response from a /panel query. ToolCalls (E1)
// is the daemon-side audit of every read the model made through the
// scoped FS tools — journaled with the result so /panel answers are
// traceable to the exact files they saw.
type PanelResult struct {
	Model     string          `json:"model"`
	Text      string          `json:"text"`
	Error     string          `json:"error,omitempty"`
	ToolCalls []moa.ToolAudit `json:"tool_calls,omitempty"`
	// Output-budget ledger (journaling: falsifiable, per the epoch-fold
	// ledger principle). Truncated=true means Text is a flagged partial at
	// the model's hard output cap, not an error; Budget is that final
	// max_tokens and Escalations the bumps taken to reach it.
	Truncated    bool             `json:"truncated,omitempty"`
	Budget       int              `json:"budget,omitempty"`
	OutputTokens int              `json:"output_tokens,omitempty"`
	Escalations  []moa.Escalation `json:"escalations,omitempty"`
	// Request receipt (R-W1.5): sha16 + length of the exact request body
	// whose answer shipped (the final round for tool loops). Absent on
	// error legs — no answer shipped, nothing to attest.
	RequestSHA16 string `json:"request_sha16,omitempty"`
	RequestBytes int    `json:"request_bytes,omitempty"`
}

// truncationMarker renders the visible badge appended to a flagged partial
// answer (panel and vision paths): a half answer with a stated reason beats
// a black-screen error for display-only content.
func truncationMarker(budget, escalations int) string {
	return fmt.Sprintf("\n\n*[output truncated at the %d-token cap after %d budget escalation(s)]*", budget, escalations)
}

// formatPanelResults renders the N model responses as readable text for the
// journal's agent_text event.
func formatPanelResults(results []PanelResult) string {
	var b strings.Builder
	for i, r := range results {
		if i > 0 {
			b.WriteString("\n\n---\n\n")
		}
		b.WriteString("## ")
		b.WriteString(r.Model)
		b.WriteString("\n\n")
		if r.Error != "" {
			b.WriteString("(error: ")
			b.WriteString(r.Error)
			b.WriteString(")")
		} else {
			b.WriteString(r.Text)
			if r.Truncated {
				b.WriteString(truncationMarker(r.Budget, len(r.Escalations)))
			}
		}
		if len(r.ToolCalls) > 0 {
			b.WriteString("\n\n— tools: ")
			b.WriteString(summarizeToolCalls(r.ToolCalls))
		}
	}
	return b.String()
}

// summarizeToolCalls renders a one-line E1 audit: per-tool counts, bytes
// returned, and error marks (grouped in first-seen order).
func summarizeToolCalls(calls []moa.ToolAudit) string {
	type agg struct{ n, bytes, errs int }
	var order []string
	aggs := map[string]*agg{}
	for _, c := range calls {
		a, ok := aggs[c.Name]
		if !ok {
			a = &agg{}
			aggs[c.Name] = a
			order = append(order, c.Name)
		}
		a.n++
		a.bytes += c.ResultBytes
		if c.Error != "" {
			a.errs++
		}
	}
	parts := make([]string, 0, len(order))
	for _, name := range order {
		a := aggs[name]
		p := fmt.Sprintf("%s×%d (%s)", name, a.n, humanBytes(a.bytes))
		if a.errs > 0 {
			p += fmt.Sprintf(", %d err", a.errs)
		}
		parts = append(parts, p)
	}
	return strings.Join(parts, ", ")
}

// humanBytes renders a byte count compactly (B under 1KB, else KB).
func humanBytes(n int) string {
	if n < 1024 {
		return fmt.Sprintf("%dB", n)
	}
	return fmt.Sprintf("%.1fKB", float64(n)/1024)
}

// handleVisionQuery routes a /vision prompt to K3 (the only vision-capable
// model on the gateway) via direct API. Unlike /panel which fans out to N
// models, /vision uses a single model because GLM/DS lack vision capability.
// The prompt text is sent to K3 via direct HTTP API. Image content blocks
// are sent as Anthropic image content blocks when attachments are provided.
func (s *Server) handleVisionQuery(ctx context.Context, c *store.Conversation, text string, attachments []string) (Response, error) {
	if text == "" {
		return Response{}, fmt.Errorf("/vision: prompt text is required after /vision")
	}
	// M12: same gates + slash-slot registration as /panel (one critical
	// section — see handlePanelQuery).
	s.mu.Lock()
	if runID, ok := s.byConv[c.ID]; ok {
		if meta := s.runs[runID]; meta != nil && !meta.finished {
			s.mu.Unlock()
			return Response{}, fmt.Errorf("send_message: agent already running for conversation %d", c.ID)
		}
	}
	if err := s.gateAutoDistillForSendLocked(ctx, c.ID); err != nil {
		s.mu.Unlock()
		return Response{}, err
	}
	s.slashing[c.ID]++
	s.mu.Unlock()
	defer s.releaseSlashSlot(ctx, c.ID)

	// slashVisionModel (slashctx.go): K3 — the only vision-capable model.
	w, err := s.store.GetWorkstream(ctx, c.WorkstreamID)
	if err != nil {
		return Response{}, err
	}

	// Same slash-context ordering as /panel: the block is assembled before
	// the /vision user_message journals, so it never contains the vision
	// question itself. One events fetch feeds the conversation tail.
	var events []store.Event
	if evs, lerr := s.store.ListEvents(ctx, c.ID, 0); lerr == nil {
		events = evs
	}
	scope := resolvePanelContextScope()
	system := "You are a vision-capable coding assistant. Analyze the image or screenshot described in the prompt. Identify visual issues, layout problems, or design suggestions."
	block, receipt, convBlock, conv := s.slashContextBlock(ctx, w.Name, c.ID, text, events, scope, slashModeVision)
	if block != "" {
		system += "\n\n---\n\n" + block
	}

	// Pre-read the images BEFORE journaling, so the user_message receipt
	// covers exactly the bytes the gateway request will carry (ADR-0003):
	// the attachment paths are journaled either way, and image_bytes only
	// when every file read succeeded. A read failure keeps the old error
	// text and skips the gateway call — the user_message is still journaled.
	images := make([]moa.VisionImage, 0, len(attachments))
	// imageShas (M18 W2 item 4): sha16 of each image's file bytes, ALIGNED
	// with attachments by index — the per-image counterpart of image_bytes.
	// Read failures leave the entry "" (absent while the slice stays
	// aligned); entries past the first failure were never attempted, so
	// they stay "" too — a receipt never claims bytes it did not read.
	imageShas := make([]string, len(attachments))
	imageBytes := 0
	var imageErr error
	for i, p := range attachments {
		data, rerr := os.ReadFile(p)
		if rerr != nil {
			imageErr = fmt.Errorf("moa: read image %s: %w", p, rerr)
			break
		}
		images = append(images, moa.VisionImage{Path: p, MediaType: moa.ImageMediaType(p), Data: data})
		imageBytes += len(data)
		imageShas[i] = sha16(data)
	}

	// Journal the user message with the injection receipt, the effective
	// context scope, the assembled prompt size (item D), and the
	// conversation tail's replay receipt (dropped_seqs on omission).
	msgPayload := slashUserMessagePayload("/vision", text, receipt, scope, len(system)+len(text), conv)
	if len(attachments) > 0 {
		msgPayload["attachments"] = attachments
		msgPayload["image_sha16"] = imageShas
		if imageErr == nil {
			msgPayload["image_bytes"] = imageBytes
		}
	}
	ev, err := s.store.AppendEvent(ctx, c.ID, store.EventUserMessage, mustJSON(msgPayload))
	if err != nil {
		return Response{}, err
	}

	// M18 W2 item 4: fail closed BEFORE any moa call — the attempt
	// (user_message above) and the breach (agent_error below) both stay on
	// record (the send path's evidence-first ordering).
	if aerr := s.assertSlashReceipts(block, receipt, convBlock); aerr != nil {
		return Response{}, s.failRun(ctx, c.ID, fmt.Errorf("slash receipt assertion failed: %w", aerr))
	}

	client := s.sharedMoa() // P1 #10: shared sem, not a per-leg one
	// P1 #9: same outer deadline as the /panel leg (one worst-case
	// attempt chain) — a hung vision leg must not hold the consult.
	vctx, cancel := context.WithTimeout(ctx, s.legTimeout(slashVisionModel))
	defer cancel()
	var res moa.Result
	switch {
	case imageErr != nil:
		err = imageErr
	case len(images) > 0:
		res, err = client.QueryWithImages(vctx, slashVisionModel, system, text, images)
	default:
		res, err = client.Query(vctx, slashVisionModel, system, text)
	}

	var resultText string
	if err != nil {
		resultText = "(vision error: " + err.Error() + ")"
	} else {
		resultText = "## " + slashVisionModel + "\n\n" + res.Text
		if res.Truncated {
			resultText += truncationMarker(res.Budget, len(res.Escalations))
		}
	}

	agentPayload := map[string]interface{}{
		"text":   resultText,
		"vision": true,
	}
	if err == nil && res.Truncated {
		agentPayload["truncated"] = true
		agentPayload["output_tokens"] = res.OutputTokens
	}
	if _, err := s.store.AppendEvent(ctx, c.ID, store.EventAgentText, mustJSON(agentPayload)); err != nil {
		return Response{}, err
	}
	if _, err := s.store.AppendEvent(ctx, c.ID, store.EventAgentDone, mustJSON(map[string]interface{}{
		"vision": true,
	})); err != nil {
		return Response{}, err
	}

	return Response{Event: &ev}, nil
}

// A line like "I cannot accept this" must NOT match — only a verdict token
// on its own or as the first word of the line counts. The LAST verdict line
// wins: models think out loud and emit verdict-shaped headers mid-analysis,
// and a crafted diff can prime a stray early token — first-match would read
// a concluding NEEDS_FIXES as an earlier accidental ACCEPT. Comments are the
// text after that line; when it is empty (the prompted shape is analysis
// first, verdict last), they fall back to the pre-verdict analysis so a
// recorded vote keeps its justification. Unparseable output degrades to
// needs_fixes — a review must never silently read as an accept.
func parseVerdict(model, text string) ReviewResult {
	lines := strings.Split(text, "\n")
	verdict, last := "", -1
	for i, line := range lines {
		up := strings.ToUpper(strings.TrimSpace(line))
		v := ""
		switch {
		case up == "NEEDS_FIXES" || strings.HasPrefix(up, "NEEDS_FIXES ") || strings.HasPrefix(up, "NEEDS FIXES"):
			v = "needs_fixes"
		case up == "REJECT" || strings.HasPrefix(up, "REJECT "):
			v = "reject"
		case up == "ACCEPT" || strings.HasPrefix(up, "ACCEPT "):
			v = "accept"
		}
		if v != "" {
			verdict, last = v, i
		}
	}
	if verdict == "" {
		return ReviewResult{Model: model, Verdict: "needs_fixes", Comments: strings.TrimSpace(text)}
	}
	comments := strings.TrimSpace(strings.Join(lines[last+1:], "\n"))
	if comments == "" {
		// Compliant panels put the verdict on the FINAL line (both prompts
		// demand think-first, verdict-last), so "text after it" is empty for
		// every vote and the justification is silently dropped — the M16
		// blocked-row reviews carried three empty comments. Fall back to
		// the pre-verdict analysis so the recorded vote carries its
		// reasons.
		comments = capDetail(strings.TrimSpace(strings.Join(lines[:last], "\n")))
	}
	return ReviewResult{
		Model:    model,
		Verdict:  verdict,
		Comments: comments,
	}
}

// handleGetSettings returns the effective daemon settings: prefs.md values
// where present, compiled-in adapter defaults elsewhere.
func (s *Server) handleGetSettings(ctx context.Context, req Request) (Response, error) {
	if _, err := s.resolveProject(ctx, req.ProjectRoot); err != nil {
		return Response{}, fmt.Errorf("get_settings: %w", err)
	}
	st := adapter.ReadSettings()
	return Response{Settings: &st}, nil
}

// handleUpdateSettings writes the request's non-empty fields to prefs.md and
// returns the resulting effective settings. The daemon is not restarted —
// adapters re-read prefs on every run, so changes take effect on next run.
func (s *Server) handleUpdateSettings(ctx context.Context, req Request) (Response, error) {
	if _, err := s.resolveProject(ctx, req.ProjectRoot); err != nil {
		return Response{}, fmt.Errorf("update_settings: %w", err)
	}
	if req.Settings == nil {
		return Response{}, fmt.Errorf("update_settings: settings object is required")
	}
	if err := adapter.UpdateSettings(*req.Settings); err != nil {
		return Response{}, err
	}
	st := adapter.ReadSettings()
	return Response{Settings: &st}, nil
}

// distillTimeout bounds the blocking distill agent run on the OMP route
// (the prefs `distill_via: moa` route derives its own outer bound from
// moa.TimeoutForModel — see runDistillViaMoa). The adapter wrapper
// applies its own timeout on a similar scale; a skew between the two only
// changes which error message the user sees.
const distillTimeout = 10 * time.Minute

// handleDistill summarizes the conversation's journaled events into a wiki
// note at <project>/wiki/<workstream>-epoch-<N>.md (N = the distilled epoch)
// and starts a new epoch. The summary comes from a single completion
// (OMP-adapter one-shot by default; direct moa.Query when prefs
// `distill_via: moa`, R-W2) — it blocks only its own
// connection (M11 P0) and never touches the user's working tree. Old events
// stay in the append-only journal; only the epoch counter moves (ADR-0002).
func (s *Server) handleDistill(ctx context.Context, req Request) (Response, error) {
	c, err := s.checkConversation(ctx, req.ConversationID)
	if err != nil {
		return Response{}, err
	}
	// Reserve this conversation's distill slot under the mutex (M11 P0), then
	// drop the lock for the 10-minute agent run so other connections
	// (poll_events, cancel, …) stay responsive throughout the distill.
	s.mu.Lock()
	// M12: a manual distill supersedes any scheduled auto-distill (Journaled
	// disarm — the trigger does not re-fire on top of the manual fold).
	s.disarmAutoLocked(ctx, c.ID, "superseded_by_manual")
	if _, ok := s.distilling[c.ID]; ok {
		s.mu.Unlock()
		return Response{}, fmt.Errorf("distill: already in progress for conversation %d", c.ID)
	}
	if runID, ok := s.byConv[c.ID]; ok {
		if meta := s.runs[runID]; meta != nil && !meta.finished {
			s.mu.Unlock()
			return Response{}, fmt.Errorf("distill: agent still running for conversation %d", c.ID)
		}
	}
	// M12 (slash gate, reciprocal half): a live /panel or /vision query is
	// about to journal answers into this conversation; folding now would
	// mark them distilled unseen. Refuse like a live run does.
	if s.slashing[c.ID] > 0 {
		s.mu.Unlock()
		return Response{}, fmt.Errorf("distill: slash query in progress for conversation %d", c.ID)
	}
	// Mid-delete/deleted lane bar (2026-08-25 review follow-up): w loads
	// under this same hold so the read cannot slip past the delete's
	// flag window.
	w, err := s.store.GetWorkstream(ctx, c.WorkstreamID)
	if err != nil {
		s.mu.Unlock()
		return Response{}, err
	}
	if err := s.guardLiveWorkstreamLocked(w); err != nil {
		s.mu.Unlock()
		return Response{}, fmt.Errorf("distill: %w", err)
	}
	s.distilling[c.ID] = struct{}{}
	s.distillKind[c.ID] = distillTriggerManual
	s.mu.Unlock()
	defer func() {
		s.mu.Lock()
		delete(s.distilling, c.ID)
		delete(s.distillKind, c.ID)
		s.mu.Unlock()
	}()
	resp, err := s.distillCore(ctx, c, distillTriggerManual)
	if err != nil {
		// M12: failed manual distills journal too — an error toast alone
		// leaves no durable trace (the daemon.log line rides on the GUI
		// actually watching the log).
		_, _ = s.store.AppendEvent(context.WithoutCancel(ctx), c.ID, store.EventMemoryUpdate, mustJSON(map[string]interface{}{
			"layer":  "distill",
			"cause":  "failed",
			"detail": err.Error(),
		}))
		return Response{}, err
	}
	return resp, nil
}

// distillCore is the shared distill pipeline both the manual command and
// the M12 auto-distill scheduler drive: render the epoch window, one-shot
// the note, learner + skill gate, then the fold marker. trigger is one of
// distillTriggerManual/idle/startup/urgent and lands verbatim in the
// marker payload with the measured window stats (spend per trigger class
// becomes a SQL query). Errors are returned WITHOUT journaling — the
// caller owns outcome journaling (manual: layer "distill"; auto:
// layer "auto_distill" with its backoff semantics).
func (s *Server) distillCore(ctx context.Context, c store.Conversation, trigger string) (Response, error) {
	start := time.Now() // M6: distill duration metric (ledger)
	w, err := s.store.GetWorkstream(ctx, c.WorkstreamID)
	if err != nil {
		return Response{}, err
	}
	events, err := s.store.ListEvents(ctx, c.ID, 0)
	if err != nil {
		return Response{}, err
	}

	// The note covers only the current epoch's window — events after the
	// previous distill marker (the same FoldWindow arithmetic the new
	// marker records below). Rendering the full history let the prompt
	// grow past the kernel's ARG_MAX (exec E2BIG before the agent even
	// started, 2026-08-09) and past the model's context as the journal
	// grows.
	window := windowEvents(events)
	firstSeq, lastSeq := FoldWindow(events)
	// The nothing-new guard counts CONTENT, not any row above the
	// boundary: post-fold bookkeeping (apply rows, wiki commits) lands
	// above the marker but is authored by the fold itself — the same
	// attribution unownedFoldGrowth pins for the supersession probe. A
	// window holding only such rows folds pure self-telemetry, so it is
	// empty for this guard (a successful distill must not invite a
	// follow-up distill that summarizes the first one's receipts).
	if len(window) == 0 || !unownedFoldGrowth(events, firstSeq-1) {
		return Response{}, fmt.Errorf("distill: nothing journaled since the last distill")
	}
	winStats := measureWindow(window)
	// Pin the marker window NOW, against the exact snapshot the prompt
	// renders from (P1-2). INVARIANT: a marker's window is always exactly
	// the rendered window — never a post-hoc re-list. The old re-list
	// after the learner/gate tail claimed rows the note never saw (fold
	// bookkeeping, and any user message a committed-phase send journaled
	// mid-fold), and those rows then sat below the replay boundary,
	// invisible to future prompts.

	// M12 (D-todo): surviving open plan items ride the distill prompt as
	// labeled authoritative state → the note's Open loops are seeded from
	// truth (the distiller can't drop a loop it was explicitly handed).
	prompt, om := distillPrompt(window)
	if seed := distillTodoSeed(events); seed != "" {
		prompt += "\n\n" + seed
	}
	note, rec, err := s.runDistillAgent(ctx, prompt)
	if err != nil {
		return Response{}, fmt.Errorf("distill: %w", err)
	}

	// M12 cancel-before-note: an auto distill that a send/steer/slash
	// cancelled while the one-shot was in flight aborts HERE — after the
	// agent returned, before any artifact. Zero writes happened, the
	// cancelled_by_send row is already journaled by the cancelling send,
	// and the trigger re-arms off the send's own activity.
	if trigger != distillTriggerManual {
		s.mu.Lock()
		ifl := s.autoInFlight[c.ID]
		cancelled := ifl != nil && ifl.cancelled
		if !cancelled && ifl != nil {
			// P1-2: commit the fold. Past this point the gate stops
			// cancelling (inputPassed records the pass instead), because a
			// cancelled_by_send row for a fold that then lands is a
			// journal lie — the cancel below is a no-op by design.
			ifl.committed = true
		}
		s.mu.Unlock()
		if cancelled {
			return Response{}, errAutoDistillCancelled
		}
		// Past the pre-note checkpoint the fold must complete: cancel only
		// guards against a phantom marker for a window the user just
		// resumed; once the note write starts, journal work switches to a
		// cancel-free context so an in-flight cancel can never half-land
		// (note on disk, marker append failed).
		ctx = context.WithoutCancel(ctx)
	}

	wikiDir := filepath.Join(s.projectRoot, "wiki")
	if err := os.MkdirAll(wikiDir, 0o755); err != nil {
		return Response{}, fmt.Errorf("distill: create wiki dir: %w", err)
	}
	wikiPath := filepath.Join(wikiDir, fmt.Sprintf("%s-epoch-%d.md", w.Name, c.Epoch))
	// Safety net (GLM defect 2): strip any preamble before the first
	// `# ` heading — the distiller sometimes emits chain-of-thought or
	// tool-output fragments before the actual summary. The prompt now
	// forbids this, but the strip is defense-in-depth.
	note = stripPreamble(note)
	if err := writeFileWithin(s.projectRoot, wikiPath, note, 0o644); err != nil {
		return Response{}, fmt.Errorf("distill: write wiki note: %w", err)
	}

	// M6: contradiction pass (daemon-side, no LLM). Runs between the note
	// write and the learner, before the epoch moves.
	noteName := fmt.Sprintf("%s-epoch-%d", w.Name, c.Epoch)
	contradictions := s.runContradictionPass(ctx, c.ID, noteName, note, c.Epoch)

	// Panel review lineup (prefs `review:`). Empty = the gate is inert:
	// proposals journal unreviewed, the batch stays pending, and the human
	// apply path remains the decision.
	models := parseReviewModels(adapter.LoadPrefsRaw("review"))

	// Legacy sweep, before the new learner output exists: an older
	// unconsumed batch (pre-panel rows, or a leftover of a crash/refused
	// auto-apply) still deserves its panel decision — no batch drops
	// silently under supersession anymore. Its rows stay this fold's own
	// bookkeeping for the supersession probe below.
	if len(models) > 0 {
		s.sweepPendingBatch(ctx, c, w, models)
	}

	// Learner pass (M4 §2 + M9): propose behavior rules and skill procedures
	// from the note just written. Automatic triggers skip the learner by
	// default (P1-12, learnerAutoEnabled: 28 automatic runs over 4 days
	// produced zero applied rules, so the one-shot is manual-only unless
	// the prefs escape hatch opts back in). The policy narrows HERE in
	// distillCore because this is where manual and automatic folds meet —
	// runLearner itself stays trigger-blind. runLearner no longer journals —
	// distillCore journals after gating the proposals. A learner
	// failure degrades to a journaled memory_update and never fails the
	// distill.
	var proposals []MemoryProposal
	var reaffirm []string
	var stats vetoStats
	var learnerRec *moaReceipt
	if trigger == distillTriggerManual || learnerAutoEnabled() {
		proposals, reaffirm, stats, learnerRec, _ = s.runLearner(ctx, c.ID, noteName, note, c.Epoch)
	}

	// Panel gate (M9 origins, now all targets): every proposal × every
	// review model fans out. memory/user rules are gated straight into the
	// batch — their reviews ride MemoryProposal.Reviews for the post-fold
	// decision and the outcome view. Skills keep the stricter pre-batch
	// split: all-reject → auto_discard (dropped + journaled), anything else
	// stays with reviews for the same post-fold decision.
	nonSkills, skillProposals := splitSkillProposals(proposals)
	if len(models) > 0 && len(nonSkills) > 0 {
		allReviews := s.reviewProposals(ctx, nonSkills, models, func(p MemoryProposal) string {
			return ruleReviewPrompt(p, note)
		})
		for i := range nonSkills {
			nonSkills[i].Reviews = allReviews[i]
		}
	}
	var humanGateSkills []MemoryProposal
	if len(skillProposals) > 0 {
		gateResults := s.gateSkillProposals(ctx, skillProposals, models, note)
		for _, gr := range gateResults {
			if gr.Tier == "auto_discard" {
				// Journaled as skill_gate event (auditable, never in the batch).
				if _, err := s.store.AppendEvent(ctx, c.ID, store.EventReviewAction, mustJSON(map[string]interface{}{
					"action":  "skill_gate",
					"epoch":   c.Epoch,
					"name":    gr.Proposal.Name,
					"tier":    "auto_discard",
					"reviews": gr.Reviews,
				})); err != nil {
					// Fallback: journal a memory_update so the discard is never
					// invisible (the skill_gate event exists for auditability).
					_, _ = s.store.AppendEvent(ctx, c.ID, store.EventMemoryUpdate, mustJSON(map[string]interface{}{
						"layer":  "skills",
						"cause":  "gate_journal_failed",
						"detail": fmt.Sprintf("auto_discard %s: %s", gr.Proposal.Name, err.Error()),
					}))
				}
			} else {
				// human_gate: attach reviews, include in the batch.
				p := gr.Proposal
				p.Reviews = gr.Reviews
				humanGateSkills = append(humanGateSkills, p)
			}
		}
	}

	// Journal memory_propose with non-skills + human_gate skills.
	// If zero surviving proposals total, skip (same as today's len==0 check).
	batchProposals := append(nonSkills, humanGateSkills...)
	if len(batchProposals) > 0 {
		payload := map[string]interface{}{
			"action":    "memory_propose",
			"epoch":     c.Epoch,
			"proposals": batchProposals,
			"reaffirm":  reaffirm,
			"stats":     stats,
		}
		if _, err := s.store.AppendEvent(ctx, c.ID, store.EventReviewAction, mustJSON(payload)); err != nil {
			_, _ = s.store.AppendEvent(ctx, c.ID, store.EventMemoryUpdate, mustJSON(map[string]interface{}{
				"layer":  "learner",
				"cause":  "failed",
				"detail": fmt.Sprintf("journal memory_propose: %s", err.Error()),
			}))
		}
	}

	// P1-2 committed-phase supersession probe (auto triggers only): the
	// pinned window above keeps every marker honest, but journal growth
	// past lastSeq that this fold did not author — its own mid-fold rows
	// are contradiction candidates, skill_gate discards, and the
	// memory_propose batch, and the attributed metadata bookkeeping
	// (curate/index/pins layers) is read fresh at prompt time rather than
	// render-covered — with no post-commit input through the gate
	// means the conversation moved under the fold unattributed. Abandon:
	// no marker, no epoch move; the orphan note is deleted (the journal
	// never links it), the skip is journaled, and runAutoDistill re-arms
	// a fresh fold that renders the grown window. A post-commit
	// send/steer/slash (inputPassed) is attributed: the fold lands and
	// those rows stay above the boundary for the next epoch.
	if trigger != distillTriggerManual {
		probe, perr := s.store.ListEvents(ctx, c.ID, 0)
		s.mu.Lock()
		inputPassed := s.autoInFlight[c.ID] != nil && s.autoInFlight[c.ID].inputPassed
		s.mu.Unlock()
		if perr == nil && !inputPassed && unownedFoldGrowth(probe, lastSeq) {
			// Orphan note: never marker-linked, never kept. Best-effort —
			// a failed remove only strands an unlinked file, but log it so
			// the hole is visible instead of silently discarded.
			if rerr := os.Remove(wikiPath); rerr != nil {
				log.Printf("auto-distill: remove orphan note %s: %v", wikiPath, rerr)
			}
			s.journalAuto(ctx, c.ID, "skipped", fmt.Sprintf(
				"trigger=%s window_events=%d window_bytes=%d reason=superseded_by_activity",
				trigger, winStats.events, winStats.eligibleBytes))
			return Response{}, errAutoDistillSuperseded
		}
	}

	// P0-4 batch-supersede snapshot: capture the PRE-marker pending batch
	// BEFORE the fold marker re-pins the pending epoch to newEpoch−1.
	// findPendingBatch resolves pending from the LATEST distill marker,
	// so the old pin stops resolving the moment the marker lands — a
	// post-marker scan could never see the orphan again. Best-effort: a
	// list failure logs and skips the row (bookkeeping never fails the
	// fold).
	var prevBatch pendingBatch
	if pre, perr := s.store.ListEvents(ctx, c.ID, 0); perr == nil {
		prevBatch = findPendingBatch(pre)
	} else {
		log.Printf("distill: batch_superseded snapshot: list events: %v", perr)
	}

	newEpoch, err := s.store.IncrementEpoch(ctx, c.ID)
	if err != nil {
		return Response{}, err
	}
	// Fold provenance (epoch-fold root fix): the marker records the folded
	// window [first_seq, last_seq] explicitly instead of letting consumers
	// reverse-derive it from journal scans, plus the note's content hash so
	// the fold is falsifiable against the artifact on disk. The window is
	// the render-time pin from above (the INVARIANT there) — rows the fold
	// itself journaled after the render sit ABOVE it for the next epoch.
	marker := map[string]interface{}{
		"action":         "distill",
		"epoch":          newEpoch,
		"wiki_path":      wikiPath,
		"duration_ms":    time.Since(start).Milliseconds(), // M6: ledger metric
		"contradictions": contradictions,                   // M6: contradiction report count
		"first_seq":      firstSeq,
		"last_seq":       lastSeq,
		"note_path":      filepath.Join("wiki", noteName+".md"),
		"note_sha":       sha16([]byte(note)),
		// M12: provenance for spend-per-trigger-class queries. window_events
		// counts every journaled event in the folded window; window_bytes is
		// the measured POST-FILTER render size (M17 F1: thinking/tool-result
		// tombstones and action/cause one-liners; /panel and /vision
		// advisory agent_text excluded — what the distiller was sent,
		// nothing more).
		"trigger":       trigger,
		"window_events": winStats.events,
		"window_bytes":  winStats.eligibleBytes,
	}
	// R-W2 moa receipts (additive; present ONLY on the prefs
	// `distill_via: moa` route — an OMP-route marker carries none of
	// these): which model served the note off which prompt bytes, plus
	// the output-budget ledger moa returned. prompt_sha16 makes the fold
	// falsifiable against the exact wire request — the attestation the
	// receipt exemption ledger concedes for the OMP route.
	if rec != nil {
		marker["via"] = rec.via
		marker["model"] = rec.model
		marker["prompt_sha16"] = rec.promptSHA
		marker["output_tokens"] = rec.outputTokens
		marker["budget"] = rec.budget
		if len(rec.escalations) > 0 {
			marker["escalations"] = rec.escalations
		}
	}
	// R-W3 learner receipts (same additive rule: present ONLY on the prefs
	// `learner_via: moa` route). The learner rides inside the fold, so its
	// wire-request attestation lands here under the "learner_" prefix,
	// unambiguous next to the distill receipt's bare names.
	if learnerRec != nil {
		learnerRec.journal(marker, "learner_")
	}
	// M18 W2 item 2: the full-window first_seq/last_seq keep their
	// epoch-window meaning (the FoldWindow pin above); the omitted_* keys
	// name the held-back PREFIX the prompt cap cut out of that window — the
	// fact the prompt's omission line declares, now journaled (present ONLY
	// when the cap dropped events; additive, absent otherwise).
	if om.count > 0 {
		marker["omitted_count"] = om.count
		marker["omitted_first_seq"] = om.firstSeq
		marker["omitted_last_seq"] = om.lastSeq
	}
	distillEv, err := s.store.AppendEvent(ctx, c.ID, store.EventReviewAction, mustJSON(marker))
	if err != nil {
		return Response{}, err
	}

	// P0-4: the marker just re-pinned the pending epoch to newEpoch−1.
	// Close the ledger on an older unconsumed batch the re-pin orphaned
	// (crash-lost between propose and apply, or sweeper-refused via
	// user.md overflow) — one idempotent batch_superseded row, journaled
	// immediately after the marker so the crash window is a single append.
	s.journalBatchSuperseded(ctx, c.ID, prevBatch, newEpoch)

	// Panel-gated memory apply: the fold committed and the marker made
	// this distill's batch the pending one — decide it from the riding
	// reviews and consume it now. Post-fold by design: an abandoned fold
	// (supersession probe above) deletes its note, and rules must never
	// apply with evidence that no longer exists; the apply rows land above
	// the pinned window, unclaimed by any note and visible in replay.
	if len(models) > 0 {
		s.autoApplyProposals(ctx, c, batchProposals, len(models))
	}

	// M6: ledger append (best-effort, after the distill event so its seq
	// is citable; AFTER the panel apply — P0-4 — so the epoch section's
	// "memory apply" row records the outcome the fold just decided instead
	// of rendering "pending" on the apply that lands a moment later, the
	// "30 batches proposed, 0 applied" blind spot). Section header uses
	// c.Epoch — the distilled note's epoch, not newEpoch (the counter
	// after increment).
	s.journalDistillLedger(ctx, c.ID, c.Epoch, distillEv)

	// M12: the fold retired the window. Evaluate the conditional
	// auto-curate (never chained: it fires only when the notes/age
	// thresholds say so).
	s.maybeAutoCurate(w.ProjectID, c.ID)

	// The note (+ any same-pass metadata) is daemon output on a tracked
	// surface — durable beats rebuildable, and review has already happened
	// upstream (the panel gates rules; wiki text itself is journaled).
	s.commitWiki(ctx, c.ID, fmt.Sprintf("distill %s/epoch %d", w.Name, c.Epoch))
	return Response{WikiPath: wikiPath, Epoch: newEpoch, MemoryProposals: len(batchProposals)}, nil
}

// isDistillMarkerEvent reports whether ev is a review_action{action:
// "distill"} fold marker.
func isDistillMarkerEvent(ev store.Event) bool {
	if ev.Type != store.EventReviewAction {
		return false
	}
	var p struct {
		Action string `json:"action"`
	}
	return json.Unmarshal(ev.Payload, &p) == nil && p.Action == "distill"
}

// FoldWindow computes the journal window [firstSeq, lastSeq] that a new
// distill marker folds: the first content row past the newest fold
// boundary through the newest journaled event. events is seq-ascending and
// does NOT yet include the marker about to be appended. The boundary comes
// from the newest marker's explicit last_seq payload when present (the
// pinned schema — rows in (last_seq, marker_seq) the fold did NOT render
// stay visible), falling back to the marker's own seq for pre-schema
// markers (the legacy implicit contract). Marker rows themselves are never
// window content: firstSeq walks past any marker sitting inside the
// boundary gap (a pinned marker always lands after its own last_seq, so a
// fold's bookkeeping and committed-phase inputs are covered by the NEXT
// note while the marker row belongs to neither window). An empty log (or
// nothing journaled since the last marker) yields lastSeq < firstSeq —
// consumers treat that as an empty window. Exported: the rehydration CLI
// derives legacy markers' windows with the same arithmetic (single
// convention).
func FoldWindow(events []store.Event) (firstSeq, lastSeq int) {
	boundary := 0 // folded: rows with seq <= boundary are out of view
	for i := len(events) - 1; i >= 0; i-- {
		if !isDistillMarkerEvent(events[i]) {
			continue
		}
		var p struct {
			LastSeq *int `json:"last_seq"`
		}
		if json.Unmarshal(events[i].Payload, &p) == nil && p.LastSeq != nil {
			boundary = *p.LastSeq // pinned schema
		} else {
			boundary = events[i].Seq // legacy implicit contract
		}
		break
	}
	firstSeq = boundary + 1
	for _, ev := range events {
		if ev.Seq < firstSeq {
			continue
		}
		if !isDistillMarkerEvent(ev) {
			break // first content row
		}
		firstSeq = ev.Seq + 1
	}
	if len(events) > 0 {
		lastSeq = events[len(events)-1].Seq
	}
	return firstSeq, lastSeq
}

// windowEvents returns the events the next distill note should cover: the
// window FoldWindow computes, sliced out of the seq-ascending list with
// fold-marker rows filtered out — markers are bookkeeping, never note
// content (a pinned marker whose own seq sits inside its boundary gap must
// not render into the next fold's prompt, keeping the invariant: a
// marker's window is always exactly the rendered window).
func windowEvents(events []store.Event) []store.Event {
	firstSeq, _ := FoldWindow(events)
	i := sort.Search(len(events), func(i int) bool { return events[i].Seq >= firstSeq })
	tail := events[i:]
	out := make([]store.Event, 0, len(tail))
	for _, ev := range tail {
		if isDistillMarkerEvent(ev) {
			continue
		}
		out = append(out, ev)
	}
	return out
}

// unownedFoldGrowth reports whether the journal grew past a fold's pinned
// window end with rows the fold itself could not have authored — the only
// rows distillCore journals between render and marker are contradiction
// candidates (memory_update{layer:note, cause:contradiction_candidate};
// legacy cause:retract rows from curated/human paths stay attributed on the
// same fold-authorship grounds), skill_gate discards, the memory_propose
// batch, and their journal-failure fallbacks. Also
// attributed (never render-covered, but safe): the daemon's curated-wiki
// and pins bookkeeping — review_action{action:"curate"} and
// memory_update{layer: curator | index | pins}, all causes. Those rows
// describe metadata layers read FRESH at prompt time (wiki topics,
// index.md, pins.md), not conversation coverage the note claims, so an
// auto-curate or /pin landing mid-fold must not abort it: the pinned
// marker handles them honestly (above lastSeq → visible in the window and
// replay, merely unclaimed by this epoch's note). Anything else (a user
// message, a slash answer, a diff accept, a todo merge, …) is
// unattributed growth: the conversation moved under the fold.
func unownedFoldGrowth(events []store.Event, lastSeq int) bool {
	for i := len(events) - 1; i >= 0 && events[i].Seq > lastSeq; i-- {
		ev := events[i]
		switch ev.Type {
		case store.EventReviewAction:
			var p struct {
				Action string `json:"action"`
			}
			// Fold-authored memory-pipeline rows: the gate discards, the
			// propose batch, and the legacy sweep's verdict receipt +
			// apply marker. A memory_apply landing mid-fold from a HUMAN
			// click gets the same attribution on the same grounds as the
			// metadata layers below — it records a prompt-input change,
			// never conversation coverage the note claims.
			if jsonUnmarshalOK(ev.Payload, &p) && (p.Action == "skill_gate" || p.Action == "memory_propose" ||
				p.Action == "memory_gate" || p.Action == "memory_apply" ||
				p.Action == "curate") {
				continue
			}
			return true
		case store.EventMemoryUpdate:
			var p struct {
				Layer string `json:"layer"`
				Cause string `json:"cause"`
			}
			// Fold-authored: contradiction candidates (advisory-only
			// since 2026-08-22) and any legacy retract row, learner/gate/
			// apply failure fallbacks; learner batch bookkeeping
			// (batch_superseded close-outs, P0-4). Metadata layers read
			// FRESH at prompt
			// time (memory.md, user.md, skills, wiki on disk, ledger,
			// curated wiki, index, pins): their rows describe input
			// bookkeeping, not coverage — a same-conversation human apply
			// or wiki commit landing mid-fold must not abort it.
			if jsonUnmarshalOK(ev.Payload, &p) && ((p.Layer == "note" && p.Cause == "retract") ||
				(p.Layer == "note" && p.Cause == "contradiction_candidate") ||
				(p.Layer == "learner" && (p.Cause == "failed" || p.Cause == "batch_superseded")) ||
				p.Layer == "skills" || p.Layer == "memory" || p.Layer == "user" ||
				p.Layer == "apply" || p.Layer == "ledger" || p.Layer == "wiki" ||
				p.Layer == "curator" || p.Layer == "index" || p.Layer == "pins") {
				continue
			}
			return true
		default:
			return true
		}
	}
	return false
}

// journalDistillLedger appends the distill's section to .odo/ledger.md from
// a fresh events scan. Best-effort: a failed ledger write journals
// memory_update{layer:"ledger", cause:"write_failed"} and a failed
// metrics scan journals the same with the underlying list error —
// silently dropping the section would leave an unaccountable hole in the
// ledger (inv 3). The gap journal goes out on a cancel-free copy of ctx:
// when the request's ctx is what failed (client dropped mid-distill), the
// original ctx could never carry the record.
func (s *Server) journalDistillLedger(ctx context.Context, conversationID int64, epoch int, distillEv store.Event) {
	// Resolve the workstream name so the ledger section header is
	// uniquely addressable (GLM defect 2: "epoch 1" appears 4× across
	// workstreams; the curator qualifies topic citations as
	// "(main-epoch-3)" — the ledger must match that discipline).
	wsName := "unknown"
	if conv, err := s.store.GetConversation(ctx, conversationID); err == nil {
		if ws, err := s.store.GetWorkstream(ctx, conv.WorkstreamID); err == nil {
			wsName = ws.Name
		}
	}
	events, lerr := s.store.ListEvents(ctx, conversationID, 0)
	if lerr != nil {
		_, _ = s.store.AppendEvent(context.WithoutCancel(ctx), conversationID, store.EventMemoryUpdate, mustJSON(map[string]interface{}{
			"layer":  "ledger",
			"cause":  "write_failed",
			"detail": "list_events: " + lerr.Error(),
		}))
		return
	}
	if err := appendLedger(s.projectRoot, fmt.Sprintf("%s/epoch %d", wsName, epoch),
		distillLedgerMetrics(events, distillEv, lastRecallCount(events), epoch)); err != nil {
		_, _ = s.store.AppendEvent(ctx, conversationID, store.EventMemoryUpdate, mustJSON(map[string]interface{}{
			"layer":  "ledger",
			"cause":  "write_failed",
			"detail": err.Error(),
		}))
	}
}

// handlePendingCounts reports, per workstream, the number of pending diffs
// plus which workstreams have a live agent run (M3 §3c sidebar badges).
// Read-only: SQL over diffs + a snapshot of the in-memory tables. The two
// phases are split so per-conversation SQL (slash consult / parked-goal
// resolution) runs AFTER s.mu is released — under multi-project badge
// polling those lookups inside the global lock stalled drains and
// admissions daemon-wide.
func (s *Server) handlePendingCounts(ctx context.Context, req Request) (Response, error) {
	p, err := s.resolveProject(ctx, req.ProjectRoot)
	if err != nil {
		return Response{}, fmt.Errorf("pending_counts: %w", err)
	}
	counts, err := s.store.PendingDiffCountsByWorkstream(ctx, p.ID)
	if err != nil {
		return Response{}, err
	}
	snap := s.snapshotBadgeState()
	running := snap.running
	// Advisory slash consults (/panel, /vision, /preview) hold no run-table
	// entry but keep the workstream just as busy for minutes: count them as
	// running so the sidebar dot, the "still running" activity line, and
	// the StatusBar background chip light during the consult (display-only
	// consumers). Conversation lookups are best-effort, mirroring the
	// parked-goals loop below.
	for _, convID := range snap.slashConvs {
		c, err := s.store.GetConversation(ctx, convID)
		if err != nil {
			continue
		}
		dup := false
		for _, id := range running {
			if id == c.WorkstreamID {
				dup = true
				break
			}
		}
		if !dup {
			running = append(running, c.WorkstreamID)
		}
	}
	// W6: parked-goal queue depth per workstream (the badge's queue
	// counterpart; keyed like PendingCounts). Conversation lookups are
	// best-effort — a conversation that vanished between park and poll
	// drops out of the badge, never out of the journal.
	var parkedByWS map[int64]int
	for convID, depth := range snap.parkedDepth {
		c, err := s.store.GetConversation(ctx, convID)
		if err != nil {
			continue
		}
		if parkedByWS == nil {
			parkedByWS = make(map[int64]int)
		}
		parkedByWS[c.WorkstreamID] += depth
	}
	// Stranded crash-recoveries (2026-08-26 memory-replay doctrine):
	// unresolved heal_conflict rows across the whole project — the Memory
	// tab's banner count. SQL prefilters the two marker causes; the pairing
	// fold runs in Go. Best-effort: a ledger read error must never blank
	// the badges.
	stranded := 0
	var strandedOps []StrandedOp
	if ledgerRows, lerr := s.store.ListHealLedgerRows(ctx, p.ID); lerr == nil {
		unresolved, _ := foldHealLedger(ledgerRows)
		stranded = len(unresolved)
		strandedOps = strandedOpRows(unresolved)
	} else {
		log.Printf("pending_counts: stranded-ops fold: %v", lerr)
	}
	return Response{
		PendingCounts:      counts,
		RunningWorkstreams: running,
		ParkedGoals:        parkedByWS,
		AutoDistill:        snap.auto,
		Distilling:         len(snap.distillingConvs) > 0,
		DistillingConvs:    snap.distillingConvs,
		StrandedMemoryOps:  stranded,
		StrandedOps:        strandedOps,
		// Daily-cap suspension disclosure (storm fix): journal-derived,
		// gated on the subsystem being enabled (FIX 3); nil while quiet.
		AutoDistillCapResume: s.autoCapResumeForBadges(ctx, p.ID),
	}, nil
}

// badgeSnapshot is the s.mu-owned slice of a pending_counts answer, copied
// under the lock so the handler can resolve conversation→workstream ids
// with plain store SQL after releasing it.
type badgeSnapshot struct {
	running         []int64
	slashConvs      []int64
	auto            []AutoDistillInfo
	distillingConvs []int64
	parkedDepth     map[int64]int
}

// snapshotBadgeState copies the shared in-memory tables (M11 P0) and
// releases the lock before returning.
func (s *Server) snapshotBadgeState() badgeSnapshot {
	s.mu.Lock()
	defer s.mu.Unlock()
	var snap badgeSnapshot
	for _, meta := range s.runs {
		if !meta.finished {
			snap.running = append(snap.running, meta.workstreamID)
		}
	}
	for convID := range s.slashing {
		snap.slashConvs = append(snap.slashConvs, convID)
	}
	// M12 (D-auto): scheduled auto-distills (composer countdown chip) and
	// in-flight distills ride the same badge poll — the GUI never owns a
	// trigger, it only discloses daemon state.
	for convID, entry := range s.autoPending {
		snap.auto = append(snap.auto, AutoDistillInfo{ConversationID: convID, EtaUnix: entry.fireAt.Unix(), Trigger: entry.trigger})
	}
	for convID := range s.distilling {
		snap.distillingConvs = append(snap.distillingConvs, convID)
	}
	for convID, goals := range s.parked {
		if len(goals) > 0 {
			if snap.parkedDepth == nil {
				snap.parkedDepth = make(map[int64]int)
			}
			snap.parkedDepth[convID] = len(goals)
		}
	}
	return snap
}

// handleListAllPendingDiffs returns every pending diff across all active
// workstreams of the project with full content and workstream labels (P1a
// review inbox). Read-only: no journal writes, no locks (mirrors
// handlePendingCounts). An unreadable diff file leaves Content empty — the
// row stays actionable by ID.
func (s *Server) handleListAllPendingDiffs(ctx context.Context, req Request) (Response, error) {
	p, err := s.resolveProject(ctx, req.ProjectRoot)
	if err != nil {
		return Response{}, fmt.Errorf("list_all_pending_diffs: %w", err)
	}
	rows, err := s.store.ListAllPendingDiffs(ctx, p.ID)
	if err != nil {
		return Response{}, fmt.Errorf("list_all_pending_diffs: %w", err)
	}
	out := make([]DiffInfoEx, 0, len(rows))
	for _, r := range rows {
		info := DiffInfoEx{
			DiffInfo:       DiffInfo{ID: r.ID, Status: r.Status, Path: r.PathOnDisk},
			ConversationID: r.ConversationID,
			WorkstreamID:   r.WorkstreamID,
			WorkstreamName: r.WorkstreamName,
		}
		if b, err := os.ReadFile(r.PathOnDisk); err == nil {
			info.Content = string(b)
		}
		out = append(out, info)
	}
	return Response{OK: true, AllPendingDiffs: out}, nil
}

// handleLedger returns the .odo/ledger.md content as memory_content (same
// shape as read_pins; "" when the file is absent). The ledger is read-only
// in the UI — the daemon is the only writer. Read-only: no journal writes.
func (s *Server) handleLedger(ctx context.Context, req Request) (Response, error) {
	if _, err := s.resolveProject(ctx, req.ProjectRoot); err != nil {
		return Response{}, fmt.Errorf("ledger: %w", err)
	}
	// Contained (2026-08-25 review P0): .odo is committable — a planted
	// symlink must not render external bytes as the daemon's ledger.
	return Response{MemoryContent: readFileWithin(s.projectRoot, filepath.Join(s.projectRoot, ".odo"), ledgerPath(s.projectRoot))}, nil
}

// handleContradictions returns the conversation's note-retraction events
// (memory_update{layer:"note", cause:"retract"}) for the wiki browser's
// "⚠ retracted" badges. Read-only: no journal writes.
func (s *Server) handleContradictions(ctx context.Context, req Request) (Response, error) {
	c, err := s.checkConversation(ctx, req.ConversationID)
	if err != nil {
		return Response{}, err
	}
	events, err := s.store.ListEvents(ctx, c.ID, 0)
	if err != nil {
		return Response{}, err
	}
	var out []store.Event
	for _, ev := range events {
		if ev.Type != store.EventMemoryUpdate {
			continue
		}
		var p struct {
			Layer string `json:"layer"`
			Cause string `json:"cause"`
		}
		if err := json.Unmarshal(ev.Payload, &p); err != nil {
			continue
		}
		if p.Layer == "note" && p.Cause == "retract" {
			out = append(out, ev)
		}
	}
	return Response{Events: out}, nil
}

// handleSearchEvents searches event payloads across all active workstreams
// in the project for the given query. Returns matches ordered newest-first.
func (s *Server) handleSearchEvents(ctx context.Context, req Request) (Response, error) {
	if req.Text == "" {
		return Response{}, fmt.Errorf("search_events: query is required")
	}
	p, err := s.resolveProject(ctx, req.ProjectRoot)
	if err != nil {
		return Response{}, fmt.Errorf("search_events: %w", err)
	}
	results, err := s.store.SearchEvents(ctx, p.ID, req.Text, 100)
	if err != nil {
		return Response{}, fmt.Errorf("search_events: %w", err)
	}
	return Response{SearchResults: results}, nil
}

// isProtectedPath reports whether p lives under a protected prefix
// (ADR-0003 invariant 1: agents never write memory). Protected: .odo/
// (memory.md, memory-archive.md, pins.md, ledger.md, journal.sqlite,
// worktrees) and wiki/ (epoch notes, topics, index.md — derived artifacts
// owned by the daemon, not the agent).
//
// Self-improving safety extension (2026-08-15, tri-model 3/3): the gate
// mechanism source files are also protected — autoland.go, autonomy.go,
// learner.go, review.go, settle.go, ledger.go, risk.go, contradiction.go,
// design_moa.go. These files implement the auto-land and self-improving
// gates. 2026-08-20 user doctrine supersedes "must never auto-land":
// their diffs route through the full verify+panel pipeline like any
// other, annotated as gate-source for the panel, and land only behind
// panelVerdictAttestsDiff — a journaled UNANIMOUS verdict bound to the
// exact patch bytes (2026-08-22 cut: the settle ladder's majority_accept
// verdict never attests gate sources; the judged still never modifies
// its own judge without its judges, and the human Accept click stays the
// unconditional escape).
//
// Case-insensitive: macOS APFS/HFS+ resolve .ODO/ and Wiki/ identically.
var protectedGateFiles = map[string]bool{
	"internal/ipc/autoland.go":      true,
	"internal/ipc/autonomy.go":      true,
	"internal/ipc/learner.go":       true,
	"internal/ipc/review.go":        true,
	"internal/ipc/settle.go":        true,
	"internal/ipc/ledger.go":        true,
	"internal/ipc/risk.go":          true,
	"internal/ipc/contradiction.go": true,
	"internal/ipc/design_moa.go":    true,
	"internal/ipc/skills_gate.go":   true,
}

func isProtectedPath(p string) bool {
	lp := strings.ToLower(p)
	if strings.HasPrefix(lp, ".odo/") || strings.HasPrefix(lp, "wiki/") {
		return true
	}
	return protectedGateFiles[lp]
}

// rejectMemoryPaths is layer (1) of the accept-time guard inside
// handleDiffAction: daemon-owned memory content (.odo/, wiki/) refused for
// EVERY actor — even a human Accept click lands the agent-produced patch
// into daemon-owned content, so the daemon itself must stay the
// enforcement point. Callers pass both patch sides (git.PatchPaths):
// deleting or renaming memory content is a memory write too, so the
// pre-image side is guarded as well as the post-image side.
func rejectMemoryPaths(paths []string) error {
	for _, f := range paths {
		if lp := strings.ToLower(f); strings.HasPrefix(lp, ".odo/") || strings.HasPrefix(lp, "wiki/") {
			return fmt.Errorf("diff touches protected path %q (invariant 1: agents never write memory)", f)
		}
	}
	return nil
}

// gateSourceHit returns the first patch path that is a protected gate
// source file (protectedGateFiles only — NOT the memory prefix), or "".
func gateSourceHit(paths []string) (string, bool) {
	for _, f := range paths {
		if protectedGateFiles[strings.ToLower(f)] {
			return f, true
		}
	}
	return "", false
}

// panelVerdictAttestsDiff is the gate-source evidence gate (2026-08-20
// user doctrine, layer (2) of the accept-time guard): a non-human accept
// of gate source files requires a journaled moa_review row — actor
// auto_panel, consensus "accept", NOTHING ELSE — whose recorded
// patch_sha16 matches the diff's CURRENT on-disk bytes. The sha binding
// is the point: the panel must have judged exactly the bytes being
// landed, never an earlier generation of the diff, and the judged never
// modifies its own judge without its judges. Every failure (patch read,
// journal read, no matching row) is fail-closed.
//
// 2026-08-22 security cut: the settle ladder's "majority_accept" verdict
// rows NO LONGER attest gate sources. The majority-accept valve (2/3
// accept + 1 dissent) must never rewrite the reviewing pipeline itself,
// so settle never lands a gate diff via majority — gate diffs that
// exhaust the revise ladder reach the round cap and suspend for a
// unanimous verdict or the human Accept click, and the gate check sits
// INSIDE the valve branch (settleDraft) before any majority journal
// row could be written. A majority_accept row may still exist in the
// journal for NON-gate diffs — it just never attests gate sources here.
// Historical majority_accept rows in production journals lose their
// gate-attestation power on upgrade; that is the intended cut.
func (s *Server) panelVerdictAttestsDiff(ctx context.Context, d store.Diff) bool {
	data, err := os.ReadFile(d.PathOnDisk)
	if err != nil {
		return false
	}
	want := sha16(data)
	events, err := s.store.ListEvents(ctx, d.ConversationID, 0)
	if err != nil {
		return false
	}
	for i := len(events) - 1; i >= 0; i-- {
		ev := events[i]
		if ev.Type != store.EventReviewAction {
			continue
		}
		var p struct {
			Action    string `json:"action"`
			Actor     string `json:"actor"`
			DiffID    int64  `json:"diff_id"`
			Consensus string `json:"consensus_verdict"`
			PatchSHA  string `json:"patch_sha16"`
		}
		if err := json.Unmarshal([]byte(ev.Payload), &p); err != nil {
			continue
		}
		if p.Action == "moa_review" && p.Actor == autoActor && p.DiffID == d.ID &&
			p.Consensus == "accept" && p.PatchSHA == want {
			return true
		}
	}
	return false
}

// supersedeChain marks older pending diffs in the same revise chain as
// superseded when one diff in the chain lands. The chain is derived from
// auto_revise_round journal rows: each round links diff_id and
// origin_diff_id, forming a lineage. When a diff lands, every other
// pending diff that shares the same chain root (origin_diff_id) is
// marked superseded — NOT rejected, just no longer actionable.
func (s *Server) supersedeChain(ctx context.Context, landed store.Diff) {
	// Find all events in this conversation.
	events, err := s.store.ListEvents(ctx, landed.ConversationID, 0)
	if err != nil {
		return
	}
	// Build the chain from two sources:
	// 1. auto_revise_round rows: {diff_id (input), origin_diff_id (root)}
	// 2. auto_revise_product rows: {product_diff_id, origin_diff_id}
	//
	// The landed diff may be:
	// - the chain root (d0): appears as origin_diff_id in round/product rows
	// - a revise input (dN): appears as diff_id in a round row
	// - a revise product (dN): appears as product_diff_id in a product row
	chainIDs := map[int64]bool{landed.ID: true}
	originRoot := int64(0)

	// Find originRoot: check round rows and product rows for the landed diff
	for _, e := range events {
		if e.Type != store.EventReviewAction {
			continue
		}
		var p struct {
			Action        string `json:"action"`
			DiffID        int64  `json:"diff_id"`
			OriginDiffID  int64  `json:"origin_diff_id"`
			ProductDiffID int64  `json:"product_diff_id"`
		}
		if json.Unmarshal(e.Payload, &p) != nil {
			continue
		}
		if p.Action == "auto_revise_round" && (p.DiffID == landed.ID || p.OriginDiffID == landed.ID) {
			originRoot = p.OriginDiffID
			chainIDs[p.DiffID] = true
			chainIDs[p.OriginDiffID] = true
		}
		if p.Action == "auto_revise_product" && (p.ProductDiffID == landed.ID || p.OriginDiffID == landed.ID) {
			originRoot = p.OriginDiffID
			chainIDs[p.ProductDiffID] = true
			chainIDs[p.OriginDiffID] = true
		}
	}

	// Collect ALL chain members sharing the origin root
	if originRoot > 0 {
		for _, e := range events {
			if e.Type != store.EventReviewAction {
				continue
			}
			var p struct {
				Action        string `json:"action"`
				DiffID        int64  `json:"diff_id"`
				OriginDiffID  int64  `json:"origin_diff_id"`
				ProductDiffID int64  `json:"product_diff_id"`
			}
			if json.Unmarshal(e.Payload, &p) != nil {
				continue
			}
			if p.Action == "auto_revise_round" && p.OriginDiffID == originRoot {
				chainIDs[p.DiffID] = true
				chainIDs[p.OriginDiffID] = true
			}
			if p.Action == "auto_revise_product" && p.OriginDiffID == originRoot {
				chainIDs[p.ProductDiffID] = true
				chainIDs[p.OriginDiffID] = true
			}
		}
	}
	// Get all diffs in this conversation and supersede pending ones in
	// the chain except the landed one.
	diffs, err := s.store.ListDiffs(ctx, landed.ConversationID)
	if err != nil {
		return
	}
	for _, d := range diffs {
		// N2 fix: only supersede OLDER pending diffs (ID < landed.ID).
		// A newer product diff should not be killed by an older one landing.
		if d.ID >= landed.ID || d.Status != store.DiffPending {
			continue
		}
		if !chainIDs[d.ID] {
			continue
		}
		if err := s.store.UpdateDiffStatus(ctx, d.ID, store.DiffSuperseded); err != nil {
			log.Printf("supersedeChain: mark diff %d superseded: %v", d.ID, err)
			continue
		}
		s.store.AppendEvent(ctx, d.ConversationID, store.EventReviewAction, mustJSON(map[string]interface{}{
			"action":        "superseded",
			"actor":         autoActor,
			"diff_id":       d.ID,
			"superseded_by": landed.ID,
			"reason":        "superseded_by_revise_chain_landing",
		}))
	}
}

// handleListWiki lists the distilled wiki notes for the conversation's
// workstream, newest epoch first. Read-only: no journal writes.
func (s *Server) handleListWiki(ctx context.Context, req Request) (Response, error) {
	c, err := s.checkConversation(ctx, req.ConversationID)
	if err != nil {
		return Response{}, err
	}
	w, err := s.store.GetWorkstream(ctx, c.WorkstreamID)
	if err != nil {
		return Response{}, err
	}
	matches, err := filepath.Glob(filepath.Join(s.projectRoot, "wiki", w.Name+"-epoch-*.md"))
	if err != nil {
		return Response{}, fmt.Errorf("list_wiki: %w", err)
	}
	var notes []WikiNoteInfo
	for _, m := range matches {
		epoch, ok := wikiNoteEpoch(m)
		if !ok {
			continue // skip unparseable names defensively
		}
		fi, err := os.Stat(m)
		if err != nil {
			continue // vanished between glob and stat
		}
		notes = append(notes, WikiNoteInfo{
			Path:       m,
			Name:       strings.TrimSuffix(filepath.Base(m), ".md"),
			Epoch:      epoch,
			ModifiedAt: fi.ModTime().UTC().Format(time.RFC3339),
		})
	}
	sort.Slice(notes, func(i, j int) bool { return notes[i].Epoch > notes[j].Epoch })
	return Response{WikiNotes: notes}, nil
}

// handleReadWiki returns the content of one memory file: a note under
// <projectRoot>/wiki/ or the global ~/.odo/user.md — anything else is
// rejected (path-traversal guard). A missing wiki note is an error; a
// missing user.md is OK with empty content so the frontend can render a
// create-hint. Read-only: no journal writes.
func (s *Server) handleReadWiki(_ context.Context, req Request) (Response, error) {
	// Class 2: exactly ~/.odo/user.md (the one global file), accepted as the
	// expanded absolute path or as the literal "~/.odo/user.md".
	if home, err := os.UserHomeDir(); err == nil {
		userMemPath := filepath.Join(home, ".odo", "user.md")
		expanded := req.Path
		if strings.HasPrefix(expanded, "~/") {
			expanded = filepath.Join(home, expanded[len("~/"):])
		}
		if filepath.Clean(expanded) == userMemPath {
			b, err := os.ReadFile(userMemPath)
			switch {
			case err == nil:
				return Response{WikiContent: string(b)}, nil
			case os.IsNotExist(err):
				return Response{WikiContent: ""}, nil // frontend renders a create-hint
			default:
				return Response{}, fmt.Errorf("read_wiki: %w", err)
			}
		}
	}

	// Class 1: a file under <projectRoot>/wiki/, no escaping the project —
	// through symlinks included (tri-review P0, 2026-08-24): lexical
	// Clean+Rel passes for a checked-in wiki/ symlink (or symlinked
	// parent dir) pointing at ~/.ssh and friends; readWithinDir resolves
	// and refuses the escape. Class 2 above stays on plain os.ReadFile —
	// ~/.odo/user.md is outside the repo-committable threat model.
	clean := filepath.Clean(req.Path)
	rel, relErr := filepath.Rel(s.projectRoot, clean)
	if relErr == nil && !strings.HasPrefix(rel, "..") &&
		(rel == "wiki" || strings.HasPrefix(rel, "wiki"+string(filepath.Separator))) {
		b, err := readWithinDir(s.projectRoot, filepath.Join(s.projectRoot, "wiki"), clean)
		if err != nil {
			return Response{}, fmt.Errorf("read_wiki: %s: %w", clean, err)
		}
		return Response{WikiContent: string(b)}, nil
	}
	return Response{}, fmt.Errorf("read_wiki: only files under wiki/ or ~/.odo/user.md are readable, got %q", req.Path)
}

// readFileMaxBytes caps one file's preview payload (tri-model right sidebar
// gap: inline file preview). 512 KiB keeps a worst-case response under ~1 MiB
// of JSON, matching the READ_TIMEOUT headroom for poll_events.
const readFileMaxBytes = 512 * 1024

// handleReadFile returns a text file's content for the GUI's inline preview.
// Containment mirrors the GUI's open_path rule: relative paths join against
// the daemon's bound root, ~ expands to $HOME, the candidate is canonicalized
// (symlinks resolved) and must land inside the project root or ~/.odo.
// Binary files (NUL in the first 8 KiB) and any escape are rejected.
// Read-only: no journal writes.
func (s *Server) handleReadFile(_ context.Context, req Request) (Response, error) {
	p := req.Path
	if p == "" {
		return Response{}, fmt.Errorf("read_file: path is required")
	}
	// Resolve: ~ and relative paths → absolute under the bound root.
	if strings.HasPrefix(p, "~/") || p == "~" {
		home, err := os.UserHomeDir()
		if err != nil {
			return Response{}, fmt.Errorf("read_file: home: %w", err)
		}
		p = filepath.Join(home, p[1:])
	}
	if !filepath.IsAbs(p) {
		p = filepath.Join(s.projectRoot, p)
	}
	canonical, err := filepath.EvalSymlinks(p)
	if err != nil {
		return Response{}, fmt.Errorf("read_file: %s: %w", p, err)
	}
	// Containment: project root (resolved) or ~/.odo — component-wise, so
	// a symlink pointing out of the project is rejected like for open_path.
	resolvedRoot := s.resolvedRoot
	if resolvedRoot == "" {
		resolvedRoot = s.projectRoot
	}
	allowed := canonical == resolvedRoot ||
		strings.HasPrefix(canonical, resolvedRoot+string(filepath.Separator))
	if !allowed {
		if home, herr := os.UserHomeDir(); herr == nil {
			odoDir := filepath.Join(home, ".odo")
			if real, rerr := filepath.EvalSymlinks(odoDir); rerr == nil {
				odoDir = real
			}
			allowed = canonical == odoDir ||
				strings.HasPrefix(canonical, odoDir+string(filepath.Separator))
		}
	}
	if !allowed {
		return Response{}, fmt.Errorf("read_file: path outside project root: %q", req.Path)
	}
	// Binary guard + bounded read on ONE handle (tri-review P2,
	// 2026-08-24): the old sniff → close → Stat → re-open/ReadFile flow
	// was a TOCTOU — a file grown between the Stat size gate and
	// os.ReadFile bypassed the cap entirely (the small-file branch read
	// unbounded), and a swapped inode mixed sniff and payload. Everything
	// below runs on the fd opened once here.
	fh, err := os.Open(canonical)
	if err != nil {
		return Response{}, fmt.Errorf("read_file: %w", err)
	}
	defer fh.Close()
	head := make([]byte, 8192)
	n, rerr := fh.Read(head)
	if rerr != nil && rerr != io.EOF {
		return Response{}, fmt.Errorf("read_file: %w", rerr)
	}
	if strings.IndexByte(string(head[:n]), 0) >= 0 {
		return Response{}, fmt.Errorf("read_file: binary file (not previewable): %q", req.Path)
	}
	// The tail continues on the same fd, capped at cap+1 bytes — the read
	// itself is bounded (a multi-GB log/dataset must never come fully
	// into memory, tri-review P2: the stated cap must bound the read too),
	// no Stat size gate remains to race. >cap truncates exactly at the cap.
	rest, err := io.ReadAll(io.LimitReader(fh, readFileMaxBytes+1))
	if err != nil {
		return Response{}, fmt.Errorf("read_file: %w", err)
	}
	content := append(head[:n], rest...)
	truncated := len(content) > readFileMaxBytes
	if truncated {
		content = content[:readFileMaxBytes]
	}
	return Response{
		FileContent:   string(content),
		FileResolved:  canonical,
		FileTruncated: truncated,
	}, nil
}

// handleReadMemory returns the contents of the three canonical memory files
// (project memory.md, append-only archive, global user.md) as
// memory_content/archive_content/user_content. The daemon constructs the
// paths itself; req.ProjectRoot must be the bound root (same guard as
// resolveProject). Missing files come back "" — the archive is returned
// uncapped (append-only, never injected). Read-only: no journal writes.
func (s *Server) handleReadMemory(ctx context.Context, req Request) (Response, error) {
	if _, err := s.resolveProject(ctx, req.ProjectRoot); err != nil {
		return Response{}, fmt.Errorf("read_memory: %w", err)
	}
	resp := Response{
		// Contained (2026-08-25 review P0): same planted-symlink model as
		// the prompt-side readers — the panel must never render external
		// bytes as the project's rules.
		MemoryContent:  readFileWithin(s.projectRoot, filepath.Join(s.projectRoot, ".odo"), filepath.Join(s.projectRoot, ".odo", memoryFileName)),
		ArchiveContent: readArchive(s.projectRoot),
	}
	if home, err := os.UserHomeDir(); err == nil {
		resp.UserContent = readFileFull(filepath.Join(home, ".odo", "user.md"))
	}
	return resp, nil
}

// handleMemoryProposals returns the latest epoch's propose batch: pending
// (actionable, pre-decision) or consumed with its outcome (the panel-gated
// path consumes batches itself; the MemoryPanel renders what was decided —
// per-proposal reviews ride the batch, accepted/rejected refs and the
// actor ride the response). No batch for the latest epoch → empty
// response (epoch 0).
func (s *Server) handleMemoryProposals(ctx context.Context, req Request) (Response, error) {
	c, err := s.checkConversation(ctx, req.ConversationID)
	if err != nil {
		return Response{}, err
	}
	events, err := s.store.ListEvents(ctx, c.ID, 0)
	if err != nil {
		return Response{}, err
	}
	batch := findPendingBatch(events)
	if !batch.exists {
		return Response{}, nil
	}
	return Response{
		Epoch:      batch.epoch,
		Seq:        batch.seq,
		Proposals:  batch.proposals,
		Reaffirm:   batch.reaffirm,
		Consumed:   batch.consumed,
		ApplyActor: batch.applyActor,
		Accepted:   batch.accepted,
		Rejected:   batch.rejected,
	}, nil
}

// handleApplyMemory consumes the pending batch all-or-nothing (spec §5):
// the human decision path. It resolves and validates the caller's accepted
// refs, then hands the per-index decision to applyResolvedBatch. The
// default (panel-gated) path reaches the same core from distillCore.
func (s *Server) handleApplyMemory(ctx context.Context, req Request) (Response, error) {
	c, err := s.checkConversation(ctx, req.ConversationID)
	if err != nil {
		return Response{}, err
	}
	events, err := s.store.ListEvents(ctx, c.ID, 0)
	if err != nil {
		return Response{}, err
	}
	batch := findPendingBatch(events)
	if !batch.exists {
		return Response{}, errors.New("apply_memory: no pending batch")
	}
	if batch.consumed {
		// Replay before refusing (2026-08-26 memory-replay doctrine): the
		// consumption marker may have outlived a crash before its file
		// writes — the refusal is only truthful once files and journal
		// agree, so restore the stranded layers first. The engine re-reads
		// under memMu: apply/pin receipts append only under that lock, so
		// the unlocked snapshot at handler top may already lag a landed
		// racer.
		s.memMu.Lock()
		var repaired []int
		if fresh, rerr := s.store.ListEvents(ctx, c.ID, 0); rerr == nil {
			repaired = s.replayLaneMemReceipts(ctx, c.ID, fresh, replayApply)
		}
		s.memMu.Unlock()
		// The replay restoring/entry-merging THIS batch's recorded layers
		// means the caller's retry just performed the apply it asked for —
		// report success, mirroring the in-core check (the marker-first
		// ordering otherwise regressed the crashed-apply retry from the
		// idempotent success into a refusal, TestUserMemoryIdempotency).
		// A no-op pass (fully landed earlier, or foreign state that
		// conflicted into the review ledger instead) still refuses, per
		// the consumed contract.
		for _, epoch := range repaired {
			if epoch == batch.epoch && req.Epoch == batch.epoch {
				return Response{Applied: true}, nil
			}
		}

		return Response{}, fmt.Errorf("apply_memory: epoch %d already applied", req.Epoch)
	}
	if req.Epoch != batch.epoch {
		return Response{}, fmt.Errorf("apply_memory: no pending batch for epoch %d (pending epoch is %d)", req.Epoch, batch.epoch)
	}

	// Resolve + validate every accepted ref against the batch proposals.
	accepted := make([]bool, len(batch.proposals))
	for _, a := range req.Accepted {
		if a.Index < 0 || a.Index >= len(batch.proposals) {
			return Response{}, fmt.Errorf("apply_memory: proposal index %d out of range (%d proposals)", a.Index, len(batch.proposals))
		}
		p := batch.proposals[a.Index]
		if a.Target != p.Target {
			return Response{}, fmt.Errorf("apply_memory: proposal %d is target %q, not %q", a.Index, p.Target, a.Target)
		}
		if accepted[a.Index] {
			return Response{}, fmt.Errorf("apply_memory: proposal %d accepted twice", a.Index)
		}
		accepted[a.Index] = true
	}
	return s.applyResolvedBatch(ctx, c, batch, accepted, "")
}

// applyResolvedBatch consumes a pending batch all-or-nothing (spec §5):
// every target is pre-computed in memory before anything hits disk or the
// journal; a user.md overflow refusal writes nothing and leaves the batch
// pending for retry. Per changed layer a memory_update event is journaled
// (rotation and successful retraction are their own causes, spec §6), then
// the review_action apply marker; a second apply errors "already
// applied".
//
// actor names the decision-maker on the apply marker: "" for the human
// path (additive — pre-panel rows carry no actor), autoActor for the
// panel-gated auto-apply and legacy sweep.
func (s *Server) applyResolvedBatch(ctx context.Context, c store.Conversation, batch pendingBatch, accepted []bool, actor string) (Response, error) {
	s.memMu.Lock()
	defer s.memMu.Unlock()
	// Single-writer re-check (2026-08-25 audit P1): every caller's
	// pending/consumed probe ran UNLOCKED, so two consumers of the same
	// batch (manual apply vs the auto sweep, or two distills) both passed
	// and applied. Under memMu the journal is re-folded; a batch already
	// consumed by the racing apply fails closed here instead of
	// re-appending archive lines, reaffirm bumps, and the apply marker.
	if events, err := s.store.ListEvents(ctx, c.ID, 0); err == nil {
		// Recovery FIRST (2026-08-25 review follow-up P1, engine since the
		// 2026-08-26 memory-replay doctrine): the apply marker fell to
		// marker-first ordering below — a crash after it leaves the batch
		// consumed with FILES lagging. Replaying those layers from the
		// marker's recovery block (restore, or entry-merge onto a foreign
		// projection) before planning has two jobs here: an older stranded
		// marker's rules return before the fresh plan reads the files, and
		// when the stranded marker IS this batch the work just completed —
		// report success rather than a double-consumption refusal. A LIVE
		// racing winner cannot be mistaken for a stranded one: memMu is
		// held across its whole write section, so any marker visible here
		// has its writer finished (or dead).
		for _, repaired := range s.replayLaneMemReceipts(ctx, c.ID, events, replayApply) {
			if repaired == batch.epoch {
				return Response{Applied: true}, nil
			}
		}
		if cur := findPendingBatch(events); !cur.exists || cur.consumed || cur.epoch != batch.epoch {
			return Response{}, fmt.Errorf("apply_memory: epoch %d already applied", batch.epoch)
		}
	}
	var acceptedRefs []MemoryAccept
	var memAccepted []acceptedRule
	var userAccepted []acceptedUserRule
	var skillWrites []skillWrite // M9: pre-computed skill file writes
	for i, p := range batch.proposals {
		if !accepted[i] {
			continue
		}
		acceptedRefs = append(acceptedRefs, MemoryAccept{Target: p.Target, Index: i})
		switch p.Target {
		case "memory.md":
			memAccepted = append(memAccepted, acceptedRule{
				rule: p.Rule, evidence: p.Evidence, contradicts: p.Contradicts,
			})
		case "user.md":
			// Projects on the proposal are the daemon-verified recurrence set
			// (the LLM's self-tagged list was replaced at vet time).
			userAccepted = append(userAccepted, acceptedUserRule{
				rule: p.Rule, projects: p.Projects,
			})
		case "skills":
			// M9: skill proposals write to .odo/skills/<name>.md. Use the
			// vetted p.Name directly (NOT re-parsed frontmatter — TOCTOU risk).
			if p.Name == "" {
				return Response{}, fmt.Errorf("apply_memory: skill proposal %d has empty name", i)
			}
			fname := filepath.Base(p.Name)
			if !strings.HasSuffix(fname, ".md") {
				fname += ".md"
			}
			if fname == "" || strings.Contains(fname, "..") {
				return Response{}, fmt.Errorf("apply_memory: invalid skill name: %s", p.Name)
			}
			target := filepath.Join(s.projectRoot, ".odo", "skills", fname)
			skillWrites = append(skillWrites, skillWrite{path: target, content: p.Rule})
		default:
			return Response{}, fmt.Errorf("apply_memory: unknown proposal target %q", p.Target)
		}
	}
	// Rejected refs are daemon-computed (every proposal not accepted).
	var rejected []MemoryAccept
	for i, ok := range accepted {
		if !ok {
			rejected = append(rejected, MemoryAccept{Target: batch.proposals[i].Target, Index: i})
		}
	}

	// Pre-compute EVERY target before any write (all-or-nothing).
	memPath := filepath.Join(s.projectRoot, ".odo", memoryFileName)
	oldMem := readFileFull(memPath) // FULL uncapped: the write basis (inv 3)
	memPlan := memoryApplyPlan{content: oldMem}
	memChanged := false
	if len(memAccepted) > 0 || len(batch.reaffirm) > 0 {
		memPlan = planMemoryApply(oldMem, memAccepted, batch.reaffirm, batch.epoch)
		memChanged = memPlan.content != oldMem || memPlan.archiveAppend != ""
	}

	var userPath, oldUser, newUser string
	userChanged := false
	if len(userAccepted) > 0 {
		home, err := os.UserHomeDir()
		if err != nil {
			return Response{}, fmt.Errorf("apply_memory: resolve home: %w", err)
		}
		userPath = filepath.Join(home, ".odo", "user.md")
		oldUser = readFileFull(userPath)
		newUser, err = planUserApply(oldUser, userAccepted)
		if err != nil {
			// Refused: nothing written, nothing journaled, the batch
			// stays pending (a retry recomputes from the same proposes).
			return Response{}, fmt.Errorf("apply_memory: %w", err)
		}
		userChanged = newUser != oldUser
	}

	// Batch-consumed marker (daemon-computed counts, ADR inv 4) — journaled
	// BEFORE the file writes (2026-08-25 review follow-up P1): the old
	// file-then-journal order's crash window left the model reading rules
	// the journal still showed pending, and a post-restart re-apply
	// doubled reaffirm bumps and archive lines onto the changed file.
	// Marker-first makes consumption the journaled intent: the batch can
	// never be consumed twice, and a crash after this point is repaired by
	// the replay engine (boot replayer / next apply) from the recovery
	// block — each changed layer's before/after hash plus its post-state
	// body, so a stranded file is rewritten exactly, an already-landed one
	// is left alone, and a foreign-advanced one entry-merges add-style
	// intent or conflicts into the review ledger (2026-08-26 doctrine).
	memBeforeSHA := sha16([]byte(oldMem))
	memAfterSHA := sha16([]byte(memPlan.content))
	var oldArchive string
	if memChanged && memPlan.archiveAppend != "" {
		oldArchive = readArchive(s.projectRoot)
	}
	recovery := applyRecovery{}
	if memChanged {
		recovery.Memory = &applyRecoveryLayer{
			BeforeSHA: memBeforeSHA,
			AfterSHA:  memAfterSHA,
			Body:      memPlan.content,
			Entries:   memPlan.addedEntries,
		}
		if memPlan.archiveAppend != "" {
			recovery.Archive = &applyRecoveryLayer{
				BeforeSHA: sha16([]byte(oldArchive)),
				AfterSHA:  sha16([]byte(oldArchive + memPlan.archiveAppend)),
				Body:      memPlan.archiveAppend, // append chunk only
			}
		}
	}
	if userChanged {
		recovery.User = &applyRecoveryLayer{
			BeforeSHA: sha16([]byte(oldUser)), AfterSHA: sha16([]byte(newUser)), Body: newUser,
		}
	}
	for _, sw := range skillWrites {
		recovery.Skills = append(recovery.Skills, applyRecoverySkill{
			Name:      filepath.Base(sw.path),
			BeforeSHA: sha16([]byte(readFileFull(sw.path))), // pre-write bytes ("" when the file is absent)
			AfterSHA:  sha16([]byte(sw.content)),
			Body:      sw.content,
		})
	}
	applyPayload := map[string]interface{}{
		"action":   "memory_apply",
		"epoch":    batch.epoch,
		"accepted": acceptedRefs,
		"rejected": rejected,
		"metrics":  map[string]int{"accepted": len(acceptedRefs), "rejected": len(rejected)},
		"recovery": recovery,
	}
	if actor != "" {
		applyPayload["actor"] = actor
	}
	applyEv, err := s.store.AppendEvent(ctx, c.ID, store.EventReviewAction, mustJSON(applyPayload))
	if err != nil {
		return Response{}, err
	}
	// Test-only crash drill (2026-08-25 review follow-up): return as if the
	// daemon died here — marker journaled, no file written — so the heal
	// path is exercised end to end. Production never sets the seam.
	if s.failApplyAfterMarker != nil {
		return Response{}, s.failApplyAfterMarker
	}

	// Writes: archive first, then user.md, memory.md LAST. A mid-sequence
	// archive-write failure leaves the previous memory.md intact; the batch
	// is consumed either way (marker above), and the heal restores whatever
	// the failure skipped on the next fold.
	if memChanged && memPlan.archiveAppend != "" {
		arcPath := filepath.Join(s.projectRoot, ".odo", archiveFileName)
		if err := writeFileWithin(s.projectRoot, arcPath, oldArchive+memPlan.archiveAppend, 0o644); err != nil {
			return Response{}, fmt.Errorf("apply_memory: append archive: %w", err)
		}
	}
	if userChanged {
		if err := writeFileAtomic(userPath, newUser, 0o600); err != nil {
			return Response{}, fmt.Errorf("apply_memory: write user.md: %w", err)
		}
	}
	// M9: write skill files before memory.md (memory.md is still last for
	// convergence). Skill writes are idempotent by overwrite (atomic rename,
	// same content = no-op).
	for _, sw := range skillWrites {
		if err := writeFileWithin(s.projectRoot, sw.path, sw.content, 0o644); err != nil {
			return Response{}, fmt.Errorf("apply_memory: write skill %s: %w", sw.path, err)
		}
	}
	if memChanged {
		if err := writeFileWithin(s.projectRoot, memPath, memPlan.content, 0o644); err != nil {
			return Response{}, fmt.Errorf("apply_memory: write memory.md: %w", err)
		}
	}

	// Journal per changed layer. Rotation and successful retraction are
	// DISTINCT memory_update causes (spec §6: the UI switch is exhaustive),
	// not clauses folded into the apply detail.
	if memChanged {
		detail := fmt.Sprintf("accepted %d rule(s)", len(memAccepted))
		if memPlan.reaffirmed > 0 {
			detail += fmt.Sprintf("; reaffirmed %d", memPlan.reaffirmed)
		}
		beforeSHA, afterSHA := memBeforeSHA, memAfterSHA // hoisted: the marker's recovery block attests the same pair
		if _, err := s.store.AppendEvent(ctx, c.ID, store.EventMemoryUpdate, mustJSON(map[string]interface{}{
			"layer":      "memory",
			"cause":      "apply",
			"before_sha": beforeSHA,
			"after_sha":  afterSHA,
			"detail":     detail,
		})); err != nil {
			return Response{}, err
		}
		if len(memPlan.rotated) > 0 {
			if _, err := s.store.AppendEvent(ctx, c.ID, store.EventMemoryUpdate, mustJSON(map[string]interface{}{
				"layer":      "memory",
				"cause":      "rotate",
				"before_sha": beforeSHA,
				"after_sha":  afterSHA,
				"detail": fmt.Sprintf("rotated %d to memory-archive.md (overflow): %s",
					len(memPlan.rotated), strings.Join(memPlan.rotated, " | ")),
			})); err != nil {
				return Response{}, err
			}
		}
		if len(memPlan.retracted) > 0 {
			if _, err := s.store.AppendEvent(ctx, c.ID, store.EventMemoryUpdate, mustJSON(map[string]interface{}{
				"layer":      "memory",
				"cause":      "retract",
				"before_sha": beforeSHA,
				"after_sha":  afterSHA,
				"detail": fmt.Sprintf("retracted %d (conflict): %s",
					len(memPlan.retracted), strings.Join(memPlan.retracted, " | ")),
			})); err != nil {
				return Response{}, err
			}
		}
	}
	for _, unmatched := range memPlan.unmatchedContradicts {
		if _, err := s.store.AppendEvent(ctx, c.ID, store.EventMemoryUpdate, mustJSON(map[string]interface{}{
			"layer":  "memory",
			"cause":  "retract",
			"detail": fmt.Sprintf("no match for contradicts: %q", unmatched),
		})); err != nil {
			return Response{}, err
		}
	}
	if userChanged {
		if _, err := s.store.AppendEvent(ctx, c.ID, store.EventMemoryUpdate, mustJSON(map[string]interface{}{
			"layer":      "user",
			"cause":      "apply",
			"before_sha": sha16([]byte(oldUser)),
			"after_sha":  sha16([]byte(newUser)),
			"detail":     fmt.Sprintf("accepted %d rule(s)", len(userAccepted)),
		})); err != nil {
			return Response{}, err
		}
	}
	// M9: journal one memory_update per skill write.
	for _, sw := range skillWrites {
		if _, err := s.store.AppendEvent(ctx, c.ID, store.EventMemoryUpdate, mustJSON(map[string]interface{}{
			"layer":  "skills",
			"cause":  "applied",
			"detail": fmt.Sprintf("wrote %s", filepath.Base(sw.path)),
		})); err != nil {
			return Response{}, err
		}
	}

	// M6: ledger append (best-effort). Separate "(apply)" section: the file
	// is append-only and a later epoch's distill section may already follow
	// the epoch this apply belongs to. Section header includes the
	// workstream name for unique addressability (GLM defect 2).
	applyWsName := "unknown"
	if ws, err := s.store.GetWorkstream(ctx, c.WorkstreamID); err == nil {
		applyWsName = ws.Name
	}
	if err := appendLedger(s.projectRoot, fmt.Sprintf("%s/epoch %d (apply)", applyWsName, batch.epoch), applyLedgerMetrics(applyEv)); err != nil {
		_, _ = s.store.AppendEvent(ctx, c.ID, store.EventMemoryUpdate, mustJSON(map[string]interface{}{
			"layer":  "ledger",
			"cause":  "write_failed",
			"detail": err.Error(),
		}))
	}
	return Response{Applied: true}, nil
}

// applyRecoveryEntry is the memory layer's verbatim per-rule record of
// one appended add-style line: the rule text plus the EXACT line the live
// apply wrote (evidence and the apply epoch's reaffirmed count as
// planMemoryApply rendered them). The replay engine's entry-merge reuses
// Line verbatim when the receipt carries it — a cross-lane merge never
// re-stamps metadata the apply already authored (2026-08-26 memory-replay
// doctrine, round-3 FIX C).
type applyRecoveryEntry struct {
	Rule string `json:"rule"`
	Line string `json:"line"`
}

// applyRecoveryLayer is one layer's recorded post-state on the
// memory_apply marker (2026-08-25 review follow-up P1): the before/after
// hashes make the crash-window heal exact, and Body is what gets written.
// For the archive Body is the APPEND CHUNK only (the file is unbounded;
// the heal replays the append onto a still-before archive). Entries rides
// the memory layer only (omit on archive/user): the entry-merge replay's
// verbatim line source — legacy receipts without it fall back to the
// synthesized reaffirmed: 1 line.
type applyRecoveryLayer struct {
	BeforeSHA string               `json:"before_sha"`
	AfterSHA  string               `json:"after_sha"`
	Body      string               `json:"body"`
	Entries   []applyRecoveryEntry `json:"entries,omitempty"`
}

// applyRecoverySkill is one skill file's applyRecoveryLayer plus the
// basename it writes to.
type applyRecoverySkill struct {
	Name      string `json:"name"`
	BeforeSHA string `json:"before_sha"`
	AfterSHA  string `json:"after_sha"`
	Body      string `json:"body"`
}

// applyRecovery is the memory_apply marker's recovery block: every layer
// whose write the marker precedes, so a crash between marker and writes
// is repairable from the journal alone.
type applyRecovery struct {
	Memory  *applyRecoveryLayer  `json:"memory,omitempty"`
	Archive *applyRecoveryLayer  `json:"archive,omitempty"`
	User    *applyRecoveryLayer  `json:"user,omitempty"`
	Skills  []applyRecoverySkill `json:"skills,omitempty"`
}

// Empty reports whether nothing was recorded (a marker from a no-write
// apply — nothing accepted — or a legacy pre-recovery row).
func (r applyRecovery) Empty() bool {
	return r.Memory == nil && r.Archive == nil && r.User == nil && len(r.Skills) == 0
}

// The 2026-08-25 per-lane heals (lane-local scans with a silent
// never-clobber foreign branch) were RETIRED by the 2026-08-26
// memory-replay doctrine: the shared engine in memory_replay.go owns the
// predicate at every former call site — the boot replayer (project-wide,
// authoritative), the apply paths' retry convergence, and the pin
// handler's RMW-basis pass.

// R-W2 (router-vs-omp-eval-2026-08-14): prefs.md `distill_via:` picks the
// distill route; R-W3 adds `learner_via:` and `curator_via:` for the other
// two memory-pipeline one-shots. Absent or "omp" keeps the historical OMP
// one-shot — the dark-launch default until moa-route telemetry says flip;
// "moa" routes a single moa.Query (the D5 review precedent). The values
// name routes, not adapters: the moa route bypasses every adapter
// internal.
const (
	viaOMP = "omp"
	viaMoa = "moa"
)

// resolveVia re-reads prefs per call (the resolveMaxConcurrent pattern): a
// prefs edit takes effect on the next pass. Unrecognized values log and
// fall back to OMP — a typo must never silently reroute the memory
// pipeline.
func resolveVia(task, prefKey string) string {
	switch v := adapter.LoadPrefsRaw(prefKey); v {
	case "", viaOMP:
		return viaOMP
	case viaMoa:
		return viaMoa
	default:
		log.Printf("%s: unknown %s %q; falling back to %q", task, prefKey, v, viaOMP)
		return viaOMP
	}
}

// distillReceipt is the moa route's ledger for the fold marker: which
// model served the note, the sha of the exact prompt bytes on the wire,
// and the output-budget outcome moa.Result carries. Nil on the OMP route
// (whose receipts stay the exemption-ledger's, server.go assertPromptReceipts).
type distillReceipt struct {
	via          string
	model        string
	promptSHA    string
	budget       int
	outputTokens int
	escalations  []moa.Escalation
}

// runDistillAgent runs the assembled distill prompt through the
// prefs-selected route and returns the wiki note body. The CALLER
// assembles the prompt (distillPrompt over the window, plus the M12 todo
// seed) because seeding needs the full event history while the window
// does not.
func (s *Server) runDistillAgent(ctx context.Context, prompt string) (string, *distillReceipt, error) {
	if resolveVia("distill", "distill_via") == viaMoa {
		return s.runDistillViaMoa(ctx, prompt)
	}
	ad := s.distillAdapter
	if ad == nil {
		ad = s.adapterFor("") // fallback to default if distill adapter not configured
	}
	note, err := runOneShot(ctx, ad, prompt, distillTimeout)
	return note, nil, err
}

// runDistillViaMoa sends the distill prompt to the prefs orchestrator
// model as one direct moa.Query — no OMP process, no tmpdir, no session.
// The system field stays empty: distillPrompt is self-contained
// instructions-plus-events, and an empty system keeps the wire body
// exactly the journal-attestable bytes (model-visible ⇔ logged).
//
// Deadline policy (router-vs-omp-eval §5 risk 3, explicit): the outer
// bound is ONE worst-case moa attempt chain at the model's hard output
// cap — moa.TimeoutForModel, 1446s at the current 64K cap — replacing the
// 10-min distillTimeout ONLY on this route. Escalation re-issues race the
// same outer deadline; a truncated-then-hung chain dies here with a typed
// timeout, nothing written, fold not committed.
//
// A truncated answer fails CLOSED: a partial note must never commit a
// fold over a window it summarized incompletely (the review path's
// truncation rule, distill side). The learner's parse-failure degrade is
// unaffected — this check is upstream of every artifact write.
func (s *Server) runDistillViaMoa(ctx context.Context, prompt string) (string, *distillReceipt, error) {
	model := adapter.ReadSettings().OrchestratorModel
	client := s.sharedMoa() // P1 #10: distill traffic contends on the same cap
	ctx, cancel := context.WithTimeout(ctx, moa.TimeoutForModel(model))
	defer cancel()
	res, err := client.Query(ctx, model, "", prompt)
	if err != nil {
		return "", nil, err
	}
	if res.Truncated {
		return "", nil, fmt.Errorf("%s truncated at the %d-token hard cap after %d escalation(s); note not written, fold not committed", model, res.Budget, len(res.Escalations))
	}
	return res.Text, &distillReceipt{
		via:          viaMoa,
		model:        model,
		promptSHA:    sha16([]byte(prompt)),
		budget:       res.Budget,
		outputTokens: res.OutputTokens,
		escalations:  res.Escalations,
	}, nil
}

// moaReceipt is the R-W3 learner/curate route ledger: which model served
// the pass, the client-stamped wire-request receipt (sha16 of the exact
// marshaled body + its length — R-W1.5), and the output-budget outcome
// moa.Result reports. Nil on the OMP route (whose receipts stay the
// exemption-ledger's, server.go assertPromptReceipts).
type moaReceipt struct {
	via          string
	model        string
	requestSHA16 string
	requestBytes int
	budget       int
	outputTokens int
	escalations  []moa.Escalation
}

// journal adds the receipt's additive keys under prefix — bare on the
// curate marker (no naming collision), "learner_" on the fold marker
// (where the distill receipt already owns the bare names). Kept to one
// method so the three markers never drift key spellings.
func (r *moaReceipt) journal(m map[string]interface{}, prefix string) {
	m[prefix+"via"] = r.via
	m[prefix+"model"] = r.model
	m[prefix+"request_sha16"] = r.requestSHA16
	m[prefix+"request_bytes"] = r.requestBytes
	m[prefix+"output_tokens"] = r.outputTokens
	m[prefix+"budget"] = r.budget
	if len(r.escalations) > 0 {
		m[prefix+"escalations"] = r.escalations
	}
}

// runMoaOneShot sends one memory-pipeline pass's prompt to the prefs
// orchestrator model as a single direct moa.Query — no OMP process, no
// tmpdir, no session. The system field stays empty: learnerPrompt /
// curatorPrompt are self-contained instructions-plus-data, and an empty
// system keeps the wire body exactly the journal-attestable bytes
// (model-visible ⇔ logged). runDistillViaMoa is the sibling for the fold
// itself; this is the R-W3 learner/curator share.
//
// Deadline policy: the R-W2 one — the outer bound is ONE worst-case moa
// attempt chain at the model's hard output cap (moa.TimeoutForModel),
// replacing learnerTimeout/curatorTimeout ONLY on this route. A truncated
// answer fails CLOSED: a partial proposal set or topic rewrite must never
// reach parsers/vet or the page writer. task names the pass in messages
// ("learner" | "curator").
// client is the caller's (P1 #10: the Server's shared) MoA client — one
// semaphore pool governs every moa route, review lane and one-shots alike.
func runMoaOneShot(ctx context.Context, client *moa.Client, task, prompt string) (string, *moaReceipt, error) {
	model := adapter.ReadSettings().OrchestratorModel
	ctx, cancel := context.WithTimeout(ctx, moa.TimeoutForModel(model))
	defer cancel()
	res, err := client.Query(ctx, model, "", prompt)
	if err != nil {
		return "", nil, err
	}
	if res.Truncated {
		return "", nil, fmt.Errorf("%s truncated at the %d-token hard cap after %d escalation(s); output discarded, nothing written", task, res.Budget, len(res.Escalations))
	}
	return res.Text, &moaReceipt{
		via:          viaMoa,
		model:        model,
		requestSHA16: res.RequestSHA16,
		requestBytes: res.RequestBytes,
		budget:       res.Budget,
		outputTokens: res.OutputTokens,
		escalations:  res.Escalations,
	}, nil
}

// runOneShot runs prompt through ad in a throwaway directory, blocking until
// the run's terminal event or timeout, and returns the concatenated
// agent_text output. Distill (OMP route), learner, and curator use it
// (review migrated to moa.Query in D5; distill/learner/curator migrate on
// their prefs `*_via: moa` routes, R-W2/R-W3).
func runOneShot(ctx context.Context, ad adapter.Adapter, prompt string, timeout time.Duration) (string, error) {
	tmpDir, err := os.MkdirTemp("", "odo-oneshot-")
	if err != nil {
		return "", fmt.Errorf("oneshot dir: %w", err)
	}
	defer os.RemoveAll(tmpDir)

	runID, err := ad.Start(ctx, tmpDir, prompt)
	if err != nil {
		return "", fmt.Errorf("start run: %w", err)
	}
	defer ad.Close(ctx, runID)

	deadline := time.Now().Add(timeout)
	consumed := 0
	var texts []string
	var runErr string
	for {
		// M12 cancel-before-note: a cancelled ctx (auto-distill aborted by a
		// user send) stops the poll promptly instead of riding out the agent.
		// The deferred Close kills the wrapper process.
		if err := ctx.Err(); err != nil {
			return "", err
		}
		evs, err := ad.Events(ctx, runID, consumed)
		if err != nil {
			return "", err
		}
		// M7: a trailing partial event is the transient streaming preview —
		// not journaled, not counted, not part of the concatenated output.
		if n := len(evs); n > 0 && evs[n-1].Payload["partial"] == true {
			evs = evs[:n-1]
		}
		consumed += len(evs)
		for _, ev := range evs {
			switch ev.Type {
			case store.EventAgentText:
				if t, ok := ev.Payload["text"].(string); ok && t != "" {
					texts = append(texts, t)
				}
			case store.EventAgentError:
				if e, ok := ev.Payload["error"].(string); ok {
					runErr = e
				}
			}
		}
		if n := len(evs); n > 0 {
			if t := evs[n-1].Type; t == store.EventAgentDone || t == store.EventAgentError {
				break // terminal adapter event
			}
		}
		if time.Now().After(deadline) {
			_ = ad.Cancel(ctx, runID)
			return "", fmt.Errorf("run timed out")
		}
		time.Sleep(200 * time.Millisecond)
	}
	out := strings.Join(texts, "\n\n")
	if runErr != "" {
		return "", errors.New(runErr)
	}
	if out == "" {
		return "", fmt.Errorf("run produced no output")
	}
	return out, nil
}

// omission is the over-cap fact distillPrompt threads to the distill
// marker (M18 W2 item 2): count = how many events the cap cut from the
// window's HEAD, firstSeq/lastSeq = the held-back prefix's seq range — the
// SAME seq range the prompt's omission line declares. Zero value: nothing
// omitted (under budget).
type omission struct {
	count, firstSeq, lastSeq int
}

// stripPreamble removes any text before the first markdown `# ` heading.
// The distiller is instructed to start with `# `, but as defense-in-depth
// (GLM defect 2: Vietnamese tool-output fragments in moa-chain-epoch-2),
// any preamble — chain-of-thought, tool output, scratch text — is stripped
// before the note is written to disk. If no `# ` heading is found, the
// note is returned as-is (don't discard a valid note that just lacks a
// heading — the curate pass or a future re-distill can fix it).
func stripPreamble(note string) string {
	for i, line := range strings.Split(note, "\n") {
		if strings.HasPrefix(line, "# ") {
			return strings.Join(strings.Split(note, "\n")[i:], "\n")
		}
	}
	return note
}

// distillPrompt renders journaled events into the summary prompt: the M1
// spec's instruction line, the Open loops mandate (R4 — the next epoch's
// cold-start resume card is rendered from this section), then each event
// through distillRender's fold filter (M17 F1). Windows larger than
// distillPromptBytesCap keep the newest events; the omission is declared in
// the prompt so the note never silently claims coverage it didn't see. The
// returned omission comes from the SAME capEvents call that cut the tail
// (threaded, never recomputed) so the marker fact and the prompt line can
// never disagree.
func distillPrompt(events []store.Event) (string, omission) {
	var b strings.Builder
	b.WriteString("Summarize the key decisions, code changes, and open questions from this conversation. Format as markdown.\n\n")
	b.WriteString("Output rules (critical):\n")
	b.WriteString("- Begin with a single `# ` heading — no preamble, no chain-of-thought, no scratch text before it.\n")
	b.WriteString("- Write in English. Code, identifiers, and commit messages stay in English.\n")
	b.WriteString("- The note is a persistent wiki artifact, not a chat reply: be concise, factual, and self-contained.\n\n")
	b.WriteString("The note MUST end with a `## Open loops` section: one bullet per unresolved question, pending task, or decision still awaiting the user. If nothing is open, write `## Open loops` followed by the single line `None.` — never omit the section.\n\n")
	var om omission
	if tail, omitted := capEvents(events, distillPromptBytesCap); omitted > 0 {
		om = omission{count: omitted, firstSeq: events[0].Seq, lastSeq: events[omitted-1].Seq}
		fmt.Fprintf(&b, "[odo: %d older event(s), seq %d–%d, omitted — the journaled window outgrew the %d KiB prompt budget. Summarize only the events below; do not claim coverage of the omitted range.]\n\n",
			omitted, events[0].Seq, events[omitted-1].Seq, distillPromptBytesCap/1024)
		events = tail
	}
	for _, ev := range events {
		b.WriteString(distillRender(ev))
	}
	return b.String(), om
}

// distillRender renders one journaled event for the fold prompt. "" means
// excluded: /panel and /vision advisory agent_text never folds — the same
// eligibility exclusion measureWindow counted, now made concrete in the
// render so eligibility and render agree byte-for-byte (M17 F1).
//
// The filter is why auto-distill windows stopped outgrowing the 256 KiB
// cap inside one run: agent_thinking, agent_tool_call, and
// agent_tool_result payloads are multi-KB by construction but carry no
// fold signal (thinking is the agent's scratch; tool args/results are the
// transcript noise around the user's actual asks — full file contents
// ride write/edit args, omp.go), so they render as one-line tombstones;
// review_action and memory_update bookkeeping renders as
// action/verdict/actor/layer/cause one-liners (auto-panel moa_review /
// auto_revise_round / run_prompt rows excluded —
// foldExcludedReviewAction). user_message and plain agent_text stay
// verbatim — they are what the note must summarize.
func distillRender(ev store.Event) string {
	switch ev.Type {
	case store.EventAgentThinking:
		return fmt.Sprintf("### agent_thinking (seq %d) [thinking omitted — %d bytes]\n\n", ev.Seq, len(ev.Payload))
	case store.EventAgentToolResult:
		var p struct {
			Tool string `json:"tool"`
		}
		if jsonUnmarshalOK(ev.Payload, &p) && p.Tool != "" {
			return fmt.Sprintf("### agent_tool_result (seq %d) [result omitted — %d bytes; tool: %s]\n\n", ev.Seq, len(ev.Payload), p.Tool)
		}
		return fmt.Sprintf("### agent_tool_result (seq %d) [result omitted — %d bytes]\n\n", ev.Seq, len(ev.Payload))
	case store.EventAgentToolCall:
		// M17 F1: write/edit tool calls journal the FULL args (the file
		// content/patch, omp.go) — verbatim they re-create the P0-1
		// over-cap shape in miniature AND capEvents' newest-first fold
		// would evict user messages to keep file contents. Tombstone the
		// args, keep the tool name (which side of the codebase the run
		// touched IS fold signal).
		var p struct {
			Tool string `json:"tool"`
		}
		if jsonUnmarshalOK(ev.Payload, &p) && p.Tool != "" {
			return fmt.Sprintf("### agent_tool_call (seq %d) [args omitted — %d bytes; tool: %s]\n\n", ev.Seq, len(ev.Payload), p.Tool)
		}
		return fmt.Sprintf("### agent_tool_call (seq %d) [args omitted — %d bytes]\n\n", ev.Seq, len(ev.Payload))
	case store.EventReviewAction:
		var p struct {
			Action  string `json:"action"`
			Verdict string `json:"consensus_verdict"`
			Actor   string `json:"actor"`
			Reason  string `json:"reason"`
		}
		if jsonUnmarshalOK(ev.Payload, &p) && p.Action != "" {
			// M18 W2: auto-panel churn rows (moa_review / auto_revise_round /
			// run_prompt / auto_land_started journaled with actor:auto_panel)
			// never fold — the prompt carries the pipeline's OUTCOMES, not
			// its mechanics.
			if foldExcludedReviewAction(p.Action, p.Actor) {
				return ""
			}
			var line strings.Builder
			fmt.Fprintf(&line, `{"action":%q`, p.Action)
			if p.Verdict != "" {
				fmt.Fprintf(&line, `,"verdict":%q`, p.Verdict)
			}
			if p.Actor != "" {
				fmt.Fprintf(&line, `,"actor":%q`, p.Actor)
			}
			if p.Action == "auto_land_blocked" && p.Reason != "" {
				fmt.Fprintf(&line, `,"reason":%q`, p.Reason)
			}
			line.WriteByte('}')
			return fmt.Sprintf("### review_action (seq %d) %s\n\n", ev.Seq, line.String())
		}
		return fmt.Sprintf("### review_action (seq %d) [payload omitted — %d bytes]\n\n", ev.Seq, len(ev.Payload))
	case store.EventMemoryUpdate:
		var p struct {
			Layer string `json:"layer"`
			Cause string `json:"cause"`
		}
		if jsonUnmarshalOK(ev.Payload, &p) {
			// Auto-distill scheduler bookkeeping never renders (the same
			// exclusion measureWindow applies to eligibility): under the
			// daily-cap storm these rows were the noise that outgrew the
			// window they were measuring. The note summarizes agent/user
			// activity; the scheduler's own arming decisions are not it.
			if foldExcludedMemoryUpdate(p.Layer, p.Cause) {
				return ""
			}
			return fmt.Sprintf("### memory_update (seq %d) {\"layer\":%q,\"cause\":%q}\n\n", ev.Seq, p.Layer, p.Cause)
		}
		return fmt.Sprintf("### memory_update (seq %d) [payload omitted — %d bytes]\n\n", ev.Seq, len(ev.Payload))
	case store.EventAgentText:
		if isAdvisoryAgentText(ev) {
			return ""
		}
		return fmt.Sprintf("### %s (seq %d)\n%s\n\n", ev.Type, ev.Seq, ev.Payload)
	case store.EventLoopEvent:
		// M19: the fold renders the loop's KIND — suspensions are the
		// open loops the note must surface; payloads (findings rounds can
		// carry tens of KB of audits) stay out, same doctrine as
		// review_action one-liners.
		if k := jsonStr(ev.Payload, "kind"); k != "" {
			cause := jsonStr(ev.Payload, "cause")
			if cause != "" {
				return fmt.Sprintf("### loop_event (seq %d) {\"kind\":%q,\"cause\":%q}\n\n", ev.Seq, k, cause)
			}
			return fmt.Sprintf("### loop_event (seq %d) {\"kind\":%q}\n\n", ev.Seq, k)
		}
		return fmt.Sprintf("### loop_event (seq %d) [payload omitted — %d bytes]\n\n", ev.Seq, len(ev.Payload))
	case store.EventUserMessage:
		// M18: a synthesized repair prompt is daemon bookkeeping wearing a
		// user_message row — multi-KB by construction (32KB diff + 12KB
		// comments), and the note summarizes USER asks. One-line
		// tombstone, M17 F1 shape.
		if m, ok := parseAutoReviseMarker(ev.Payload); ok {
			return fmt.Sprintf("### user_message (seq %d) [auto_revise round %d prompt omitted — %d bytes]\n\n", ev.Seq, m.Round, len(ev.Payload))
		}
		// M19: loop fix/implement prompts are the same shape (BYOF
		// findings verbatim + demotion directive).
		if m, ok := parseLoopFixMarker(ev.Payload); ok {
			return fmt.Sprintf("### user_message (seq %d) [loop_fix loop %d prompt omitted — %d bytes]\n\n", ev.Seq, m.LoopID, len(ev.Payload))
		}
		return fmt.Sprintf("### %s (seq %d)\n%s\n\n", ev.Type, ev.Seq, ev.Payload)
	default:
		return fmt.Sprintf("### %s (seq %d)\n%s\n\n", ev.Type, ev.Seq, ev.Payload)
	}
}

// isAdvisoryAgentText reports whether ev is a /panel or /vision advisory
// answer (payload-flagged, never a run answer): excluded from the fold
// render and from eligibility bytes.
func isAdvisoryAgentText(ev store.Event) bool {
	if ev.Type != store.EventAgentText {
		return false
	}
	var p struct {
		Panel  bool `json:"panel"`
		Vision bool `json:"vision"`
	}
	return jsonUnmarshalOK(ev.Payload, &p) && (p.Panel || p.Vision)
}

// foldExcludedReviewAction reports whether a review_action row is auto-land
// pipeline churn: moa_review / auto_revise_round / run_prompt rows journaled
// with actor:auto_panel never fold — the per-leg panel evidence and round
// mechanics are transcript noise. (W6: run_prompt{origin:"parked_goal",
// actor:auto_panel} rows are covered by the same run_prompt entry; a parked
// user_message folds like any user turn — a parked goal IS a user ask — and
// manual resume/drop rows carry no actor, so they render their one-liner.) The note carries the pipeline's OUTCOMES
// instead: accept rows (what landed) and auto_land_blocked rows, whose
// reason IS the open loop the note must surface. auto_land_started rows
// (indicator-lock Phase 2 liveness breadcrumbs) are the same class: GUI
// chip signals, never distill-prompt content.
func foldExcludedReviewAction(action, actor string) bool {
	if actor != autoActor {
		return false
	}
	switch action {
	case "moa_review", "auto_revise_round", "run_prompt", "auto_land_started":
		return true
	}
	return false
}

// foldExcludedMemoryUpdate reports whether a memory_update row is
// auto-distill SCHEDULER bookkeeping: scheduled / skipped /
// cap_suspended_until rows are the trigger machinery's own noise. They
// journal every evaluation (nothing skips silently — the scheduler's
// audit trail), but they never fold into the prompt and never count
// toward eligibility: under the daily-cap feedback loop those rows were
// precisely what outgrew the un-folded window they were measuring
// (3786→3819 events / 497KB→565KB of pure scheduler noise in production).
// Outcome rows keep rendering: fired / failed / cancelled_by_send /
// supersession markers are epoch signal, one per actual fold at most.
//
// This is the RENDER-side predicate only. The eligibility count
// (measureWindow) is the strictly wider windowExcludedMemoryUpdate — the
// two must not reconverge: the render/exclusion split is load-bearing.
func foldExcludedMemoryUpdate(layer, cause string) bool {
	if layer != "auto_distill" {
		return false
	}
	switch cause {
	case "scheduled", "skipped", autoCauseCapSuspended:
		return true
	}
	return false
}

// windowExcludedMemoryUpdate is the ELIGIBILITY-side predicate
// (measureWindow only — the distill prompt never sees it): everything
// foldExcludedMemoryUpdate excludes PLUS the boot replayer's recovery
// family — recover / heal_merged / heal_conflict / heal_resolved
// (design notes also call the restore row heal_replayed; both names
// below), journaled with layer = the healed memory layer (NOT
// auto_distill, so the render predicate above never matches them). Heal
// rows are boot/crash-recovery bookkeeping, not agent/user activity: a
// post-crash boot that journals a merge storm re-stamps whichever
// conversation window those rows ride, and heal_conflict rows embed
// KB-sized stranded_body payloads that would swamp the byte axis (DSF
// verification note, 2026-08-26).
//
// The split is deliberate and asymmetric: the SAME heal rows KEEP
// RENDERING in the distill prompt — they are outcome rows in the
// fired / failed / cancelled_by_send class (they happened to real memory
// content; they ARE the epoch's history), so the eligibility count knows
// them and the render filter must not.
//
// The recovery-cause set is matched only on layers the boot replayer
// actually heals (replayLayerKind: memory / archive / user / pins /
// skill:<base>) — a structural tie, not just a comment: the replayer is
// the family's only writer today, and a memory_update on any OTHER layer
// carrying one of these cause strings (no such writer exists) keeps
// counting as activity instead of silently vanishing from both axes.
func windowExcludedMemoryUpdate(layer, cause string) bool {
	if foldExcludedMemoryUpdate(layer, cause) {
		return true
	}
	// recover is the journaled restore cause in memory_replay.go;
	// heal_replayed is the same family's name in the design notes and the
	// original triage — neither naming must ever count as window activity.
	switch cause {
	case "recover", "heal_replayed", "heal_merged", "heal_conflict", "heal_resolved":
		_, ok := replayLayerKind(layer)
		return ok
	}
	return false
}

// distillRenderSize sizes ev's fold-prompt render. Eligibility
// (measureWindow) and coverage honesty (capEvents) share this accounting so
// "window bytes" is always the size of what the distiller is actually sent.
// Full renders keep the len(type)+len(payload)+64 estimate — never
// materializing a multi-KB payload just to count it; the one-liner kinds
// are small enough to render and measure exactly.
func distillRenderSize(ev store.Event) int {
	switch ev.Type {
	case store.EventAgentThinking, store.EventAgentToolResult,
		store.EventAgentToolCall, store.EventReviewAction,
		store.EventMemoryUpdate, store.EventLoopEvent:
		return len(distillRender(ev))
	case store.EventAgentText:
		if isAdvisoryAgentText(ev) {
			return 0
		}
		return len(ev.Type) + len(ev.Payload) + 64 // header + separators, over-estimated
	case store.EventUserMessage:
		// M18: tombstoned repair prompts measure exactly (render ==
		// accounting, the M17 F1 byte-for-byte agreement). M19 loop
		// prompts measure exactly too.
		if _, ok := parseAutoReviseMarker(ev.Payload); ok {
			return len(distillRender(ev))
		}
		if _, ok := parseLoopFixMarker(ev.Payload); ok {
			return len(distillRender(ev))
		}
		return len(ev.Type) + len(ev.Payload) + 64
	default:
		return len(ev.Type) + len(ev.Payload) + 64 // header + separators, over-estimated
	}
}

// distillPromptBytesCap bounds the rendered event section (~256 KiB ≈
// 60–85K tokens) so the one-shot prompt stays inside the distill model's
// context with room for its thinking budget. The epoch window keeps
// routine distills far below this; the cap is the no-marker / pathological
// epoch backstop.
const distillPromptBytesCap = 256 * 1024

// capEvents keeps the newest events whose RENDERED size (distillRenderSize
// — post-filter bytes, advisory answers excluded) fits budget. The newest
// event is kept even when it alone exceeds the budget.
func capEvents(events []store.Event, budget int) (tail []store.Event, omitted int) {
	size := 0
	start := 0
	for i := len(events) - 1; i >= 0; i-- {
		n := distillRenderSize(events[i])
		if size > 0 && size+n > budget {
			start = i + 1
			break
		}
		size += n
	}
	return events[start:], start
}

// latestDiffInfo returns the latest diff for a conversation with its content,
// or nil when the conversation has no diffs.
func (s *Server) latestDiffInfo(ctx context.Context, conversationID int64) *DiffInfo {
	d, err := s.store.LatestDiff(ctx, conversationID)
	if err != nil {
		return nil
	}
	info := &DiffInfo{ID: d.ID, Status: d.Status, Path: d.PathOnDisk}
	if b, err := os.ReadFile(d.PathOnDisk); err == nil {
		info.Content = string(b)
	}
	return info
}

// pendingDiffInfos returns all pending diffs for the conversation with
// their content. Newest-first ordering matches the review flow.
func (s *Server) pendingDiffInfos(ctx context.Context, conversationID int64) []DiffInfo {
	diffs, err := s.store.ListPendingDiffs(ctx, conversationID)
	if err != nil || len(diffs) == 0 {
		return nil
	}
	out := make([]DiffInfo, 0, len(diffs))
	for _, d := range diffs {
		info := DiffInfo{ID: d.ID, Status: d.Status, Path: d.PathOnDisk}
		if b, err := os.ReadFile(d.PathOnDisk); err == nil {
			info.Content = string(b)
		}
		out = append(out, info)
	}
	return out
}

// guardLiveWorkstreamLocked refuses to begin new liveness-bearing work
// (run, distill, slash consult, loop admission, scheduled auto) on a
// workstream that is mid-delete or already soft-deleted (2026-08-25
// review follow-up, closing the audit P1's residual window): the
// previous diff's busy-check released s.mu before the SQL delete — a
// start inside that window keyed live work onto a lane the Review inbox
// (active-only) had just stopped listing, and its next diff stranded
// unseen. The mid-delete flag rises under the SAME s.mu hold that proved
// the lane idle, so exactly one refusal fires: the delete sees the
// start's registration (busy), or the start sees the flag here. The
// status half covers starts arriving after the commit. Caller holds
// s.mu; w is the caller's row read under the same hold.
func (s *Server) guardLiveWorkstreamLocked(w store.Workstream) error {
	if _, ok := s.deletingWs[w.ID]; ok {
		return fmt.Errorf("workstream %d is being deleted — let the delete finish", w.ID)
	}
	if w.Status != store.WorkstreamActive {
		return fmt.Errorf("workstream %d is %s — new work refuses a deleted lane", w.ID, w.Status)
	}
	return nil
}

// guardLiveConversationLocked is guardLiveWorkstreamLocked for call sites
// holding only a conversation ID (armAutoLocked): conv and workstream
// load under the caller's s.mu hold so the read is race-free against the
// delete commit (store reads under s.mu are the maybeAutoAutoAfterActivity
// precedent; both are human-paced).
func (s *Server) guardLiveConversationLocked(ctx context.Context, convID int64) error {
	c, err := s.store.GetConversation(ctx, convID)
	if err != nil {
		return err
	}
	w, err := s.store.GetWorkstream(ctx, c.WorkstreamID)
	if err != nil {
		return err
	}
	return s.guardLiveWorkstreamLocked(w)
}

// checkConversation validates that the conversation exists and belongs to
// this daemon's project.
func (s *Server) checkConversation(ctx context.Context, conversationID int64) (store.Conversation, error) {
	if conversationID == 0 {
		return store.Conversation{}, fmt.Errorf("conversation_id is required")
	}
	c, err := s.store.GetConversation(ctx, conversationID)
	if err != nil {
		return store.Conversation{}, err
	}
	w, err := s.store.GetWorkstream(ctx, c.WorkstreamID)
	if err != nil {
		return store.Conversation{}, err
	}
	p, err := s.store.GetProject(ctx, w.ProjectID)
	if err != nil {
		return store.Conversation{}, err
	}
	if p.RootPath != s.projectRoot {
		return store.Conversation{}, fmt.Errorf("conversation %d belongs to project %s, not %s",
			conversationID, p.RootPath, s.projectRoot)
	}
	return c, nil
}

// failRun journals an agent_error for a run that could not start and returns
// the error for the response.
func (s *Server) failRun(ctx context.Context, conversationID int64, cause error) error {
	_, _ = s.store.AppendEvent(ctx, conversationID, store.EventAgentError,
		mustJSON(map[string]interface{}{"error": cause.Error()}))
	return cause
}

// mustJSON marshals a payload map; adapter payloads are always marshal-safe.
func mustJSON(v interface{}) string {
	b, err := json.Marshal(v)
	if err != nil {
		return `{"error":"payload marshal failed"}`
	}
	return string(b)
}

// M8 (Skills): handleListSkills returns metadata for all discovered skills
// (global ~/.odo/skills/*.md + project .odo/skills/*.md). Read-only.
func (s *Server) handleListSkills(ctx context.Context, req Request) (Response, error) {
	if _, err := s.resolveProject(ctx, req.ProjectRoot); err != nil {
		return Response{}, fmt.Errorf("list_skills: %w", err)
	}
	entries := scanSkills(s.projectRoot)
	var infos []SkillInfo
	for _, e := range entries {
		infos = append(infos, e.info)
	}
	return Response{OK: true, Skills: infos}, nil
}

// handleReadSkill returns the full markdown body of one skill file.
func (s *Server) handleReadSkill(ctx context.Context, req Request) (Response, error) {
	if _, err := s.resolveProject(ctx, req.ProjectRoot); err != nil {
		return Response{}, fmt.Errorf("read_skill: %w", err)
	}
	if req.Path == "" {
		return Response{}, fmt.Errorf("read_skill: path is required")
	}
	// Before building candidates, clean the path and reject traversal.
	// The GUI sends bare filenames, never absolute paths.
	name := filepath.Clean(req.Path)
	if strings.Contains(name, "..") || filepath.IsAbs(name) {
		return Response{}, fmt.Errorf("read_skill: invalid path: %s", req.Path)
	}
	// Resolve the path: project skills dir first, then global. The project
	// candidate is repo-committable, so a checked-in symlink must not read
	// outside .odo/skills (tri-review P0, 2026-08-24); readWithinDir
	// resolves and refuses the escape, and any failure other than "missing"
	// is surfaced instead of being silently shadowed by a same-named
	// global skill. The global candidate is the user's own tree, outside
	// that model — plain os.ReadFile.
	home, _ := os.UserHomeDir()
	base := filepath.Base(name)
	projectSkills := filepath.Join(s.projectRoot, ".odo", "skills")
	if b, err := readWithinDir(s.projectRoot, projectSkills, filepath.Join(projectSkills, base)); err == nil {
		return Response{OK: true, SkillContent: string(b)}, nil
	} else if !os.IsNotExist(err) {
		return Response{}, fmt.Errorf("read_skill: %w", err)
	}
	if b, err := os.ReadFile(filepath.Join(home, ".odo", "skills", base)); err == nil {
		return Response{OK: true, SkillContent: string(b)}, nil
	}
	return Response{}, fmt.Errorf("read_skill: skill file not found: %s", req.Path)
}

// handleUpdateSkill writes (creates or overwrites) a skill file. The scope
// ("global" or "project") is passed explicitly on the wire — the daemon
// never infers scope from path prefix (K3 H3 fix). This is the
// human-in-the-loop write path — the daemon never auto-writes skills
// (ADR-0003 invariant).
func (s *Server) handleUpdateSkill(ctx context.Context, req Request) (Response, error) {
	if _, err := s.resolveProject(ctx, req.ProjectRoot); err != nil {
		return Response{}, fmt.Errorf("update_skill: %w", err)
	}
	if req.Name == "" {
		return Response{}, fmt.Errorf("update_skill: name is required")
	}
	if req.Text == "" {
		return Response{}, fmt.Errorf("update_skill: content is required")
	}
	// Determine target dir by explicit scope (not path inference).
	projectScope := req.Scope != "global"
	var dir string
	if projectScope {
		dir = filepath.Join(s.projectRoot, ".odo", "skills")
		// Guard BEFORE MkdirAll: a symlinked .odo or skills dir would pull
		// the mkdir and the write outside the committable tree
		// (2026-08-25 review P0). Global ~/.odo/skills stays unguarded —
		// outside the threat model, dotfiles links legitimate.
		if err := guardProjectWritePath(s.projectRoot, filepath.Join(dir, "skill.md")); err != nil {
			return Response{}, fmt.Errorf("update_skill: %w", err)
		}
	} else {
		home, _ := os.UserHomeDir()
		dir = filepath.Join(home, ".odo", "skills")
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return Response{}, fmt.Errorf("update_skill: mkdir: %w", err)
	}
	// Sanitize: strip directory components from name to prevent path traversal.
	fname := filepath.Base(req.Name)
	if !strings.HasSuffix(fname, ".md") {
		fname += ".md"
	}
	if strings.Contains(fname, "..") {
		return Response{}, fmt.Errorf("update_skill: invalid name: %s", req.Name)
	}
	target := filepath.Join(dir, fname)
	if projectScope {
		if err := writeFileWithin(s.projectRoot, target, req.Text, 0o644); err != nil {
			return Response{}, fmt.Errorf("update_skill: write: %w", err)
		}
	} else if err := writeFileAtomic(target, req.Text, 0o644); err != nil {
		return Response{}, fmt.Errorf("update_skill: write: %w", err)
	}
	return Response{OK: true}, nil
}

// handleDeleteSkill removes a skill file. The scope ("global" or "project")
// is passed explicitly on the wire. The filename is sanitized via
// filepath.Base to prevent path traversal. Only known skills directories
// are targeted.
func (s *Server) handleDeleteSkill(ctx context.Context, req Request) (Response, error) {
	if _, err := s.resolveProject(ctx, req.ProjectRoot); err != nil {
		return Response{}, fmt.Errorf("delete_skill: %w", err)
	}
	if req.Name == "" {
		return Response{}, fmt.Errorf("delete_skill: name is required")
	}
	var dir string
	if req.Scope == "global" {
		home, _ := os.UserHomeDir()
		dir = filepath.Join(home, ".odo", "skills")
	} else {
		dir = filepath.Join(s.projectRoot, ".odo", "skills")
	}
	fname := filepath.Base(req.Name)
	if !strings.HasSuffix(fname, ".md") {
		fname += ".md"
	}
	if strings.Contains(fname, "..") {
		return Response{}, fmt.Errorf("delete_skill: invalid name: %s", req.Name)
	}
	target := filepath.Join(dir, fname)
	if req.Scope != "global" {
		// Containment (2026-08-25 audit P0): update_skill was guarded but
		// delete was not — a symlinked .odo or .odo/skills would make
		// os.Remove unlink a file OUTSIDE the committable tree. Refuse
		// before touching the filesystem, mirroring handleUpdateSkill.
		if err := guardProjectWritePath(s.projectRoot, target); err != nil {
			return Response{}, fmt.Errorf("delete_skill: %w", err)
		}
	}
	if err := os.Remove(target); err != nil {
		return Response{}, fmt.Errorf("delete_skill: %w", err)
	}
	return Response{OK: true}, nil
}

// A1: handleSaveAttachment writes a base64-encoded file (from clipboard paste)
// to .odo/attachments/<timestamp>-<name> and returns the absolute path so the
// frontend can use it as an attachment for /vision queries.
func (s *Server) handleSaveAttachment(ctx context.Context, req Request) (Response, error) {
	if req.Name == "" {
		return Response{}, fmt.Errorf("save_attachment: name is required")
	}
	if req.Data == "" {
		return Response{}, fmt.Errorf("save_attachment: data is required")
	}
	// Sanitize filename — prevent path traversal.
	base := filepath.Base(req.Name)
	attachDir, err := attachmentDir(s.projectRoot)
	if err != nil {
		return Response{}, fmt.Errorf("save_attachment: mkdir: %w", err)
	}
	// Prepend timestamp to avoid collisions.
	ts := time.Now().UnixMilli()
	dest := filepath.Join(attachDir, fmt.Sprintf("%d-%s", ts, base))
	// Decode base64.
	data, err := base64.StdEncoding.DecodeString(req.Data)
	if err != nil {
		return Response{}, fmt.Errorf("save_attachment: base64 decode: %w", err)
	}
	// Contained write (2026-08-25 review P0): the clipboard bytes land
	// under the committable .odo tree — a planted symlink must not pull
	// them onto an external path.
	if err := guardProjectWritePath(s.projectRoot, dest); err != nil {
		return Response{}, fmt.Errorf("save_attachment: %w", err)
	}
	if err := os.WriteFile(dest, data, 0o644); err != nil {
		return Response{}, fmt.Errorf("save_attachment: write: %w", err)
	}
	return Response{OK: true, Path: dest}, nil
}
