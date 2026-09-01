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

// D5b (A2-4): k8s_batch_status tests. The local path is exercised with
// real tmp dirs; the pod fallback rides the same mock-kubectl harness as
// k8s_test.go (argv logging proves the get-pods shape and the exec-cat
// whitelist).

func consumeK8sBatchStatus(t *testing.T, rig *testRig, root string) K8sBatchStatus {
	t.Helper()
	resp := rig.call(t, Request{Cmd: CmdK8sBatchStatus, ProjectRoot: root})
	if resp.K8sBatchStatus == nil {
		t.Fatal("k8s_batch_status: response K8sBatchStatus is nil")
	}
	return *resp.K8sBatchStatus
}

// writeStatusFile writes one status.json fixture row; updatedUnix is the
// HEARTBEAT (staleness is computed from it, never file mtimes).
func writeStatusFile(t *testing.T, dir, name, batch, stage, status string, schemaVersion, total, done, errs int, rate float64, updatedUnix int64) {
	t.Helper()
	blob := fmt.Sprintf(`{"batch":%q,"stage":%q,"total":%d,"done":%d,"errs":%d,"rate_per_min":%v,"updated_unix":%d,"status":%q,"schema_version":%d}`,
		batch, stage, total, done, errs, rate, updatedUnix, status, schemaVersion)
	if err := os.WriteFile(filepath.Join(dir, name), []byte(blob), 0o644); err != nil {
		t.Fatal(err)
	}
}

func findBatch(t *testing.T, rows []K8sBatch, name string) K8sBatch {
	t.Helper()
	for _, r := range rows {
		if r.Batch == name {
			return r
		}
	}
	t.Fatalf("k8s_batch_status: row %q absent in %+v", name, rows)
	return K8sBatch{}
}

func TestK8sBatchStatusOff(t *testing.T) {
	root := initRepo(t)
	home := t.TempDir()
	t.Setenv("HOME", home)
	writePrefs(t, home, "")

	rig := startRig(t, root)
	defer rig.stop(t)

	st := consumeK8sBatchStatus(t, rig, root)
	if st.Available || st.Reason != "off" {
		t.Fatalf("k8s_batch_status: got available=%v reason=%q, want off", st.Available, st.Reason)
	}
}

func TestK8sBatchStatusLocalRead(t *testing.T) {
	root := initRepo(t)
	home := t.TempDir()
	t.Setenv("HOME", home)
	dir := t.TempDir()
	now := time.Now().Unix()
	writeStatusFile(t, dir, "transcode.json", "transcode", "transcode", "running", 1, 100, 72, 0, 5.2, now-10)
	writeStatusFile(t, dir, "frozen.json", "frozen", "push", "running", 1, 50, 10, 2, 1.0, now-300) // heartbeat >90s → stale
	writeStatusFile(t, dir, "done.json", "dsv2", "verify", "done", 1, 20, 20, 3, 0, now-60)
	writeStatusFile(t, dir, "next.json", "nextgen", "transcode", "running", 2, 10, 0, 0, 1.0, now-5) // schema_version 2 → mismatch row
	if err := os.WriteFile(filepath.Join(dir, "notes.txt"), []byte("not a status file"), 0o644); err != nil {
		t.Fatal(err)
	}
	writePrefs(t, home, "k8s_batch_dir: "+dir)

	rig := startRig(t, root)
	defer rig.stop(t)

	st := consumeK8sBatchStatus(t, rig, root)
	if !st.Available {
		t.Fatalf("k8s_batch_status: unavailable: %q", st.Reason)
	}
	// Every *.json surfaces — the schema_mismatch row is VISIBLE (never a
	// silently dropped file); non-json files are ignored.
	if len(st.Batches) != 4 {
		t.Fatalf("k8s_batch_status: got %d rows, want 4: %+v", len(st.Batches), st.Batches)
	}
	// Newest-first sort by heartbeat; the schema_mismatch row carries no
	// trustworthy timestamp (version-gated files aren't field-read at all)
	// and sinks to the bottom.
	if st.Batches[0].Batch != "transcode" || st.Batches[1].Batch != "dsv2" || st.Batches[2].Batch != "frozen" || st.Batches[3].Reason != "schema_mismatch" {
		t.Fatalf("k8s_batch_status: sort = %+v, want updated_unix desc with reason rows last", st.Batches)
	}
	tc := findBatch(t, st.Batches, "transcode")
	if tc.Stage != "transcode" || tc.Total != 100 || tc.Done != 72 || tc.RatePerMin != 5.2 || tc.Status != "running" {
		t.Fatalf("k8s_batch_status: transcode row = %+v", tc)
	}
	if tc.Stale {
		t.Fatal("k8s_batch_status: 10s heartbeat must not be stale")
	}
	if !findBatch(t, st.Batches, "frozen").Stale {
		t.Fatal("k8s_batch_status: 300s heartbeat must be stale (>90s rule)")
	}
	done := findBatch(t, st.Batches, "dsv2")
	if done.Status != "done" || done.Errs != 3 {
		t.Fatalf("k8s_batch_status: done row = %+v, want done with 3 errs", done)
	}
	// A version-gated row is labeled by its FILENAME — v2 could rename
	// the "batch" field, so nothing inside the file is trusted.
	mismatch := findBatch(t, st.Batches, "next.json")
	if mismatch.Reason != "schema_mismatch" {
		t.Fatalf("k8s_batch_status: schema v2 row = %+v, want schema_mismatch", mismatch)
	}
}

