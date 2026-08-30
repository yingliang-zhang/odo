package ipc

import (
	"bytes"
	"encoding/hex"
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

// learningCandidateFixture returns a well-formed candidate; slices are
// non-nil so a JSON round trip is DeepEqual-stable.
func learningCandidateFixture() LearningCandidate {
	return LearningCandidate{
		Version:       1,
		Scope:         "project:memory",
		BaseSHA16:     "ab12cd34ef56ab78",
		BaseSourceSeq: 411,
		Delta: LearningCandidateDelta{
			Add: []LearningRuleAdd{
				{Rule: "Always run go vet before claiming done", Evidence: "main-epoch-16"},
			},
			Retract: []string{},
		},
		Content: "- Prefer compact output — cites: main-epoch-2; reaffirmed: 9\n- Always run go vet before claiming done — cites: main-epoch-16; reaffirmed: 17\n",
		Provenance: LearningCandidateProvenance{
			CreatedBy:    "learner_batch",
			SourceSeq:    []int{455},
			ProposeEpoch: 17,
			Cost:         map[string]interface{}{"usage_available": false},
			Uses:         0,
		},
		CreatedAt:  "2026-08-30T01:12:44Z",
		CreatedSeq: 460,
	}
}

// TestLearningArtifactHash_Stability pins two things at once: the same truth
// fields hash identically across calls (⇒ the canonical map marshal is
// byte-deterministic), and the hash is a full lowercase SHA-256 hex.
func TestLearningArtifactHash_Stability(t *testing.T) {
	c := learningCandidateFixture()
	h1, h2 := LearningArtifactHash(c), LearningArtifactHash(c)
	if h1 != h2 {
		t.Fatalf("unstable hash: %q vs %q", h1, h2)
	}
	if len(h1) != 64 {
		t.Fatalf("hash length = %d, want 64", len(h1))
	}
	if _, err := hex.DecodeString(h1); err != nil {
		t.Fatalf("hash not lowercase hex: %v", err)
	}
}

// TestLearningArtifactHash_ProvenanceExcluded: provenance, created_at,
// created_seq, and artifact_hash are creation-time metadata — mutating ONLY
// those fields never changes the artifact (re-creation idempotence).
func TestLearningArtifactHash_ProvenanceExcluded(t *testing.T) {
	base := learningCandidateFixture()
	want := LearningArtifactHash(base)

	mutated := base
	mutated.ArtifactHash = "0000000000000000000000000000000000000000000000000000000000000000"
	mutated.CreatedAt = "2030-01-01T00:00:00Z"
	mutated.CreatedSeq = 99999
	mutated.Provenance.CreatedBy = "human"
	mutated.Provenance.SourceSeq = []int{1, 2, 3}
	mutated.Provenance.ProposeEpoch = 99
	mutated.Provenance.PanelReceiptSeq = 42
	mutated.Provenance.Uses = 7777
	mutated.Provenance.Cost = map[string]interface{}{"cost_usd": "lots"}
	sup := "abc"
	mutated.Provenance.Supersedes = &sup
	if got := LearningArtifactHash(mutated); got != want {
		t.Fatalf("provenance leaked into hash:\n want %s\n got  %s", want, got)
	}
}

// TestLearningArtifactHash_TruthFields: every truth field must perturb the
// hash — a field dropped from the canonical map would silently collide.
func TestLearningArtifactHash_TruthFields(t *testing.T) {
	base := learningCandidateFixture()
	want := LearningArtifactHash(base)

	t.Run("version", func(t *testing.T) {
		c := base
		c.Version = 2
		if got := LearningArtifactHash(c); got == want {
			t.Fatal("version change did not change hash")
		}
	})
	t.Run("scope", func(t *testing.T) {
		c := base
		c.Scope = "project:hooks"
		if got := LearningArtifactHash(c); got == want {
			t.Fatal("scope change did not change hash")
		}
	})
	t.Run("base_sha16", func(t *testing.T) {
		c := base
		c.BaseSHA16 = "ffffffffffffffff"
		if got := LearningArtifactHash(c); got == want {
			t.Fatal("base_sha16 change did not change hash")
		}
	})
	t.Run("base_source_seq", func(t *testing.T) {
		c := base
		c.BaseSourceSeq = base.BaseSourceSeq + 1
		if got := LearningArtifactHash(c); got == want {
			t.Fatal("base_source_seq change did not change hash")
		}
	})
	t.Run("delta", func(t *testing.T) {
		c := base
		c.Delta = LearningCandidateDelta{
			Add:     []LearningRuleAdd{{Rule: "Never amend a pushed commit", Evidence: "main-epoch-30", FlagSeq: 977}},
			Retract: []string{"outdated rule"},
		}
		if got := LearningArtifactHash(c); got == want {
			t.Fatal("delta change did not change hash")
		}
	})
	t.Run("content", func(t *testing.T) {
		c := base
		c.Content = base.Content + "- extra line\n"
		if got := LearningArtifactHash(c); got == want {
			t.Fatal("content change did not change hash")
		}
	})
}

// TestLearningCandidateStore_Roundtrip: two appends land in order with
// fields intact; the dir is created on first write and the second append
// grows the file (append-only, never truncated).
func TestLearningCandidateStore_Roundtrip(t *testing.T) {
	root := t.TempDir()
	first := learningCandidateFixture()
	second := learningCandidateFixture()
	second.Content = "- different projected block\n"
	second.Delta = LearningCandidateDelta{
		Add:     []LearningRuleAdd{},
		Retract: []string{"Always run go vet before claiming done"},
	}

	row1, appended, err := AppendLearningCandidate(root, first)
	if err != nil {
		t.Fatalf("append 1: %v", err)
	}
	if !appended {
		t.Fatal("append 1 not appended")
	}
	row2, appended, err := AppendLearningCandidate(root, second)
	if err != nil {
		t.Fatalf("append 2: %v", err)
	}
	if !appended {
		t.Fatal("append 2 not appended")
	}

	if fi, err := os.Stat(filepath.Join(root, ".odo", "learning")); err != nil || !fi.IsDir() {
		t.Fatalf("learning dir not created: %v", err)
	}
	data, err := os.ReadFile(LearningCandidatesPath(root))
	if err != nil {
		t.Fatal(err)
	}
	if n := bytes.Count(data, []byte("\n")); n != 2 {
		t.Fatalf("line count = %d, want 2 (append must not truncate)", n)
	}

	rows, err := ReadLearningCandidates(root)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 2 {
		t.Fatalf("read %d rows, want 2", len(rows))
	}
	if !reflect.DeepEqual(rows[0], row1) {
		t.Fatalf("row 0 mismatch:\n got %+v\nwant %+v", rows[0], row1)
	}
	if !reflect.DeepEqual(rows[1], row2) {
		t.Fatalf("row 1 mismatch:\n got %+v\nwant %+v", rows[1], row2)
	}
}

// TestLearningCandidateStore_Dedupe: re-appending the same artifact is a
// no-op returning the existing row — hash dedupe keeps the file append-only.
func TestLearningCandidateStore_Dedupe(t *testing.T) {
	root := t.TempDir()
	c := learningCandidateFixture()

	row1, appended, err := AppendLearningCandidate(root, c)
	if err != nil || !appended {
		t.Fatalf("append 1: appended=%v err=%v", appended, err)
	}
	// The caller's ArtifactHash field is recomputed/idempotent-irrelevant.
	c.ArtifactHash = "stale"
	row2, appended, err := AppendLearningCandidate(root, c)
	if err != nil {
		t.Fatalf("append 2: %v", err)
	}
	if appended {
		t.Fatal("duplicate append reported as appended")
	}
	if !reflect.DeepEqual(row2, row1) {
		t.Fatalf("dedupe returned different row:\n got %+v\nwant %+v", row2, row1)
	}
	data, err := os.ReadFile(LearningCandidatesPath(root))
	if err != nil {
		t.Fatal(err)
	}
	if n := bytes.Count(data, []byte("\n")); n != 1 {
		t.Fatalf("line count = %d, want 1 after dedupe", n)
	}
}

// TestLearningCandidateStore_MissingFile: reading a project with no
// candidates file is (nil, nil), never an error.
func TestLearningCandidateStore_MissingFile(t *testing.T) {
	rows, err := ReadLearningCandidates(t.TempDir())
	if err != nil {
		t.Fatalf("missing file read: %v", err)
	}
	if rows != nil {
		t.Fatalf("missing file read %d rows, want nil", len(rows))
	}
}

// TestLearningCandidateStore_Corrupt fail-closed: a malformed line errors
// BOTH reads and appends — never a partial read, never silently writing
// around a torn file.
func TestLearningCandidateStore_Corrupt(t *testing.T) {
	root := t.TempDir()
	if _, _, err := AppendLearningCandidate(root, learningCandidateFixture()); err != nil {
		t.Fatalf("seed append: %v", err)
	}
	f, err := os.OpenFile(LearningCandidatesPath(root), os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.WriteString("not json\n"); err != nil {
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}

	if _, err := ReadLearningCandidates(root); err == nil {
		t.Fatal("read tolerated a corrupt line")
	}
	if _, _, err := AppendLearningCandidate(root, learningCandidateFixture()); err == nil {
		t.Fatal("append tolerated a corrupt line")
	}
}
