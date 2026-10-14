package autopilot

import (
	"bytes"
	"context"
	"io"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/vigolium/vigolium/pkg/olium/provider"
	"github.com/vigolium/vigolium/pkg/olium/stream"
)

// promptRecorder captures the user prompt behind every provider request so a
// test can assert which mission the loop sent.
type promptRecorder struct {
	mu      sync.Mutex
	prompts []string
}

func (r *promptRecorder) record(req provider.Request) {
	if n := len(req.Messages); n > 0 {
		m := req.Messages[n-1]
		r.mu.Lock()
		r.prompts = append(r.prompts, m.Text+m.Content)
		r.mu.Unlock()
	}
}

func (r *promptRecorder) sawPrompt(substr string) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, s := range r.prompts {
		if strings.Contains(s, substr) {
			return true
		}
	}
	return false
}

func (r *promptRecorder) promptCount() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.prompts)
}

// silentProvider answers with text and no tool call, which is the state the
// engine gives up on by itself — no halt signal. That is what the report-only
// turn exists to rescue: an operator that verified issues without ever calling
// a tool the stall detector counts as progress would otherwise end the run with
// nothing reported.
type silentProvider struct{ promptRecorder }

func (*silentProvider) Name() string { return "silent" }

func (p *silentProvider) Stream(ctx context.Context, req provider.Request) (<-chan stream.Event, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	p.record(req)
	ch := make(chan stream.Event, 4)
	go func() {
		defer close(ch)
		ch <- stream.Event{Type: stream.EventTextDelta, Delta: "nothing further to check"}
		ch <- stream.Event{Type: stream.EventDone, StopReason: stream.StopReasonStop}
	}()
	return ch, nil
}

// loopingProvider always calls the read-only `ls` tool: a tool that touches
// nothing on the target and records no result, so every turn counts as
// unproductive for the stall detector — the shape of a bash-style investigation.
type loopingProvider struct{ promptRecorder }

func (*loopingProvider) Name() string { return "loop" }

func (p *loopingProvider) Stream(ctx context.Context, req provider.Request) (<-chan stream.Event, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	p.record(req)
	ch := make(chan stream.Event, 4)
	go func() {
		defer close(ch)
		ch <- stream.Event{Type: stream.EventToolCallStart, ToolCall: &stream.ToolCall{ID: "c", Name: "ls"}}
		ch <- stream.Event{Type: stream.EventToolCallEnd, ToolCall: &stream.ToolCall{ID: "c", Name: "ls", Arguments: map[string]any{"path": "."}}}
		ch <- stream.Event{Type: stream.EventDone, StopReason: stream.StopReasonToolUse}
	}()
	return ch, nil
}

// runSilent runs a scan whose model never calls a tool and never halts, so the
// engine ends the iteration on its own.
func runSilent(t *testing.T, opts Options) *silentProvider {
	t.Helper()
	t.Setenv("HOME", t.TempDir()) // never read the operator's ~/.vigolium
	prov := &silentProvider{}
	opts.Provider = prov
	opts.Model = "claude-sonnet-4"
	opts.Out = io.Discard
	opts.ToolLog = io.Discard
	opts.NoSkillFilter = true
	opts.SourcePath = t.TempDir()
	if opts.SessionDir == "" {
		opts.SessionDir = t.TempDir()
	}
	if opts.MaxTurns == 0 {
		opts.MaxTurns = 5
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	res, err := Run(ctx, opts)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if res.FindingCount != 0 {
		t.Fatalf("FindingCount = %d, want 0: this provider never reports", res.FindingCount)
	}
	return prov
}

// TestRunGrantsReportOnlyTurnOnSilentStop: a run that stops with nothing
// reported re-enters once with the report-only mission. Before the report-only
// turn existed this test saw no such prompt, and the verified work stayed in
// the transcript forever.
func TestRunGrantsReportOnlyTurnOnSilentStop(t *testing.T) {
	prov := runSilent(t, Options{})
	if !prov.sawPrompt("Final turn: report what you verified") {
		t.Fatalf("report-only turn was not granted (%d prompts seen)", prov.promptCount())
	}
	// The context is reset for that turn, so the prompt has to say where the
	// evidence and the artifacts live.
	if !prov.sawPrompt("transcript.jsonl") {
		t.Errorf("report prompt does not point at the run transcript")
	}
	if !prov.sawPrompt("autopilot/findings.json") {
		t.Errorf("report prompt does not name the findings artifact")
	}
}

// TestRunNoFinalReportTurnOptOut: the re-entry is opt-out, so callers that want
// the old stop-with-nothing behaviour still get it.
func TestRunNoFinalReportTurnOptOut(t *testing.T) {
	prov := runSilent(t, Options{NoFinalReportTurn: true})
	if prov.sawPrompt("Final turn: report what you verified") {
		t.Fatalf("report-only turn ran despite NoFinalReportTurn")
	}
}

// TestBuildFinalReportPromptModeAware: enforced mode never registers
// report_finding (the operator proposes candidates and a verifier promotes
// them), so the report-only mission must name the tool that mode has.
func TestBuildFinalReportPromptModeAware(t *testing.T) {
	shadow := buildFinalReportPrompt(Options{Mode: "shadow", SessionDir: "/tmp/s"})
	if !strings.Contains(shadow, "`report_finding`") {
		t.Errorf("shadow mode prompt should name report_finding:\n%s", shadow)
	}
	enforced := buildFinalReportPrompt(Options{Mode: "enforced", SessionDir: "/tmp/s"})
	if !strings.Contains(enforced, "`propose_candidate`") {
		t.Errorf("enforced mode prompt should name propose_candidate:\n%s", enforced)
	}
	if strings.Contains(enforced, "`report_finding`") {
		t.Errorf("enforced mode prompt names report_finding, which that mode does not register:\n%s", enforced)
	}
}

// TestRunHonoursConfiguredStallLimits: the stall/section budgets reach the
// section controller instead of being pinned to the package constants, and a
// stall-stopped run still gets its report-only turn. With the defaults (12
// stalled turns, 3 rotations) this provider would need ~36 turns to reach the
// same stop, so the tool-call ceiling is what proves the knobs took effect.
func TestRunHonoursConfiguredStallLimits(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	prov := &loopingProvider{}
	var toolLog bytes.Buffer
	opts := Options{
		Provider:           prov,
		Model:              "claude-sonnet-4",
		Out:                io.Discard,
		ToolLog:            &toolLog,
		NoSkillFilter:      true,
		SourcePath:         t.TempDir(),
		SessionDir:         t.TempDir(),
		Mode:               "shadow",
		MaxTurns:           60,
		MaxTurnsPerSection: 10,
		StallTurns:         2,
		MaxStallRotations:  1,
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	res, err := Run(ctx, opts)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if res.ToolCalls > 12 {
		t.Errorf("ToolCalls = %d: the configured stall limits (2 turns, 1 rotation) did not bound the run", res.ToolCalls)
	}
	if !prov.sawPrompt("Final turn: report what you verified") {
		t.Fatalf("a stall-stopped run did not get the report-only turn (%d prompts seen)", prov.promptCount())
	}
	// The report-only turn recovers evidence by reading — unproductive to the
	// stall detector. It must survive that, or the rescue reports nothing
	// (observed live: the turn was killed after a single read).
	if got := strings.Count(toolLog.String(), "[autopilot] no productive tool call across"); got != 1 {
		t.Errorf("stall guard fired %d times; the report-only turn must not be stall-killed:\n%s",
			got, toolLog.String())
	}
}
