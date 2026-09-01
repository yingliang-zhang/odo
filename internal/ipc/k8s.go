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
	"sync"
	"time"

	"github.com/yingliang-zhang/odo/internal/adapter"
)

// k8sTimeout bounds the N-fork kubectl fan per handler call so a hung
// cluster never blocks the daemon's IPC thread. It is ONE shared deadline
// across all namespace legs (A4 D2, user ruling ②): parallel fans with a
// common 10s budget keep the handler's wall-clock profile identical to the
// single-ns posture; a sequential loop would be N×10s worst case, and
// per-leg deadlines let a hung first leg starve the rest. Matches the
// runOmpJSON posture (A2-2).
const k8sTimeout = 10 * time.Second

// k8sStderrCap bounds the stderr tail a classified failure carries to the
// GUI in K8sStatus.Detail — the popover degrades with a capped tail, never
// a log dump. The bound is applied AT CAPTURE (LimitReader on the stderr
// pipe), so no unbounded buffer exists anywhere in the daemon.
const k8sStderrCap = 1024

// k8sJobRowCap is the hard row cap per answering namespace when
// k8s_job_selector is empty (A2-3 + A4: the cap stays PER-NS so one fat
// namespace cannot crowd out the others in the flat merge). Truncated
// declares the rest (OR'd across namespaces).
const k8sJobRowCap = 50

// k8sMaxNamespaces caps the comma list (A4 D6, locked at 5 by the user
// ruling): beyond it the fetch is refused loud — a silently-truncated ns
// set would make the chip's summed counts a lie of omission.
const k8sMaxNamespaces = 5

// k8sNamespacePattern is the RFC 1123 DNS-label subset k8s enforces on
// namespaces. Validation rejects before ANY exec (fail loud, not exec) —
// the argv-only posture leaves "--selector"-style injections impossible,
// but a garbage pref still answers bad_namespace instead of exec'ing junk.
var k8sNamespacePattern = regexp.MustCompile(`^[a-z0-9]([-a-z0-9]*[a-z0-9])?$`)

// parseK8sNamespaces splits the k8s_namespace pref (A4 D1: ONE key, comma
// list — "lab" ≡ ["lab"], "" = off, zero migration) into a deduped
// ordered list. Empty elements are dropped (trailing commas are a
// hand-edit artifact, not data); every non-empty element that fails RFC
// 1123 lands in bad. overCap reports a valid-but-too-long list — both
// failure shapes answer bad_namespace with the offender(s) named in the
// Detail (fail loud, never silently drop).
func parseK8sNamespaces(raw string) (namespaces, bad []string, overCap bool) {
	seen := make(map[string]bool)
	for _, seg := range strings.Split(raw, ",") {
		s := strings.TrimSpace(seg)
		if s == "" {
			continue
		}
		if !k8sNamespacePattern.MatchString(s) {
			bad = append(bad, s)
			continue
		}
		if !seen[s] {
			seen[s] = true
			namespaces = append(namespaces, s)
		}
	}
	overCap = len(namespaces) > k8sMaxNamespaces
	return namespaces, bad, overCap
}

// k8sNoNamespace answers the off-by-config case: empty k8s_namespace pref.
// A2-1: off → NO chip, no tab, no polling, no exec.
func k8sNoNamespace() Response {
	return Response{K8sStatus: &K8sStatus{Available: false, Reason: "off"}}
}

// k8sUnavailable answers the on-but-broken case (A2-1): the cause class is
// mandatory — a configured sensor never fails silently. detail carries
// kubectl's stderr tail for exec-shaped failures; pre-exec failures
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

// k8sExecOutcome is one bounded kubectl run: stdout, the capped stderr
// tail, and the classified cause ("" on success). Shared by the k8s_status
// namespace fan and the k8s_batch_status pod fallback.
type k8sExecOutcome struct {
	stdout []byte
	detail string
	class  string
}

