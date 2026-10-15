package cli

import "testing"

// TestRunMessageLabel: a run's warning list and its error share one field, so the
// heading has to follow the status. Rendering a completed run's warnings as
// "Error" made a 7h45m run with 13 findings read as broken.
func TestRunMessageLabel(t *testing.T) {
	for _, tc := range []struct {
		status string
		label  string
		warn   bool
	}{
		{"completed_with_warnings", "Warnings", true},
		{"failed", "Error", false},
		{"cancelled", "Error", false},
		{"completed", "Error", false},
	} {
		label, warn := runMessageLabel(tc.status)
		if label != tc.label || warn != tc.warn {
			t.Errorf("runMessageLabel(%q) = (%q, %v), want (%q, %v)", tc.status, label, warn, tc.label, tc.warn)
		}
	}
}
