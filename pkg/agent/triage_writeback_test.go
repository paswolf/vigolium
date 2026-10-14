package agent

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/vigolium/vigolium/pkg/database"
)

// findingStatusByHash loads the lifecycle status of a single project-scoped
// finding by its hash.
func findingStatusByHash(t *testing.T, repo *database.Repository, projectUUID, hash string) string {
	t.Helper()
	var f database.Finding
	if err := repo.DB().NewSelect().Model(&f).
		Where("project_uuid = ?", projectUUID).
		Where("finding_hash = ?", hash).
		Scan(context.Background()); err != nil {
		t.Fatalf("load finding %s: %v", hash, err)
	}
	return f.Status
}

func saveDraftFinding(t *testing.T, repo *database.Repository, projectUUID, agenticScanUUID, hash string) {
	t.Helper()
	if err := repo.SaveFindingDirect(context.Background(), &database.Finding{
		FindingHash:     hash,
		ProjectUUID:     projectUUID,
		AgenticScanUUID: agenticScanUUID,
		Status:          database.StatusDraft,
		Severity:        database.SeverityHigh,
	}); err != nil {
		t.Fatalf("save finding %s: %v", hash, err)
	}
}

func createRun(t *testing.T, repo *database.Repository, uuid, parent string) {
	t.Helper()
	if err := repo.CreateAgenticScan(context.Background(), &database.AgenticScan{
		UUID:                  uuid,
		ProjectUUID:           database.DefaultProjectUUID,
		Mode:                  "autopilot",
		AgentName:             "olium",
		Status:                "completed",
		ParentAgenticScanUUID: parent,
		CompletedAt:           time.Now(),
	}); err != nil {
		t.Fatalf("create run %s: %v", uuid, err)
	}
}

// TestWriteBackTriageVerdictsChildRuns locks the autopilot regression: the
// parent run's post-scan triage writes back for the parent uuid, but the audit
// the parent spawned owns its imported findings under the CHILD run uuid.
// Scoping the write to the parent uuid alone leaves every audit finding at
// draft, invisible to `vigolium finding --verdict confirmed,triaged` and never
// reported. Before the fix this test's child-run assertion failed.
func TestWriteBackTriageVerdictsChildRuns(t *testing.T) {
	repo := newAuditTestRepo(t)
	ctx := context.Background()

	parent := uuid.NewString()
	createRun(t, repo, parent, "")

	child := uuid.NewString()
	createRun(t, repo, child, parent)
	otherChild := uuid.NewString()
	createRun(t, repo, otherChild, parent)
	// A run that is not a child of the parent must not be touched.
	unrelated := uuid.NewString()
	createRun(t, repo, unrelated, "")

	childHash := uuid.NewString()
	otherChildHash := uuid.NewString()
	unrelatedHash := uuid.NewString()
	saveDraftFinding(t, repo, database.DefaultProjectUUID, child, childHash)
	saveDraftFinding(t, repo, database.DefaultProjectUUID, otherChild, otherChildHash)
	saveDraftFinding(t, repo, database.DefaultProjectUUID, unrelated, unrelatedHash)

	writeBackTriageVerdicts(ctx, repo, database.DefaultProjectUUID, parent, &TriageResult{
		Confirmed: []TriagedFinding{{FindingHash: childHash}, {FindingHash: otherChildHash}},
	})

	if got := findingStatusByHash(t, repo, database.DefaultProjectUUID, childHash); got != database.StatusTriaged {
		t.Errorf("child-run finding status = %q, want %q — parent triage must reach child-owned findings", got, database.StatusTriaged)
	}
	if got := findingStatusByHash(t, repo, database.DefaultProjectUUID, otherChildHash); got != database.StatusTriaged {
		t.Errorf("second child-run finding status = %q, want %q", got, database.StatusTriaged)
	}
	if got := findingStatusByHash(t, repo, database.DefaultProjectUUID, unrelatedHash); got != database.StatusDraft {
		t.Errorf("unrelated run finding status = %q, want %q — must not be flipped", got, database.StatusDraft)
	}
}

