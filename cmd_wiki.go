package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// M6 (Precision + Ledger) §9: pull-based recall for coding agents. The CLI
// is a subcommand of the existing `odo` binary that reads files directly
// from the project root (cwd) — NO daemon access. Wiki notes, topic pages,
// index.md, and ledger.md are plain files on disk (derived artifacts; the
// journal is the source of truth for events, not the daemon process), so an
// agent's own shell access is the transport — no MCP server, no socket.

// runWikiCLI dispatches `odo wiki <sub>`. M6: `read <page>` only.
func runWikiCLI(args []string) int {
	if len(args) < 2 || args[0] != "read" {
		fmt.Fprintln(os.Stderr, "usage: odo wiki read <page>")
		return 2
	}
	return wikiRead(args[1])
}

// wikiRead resolves page to a file under the cwd and prints its content to
// stdout:
//
//	ledger           → .odo/ledger.md  (friendly name for the metrics ledger)
//	<name>           → wiki/<name>.md  (epoch notes, index, topics/<slug>)
//
// Path guard (same class as read_wiki's, extended for the .odo/ledger.md
// exception): the resolved path must sit under wiki/ or equal exactly
// .odo/ledger.md; traversal (`../../etc/passwd`) is rejected. A missing
// file is an error (exit 1), not empty stdout — so a shell
// `test -n "$(odo wiki read …)"` check is reliable.
func wikiRead(page string) int {
	cwd, err := os.Getwd()
	if err != nil {
		fmt.Fprintf(os.Stderr, "odo wiki read: resolve cwd: %v\n", err)
		return 1
	}
	var resolved string
	if page == "ledger" {
		resolved = filepath.Join(cwd, ".odo", "ledger.md")
	} else {
		resolved = filepath.Join(cwd, "wiki", page+".md")
	}
	clean := filepath.Clean(resolved)
	rel, err := filepath.Rel(cwd, clean)
	if err != nil || strings.HasPrefix(rel, "..") ||
		(rel != filepath.Join(".odo", "ledger.md") &&
			!strings.HasPrefix(rel, "wiki"+string(filepath.Separator))) {
		fmt.Fprintf(os.Stderr, "odo wiki read: only files under wiki/ (or .odo/ledger.md) are readable, got %q\n", page)
		return 1
	}
	b, err := os.ReadFile(clean)
	if err != nil {
		fmt.Fprintf(os.Stderr, "odo wiki read: %v\n", err)
		return 1
	}
	if _, err := os.Stdout.Write(b); err != nil {
		fmt.Fprintf(os.Stderr, "odo wiki read: write stdout: %v\n", err)
		return 1
	}
	return 0
}

// runLedgerCLI implements `odo ledger [tail N]`: print .odo/ledger.md from
// the cwd (equivalent to `odo wiki read ledger`), optionally only the last
// N `## epoch …` sections. Plain files, no daemon access.
func runLedgerCLI(args []string) int {
	tail := 0
	switch {
	case len(args) == 0:
	case len(args) == 2 && args[0] == "tail":
		n, err := strconv.Atoi(args[1])
		if err != nil || n < 1 {
			fmt.Fprintf(os.Stderr, "odo ledger: tail count must be a positive integer, got %q\n", args[1])
			return 2
		}
		tail = n
	default:
		fmt.Fprintln(os.Stderr, "usage: odo ledger [tail N]")
		return 2
	}
	cwd, err := os.Getwd()
	if err != nil {
		fmt.Fprintf(os.Stderr, "odo ledger: resolve cwd: %v\n", err)
		return 1
	}
	b, err := os.ReadFile(filepath.Join(cwd, ".odo", "ledger.md"))
	if err != nil {
		fmt.Fprintf(os.Stderr, "odo ledger: %v\n", err)
		return 1
	}
	content := string(b)
	if tail > 0 {
		content = tailLedgerSections(content, tail)
	}
	if _, err := os.Stdout.Write([]byte(content)); err != nil {
		fmt.Fprintf(os.Stderr, "odo ledger: write stdout: %v\n", err)
		return 1
	}
	return 0
}

// tailLedgerSections returns the last n `## `-headed sections of the
// ledger (or the whole content when fewer exist). Section-aware: a ledger
// line count would cut a section's bullets in half.
func tailLedgerSections(content string, n int) string {
	var starts []int
	for i, line := range strings.Split(content, "\n") {
		if strings.HasPrefix(line, "## ") {
			starts = append(starts, i)
		}
	}
	if len(starts) <= n {
		return content
	}
	lines := strings.Split(content, "\n")
	return strings.Join(lines[starts[len(starts)-n]:], "\n")
}