// k8sExec runs kubectl argv-only (no shell) under the caller's deadline,
// fed with EnrichedEnv() so homebrew PATH + KUBECONFIG reach a
// Finder-launched daemon (A2-2). Read-only invocation ONLY — the callers
// pass get/exec-cat argv assembled from validated settings. stderr is read
// through a LimitReader pipe: the diagnosis tail is bounded at capture
// (k8sStderrCap), never an unbounded buffer sliced after the fact. A stderr
// flood that overflows the OS pipe buffer blocks kubectl's writes until the
// deadline kills it — pathological output degrades to class "timeout",
// never a daemon-side memory stall.
func k8sExec(cctx context.Context, args ...string) (out k8sExecOutcome, err error) {
	cmd := exec.CommandContext(cctx, "kubectl", args...)
	cmd.Env = adapter.EnrichedEnv()
	var stdout bytes.Buffer
	cmd.Stdout = &stdout
	stderrPipe, err := cmd.StderrPipe()
	if err != nil {
		return out, fmt.Errorf("kubectl %s: stderr pipe: %w", args[0], err)
	}
	if err := cmd.Start(); err != nil {
		// LookPath already passed upstream; a spawn failure here is
		// exec-class with no subprocess output (same no-detail posture).
		out.class = "unreachable"
		return out, nil
	}
	// ReadAll before Wait is mandatory (os/exec StderrPipe contract).
	tail, _ := io.ReadAll(io.LimitReader(stderrPipe, k8sStderrCap))
	out.detail = string(tail)
	if err := cmd.Wait(); err != nil {
		out.class = k8sClassify(cctx, out.detail)
		return out, nil
	}
	out.stdout = stdout.Bytes()
	return out, nil
}

// k8sNsFetch is one namespace leg's result: the per-ns status row plus its
// kind-split raw slices (nil unless the leg answered).
type k8sNsFetch struct {
	st        K8sNsStatus
	jobs      []json.RawMessage
	pods      []json.RawMessage
	truncated bool
}

// k8sFetchNamespace runs `get jobs,pods -n <ns> -o json` and classifies the
// outcome into a per-ns row. The context/selector passthroughs ride argv
// (--context/--selector, A2-3); enforcement stays argv-only. Failure of
// one leg NEVER aborts the fan — its row carries the cause class.
func k8sFetchNamespace(cctx context.Context, settings adapter.Settings, ns string) k8sNsFetch {
	res := k8sNsFetch{st: K8sNsStatus{Name: ns}}
	args := []string{"get", "jobs,pods", "-n", ns, "-o", "json"}
	if settings.K8sContext != "" {
		args = append(args, "--context", settings.K8sContext)
	}
	if settings.K8sJobSelector != "" {
		args = append(args, "--selector", settings.K8sJobSelector)
	}
	out, err := k8sExec(cctx, args...)
	if err != nil {
		res.st.Reason = "unreachable"
		return res
	}
	if out.class != "" {
		res.st.Reason = out.class
		res.st.Detail = out.detail
		return res
	}

	// kubectl answers a kind:List of raw items (mixed Job/Pod). Split by
	// kind, keeping each slice raw. kubectl items carry metadata.namespace,
	// so the flat merge stays ns-identifiable without daemon interpretation.
	var list struct {
		Items []json.RawMessage `json:"items"`
	}
	if err := json.Unmarshal(out.stdout, &list); err != nil {
		res.st.Reason = "unreachable"
		return res
	}
	for _, item := range list.Items {
		var peek k8sListItem
		if err := json.Unmarshal(item, &peek); err != nil {
			continue // malformed item — drop it, keep the rest
		}
		switch peek.Kind {
		case "Job":
			res.jobs = append(res.jobs, item)
		case "Pod":
			res.pods = append(res.pods, item)
		}
	}

	// A2-3/A4: the 50-row cap + declared truncation apply ONLY to the
	// empty k8s_job_selector shape, per answering namespace.
	if settings.K8sJobSelector == "" && len(res.jobs) > k8sJobRowCap {
		res.jobs = res.jobs[:k8sJobRowCap]
		res.truncated = true
	}
	res.st.OK = true
	res.st.JobCount = len(res.jobs)
	return res
}

