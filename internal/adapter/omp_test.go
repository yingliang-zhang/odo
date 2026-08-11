package adapter

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestParseToolCalls(t *testing.T) {
	text := `I'll look at the file first.
⏺ read(file_path="hello.txt")
  ⇐ 1  // hello.txt
⏺ write(file_path="hello.txt", content="hello)")
  ⇐ File written successfully
⏺ bash(command="ls -la")
  ⇐ total 24`

	events := parseToolCalls(text)
	if len(events) != 7 {
		t.Fatalf("expected 7 events, got %d: %#v", len(events), events)
	}

	want := []struct {
		typ string
		key string
	}{
		{"agent_text", "text"},
		{"agent_tool_call", "tool"},
		{"agent_tool_result", "tool"},
		{"agent_tool_call", "tool"},
		{"agent_tool_result", "tool"},
		{"agent_tool_call", "tool"},
		{"agent_tool_result", "tool"},
	}
	for i, w := range want {
		if events[i].Type != w.typ {
			t.Errorf("event %d: type = %q, want %q", i, events[i].Type, w.typ)
		}
		if _, ok := events[i].Payload[w.key]; !ok {
			t.Errorf("event %d: payload missing key %q: %#v", i, w.key, events[i].Payload)
		}
	}

	if got := events[0].Payload["text"]; got != "I'll look at the file first." {
		t.Errorf("prefix text = %q", got)
	}
	if got := events[1].Payload["tool"]; got != "read" {
		t.Errorf("call 1 tool = %q", got)
	}
	if got := events[1].Payload["args"]; got != `file_path="hello.txt"` {
		t.Errorf("call 1 args = %q", got)
	}
	if got := events[2].Payload["result"]; got != "1  // hello.txt" {
		t.Errorf("result 1 = %q", got)
	}
	// Greedy args capture keeps a ")" inside the argument list.
	if got := events[3].Payload["args"]; got != `file_path="hello.txt", content="hello)"` {
		t.Errorf("call 2 args = %q", got)
	}
	if got := events[5].Payload["tool"]; got != "bash" {
		t.Errorf("call 3 tool = %q", got)
	}
	// Keys must match ADR-0002 exactly: tool/args on call, tool/result on result.
	for _, i := range []int{1, 3, 5} {
		if _, bad := events[i].Payload["name"]; bad {
			t.Errorf("event %d uses forbidden key \"name\"", i)
		}
		if _, bad := events[i].Payload["input"]; bad {
			t.Errorf("event %d uses forbidden key \"input\"", i)
		}
	}
}

func TestParseToolCallsResultAttribution(t *testing.T) {
	// A result line lands with the tool call that precedes it, even when the
	// second call has no result.
	text := "⏺ bash(command=\"ls\")\n  ⇐ total 24\n⏺ read(file_path=\"x.go\")\n"

	events := parseToolCalls(text)
	if len(events) != 3 {
		t.Fatalf("expected 3 events, got %d: %#v", len(events), events)
	}
	if events[0].Type != "agent_tool_call" || events[1].Type != "agent_tool_result" || events[2].Type != "agent_tool_call" {
		t.Fatalf("unexpected sequence: %#v", events)
	}
	if events[1].Payload["tool"] != "bash" || events[1].Payload["result"] != "total 24" {
		t.Errorf("result misattributed: %#v", events[1].Payload)
	}
}

func TestParseToolCallsFallback(t *testing.T) {
	if got := parseToolCalls("Just some prose, no tools at all."); got != nil {
		t.Errorf("expected nil for plain text, got %#v", got)
	}
	if got := parseToolCalls(""); got != nil {
		t.Errorf("expected nil for empty text, got %#v", got)
	}
	// A bare ⇐ line without a preceding ⏺ call is not a tool call.
	if got := parseToolCalls("  ⇐ orphan result"); got != nil {
		t.Errorf("expected nil for orphan result, got %#v", got)
	}
}

