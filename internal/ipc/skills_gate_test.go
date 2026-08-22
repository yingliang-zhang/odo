package ipc

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// M9 (Skill Distillation + Three-Tier Gating) unit tests: classifyGate
// tier logic, composeSkillMD frontmatter, and vetLearnerOutput procedure
// vetting (slugify, cap-3, dedupe, body-cap, keywords, description-sanitize).

// --- classifyGate tests ---

func TestClassifyGate_AllReject_AutoDiscard(t *testing.T) {
	reviews := []ReviewResult{
		{Model: "m1", Verdict: "reject"},
		{Model: "m2", Verdict: "reject"},
		{Model: "m3", Verdict: "reject"},
	}
	if got := classifyGate(reviews, 3); got != "auto_discard" {
		t.Errorf("classifyGate(3/3 reject) = %q, want auto_discard", got)
	}
}

func TestClassifyGate_TwoReject_HumanGate(t *testing.T) {
	reviews := []ReviewResult{
		{Model: "m1", Verdict: "reject"},
		{Model: "m2", Verdict: "reject"},
		{Model: "m3", Verdict: "accept"},
	}
	if got := classifyGate(reviews, 3); got != "human_gate" {
		t.Errorf("classifyGate(2/3 reject) = %q, want human_gate", got)
	}
}

func TestClassifyGate_ZeroModels_HumanGate(t *testing.T) {
	if got := classifyGate(nil, 0); got != "human_gate" {
		t.Errorf("classifyGate(0 models) = %q, want human_gate (not auto_discard)", got)
	}
	if got := classifyGate([]ReviewResult{}, 0); got != "human_gate" {
		t.Errorf("classifyGate(empty reviews, 0 models) = %q, want human_gate", got)
	}
}

func TestClassifyGate_AllNeedsFixes_HumanGate(t *testing.T) {
	reviews := []ReviewResult{
		{Model: "m1", Verdict: "needs_fixes"},
		{Model: "m2", Verdict: "needs_fixes"},
		{Model: "m3", Verdict: "needs_fixes"},
	}
	if got := classifyGate(reviews, 3); got != "human_gate" {
		t.Errorf("classifyGate(3/3 needs_fixes) = %q, want human_gate", got)
	}
}

func TestClassifyGate_AllAccept_HumanGate(t *testing.T) {
	// auto_accept is deferred in MVP — 3/3 accept still goes to human_gate.
	reviews := []ReviewResult{
		{Model: "m1", Verdict: "accept"},
		{Model: "m2", Verdict: "accept"},
		{Model: "m3", Verdict: "accept"},
	}
	if got := classifyGate(reviews, 3); got != "human_gate" {
		t.Errorf("classifyGate(3/3 accept) = %q, want human_gate (auto_accept deferred)", got)
	}
}

func TestClassifyGate_OneRejectTwoAccept_HumanGate(t *testing.T) {
	reviews := []ReviewResult{
		{Model: "m1", Verdict: "reject"},
		{Model: "m2", Verdict: "accept"},
		{Model: "m3", Verdict: "accept"},
	}
	if got := classifyGate(reviews, 3); got != "human_gate" {
		t.Errorf("classifyGate(1/3 reject) = %q, want human_gate", got)
	}
}

// --- composeSkillMD tests ---

func TestComposeSkillMD_ParseableFrontmatter(t *testing.T) {
	md := composeSkillMD("run-tests", "Use when claiming done.", []string{"test", "commit"}, "# Run Tests\n\n1. Run `go test`")
	name, desc, origin, keywords, body := parseFrontmatter(md)
	if name != "run-tests" {
		t.Errorf("name = %q, want run-tests", name)
	}
	if desc != "Use when claiming done." {
		t.Errorf("desc = %q", desc)
	}
	if origin != "agent-authored" {
		t.Errorf("origin = %q, want agent-authored", origin)
	}
	if len(keywords) != 2 || keywords[0] != "test" || keywords[1] != "commit" {
		t.Errorf("keywords = %v, want [test commit]", keywords)
	}
	if !strings.HasPrefix(body, "# Run Tests") {
		t.Errorf("body = %q, want # Run Tests...", body[:min(30, len(body))])
	}
}

