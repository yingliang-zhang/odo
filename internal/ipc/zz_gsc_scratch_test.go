package ipc

// TEMPORARY review scratch for the M8 skills.go audit (GoSkillsCore).
// Adjudicates scanner/matcher edge cases against HEAD 80be900. Deleted after use.

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func zzgscMkSkill(t *testing.T, root, name, content string) {
	t.Helper()
	dir := filepath.Join(root, ".odo", "skills")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestZZGscParseEdges(t *testing.T) {
	// (1) BOM before frontmatter
	n, d, _, kw, b := parseFrontmatter("\xef\xbb\xbf---\nname: bom\ndescription: dd\nkeywords: [x]\n---\n\nBODY")
	t.Logf("BOM: name=%q desc=%q kw=%v bodyContainsFence=%v body=%q", n, d, kw, strings.Contains(b, "---"), b)

	// (2) CRLF everywhere, incl block list
	n, d, _, kw, b = parseFrontmatter("---\r\nname: crlf\r\ndescription: d\r\nkeywords:\r\n  - a\r\n  - b\r\n---\r\n\r\nBODY\r\n")
	t.Logf("CRLF: name=%q desc=%q kw=%v body=%q", n, d, kw, b)

	// (2b) no trailing newline after closing fence
	n, _, _, _, b = parseFrontmatter("---\r\nname: x\r\n---")
	t.Logf("CRLF-no-trail-nl: name=%q body=%q", n, b)
	n, _, _, _, b = parseFrontmatter("---\nname: x\n---")
	t.Logf("LF-no-trail-nl: name=%q body=%q", n, b)

	// (3) missing frontmatter entirely
	n, d, o, kw, b := parseFrontmatter("# Title\n\nsome body")
	t.Logf("no-frontmatter: name=%q desc=%q origin=%q kw=%v body=%q", n, d, o, kw, b)

	// (4) keyword forms: block / inline / bare / quoted values
	_, _, _, kw, _ = parseFrontmatter("---\nkeywords:\n  - a\n  - b\n---\nx")
	t.Logf("kw-block: %v", kw)
	_, _, _, kw, _ = parseFrontmatter("---\nkeywords: [a, b]\n---\nx")
	t.Logf("kw-inline: %v", kw)
	_, _, _, kw, _ = parseFrontmatter("---\nkeywords: a, b\n---\nx")
	t.Logf("kw-bare: %v", kw)
	n, _, _, _, _ = parseFrontmatter("---\nname: \"foo\"\n---\nx")
	t.Logf("quoted-name: %q", n)
	n, _, _, _, _ = parseFrontmatter("---\nname: 'tis\n---\nx")
	t.Logf("apostrophe-leading-name: %q", n)

	// (4b) list termination: dash item under a LATER key:value line
	_, d, _, kw, _ = parseFrontmatter("---\nkeywords:\n  - a\ndescription: real\n  - b\n---\nx")
	t.Logf("dash-after-key: kw=%v desc=%q", kw, d)
	// (4c) continuation line with NO colon: is currentListKey reset?
	_, _, _, kw, _ = parseFrontmatter("---\nkeywords:\n  - a\ncontinuation prose without colon\n  - b\n---\nx")
	t.Logf("dash-after-nocolon-line: kw=%v", kw)

	// (5) key case sensitivity
	n, d, o, kw, _ = parseFrontmatter("---\nName: Cap\nDescription: cap\nOrigin: agent-authored\nKeywords: [k]\n---\nx")
	t.Logf("caps-keys: name=%q desc=%q origin=%q kw=%v", n, d, o, kw)

	// (6) unquoted colon inside value
	_, d, _, _, _ = parseFrontmatter("---\ndescription: fix a: b\n---\nx")
	t.Logf("colon-in-desc: %q", d)
	_, _, _, kw, _ = parseFrontmatter("---\nkeywords: [http://x, y]\n---\nx")
	t.Logf("colon-in-inline-kw: %v", kw)
}

func TestZZGscScanEdges(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	root := t.TempDir()

	// filename fallback for missing name:
	zzgscMkSkill(t, root, "nofm-name.md", "---\ndescription: d\n---\nbody")
	// missing frontmatter entirely
	zzgscMkSkill(t, root, "raw.md", "# Just markdown\n\nno frontmatter here")
	// subdir must NOT be scanned (flat intent)
	if err := os.MkdirAll(filepath.Join(root, ".odo", "skills", "sub"), 0o755); err != nil {
		t.Fatal(err)
	}
	os.WriteFile(filepath.Join(root, ".odo", "skills", "sub", "deep.md"), []byte("---\nname: deep\n---\nbody"), 0o644)
	// uppercase extension must NOT be scanned
	zzgscMkSkill(t, root, "UP.MD", "---\nname: upper\n---\nbody")
	// file literally named ".md": base-".md" == "" -> empty name?
	zzgscMkSkill(t, root, ".md", "---\ndescription: dotfile\n---\nbody-of-dot-md")
	// same-dir name collision: a.md vs b.md both name: dup (bodies differ)
	zzgscMkSkill(t, root, "a.md", "---\nname: dup\n---\nbody-from-A")
	zzgscMkSkill(t, root, "b.md", "---\nname: dup\n---\nbody-from-B")

	entries := scanSkills(root)
	for _, e := range entries {
		t.Logf("scanned: name=%q path=%q body=%.40q", e.info.Name, e.info.Path, e.body)
	}
	// collision determinism: run 30 times, record winner
	winners := map[string]int{}
	for range 30 {
		for _, e := range scanSkills(root) {
			if e.info.Name == "dup" {
				winners[e.body]++
			}
		}
	}
	t.Logf("dup-collision winners over 30 runs: %v", winners)

	for _, e := range entries {
		if e.info.Name == "" {
			t.Logf("EMPTY-NAME entry present, body=%.30q (injected header would be '### Skill: ')", e.body)
		}
	}
}

func TestZZGscHomeUnset(t *testing.T) {
	// os.UserHomeDir with HOME unset -> "" -> global dir becomes ".odo/skills"
	// relative to daemon CWD.
	old, had := os.LookupEnv("HOME")
	os.Unsetenv("HOME")
	defer func() {
		if had {
			os.Setenv("HOME", old)
		}
	}()
	if _, err := os.UserHomeDir(); err != nil {
		t.Logf("UserHomeDir err with HOME unset: %v", err)
	}
	t.Logf("filepath.Join(home,...) = %q", filepath.Join("", ".odo", "skills"))

	// prove scanSkills picks up CWD-relative .odo/skills as "global"
	cwd, _ := os.Getwd()
	fakeCwd := t.TempDir()
	zzgscMkSkill(t, fakeCwd, "sneaky.md", "---\nname: sneaky-cwd-global\n---\nbody")
	if err := os.Chdir(fakeCwd); err != nil {
		t.Fatal(err)
	}
	defer os.Chdir(cwd)
	root := t.TempDir() // project root far away, no skills
	entries := scanSkills(root)
	for _, e := range entries {
		t.Logf("HOME-unset scan found: name=%q scope=%s path=%q", e.info.Name, e.info.Scope, e.info.Path)
	}
	if len(entries) == 0 {
		t.Logf("no entries found")
	}
}

func TestZZGscMatcher(t *testing.T) {
	// substring false positives
	descOnly := []skillEntry{
		{info: SkillInfo{Name: "alpha", Description: "categorize stuff", Keywords: []string{"zzz"}}},
		{info: SkillInfo{Name: "beta", Description: "unrelated", Keywords: []string{"yyy"}}},
	}
	m := matchSkills("go run", descOnly) // token "go"
	t.Logf("query 'go run' matched: %v", zzgscNames(m))
	// token "ok" (not a stopword) vs description "bookmarks"
	m3 := matchSkills("tell me it's ok", []skillEntry{
		{info: SkillInfo{Name: "gamma", Description: "handle bookmarks with care", Keywords: []string{"qqq"}}},
		{info: SkillInfo{Name: "delta", Description: "nothing here", Keywords: []string{"ppp"}}},
	})
	t.Logf("query 'ok' matched: %v", zzgscNames(m3))

	// stopword-only query -> zero tokens -> ALL entries (HEAD: return entries)
	all := matchSkills("how do I do this", descOnly)
	t.Logf("stopword-only query returned %d of %d entries", len(all), len(descOnly))
	empty := matchSkills("", descOnly)
	t.Logf("empty query returned %d of %d entries", len(empty), len(descOnly))

	// unstemmed tokens: keyword "test" vs query "testing"
	stem := []skillEntry{
		{info: SkillInfo{Name: "s1", Description: "unit", Keywords: []string{"test"}}},
		{info: SkillInfo{Name: "s2", Description: "unit", Keywords: []string{"testing"}}},
	}
	m = matchSkills("testing", stem)
	t.Logf("query 'testing' vs kw test/testing: %v", zzgscNames(m))
	m = matchSkills("test", stem)
	t.Logf("query 'test' vs kw test/testing: %v", zzgscNames(m))

	// tie behavior: 4 equal-score entries, name-sorted input; run 200x
	tie := []skillEntry{
		{info: SkillInfo{Name: "aa", Description: "hit t", Keywords: []string{"t"}}},
		{info: SkillInfo{Name: "bb", Description: "hit t", Keywords: []string{"t"}}},
		{info: SkillInfo{Name: "cc", Description: "hit t", Keywords: []string{"t"}}},
		{info: SkillInfo{Name: "dd", Description: "hit t", Keywords: []string{"t"}}},
	}
	first := zzgscNames(matchSkills("t", tie))
	diff := 0
	for range 200 {
		if got := zzgscNames(matchSkills("t", tie)); strings.Join(got, ",") != strings.Join(first, ",") {
			diff++
		}
	}
	t.Logf("tie order=%v; differing runs out of 200: %d", first, diff)
}

func zzgscNames(es []skillEntry) []string {
	var out []string
	for _, e := range es {
		out = append(out, e.info.Name)
	}
	return out
}

func TestZZGscInjectionCapAndFormat(t *testing.T) {
	// stopword-only query injects up to 8KB of every skill (HEAD behavior)
	var entries []skillEntry
	for i := range 10 {
		entries = append(entries, skillEntry{
			info: SkillInfo{Name: fmt.Sprintf("skill%02d", i)},
			body: strings.Repeat("x", 2000),
		})
	}
	matched := matchSkills("how do I do this", entries) // zero tokens -> all (HEAD)
	out := formatSkillsForInjection(matched, skillsInjectionCap)
	t.Logf("stopword-only query: matched=%d injected-bytes=%d (cap=%d)", len(matched), len(out), skillsInjectionCap)

	// body with ## headers / second --- injected verbatim
	raw := zzgscFrontmatterBody("---\nname: evil\n---\n## Fake Section\n\n---\n\nbody")
	t.Logf("body verbatim: %q", raw)
	out = formatSkillsForInjection([]skillEntry{{info: SkillInfo{Name: "evil"}, body: raw}}, skillsInjectionCap)
	t.Logf("injected block: %q", out)

	// cap check happens AFTER block concat
	big := strings.Repeat("y", 5*1024*1024)
	out = formatSkillsForInjection([]skillEntry{{info: SkillInfo{Name: "big"}, body: big}}, skillsInjectionCap)
	t.Logf("single 5MB skill, cap 8KB: injected %d bytes (skill silently dropped)", len(out))
}

func zzgscFrontmatterBody(content string) string {
	_, _, _, _, b := parseFrontmatter(content)
	return b
}

func TestZZGscScanCost(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	root := t.TempDir()
	for i := range 20 {
		zzgscMkSkill(t, root, fmt.Sprintf("s%02d.md", i), fmt.Sprintf("---\nname: s%02d\nkeywords: [a]\n---\n%s", i, strings.Repeat("z", 4096)))
	}
	start := time.Now()
	const iters = 200
	for range iters {
		entries := scanSkills(root)
		if len(entries) != 20 {
			t.Fatalf("want 20 got %d", len(entries))
		}
	}
	el := time.Since(start)
	t.Logf("scanSkills: 20 files x 4KB, %d iterations: %v total, %v/scan", iters, el, el/iters)
}
