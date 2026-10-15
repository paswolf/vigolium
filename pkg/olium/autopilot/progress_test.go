package autopilot

import (
	"bytes"
	"context"
	"io"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/vigolium/vigolium/pkg/olium/provider"
	"github.com/vigolium/vigolium/pkg/olium/stream"
)

// TestProductiveCall pins the progress rule's boundary: the static tool set plus
// a bash turn that speaks HTTP to an in-scope host. Everything else — reading
// source, grepping for a URL, querying the target's database through docker —
// stays unproductive, which is what keeps the stall detector meaningful.
func TestProductiveCall(t *testing.T) {
	hosts := progressTargetHosts("http://localhost:8080/shop", []string{"*.example.com", "https://api.internal:8443"})

	cases := []struct {
		name string
		tool string
		args map[string]any
		want bool
	}{
		{"static productive tool", "report_finding", nil, true},
		{"static productive tool keeps its slot", "run_native_scan", nil, true},
		{"read-only tool", "read_file", map[string]any{"path": "x"}, false},
		{"bash curls the target", "bash", map[string]any{"command": `curl -s -H 'Cookie: x' http://localhost/api/orders`}, true},
		{"bash curls the loopback alias of localhost", "bash", map[string]any{"command": "curl -s http://127.0.0.1:8080/"}, true},
		{"bash curls a scoped wildcard host", "bash", map[string]any{"command": "curl -sk https://shop.example.com/"}, true},
		{"bash curls a scoped host with a path", "bash", map[string]any{"command": "curl -s https://api.internal:8443/v1/status"}, true},
		{"bash wgets the target", "bash", map[string]any{"command": "wget -qO- http://localhost/"}, true},
		{"bash curls an unrelated host", "bash", map[string]any{"command": "curl -s https://pypi.org/simple/"}, false},
		{"bash greps for the target URL", "bash", map[string]any{"command": `grep -rn "http://localhost/api" engine/`}, false},
		{"bash reads source", "bash", map[string]any{"command": "sed -n '1,200p' engine/Shopware/Bootstrap.php"}, false},
		{"bash queries the target's database", "bash", map[string]any{"command": `docker exec -i shopware-mysql-1 mysql -uapp -papp shopware -e "select 1"`}, false},
		{"client name inside a filename", "bash", map[string]any{"command": "python3 curl_to_json.py http://localhost/"}, false},
		{"client with no host", "bash", map[string]any{"command": "curl file:///etc/passwd"}, false},
		{"bash without a command", "bash", map[string]any{"path": "engine/"}, false},
		{"nil args", "bash", nil, false},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := productiveCall(tc.tool, tc.args, hosts); got != tc.want {
				t.Errorf("productiveCall(%q, %v) = %v, want %v", tc.tool, tc.args, got, tc.want)
			}
		})
	}
}

// TestProgressTargetHosts: the target and scope reduce to the lowercase host
// strings a shell command would carry, with loopback aliases included.
func TestProgressTargetHosts(t *testing.T) {
	hosts := strings.Join(progressTargetHosts("http://localhost:8080/shop", []string{"*.example.com", "https://api.internal:8443", "user:pw@other.example.net"}), ",")
	for _, want := range []string{"localhost", "127.0.0.1", "example.com", "api.internal", "other.example.net"} {
		if !strings.Contains(hosts, want) {
			t.Errorf("hosts %q missing %q", hosts, want)
		}
	}
	for _, unwanted := range []string{"8080", "/shop", "8443", "user:pw"} {
		if strings.Contains(hosts, unwanted) {
			t.Errorf("hosts %q should not carry %q", hosts, unwanted)
		}
	}
	if got := progressTargetHosts("", nil); len(got) != 0 {
		t.Errorf("empty target/scope = %v, want none", got)
	}
}

// bashProvider answers every turn with one bash call carrying command, so the
// loop's progress accounting can be driven without a model.
type bashProvider struct {
	promptRecorder
	command string
}

func (*bashProvider) Name() string { return "bash-loop" }

func (p *bashProvider) Stream(ctx context.Context, req provider.Request) (<-chan stream.Event, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	p.record(req)
	ch := make(chan stream.Event, 4)
	go func() {
		defer close(ch)
		ch <- stream.Event{Type: stream.EventToolCallStart, ToolCall: &stream.ToolCall{ID: "b1", Name: "bash"}}
		ch <- stream.Event{Type: stream.EventToolCallEnd, ToolCall: &stream.ToolCall{ID: "b1", Name: "bash", Arguments: map[string]any{"command": p.command}}}
		ch <- stream.Event{Type: stream.EventDone, StopReason: stream.StopReasonToolUse}
	}()
	return ch, nil
}

// runBashLoop drives a shadow-mode run whose operator only ever calls bash with
// command, in a tiny loop bounded by the turn budget.
func runBashLoop(t *testing.T, command string) string {
	t.Helper()
	if _, err := exec.LookPath("curl"); err != nil {
		t.Skip("curl is not on PATH: the bash call would fail, and a failed call never counts as progress")
	}
	t.Setenv("HOME", t.TempDir())
	var toolLog bytes.Buffer
	opts := Options{
		Provider:      &bashProvider{command: command},
		Model:         "claude-sonnet-4",
		Out:           io.Discard,
		ToolLog:       &toolLog,
		NoSkillFilter: true,
		SourcePath:    t.TempDir(),
		SessionDir:    t.TempDir(),
		Mode:          "shadow",
		Target:        "http://127.0.0.1:8080",
		MaxTurns:      12,
		// A productive loop has nothing to trip it: sections rotate and each
		// gets a fresh turn budget, so the run is bounded here by the tool-call
		// budget, which survives rotation by design.
		MaxToolCalls:       12,
		MaxTurnsPerSection: 4,
		StallTurns:         2,
		MaxStallRotations:  1,
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	if _, err := Run(ctx, opts); err != nil {
		t.Fatalf("Run: %v", err)
	}
	return toolLog.String()
}

// TestRunCountsTargetFacingBashAsProgress: a shell turn that puts a request on
// the wire keeps the session alive. Before the rule existed this run was
// stall-halted after two turns even though every turn probed the target — the
// exact shape that cost a real 2.5h run its findings.
func TestRunCountsTargetFacingBashAsProgress(t *testing.T) {
	log := runBashLoop(t, "curl -s -o /dev/null --max-time 1 http://127.0.0.1:9/ || true")
	if strings.Contains(log, "no productive tool call") {
		t.Errorf("a bash turn curling an in-scope host was treated as a stall:\n%s", log)
	}
}

// TestRunStillStallsOnNonTargetBash: the rule is not an escape hatch — the same
// loop with a command that never addresses the target still trips the guard.
func TestRunStillStallsOnNonTargetBash(t *testing.T) {
	log := runBashLoop(t, "curl --version >/dev/null 2>&1; echo nothing-to-see-here")
	if !strings.Contains(log, "no productive tool call") {
		t.Errorf("bash that never addresses the target must still trip the stall guard:\n%s", log)
	}
}
