// Package ipc — fix-INT W5 (Guardian risk taxonomy, design lock
// docs/design/fix-int-w5-risk-taxonomy-lock.md): a PURE-MECHANICAL risk
// classifier over the diff text — zero model spend, no single-model
// judge — whose receipt rides every receipt-eligible review_action row
// as three additive-optional keys (ADR-0002 immune: absence means
// pre-W5, older readers ignore them):
//
//	risk_class      []string, severity-ranked multi-label,
//	                ["none"] when rated clean (explicit: distinguishes
//	                rated-clean from pre-W5 unrated rows)
//	risk_evidence   map[class]→one trigger artifact; omitted when
//	                ["none"] or the patch is unreadable
//	risk_classifier constant "mechanical" this wave
//
// When the patch file is unreadable: all three keys omitted (the
// patch_sha16 precedent — a row about an unreadable patch simply
// attests less).
//
// Risk classes (snake_case, severity rank = leak-cost order; element 0
// is the primary class for single-string consumers):
//
//	credential_probe     added code reads secret-shaped material: env
//	                     *_KEY/_TOKEN/_SECRET/_PASSWORD via a getenv-
//	                     shaped call, ~/.ssh/id_*, .aws/credentials,
//	                     .gnupg, keychain
//	data_exfil           one hunk co-adds a local-source read (os.
//	                     ReadFile/open/readFileSync/…) AND a network
//	                     egress (http(s)/fetch/post/curl/…)
//	destructive          DeletedFile in the patch, or rm -rf/
//	                     RemoveAll/rmtree/DROP TABLE/push --force/
//	                     reset --hard in added lines
//	security_weakening   an ADDED line weakens a control (Insecure-
//	                     SkipVerify, --insecure, //nosec, chmod 777/
//	                     666, CORS *, auth-disable) — removing one is
//	                     an improvement, never a risk
//	supply_chain         touches a basename in autoLandSupplyChain-
//	                     Files (SSOT — NO second list)
//	none                 no hit (explicit rated-clean marker)
//
// Relation to C0–C3 (autonomy.go): orthogonal, not a superclass.
// C-classes measure automaticability (diff shape); risk classes
// measure hazard (content intent). They cross-tab: a C3 small in-scope
// diff reading .env is credential_probe. The ratchet that will gate on
// these tallies is a later wave — W5 is observational only (M15 rung-0
// instrument-before-gate precedent).
//
// Write sites (4 + 1): journalAutoLandBlocked (autoland.go), the auto
// moa_review (autoland.go), manual moa_review (server.go
// handleReviewDiff), accept/reject (server.go handleDiffAction), and
// auto_revise_round (settle.go — the round's own diff).

package ipc

import (
	"fmt"
	"os"
	"regexp"
	"strings"
)

// riskClassOrder is the severity rank (leak-cost order), including the
// explicit "none" floor. classifyRisk emits classes in this order and
// ComputeAutonomy renders its risk table in it.
var riskClassOrder = []string{
	"credential_probe",
	"data_exfil",
	"destructive",
	"security_weakening",
	"supply_chain",
	"none",
}

// riskClassifierLabel is the constant classifier provenance this wave.
const riskClassifierLabel = "mechanical"

// riskEnvSecretRe matches an UPPER_SNAKE env name ending in a
// secret-shape suffix (AWS_SECRET_ACCESS_KEY, OPENAI_API_KEY, MY_TOKEN,
// DB_PASSWORD, …) — word-bounded so PLAIN_KEYWORD or MONKEY never hit.
var riskEnvSecretRe = regexp.MustCompile(`[A-Z][A-Z0-9_]*(_KEY|_TOKEN|_SECRET|_PASSWORD)([^A-Za-z0-9_]|$)`)

// riskEnvReadTokens are getenv-shaped env-read calls. A credential
// probe pairs one of these with a secret-shaped name on the same added
// line — a constant FOO_KEY declaration alone never trips.
var riskEnvReadTokens = []string{
	"os.Getenv(", "os.LookupEnv(", "getenv(",
	"process.env.", "process.env[",
	"os.environ[", "os.environ.get(",
	"ENV[", "System.getenv(",
}

// riskSecretPathTokens are secret-material path/artifact literals
// (added lines, case-insensitive).
var riskSecretPathTokens = []string{
	".ssh/id_", ".aws/credentials", ".gnupg", "keychain",
}

// riskLocalReadTokens are local-source reads the data_exfil rule pairs
// with an egress inside the same hunk.
var riskLocalReadTokens = []string{
	"os.ReadFile(", "ioutil.ReadFile(", "os.Open(",
	"readFileSync(", "fs.readFile(",
	"open(", ".read_text()", ".read_bytes()",
}

