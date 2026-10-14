package agent

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/google/uuid"

	"github.com/vigolium/vigolium/pkg/database"
)

// readArtifactRows reads a findings/dismissed artifact as a JSON array.
func readArtifactRows(t *testing.T, path string) []map[string]any {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	var rows []map[string]any
	if err := json.Unmarshal(data, &rows); err != nil {
		t.Fatalf("parse %s as a JSON array: %v (content %q)", path, err, string(data))
	}
	return rows
}

// TestSynthesizeAutopilotFindingArtifactsFromDB locks the second half of the
// regression: two autopilot runs ended completed_with_warnings with
// "artifact missing: findings.json" while the judged rows sat in the project
// DB. Synthesis must derive both artifacts from the run tree (the run plus the
// child audit run it spawned), keep draft rows out, and carry the evidence
// columns the verifier keys on.
func TestSynthesizeAutopilotFindingArtifactsFromDB(t *testing.T) {
	repo := newAuditTestRepo(t)
	ctx := context.Background()

	dir := t.TempDir()
	spec := AutopilotArtifactSpec{
		FindingsPath:  filepath.Join(dir, "findings.json"),
		DismissedPath: filepath.Join(dir, "dismissed.json"),
		EvidenceDir:   filepath.Join(dir, "evidence"),
	}

	project := database.DefaultProjectUUID
	run := uuid.NewString()
	child := uuid.NewString()
	createRun(t, repo, run, "")
	createRun(t, repo, child, run)

	save := func(hash, agenticScan, status string) {
		t.Helper()
		f := &database.Finding{
			FindingHash:     hash,
			ProjectUUID:     project,
			AgenticScanUUID: agenticScan,
			Status:          status,
			Severity:        database.SeverityHigh,
			Request:         "GET /api/customers HTTP/1.1\nHost: app.example.com\n\n",
			Response:        "HTTP/1.1 200 OK\n\n[...]",
		}
		if err := repo.SaveFindingDirect(ctx, f); err != nil {
			t.Fatalf("save finding %s: %v", hash, err)
		}
	}
	save("run-triaged", run, database.StatusTriaged)
	save("child-confirmed", child, database.StatusConfirmed)
	save("run-dismissed", run, database.StatusFalsePositive)
	save("child-draft", child, database.StatusDraft)

	synth, err := synthesizeAutopilotFindingArtifacts(ctx, repo, spec, project, run, "")
	if err != nil {
		t.Fatalf("synthesize: %v", err)
	}
	if !synth.Findings || !synth.Dismissed {
		t.Fatalf("both artifacts should be synthesized, got %+v", synth)
	}

	confirmed := readArtifactRows(t, spec.FindingsPath)
	if len(confirmed) != 2 {
		t.Fatalf("findings.json = %d entries, want 2 (run + child run): %v", len(confirmed), confirmed)
	}
	dismissed := readArtifactRows(t, spec.DismissedPath)
	if len(dismissed) != 1 {
		t.Fatalf("dismissed.json = %d entries, want 1: %v", len(dismissed), dismissed)
	}
	if got := dismissed[0]["finding_hash"]; got != "run-dismissed" {
		t.Errorf("dismissed.json entry = %v, want run-dismissed", got)
	}
	for _, row := range confirmed {
		if row["finding_hash"] == "child-draft" {
			t.Error("draft (unjudged) finding must not appear in findings.json")
		}
		if row["request"] == "" || row["request"] == nil {
			t.Errorf("entry %v lost its request evidence", row["finding_hash"])
		}
	}

	// The verifier must count the synthesized rows and record the provenance.
	report := verifyAutopilotArtifacts(spec, synth)
	if report.ConfirmedCount != 2 || report.DismissedCount != 1 {
		t.Errorf("verification counts = (%d,%d), want (2,1)",
			report.ConfirmedCount, report.DismissedCount)
	}
	if !report.FindingsSynthesized || !report.DismissedSynthesized {
		t.Errorf("verification should record synthesis, got %+v", report)
	}
	for _, w := range report.Warnings {
		if w == "artifact missing: findings.json" || w == "artifact missing: dismissed.json" {
			t.Errorf("synthesized artifacts must not be reported missing: %v", report.Warnings)
		}
	}
}

// TestSynthesizeAutopilotFindingArtifactsPreservesOperatorFile locks the
// precedence rule: an operator-authored artifact is never overwritten, while a
// genuinely absent one with no matching rows is written as `[]` so readers see
// an empty verdict rather than a missing file.
func TestSynthesizeAutopilotFindingArtifactsPreservesOperatorFile(t *testing.T) {
	repo := newAuditTestRepo(t)
	ctx := context.Background()

	dir := t.TempDir()
	spec := AutopilotArtifactSpec{
		FindingsPath:  filepath.Join(dir, "findings.json"),
		DismissedPath: filepath.Join(dir, "dismissed.json"),
		EvidenceDir:   filepath.Join(dir, "evidence"),
	}

	project := database.DefaultProjectUUID
	run := uuid.NewString()
	createRun(t, repo, run, "")
	if err := repo.SaveFindingDirect(ctx, &database.Finding{
		FindingHash:     "run-triaged",
		ProjectUUID:     project,
		AgenticScanUUID: run,
		Status:          database.StatusTriaged,
		Severity:        database.SeverityHigh,
		Request:         "GET / HTTP/1.1\n\n",
	}); err != nil {
		t.Fatalf("save finding: %v", err)
	}

	const operatorContent = `[{"title":"operator verdict","evidence":"poc.md"}]`
	if err := os.WriteFile(spec.FindingsPath, []byte(operatorContent), 0o644); err != nil {
		t.Fatal(err)
	}

	synth, err := synthesizeAutopilotFindingArtifacts(ctx, repo, spec, project, run, "")
	if err != nil {
		t.Fatalf("synthesize: %v", err)
	}
	if synth.Findings {
		t.Error("an operator-authored findings.json must not be reported as synthesized")
	}
	if !synth.Dismissed {
		t.Error("the absent dismissed.json should still be synthesized")
	}

	got, err := os.ReadFile(spec.FindingsPath)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != operatorContent {
		t.Errorf("operator-authored findings.json was modified: %q", string(got))
	}
	if rows := readArtifactRows(t, spec.DismissedPath); len(rows) != 0 {
		t.Errorf("dismissed.json = %v, want []", rows)
	}
}

// TestSynthesizeAutopilotFindingArtifactsNoRepoIsNoop guards the degraded
// paths: without a repo or project there is nothing authoritative to read, so
// synthesis must not invent empty artifacts.
func TestSynthesizeAutopilotFindingArtifactsNoRepoIsNoop(t *testing.T) {
	dir := t.TempDir()
	spec := AutopilotArtifactSpec{
		FindingsPath:  filepath.Join(dir, "findings.json"),
		DismissedPath: filepath.Join(dir, "dismissed.json"),
	}
	synth, err := synthesizeAutopilotFindingArtifacts(context.Background(), nil, spec, "project", "run", "")
	if err != nil {
		t.Fatalf("synthesize: %v", err)
	}
	if synth.Findings || synth.Dismissed {
		t.Errorf("nil repo should synthesize nothing, got %+v", synth)
	}
	if _, err := os.Stat(spec.FindingsPath); !os.IsNotExist(err) {
		t.Error("nil repo must not write findings.json")
	}
}
