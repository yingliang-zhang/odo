package ipc

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path"
	"sort"
	"strings"
	"time"

	"github.com/yingliang-zhang/odo/internal/adapter"
)

// k8sBatchRowCap bounds the batch rows shipped per call — a runaway glob
// over a huge CPFS dir must never flood the GUI; the cap is declared via
// Truncated, never a silent drop.
const k8sBatchRowCap = 25

// k8sBatchStaleSeconds is the heartbeat staleness horizon (A2-4): a batch
// whose status.json heartbeat is older than 90s is UNKNOWN, never
// progress — the file could be frozen mid-crash (B4).
const k8sBatchStaleSeconds = 90

// k8sBatchStatusFile is the status.json schema (pinned in
// docs/design/d5b-batch-status.md). SchemaVersion gates upgrades: an old
// daemon against a new file renders a visible schema_mismatch row, never
// garbage.
type k8sBatchStatusFile struct {
	Batch         string  `json:"batch"`
	Stage         string  `json:"stage"`
	Total         int     `json:"total"`
	Done          int     `json:"done"`
	Errs          int     `json:"errs"`
	RatePerMin    float64 `json:"rate_per_min"`
	UpdatedUnix   int64   `json:"updated_unix"`
	Status        string  `json:"status"`
	SchemaVersion int     `json:"schema_version"`
}

// k8sPodRef is one Running pod matched by the job selector — resolved
// PER-READ (pods are ephemeral; a stored pod name rots immediately, B4).
type k8sPodRef struct {
	name string
	ns   string
}

// k8sBatchRow builds the batch row for one status.json blob. name is the
// row label when the file itself cannot name things (filename-shaped).
// Data file present + well-formed → a full row; any degradation lands as
// a row WITH a Reason and no data — never a dropped file.
func k8sBatchRow(name string, blob []byte, nowUnix int64) K8sBatch {
	var f k8sBatchStatusFile
	if err := json.Unmarshal(blob, &f); err != nil {
		return K8sBatch{Batch: name, Reason: "unparseable"}
	}
	if f.SchemaVersion != 1 {
		return K8sBatch{Batch: name, Reason: "schema_mismatch"}
	}
	batch := f.Batch
	if batch == "" {
		batch = name
	}
	stale := f.UpdatedUnix > 0 && nowUnix-f.UpdatedUnix > k8sBatchStaleSeconds
	return K8sBatch{
		Batch:       batch,
		Stage:       f.Stage,
		Total:       f.Total,
		Done:        f.Done,
		Errs:        f.Errs,
		RatePerMin:  f.RatePerMin,
		UpdatedUnix: f.UpdatedUnix,
		Status:      f.Status,
		Stale:       stale,
	}
}

// sortK8sBatches orders newest-first by heartbeat; rows without a stamp
// (degraded rows) sink to the bottom in stable order.
func sortK8sBatches(rows []K8sBatch) {
	sort.SliceStable(rows, func(i, j int) bool {
		return rows[i].UpdatedUnix > rows[j].UpdatedUnix
	})
}

// k8sBatchLocalRead lists every *.json directly under dir (depth 1 — the
// CPFS-on-Mac mount is flat by contract) and returns the rows with a
// "errored files keep their reason" posture. The bool reports whether the
// DIRECTORY itself was readable: false hands the caller the fallback
// decision (local unreadable ≠ empty — an empty dir answers no batches).
func k8sBatchLocalRead(dir string, nowUnix int64) ([]K8sBatch, bool) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, false
	}
	var rows []K8sBatch
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		name := e.Name()
		if !strings.HasSuffix(name, ".json") {
			continue
		}
		full := path.Join(dir, name)
		blob, err := os.ReadFile(full)
		if err != nil {
			rows = append(rows, K8sBatch{Batch: name, Reason: "unreadable"})
			continue
		}
		rows = append(rows, k8sBatchRow(name, blob, nowUnix))
	}
	return rows, true
}

