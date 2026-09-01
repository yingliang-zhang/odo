package ipc

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// UX-2 (D5 Stage 0 / A2-1..3): the k8s_status handler shells out to
// `kubectl get jobs,pods -o json`, so tests install a shell script named
// "kubectl" in a temp PATH dir — the installMockOmp pattern. The script
// records its argv (one line per call) into an args file whose path is
// baked in at install time, so --context/--selector passthroughs and the
// NO-exec cases (off / bad_namespace) are assertable.

// installMockKubectl writes a mock kubectl into a temp dir and PREPENDS
// that dir to PATH. behavior: "ok" → print listJSON; "fail" → print
// failText to stderr and exit 1; "sleep" → hang past any test timeout.
// listJSON is kubectl's kind:List wire blob. Returns the args log path.
func installMockKubectl(t *testing.T, behavior, listJSON, failText string) string {
	t.Helper()
	dir := t.TempDir()
	argsLog := filepath.Join(dir, "args.log")

	var body string
	switch behavior {
	case "ok":
		body = "cat <<'KUBECTL_JSON'\n" + listJSON + "\nKUBECTL_JSON\n"
	case "fail":
		body = "echo '" + failText + "' >&2\nexit 1\n"
	case "sleep":
		body = "sleep 30\n"
	default:
		t.Fatalf("unknown mock behavior %q", behavior)
	}
	script := "#!/bin/sh\necho \"$@\" >> '" + argsLog + "'\n" + body
	path := filepath.Join(dir, "kubectl")
	if err := os.WriteFile(path, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	// Prepend only (the installMockOmp precedent): the mock wins on PATH
	// order, while /bin stays reachable for the script's own cat/echo.
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	return argsLog
}

// A4 (multi-ns): installMockKubectl holds ONE behavior for every leg,
// but the parallel fan needs per-namespace outcomes (partial failure,
// per-ns payloads). installMockKubectlDispatch writes a kubectl that
// dispatches on argv substrings — each rule is a (contains, body) pair
// checked in order; unexpected argv fails loudly on stderr so a shape
// drift never reads as a passing "ok". Rule bodies reuse the same three
// postures (ok JSON / fail text / sleep) via the builders below.
type mockKubectlRule struct {
	contains string
	body     string
}

func mockOkBody(listJSON string) string {
	return "cat <<'KUBECTL_JSON'\n" + listJSON + "\nKUBECTL_JSON\n"
}

func mockFailBody(failText string) string {
	return "echo '" + failText + "' >&2\nexit 1\n"
}

// exec-sleep: the case-arm structure prevents sh's usual "exec the last
// command" optimization, so a bare `sleep 30` would survive the deadline
// SIGKILL as an orphan holding the stderr pipe open — cmd.Wait would then
// block until sleep exits (~30s). `exec sleep` REPLACES the shell image,
// so the deadline kill takes the sleeper directly (the linear
// installMockKubectl script gets this via sh's trailing-exec optimization).
const mockSleepBody = "exec sleep 30\n"

func installMockKubectlDispatch(t *testing.T, rules []mockKubectlRule) string {
	t.Helper()
	dir := t.TempDir()
	argsLog := filepath.Join(dir, "args.log")
	var sb strings.Builder
	sb.WriteString("#!/bin/sh\necho \"$@\" >> '" + argsLog + "'\ncase \"$*\" in\n")
	for _, r := range rules {
		sb.WriteString("  *\"" + r.contains + "\"*)\n" + r.body + "    ;;\n")
	}
	sb.WriteString("  *)\n    echo 'unexpected argv' >&2\n    exit 1\nesac\n")
	path := filepath.Join(dir, "kubectl")
	if err := os.WriteFile(path, []byte(sb.String()), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	return argsLog
}

// stripRealKubectlOffPATH swaps PATH for one empty temp dir: LookPath
// misses, no exec possible. Returns nothing; asserts happen in callers.
func stripRealKubectlOffPATH(t *testing.T) {
	t.Helper()
	t.Setenv("PATH", t.TempDir())
}

func consumeK8sStatus(t *testing.T, rig *testRig, root string) (K8sStatus, []json.RawMessage, []json.RawMessage) {
	t.Helper()
	resp := rig.call(t, Request{Cmd: CmdK8sStatus, ProjectRoot: root})
	if resp.K8sStatus == nil {
		t.Fatal("k8s_status: response K8sStatus is nil")
	}
	var jobs, pods []json.RawMessage
	if resp.K8sStatus.Jobs != nil {
		if err := json.Unmarshal(resp.K8sStatus.Jobs, &jobs); err != nil {
			t.Fatalf("k8s_status: jobs blob not an array: %v", err)
		}
	}
	if resp.K8sStatus.Pods != nil {
		if err := json.Unmarshal(resp.K8sStatus.Pods, &pods); err != nil {
			t.Fatalf("k8s_status: pods blob not an array: %v", err)
		}
	}
	return *resp.K8sStatus, jobs, pods
}

// kubectlList builds a kubectl `get jobs,pods -o json` kind:List with n
// jobs followed by m pods (kind-peeked fields only).
func kubectlList(nJobs, nPods int) string {
	return kubectlListNs("", nJobs, nPods)
}

// kubectlListNs is kubectlList with metadata.namespace stamped on every
// item — A4: kubectl items self-identify, so the flat merge stays
// ns-attributable and the GUI groups by it without daemon interpretation.
func kubectlListNs(ns string, nJobs, nPods int) string {
	nsField := ""
	if ns != "" {
		nsField = fmt.Sprintf(`,"namespace":%q`, ns)
	}
	items := make([]string, 0, nJobs+nPods)
	for i := 0; i < nJobs; i++ {
		items = append(items, fmt.Sprintf(`{"kind":"Job","metadata":{"name":"job-%d"%s,"creationTimestamp":"2026-09-01T00:00:00Z"},"status":{}}`, i, nsField))
	}
	for i := 0; i < nPods; i++ {
		items = append(items, fmt.Sprintf(`{"kind":"Pod","metadata":{"name":"pod-%d"%s},"status":{"phase":"Running"}}`, i, nsField))
	}
	return `{"kind":"List","items":[` + strings.Join(items, ",") + `]}`
}

func readArgsLog(t *testing.T, path string) []string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil // kubectl never ran — the assertable negative
		}
		t.Fatal(err)
	}
	trimmed := strings.TrimSpace(string(data))
	if trimmed == "" {
		return nil
	}
	return strings.Split(trimmed, "\n")
}