func TestK8sBatchStatusLocalUnavailableNoFallbackSource(t *testing.T) {
	root := initRepo(t)
	home := t.TempDir()
	t.Setenv("HOME", home)
	// Dir missing and NO k8s configured — nothing to fall back to.
	writePrefs(t, home, "k8s_batch_dir: /definitely/not/here")

	rig := startRig(t, root)
	defer rig.stop(t)

	st := consumeK8sBatchStatus(t, rig, root)
	if st.Available || st.Reason != "local_missing" {
		t.Fatalf("k8s_batch_status: got available=%v reason=%q, want local_missing", st.Available, st.Reason)
	}
}

func TestK8sBatchStatusNoPodSelectorIsHonestRefusal(t *testing.T) {
	root := initRepo(t)
	home := t.TempDir()
	t.Setenv("HOME", home)
	writePrefs(t, home, "k8s_namespace: lab\nk8s_batch_dir: /definitely/not/here")
	argsLog := installMockKubectl(t, "ok", kubectlList(0, 0), "")

	rig := startRig(t, root)
	defer rig.stop(t)

	st := consumeK8sBatchStatus(t, rig, root)
	if !st.Available {
		t.Fatalf("k8s_batch_status: selector-less refusal must stay available, got %q", st.Reason)
	}
	row := findBatch(t, st.Batches, "status.json")
	if row.Reason != "no_pod_selector" {
		t.Fatalf("k8s_batch_status: row = %+v, want no_pod_selector", row)
	}
	// The refusal never guesses a pod — zero kubectl calls.
	if got := readArgsLog(t, argsLog); len(got) != 0 {
		t.Fatalf("k8s_batch_status: queried pods without a selector: %v", got)
	}
}

func TestK8sBatchStatusPodFallbackCatsCanonicalFile(t *testing.T) {
	root := initRepo(t)
	home := t.TempDir()
	t.Setenv("HOME", home)
	writePrefs(t, home, "k8s_namespace: lab\nk8s_job_selector: app=dsv\nk8s_batch_dir: /cpfs/ylzhang/batches")
	now := time.Now().Unix()
	podsList := `{"kind":"List","items":[{"kind":"Pod","metadata":{"name":"worker-1","namespace":"lab"},"status":{"phase":"Running"}},{"kind":"Pod","metadata":{"name":"done-0","namespace":"lab"},"status":{"phase":"Succeeded"}}]}`
	statusJSON := fmt.Sprintf(`{"batch":"transcode","stage":"transcode","total":100,"done":72,"errs":1,"rate_per_min":5.2,"updated_unix":%d,"status":"running","schema_version":1}`, now-10)
	argsLog := installMockKubectlDispatch(t, []mockKubectlRule{
		{"get pods", mockOkBody(podsList)},
		{"exec", "cat <<'STATUS_JSON'\n" + statusJSON + "\nSTATUS_JSON\n"},
	})

	rig := startRig(t, root)
	defer rig.stop(t)

	st := consumeK8sBatchStatus(t, rig, root)
	if !st.Available {
		t.Fatalf("k8s_batch_status: unavailable: %q", st.Reason)
	}
	row := findBatch(t, st.Batches, "transcode")
	if row.Stage != "transcode" || row.Done != 72 || row.Total != 100 || row.Status != "running" || row.Stale {
		t.Fatalf("k8s_batch_status: fallback row = %+v", row)
	}
	got := readArgsLog(t, argsLog)
	if len(got) != 2 {
		t.Fatalf("k8s_batch_status: argv = %v, want get pods + exec cat", got)
	}
	if !strings.Contains(got[0], "get pods -n lab -o json --selector app=dsv") {
		t.Fatalf("k8s_batch_status: pods query argv shape = %q", got[0])
	}
	// ONLY-cat verb, argv-whitelisted path, per-read pod resolution.
	if !strings.Contains(got[1], "exec worker-1 -n lab -- cat /cpfs/ylzhang/batches/status.json") {
		t.Fatalf("k8s_batch_status: exec argv shape = %q (cat is the ONLY whitelisted verb)", got[1])
	}
}

