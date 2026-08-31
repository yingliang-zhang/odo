package ipc

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// M8 (Skills) unit tests: parseFrontmatter, matchSkills, formatSkillsForInjection,
// scanSkills. Covers edge cases including BOM, no-trailing-newline, block-list
// keywords, and injection cap boundary.

func TestParseFrontmatter_Standard(t *testing.T) {
	t.Parallel()
	content := "---\nname: my-skill\ndescription: Use when testing.\nkeywords: [tdd, test, refactor]\norigin: ported\n---\n\n# My Skill\n\nBody text here.\n"
	name, desc, origin, keywords, body := parseFrontmatter(content)
	if name != "my-skill" {
		t.Errorf("name = %q, want %q", name, "my-skill")
	}
	if desc != "Use when testing." {
		t.Errorf("desc = %q", desc)
	}
	if origin != "ported" {
		t.Errorf("origin = %q, want %q", origin, "ported")
	}
	if len(keywords) != 3 || keywords[0] != "tdd" || keywords[2] != "refactor" {
		t.Errorf("keywords = %v", keywords)
	}
	if body != "# My Skill\n\nBody text here." {
		t.Errorf("body = %q", body)
	}
}

func TestParseFrontmatter_BlockListKeywords(t *testing.T) {
	t.Parallel()
	content := "---\nname: list-skill\nkeywords:\n  - alpha\n  - beta\n  - gamma\n---\n\nBody\n"
	_, _, _, keywords, _ := parseFrontmatter(content)
	if len(keywords) != 3 || keywords[0] != "alpha" || keywords[2] != "gamma" {
		t.Errorf("keywords = %v, want [alpha beta gamma]", keywords)
	}
}

func TestParseFrontmatter_NoFrontmatter(t *testing.T) {
	t.Parallel()
	content := "# Just a title\n\nNo frontmatter here."
	name, _, origin, keywords, body := parseFrontmatter(content)
	if name != "" {
		t.Errorf("name = %q, want empty", name)
	}
	if origin != "human" {
		t.Errorf("origin = %q, want human", origin)
	}
	if len(keywords) != 0 {
		t.Errorf("keywords = %v, want empty", keywords)
	}
	if body != content {
		t.Errorf("body should be trimmed content")
	}
}

func TestParseFrontmatter_BOMPrefix(t *testing.T) {
	t.Parallel()
	content := "\uFEFF---\nname: bom-skill\ndescription: BOM test\n---\n\nBody"
	name, _, _, _, _ := parseFrontmatter(content)
	if name != "bom-skill" {
		t.Errorf("name = %q, want bom-skill (BOM should be stripped)", name)
	}
}

func TestParseFrontmatter_NoTrailingNewline(t *testing.T) {
	t.Parallel()
	content := "---\nname: eof-skill\ndescription: EOF test\n---"
	name, _, _, _, _ := parseFrontmatter(content)
	if name != "eof-skill" {
		t.Errorf("name = %q, want eof-skill (closing --- without trailing newline should match)", name)
	}
}

func TestParseFrontmatter_EmptyKeywords(t *testing.T) {
	t.Parallel()
	content := "---\nname: no-kw\ndescription: No keywords\nkeywords: []\n---\n\nBody\n"
	_, _, _, keywords, _ := parseFrontmatter(content)
	if len(keywords) != 0 {
		t.Errorf("keywords = %v, want empty", keywords)
	}
}

func TestParseFrontmatter_OriginDefault(t *testing.T) {
	t.Parallel()
	content := "---\nname: test\n---\n\nBody"
	_, _, origin, _, _ := parseFrontmatter(content)
	if origin != "human" {
		t.Errorf("origin = %q, want human (default)", origin)
	}
}

func TestParseFrontmatter_QuotedValues(t *testing.T) {
	t.Parallel()
	content := "---\nname: \"quoted-name\"\ndescription: \"Use when quoted\"\n---\n\nBody"
	name, desc, _, _, _ := parseFrontmatter(content)
	if name != "quoted-name" {
		t.Errorf("name = %q, want quoted-name (quotes stripped)", name)
	}
	if desc != "Use when quoted" {
		t.Errorf("desc = %q", desc)
	}
}

func TestParseFrontmatter_CRLF(t *testing.T) {
	t.Parallel()
	content := "---\r\nname: crlf-skill\r\ndescription: CRLF test\r\n---\r\n\r\nBody\r\n"
	name, _, _, _, _ := parseFrontmatter(content)
	if name != "crlf-skill" {
		t.Errorf("name = %q, want crlf-skill (CRLF should be handled)", name)
	}
}

