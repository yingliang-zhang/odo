package ipc

// E1 (file access for /panel): the MoA models answer through a direct API
// with no tools, so every grounded question used to mean hand-pasting code
// into the prompt. This executor gives them three READ-ONLY tools —
// read_file, grep, glob — enforced daemon-side, no shell, no writes.
//
// Scope posture (user decision 2026-08-09): reads are allowed under the
// user's HOME directory, with app-content and secret dirs excluded by
// default. Both are prefs.md-configurable:
//
//	moa_fs_root: absolute or ~/ path of the allowed root (default: ~)
//	moa_fs_deny: comma-separated dirs to exclude, absolute or root-relative;
//	             extends the built-in deny list (a `-` prefix removes one
//	             entry — any entry, credentials included); absent, empty,
//	             or noise-only values keep the built-ins
//	             (default: Music, Pictures, Movies, .ssh, .aws, .gnupg,
//	              .netrc, .kube, .docker, .npmrc, .pypirc, .git-credentials,
//	              plus the 2026-08 SEC audit batch — see defaultFSDeny)
//
// Home covers most secrets-in-dotfiles reality; .ssh/.aws/.gnupg are
// hard-coded into the default deny because a /panel answer ships file
// contents to a third-party gateway — credential exfiltration via a helpful
// model is a bigger risk than read access itself. Reads bump hard caps and
// every executed call is journaled (PanelResult.ToolCalls).

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"github.com/yingliang-zhang/odo/internal/adapter"
	"github.com/yingliang-zhang/odo/internal/moa"
)

const (
	fsReadBytesCap   = 64 * 1024  // response body cap per read_file
	fsReadLinesCap   = 2000       // response line cap per read_file
	fsReadLineMax    = 1 << 20    // scanner buffer: skip ultra-long lines gracefully
	fsReadDefaultN   = 400        // default line limit
	fsGrepFileCap    = 512 * 1024 // larger files are not grep-scanned
	fsGrepScanCap    = 32 << 20   // total bytes scanned per grep call
	fsGrepMatchesCap = 200        // hard match cap (default 100)
	fsGrepLineCap    = 300        // matched line display cap
	fsGlobResultsCap = 500
)

// defaultFSDeny lists root-relative paths (dirs and credential files)
// excluded from every tool: Apple app-content stores (user decision) plus
// credential dirs and dotfiles whose contents ship to the model gateway.
// prefs moa_fs_deny extends this list (a -name token removes one entry);
// see parseFSDeny.
var defaultFSDeny = []string{
	"Music", "Pictures", "Movies",
	".ssh", ".aws", ".gnupg",
	".netrc", ".kube", ".docker", ".npmrc", ".pypirc",
	".git-credentials",
	// 2026-08 SEC audit batch: agent-config dirs/files, further
	// credential stores, language tooling caches, and editor swap
	// files whose contents must never ship to the model gateway.
	// (.kube already covered above.)
	".claude", "CLAUDE.md", "Makefile", ".cargo", ".rustup",
	".thunderbird", "trustdb.gpg", "ages", ".gnupg/private-keys-v1.d",
	"pip", "__pycache__", ".venv", "venv", "node_modules", "swap",
}