func TestK8sStatusOffWhenNamespaceUnset(t *testing.T) {
	root := initRepo(t)
	home := t.TempDir()
	t.Setenv("HOME", home)
	writePrefs(t, home, "") // no k8s_namespace line at all
	argsLog := installMockKubectl(t, "ok", kubectlList(1, 1), "")

	rig := startRig(t, root)
	defer rig.stop(t)

	st, _, _ := consumeK8sStatus(t, rig, root)
	if st.Available {
		t.Fatal("k8s_status: available with no namespace configured")
	}
	if st.Reason != "off" {
		t.Fatalf("k8s_status: reason = %q, want off", st.Reason)
	}
	// A2-1: off-by-config never execs — "no chip, no tab, no polling".
	if got := readArgsLog(t, argsLog); len(got) != 0 {
		t.Fatalf("k8s_status: kubectl ran while off: %v", got)
	}
}

func TestK8sStatusBadNamespaceRejectedBeforeExec(t *testing.T) {
	root := initRepo(t)
	home := t.TempDir()
	t.Setenv("HOME", home)
	writePrefs(t, home, "k8s_namespace: Bad_NS!")
	argsLog := installMockKubectl(t, "ok", kubectlList(1, 1), "")

	rig := startRig(t, root)
	defer rig.stop(t)

	st, _, _ := consumeK8sStatus(t, rig, root)
	if st.Reason != "bad_namespace" {
		t.Fatalf("k8s_status: reason = %q, want bad_namespace", st.Reason)
	}
	if got := readArgsLog(t, argsLog); len(got) != 0 {
		t.Fatalf("k8s_status: exec'd an invalid namespace: %v", got)
	}
}

func TestK8sStatusKubectlMissing(t *testing.T) {
	root := initRepo(t)
	home := t.TempDir()
	t.Setenv("HOME", home)
	writePrefs(t, home, "k8s_namespace: lab")
	stripRealKubectlOffPATH(t)

	rig := startRig(t, root)
	defer rig.stop(t)

	st, _, _ := consumeK8sStatus(t, rig, root)
	if st.Available {
		t.Fatal("k8s_status: available without kubectl")
	}
	if st.Reason != "ENOENT" {
		t.Fatalf("k8s_status: reason = %q, want ENOENT", st.Reason)
	}
}