// TestCompactionOverlayArgs pins the per-model policy delivery: known models
// get a fixed-token overlay mirroring the Hermes profile ratios, unknown
// models stay on the global omp config untouched.
func TestCompactionOverlayArgs(t *testing.T) {
	dir := t.TempDir()

	t.Run("known model writes overlay and appends --config", func(t *testing.T) {
		args := compactionOverlayArgs("t9s/kimi-k3", dir, []string{"-x"})
		if len(args) != 2 || !strings.HasPrefix(args[1], "--config=") {
			t.Fatalf("args = %v, want --config appended", args)
		}
		data, err := os.ReadFile(filepath.Join(dir, "odo-compaction.yml"))
		if err != nil {
			t.Fatal(err)
		}
		want := "compaction:\n  thresholdTokens: 315000\n  thresholdPercent: -1\n"
		if string(data) != want {
			t.Errorf("overlay = %q, want %q", data, want)
		}
	})

	t.Run("deepseek gets its own ratio", func(t *testing.T) {
		args := compactionOverlayArgs("t9s/deepseek-v4-flash", dir, nil)
		if len(args) != 1 {
			t.Fatalf("args = %v", args)
		}
		data, _ := os.ReadFile(filepath.Join(dir, "odo-compaction.yml"))
		if !strings.Contains(string(data), "thresholdTokens: 600000") {
			t.Errorf("overlay = %q, want 600000 (0.60 × 1M)", data)
		}
	})

	t.Run("unknown model gets no overlay", func(t *testing.T) {
		empty := t.TempDir()
		args := compactionOverlayArgs("acme/pi-9", empty, []string{"-x"})
		if len(args) != 1 {
			t.Errorf("args = %v, want unchanged", args)
		}
		if _, err := os.Stat(filepath.Join(empty, "odo-compaction.yml")); !os.IsNotExist(err) {
			t.Error("unknown model must not write an overlay")
		}
	})
}

func TestResolveModelConfig(t *testing.T) {
	writePrefs := func(t *testing.T, content string) string {
		t.Helper()
		home := t.TempDir()
		if content != "" {
			dir := filepath.Join(home, ".odo")
			if err := os.MkdirAll(dir, 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(dir, "prefs.md"), []byte(content), 0o600); err != nil {
				t.Fatal(err)
			}
		}
		t.Setenv("HOME", home)
		return home
	}

	t.Run("missing prefs file falls back to defaults", func(t *testing.T) {
		writePrefs(t, "")
		a := NewOMP(t.TempDir())
		model, provider := a.resolveModelConfig()
		if model != "t9s/kimi-k3" || provider != "custom:sudo" {
			t.Errorf("got model=%q provider=%q", model, provider)
		}
	})

	t.Run("coding line parsed", func(t *testing.T) {
		writePrefs(t, "# Odo prefs\n\ncoding: acme/pi-9@anthropic\neditor: vim\n")
		a := NewOMP(t.TempDir())
		model, provider := a.resolveModelConfig()
		if model != "acme/pi-9" || provider != "custom:anthropic" {
			t.Errorf("got model=%q provider=%q", model, provider)
		}
	})

	t.Run("malformed coding line falls back", func(t *testing.T) {
		for _, content := range []string{
			"coding: no-at-sign\n",
			"coding: @provider-only\n",
			"coding: model-only@\n",
			"coding:\n",
		} {
			writePrefs(t, content)
			a := NewOMP(t.TempDir())
			model, provider := a.resolveModelConfig()
			if model != "t9s/kimi-k3" || provider != "custom:sudo" {
				t.Errorf("content %q: got model=%q provider=%q", content, model, provider)
			}
		}
	})

	t.Run("prefs edit re-read on next Start", func(t *testing.T) {
		home := writePrefs(t, "coding: acme/pi-9@anthropic\n")
		a := NewOMP(t.TempDir())
		a.resolveModelConfig()
		if err := os.WriteFile(filepath.Join(home, ".odo", "prefs.md"), []byte("coding: v2/model-x@openai\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		model, provider := a.resolveModelConfig()
		if model != "v2/model-x" || provider != "custom:openai" {
			t.Errorf("got model=%q provider=%q", model, provider)
		}
	})
}
