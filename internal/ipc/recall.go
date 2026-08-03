package ipc

import (
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

// recallMemoryCap bounds the total recalled memory injected into a prompt.
// Wiki notes are distill summaries (small by design); the cap keeps a
// long-running project from overwhelming the agent's context window. Notes
// are included most-recent-first; the cut happens on a note boundary so no
// note is half-included.
const recallMemoryCap = 12 * 1024 // 12 KB ≈ 3k tokens

// wikiEpochRe parses the epoch number out of a wiki note name
// (<workstream>-epoch-<N>.md).
var wikiEpochRe = regexp.MustCompile(`-epoch-(\d+)\.md$`)

// wikiNoteEpoch extracts the epoch from a wiki note path. Callers skip
// unparseable names defensively.
func wikiNoteEpoch(path string) (int, bool) {
	m := wikiEpochRe.FindStringSubmatch(filepath.Base(path))
	if m == nil {
		return 0, false
	}
	n, err := strconv.Atoi(m[1])
	return n, err == nil
}

// recallWikiNotes reads all wiki/<workstreamName>-epoch-*.md files for the
// workstream, ordered newest-epoch first, concatenates them under headers,
// and truncates to recallMemoryCap on a note boundary. Returns the memory
// block ("" when no notes exist), the paths of the notes actually included
// (for journaling), and noteBytes — the exact block string injected per note
// (`## <basename>\n\n<content>\n\n---\n\n`) so the injection receipt can hash
// precisely what the prompt carried.
func recallWikiNotes(projectRoot, workstreamName string) (memory string, paths []string, noteBytes [][]byte) {
	matches, err := filepath.Glob(filepath.Join(projectRoot, "wiki", workstreamName+"-epoch-*.md"))
	if err != nil {
		return "", nil, nil
	}
	type note struct {
		path  string
		epoch int
	}
	notes := make([]note, 0, len(matches))
	for _, m := range matches {
		if epoch, ok := wikiNoteEpoch(m); ok {
			notes = append(notes, note{path: m, epoch: epoch})
		}
	}
	sort.Slice(notes, func(i, j int) bool { return notes[i].epoch > notes[j].epoch })

	var b strings.Builder
	for _, n := range notes {
		content, err := os.ReadFile(n.path)
		if err != nil {
			continue // note vanished between glob and read: skip it
		}
		block := "## " + filepath.Base(n.path) + "\n\n" + string(content) + "\n\n---\n\n"
		if b.Len()+len(block) > recallMemoryCap {
			break // cut on a note boundary: no note is half-included
		}
		b.WriteString(block)
		paths = append(paths, n.path)
		noteBytes = append(noteBytes, []byte(block))
	}
	if b.Len() == 0 {
		return "", nil, nil
	}
	return b.String(), paths, noteBytes
}

// userMemoryCap bounds the global user memory injected into every prompt.
// Durable principles are few by nature; the cap keeps steering small by
// design (ADR-0003).
const userMemoryCap = 4 * 1024 // 4 KB ≈ 1k tokens

// readUserMemory reads ~/.odo/user.md (global, user-maintained durable
// principles and preferences). Returns "" when the file is absent or empty.
// Content is capped at userMemoryCap with a line-boundary cut. M3 only
// reads this file; M4 adds the learner that writes it.
func readUserMemory() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	b, err := os.ReadFile(filepath.Join(home, ".odo", "user.md"))
	if err != nil {
		return ""
	}
	return capAtLineBoundary(string(b), userMemoryCap)
}

// capAtLineBoundary trims s to cap bytes, cutting at the last newline so no
// line is half-kept; returns "" when the content is blank or no complete
// line fits under the cap.
func capAtLineBoundary(s string, cap int) string {
	if strings.TrimSpace(s) == "" {
		return ""
	}
	if len(s) > cap {
		cut := strings.LastIndex(s[:cap], "\n")
		if cut < 0 {
			return "" // no complete line fits under the cap
		}
		s = strings.TrimRight(s[:cut], " \t\r\n")
	}
	return s
}