func TestK8sStatusOKSplitsKinds(t *testing.T) {
	root := initRepo(t)
	home := t.TempDir()
	t.Setenv("HOME", home)
	writePrefs(t, home, "k8s_namespace: lab")
	installMockKubectl(t, "ok", kubectlList(2, 1), "")

	rig := startRig(t, root)
	defer rig.stop(t)

	st, jobs, pods := consumeK8sStatus(t, rig, root)
	if !st.Available {
		t.Fatalf("k8s_status: unavailable: %q", st.Reason)
	}
	if len(jobs) != 2 || len(pods) != 1 {
		t.Fatalf("k8s_status: got %d jobs %d pods, want 2/1", len(jobs), len(pods))
	}
	if st.Truncated {
		t.Fatal("k8s_status: truncated on 2 jobs")
	}
	if st.FetchedUnix <= 0 {
		t.Fatal("k8s_status: fetched_unix not stamped")
	}
	// Swap-friendly passthrough (A2 notes): the daemon never rewrites
	// metadata — the GUI must see kubectl's own schema one-for-one.
	var peek struct {
		Metadata struct {
			Name              string `json:"name"`
			CreationTimestamp string `json:"creationTimestamp"`
		} `json:"metadata"`
	}
	if err := json.Unmarshal(jobs[0], &peek); err != nil {
		t.Fatal(err)
	}
	if peek.Metadata.Name != "job-0" || peek.Metadata.CreationTimestamp == "" {
		t.Fatalf("k8s_status: job passthrough mangled: %+v", peek.Metadata)
	}
}

func TestK8sStatusRowCapAndTruncation(t *testing.T) {
	root := initRepo(t)
	home := t.TempDir()
	t.Setenv("HOME", home)
	writePrefs(t, home, "k8s_namespace: lab")
	installMockKubectl(t, "ok", kubectlList(60, 0), "")

	rig := startRig(t, root)
	defer rig.stop(t)

	st, jobs, _ := consumeK8sStatus(t, rig, root)
	if len(jobs) != 50 {
		t.Fatalf("k8s_status: got %d jobs, want the 50-row cap", len(jobs))
	}
	if !st.Truncated {
		t.Fatal("k8s_status: truncation not declared at the cap")
	}
}

func TestK8sStatusSelectorPassthroughSkipsCap(t *testing.T) {
	root := initRepo(t)
	home := t.TempDir()
	t.Setenv("HOME", home)
	writePrefs(t, home, "k8s_namespace: lab\nk8s_job_selector: app=training")
	argsLog := installMockKubectl(t, "ok", kubectlList(60, 0), "")

	rig := startRig(t, root)
	defer rig.stop(t)

	st, jobs, _ := consumeK8sStatus(t, rig, root)
	if len(jobs) != 60 || st.Truncated {
		t.Fatalf("k8s_status: selector path capped or truncated (%d jobs, truncated=%v)", len(jobs), st.Truncated)
	}
	got := readArgsLog(t, argsLog)
	if len(got) != 1 || !strings.Contains(got[0], "--selector app=training") {
		t.Fatalf("k8s_status: selector not passed argv-only: %v", got)
	}
	if !strings.Contains(got[0], "get jobs,pods -n lab -o json") {
		t.Fatalf("k8s_status: unexpected argv shape: %v", got)
	}
}

func TestK8sStatusContextPassthrough(t *testing.T) {
	root := initRepo(t)
	home := t.TempDir()
	t.Setenv("HOME", home)
	writePrefs(t, home, "k8s_namespace: lab\nk8s_context: prod")
	argsLog := installMockKubectl(t, "ok", kubectlList(1, 0), "")

	rig := startRig(t, root)
	defer rig.stop(t)

	consumeK8sStatus(t, rig, root)
	got := readArgsLog(t, argsLog)
	if len(got) != 1 || !strings.Contains(got[0], "--context prod") {
		t.Fatalf("k8s_status: --context not passed through: %v", got)
	}
}