func TestComposeSkillMD_SanitizesName(t *testing.T) {
	// Name with control chars should be sanitized to single-line.
	md := composeSkillMD("test\nskill", "desc", []string{"k"}, "body")
	name, _, _, _, _ := parseFrontmatter(md)
	if strings.Contains(name, "\n") {
		t.Errorf("name contains newline after sanitize: %q", name)
	}
}

// --- vetLearnerOutput procedure tests ---

func makeLearnerResult(procedures []struct {
	Name        string   `json:"name"`
	Description string   `json:"description"`
	Keywords    []string `json:"keywords"`
	Body        string   `json:"body"`
}) *learnerResult {
	return &learnerResult{Procedures: procedures}
}

func TestVetProcedures_Slugify(t *testing.T) {
	res := makeLearnerResult([]struct {
		Name        string   `json:"name"`
		Description string   `json:"description"`
		Keywords    []string `json:"keywords"`
		Body        string   `json:"body"`
	}{
		{Name: "Run Tests Before Commit!", Description: "Use when done.", Keywords: []string{"test"}, Body: "# Run Tests"},
	})
	_, procedures, _, stats := vetLearnerOutput(res, "epoch-1.md", "", t.TempDir(), true)
	if len(procedures) != 1 {
		t.Fatalf("procedures = %d, want 1: %+v", len(procedures), procedures)
	}
	if procedures[0].Name != "run-tests-before-commit" {
		t.Errorf("name = %q, want run-tests-before-commit", procedures[0].Name)
	}
	if stats.ProceduresKept != 1 || stats.ProceduresDropped != 0 {
		t.Errorf("stats = kept=%d dropped=%d, want kept=1 dropped=0", stats.ProceduresKept, stats.ProceduresDropped)
	}
}

func TestVetProcedures_EmptyNameDropped(t *testing.T) {
	res := makeLearnerResult([]struct {
		Name        string   `json:"name"`
		Description string   `json:"description"`
		Keywords    []string `json:"keywords"`
		Body        string   `json:"body"`
	}{
		{Name: "!!!", Description: "d", Keywords: []string{"k"}, Body: "b"},
	})
	_, procedures, _, stats := vetLearnerOutput(res, "epoch-1.md", "", t.TempDir(), true)
	if len(procedures) != 0 {
		t.Errorf("procedures = %d, want 0 (name slugifies to empty)", len(procedures))
	}
	if stats.ProceduresDropped != 1 {
		t.Errorf("dropped = %d, want 1", stats.ProceduresDropped)
	}
}

func TestVetProcedures_Cap3(t *testing.T) {
	procs := make([]struct {
		Name        string   `json:"name"`
		Description string   `json:"description"`
		Keywords    []string `json:"keywords"`
		Body        string   `json:"body"`
	}, 5)
	for i := range procs {
		procs[i] = struct {
			Name        string   `json:"name"`
			Description string   `json:"description"`
			Keywords    []string `json:"keywords"`
			Body        string   `json:"body"`
		}{
			Name:        "skill-" + string(rune('a'+i)),
			Description: "desc",
			Keywords:    []string{"k"},
			Body:        "body",
		}
	}
	res := makeLearnerResult(procs)
	_, procedures, _, stats := vetLearnerOutput(res, "epoch-1.md", "", t.TempDir(), true)
	if len(procedures) != 3 {
		t.Errorf("procedures = %d, want 3 (cap-3)", len(procedures))
	}
	if stats.ProceduresKept != 3 || stats.ProceduresDropped != 2 {
		t.Errorf("stats = kept=%d dropped=%d, want kept=3 dropped=2", stats.ProceduresKept, stats.ProceduresDropped)
	}
}

func TestVetProcedures_Dedupe(t *testing.T) {
	res := makeLearnerResult([]struct {
		Name        string   `json:"name"`
		Description string   `json:"description"`
		Keywords    []string `json:"keywords"`
		Body        string   `json:"body"`
	}{
		{Name: "my-skill", Description: "d", Keywords: []string{"k"}, Body: "b"},
		{Name: "my-skill", Description: "d2", Keywords: []string{"k2"}, Body: "b2"},
	})
	_, procedures, _, stats := vetLearnerOutput(res, "epoch-1.md", "", t.TempDir(), true)
	if len(procedures) != 1 {
		t.Errorf("procedures = %d, want 1 (dedupe)", len(procedures))
	}
	if procedures[0].Description != "d" {
		t.Errorf("first wins: desc = %q, want d", procedures[0].Description)
	}
	if stats.ProceduresDropped != 1 {
		t.Errorf("dropped = %d, want 1 (duplicate)", stats.ProceduresDropped)
	}
}

