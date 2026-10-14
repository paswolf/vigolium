package cli

import (
	"testing"

	"github.com/stretchr/testify/require"
)

// TestAutopilotUnknownProjectNameFailsFast locks the guard that came with the
// agent-pipeline fixes. `--project-name` only *resolves* a project, and the
// resolution error used to be discarded: the run then wrote its findings into
// the Default Project and the post-run triage failed for want of a project UUID,
// with nothing in the log saying why. The error is now propagated before the run
// spends anything.
func TestAutopilotUnknownProjectNameFailsFast(t *testing.T) {
	e := newB5Env(t)
	src := t.TempDir()

	code, _, stderr := e.run(t, nil,
		"--project-name", "no-such-project",
		"agent", "autopilot",
		"--source", src,
		"-t", "http://127.0.0.1:9",
		"--audit", "off",
		"--no-preflight",
	)

	require.Equal(t, 1, code, "stderr:\n%s", stderr)
	require.Contains(t, stderr, "cannot scope this run to a project")
	require.Contains(t, stderr, "no-such-project")
}