func TestK8sBatchStatusPodFallbackRefusalClasses(t *testing.T) {
	for _, tc := range []struct {
		name     string
		podsList string
		want     string
	}{
		{"none running", `{"kind":"List","items":[{"kind":"Pod","metadata":{"name":"done-0","namespace":"lab"},"status":{"phase":"Succeeded"}}]}`, "pod_not_found"},
		{"multiple running", `{"kind":"List","items":[{"kind":"Pod","metadata":{"name":"worker-1","namespace":"lab"},"status":{"phase":"Running"}},{"kind":"Pod","metadata":{"name":"worker-2","namespace":"lab"},"status":{"phase":"Running"}}]}`, "ambiguous_pod"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := initRepo(t)
			home := t.TempDir()
			t.Setenv("HOME", home)
			writePrefs(t, home, "k8s_namespace: lab\nk8s_job_selector: app=dsv\nk8s_batch_dir: /cpfs/ylzhang/batches")
			argsLog := installMockKubectlDispatch(t, []mockKubectlRule{
				{"get pods", mockOkBody(tc.podsList)},
				{"exec", "cat <<'STATUS_JSON'\n{}\nSTATUS_JSON\n"},
			})

			rig := startRig(t, root)
			defer rig.stop(t)

			st := consumeK8sBatchStatus(t, rig, root)
			if !st.Available {
				t.Fatalf("k8s_batch_status: refusal classes stay available, got %q", st.Reason)
			}
			row := findBatch(t, st.Batches, "status.json")
			if row.Reason != tc.want {
				t.Fatalf("k8s_batch_status: row = %+v, want %s", row, tc.want)
			}
			// Deterministic refusal: NEVER exec a guessed pod (Sol B4).
			for _, line := range readArgsLog(t, argsLog) {
				if strings.Contains(line, " exec ") {
					t.Fatalf("k8s_batch_status: exec'd despite %s: %v", tc.want, line)
				}
			}
		})
	}
}

func TestK8sBatchStatusRowCapDeclaresTruncation(t *testing.T) {
	root := initRepo(t)
	home := t.TempDir()
	t.Setenv("HOME", home)
	dir := t.TempDir()
	now := time.Now().Unix()
	for i := 0; i < k8sBatchRowCap+5; i++ {
		writeStatusFile(t, dir, fmt.Sprintf("b-%02d.json", i), fmt.Sprintf("batch-%02d", i), "s", "running", 1, 10, i, 0, 1.0, now-int64(i))
	}
	writePrefs(t, home, "k8s_batch_dir: "+dir)

	rig := startRig(t, root)
	defer rig.stop(t)

	st := consumeK8sBatchStatus(t, rig, root)
	if len(st.Batches) != k8sBatchRowCap {
		t.Fatalf("k8s_batch_status: got %d rows, want the %d cap", len(st.Batches), k8sBatchRowCap)
	}
	if !st.Truncated {
		t.Fatal("k8s_batch_status: the row cap must declare truncation")
	}
}

