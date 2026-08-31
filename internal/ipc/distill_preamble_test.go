package ipc

import "testing"

func TestStripPreamble(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name  string
		input string
		want  string
	}{
		{
			name:  "clean note — no preamble",
			input: "# Summary\n\nBody text.\n",
			want:  "# Summary\n\nBody text.\n",
		},
		{
			name:  "preamble before heading",
			input: "Some scratch text.\nMore preamble.\n# Summary\n\nBody.\n",
			want:  "# Summary\n\nBody.\n",
		},
		{
			name:  "Vietnamese tool fragments before heading",
			input: "design_moa.go không có trong repo chính.\nKiểm tra workdir.\n# Session Summary\n\nActual content.\n",
			want:  "# Session Summary\n\nActual content.\n",
		},
		{
			name:  "no heading at all — returned as-is",
			input: "Just some text without a heading.\n",
			want:  "Just some text without a heading.\n",
		},
		{
			name:  "empty string",
			input: "",
			want:  "",
		},
		{
			name:  "heading on first line",
			input: "# Title\ncontent\n",
			want:  "# Title\ncontent\n",
		},
		{
			name:  "agent-memory path preamble",
			input: "Summary — also persisted to agent-memory/summaries/foo:\n\n# Pipeline chip revision\n\nBody.\n",
			want:  "# Pipeline chip revision\n\nBody.\n",
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := stripPreamble(c.input)
			if got != c.want {
				t.Errorf("stripPreamble(%q) = %q, want %q", c.input, got, c.want)
			}
		})
	}
}
