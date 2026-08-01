// odo is the M0 daemon: SQLite journal + OMP agent runs in git worktrees +
// line-delimited JSON IPC over a Unix socket. Started by the Tauri app (M0: by
// hand or a launcher). Logs to stderr.
package main

import (
	"errors"
	"flag"
	"log"
	"net"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"

	"github.com/yingliang-zhang/odo/internal/adapter"
	"github.com/yingliang-zhang/odo/internal/ipc"
	"github.com/yingliang-zhang/odo/internal/store"
	"github.com/yingliang-zhang/odo/internal/worktree"
)

func main() {
	log.SetPrefix("odo: ")
	log.SetFlags(log.LstdFlags | log.Lmsgprefix)

	var projectFlag, socketFlag string
	flag.StringVar(&projectFlag, "project", "", "project root (default: current working directory)")
	flag.StringVar(&socketFlag, "socket", "", "IPC socket path (default: <project>/.odo/odo.sock)")
	flag.Parse()

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
	st, err := store.Open(filepath.Join(mgr.StateDir(), "journal.sqlite"))
	if err != nil {
		log.Fatalf("open journal: %v", err)
	}

	omp := adapter.NewOMP(mgr.StateDir())
	srv := ipc.NewServer(st, root, omp, mgr)

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