// riskEgressTokens are network-egress shapes the data_exfil rule pairs
// with a local read inside the same hunk.
var riskEgressTokens = []string{
	"http://", "https://", "fetch(",
	"http.Get(", "http.Post(", "http.NewRequest",
	".post(", ".Post(", "axios.",
	"curl ", "wget ", "urllib",
	"requests.get(", "requests.post(",
	"websocket", "WebSocket", "smtp.", "Dial(",
}

// riskWeakeningTokens: an ADDED line containing one of these weakens a
// security control. CORS/auth-disable are two-signal rules below. a
// comment shaped trigger (a doc mentioning "chmod 777") must NOT fire,
// so these are checked AFTER the comment filter — except //nosec, which
// is checked before it on purpose (a comment directive that exempts
// code from gosec IS the weakening). Value-agnostic by design (the
// mechanical bar): "InsecureSkipVerify: false" in an added line still
// trips — parsing per-language value syntax is a model's job; the rare
// false positive is an audit annotation, never a gate (observational
// wave).
var riskWeakeningTokens = []string{
	"InsecureSkipVerify", "--insecure", "chmod 777", "chmod 666",
}

// riskDestructiveTokens are destructive-command shapes in added lines
// (a DeletedFile in the patch stats is the other, separate trigger).
var riskDestructiveTokens = []string{
	"rm -rf", "rm -fr", ".RemoveAll(", "rmtree(", "shutil.rmtree",
	"push --force", "push -f", "reset --hard",
}

// riskCommentPrefixes: trimmed added content starting with one of these
// is a comment — it does not execute, so content patterns skip it
// (//nosec is checked before this filter, deliberately).
var riskCommentPrefixes = []string{"//", "#", "*", "\"\"\""}

// riskEvidenceCap keeps one receipt artifact to a journal-reviewable
// size (the lock's example artifact is 30-ish chars; 120 is generous).
const riskEvidenceCap = 120

// riskScan accumulates first-trigger evidence while walking one unified
// diff. hits maps class → its first trigger artifact; per-hunk state
// feeds the data_exfil co-presence rule.
type riskScan struct {
	hits     map[string]string
	path     string // current file (post-image side)
	hunkLine int    // current new-file line number (0 = outside hunks)

	pendDel bool // deleted file mode seen, awaiting its --- a/ path

	// data_exfil per-hunk co-presence (first artifacts only).
	hRead, hEgress string
	hEgressPath    string
	hEgressLine    int
}

// classifyRisk walks diffText and returns the severity-ranked risk
// classes plus one trigger artifact per class. Empty/clean diff →
// classes ["none"], evidence nil (the receipt omits risk_evidence).
// Pure: patch text in, classes + evidence out.
func classifyRisk(diffText string) (classes []string, evidence map[string]string) {
	s := &riskScan{hits: map[string]string{}}
	for _, line := range strings.Split(diffText, "\n") {
		s.scanLine(line)
	}
	s.flushHunk()

	for _, class := range riskClassOrder[:len(riskClassOrder)-1] { // "none" is the floor, never a hit
		if _, ok := s.hits[class]; ok {
			classes = append(classes, class)
		}
	}
	if len(classes) == 0 {
		return []string{"none"}, nil
	}
	evidence = make(map[string]string, len(classes))
	for _, class := range classes {
		evidence[class] = s.hits[class]
	}
	return classes, evidence
}

// scanLine consumes one diff line: file/hunk bookkeeping first, then
// the content rules (added lines only — removing a weakening or
// destructive shape is an improvement, never a risk).
func (s *riskScan) scanLine(line string) {
	switch {
	case strings.HasPrefix(line, "diff --git "):
		s.flushHunk()
		s.path, s.hunkLine, s.pendDel = "", 0, false
		// Mode/rename-only changes carry no --- / +++ lines — the header
		// is the only path evidence (a mode-only lockfile chmod is still
		// a supply-chain touch).
		fields := strings.Fields(strings.TrimPrefix(line, "diff --git "))
		for _, f := range fields {
			s.checkSupplyChain(f)
		}
		return
	case strings.HasPrefix(line, "deleted file mode"):
		s.pendDel = true
		return
	case strings.HasPrefix(line, "--- "):
		p := riskDiffPath(strings.TrimPrefix(line, "--- "), "a/")
		if s.pendDel && p != "" {
			s.record("destructive", p+" (file deleted)")
			s.pendDel = false
		}
		s.checkSupplyChain(p)
		return
	case strings.HasPrefix(line, "+++ "):
		if p := riskDiffPath(strings.TrimPrefix(line, "+++ "), "b/"); p != "" {
			// Post-image name wins (renames): the change is reviewed
			// under the new path (PatchStats precedent).
			s.path = p
			s.checkSupplyChain(p)
		}
		return
	case strings.HasPrefix(line, "@@ "):
		s.flushHunk()
		s.hunkLine = riskHunkNewStart(line)
		return
	}

	h, ok := lineHunkContent(line)
	if !ok {
		return
	}
	if !h.added {
		// Context lines advance the new-file counter; removed lines do
		// not (a - line has no new-file position).
		if !h.removed && s.hunkLine > 0 {
			s.hunkLine++
		}
		return
	}
	s.checkAddedContent(h.content)
	if s.hunkLine > 0 {
		s.hunkLine++
	}
}

