package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/yingliang-zhang/odo/internal/ipc"
	"github.com/yingliang-zhang/odo/internal/store"
)

// Model-side rehydrate (epoch-fold root fix): `odo journal` is the
// first-class way for a coding agent to pull back journaled events that a
// distill folded out of view — the raw record behind a wiki note's lossy
// summary. Like `odo wiki read` (M6 §9), the transport is the agent's own
// shell — no daemon, no socket; the journal is opened READ-ONLY (query_only)
// so a live daemon's ownership is never disturbed.
//
// Subcommands (the windowed reads default to the "main" workstream's
// active conversation — --workstream <name> selects another; search
// always spans every active workstream in the project):
//
//	odo journal folded   — the window the latest distill folded out of view
//	odo journal range A [B] — events with seq in [A, B] (B optional: to end)
//	odo journal tail N   — the conversation's last N events
//	odo journal search <terms> — keyword hits across active workstreams
//
// Output is one JSON object per line (stdout), same shape poll_events
// serves; a human summary of the resolved window goes to stderr. The
// correct agent response to "the summary lost a detail" is
// `odo journal search <terms>` to locate the seq window, then
// `odo journal folded` (or `range`) to pull it — never guessing from
// the note.

// journalUsage is printed on invocation errors (exit 2).
const journalUsage = `usage: odo journal <subcommand> [--workstream <name>]
  folded          events folded by the latest distill (window from its marker)
  range A [B]     events with seq in [A, B]; B omitted reads to the end
  tail N          the conversation's last N events
  search <terms>  keyword search across ALL active workstreams, newest first
                  [--limit N] (default 20; % and _ are LIKE wildcards)`

// runJournalCLI dispatches `odo journal <sub>`.
func runJournalCLI(args []string) int {
	workstream := "main"
	limit := 20 // search only; capped again by the store's default when <= 0
	var positional []string
	for i := 0; i < len(args); i++ {
		switch {
		case args[i] == "--workstream" && i+1 < len(args):
			workstream = args[i+1]
			i++
		case strings.HasPrefix(args[i], "--workstream="):
			workstream = strings.TrimPrefix(args[i], "--workstream=")
		case args[i] == "--limit":
			if i+1 >= len(args) {
				fmt.Fprintf(os.Stderr, "odo journal: --limit needs a value\n")
				return 2
			}
			n, err := strconv.Atoi(args[i+1])
			if err != nil || n < 1 {
				fmt.Fprintf(os.Stderr, "odo journal: --limit must be a positive integer, got %q\n", args[i+1])
				return 2
			}
			limit = n
			i++
		case strings.HasPrefix(args[i], "--limit="):
			n, err := strconv.Atoi(strings.TrimPrefix(args[i], "--limit="))
			if err != nil || n < 1 {
				fmt.Fprintf(os.Stderr, "odo journal: --limit must be a positive integer, got %q\n", args[i])
				return 2
			}
			limit = n
		default:
			positional = append(positional, args[i])
		}
	}
	if len(positional) == 0 || workstream == "" {
		fmt.Fprintln(os.Stderr, journalUsage)
		return 2
	}

	ctx := context.Background()
	sub := positional[0]
	rest := positional[1:]

	// search is conversation-independent (project-wide, cross-workstream):
	// resolve the store+project only, so it also works on workstreams with
	// no active conversation.
	if sub == "search" {
		if len(rest) == 0 {
			fmt.Fprintln(os.Stderr, journalUsage)
			return 2
		}
		jc, closeStore, err := journalStore(ctx)
		if err != nil {
			fmt.Fprintf(os.Stderr, "odo journal: %v\n", err)
			return 1
		}
		defer closeStore()
		return journalSearch(ctx, jc, strings.Join(rest, " "), limit)
	}

	conv, closeStore, err := journalConversation(ctx, workstream)
	if err != nil {
		fmt.Fprintf(os.Stderr, "odo journal: %v\n", err)
		return 1
	}
	defer closeStore()

	switch sub {
	case "folded":
		if len(rest) != 0 {
			fmt.Fprintln(os.Stderr, journalUsage)
			return 2
		}
		return journalFolded(ctx, conv)
	case "range":
		if len(rest) < 1 || len(rest) > 2 {
			fmt.Fprintln(os.Stderr, journalUsage)
			return 2
		}
		first, err1 := strconv.Atoi(rest[0])
		last := -1 // to end
		if len(rest) == 2 {
			var err2 error
			last, err2 = strconv.Atoi(rest[1])
			if err2 != nil {
				fmt.Fprintf(os.Stderr, "odo journal range: bad end seq %q\n", rest[1])
				return 2
			}
		}
		if err1 != nil || first < 1 || (last != -1 && last < first) {
			fmt.Fprintf(os.Stderr, "odo journal range: want 1 <= A <= B, got %v\n", rest)
			return 2
		}
		return journalRange(ctx, conv, first, last)
	case "tail":
		if len(rest) != 1 {
			fmt.Fprintln(os.Stderr, journalUsage)
			return 2
		}
		n, err := strconv.Atoi(rest[0])
		if err != nil || n < 1 {
			fmt.Fprintf(os.Stderr, "odo journal tail: count must be a positive integer, got %q\n", rest[0])
			return 2
		}
		return journalTail(ctx, conv, n)
	default:
		fmt.Fprintln(os.Stderr, journalUsage)
		return 2
	}
}

