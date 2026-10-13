package agent

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/vigolium/vigolium/internal/config"
	oengine "github.com/vigolium/vigolium/pkg/olium/engine"
)

// turnCeilingSSE answers every provider call with a complete tool call, so the
// engine keeps looping until its turn ceiling fires instead of ending the run
// on its own. The tool name is a builtin that IncludeTools registers and that
// fails fast on empty arguments (bash rejects an empty command without
// spawning a shell), so each turn is dispatched and consumed promptly.
func turnCeilingSSE(w http.ResponseWriter) {
	w.Header().Set("Content-Type", "text/event-stream")
	w.WriteHeader(http.StatusOK)
	_, _ = fmt.Fprint(w, "data: "+`{"choices":[{"delta":{"tool_calls":[{"index":0,"id":"call_bash","type":"function","function":{"name":"bash","arguments":"{}"}}]},"finish_reason":"tool_calls"}]}`+"\n\n")
	if f, ok := w.(http.Flusher); ok {
		f.Flush()
	}
	_, _ = fmt.Fprint(w, "data: [DONE]\n\n")
}

// TestMaxTurnsResolution pins the turn ceiling an engine built from config ends
// up with: a per-call SessionSpec.MaxTurns (guardrail 1, intent 30, verifier
// cap) wins, otherwise agent.olium.max_turns applies, otherwise the engine's
// own default (32) stands.
//
// The fallback is the point of the test: triage and the other shared-engine
// callers pass no per-call cap, so a dropped config value silently capped them
// at 32 turns — a 25-finding triage batch blew through it and aborted the pass.
func TestMaxTurnsResolution(t *testing.T) {
	cases := []struct {
		name      string
		cfgTurns  int
		specTurns int
		want      string
	}{
		{name: "config knob reaches the engine", cfgTurns: 7, specTurns: 0, want: "exceeded max turns (7)"},
		{name: "per-call override wins", cfgTurns: 7, specTurns: 3, want: "exceeded max turns (3)"},
		{name: "neither set keeps the engine default", cfgTurns: 0, specTurns: 0, want: "exceeded max turns (32)"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				turnCeilingSSE(w)
			}))
			defer srv.Close()

			cfg := &config.OliumConfig{
				Provider: "openai-compatible",
				MaxTurns: tc.cfgTurns,
				CustomProvider: config.CustomProviderConfig{
					BaseURL: srv.URL + "/v1",
					ModelID: "test-model",
					APIKey:  "test-key",
				},
			}

			eng, err := buildOliumEngineWithSpec(cfg, SessionSpec{IncludeTools: true, MaxTurns: tc.specTurns})
			if err != nil {
				t.Fatalf("build engine: %v", err)
			}

			ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
			defer cancel()

			var errMsg string
			for ev := range eng.Run(ctx, "go") {
				if ev.Type == oengine.EventError {
					errMsg = ev.Err
					break
				}
				if ev.Type == oengine.EventRunDone {
					break
				}
			}

			if !strings.Contains(errMsg, tc.want) {
				t.Fatalf("engine stopped with %q, want it to contain %q", errMsg, tc.want)
			}
		})
	}
}
