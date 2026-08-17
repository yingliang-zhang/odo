// odo is the M0 daemon: SQLite journal + OMP agent runs in git worktrees +
// line-delimited JSON IPC over a Unix socket. Started by the Tauri app (M0: by
// hand or a launcher). Logs to stderr.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log"
	"net"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"

	"github.com/yingliang-zhang/odo/internal/adapter"
	"github.com/yingliang-zhang/odo/internal/ipc"
	"github.com/yingliang-zhang/odo/internal/store"
	"github.com/yingliang-zhang/odo/internal/worktree"
)

func main() {
	log.SetPrefix("odo: ")
	log.SetFlags(log.LstdFlags | log.Lmsgprefix)

	// .app-launch env enrichment: macOS GUI apps don't source ~/.zshrc, so
	// SUDO_CODING_KEY (used by moa.NewClientFromEnv for review_diff/panel/
	// vision/skill-gate) and PATH (for git, omp, etc.) are missing.
	// Inject them here so the daemon process itself has them, not just the
	// OMP child subprocess (enrichedEnv in omp.go handles the child).
	enrichDaemonEnv()

	var projectFlag, socketFlag string
	flag.StringVar(&projectFlag, "project", "", "project root (default: current working directory)")
	flag.StringVar(&socketFlag, "socket", "", "IPC socket path (default: <project>/.odo/odo.sock)")
	flag.Parse()

	// M6: subcommand dispatch. `odo wiki read <page>` / `odo ledger [tail N]`
	// are pull-based recall CLIs that read files directly (no daemon);
	// `odo journal <sub>` is the read-only rehydration CLI for folded
	// events; `odo recall audit` (M12 Batch 3a) is the read-only recall
	// miss-rate report; `odo skills audit` / `odo autonomy audit` (M15)
	// are read-only outcome-observability reports; `odo rules audit`
	// (Wave 1 of the self-improving MVP) is the memory-rule outcome audit
	// with flag sinks; `odo unretract <note>` (M17 F3) and the rules-audit
	// sinks are the WRITE CLIs — they journal via the store's own write
	// open. Any other invocation without a subcommand runs the daemon.
	if args := flag.Args(); len(args) > 0 {
		switch args[0] {
		case "wiki":
			os.Exit(runWikiCLI(args[1:]))
		case "ledger":
			os.Exit(runLedgerCLI(args[1:]))
		case "journal":
			os.Exit(runJournalCLI(args[1:]))
		case "todo":
			os.Exit(runTodoCLI(args[1:]))
		case "recall":
			os.Exit(runRecallCLI(args[1:]))
		case "skills":
			os.Exit(runSkillsCLI(args[1:]))
		case "autonomy":
			os.Exit(runAutonomyCLI(args[1:]))
		case "rules":
			os.Exit(runRulesCLI(args[1:]))
		case "unretract":
			os.Exit(runUnretractCLI(args[1:]))
		case "models":
			os.Exit(runModelsCLI(args[1:]))
		}
	}

	root := projectFlag
	if root == "" {
		var err error
		root, err = os.Getwd()
		if err != nil {
			log.Fatalf("resolve cwd: %v", err)
		}
	}
	root, err := filepath.Abs(root)
	if err != nil {
		log.Fatalf("resolve project root: %v", err)
	}
	if st, err := os.Stat(root); err != nil || !st.IsDir() {
		log.Fatalf("project root %s is not a directory", root)
	}

	mgr := worktree.NewManager(root)
	if err := mgr.EnsureDirs(); err != nil {
		log.Fatalf("init state dirs: %v", err)
	}

	// Single-instance guard (epoch-8 outstanding #4): one flock per project
	// state dir, taken BEFORE the journal open and before socket remove/
	// listen. The old flow removed + rebound the socket unconditionally — a
	// second daemon deleted the live one's socket file (dual daemons then
	// silently void acceptMu/autoLandMu serialization and race the journal),
	// and an old instance's shutdown unlink could remove the NEW owner's
	// socket. flock releases on any process death including SIGKILL, so a
	// crash-stale lock is impossible by construction; the leftover socket
	// file it leaves behind is handled by the stale-socket remove below.
	instanceLock, err := acquireInstanceLock(mgr.StateDir())
	if err != nil {
		if errors.Is(err, errDaemonAlreadyRunning) {
			// Exit 3 = "a live daemon already serves this project": the
			// Tauri respawn loop reads this code as attach-to-live, not as
			// a spawn failure (panel 2026-08-12 — silent dual-state was
			// the worse failure, noisy respawn-thrash the second).
			log.Printf("%v", err)
			os.Exit(exitCodeAlreadyRunning)
		}
		log.Fatalf("%v", err)
	}
	defer instanceLock.Close()

	st, err := store.Open(filepath.Join(mgr.StateDir(), "journal.sqlite"))
	if err != nil {
		log.Fatalf("open journal: %v", err)
	}

	omp := adapter.NewOMP(mgr.StateDir())
	distillOMP := adapter.NewOMPForKey(mgr.StateDir(), "orchestrator")
	srv := ipc.NewServer(st, root, omp, mgr)
	srv.SetDistillAdapter(distillOMP)

	// M12 (D-auto): startup compensation — arm startup triggers for active
	// conversations whose un-folded window went stale while the app was
	// closed (the missed-fold hole), run the legacy auto-curate pref
	// migration, and evaluate the conditional auto-curate. Best-effort:
	// failures are logged per conversation; a hard failure must not stop
	// the daemon from serving.
	if err := srv.StartupAutoScan(context.Background()); err != nil {
		log.Printf("auto-distill startup scan: %v", err)
	}

	// B-class lifecycle (I8/I10): converge .odo/worktrees to the journal's
	// truth before serving — reclaim orphans (crashed runs, failed retires),
	// keep pending-review and live bindings, retire legacy odo/* refs.
	// Best-effort like the auto scan above; every decision is audit-logged.
	srv.SweepOrphanWorktrees(context.Background())

	socket := socketFlag
	if socket == "" {
		socket = filepath.Join(mgr.StateDir(), "odo.sock")
	}
	// Remove a stale socket from a previous crash before binding.
	if err := os.Remove(socket); err != nil && !errors.Is(err, os.ErrNotExist) {
		log.Fatalf("remove stale socket: %v", err)
	}
	listener, err := net.Listen("unix", socket)
	if err != nil {
		log.Fatalf("listen on %s: %v", socket, err)
	}
	// F-chmod: restrict socket to owner-only (0600). The Tauri client
	// runs as the same user; this closes the cross-user read/write vector.
	if err := os.Chmod(socket, 0o600); err != nil {
		log.Fatalf("odo: chmod socket %s: %v", socket, err)
	}

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	go func() {
		sig := <-sigCh
		log.Printf("received %s, shutting down", sig)
		listener.Close()
	}()

	log.Printf("project %s", root)
	log.Printf("journal %s", filepath.Join(mgr.StateDir(), "journal.sqlite"))
	log.Printf("listening on %s", socket)
	if err := srv.Serve(listener); err != nil {
		log.Printf("serve: %v", err)
	}
	// Drain in-flight per-connection handler goroutines (M11 P0) — e.g. a
	// distill still inside its agent run — before killing agents. Connections
	// are per-request, so this waits only for requests already being served.
	srv.Wait()

	// Kill in-flight agents so no orphan keeps writing into a worktree, then
	// release resources. Worktrees and diffs persist for review on next boot.
	omp.CloseAll()
	if err := listener.Close(); err != nil && !errors.Is(err, net.ErrClosed) {
		log.Printf("close listener: %v", err)
	}
	if err := os.Remove(socket); err != nil {
		log.Printf("remove socket: %v", err)
	}
	if err := st.Close(); err != nil {
		log.Printf("close journal: %v", err)
	}
	log.Printf("bye")
}