// k8sBatchFindPods queries `get pods -l <selector> -o json` across the
// configured namespaces and keeps Running pods (N≤5 legs, cold path —
// the fallback fires only when the local mount is missing). Per-ns
// failure is tolerated by skipping that leg: the 0-match class then
// honestly answers pod_not_found, never a fabricated pod.
func k8sBatchFindPods(cctx context.Context, settings adapter.Settings, namespaces []string) []k8sPodRef {
	var found []k8sPodRef
	for _, ns := range namespaces {
		args := []string{"get", "pods", "-n", ns, "-o", "json",
			"--selector", settings.K8sJobSelector}
		if settings.K8sContext != "" {
			args = append(args, "--context", settings.K8sContext)
		}
		out, err := k8sExec(cctx, args...)
		if err != nil || out.class != "" {
			continue
		}
		var list struct {
			Items []struct {
				Metadata struct {
					Name      string `json:"name"`
					Namespace string `json:"namespace"`
				} `json:"metadata"`
				Status struct {
					Phase string `json:"phase"`
				} `json:"status"`
			} `json:"items"`
		}
		if err := json.Unmarshal(out.stdout, &list); err != nil {
			continue
		}
		for _, p := range list.Items {
			if p.Status.Phase != "Running" {
				continue
			}
			podNS := p.Metadata.Namespace
			if podNS == "" {
				podNS = ns
			}
			found = append(found, k8sPodRef{name: p.Metadata.Name, ns: podNS})
		}
	}
	return found
}

// handleK8sBatchStatus implements the D5b batch progress bridge (A2-4).
// Local-mount-first: os.ReadDir over <k8s_batch_dir>/*.json — the CPFS
// mount on the Mac, zero privilege. ONLY when that read fails AND k8s is
// configured does the kubectl fallback fire: resolve the pod per-read via
// the job selector across the configured namespaces, then
// `kubectl exec <pod> -- cat <dir>/status.json` — cat is the ONLY
// whitelisted exec verb on pods, argv-only, EnrichedEnv, capped stderr.
// Multi-match is a deterministic refusal (ambiguous_pod, Sol B4), never a
// guess. NEVER journaled (same containment as k8s_status).
func (s *Server) handleK8sBatchStatus(ctx context.Context, req Request) (Response, error) {
	if _, err := s.resolveProject(ctx, req.ProjectRoot); err != nil {
		return Response{}, fmt.Errorf("k8s_batch_status: %w", err)
	}

	settings := adapter.ReadSettings()
	dir := settings.K8sBatchDir
	if dir == "" {
		return Response{K8sBatchStatus: &K8sBatchStatus{Available: false, Reason: "off"}}, nil
	}

	nowUnix := time.Now().Unix()
	status := &K8sBatchStatus{Available: true}

	rows, localOK := k8sBatchLocalRead(dir, nowUnix)
	if !localOK {
		// Fallback: kubectl exec cat — only reachable with k8s configured.
		namespaces, bad, overCap := parseK8sNamespaces(settings.K8sNamespace)
		if (len(namespaces) == 0 && len(bad) == 0) || len(bad) > 0 || overCap {
			return Response{K8sBatchStatus: &K8sBatchStatus{
				Available: false,
				Reason:    "local_missing",
				Detail:    fmt.Sprintf("%s is not readable from the daemon and no k8s pod fallback is configured", dir),
			}}, nil
		}
		if settings.K8sJobSelector == "" {
			// The fallback needs pods to cat FROM — without the selector
			// every entry is an honest refusal, not a guessed pod.
			rows = []K8sBatch{{Batch: "status.json", Reason: "no_pod_selector"}}
		} else if _, err := exec.LookPath("kubectl"); err != nil {
			return Response{K8sBatchStatus: &K8sBatchStatus{Available: false, Reason: "ENOENT"}}, nil
		} else {
			timeout := k8sTimeout
			if s.k8sTimeoutForTest > 0 {
				timeout = s.k8sTimeoutForTest
			}
			cctx, cancel := context.WithTimeout(ctx, timeout)
			defer cancel()
			pods := k8sBatchFindPods(cctx, settings, namespaces)
			switch {
			case len(pods) == 0:
				rows = []K8sBatch{{Batch: "status.json", Reason: "pod_not_found"}}
			case len(pods) > 1:
				rows = []K8sBatch{{Batch: "status.json", Reason: "ambiguous_pod"}}
			default:
				// One pod — exec the SINGLE whitelisted verb, on an
				// argv-whitelisted path derived from settings.
				statusPath := path.Join(dir, "status.json")
				args := []string{"exec", pods[0].name, "-n", pods[0].ns, "--", "cat", statusPath}
				out, err := k8sExec(cctx, args...)
				switch {
				case err != nil:
					rows = []K8sBatch{{Batch: "status.json", Reason: "unreachable"}}
				case out.class != "":
					rows = []K8sBatch{{Batch: "status.json", Reason: out.class}}
				default:
					rows = []K8sBatch{k8sBatchRow("status.json", out.stdout, nowUnix)}
				}
			}
		}
	}

	sortK8sBatches(rows)
	if len(rows) > k8sBatchRowCap {
		rows = rows[:k8sBatchRowCap]
		status.Truncated = true
	}
	if rows == nil {
		rows = []K8sBatch{}
	}
	status.Batches = rows
	return Response{K8sBatchStatus: status}, nil
}