func TestVetProcedures_BodyCap(t *testing.T) {
	longBody := strings.Repeat("x", procedureBodyCap+1)
	res := makeLearnerResult([]struct {
		Name        string   `json:"name"`
		Description string   `json:"description"`
		Keywords    []string `json:"keywords"`
		Body        string   `json:"body"`
	}{
		{Name: "big-skill", Description: "d", Keywords: []string{"k"}, Body: longBody},
	})
	_, procedures, _, stats := vetLearnerOutput(res, "epoch-1.md", "", t.TempDir(), true)
	if len(procedures) != 0 {
		t.Errorf("procedures = %d, want 0 (body over cap)", len(procedures))
	}
	if stats.ProceduresDropped != 1 {
		t.Errorf("dropped = %d, want 1 (body cap)", stats.ProceduresDropped)
	}
}

func TestVetProcedures_BadKeywords(t *testing.T) {
	res := makeLearnerResult([]struct {
		Name        string   `json:"name"`
		Description string   `json:"description"`
		Keywords    []string `json:"keywords"`
		Body        string   `json:"body"`
	}{
		{Name: "bad-kw", Description: "d", Keywords: []string{"has space"}, Body: "b"},
		{Name: "empty-kw", Description: "d", Keywords: []string{}, Body: "b"},
		{Name: "comma-kw", Description: "d", Keywords: []string{"has,comma"}, Body: "b"},
	})
	_, procedures, _, stats := vetLearnerOutput(res, "epoch-1.md", "", t.TempDir(), true)
	if len(procedures) != 0 {
		t.Errorf("procedures = %d, want 0 (all bad keywords)", len(procedures))
	}
	if stats.ProceduresDropped != 3 {
		t.Errorf("dropped = %d, want 3", stats.ProceduresDropped)
	}
}

func TestVetProcedures_DescriptionSanitize(t *testing.T) {
	res := makeLearnerResult([]struct {
		Name        string   `json:"name"`
		Description string   `json:"description"`
		Keywords    []string `json:"keywords"`
		Body        string   `json:"body"`
	}{
		{Name: "good", Description: "line1\nline2\ttabbed", Keywords: []string{"k"}, Body: "b"},
	})
	_, procedures, _, _ := vetLearnerOutput(res, "epoch-1.md", "", t.TempDir(), true)
	if len(procedures) != 1 {
		t.Fatalf("procedures = %d, want 1", len(procedures))
	}
	if strings.Contains(procedures[0].Description, "\n") {
		t.Errorf("description contains newline: %q", procedures[0].Description)
	}
	if strings.Contains(procedures[0].Description, "\t") {
		t.Errorf("description contains tab: %q", procedures[0].Description)
	}
}

func TestVetProcedures_EmptyDescriptionDropped(t *testing.T) {
	res := makeLearnerResult([]struct {
		Name        string   `json:"name"`
		Description string   `json:"description"`
		Keywords    []string `json:"keywords"`
		Body        string   `json:"body"`
	}{
		{Name: "no-desc", Description: "", Keywords: []string{"k"}, Body: "b"},
	})
	_, procedures, _, stats := vetLearnerOutput(res, "epoch-1.md", "", t.TempDir(), true)
	if len(procedures) != 0 {
		t.Errorf("procedures = %d, want 0 (empty description)", len(procedures))
	}
	if stats.ProceduresDropped != 1 {
		t.Errorf("dropped = %d, want 1", stats.ProceduresDropped)
	}
}

