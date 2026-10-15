package agent

import (
	"os"
	"path/filepath"
	"strings"
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
		{"linked record reference", map[string]any{"http_record_uuids": []any{"0f1e2d3c-4b5a"}}, emptyDir, true},
		{"linked record reference (string slice)", map[string]any{"evidence_record_uuids": []string{"0f1e2d3c"}}, emptyDir, true},
		{"empty reference list", map[string]any{"http_record_uuids": []any{}}, emptyDir, false},
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

// TestCountEvidenceBackedFindingsAcceptsPlainStringEntries pins the shape the
// operator actually writes for dismissed.json: a flat array of ruled-out
// reasons as plain strings. The counter only understood an array of objects or
// a {"findings": [...]} wrapper, so a run that produced its findings and its
// dismissals was reported degraded with "failed to parse dismissed.json as
// findings artifact" — the entries were there, just not in the expected shape.
// String entries carry no evidence fields, so they count only where no evidence
// is required (the dismissed artifact).
func TestCountEvidenceBackedFindingsAcceptsPlainStringEntries(t *testing.T) {
	dir := t.TempDir()
	// Shape (and style of entry) taken from a real run's autopilot/dismissed.json.
	body := `[
  "M1 dot-notation live PoC not reproduced dynamically: GET /backend/UserManager/getUsers and GET /backend/user.manager/getUsers both returned HTTP 400 (identical to baseline), not the expected 401-vs-200 differential.",
  "M1 exploitability blocked by data state: the demo DB has only one backend user with full access — no restricted role existed to exercise the fail-open path.",
  "GET /backend/ returned 302 to the login form as expected."
]`

	dismissed := filepath.Join(dir, "dismissed.json")
	if err := os.WriteFile(dismissed, []byte(body), 0o600); err != nil {
		t.Fatalf("write dismissed artifact: %v", err)
	}
	count, warn := countEvidenceBackedFindings(dismissed, "")
	if count != 3 {
		t.Errorf("dismissed count = %d, want 3 (every entry counts where no evidence is required)", count)
	}
	if warn != "" {
		t.Errorf("dismissed warning = %q, want none", warn)
	}

	// The same shape in the findings artifact must still flag the entries that
	// carry no evidence reference.
	findings := filepath.Join(dir, "findings.json")
	if err := os.WriteFile(findings, []byte(body), 0o600); err != nil {
		t.Fatalf("write findings artifact: %v", err)
	}
	count, warn = countEvidenceBackedFindings(findings, filepath.Join(dir, "evidence"))
	if count != 0 {
		t.Errorf("findings count = %d, want 0: string entries carry no evidence", count)
	}
	if !strings.Contains(warn, "without evidence references") {
		t.Errorf("findings warning = %q, want an evidence-reference warning", warn)
	}
}