func TestK8sStatusTimeout(t *testing.T) {
	root := initRepo(t)
	home := t.TempDir()
	t.Setenv("HOME", home)
	writePrefs(t, home, "k8s_namespace: lab")
	installMockKubectl(t, "sleep", "", "")

	rig := startRig(t, root)
	defer rig.stop(t)
	rig.server.k8sTimeoutForTest = 50 * time.Millisecond

	start := time.Now()
	st, _, _ := consumeK8sStatus(t, rig, root)
	if elapsed := time.Since(start); elapsed > 5*time.Second {
		t.Fatalf("k8s_status: timeout took %v — the deadline never fired", elapsed)
	}
	if st.Reason != "timeout" {
		t.Fatalf("k8s_status: reason = %q, want timeout", st.Reason)
	}
}

// UX-2 revise-1 (panel finding #2): the stderr tail must ride the envelope
// — capped at k8sStderrCap, bounded AT CAPTURE (LimitReader), and carrying
// kubectl's real diagnosis so the GUI popover shows more than the class.
func TestK8sStatusDetailCarriesCappedStderrTail(t *testing.T) {
	root := initRepo(t)
	home := t.TempDir()
	t.Setenv("HOME", home)
	writePrefs(t, home, "k8s_namespace: lab")
	longErr := "ERRDIAG-start " + strings.Repeat("x", 3000) + " <TRUNC-END>"
	installMockKubectl(t, "fail", "", longErr)

	rig := startRig(t, root)
	defer rig.stop(t)

	st, _, _ := consumeK8sStatus(t, rig, root)
	if st.Available {
		t.Fatal("k8s_status: available on failing kubectl")
	}
	if st.Reason != "unreachable" {
		t.Fatalf("k8s_status: reason = %q, want unreachable", st.Reason)
	}
	if st.Detail == "" {
		t.Fatal("k8s_status: detail empty — kubectl's diagnosis never reached the response")
	}
	if len(st.Detail) != k8sStderrCap {
		t.Fatalf("k8s_status: detail %d bytes, want exactly the %d-byte capture cap", len(st.Detail), k8sStderrCap)
	}
	if !strings.HasPrefix(st.Detail, "ERRDIAG-start") {
		t.Fatalf("k8s_status: detail lost its diagnostic head: %.40q", st.Detail)
	}
	if strings.Contains(st.Detail, "<TRUNC-END>") {
		t.Fatal("k8s_status: detail must be the bounded head — the end marker should not survive capture")
	}
}

func TestK8sStatusCauseClasses(t *testing.T) {
	for _, tc := range []struct {
		name   string
		stderr string
		want   string
	}{
		{"unreachable", "Unable to connect to the server: dial tcp 10.0.0.1:6443: connect: connection refused", "unreachable"},
		{"auth", `error: Unauthorized`, "auth"},
		{"forbidden", "Error from server (Forbidden): jobs.batch is forbidden: User \"x\" cannot list resource \"jobs\"", "auth"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := initRepo(t)
			home := t.TempDir()
			t.Setenv("HOME", home)
			writePrefs(t, home, "k8s_namespace: lab")
			installMockKubectl(t, "fail", "", tc.stderr)

			rig := startRig(t, root)
			defer rig.stop(t)

			st, _, _ := consumeK8sStatus(t, rig, root)
			if st.Reason != tc.want {
				t.Fatalf("k8s_status: reason = %q, want %q", st.Reason, tc.want)
			}
		})
	}
}

// ---------- A4 (multi-namespace) ----------

