package ipc

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os/exec"
	"regexp"
	"strings"
	"time"

	"github.com/yingliang-zhang/odo/internal/adapter"
)

// k8sTimeout bounds the kubectl subprocess so a hung cluster call never
// blocks the daemon's IPC thread. Matches the runOmpJSON posture (A2-2).
const k8sTimeout = 10 * time.Second

// k8sStderrCap bounds the stderr tail a classified failure carries to the
// GUI in K8sStatus.Detail — the popover degrades with a capped tail, never
// a log dump. The bound is applied AT CAPTURE (LimitReader on the stderr
// pipe), so no unbounded buffer exists anywhere in the daemon.
const k8sStderrCap = 1024

// k8sJobRowCap is the hard row cap when k8s_job_selector is empty (A2-3):
// all jobs in the namespace, first 50 rows, truncated:true declares the rest.
const k8sJobRowCap = 50

// k8sNamespacePattern is the RFC 1123 DNS-label subset k8s enforces on
// namespaces. Validation rejects before ANY exec (fail loud, not exec) —
// the argv-only posture leaves "--selector"-style injections impossible,
// but a garbage pref still answers bad_namespace instead of exec'ing junk.
var k8sNamespacePattern = regexp.MustCompile(`^[a-z0-9]([-a-z0-9]*[a-z0-9])?$`)

// k8sNoNamespace answers the off-by-config case: empty k8s_namespace pref.
// A2-1: off → NO chip, no tab, no polling, no exec.
func k8sNoNamespace() Response {
	return Response{K8sStatus: &K8sStatus{Available: false, Reason: "off"}}
}

// k8sUnavailable answers the on-but-broken case (A2-1): the cause class is
// mandatory — a configured sensor never fails silently. detail carries
// kubectl's stderr tail when the failure is exec-shaped; pre-exec failures
// (bad_namespace, ENOENT) produce no subprocess output and pass "".
func k8sUnavailable(reason, detail string) Response {
	return Response{K8sStatus: &K8sStatus{Available: false, Reason: reason, Detail: detail}}
}

// k8sClassify maps a kubectl exec failure to its cause class. Timeout beats
// the stderr sniff: a 10s deadline kill often drags no usable error text.
// Default is "unreachable" — the honest class for any connection-shaped
// failure the sniffs don't recognize (exit errors are the only other path;
// ENOENT is resolved upstream via LookPath).
func k8sClassify(ctx context.Context, stderr string) string {
	if ctx.Err() == context.DeadlineExceeded {
		return "timeout"
	}
	lower := strings.ToLower(stderr)
	if strings.Contains(lower, "forbidden") || strings.Contains(lower, "unauthorized") {
		return "auth"
	}
	return "unreachable"
}

// k8sListItem peeks at each raw item of kubectl's `get … -o json` List to
// split Jobs from Pods without interpreting the payloads (swap-friendly,
// A2 notes — the GUI receives kubectl's own schema one-for-one).
type k8sListItem struct {
	Kind string `json:"kind"`
}