// parseFSDeny merges the raw moa_fs_deny prefs value into the deny list.
// The built-in defaults always apply (fail-closed): each bare token adds
// one exclusion, each "-name" token removes one default or previously
// added entry — any entry, credentials included (a recorded conscious
// operator act; ADR-0004). A name appearing both ways stays denied:
// contradiction resolves toward DENY, independent of token order. Dedup
// is case-insensitive, matching check()'s fold (macOS APFS resolves .SSH/
// and .ssh/ identically). An absent, empty, whitespace, or noise-only
// value (",,,", " - ") yields exactly the defaults — there is no syntax
// for an empty list. Result order: defaultFSDeny declared order, then
// additions in file order. The result is never nil.
func parseFSDeny(raw string) []string {
	added := map[string]bool{}   // lowercased names of bare tokens
	removed := map[string]bool{} // lowercased names of "-name" tokens
	var order []string           // bare tokens, first-seen file order
	for _, tok := range strings.Split(raw, ",") {
		tok = strings.TrimSpace(tok)
		if tok == "" {
			continue
		}
		if strings.HasPrefix(tok, "-") {
			if name := strings.TrimSpace(tok[1:]); name != "" {
				removed[strings.ToLower(name)] = true
			}
			continue
		}
		l := strings.ToLower(tok)
		if !added[l] {
			added[l] = true
			order = append(order, tok)
		}
	}
	// Contradiction resolves toward DENY: a name both added and removed
	// stays in the list, whichever token came first.
	for l := range added {
		delete(removed, l)
	}
	merged := make([]string, 0, len(defaultFSDeny)+len(order))
	seen := make(map[string]bool, len(defaultFSDeny)+len(order))
	emit := func(name string) {
		l := strings.ToLower(name)
		if !seen[l] && !removed[l] {
			seen[l] = true
			merged = append(merged, name)
		}
	}
	for _, d := range defaultFSDeny {
		emit(d)
	}
	for _, tok := range order {
		emit(tok)
	}
	return merged
}

// errWalkAbort is the sentinel that stops a capped walk early; the concrete
// cause rides on the executor-local state (match cap vs scan cap).
var errWalkAbort = errors.New("fstools: walk aborted at cap")

// fsToolExecutor resolves and runs the three read-only tools under one root.
type fsToolExecutor struct {
	root string   // resolved, symlink-evaluated allowed root
	home string   // for ~ expansion and display
	deny []string // resolved absolute prefixes excluded from every op
}

// newFSToolExecutor reads moa_fs_root / moa_fs_deny from prefs.md with the
// defaults above. A botched home resolution yields an executor whose every
// resolve fails closed.
func newFSToolExecutor() *fsToolExecutor {
	home, _ := os.UserHomeDir()
	root := adapter.LoadPrefsRaw("moa_fs_root")
	if root == "" {
		root = home
	}
	root = expandHomePath(root, home)
	if abs, err := filepath.Abs(root); err == nil {
		root = abs
	}
	if real, err := filepath.EvalSymlinks(root); err == nil {
		root = real
	}
	entries := parseFSDeny(adapter.LoadPrefsRaw("moa_fs_deny"))
	deny := make([]string, 0, len(entries))
	for _, d := range entries {
		d = expandHomePath(d, home)
		if !filepath.IsAbs(d) {
			d = filepath.Join(root, d)
		}
		deny = append(deny, filepath.Clean(d))
	}
	return &fsToolExecutor{root: root, home: home, deny: deny}
}

func expandHomePath(p, home string) string {
	if p == "~" {
		return home
	}
	if strings.HasPrefix(p, "~/") {
		return filepath.Join(home, p[2:])
	}
	return p
}

// display renders a path as ~/relative when it sits under home (compact in
// model-facing output and audits).
func (e *fsToolExecutor) display(p string) string {
	if e.home != "" {
		if p == e.home {
			return "~"
		}
		if strings.HasPrefix(p, e.home+string(filepath.Separator)) {
			return "~" + p[len(e.home):]
		}
	}
	return p
}

// check enforces the root boundary and the deny prefixes on a cleaned,
// absolute path. Matching is case-insensitive: macOS APFS/HFS+ resolve
// .SSH/ and .Netrc identically to .ssh/ and .netrc.
func (e *fsToolExecutor) check(p string) error {
	if e.root == "" {
		return fmt.Errorf("no allowed root (home dir unresolved)")
	}
	lp := strings.ToLower(p)
	if p != e.root && !strings.HasPrefix(lp, strings.ToLower(e.root)+string(filepath.Separator)) {
		return fmt.Errorf("path %s is outside the allowed root %s", e.display(p), e.display(e.root))
	}
	for _, d := range e.deny {
		ld := strings.ToLower(d)
		if lp == ld || strings.HasPrefix(lp, ld+string(filepath.Separator)) {
			return fmt.Errorf("path %s is excluded (moa_fs_deny)", e.display(p))
		}
	}
	return nil
}

