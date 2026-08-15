package ipc

// Terminal harness mirroring handlePanelQuery's fanout (review: models +
// home-scoped read-only FS tools) for running a /panel-grade review
// without the GUI. Diagnostic only — default-skipped, never part of the
// suite: ODO_PANEL_LIVE=1 ODO_PANEL_PROMPT=<file> ODO_PANEL_OUT=<file>
// go test ./internal/ipc/ -run TestPanelLive -v
import (
	"context"
	"fmt"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/yingliang-zhang/odo/internal/adapter"
	"github.com/yingliang-zhang/odo/internal/moa"
)

func TestPanelLive(t *testing.T) {
	if os.Getenv("ODO_PANEL_LIVE") == "" {
		t.Skip("diagnostic harness; set ODO_PANEL_LIVE=1")
	}
	data, err := os.ReadFile(os.Getenv("ODO_PANEL_PROMPT"))
	if err != nil {
		t.Fatal(err)
	}
	models := parseReviewModels(adapter.LoadPrefsRaw("review"))
	if len(models) == 0 {
		t.Fatal("prefs.md has no review: line")
	}
	client := moa.NewClientFromEnv("", "")
	exec := newFSToolExecutor()
	system := "You are an expert advisor. Provide a thorough, independent analysis." +
		"\n\nYou have read-only tools over the user's files: read_file, grep, glob. " +
		exec.describeScope() +
		" Use them to ground your answer in the actual files whenever the question touches code or documents — do not ask the user to paste content."
	ctx, cancel := context.WithTimeout(context.Background(), 12*time.Minute)
	defer cancel()
	results := make([]PanelResult, len(models))
	var wg sync.WaitGroup
	for i, m := range models {
		wg.Add(1)
		go func() {
			defer wg.Done()
			label := m.model + "@" + m.provider
			resp, calls, err := client.QueryWithTools(ctx, m.model, system, string(data), moaFSTools(), exec.Execute, 0)
			if err != nil {
				results[i] = PanelResult{Model: label, Error: err.Error(), ToolCalls: calls}
				return
			}
			results[i] = PanelResult{
				Model: label, Text: resp.Text, ToolCalls: calls,
				Truncated: resp.Truncated, Budget: resp.Budget,
				OutputTokens: resp.OutputTokens, Escalations: resp.Escalations,
				RequestSHA16: resp.RequestSHA16, RequestBytes: resp.RequestBytes,
			}
		}()
	}
	wg.Wait()
	out := formatPanelResults(results)
	for _, r := range results {
		fmt.Printf("== %s | tokens=%d truncated=%v tools=%d err=%q\n", r.Model, r.OutputTokens, r.Truncated, len(r.ToolCalls), r.Error)
	}
	if outFile := os.Getenv("ODO_PANEL_OUT"); outFile != "" {
		if err := os.WriteFile(outFile, []byte(out), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}