// handleK8sStatus fetches `kubectl get jobs,pods -n <ns> -o json` and returns
// the split item slices. Exec is direct (no shell), argv-only, bounded by
// k8sTimeout, fed with EnrichedEnv() so homebrew PATH + KUBECONFIG reach a
// Finder-launched daemon (A2-2). Read-only get ONLY — apply/delete/logs are
// escalate-class (D5 Stage 0). NEVER journaled.
func (s *Server) handleK8sStatus(ctx context.Context, req Request) (Response, error) {
	if _, err := s.resolveProject(ctx, req.ProjectRoot); err != nil {
		return Response{}, fmt.Errorf("k8s_status: %w", err)
	}

	settings := adapter.ReadSettings()
	ns := settings.K8sNamespace
	if ns == "" {
		return k8sNoNamespace(), nil
	}
	if !k8sNamespacePattern.MatchString(ns) {
		return k8sUnavailable("bad_namespace", ""), nil
	}
	if _, err := exec.LookPath("kubectl"); err != nil {
		return k8sUnavailable("ENOENT", ""), nil
	}

	args := []string{"get", "jobs,pods", "-n", ns, "-o", "json"}
	if settings.K8sContext != "" {
		args = append(args, "--context", settings.K8sContext)
	}
	if settings.K8sJobSelector != "" {
		args = append(args, "--selector", settings.K8sJobSelector)
	}

	timeout := k8sTimeout
	if s.k8sTimeoutForTest > 0 {
		timeout = s.k8sTimeoutForTest
	}
	cctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	cmd := exec.CommandContext(cctx, "kubectl", args...)
	cmd.Env = adapter.EnrichedEnv()
	var stdout bytes.Buffer
	cmd.Stdout = &stdout
	// stderr is read through a LimitReader-backed pipe: the diagnosis tail
	// is bounded at capture (k8sStderrCap), never an unbounded buffer sliced
	// after the fact. A stderr flood that overflows the OS pipe buffer
	// blocks kubectl's writes until the cctx deadline kills it — pathological
	// output degrades to reason:"timeout", never a daemon-side memory stall.
	// ReadAll before Wait is mandatory (os/exec StderrPipe contract).
	stderrPipe, err := cmd.StderrPipe()
	if err != nil {
		return Response{}, fmt.Errorf("k8s_status: stderr pipe: %w", err)
	}
	if err := cmd.Start(); err != nil {
		// LookPath already passed; a spawn failure here is exec-class with
		// no subprocess output (same no-detail posture as ENOENT).
		return k8sUnavailable("unreachable", ""), nil
	}
	tail, _ := io.ReadAll(io.LimitReader(stderrPipe, k8sStderrCap))
	if err := cmd.Wait(); err != nil {
		detail := string(tail)
		return k8sUnavailable(k8sClassify(cctx, detail), detail), nil
	}

	// kubectl answers a kind:List of raw items (mixed Job/Pod for the
	// `get jobs,pods` form). Split by kind, keeping each slice raw.
	var list struct {
		Items []json.RawMessage `json:"items"`
	}
	if err := json.Unmarshal(stdout.Bytes(), &list); err != nil {
		return k8sUnavailable("unreachable", ""), nil
	}
	var jobs []json.RawMessage
	var pods []json.RawMessage
	for _, item := range list.Items {
		var peek k8sListItem
		if err := json.Unmarshal(item, &peek); err != nil {
			continue // malformed item — drop it, keep the rest
		}
		switch peek.Kind {
		case "Job":
			jobs = append(jobs, item)
		case "Pod":
			pods = append(pods, item)
		}
	}

	// A2-3: the 50-row cap + declared truncation apply ONLY to the empty
	// k8s_job_selector shape; an explicit selector is an argv passthrough.
	truncated := false
	if settings.K8sJobSelector == "" && len(jobs) > k8sJobRowCap {
		jobs = jobs[:k8sJobRowCap]
		truncated = true
	}

	// "[]" not "null" — the GUI folds length; a nil slice marshals as
	// null and would need a second branch per consumer.
	if jobs == nil {
		jobs = []json.RawMessage{}
	}
	if pods == nil {
		pods = []json.RawMessage{}
	}
	jobsBlob, err := json.Marshal(jobs)
	if err != nil {
		return Response{}, fmt.Errorf("k8s_status: marshal jobs: %w", err)
	}
	podsBlob, err := json.Marshal(pods)
	if err != nil {
		return Response{}, fmt.Errorf("k8s_status: marshal pods: %w", err)
	}
	return Response{K8sStatus: &K8sStatus{
		Available:   true,
		Jobs:        jobsBlob,
		Pods:        podsBlob,
		Truncated:   truncated,
		FetchedUnix: time.Now().Unix(),
	}}, nil
}