// handleK8sStatus fans `kubectl get jobs,pods -n <ns> -o json` out to every
// configured namespace, PARALLEL, from ONE shared deadline (A4 D2). The
// pref parses as a comma list (parseK8sNamespaces): off-by-config and
// validation failures answer before any exec; the kubectl capture posture
// is k8sExec — argv-only, EnrichedEnv, capped stderr tail. Read-only get
// ONLY; --all-namespaces stays forbidden (A4 D2: cluster-scope list RBAC
// blast radius, one denied grant blanks everything). NEVER journaled.
func (s *Server) handleK8sStatus(ctx context.Context, req Request) (Response, error) {
	if _, err := s.resolveProject(ctx, req.ProjectRoot); err != nil {
		return Response{}, fmt.Errorf("k8s_status: %w", err)
	}

	settings := adapter.ReadSettings()
	namespaces, bad, overCap := parseK8sNamespaces(settings.K8sNamespace)
	if len(namespaces) == 0 && len(bad) == 0 {
		return k8sNoNamespace(), nil
	}
	if len(bad) > 0 {
		return k8sUnavailable("bad_namespace", "invalid namespace element(s): "+strings.Join(bad, ", ")), nil
	}
	if overCap {
		return k8sUnavailable("bad_namespace", fmt.Sprintf("at most %d namespaces supported (%d configured)", k8sMaxNamespaces, len(namespaces))), nil
	}
	if _, err := exec.LookPath("kubectl"); err != nil {
		return k8sUnavailable("ENOENT", ""), nil
	}

	timeout := k8sTimeout
	if s.k8sTimeoutForTest > 0 {
		timeout = s.k8sTimeoutForTest
	}
	cctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	// parallel fan — no locks: each IPC connection is served by its own
	// goroutine and the handler holds no daemon state (k3 read-through).
	fetches := make([]k8sNsFetch, len(namespaces))
	var wg sync.WaitGroup
	for i, ns := range namespaces {
		wg.Add(1)
		go func(i int, ns string) {
			defer wg.Done()
			fetches[i] = k8sFetchNamespace(cctx, settings, ns)
		}(i, ns)
	}
	wg.Wait()

	// Merge: per-ns rows in CONFIGURED order; Jobs/Pods flat-merged across
	// ANSWERING namespaces (kubectl's metadata.namespace keeps each row
	// attributable); Truncated OR'd. Available while ≥1 ns answered; when
	// every leg failed, the whole response degrades to Available:false and
	// the Reason/Detail come from the FIRST parsed namespace's failure —
	// the per-ns rows still ship, so the popover renders all causes.
	nsRows := make([]K8sNsStatus, 0, len(fetches))
	var jobs, pods []json.RawMessage
	truncated := false
	var firstFail *K8sNsStatus
	for i := range fetches {
		f := &fetches[i]
		nsRows = append(nsRows, f.st)
		if f.st.OK {
			jobs = append(jobs, f.jobs...)
			pods = append(pods, f.pods...)
			truncated = truncated || f.truncated
		} else if firstFail == nil {
			firstFail = &f.st
		}
	}

	if firstFail != nil && len(fetches) > 0 && !anyNsOK(nsRows) {
		return Response{K8sStatus: &K8sStatus{
			Available:  false,
			Reason:     firstFail.Reason,
			Detail:     firstFail.Detail,
			Namespaces: nsRows,
		}}, nil
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
		Namespaces:  nsRows,
		FetchedUnix: time.Now().Unix(),
	}}, nil
}

func anyNsOK(rows []K8sNsStatus) bool {
	for _, r := range rows {
		if r.OK {
			return true
		}
	}
	return false
}
