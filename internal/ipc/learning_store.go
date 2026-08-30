package ipc

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// D9-W3 (learning control plane, pure observability): content-addressed
// append-only store for learning candidates at
// <projectRoot>/.odo/learning/candidates.jsonl (lock §1.2, one compact JSON
// object per line). W3 ships the writer + reader only — no decision path
// consumes these rows yet (zero behavior change). artifact_hash covers the
// artifact's TRUTH fields only {version, scope, base_sha16, base_source_seq,
// delta, content}: provenance is creation-time metadata (lock §0.5 ruling —
// `uses` stays 0 forever; running counters are journal-fold-derived in W4+,
// never stored on the immutable row), so the same delta on the same base is
// the same artifact and append dedupes by hash (O(n) scan; n stays tiny).

// LearningRuleAdd is one delta.add entry; FlagSeq is set only when the rule
// is flag-driven (a harmful/effective flag tuple motivated it).
type LearningRuleAdd struct {
	Rule     string `json:"rule"`
	Evidence string `json:"evidence"`
	FlagSeq  int    `json:"flag_seq,omitempty"`
}

// LearningCandidateDelta is the rule-set change the candidate proposes.
// Adds carry learner-vetted fields only (no reaffirms — a reaffirm is not a
// rule-set change); retractions are normalized verbatim rule texts.
type LearningCandidateDelta struct {
	Add     []LearningRuleAdd `json:"add"`
	Retract []string          `json:"retract"`
}

// LearningCandidateProvenance is CREATION-TIME only (lock §0.5 ruling): Uses
// stays 0 forever; running counters are journal-fold-derived (W4+), never
// stored on the row.
type LearningCandidateProvenance struct {
	CreatedBy       string                 `json:"created_by"` // "learner_batch" | "human"
	SourceSeq       []int                  `json:"source_seq"`
	ProposeEpoch    int                    `json:"propose_epoch"`
	PanelReceiptSeq int                    `json:"panel_receipt_seq,omitempty"`
	Uses            int                    `json:"uses"`
	Cost            map[string]interface{} `json:"cost"`
	Supersedes      *string                `json:"supersedes"`
}

// LearningCandidate is one candidates.jsonl row (lock §1.2). Content is the
// FULL projected injected block bytes under the delta (what the prompt would
// carry), not a patch — self-contained and replay-trivial.
type LearningCandidate struct {
	ArtifactHash  string                      `json:"artifact_hash"`
	Version       int                         `json:"version"`
	Scope         string                      `json:"scope"`
	BaseSHA16     string                      `json:"base_sha16"`
	BaseSourceSeq int                         `json:"base_source_seq"`
	Delta         LearningCandidateDelta      `json:"delta"`
	Content       string                      `json:"content"`
	Provenance    LearningCandidateProvenance `json:"provenance"`
	CreatedAt     string                      `json:"created_at"`
	CreatedSeq    int                         `json:"created_seq"`
}

// LearningCandidatesPath returns <projectRoot>/.odo/learning/candidates.jsonl.
func LearningCandidatesPath(projectRoot string) string {
	return filepath.Join(projectRoot, ".odo", "learning", "candidates.jsonl")
}

// LearningArtifactHash returns the full lowercase hex SHA-256 over the
// canonical (sorted-key, compact) serialization of the candidate's truth
// fields {version, scope, base_sha16, base_source_seq, delta, content} —
// Go's map marshal emits sorted keys, and provenance/created_at/created_seq/
// artifact_hash are excluded by construction, so re-creating the same delta
// on the same base reproduces the same artifact (idempotent append dedupe).
func LearningArtifactHash(c LearningCandidate) string {
	b, _ := json.Marshal(map[string]interface{}{
		"version":         c.Version,
		"scope":           c.Scope,
		"base_sha16":      c.BaseSHA16,
		"base_source_seq": c.BaseSourceSeq,
		"delta":           c.Delta,
		"content":         c.Content,
	})
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

// AppendLearningCandidate content-addresses c (recomputing ArtifactHash over
// the truth fields, regardless of what c carried), then dedupes by hash with
// an O(n) line scan: an existing row with the same artifact_hash returns
// (existingRow, false, nil) — the file is append-only, never spliced.
// Otherwise the row is appended as one compact JSON line + "\n" (dir created
// 0755, file 0644 on first write) and (row, true, nil) returns. A malformed
// existing line is an ERROR (fail-closed: never silently write around a
// torn file).
func AppendLearningCandidate(projectRoot string, c LearningCandidate) (LearningCandidate, bool, error) {
	c.ArtifactHash = LearningArtifactHash(c)
	existing, err := readLearningCandidatesFile(projectRoot)
	if err != nil {
		return LearningCandidate{}, false, fmt.Errorf("append learning candidate: %w", err)
	}
	for _, row := range existing {
		if row.ArtifactHash == c.ArtifactHash {
			return row, false, nil
		}
	}
	line, err := json.Marshal(c)
	if err != nil {
		return LearningCandidate{}, false, fmt.Errorf("append learning candidate: marshal: %w", err)
	}
	path := LearningCandidatesPath(projectRoot)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return LearningCandidate{}, false, fmt.Errorf("append learning candidate: create dir: %w", err)
	}
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return LearningCandidate{}, false, fmt.Errorf("append learning candidate: open: %w", err)
	}
	if _, err := f.Write(append(line, '\n')); err != nil {
		f.Close()
		return LearningCandidate{}, false, fmt.Errorf("append learning candidate: write: %w", err)
	}
	if err := f.Close(); err != nil {
		return LearningCandidate{}, false, fmt.Errorf("append learning candidate: close: %w", err)
	}
	return c, true, nil
}

// ReadLearningCandidates returns every row in file order. A missing file is
// (nil, nil), NOT an error. Every line must parse (fail-closed) — a torn or
// tampered file is an error, never a partial read.
func ReadLearningCandidates(projectRoot string) ([]LearningCandidate, error) {
	rows, err := readLearningCandidatesFile(projectRoot)
	if err != nil {
		return nil, fmt.Errorf("read learning candidates: %w", err)
	}
	return rows, nil
}

// readLearningCandidatesFile loads and parses candidates.jsonl; a missing
// file reads as zero rows. Shared by Append (dedupe scan) and Read so the
// fail-closed line discipline lives in exactly one place.
func readLearningCandidatesFile(projectRoot string) ([]LearningCandidate, error) {
	data, err := os.ReadFile(LearningCandidatesPath(projectRoot))
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	var rows []LearningCandidate
	for i, line := range strings.Split(string(data), "\n") {
		if line == "" {
			continue // trailing newline terminator
		}
		var row LearningCandidate
		if err := json.Unmarshal([]byte(line), &row); err != nil {
			return nil, fmt.Errorf("line %d: %w", i+1, err)
		}
		rows = append(rows, row)
	}
	return rows, nil
}