// TestWriteBackTriageVerdictsUnattributedRows covers the other half of the same
// read/write asymmetry: the triage reads findings project-wide, but pre-scan and
// passive findings are stored with no agentic_scan_uuid at all, so a run-scoped
// write never matched them and their verdicts were dropped (observed live: rows
// the triage called false positives kept the scanner's `triaged` and stayed in
// reports). A row owned by another run must still be left alone.
func TestWriteBackTriageVerdictsUnattributedRows(t *testing.T) {
	repo := newAuditTestRepo(t)
	ctx := context.Background()

	run := uuid.NewString()
	other := uuid.NewString()
	createRun(t, repo, run, "")
	createRun(t, repo, other, "")

	unattributedHash := uuid.NewString()
	otherHash := uuid.NewString()
	saveDraftFinding(t, repo, database.DefaultProjectUUID, "", unattributedHash)
	saveDraftFinding(t, repo, database.DefaultProjectUUID, other, otherHash)

	writeBackTriageVerdicts(ctx, repo, database.DefaultProjectUUID, run, &TriageResult{
		FalsePositives: []TriagedFinding{{FindingHash: unattributedHash}, {FindingHash: otherHash}},
	})

	if got := findingStatusByHash(t, repo, database.DefaultProjectUUID, unattributedHash); got != database.StatusFalsePositive {
		t.Errorf("unattributed finding status = %q, want %q — a verdict on a row with no run uuid must land", got, database.StatusFalsePositive)
	}
	if got := findingStatusByHash(t, repo, database.DefaultProjectUUID, otherHash); got != database.StatusDraft {
		t.Errorf("other run's finding status = %q, want %q — must not be flipped", got, database.StatusDraft)
	}
}

// TestWriteBackTriageVerdictsNoChildrenFallback covers the fallback path: a run
// with no child audit runs writes back exactly as before, by its own uuid, and
// an empty agenticScanUUID keeps project-only scoping.
func TestWriteBackTriageVerdictsNoChildrenFallback(t *testing.T) {
	repo := newAuditTestRepo(t)
	ctx := context.Background()

	run := uuid.NewString()
	other := uuid.NewString()
	runHash := uuid.NewString()
	otherHash := uuid.NewString()
	unscopedHash := uuid.NewString()
	saveDraftFinding(t, repo, database.DefaultProjectUUID, run, runHash)
	saveDraftFinding(t, repo, database.DefaultProjectUUID, other, otherHash)
	saveDraftFinding(t, repo, database.DefaultProjectUUID, other, unscopedHash)

	// No children exist for `run`: its own finding still flips, and another
	// run's finding with a different hash is untouched.
	writeBackTriageVerdicts(ctx, repo, database.DefaultProjectUUID, run, &TriageResult{
		Confirmed: []TriagedFinding{{FindingHash: runHash}},
	})
	if got := findingStatusByHash(t, repo, database.DefaultProjectUUID, runHash); got != database.StatusTriaged {
		t.Errorf("run finding status = %q, want %q", got, database.StatusTriaged)
	}
	if got := findingStatusByHash(t, repo, database.DefaultProjectUUID, otherHash); got != database.StatusDraft {
		t.Errorf("other finding status = %q, want %q", got, database.StatusDraft)
	}

	// Empty run uuid (the pipeline path) scopes by project alone, so the hash
	// flips regardless of which run owns the row.
	writeBackTriageVerdicts(ctx, repo, database.DefaultProjectUUID, "", &TriageResult{
		Confirmed: []TriagedFinding{{FindingHash: unscopedHash}},
	})
	if got := findingStatusByHash(t, repo, database.DefaultProjectUUID, unscopedHash); got != database.StatusTriaged {
		t.Errorf("project-scoped write status = %q, want %q", got, database.StatusTriaged)
	}
}
