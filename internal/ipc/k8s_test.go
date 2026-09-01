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
	items := make([]string, 0, nJobs+nPods)
	for i := 0; i < nJobs; i++ {
		items = append(items, fmt.Sprintf(`{"kind":"Job","metadata":{"name":"job-%d","creationTimestamp":"2026-09-01T00:00:00Z"},"status":{}}`, i))
	}
	for i := 0; i < nPods; i++ {
		items = append(items, fmt.Sprintf(`{"kind":"Pod","metadata":{"name":"pod-%d"},"status":{"phase":"Running"}}`, i))
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