// journalStore resolves the project root from the cwd, opens the journal
// read-only, and returns the project. closeStore releases the handle
// (non-nil only when err is nil).
func journalStore(ctx context.Context) (journalProj, func() error, error) {
	cwd, err := os.Getwd()
	if err != nil {
		return journalProj{}, nil, fmt.Errorf("resolve cwd: %w", err)
	}
	root, err := journalRoot(cwd)
	if err != nil {
		return journalProj{}, nil, err
	}
	st, err := store.OpenReadOnly(filepath.Join(root, ".odo", "journal.sqlite"))
	if err != nil {
		return journalProj{}, nil, err
	}
	p, err := st.GetProjectByRoot(ctx, root)
	if err != nil {
		st.Close()
		return journalProj{}, nil, fmt.Errorf("project %s not in journal: %w", root, err)
	}
	return journalProj{store: st, project: p}, st.Close, nil
}

// journalProj bundles a read-only store with the resolved project.
type journalProj struct {
	store   *store.Store
	project store.Project
}

// journalConversation resolves the workstream's active conversation on top
// of journalStore.
func journalConversation(ctx context.Context, workstreamName string) (journalConv, func() error, error) {
	jp, closeStore, err := journalStore(ctx)
	if err != nil {
		return journalConv{}, nil, err
	}
	st := jp.store
	w, err := st.GetWorkstreamByName(ctx, jp.project.ID, workstreamName)
	if err != nil {
		st.Close()
		return journalConv{}, nil, err
	}
	c, err := st.GetActiveConversation(ctx, w.ID)
	if err != nil {
		st.Close()
		return journalConv{}, nil, fmt.Errorf("no active conversation for workstream %q", workstreamName)
	}
	return journalConv{store: st, conversation: c}, closeStore, nil
}

// journalConv bundles the resolved conversation with its read-only store.
type journalConv struct {
	store        *store.Store
	conversation store.Conversation
}

// journalRoot resolves the project root holding .odo/journal.sqlite from a
// cwd. Two shapes: an agent's run cwd sits at <root>/.odo/worktrees/<id>
// (strip the state-dir suffix); anything else resolves to the nearest
// ancestor containing the journal.
func journalRoot(cwd string) (string, error) {
	clean := filepath.Clean(cwd)
	parts := strings.Split(clean, string(filepath.Separator))
	for i := len(parts) - 1; i >= 1; i-- {
		if parts[i] == "worktrees" && parts[i-1] == ".odo" {
			root := filepath.Join(parts[:i-1]...)
			if !filepath.IsAbs(root) {
				root = string(filepath.Separator) + root
			}
			if _, err := os.Stat(filepath.Join(root, ".odo", "journal.sqlite")); err == nil {
				return root, nil
			}
		}
	}
	for dir := clean; ; dir = filepath.Dir(dir) {
		if _, err := os.Stat(filepath.Join(dir, ".odo", "journal.sqlite")); err == nil {
			return dir, nil
		}
		if parent := filepath.Dir(dir); parent == dir {
			return "", fmt.Errorf("no .odo/journal.sqlite found from %s upward", cwd)
		}
	}
}