func TestMatchSkills_KeywordMatch(t *testing.T) {
	t.Parallel()
	entries := []skillEntry{
		{info: SkillInfo{Name: "tdd-workflow", Keywords: []string{"tdd", "test"}}},
		{info: SkillInfo{Name: "deploy-checklist", Keywords: []string{"deploy", "ship"}}},
	}
	matched := matchSkills("tdd test", entries)
	if len(matched) != 1 || matched[0].info.Name != "tdd-workflow" {
		t.Errorf("expected only tdd-workflow, got %v", matched)
	}
}

func TestMatchSkills_ScoringOrder(t *testing.T) {
	t.Parallel()
	entries := []skillEntry{
		{info: SkillInfo{Name: "low-match", Keywords: []string{"test"}}},         // score 2 (keyword)
		{info: SkillInfo{Name: "high-match", Keywords: []string{"test", "tdd"}}}, // score 4 (2 keywords)
	}
	matched := matchSkills("tdd test", entries)
	if len(matched) != 2 {
		t.Fatalf("expected 2 matched, got %d", len(matched))
	}
	if matched[0].info.Name != "high-match" {
		t.Errorf("expected high-match first (higher score), got %s", matched[0].info.Name)
	}
}

func TestMatchSkills_UnmatchedExcluded(t *testing.T) {
	t.Parallel()
	entries := []skillEntry{
		{info: SkillInfo{Name: "tdd-workflow", Keywords: []string{"tdd"}}},
		{info: SkillInfo{Name: "unrelated-skill", Keywords: []string{"cooking"}}},
	}
	matched := matchSkills("tdd", entries)
	if len(matched) != 1 || matched[0].info.Name != "tdd-workflow" {
		t.Errorf("expected only tdd-workflow, got %v", matched)
	}
}

func TestMatchSkills_EmptyQueryReturnsNil(t *testing.T) {
	t.Parallel()
	entries := []skillEntry{
		{info: SkillInfo{Name: "skill-1"}},
		{info: SkillInfo{Name: "skill-2"}},
	}
	matched := matchSkills("", entries)
	if len(matched) != 0 {
		t.Errorf("empty query should return nil, got %d entries", len(matched))
	}
}

func TestMatchSkills_StopWordOnlyQueryReturnsNil(t *testing.T) {
	t.Parallel()
	entries := []skillEntry{
		{info: SkillInfo{Name: "skill-1", Keywords: []string{"test"}}},
	}
	matched := matchSkills("how do I", entries)
	if len(matched) != 0 {
		t.Errorf("stop-word-only query should return nil, got %d entries", len(matched))
	}
}

func TestFormatSkillsForInjection_CapBoundary(t *testing.T) {
	t.Parallel()
	entries := []skillEntry{
		{info: SkillInfo{Name: "big-skill"}, body: string(make([]byte, 3000))},
		{info: SkillInfo{Name: "second-skill"}, body: string(make([]byte, 3000))},
		{info: SkillInfo{Name: "third-skill"}, body: string(make([]byte, 3000))},
	}
	// Cap at 8192 bytes. Each block is ~3020 bytes (header + body + separator).
	// Only 2 should fit; the 3rd must be cut on skill boundary.
	block, receipts := formatSkillsForInjection(entries, 8192)
	if len(receipts) > 2 {
		t.Errorf("expected at most 2 skills under 8KB cap, got %d", len(receipts))
	}
	if len(receipts) == 0 {
		t.Error("expected at least 1 skill")
	}
	if block == "" {
		t.Error("expected non-empty block")
	}
}

func TestFormatSkillsForInjection_EmptyInput(t *testing.T) {
	t.Parallel()
	block, receipts := formatSkillsForInjection(nil, 8192)
	if block != "" {
		t.Errorf("empty input should return empty block, got %q", block)
	}
	if len(receipts) != 0 {
		t.Errorf("empty input should return no receipts, got %d", len(receipts))
	}
}

func TestFormatSkillsForInjection_SingleUnderCap(t *testing.T) {
	t.Parallel()
	entries := []skillEntry{
		{info: SkillInfo{Name: "small-skill", Path: ".odo/skills/small.md"}, body: "Small body"},
	}
	block, receipts := formatSkillsForInjection(entries, 8192)
	if len(receipts) != 1 {
		t.Fatalf("expected 1 receipt, got %d", len(receipts))
	}
	if receipts[0].path != ".odo/skills/small.md" {
		t.Errorf("receipt path = %q, want .odo/skills/small.md", receipts[0].path)
	}
	if receipts[0].blockHash == "" {
		t.Error("block hash should not be empty")
	}
	if block == "" {
		t.Error("block should not be empty")
	}
}