// resolve turns a model-supplied path (absolute, ~/, or root-relative) into
// a checked absolute path. Symlinks are evaluated: a link inside the root
// cannot smuggle a target outside it or into a denied dir.
func (e *fsToolExecutor) resolve(p string) (string, error) {
	if p == "" || p == "." {
		return e.root, e.check(e.root)
	}
	p = expandHomePath(p, e.home)
	if !filepath.IsAbs(p) {
		p = filepath.Join(e.root, p)
	}
	clean := filepath.Clean(p)
	if err := e.check(clean); err != nil {
		return "", err
	}
	if real, err := filepath.EvalSymlinks(clean); err == nil && real != clean {
		if err := e.check(real); err != nil {
			return "", fmt.Errorf("symlink %s: %w", e.display(clean), err)
		}
		return real, nil
	}
	return clean, nil
}

// describeScope is the one-line scope notice for the /panel system prompt.
func (e *fsToolExecutor) describeScope() string {
	s := fmt.Sprintf("Root: %s.", e.display(e.root))
	if len(e.deny) > 0 {
		names := make([]string, 0, len(e.deny))
		for _, d := range e.deny {
			names = append(names, e.display(d))
		}
		s += " Excluded: " + strings.Join(names, ", ") + "."
	}
	return s
}

// moaFSTools defines the advertised tool schemas (Anthropic tools protocol).
func moaFSTools() []moa.Tool {
	return []moa.Tool{
		{
			Name: "read_file",
			Description: "Read a UTF-8 text file under the allowed root. Returns numbered lines " +
				"with a receipt footer. Refuses binaries and paths outside the root; caps at 64KB " +
				"or 2000 lines per call (use offset/limit to page).",
			InputSchema: map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"path":   map[string]interface{}{"type": "string", "description": "Absolute, ~/, or root-relative path"},
					"offset": map[string]interface{}{"type": "integer", "description": "1-based first line (default 1)"},
					"limit":  map[string]interface{}{"type": "integer", "description": "Max lines (default 400, cap 2000)"},
				},
				"required": []string{"path"},
			},
		},
		{
			Name: "grep",
			Description: "Regex search (RE2 syntax) over text files under the allowed root. " +
				"Returns 'path:line: text' rows. Files over 512KB and binaries are skipped; the " +
				"scan budget is 32MB per call (narrow the path for big trees).",
			InputSchema: map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"pattern":     map[string]interface{}{"type": "string", "description": "RE2 regular expression"},
					"path":        map[string]interface{}{"type": "string", "description": "Dir or file to search (default: root)"},
					"max_results": map[string]interface{}{"type": "integer", "description": "Match cap (default 100, max 200)"},
				},
				"required": []string{"pattern"},
			},
		},
		{
			Name: "glob",
			Description: "Match paths by glob relative to path (default: root). * and ? match " +
				"within one path segment; ** matches across segments. Returns up to 500 paths, " +
				"directories included.",
			InputSchema: map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"pattern": map[string]interface{}{"type": "string", "description": "Glob pattern, e.g. 'internal/**/*.go'"},
					"path":    map[string]interface{}{"type": "string", "description": "Base dir (default: root)"},
				},
				"required": []string{"pattern"},
			},
		},
	}
}

// Execute dispatches one tool call (moa.ToolExecutor). Failures are plain
// errors — the client loop turns them into is_error tool_results so the
// model sees and adapts instead of the whole /panel answer dying.
func (e *fsToolExecutor) Execute(ctx context.Context, call moa.ToolCall) (string, error) {
	switch call.Name {
	case "read_file":
		return e.readFile(call.Input)
	case "grep":
		return e.grep(ctx, call.Input)
	case "glob":
		return e.glob(ctx, call.Input)
	default:
		return "", fmt.Errorf("unknown tool %q (available: read_file, grep, glob)", call.Name)
	}
}