func TestK8sStatusMultiNsFanParallelAndFlatMerge(t *testing.T) {
	root := initRepo(t)
	home := t.TempDir()
	t.Setenv("HOME", home)
	writePrefs(t, home, "k8s_namespace: default,lab")
	argsLog := installMockKubectlDispatch(t, []mockKubectlRule{
		{"-n default", mockOkBody(kubectlListNs("default", 2, 1))},
		{"-n lab", mockOkBody(kubectlListNs("lab", 1, 0))},
	})

	rig := startRig(t, root)
	defer rig.stop(t)

	st, jobs, pods := consumeK8sStatus(t, rig, root)
	if !st.Available {
		t.Fatalf("k8s_status: unavailable: %q", st.Reason)
	}
	if len(jobs) != 3 || len(pods) != 1 {
		t.Fatalf("k8s_status: flat merge = %d jobs %d pods, want 3/1", len(jobs), len(pods))
	}
	// Configured order is preserved in the per-ns rows and the flat merge
	// (default's items land before lab's).
	if len(st.Namespaces) != 2 || st.Namespaces[0].Name != "default" || st.Namespaces[1].Name != "lab" {
		t.Fatalf("k8s_status: namespaces rows = %+v, want [default lab] in order", st.Namespaces)
	}
	for i, want := range []int{2, 1} {
		if !st.Namespaces[i].OK || st.Namespaces[i].JobCount != want {
			t.Fatalf("k8s_status: ns row %d = %+v, want ok with %d jobs", i, st.Namespaces[i], want)
		}
	}
	var peek struct {
		Metadata struct {
			Namespace string `json:"namespace"`
		} `json:"metadata"`
	}
	for i, want := range []string{"default", "default", "lab"} {
		if err := json.Unmarshal(jobs[i], &peek); err != nil {
			t.Fatal(err)
		}
		if peek.Metadata.Namespace != want {
			t.Fatalf("k8s_status: flat job %d namespace = %q, want %q", i, peek.Metadata.Namespace, want)
		}
	}
	// BOTH legs ran: the fan forked once per configured namespace.
	got := readArgsLog(t, argsLog)
	var sawDefault, sawLab int
	for _, line := range got {
		if strings.Contains(line, "get jobs,pods -n default -o json") {
			sawDefault++
		}
		if strings.Contains(line, "get jobs,pods -n lab -o json") {
			sawLab++
		}
	}
	if sawDefault != 1 || sawLab != 1 || len(got) != 2 {
		t.Fatalf("k8s_status: argv fan = %v, want exactly one get per ns", got)
	}
}

func TestK8sStatusMultiNsBadElementFailsLoud(t *testing.T) {
	root := initRepo(t)
	home := t.TempDir()
	t.Setenv("HOME", home)
	writePrefs(t, home, "k8s_namespace: default,Bad_NS!")
	argsLog := installMockKubectl(t, "ok", kubectlList(1, 0), "")

	rig := startRig(t, root)
	defer rig.stop(t)

	st, _, _ := consumeK8sStatus(t, rig, root)
	if st.Reason != "bad_namespace" {
		t.Fatalf("k8s_status: reason = %q, want bad_namespace", st.Reason)
	}
	if !strings.Contains(st.Detail, "Bad_NS!") {
		t.Fatalf("k8s_status: detail %q must name the offending element", st.Detail)
	}
	if got := readArgsLog(t, argsLog); len(got) != 0 {
		t.Fatalf("k8s_status: exec'd despite a bad element: %v", got)
	}
}

func TestK8sStatusMultiNsOverCapFailsLoud(t *testing.T) {
	root := initRepo(t)
	home := t.TempDir()
	t.Setenv("HOME", home)
	writePrefs(t, home, "k8s_namespace: a,b,c,d,e,f") // 6 valid labels > the 5 cap
	argsLog := installMockKubectl(t, "ok", kubectlList(1, 0), "")

	rig := startRig(t, root)
	defer rig.stop(t)

	st, _, _ := consumeK8sStatus(t, rig, root)
	if st.Reason != "bad_namespace" {
		t.Fatalf("k8s_status: reason = %q, want bad_namespace for the over-cap list", st.Reason)
	}
	if !strings.Contains(st.Detail, "5") {
		t.Fatalf("k8s_status: detail %q must name the cap", st.Detail)
	}
	if got := readArgsLog(t, argsLog); len(got) != 0 {
		t.Fatalf("k8s_status: silently truncated the ns set and exec'd: %v", got)
	}
}

