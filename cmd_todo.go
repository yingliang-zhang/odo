package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"

	"github.com/yingliang-zhang/odo/internal/ipc"
)

// M12 (D-todo): `odo todo` renders the conversation's durable plan
// (todo_merge journal state) read-only. Like `odo journal` (cmd_journal.go),
// the transport is the agent's own shell — no daemon, no socket; the journal
// is opened READ-ONLY (query_only) so a live daemon's ownership is never
// disturbed. There is no derived file to read by design (D-todo.2): the
// state re-materializes from the journal on every invocation via the same
// derivation the daemon's prompt injection uses
// (ipc.TodoStateFromEvents).
//
//	odo todo [--workstream <name>] [--all]
//
// Output is one JSON object per line (stdout): the visible plan items,
// open first. --all appends swept items (done/struck from earlier epochs —
// journaled forever, hidden by default). A human summary goes to stderr.

const todoUsage = `usage: odo todo [--workstream <name>] [--all]
  (default)      visible plan items: open first, then done/struck this epoch
  --all          also render swept items (done/struck from earlier epochs)
  Items print as JSONL with id/text/status/origin_seq/updated_seq/stale/swept.`

// runTodoCLI dispatches `odo todo`.
func runTodoCLI(args []string) int {
	workstream := "main"
	all := false
	// Index walk for the valued flag (mirrors cmd_journal.go's parsing).
	var rest []string
	for i := 0; i < len(args); i++ {
		switch {
		case args[i] == "--workstream" && i+1 < len(args):
			workstream = args[i+1]
			i++
		case strings.HasPrefix(args[i], "--workstream="):
			workstream = strings.TrimPrefix(args[i], "--workstream=")
		case args[i] == "--all":
			all = true
		default:
			rest = append(rest, args[i])
		}
	}
	if len(rest) != 0 || workstream == "" {
		fmt.Fprintln(os.Stderr, todoUsage)
		return 2
	}

	ctx := context.Background()
	conv, closeStore, err := journalConversation(ctx, workstream)
	if err != nil {
		fmt.Fprintf(os.Stderr, "odo todo: %v\n", err)
		return 1
	}
	defer closeStore()
	events, err := conv.store.ListEvents(ctx, conv.conversation.ID, 0)
	if err != nil {
		fmt.Fprintf(os.Stderr, "odo todo: %v\n", err)
		return 1
	}
	views := ipc.TodoStateFromEvents(events)
	// Default render set = the daemon's visible set (open first, then
	// done/struck this epoch); --all keeps swept items as a tail section.
	open, closed, swept := 0, 0, 0
	var lines []ipc.TodoViewItem
	var sweptLines []ipc.TodoViewItem
	for _, v := range views {
		switch {
		case v.Swept:
			swept++
			if all {
				sweptLines = append(sweptLines, v)
			}
		case v.Status == "open":
			open++
			lines = append(lines, v)
		default:
			closed++
			lines = append(lines, v)
		}
	}
	for _, group := range [][]ipc.TodoViewItem{lines, sweptLines} {
		for _, v := range group {
			b, err := json.Marshal(v)
			if err != nil {
				fmt.Fprintf(os.Stderr, "odo todo: marshal: %v\n", err)
				return 1
			}
			fmt.Println(string(b))
		}
	}
	hidden := ""
	if swept > 0 && !all {
		hidden = fmt.Sprintf(" · %d swept (--all shows)", swept)
	}
	fmt.Fprintf(os.Stderr, "plan: %d open, %d done/struck this epoch%s — conversation epoch %d (workstream %s)\n",
		open, closed, hidden, conv.conversation.Epoch, workstream)
	return 0
}