type readFileInput struct {
	Path   string `json:"path"`
	Offset int    `json:"offset"`
	Limit  int    `json:"limit"`
}

// readFile returns numbered lines [offset, offset+limit) with a receipt
// footer, honoring the byte/line caps. Reads stream, so multi-GB files
// can't alloc-check the daemon; a NUL byte in the first 8KB marks binary.
func (e *fsToolExecutor) readFile(raw json.RawMessage) (string, error) {
	var in readFileInput
	if err := json.Unmarshal(raw, &in); err != nil {
		return "", fmt.Errorf("read_file: bad input JSON: %v", err)
	}
	p, err := e.resolve(in.Path)
	if err != nil {
		return "", err
	}
	st, err := os.Stat(p)
	if err != nil {
		return "", fmt.Errorf("read_file: %s: %v", e.display(p), err)
	}
	if st.IsDir() {
		return "", fmt.Errorf("read_file: %s is a directory (use glob or grep)", e.display(p))
	}
	f, err := os.Open(p)
	if err != nil {
		return "", fmt.Errorf("read_file: %s: %v", e.display(p), err)
	}
	defer f.Close()

	probe := make([]byte, 8192)
	n, _ := f.Read(probe)
	if idx := bytes.IndexByte(probe[:n], 0); idx >= 0 {
		return "", fmt.Errorf("read_file: %s looks binary (NUL byte at offset %d) — not a text file", e.display(p), idx)
	}
	if _, err := f.Seek(0, 0); err != nil {
		return "", fmt.Errorf("read_file: %s: %v", e.display(p), err)
	}

	offset := in.Offset
	if offset < 1 {
		offset = 1
	}
	limit := in.Limit
	if limit <= 0 {
		limit = fsReadDefaultN
	}
	if limit > fsReadLinesCap {
		limit = fsReadLinesCap
	}

	var b strings.Builder
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, gioScanInit), fsReadLineMax)
	lineNo := 0
	last := 0
	written := 0
	cutBytes, cutLines := false, false
	for sc.Scan() {
		lineNo++
		if lineNo < offset {
			continue
		}
		if written >= limit {
			cutLines = true
			break
		}
		row := fmt.Sprintf("%d: %s\n", lineNo, sc.Text())
		if b.Len()+len(row) > fsReadBytesCap {
			cutBytes = true
			break
		}
		b.WriteString(row)
		last = lineNo
		written++
	}
	if last == 0 {
		if sc.Err() != nil {
			return "", fmt.Errorf("read_file: %s: %v", e.display(p), sc.Err())
		}
		return fmt.Sprintf("(no lines in range; file has %d lines, %d bytes)", lineNo, st.Size()), nil
	}
	var notes []string
	if cutBytes {
		notes = append(notes, fmt.Sprintf("truncated at %dKB cap", fsReadBytesCap/1024))
	}
	if cutLines {
		notes = append(notes, fmt.Sprintf("stopped at limit %d; more lines follow", limit))
	}
	footer := fmt.Sprintf("\n[read_file %s: lines %d–%d, %d bytes total", e.display(p), offset, last, st.Size())
	if len(notes) > 0 {
		footer += " — " + strings.Join(notes, "; ")
	}
	footer += "]"
	b.WriteString(footer)
	return b.String(), nil
}

// gioScanInit is the initial scanner buffer for read/grep line parsing.
const gioScanInit = 64 * 1024

type grepInput struct {
	Pattern    string `json:"pattern"`
	Path       string `json:"path"`
	MaxResults int    `json:"max_results"`
}