func TestK8sStatusMultiNsSharedDeadlineKillsEveryLeg(t *testing.T) {
	root := initRepo(t)
	home := t.TempDir()
	t.Setenv("HOME", home)
	writePrefs(t, home, "k8s_namespace: default,lab")
	argsLog := installMockKubectlDispatch(t, []mockKubectlRule{
		{"-n default", mockSleepBody},
		{"-n lab", mockSleepBody},
	})

	rig := startRig(t, root)
	defer rig.stop(t)
	// The deadline must exceed the mock's fork+sh-echo startup window: the
	// argv-log line IS the parallel-fan evidence, and a 50ms kill can race
	// shell startup on a loaded machine (the other single-ns timeout test
	// keeps its 50ms because it never reads the log).
	rig.server.k8sTimeoutForTest = 500 * time.Millisecond

	start := time.Now()
	st, _, _ := consumeK8sStatus(t, rig, root)
	if elapsed := time.Since(start); elapsed > 10*time.Second {
		t.Fatalf("k8s_status: shared deadline never fired (%v)", elapsed)
	}
	if st.Available || st.Reason != "timeout" {
		t.Fatalf("k8s_status: got available=%v reason=%q, want unavailable timeout", st.Available, st.Reason)
	}
	// Both legs degrade to timeout — the parallel fan started both (argv
	// logged BEFORE each sleep) and the one shared deadline killed both.
	if len(st.Namespaces) != 2 {
		t.Fatalf("k8s_status: namespaces rows = %+v, want 2 timeout rows", st.Namespaces)
	}
	for _, row := range st.Namespaces {
		if row.OK || row.Reason != "timeout" {
			t.Fatalf("k8s_status: ns row = %+v, want timeout", row)
		}
	}
	if got := readArgsLog(t, argsLog); len(got) != 2 {
		t.Fatalf("k8s_status: legs started = %d, want 2 (parallel fan)", len(got))
	}
}

func TestK8sStatusMultiNsPartialFailureKeepsChipHealthy(t *testing.T) {
	root := initRepo(t)
	home := t.TempDir()
	t.Setenv("HOME", home)
	writePrefs(t, home, "k8s_namespace: default,lab")
	installMockKubectlDispatch(t, []mockKubectlRule{
		{"-n default", mockOkBody(kubectlListNs("default", 2, 0))},
		{"-n lab", mockFailBody("Error from server (Forbidden): jobs.batch is forbidden")},
	})

	rig := startRig(t, root)
	defer rig.stop(t)

	st, jobs, _ := consumeK8sStatus(t, rig, root)
	// A2-1 at namespace granularity: partial availability = healthy chip
	// (Available:true) + the degraded ns row — NO third chip state.
	if !st.Available {
		t.Fatalf("k8s_status: partial failure must stay available, got %q", st.Reason)
	}
	if len(jobs) != 2 {
		t.Fatalf("k8s_status: flat jobs = %d, want default's 2", len(jobs))
	}
	if len(st.Namespaces) != 2 {
		t.Fatalf("k8s_status: namespaces rows = %+v", st.Namespaces)
	}
	def, lab := st.Namespaces[0], st.Namespaces[1]
	if !def.OK || def.JobCount != 2 {
		t.Fatalf("k8s_status: default row = %+v, want ok with 2 jobs", def)
	}
	if lab.OK || lab.Reason != "auth" {
		t.Fatalf("k8s_status: lab row = %+v, want failed auth row", lab)
	}
	if !strings.Contains(lab.Detail, "Forbidden") {
		t.Fatalf("k8s_status: lab detail %q lost the stderr tail", lab.Detail)
	}
}

func TestK8sStatusMultiNsPerNsRowCapAndOrdTruncation(t *testing.T) {
	root := initRepo(t)
	home := t.TempDir()
	t.Setenv("HOME", home)
	writePrefs(t, home, "k8s_namespace: default,lab")
	installMockKubectlDispatch(t, []mockKubectlRule{
		{"-n default", mockOkBody(kubectlListNs("default", 60, 0))},
		{"-n lab", mockOkBody(kubectlListNs("lab", 60, 0))},
	})

	rig := startRig(t, root)
	defer rig.stop(t)

	st, jobs, _ := consumeK8sStatus(t, rig, root)
	// Per-NS 50-row cap (A2-3 + A4): 60+60 caps to 50+50, Truncated OR'd.
	if len(jobs) != 100 {
		t.Fatalf("k8s_status: flat jobs = %d, want the per-ns caps merged (100)", len(jobs))
	}
	if !st.Truncated {
		t.Fatal("k8s_status: Truncated must OR across answering namespaces")
	}
	for i, row := range st.Namespaces {
		if row.JobCount != 50 {
			t.Fatalf("k8s_status: ns row %d job_count = %d, want 50", i, row.JobCount)
		}
	}
}
