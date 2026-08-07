package ipc

// M8 (Skills): skill scanner, frontmatter parser, keyword matcher, and
// prompt injection formatter. Skills are plain markdown files (SKILL.md or
// *.md) stored in ~/.odo/skills/ (global) and .odo/skills/ (project-local).
// Project skills override global on name collision. The daemon scans on
// every send_message (files are small, scanning is cheap) and injects
// keyword-matched skills into buildPrompt between pins and wiki index —
// procedures are stable, non-churning context, matching ADR-0003's injection
// order.

import (
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

// SkillInfo is the JSON-serializable metadata for one skill (list_skills
// response). Body is omitted here; read_skill returns the full content.
type SkillInfo struct {
	Name        string   `json:"name"`
	Description string   `json:"description"`
	Keywords    []string `json:"keywords,omitempty"`
	Path        string   `json:"path"`   // display path (relative when possible)
	Origin      string   `json:"origin"` // "human" | "ported" | "agent-authored"
	Scope       string   `json:"scope"`  // "global" | "project"
}

// skillEntry is the internal representation with body content.
type skillEntry struct {
	info    SkillInfo
	body    string
	absPath string
}

// skillsInjectionCap bounds the total skill text injected into one prompt.
// Skills share the injection budget with memory layers; 8 KB ≈ 2k tokens
// is enough for 2–4 concise skills.
const skillsInjectionCap = 8 * 1024

// frontmatterRe matches the YAML frontmatter block at the start of a file:
// ---\n key: value\n ... \n---\n
var frontmatterRe = regexp.MustCompile(`(?s)^---\r?\n(.*?)\r?\n---\r?\n`)

// parseFrontmatter extracts the YAML frontmatter and body from a SKILL.md
// file. Minimal parser: handles `key: value`, `key: [a, b, c]`, and list
// syntax (`key:\n  - a\n  - b`). No external YAML dependency.
func parseFrontmatter(content string) (name, desc, origin string, keywords []string, body string) {
	body = strings.TrimSpace(content)
	m := frontmatterRe.FindStringSubmatch(content)
	if m == nil {
		return "", "", "human", nil, body
	}
	yamlBlock := m[1]
	body = strings.TrimSpace(content[len(m[0]):])

	// Parse simple YAML key-value pairs and list items.
	currentListKey := ""
	for _, line := range strings.Split(yamlBlock, "\n") {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" || strings.HasPrefix(trimmed, "#") {
			continue
		}
		// List item under a previous key: "  - value"
		if strings.HasPrefix(trimmed, "-") && currentListKey != "" {
			val := strings.TrimSpace(strings.TrimPrefix(trimmed, "-"))
			val = strings.Trim(val, "\"'")
			if val != "" {
				switch currentListKey {
				case "keywords":
					keywords = append(keywords, val)
				}
			}
			continue
		}
		// Key: value
		idx := strings.Index(trimmed, ":")
		if idx < 0 {
			continue
		}
		key := strings.TrimSpace(trimmed[:idx])
		val := strings.TrimSpace(trimmed[idx+1:])
		val = strings.Trim(val, "\"'")
		currentListKey = "" // reset; non-indented line starts a new key

		switch key {
		case "name":
			name = val
		case "description":
			desc = val
		case "origin":
			origin = val
		case "keywords":
			if val == "" {
				// List syntax: keywords:\n  - a\n  - b
				currentListKey = "keywords"
			} else {
				// Inline array: [a, b, c]
				val = strings.Trim(val, "[]")
				for _, k := range strings.Split(val, ",") {
					k = strings.TrimSpace(k)
					k = strings.Trim(k, "\"'")
					if k != "" {
						keywords = append(keywords, k)
					}
				}
			}
		}
	}

	if origin == "" {
		origin = "human"
	}
	return
}

// scanSkills scans both global (~/.odo/skills/) and project (.odo/skills/)
// directories for *.md files. Project skills override global on name
// collision (later in the scan order). Returns entries sorted by name.
func scanSkills(projectRoot string) []skillEntry {
	home, _ := os.UserHomeDir()
	dirs := []struct {
		root  string
		scope string
	}{
		{filepath.Join(home, ".odo", "skills"), "global"},
		{filepath.Join(projectRoot, ".odo", "skills"), "project"},
	}

	byName := map[string]skillEntry{}

	for _, d := range dirs {
		files, err := filepath.Glob(filepath.Join(d.root, "*.md"))
		if err != nil || len(files) == 0 {
			continue
		}
		for _, f := range files {
			content, err := os.ReadFile(f)
			if err != nil {
				continue
			}
			name, desc, origin, keywords, body := parseFrontmatter(string(content))
			if name == "" {
				name = strings.TrimSuffix(filepath.Base(f), ".md")
			}
			// Display path: relative for project skills, absolute for global
			dispPath := f
			if d.scope == "project" {
				if rel, err := filepath.Rel(projectRoot, f); err == nil {
					dispPath = rel
				}
			}
			byName[name] = skillEntry{
				info: SkillInfo{
					Name:        name,
					Description: desc,
					Keywords:    keywords,
					Path:        dispPath,
					Origin:      origin,
					Scope:       d.scope,
				},
				body:    body,
				absPath: f,
			}
		}
	}

	var entries []skillEntry
	for _, e := range byName {
		entries = append(entries, e)
	}
	sort.Slice(entries, func(i, j int) bool {
		return entries[i].info.Name < entries[j].info.Name
	})
	return entries
}

// matchSkills ranks skills by keyword overlap with the query. Uses the
// same tokenizeQuery from recall.go for consistency with wiki recall.
// Keyword matches score 2; name/description substring matches score 1.
// Unmatched skills are excluded (unlike wiki recall, skills are always
// conditional — injecting an irrelevant procedure wastes context).
func matchSkills(query string, entries []skillEntry) []skillEntry {
	tokens := tokenizeQuery(query)
	if len(tokens) == 0 {
		return entries // no query = all skills, capped by formatter
	}

	type scored struct {
		entry skillEntry
		score int
	}
	var scoredList []scored
	for _, e := range entries {
		score := 0
		kwLower := map[string]bool{}
		for _, k := range e.info.Keywords {
			kwLower[strings.ToLower(k)] = true
		}
		for _, t := range tokens {
			if kwLower[t] {
				score += 2
			}
			haystack := strings.ToLower(e.info.Name + " " + e.info.Description)
			if strings.Contains(haystack, t) {
				score += 1
			}
		}
		if score > 0 {
			scoredList = append(scoredList, scored{e, score})
		}
	}

	sort.Slice(scoredList, func(i, j int) bool {
		return scoredList[i].score > scoredList[j].score
	})

	var result []skillEntry
	for _, s := range scoredList {
		result = append(result, s.entry)
	}
	return result
}

// formatSkillsForInjection formats matched skills into a prompt block.
// Caps total size at maxBytes on a skill boundary (no half-skill injection).
func formatSkillsForInjection(entries []skillEntry, maxBytes int) string {
	var b strings.Builder
	for _, e := range entries {
		header := "### Skill: " + e.info.Name + "\n\n"
		block := header + e.body + "\n\n---\n\n"
		if b.Len()+len(block) > maxBytes {
			break
		}
		b.WriteString(block)
	}
	return b.String()
}

// loadSkillsForPrompt scans, matches, and formats skills for injection into
// buildPrompt. Called from memoryLayers() on every send_message.
func loadSkillsForPrompt(projectRoot, query string) string {
	entries := scanSkills(projectRoot)
	if len(entries) == 0 {
		return ""
	}
	matched := matchSkills(query, entries)
	return formatSkillsForInjection(matched, skillsInjectionCap)
}