// exitCodeAlreadyRunning is the daemon's distinct exit for the
// lock-held-by-a-live-peer case. Tauri's ensure_daemon_running attaches
// instead of reporting a spawn failure (panel fix 2026-08-12).
//
// Exclusivity contract (round-2 panel): exit 3 means lock contention and
// NOTHING else — lock-file open/IO failures stay on the generic Fatalf
// path (exit 1), so Tauri never treats a permissions/IO fault as a live
// peer. Only errDaemonAlreadyRunning maps here.
const exitCodeAlreadyRunning = 3

// errDaemonAlreadyRunning wraps the instance-lock contention error so
// main can route it to exitCodeAlreadyRunning.
var errDaemonAlreadyRunning = errors.New("daemon already running")

// acquireInstanceLock takes the per-project single-instance flock
// (<stateDir>/odo.lock, 0600) and returns the held file; the caller keeps
// it open for the process lifetime. An already-locked file means another
// live daemon serves this project — refuse rather than fork shared state.
// The socket unlink at shutdown is owned transitively: only the process
// holding this lock ever reaches Serve/cleanup.
//
// Scope: per-host local filesystems. flock is unreliable on NFSv3-style
// network mounts, but a stateDir on a network share is already broken
// below this layer (unix-socket and sqlite journal are both host-local);
// failing closed there is the correct posture.
func acquireInstanceLock(stateDir string) (*os.File, error) {
	lockPath := filepath.Join(stateDir, "odo.lock")
	f, err := os.OpenFile(lockPath, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, fmt.Errorf("open instance lock %s: %w", lockPath, err)
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		_ = f.Close()
		return nil, fmt.Errorf("%w: another odo daemon serves this project (lock %s): %v",
			errDaemonAlreadyRunning, lockPath, err)
	}
	return f, nil
}

// enrichDaemonEnv injects environment variables that are missing when
// the daemon is launched from a .app bundle (macOS GUI apps don't source
// ~/.zshrc). This covers the daemon's OWN environment — the OMP child
// subprocess gets its own enrichment via enrichedEnv() in omp.go.
// Without this, moa.NewClientFromEnv (review_diff/panel/vision/skill-gate)
// can't find SUDO_CODING_KEY and returns "API key is empty".
func enrichDaemonEnv() {
	home, err := os.UserHomeDir()
	if err != nil {
		return
	}
	// Inject SUDO_CODING_KEY if missing.
	if os.Getenv("SUDO_CODING_KEY") == "" {
		if key := adapter.ExtractExportFromZshrc(home, "SUDO_CODING_KEY"); key != "" {
			os.Setenv("SUDO_CODING_KEY", key)
		}
	}
	// Enrich PATH if it looks minimal (missing /opt/homebrew/bin).
	if !strings.Contains(os.Getenv("PATH"), "/opt/homebrew/bin") {
		extra := []string{
			"/opt/homebrew/bin",
			"/usr/local/bin",
			filepath.Join(home, ".local", "bin"),
			filepath.Join(home, ".cargo", "bin"),
			filepath.Join(home, ".omp", "bin"),
			filepath.Join(home, ".hermes", "node", "bin"),
			filepath.Join(home, "go", "bin"),
		}
		current := os.Getenv("PATH")
		if current == "" {
			os.Setenv("PATH", strings.Join(extra, string(filepath.ListSeparator)))
		} else {
			os.Setenv("PATH", current+string(filepath.ListSeparator)+strings.Join(extra, string(filepath.ListSeparator)))
		}
	}
}