// grep walks the resolved target (file or dir tree), matching an RE2
// pattern per line. Guards: skip-list for heavy dirs (.git, node_modules),
// deny prefixes, per-file size cap, binary sniff, global scan and match
// caps — a /panel regex can never churn the whole home directory.
func (e *fsToolExecutor) grep(ctx context.Context, raw json.RawMessage) (string, error) {
	var in grepInput
	if err := json.Unmarshal(raw, &in); err != nil {
		return "", fmt.Errorf("grep: bad input JSON: %v", err)
	}
	if strings.TrimSpace(in.Pattern) == "" {
		return "", fmt.Errorf("grep: pattern is required")
	}
	re, err := regexp.Compile(in.Pattern)
	if err != nil {
		return "", fmt.Errorf("grep: bad pattern: %v", err)
	}
	base, err := e.resolve(in.Path)
	if err != nil {
		return "", err
	}
	max := in.MaxResults
	if max <= 0 {
		max = 100
	}
	if max > fsGrepMatchesCap {
		max = fsGrepMatchesCap
	}

	var rows []string
	var scanned int64
	matchCapped, scanCapped := false, false
	scanFile := func(path string, size int64) error {
		if size > fsGrepFileCap || scanned+size > fsGrepScanCap && scanned > 0 {
			if scanned+size > fsGrepScanCap {
				scanCapped = true
			}
			return nil // skip oversize/over-budget file, keep others
		}
		if ctx.Err() != nil {
			return ctx.Err()
		}
		f, err := os.Open(path)
		if err != nil {
			return nil // unreadable file: skip, don't sink the search
		}
		defer f.Close()
		scanned += size
		sc := bufio.NewScanner(f)
		sc.Buffer(make([]byte, gioScanInit), fsReadLineMax)
		lineNo := 0
		for sc.Scan() {
			lineNo++
			if len(rows) >= max {
				matchCapped = true
				return errWalkAbort
			}
			line := sc.Text()
			if strings.IndexByte(line, 0) >= 0 {
				return nil // binary: skip file
			}
			if re.MatchString(line) {
				if len(line) > fsGrepLineCap {
					// Rune-safe cut (same fix as recallQuery's seed): a raw
					// byte cut can split a CJK rune and emit invalid UTF-8.
					line = runeSafeCut(line, fsGrepLineCap) + "…"
				}
				rows = append(rows, fmt.Sprintf("%s:%d: %s", e.display(path), lineNo, line))
			}
		}
		return nil
	}

	st, err := os.Stat(base)
	if err != nil {
		return "", fmt.Errorf("grep: %s: %v", e.display(base), err)
	}
	if !st.IsDir() {
		if err := scanFile(base, st.Size()); err != nil && !errors.Is(err, errWalkAbort) {
			return "", err
		}
	} else {
		err = filepath.WalkDir(base, func(path string, d os.DirEntry, werr error) error {
			if werr != nil {
				return nil // unreadable entry: skip
			}
			if ctx.Err() != nil {
				return ctx.Err()
			}
			if d.IsDir() {
				if scanCapped {
					return filepath.SkipDir
				}
				name := d.Name()
				if name == ".git" || name == "node_modules" {
					return filepath.SkipDir
				}
				if path != base {
					if chk := e.check(path); chk != nil {
						return filepath.SkipDir // denied prefix, skipped silently
					}
				}
				return nil
			}
			if !d.Type().IsRegular() {
				return nil
			}
			info, err := d.Info()
			if err != nil {
				return nil
			}
			if scanCapped {
				return nil
			}
			return scanFile(path, info.Size())
		})
		if err != nil && !errors.Is(err, errWalkAbort) {
			return "", err
		}
	}

	var b strings.Builder
	if len(rows) == 0 {
		fmt.Fprintf(&b, "(no matches for /%s/ under %s)", in.Pattern, e.display(base))
	} else {
		b.WriteString(strings.Join(rows, "\n"))
	}
	var notes []string
	if matchCapped {
		notes = append(notes, fmt.Sprintf("stopped at %d matches", max))
	}
	if scanCapped {
		notes = append(notes, fmt.Sprintf("scan budget %dMB exhausted — narrow the path", fsGrepScanCap>>20))
	}
	footer := fmt.Sprintf("\n[grep /%s/ in %s: %d matches", in.Pattern, e.display(base), len(rows))
	if len(notes) > 0 {
		footer += " — " + strings.Join(notes, "; ")
	}
	footer += "]"
	b.WriteString(footer)
	return b.String(), nil
}

