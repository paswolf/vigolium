package agent

import (
	"os"
	"path/filepath"
	"testing"
)

// TestHasEvidenceRecognisesWhiteboxProvenance locks the evidence heuristic the
// artifact check uses. A whitebox audit finding carries no request/response —
// its evidence IS the file it was found in — so source_file has to count as
// evidence. Without it every audit finding in a synthesized findings.json reads
// as evidence-free and the run warns about entries that are, in fact, backed by
// a code location.
func TestHasEvidenceRecognisesWhiteboxProvenance(t *testing.T) {
	emptyDir := t.TempDir()
	populatedDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(populatedDir, "poc.txt"), []byte("x"), 0o600); err != nil {
		t.Fatalf("write evidence fixture: %v", err)
	}

	cases := []struct {
		name string
		item map[string]any
		dir  string
		want bool
	}{
		{"http body", map[string]any{"request": "GET / HTTP/1.1"}, emptyDir, true},
		{"evidence path", map[string]any{"evidence_path": "poc.md"}, emptyDir, true},
		{"evidence files", map[string]any{"evidence_files": []any{"poc.md"}}, emptyDir, true},
		{"source location (whitebox)", map[string]any{"source_file": "lib/auth.php:3"}, emptyDir, true},
		{"no evidence at all", map[string]any{"title": "x", "severity": "high"}, emptyDir, false},
		{"evidence dir holds files", map[string]any{"title": "x"}, populatedDir, true},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := hasEvidence(tc.item, tc.dir); got != tc.want {
				t.Errorf("hasEvidence(%v, %s) = %v, want %v", tc.item, tc.dir, got, tc.want)
			}
		})
	}
}