// checkAddedContent applies every added-line content rule at the
// scanner's current file:line position.
func (s *riskScan) checkAddedContent(content string) {
	at := s.evidenceAt()

	// //nosec before the comment filter: it IS a comment, and it
	// exempts code from gosec — exactly the weakening this wave names.
	if strings.Contains(content, "//nosec") {
		s.record("security_weakening", snippet(content)+at)
	}
	if riskIsComment(content) {
		return
	}

	// credential_probe: getenv-shaped call + secret-shaped name, or a
	// secret-material path/artifact literal.
	if riskEnvSecretRe.MatchString(content) {
		for _, tok := range riskEnvReadTokens {
			if strings.Contains(content, tok) {
				s.record("credential_probe", snippet(content)+at)
				break
			}
		}
	}
	lower := strings.ToLower(content)
	for _, tok := range riskSecretPathTokens {
		if strings.Contains(lower, tok) {
			s.record("credential_probe", snippet(content)+at)
			break
		}
	}

	// data_exfil: accumulate this hunk's first read/egress artifacts.
	if s.hRead == "" {
		for _, tok := range riskLocalReadTokens {
			if strings.Contains(content, tok) {
				s.hRead = snippet(content)
				break
			}
		}
	}
	if s.hEgress == "" {
		for _, tok := range riskEgressTokens {
			if strings.Contains(content, tok) {
				s.hEgress = snippet(content)
				s.hEgressPath, s.hEgressLine = s.path, s.hunkLine
				break
			}
		}
	}

	// security_weakening (single tokens; two-signal shapes below).
	for _, tok := range riskWeakeningTokens {
		if strings.Contains(content, tok) {
			s.record("security_weakening", snippet(content)+at)
			break
		}
	}
	if riskCorsWildcard(lower) || riskAuthDisable(lower) {
		s.record("security_weakening", snippet(content)+at)
	}

	// destructive (command shapes; file deletion recorded at the header).
	for _, tok := range riskDestructiveTokens {
		if strings.Contains(content, tok) {
			s.record("destructive", snippet(content)+at)
			break
		}
	}
	if strings.Contains(strings.ToUpper(content), "DROP TABLE") {
		s.record("destructive", snippet(content)+at)
	}
}

// flushHunk folds the data_exfil co-presence rule: one hunk that
// co-adds a local-source read AND a network egress is the mechanical
// signature of read-local → send-remote.
func (s *riskScan) flushHunk() {
	if s.hRead != "" && s.hEgress != "" {
		ev := s.hRead + " → " + s.hEgress
		if s.hEgressPath != "" && s.hEgressLine > 0 {
			ev += fmt.Sprintf(" @%s:%d", s.hEgressPath, s.hEgressLine)
		}
		s.record("data_exfil", capSnippet(ev))
	}
	s.hRead, s.hEgress = "", ""
	s.hEgressPath, s.hEgressLine = "", 0
}

// checkSupplyChain: a touched basename in autoLandSupplyChainFiles.
// SSOT — the gate and the classifier read ONE map; there is no second
// list to drift. a/, b/ prefixes handled ('diff --git' header tokens
// arrive un-stripped, --- / +++ paths already parsed).
func (s *riskScan) checkSupplyChain(p string) {
	if rest, ok := strings.CutPrefix(p, "a/"); ok {
		p = rest
	} else if rest, ok := strings.CutPrefix(p, "b/"); ok {
		p = rest
	}
	if p == "" || p == "/dev/null" {
		return
	}
	base := strings.ToLower(p[strings.LastIndex(p, "/")+1:])
	if autoLandSupplyChainFiles[base] {
		s.record("supply_chain", p+" (supply-chain manifest/lockfile)")
	}
}

// record keeps the FIRST trigger artifact per class (deterministic —
// the artifact an auditor sees is the one the diff presents first).
func (s *riskScan) record(class, ev string) {
	if _, ok := s.hits[class]; !ok {
		s.hits[class] = ev
	}
}

// evidenceAt formats the " @path:line" suffix for the current added
// line ("" when outside a file/hunk — a malformed diff still gets the
// line content itself as the artifact).
func (s *riskScan) evidenceAt() string {
	if s.path != "" && s.hunkLine > 0 {
		return fmt.Sprintf(" @%s:%d", s.path, s.hunkLine)
	}
	return ""
}

