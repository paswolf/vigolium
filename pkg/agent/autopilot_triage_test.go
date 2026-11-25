package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/vigolium/vigolium/internal/config"
	"github.com/vigolium/vigolium/pkg/database"
)

func TestRunAutopilotTriageRequiresEngineAndRepo(t *testing.T) {
	// nil engine/repo must be rejected before any work — the caller treats
	// the error as non-fatal, but it must not panic or no-op silently.
	_, err := RunAutopilotTriage(context.Background(), nil, nil, AutopilotTriageParams{
		ProjectUUID:     "p",
		AgenticScanUUID: "a",
	})
	if err == nil {
		t.Fatal("expected error when engine and repository are nil")
	}
}

func TestRunAutopilotTriageFitsBoundedProvider(t *testing.T) {
	ctx := context.Background()
	repo := newAuditTestRepo(t)
	project := database.DefaultProjectUUID
	parent, child, unrelated := uuid.NewString(), uuid.NewString(), uuid.NewString()
	createRun(t, repo, parent, "")
	createRun(t, repo, child, parent)
	createRun(t, repo, unrelated, "")
	saveDraftFinding(t, repo, project, unrelated, "other-run")
	wantSeen := make(map[string]int)
	for i := range 55 {
		run := parent
		switch i {
		case 0:
			run = child
		case 54:
			run = "" // pre-scan finding
		}
		hash := fmt.Sprint(i)
		require.NoError(t, repo.SaveFindingDirect(ctx, &database.Finding{
			ProjectUUID: project, AgenticScanUUID: run, FindingHash: hash,
			Status: database.StatusDraft, Confidence: "firm", Severity: database.SeverityHigh,
			Description: strings.Repeat("<", 10000),
		}))
		wantSeen[hash] = 1
	}

	var mu sync.Mutex
	seen := make(map[string]int)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Tools    []json.RawMessage `json:"tools"`
			Messages []struct {
				Role    string `json:"role"`
				Content string `json:"content"`
			} `json:"messages"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil || len(req.Tools) != 0 || len(req.Messages) != 2 {
			http.Error(w, "expected one tool-free request per batch", http.StatusBadRequest)
			return
		}
		if len(req.Messages[0].Content)+len(req.Messages[1].Content) > 50<<10 {
			http.Error(w, `{"error":{"type":"exceed_context_size_error","message":"request exceeds the available context size"}}`, http.StatusBadRequest)
			return
		}
		out := TriageResult{Verdict: "done"}
		for line := range strings.Lines(req.Messages[1].Content) {
			var row struct {
				Hash string `json:"finding_hash"`
			}
			if err := json.Unmarshal([]byte(line), &row); err != nil {
				http.Error(w, "invalid finding summary", http.StatusBadRequest)
				return
			}
			mu.Lock()
			seen[row.Hash]++
			mu.Unlock()
			switch row.Hash {
			case "0":
				out.Confirmed = append(out.Confirmed, TriagedFinding{FindingHash: row.Hash, Reason: "stored evidence supports finding"})
				out.FalsePositives = append(out.FalsePositives, TriagedFinding{FindingHash: "other-run"})
			case "54":
				out.FalsePositives = append(out.FalsePositives, TriagedFinding{FindingHash: row.Hash, Reason: "stored evidence refutes finding"})
			}
		}
		body, _ := json.Marshal(out)
		frame, _ := json.Marshal(map[string]any{"choices": []any{
			map[string]any{"delta": map[string]string{"content": string(body)}, "finish_reason": "stop"},
		}})
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = fmt.Fprintf(w, "data: %s\n\ndata: [DONE]\n\n", frame)
	}))
	defer srv.Close()

	settings := &config.Settings{}
	settings.Agent.Olium = config.OliumConfig{
		Provider: "openai-compatible",
		CustomProvider: config.CustomProviderConfig{
			BaseURL: srv.URL + "/v1", ModelID: "test-model", APIKey: "test-key",
		},
	}
	res, err := RunAutopilotTriage(ctx, NewEngine(settings, repo), repo, AutopilotTriageParams{
		ProjectUUID: project, AgenticScanUUID: parent,
	})
	require.NoError(t, err)
	mu.Lock()
	require.Equal(t, wantSeen, seen)
	mu.Unlock()
	require.Equal(t, 1, res.Confirmed)
	require.Equal(t, 1, res.FalsePositives)
	require.Equal(t, database.StatusTriaged, findingStatusByHash(t, repo, project, "0"))
	require.Equal(t, database.StatusFalsePositive, findingStatusByHash(t, repo, project, "54"))
	require.Equal(t, database.StatusDraft, findingStatusByHash(t, repo, project, "1"))
	require.Equal(t, database.StatusDraft, findingStatusByHash(t, repo, project, "other-run"))
}