func TestScanSkills_ProjectOverridesGlobal(t *testing.T) {
	tmpHome := t.TempDir()
	t.Setenv("HOME", tmpHome)
	tmpProject := t.TempDir()

	// Create a global skill
	globalDir := filepath.Join(tmpHome, ".odo", "skills")
	os.MkdirAll(globalDir, 0o755)
	os.WriteFile(filepath.Join(globalDir, "shared.md"), []byte("---\nname: shared\ndescription: global version\n---\n\nGlobal body"), 0o644)

	// Create a project skill with the same name
	projDir := filepath.Join(tmpProject, ".odo", "skills")
	os.MkdirAll(projDir, 0o755)
	os.WriteFile(filepath.Join(projDir, "shared.md"), []byte("---\nname: shared\ndescription: project version\n---\n\nProject body"), 0o644)

	entries := scanSkills(tmpProject)
	if len(entries) != 1 {
		t.Fatalf("expected 1 entry (override), got %d", len(entries))
	}
	if entries[0].info.Scope != "project" {
		t.Errorf("expected project scope (override), got %s", entries[0].info.Scope)
	}
	if entries[0].info.Description != "project version" {
		t.Errorf("expected project version body, got %s", entries[0].info.Description)
	}
}

func TestScanSkills_EmptyDirs(t *testing.T) {
	tmpHome := t.TempDir()
	t.Setenv("HOME", tmpHome)
	tmpProject := t.TempDir()
	entries := scanSkills(tmpProject)
	if len(entries) != 0 {
		t.Errorf("empty dirs should return no entries, got %d", len(entries))
	}
}

// TestHandleDeleteSkill_PathTraversal verifies that delete_skill rejects
// traversal attempts in the name field, mirroring the update_skill guard.
func TestHandleDeleteSkill_PathTraversal(t *testing.T) {
	root := initRepo(t)
	t.Setenv("HOME", t.TempDir())
	rig := startRig(t, root)
	defer rig.stop(t)

	// Create a target file to try to escape with traversal
	escapeDir := t.TempDir()
	target := filepath.Join(escapeDir, "escape.md")
	os.WriteFile(target, []byte("should not be deleted"), 0o644)

	// Attempt: delete a path outside skills dirs — should error because Base
	// strips the directory, leaving a non-existent name.
	resp := rig.callExpectErr(t, Request{
		Cmd:   CmdDeleteSkill,
		Name:  "../../../../" + strings.TrimPrefix(target, "/"),
		Scope: "project",
	})
	if resp.Error == "" {
		t.Error("expected error for traversal-stripped delete")
	}
	// Verify the file still exists
	if _, statErr := os.Stat(target); os.IsNotExist(statErr) {
		t.Error("target file was deleted — path traversal succeeded!")
	}
}

// TestHandleDeleteSkill_HappyPath verifies a normal delete works.
func TestHandleDeleteSkill_HappyPath(t *testing.T) {
	root := initRepo(t)
	t.Setenv("HOME", t.TempDir())
	rig := startRig(t, root)
	defer rig.stop(t)

	// Create a project skill to delete
	skillDir := filepath.Join(root, ".odo", "skills")
	os.MkdirAll(skillDir, 0o755)
	skillPath := filepath.Join(skillDir, "to-delete.md")
	os.WriteFile(skillPath, []byte("---\nname: to-delete\n---\n\nBody"), 0o644)

	resp := rig.call(t, Request{
		Cmd:   CmdDeleteSkill,
		Name:  "to-delete",
		Scope: "project",
	})
	if !resp.OK {
		t.Error("delete_skill should return OK")
	}
	if _, statErr := os.Stat(skillPath); !os.IsNotExist(statErr) {
		t.Error("skill file should be deleted")
	}
}

// TestHandleDeleteSkill_MissingName verifies name is required.
func TestHandleDeleteSkill_MissingName(t *testing.T) {
	root := initRepo(t)
	t.Setenv("HOME", t.TempDir())
	rig := startRig(t, root)
	defer rig.stop(t)

	resp := rig.callExpectErr(t, Request{
		Cmd:   CmdDeleteSkill,
		Scope: "project",
	})
	if resp.Error == "" {
		t.Error("expected error when name is missing")
	}
	if !strings.Contains(resp.Error, "name is required") {
		t.Errorf("wrong error: %s", resp.Error)
	}
}