// riskDiffPath parses one --- / +++ header path, trimming the a/ b/
// prefix and C-quoting (the PatchStats convention). /dev/null → "".
func riskDiffPath(raw, prefix string) string {
	p := strings.TrimSpace(raw)
	if p == "/dev/null" {
		return ""
	}
	if strings.HasPrefix(p, "\"") && strings.HasSuffix(p, "\"") && len(p) >= 2 {
		p = p[1 : len(p)-1]
	}
	if rest, ok := strings.CutPrefix(p, prefix); ok {
		return rest
	}
	return p
}

// riskHunkNewStart extracts the new-file start line from an @@ header
// ("@@ -a[,b] +c[,d] @@" → c). 0 on any parse failure (evidence then
// carries no position rather than a wrong one).
func riskHunkNewStart(header string) int {
	idx := strings.Index(header, "+")
	if idx < 0 {
		return 0
	}
	n := 0
	if _, err := fmt.Sscanf(header[idx:], "+%d", &n); err != nil {
		return 0
	}
	return n
}

type hunkContent struct {
	content string
	added   bool
	removed bool
}

// lineHunkContent decodes a hunk body line (+ / - / context). File
// headers (--- / +++) never reach here — the caller consumed them.
func lineHunkContent(line string) (hunkContent, bool) {
	if line == "" {
		return hunkContent{}, false
	}
	switch line[0] {
	case '+':
		return hunkContent{content: line[1:], added: true}, true
	case '-':
		return hunkContent{content: line[1:], removed: true}, true
	case ' ':
		return hunkContent{content: line[1:]}, true
	case '\\': // "\ No newline at end of file"
		return hunkContent{}, false
	}
	return hunkContent{}, false
}

// riskIsComment: a trimmed added line that is a comment in the repo's
// usual syntaxes. Comment content does not execute — no risk trigger
// (//nosec is checked before this filter, deliberately).
func riskIsComment(content string) bool {
	t := strings.TrimSpace(content)
	for _, p := range riskCommentPrefixes {
		if strings.HasPrefix(t, p) {
			return true
		}
	}
	return false
}

// riskCorsWildcard: a CORS-allow-origin knob set to * on one added line.
func riskCorsWildcard(lower string) bool {
	if !strings.Contains(lower, "*") {
		return false
	}
	for _, tok := range []string{
		"access-control-allow-origin", "alloworigin", "allow_origin",
		"allowedorigin", "allowed_origin",
	} {
		if strings.Contains(lower, tok) {
			return true
		}
	}
	return false
}

// riskAuthDisable: an auth-disable shape on one added line.
func riskAuthDisable(lower string) bool {
	for _, tok := range []string{
		"--no-auth", "--disable-auth", "disable_auth", "disableauth",
		"auth: false", "auth=false", "auth = false", "no_auth: true",
	} {
		if strings.Contains(lower, tok) {
			return true
		}
	}
	return false
}

// snippet trims and caps one added line for evidence (the lock's
// artifact shape: os.Getenv("AWS_SECRET_ACCESS_KEY") @sse.go:18).
func snippet(content string) string {
	t := strings.TrimSpace(content)
	if len(t) > 60 {
		t = t[:60] + "…"
	}
	return t
}

// capSnippet caps a pre-assembled evidence string (data_exfil's
// read → egress composite) at the receipt cap.
func capSnippet(s string) string {
	if len(s) > riskEvidenceCap {
		return s[:riskEvidenceCap] + "…"
	}
	return s
}

// riskReceiptKeys builds the W5 journal keys for diff text already in
// hand. risk_class is always present (["none"] when clean);
// risk_evidence is omitted when clean.
func riskReceiptKeys(diffText string) map[string]interface{} {
	classes, evidence := classifyRisk(diffText)
	out := map[string]interface{}{
		"risk_class":      classes,
		"risk_classifier": riskClassifierLabel,
	}
	if len(evidence) > 0 {
		out["risk_evidence"] = evidence
	}
	return out
}

// riskReceipt builds the W5 keys for the patch file at pathOnDisk. An
// unreadable patch → empty map: the caller attests less (all three keys
// simply absent — the patch_sha16 precedent, never a fabricated rating).
func riskReceipt(pathOnDisk string) map[string]interface{} {
	data, err := os.ReadFile(pathOnDisk)
	if err != nil {
		return map[string]interface{}{}
	}
	return riskReceiptKeys(string(data))
}

// mountRiskReceipt merges a W5 risk receipt into an outgoing journal
// payload. An empty receipt (unreadable patch) merges nothing.
func mountRiskReceipt(payload map[string]interface{}, receipt map[string]interface{}) {
	for k, v := range receipt {
		payload[k] = v
	}
}