func TestVetProcedures_ScanSkillsConflict(t *testing.T) {
	root := t.TempDir()
	// Seed an existing skill in the project dir.
	skillDir := filepath.Join(root, ".odo", "skills")
	if err := os.MkdirAll(skillDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(skillDir, "existing-skill.md"), []byte("---\nname: existing-skill\ndescription: Already here.\n---\n\nBody"), 0o644); err != nil {
		t.Fatal(err)
	}
	res := makeLearnerResult([]struct {
		Name        string   `json:"name"`
		Description string   `json:"description"`
		Keywords    []string `json:"keywords"`
		Body        string   `json:"body"`
	}{
		{Name: "existing-skill", Description: "New version.", Keywords: []string{"k"}, Body: "b"},
	})
	_, procedures, _, _ := vetLearnerOutput(res, "epoch-1.md", "", root, true)
	if len(procedures) != 1 {
		t.Fatalf("procedures = %d, want 1 (conflict still kept, just flagged)", len(procedures))
	}
	if !strings.Contains(procedures[0].Contradicts, "overwrites existing skill: existing-skill") {
		t.Errorf("contradicts = %q, want overwrites existing skill", procedures[0].Contradicts)
	}
}

// TestVetProcedures_OffByDefault pins the skillsDistillEnabled=false vet
// contract: the prompt never offered the procedures array, so one arriving
// anyway is out-of-contract input — dropped wholesale, NOT counted as gate
// drops (the stats count deliberation, never unrequested output).
func TestVetProcedures_OffByDefault(t *testing.T) {
	res := makeLearnerResult([]struct {
		Name        string   `json:"name"`
		Description string   `json:"description"`
		Keywords    []string `json:"keywords"`
		Body        string   `json:"body"`
	}{
		{Name: "run-tests", Description: "Use when verifying.", Keywords: []string{"test"}, Body: "# Run Tests"},
	})
	_, procedures, _, stats := vetLearnerOutput(res, "epoch-1.md", "", t.TempDir(), false)
	if len(procedures) != 0 {
		t.Errorf("procedures = %d, want 0 (contract omitted — no vetted skills)", len(procedures))
	}
	if stats.ProceduresKept != 0 || stats.ProceduresDropped != 0 {
		t.Errorf("stats = kept %d dropped %d, want 0/0 (unrequested output is not a gate drop)",
			stats.ProceduresKept, stats.ProceduresDropped)
	}
}

// --- splitSkillProposals test ---

func TestSplitSkillProposals(t *testing.T) {
	proposals := []MemoryProposal{
		{Target: "memory.md", Rule: "r1"},
		{Target: "skills", Rule: "s1", Name: "skill-1"},
		{Target: "user.md", Rule: "u1"},
		{Target: "skills", Rule: "s2", Name: "skill-2"},
	}
	nonSkills, skills := splitSkillProposals(proposals)
	if len(nonSkills) != 2 {
		t.Errorf("nonSkills = %d, want 2", len(nonSkills))
	}
	if len(skills) != 2 {
		t.Errorf("skills = %d, want 2", len(skills))
	}
	for _, p := range skills {
		if p.Target != "skills" {
			t.Errorf("skills contains non-skills target: %q", p.Target)
		}
	}
}

// --- slugifySkillName tests ---

func TestSlugifySkillName(t *testing.T) {
	cases := []struct{ in, want string }{
		{"Run Tests", "run-tests"},
		{"run-tests-before-commit", "run-tests-before-commit"},
		{"My Cool Skill!", "my-cool-skill"},
		{"  spaced  name  ", "spaced-name"},
		{"UPPER_CASE", "upper-case"},
		{"a_b c-d", "a-b-c-d"},
		{"!!!", ""},
		{"123 abc", "123-abc"},
		{"multiple---dashes", "multiple-dashes"},
	}
	for _, c := range cases {
		if got := slugifySkillName(c.in); got != c.want {
			t.Errorf("slugifySkillName(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

// --- isSingleTokenKeyword tests ---

func TestIsSingleTokenKeyword(t *testing.T) {
	if !isSingleTokenKeyword("test") {
		t.Error("isSingleTokenKeyword(test) = false, want true")
	}
	if isSingleTokenKeyword("has space") {
		t.Error("isSingleTokenKeyword(has space) = true, want false")
	}
	if isSingleTokenKeyword("has,comma") {
		t.Error("isSingleTokenKeyword(has,comma) = true, want false")
	}
	if isSingleTokenKeyword("") {
		t.Error("isSingleTokenKeyword() = true, want false")
	}
	if isSingleTokenKeyword(`"quoted"`) {
		t.Error("isSingleTokenKeyword(quoted) = true, want false")
	}
}
