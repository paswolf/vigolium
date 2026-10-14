package agent

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/vigolium/vigolium/pkg/agent/authsession"
)

// TestNormalizeAutopilotSessionConfigWarningNamesDroppedAndUsable locks the
// regression behind the opaque "some discovered auth sessions were invalid and
// could not be repaired" warning: the operator was told nothing about which
// sessions exist, so it guessed names (`admin_backend`) that were never
// configured and burned its run on failing lookups. A nil engine models the
// unrepairable path (the repair LLM is unavailable).
func TestNormalizeAutopilotSessionConfigWarningNamesDroppedAndUsable(t *testing.T) {
	cfg := &AutopilotPipelineConfig{}
	sessionCfg := &AgentSessionConfig{Sessions: []AgentSessionEntry{
		{Name: "admin_api", Role: "compare", Headers: map[string]string{"Authorization": "Basic YWRtaW4="}},
		{Name: "lowpriv_api", Role: "compare", Headers: map[string]string{"Authorization": "Basic bG93"}},
		{Name: "admin_backend", Role: "backend", Headers: map[string]string{"Cookie": "BE=xyz"}},
	}}

	var warnings []string
	got := normalizeAutopilotSessionConfig(context.Background(), nil, cfg, sessionCfg, "", &warnings)
	if got == nil || len(got.Sessions) != 2 {
		t.Fatalf("expected the two valid sessions to survive, got %+v", got)
	}
	if len(warnings) != 1 {
		t.Fatalf("expected exactly one warning, got %v", warnings)
	}

	w := warnings[0]
	if !strings.HasPrefix(w, invalidSessionWarningPrefix) {
		t.Errorf("warning must keep its leading phrase, got %q", w)
	}
	const want = `some discovered auth sessions were invalid and could not be repaired — dropped: admin_backend (role must be "primary" or "compare", got: backend); usable: admin_api, lowpriv_api`
	if w != want {
		t.Errorf("warning =\n%q\nwant\n%q", w, want)
	}
	for _, part := range []string{
		`dropped: admin_backend (role must be "primary" or "compare"`,
		"usable: admin_api, lowpriv_api",
	} {
		if !strings.Contains(w, part) {
			t.Errorf("warning %q missing %q", w, part)
		}
	}
}

// TestFormatInvalidSessionWarningReportsUnnamedEntry covers the empty-name
// case: the detail must still name the entry rather than render "()".
func TestFormatInvalidSessionWarningReportsUnnamedEntry(t *testing.T) {
	w := formatInvalidSessionWarning([]authsession.InvalidSessionEntry{
		{Entry: AgentSessionEntry{Name: "  ", Role: "backend"}},
	}, nil)
	if !strings.Contains(w, "dropped: (unnamed)") {
		t.Errorf("unnamed entry should be labelled, got %q", w)
	}
	if !strings.Contains(w, "usable: none") {
		t.Errorf("no surviving sessions should read as none, got %q", w)
	}
}

// TestBriefListsUsableSessionsAndUncapturedBrowser locks change 1's brief
// contract: both prompt shapes name the sessions that exist, and a
// browser-driven plan with no captured browser session says so and points at
// browser_auth.
func TestBriefListsUsableSessionsAndUncapturedBrowser(t *testing.T) {
	dir := t.TempDir()
	browserPath := filepath.Join(dir, "browser-session.json")
	if err := os.WriteFile(browserPath, []byte(`{"status":"pending"}`), 0o644); err != nil {
		t.Fatal(err)
	}

	prepared := &AutopilotPreparedAuth{Requested: true, Source: "source-analysis", SessionCount: 2}
	cfg := AutopilotPipelineConfig{
		TargetURL: "https://app.example.com",
		SessionConfig: &AgentSessionConfig{Sessions: []AgentSessionEntry{
			{Name: "admin_api", Role: "compare", Headers: map[string]string{"Authorization": "Basic x"}},
			{Name: "lowpriv_api", Role: "compare", Headers: map[string]string{"Authorization": "Basic y"}},
		}},
		PreparedAuth: prepared,
		ContextBundle: &AutopilotContextBundle{
			BrowserDecision: "browser_recommended",
			PreparedAuth:    prepared,
		},
		Artifacts: AutopilotArtifactSpec{BrowserSessionPath: browserPath},
	}

	const (
		sessionsLine = "- Usable auth sessions: admin_api, lowpriv_api"
		browserLine  = "browser session not captured — call `browser_auth` before probing authenticated routes."
	)
	for name, prompt := range map[string]string{
		"target": buildTargetPrompt(cfg, nil, false),
		"source": buildSourceOnlyPrompt(cfg, nil, false),
	} {
		if !strings.Contains(prompt, sessionsLine) {
			t.Errorf("%s prompt missing %q", name, sessionsLine)
		}
		if !strings.Contains(prompt, browserLine) {
			t.Errorf("%s prompt missing %q", name, browserLine)
		}
	}

	// A populated browser-session artifact means a session was captured: the
	// note must disappear so it does not send the operator to browser_auth
	// for state it already has.
	if err := os.WriteFile(browserPath, []byte(`{"status":"captured"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(buildTargetPrompt(cfg, nil, false), browserLine) {
		t.Error("captured browser session should not emit the not-captured note")
	}
}