type globInput struct {
	Pattern string `json:"pattern"`
	Path    string `json:"path"`
}

// glob matches pattern segments against base-relative paths. * / ? come from
// filepath.Match semantics; ** spans any number of segments. Patterns given
// absolute (or ~/) are first reduced to root-relative when they sit under
// the scope root.
func (e *fsToolExecutor) glob(ctx context.Context, raw json.RawMessage) (string, error) {
	var in globInput
	if err := json.Unmarshal(raw, &in); err != nil {
		return "", fmt.Errorf("glob: bad input JSON: %v", err)
	}
	pat := strings.TrimSpace(in.Pattern)
	if pat == "" {
		return "", fmt.Errorf("glob: pattern is required")
	}
	base, err := e.resolve(in.Path)
	if err != nil {
		return "", err
	}
	if filepath.IsAbs(pat) || strings.HasPrefix(pat, "~/") {
		abs := filepath.Clean(expandHomePath(pat, e.home))
		rel, err := filepath.Rel(base, abs)
		if err != nil || strings.HasPrefix(rel, "..") {
			return "", fmt.Errorf("glob: absolute pattern %s does not resolve under %s", pat, e.display(base))
		}
		pat = rel
	}
	if !hasGlobMeta(pat) {
		// No wildcards: existence check, cheap path.
		full := filepath.Join(base, filepath.FromSlash(pat))
		if res, err := e.resolve(full); err == nil {
			if _, err := os.Stat(res); err == nil {
				return e.display(res), nil
			}
		} else {
			return "", err
		}
		return fmt.Sprintf("(no match for %q under %s)", pat, e.display(base)), nil
	}
	patSegs := strings.Split(filepath.ToSlash(pat), "/")

	var matches []string
	err = filepath.WalkDir(base, func(path string, d os.DirEntry, werr error) error {
		if werr != nil {
			return nil
		}
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if d.IsDir() {
			name := d.Name()
			if name == ".git" || name == "node_modules" {
				return filepath.SkipDir
			}
			if path != base {
				if chk := e.check(path); chk != nil {
					return filepath.SkipDir
				}
			}
		}
		rel, err := filepath.Rel(base, path)
		if err != nil || rel == "." {
			return nil
		}
		if !matchGlobSegments(patSegs, strings.Split(filepath.ToSlash(rel), "/")) {
			return nil
		}
		if len(matches) >= fsGlobResultsCap {
			return errWalkAbort
		}
		matches = append(matches, e.display(path))
		return nil
	})
	if err != nil && !errors.Is(err, errWalkAbort) {
		return "", err
	}
	if len(matches) == 0 {
		return fmt.Sprintf("(no matches for %q under %s)", pat, e.display(base)), nil
	}
	sort.Strings(matches)
	out := strings.Join(matches, "\n")
	footer := fmt.Sprintf("\n[glob %q in %s: %d matches", pat, e.display(base), len(matches))
	if len(matches) >= fsGlobResultsCap {
		footer += fmt.Sprintf(" — stopped at %d-result cap", fsGlobResultsCap)
	}
	footer += "]"
	return out + footer, nil
}

func hasGlobMeta(p string) bool {
	return strings.ContainsAny(p, "*?[")
}

// matchGlobSegments matches pattern segments against path segments; "**"
// spans zero or more path segments, single segments use filepath.Match.
func matchGlobSegments(pat, path []string) bool {
	if len(pat) == 0 {
		return len(path) == 0
	}
	if pat[0] == "**" {
		for i := 0; i <= len(path); i++ {
			if matchGlobSegments(pat[1:], path[i:]) {
				return true
			}
		}
		return false
	}
	if len(path) == 0 {
		return false
	}
	ok, err := filepath.Match(pat[0], path[0])
	if err != nil || !ok {
		return false
	}
	return matchGlobSegments(pat[1:], path[1:])
}