// TestScanSkills_RejectsSymlinks verifies that a symlink in the skills
// dir is never read (Hole 2: symlink confinement).
func TestScanSkills_RejectsSymlinks(t *testing.T) {
	tmpHome := t.TempDir()
	t.Setenv("HOME", tmpHome)
	tmpProject := t.TempDir()

	// A secret file outside the skills dir.
	secretDir := t.TempDir()
	secretPath := filepath.Join(secretDir, "id_rsa")
	os.WriteFile(secretPath, []byte("SECRET KEY CONTENT"), 0o600)

	// Symlink in project skills dir → secret file.
	projDir := filepath.Join(tmpProject, ".odo", "skills")
	os.MkdirAll(projDir, 0o755)
	if err := os.Symlink(secretPath, filepath.Join(projDir, "evil.md")); err != nil {
		t.Skipf("symlink not supported on this platform: %v", err)
	}

	// Legitimate skill alongside.
	os.WriteFile(filepath.Join(projDir, "legit.md"),
		[]byte("---\nname: legit\n---\n\nBody"), 0o644)

	entries := scanSkills(tmpProject)
	if len(entries) != 1 || entries[0].info.Name != "legit" {
		var names []string
		for _, e := range entries {
			names = append(names, e.info.Name)
		}
		t.Fatalf("expected [legit], got %v (symlink must be rejected)", names)
	}
	for _, e := range entries {
		if strings.Contains(e.body, "SECRET KEY CONTENT") {
			t.Error("symlink target content leaked into skill body")
		}
	}
}

// TestScanSkills_SymlinkInGlobalDir verifies the global skills dir also
// rejects symlinks.
func TestScanSkills_SymlinkInGlobalDir(t *testing.T) {
	tmpHome := t.TempDir()
	t.Setenv("HOME", tmpHome)
	tmpProject := t.TempDir()

	secret := filepath.Join(t.TempDir(), "secret.txt")
	os.WriteFile(secret, []byte("LEAKED"), 0o600)

	globalDir := filepath.Join(tmpHome, ".odo", "skills")
	os.MkdirAll(globalDir, 0o755)
	if err := os.Symlink(secret, filepath.Join(globalDir, "link.md")); err != nil {
		t.Skipf("symlink not supported: %v", err)
	}

	entries := scanSkills(tmpProject)
	for _, e := range entries {
		if strings.Contains(e.body, "LEAKED") {
			t.Error("global symlink leaked content into prompt")
		}
	}
	if len(entries) != 0 {
		t.Errorf("expected 0 entries (symlink rejected), got %d", len(entries))
	}
}

// TestScanSkillsRejectsSwappedSymlink verifies the scan pins its read to
// the inode it validated: a path that is a regular skill on the first scan
// but swapped to a symlink before the rescan parses as NOTHING, and the
// link target's bytes never enter a skill body (2026-08 SEC TOCTOU fix —
// the fd pins the inode, os.SameFile rejects the swapped path).
func TestScanSkillsRejectsSwappedSymlink(t *testing.T) {
	tmpHome := t.TempDir()
	t.Setenv("HOME", tmpHome)
	tmpProject := t.TempDir()

	// Seed a real skill and verify it parses.
	projDir := filepath.Join(tmpProject, ".odo", "skills")
	os.MkdirAll(projDir, 0o755)
	skillPath := filepath.Join(projDir, "swapped.md")
	os.WriteFile(skillPath, []byte("---\nname: swapped\n---\n\nOriginal body"), 0o644)

	entries := scanSkills(tmpProject)
	if len(entries) != 1 || entries[0].info.Name != "swapped" {
		var names []string
		for _, e := range entries {
			names = append(names, e.info.Name)
		}
		t.Fatalf("first scan: expected [swapped], got %v", names)
	}

	// Swap the path for a symlink to a sentinel file outside the skills dir.
	sentinel := filepath.Join(t.TempDir(), "sentinel.txt")
	os.WriteFile(sentinel, []byte("SENTINEL SECRET BYTES"), 0o600)
	if err := os.Remove(skillPath); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(sentinel, skillPath); err != nil {
		t.Skipf("symlink not supported on this platform: %v", err)
	}

	// Rescan reads through the swapped path: the name must vanish and the
	// sentinel content must never appear.
	entries = scanSkills(tmpProject)
	for _, e := range entries {
		if e.info.Name == "swapped" {
			t.Error("swapped symlink must not parse as a skill")
		}
		if strings.Contains(e.body, "SENTINEL SECRET BYTES") {
			t.Error("symlink target content leaked into skill body")
		}
	}
}