func TestK8sBatchStatusLocalEmptyDirAnswersEmpty(t *testing.T) {
	root := initRepo(t)
	home := t.TempDir()
	t.Setenv("HOME", home)
	dir := t.TempDir()
	writePrefs(t, home, "k8s_batch_dir: "+dir)

	rig := startRig(t, root)
	defer rig.stop(t)

	st := consumeK8sBatchStatus(t, rig, root)
	if !st.Available {
		t.Fatalf("k8s_batch_status: an empty dir is healthy, got %q", st.Reason)
	}
	if len(st.Batches) != 0 {
		t.Fatalf("k8s_batch_status: rows = %+v, want none", st.Batches)
	}
}

// A local *file* (not dir) named as the pref is an unreadable dir —
// the fallback branches engage (k8s-configured here answers
// pod_not_found, proving the read was NOT local).
func TestK8sBatchStatusDirIsNotADir(t *testing.T) {
	root := initRepo(t)
	home := t.TempDir()
	t.Setenv("HOME", home)
	notDir := filepath.Join(t.TempDir(), "status.json")
	if err := os.WriteFile(notDir, []byte("{}"), 0o644); err != nil {
		t.Fatal(err)
	}
	writePrefs(t, home, "k8s_namespace: lab\nk8s_job_selector: app=dsv\nk8s_batch_dir: "+notDir)
	installMockKubectlDispatch(t, []mockKubectlRule{
		{"get pods", mockOkBody(`{"kind":"List","items":[]}`)},
	})

	rig := startRig(t, root)
	defer rig.stop(t)

	st := consumeK8sBatchStatus(t, rig, root)
	if !st.Available {
		t.Fatalf("k8s_batch_status: got %q", st.Reason)
	}
	row := findBatch(t, st.Batches, "status.json")
	if row.Reason != "pod_not_found" {
		t.Fatalf("k8s_batch_status: row = %+v, want pod_not_found via the fallback", row)
	}
}

// Malformed JSON files stay VISIBLE with a reason (the degradation
// contract at file granularity), and json.Unmarshal field-matching is
// exercised against the wire struct one more time.
func TestK8sBatchStatusMalformedFileKeepsReasonRow(t *testing.T) {
	root := initRepo(t)
	home := t.TempDir()
	t.Setenv("HOME", home)
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "broken.json"), []byte("{not json"), 0o644); err != nil {
		t.Fatal(err)
	}
	writePrefs(t, home, "k8s_batch_dir: "+dir)

	rig := startRig(t, root)
	defer rig.stop(t)

	st := consumeK8sBatchStatus(t, rig, root)
	row := findBatch(t, st.Batches, "broken.json")
	if row.Reason != "unparseable" {
		t.Fatalf("k8s_batch_status: row = %+v, want unparseable", row)
	}
}

// The wire shape round-trips: a response serializes exactly the protocol
// fields the GUI reads (feature catches an accidental json tag rename).
func TestK8sBatchStatusWireShape(t *testing.T) {
	root := initRepo(t)
	home := t.TempDir()
	t.Setenv("HOME", home)
	dir := t.TempDir()
	now := time.Now().Unix()
	writeStatusFile(t, dir, "transcode.json", "transcode", "push", "running", 1, 100, 25, 0, 2.0, now-5)
	writePrefs(t, home, "k8s_batch_dir: "+dir)

	rig := startRig(t, root)
	defer rig.stop(t)

	st := consumeK8sBatchStatus(t, rig, root)
	blob, err := json.Marshal(st)
	if err != nil {
		t.Fatal(err)
	}
	var probe map[string]any
	if err := json.Unmarshal(blob, &probe); err != nil {
		t.Fatal(err)
	}
	if probe["available"] != true {
		t.Fatalf("k8s_batch_status: wire payload = %s", blob)
	}
	rows, ok := probe["batches"].([]any)
	if !ok || len(rows) != 1 {
		t.Fatalf("k8s_batch_status: batches wire field = %s", blob)
	}
	row := rows[0].(map[string]any)
	for _, key := range []string{"batch", "stage", "total", "done", "errs", "rate_per_min", "updated_unix", "status", "stale"} {
		if _, ok := row[key]; !ok {
			t.Fatalf("k8s_batch_status: wire row missing %q: %s", key, blob)
		}
	}
}
