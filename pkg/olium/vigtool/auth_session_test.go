package vigtool

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/vigolium/vigolium/pkg/database"
)

func TestAuthSessionLookupRequiresHostname(t *testing.T) {
	tool := NewAuthSessionLookupTool(&SessionsContext{})
	res, _ := tool.Execute(context.Background(), map[string]any{}, nil)
	if !res.IsError {
		t.Fatal("expected IsError=true when hostname missing")
	}
	if !strings.Contains(res.Content, "hostname") {
		t.Errorf("expected hostname mention in error, got: %s", res.Content)
	}
}

func TestAuthSessionLookupReportsMissingRepo(t *testing.T) {
	tool := NewAuthSessionLookupTool(nil)
	res, _ := tool.Execute(context.Background(), map[string]any{"hostname": "x"}, nil)
	if !res.IsError {
		t.Error("nil context should produce error result")
	}
}

func TestSummarizeAuthSessionPopulatesUsageHint(t *testing.T) {
	hyd := time.Now().UTC()
	r := &database.AuthenticationHostname{
		Hostname:    "app.example.com",
		SessionName: "primary",
		SessionRole: "primary",
		Headers:     map[string]string{"Cookie": "session=abc", "Authorization": "Bearer x"},
		HydratedAt:  &hyd,
		Source:      "auth-config",
	}
	got := summarizeAuthSession(r)
	if got.Hostname != "app.example.com" || got.Name != "primary" {
		t.Errorf("identity fields wrong: %+v", got)
	}
	if !got.Hydrated {
		t.Error("Hydrated should be true when HydratedAt is set")
	}
	if len(got.Headers) != 2 {
		t.Errorf("Headers should be passed through, got %d entries", len(got.Headers))
	}
	if got.UsageHint == "" {
		t.Error("UsageHint should always be populated so the model knows what to do with the result")
	}
}

func TestSummarizeAuthSessionUnhydrated(t *testing.T) {
	r := &database.AuthenticationHostname{
		Hostname:    "app.example.com",
		SessionName: "secondary",
	}
	got := summarizeAuthSession(r)
	if got.Hydrated {
		t.Error("Hydrated should be false when HydratedAt is nil")
	}
	if got.HydratedAt != "" {
		t.Errorf("HydratedAt should be empty, got %q", got.HydratedAt)
	}
	if got.Headers == nil {
		t.Error("Headers should always be a non-nil map (json marshaling friendlier)")
	}
}

func TestAuthToolMetadata(t *testing.T) {
	cases := []struct {
		name string
		t    interface {
			Name() string
			IsReadOnly() bool
		}
		readOnly bool
	}{
		{"list_auth_sessions", NewListAuthSessionsTool(&SessionsContext{}), true},
		{"auth_session_lookup", NewAuthSessionLookupTool(&SessionsContext{}), true},
	}
	for _, c := range cases {
		if c.t.Name() != c.name {
			t.Errorf("Name() = %q, want %q", c.t.Name(), c.name)
		}
		if c.t.IsReadOnly() != c.readOnly {
			t.Errorf("%s IsReadOnly() = %v, want %v", c.name, c.t.IsReadOnly(), c.readOnly)
		}
	}
}

// saveSessionHostnames writes the session rows a prepared auth state would
// carry (two API sessions at one host), so the lookup tool has something
// concrete to advertise on a miss.
func saveSessionHostnames(t *testing.T, repo *database.Repository, projectUUID string) {
	t.Helper()
	rows := []*database.AuthenticationHostname{
		{ProjectUUID: projectUUID, Hostname: "app.example.com", SessionName: "admin_api", SessionRole: "compare",
			Headers: map[string]string{"Authorization": "Basic YWRtaW4="}},
		{ProjectUUID: projectUUID, Hostname: "app.example.com", SessionName: "lowpriv_api", SessionRole: "compare",
			Headers: map[string]string{"Authorization": "Basic bG93"}},
	}
	if err := repo.SaveAuthenticationHostnames(context.Background(), rows); err != nil {
		t.Fatalf("SaveAuthenticationHostnames: %v", err)
	}
}

// TestAuthSessionLookupUnknownNameListsConfiguredSessions locks the fix for the
// operator that spent its run calling auth_session_lookup with `admin_backend`:
// a wrong name must answer with the names that actually exist.
func TestAuthSessionLookupUnknownNameListsConfiguredSessions(t *testing.T) {
	repo := newVigtoolTestRepo(t)
	ctx := context.Background()
	project := database.DefaultProjectUUID
	saveSessionHostnames(t, repo, project)

	tool := NewAuthSessionLookupTool(&SessionsContext{Repo: repo, ProjectUUID: project})
	res, err := tool.Execute(ctx, map[string]any{"hostname": "app.example.com", "name": "admin_backend"}, nil)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if !res.IsError {
		t.Fatalf("a wrong session name must error, got %q", res.Content)
	}
	const want = `auth_session_lookup: no session named "admin_backend"; available: admin_api, lowpriv_api`
	if res.Content != want {
		t.Errorf("Content = %q, want %q", res.Content, want)
	}
}

// TestAuthSessionLookupUnknownHostnameListsProjectSessions covers the other
// guessing mode — probing a host that has no sessions — where the answer has to
// include the host so the caller can retry in the right place.
func TestAuthSessionLookupUnknownHostnameListsProjectSessions(t *testing.T) {
	repo := newVigtoolTestRepo(t)
	ctx := context.Background()
	project := database.DefaultProjectUUID
	saveSessionHostnames(t, repo, project)

	tool := NewAuthSessionLookupTool(&SessionsContext{Repo: repo, ProjectUUID: project})
	res, err := tool.Execute(ctx, map[string]any{"hostname": "other.example.com"}, nil)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if !res.IsError {
		t.Fatalf("an unknown hostname with project sessions must error, got %q", res.Content)
	}
	const want = "auth_session_lookup: no auth session for other.example.com; available: " +
		"admin_api@app.example.com, lowpriv_api@app.example.com"
	if res.Content != want {
		t.Errorf("Content = %q, want %q", res.Content, want)
	}
}