// journalFolded prints the events the latest distill folded out of view.
// The window comes from the marker's explicit first_seq/last_seq when
// journaled (the schema going forward); older markers fall back to the
// derived window (previous marker seq+1 … marker seq−1) — the two agree
// because per-conversation seqs are gap-free.
func journalFolded(ctx context.Context, conv journalConv) int {
	events, err := conv.store.ListEvents(ctx, conv.conversation.ID, 0)
	if err != nil {
		fmt.Fprintf(os.Stderr, "odo journal folded: %v\n", err)
		return 1
	}
	markerIdx, first, last := -1, 1, 0
	for i := len(events) - 1; i >= 0; i-- {
		if events[i].Type != store.EventReviewAction {
			continue
		}
		var p struct {
			Action   string `json:"action"`
			FirstSeq *int   `json:"first_seq"`
			LastSeq  *int   `json:"last_seq"`
		}
		if json.Unmarshal(events[i].Payload, &p) != nil || p.Action != "distill" {
			continue
		}
		markerIdx = i
		if p.FirstSeq != nil && p.LastSeq != nil {
			first, last = *p.FirstSeq, *p.LastSeq
		} else {
			first, last = ipc.FoldWindow(events[:i])
		}
		break
	}
	if markerIdx < 0 {
		fmt.Fprintln(os.Stderr, "odo journal folded: no distill marker — nothing folded yet")
		return 1
	}
	n := last - first + 1
	if n < 0 {
		n = 0
	}
	fmt.Fprintf(os.Stderr, "folded window: seq %d..%d (%d events) — conversation epoch %d\n",
		first, last, n, conv.conversation.Epoch)
	writeEvents(events, first, last)
	return 0
}

// journalRange prints events with seq in [first, last] (last < 0: to end).
func journalRange(ctx context.Context, conv journalConv, first, last int) int {
	events, err := conv.store.ListEvents(ctx, conv.conversation.ID, first-1)
	if err != nil {
		fmt.Fprintf(os.Stderr, "odo journal range: %v\n", err)
		return 1
	}
	writeEvents(events, first, last)
	return 0
}

// journalTail prints the conversation's last n events.
func journalTail(ctx context.Context, conv journalConv, n int) int {
	events, err := conv.store.ListEvents(ctx, conv.conversation.ID, 0)
	if err != nil {
		fmt.Fprintf(os.Stderr, "odo journal tail: %v\n", err)
		return 1
	}
	if len(events) > n {
		events = events[len(events)-n:]
	}
	first, last := 1, 0
	if len(events) > 0 {
		first, last = events[0].Seq, events[len(events)-1].Seq
	}
	writeEvents(events, first, last)
	return 0
}

// journalSearch prints keyword hits across every active workstream in the
// project, newest first — the remedy when the summary lost a detail and
// the seq window is unknown (search locates it, range pulls it).
func journalSearch(ctx context.Context, jp journalProj, query string, limit int) int {
	results, err := jp.store.SearchEvents(ctx, jp.project.ID, query, limit)
	if err != nil {
		fmt.Fprintf(os.Stderr, "odo journal search: %v\n", err)
		return 1
	}
	streams := map[string]bool{}
	for _, r := range results {
		streams[r.WorkstreamName] = true
	}
	fmt.Fprintf(os.Stderr, "%d match(es) for %q across %d active workstream(s), newest first — pull context with `odo journal range A B --workstream <name>`\n",
		len(results), query, len(streams))
	enc := json.NewEncoder(os.Stdout)
	for _, r := range results {
		if err := enc.Encode(r); err != nil {
			fmt.Fprintf(os.Stderr, "odo journal: write stdout: %v\n", err)
			return 1
		}
	}
	return 0
}

// writeEvents emits events with seq in [first, last] (last < 0: unbounded)
// as JSONL on stdout — one object per line, the store's verbatim shape.
func writeEvents(events []store.Event, first, last int) {
	enc := json.NewEncoder(os.Stdout)
	for _, e := range events {
		if e.Seq < first {
			continue
		}
		if last >= 0 && e.Seq > last {
			continue
		}
		if err := enc.Encode(e); err != nil {
			fmt.Fprintf(os.Stderr, "odo journal: write stdout: %v\n", err)
			return
		}
	}
}
