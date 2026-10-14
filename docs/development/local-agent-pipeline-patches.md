# Local agent-pipeline patches (applied on top of v0.5.3)

This file documents the local patch stack applied to this checkout for the
autopilot/scan pipeline. It is **not** upstream work: it exists so the fixes
survive a rebase onto a newer release, and so it is obvious when a release has
made one redundant.

- Baseline: `82a92a92` (`v0.5.3` plus the earlier `agent.olium.max_turns` fix)
- Patch commit: `ce8fcf20` — *"Fixed some weird behaviour causing the scan to degrade"*
- This guide plus the two tests it adds (`pkg/cli/agent_autopilot_project_test.go`,
  `pkg/agent/autopilot_evidence_test.go`) ride in a follow-up commit on top of it.
- Diff: `git show ce8fcf20` / `git diff 82a92a92..ce8fcf20 --stat`
- Config side (separate, not in git): `vigolium-tuning.sh` in the scan working
  directory applies `agent.olium.*` settings via `vigolium config set`. If a
  release does not know a key, `config set` fails — that is the signal that the
  matching code patch is missing too.

Everything below is written in terms of **symbols and strings**, not line
numbers, because line numbers do not survive a rebase.

## What the patches encode (preserve these invariants when rebasing)

1. **Progress is target-facing.** The stall detector counts only tools that
   touch the target or record a result (`productiveTools`). Reading source,
   querying a database through `bash`, or replaying a request by hand is real
   work but counts as *zero* progress — so a policy that stops a run on
   "no progress" must not also throw the run's evidence away.
2. **The triage's read scope must equal its write scope.** It reads findings
   project-wide; therefore its verdicts must be able to land on any row it
   judged — a run's own findings, its child runs' findings, and rows with no run
   attribution at all.
3. **Artifacts are derived views.** `findings.json` / `dismissed.json` describe
   DB state; a model should never be the only writer of data the database
   already holds.

## Checking whether a newer release already has a fix

Run this against the new checkout; every line that prints `MISSING` is a patch
still worth reapplying (or, if the release fixed it differently, worth deleting
from this file).

```bash
check() { if grep -rq "$2" $3 2>/dev/null; then echo "ok       $1"; else echo "MISSING  $1"; fi; }
check "1 report-only turn (option)"        "NoFinalReportTurn"                     pkg/olium/autopilot
check "1 report-only turn (prompt)"        "buildFinalReportPrompt"                pkg/olium/autopilot
check "1 stall-guard exemption"            "SuppressStallGuard"                    pkg/olium/autopilot
check "1 stall halt source"                "HaltSourceStall"                       pkg/olium/autopilot
check "2 triage write across runs"         "UpdateFindingStatusByHashForRuns"      pkg/database
check "2 write reaches child runs"         "GetChildAgenticScans"                  pkg/agent/triage.go
check "3 write reaches unattributed rows"  "includeUnattributed"                   pkg/database pkg/agent
check "4 auth warning names sessions"      "formatInvalidSessionWarning"           pkg/agent
check "4 lookup lists configured names"    "sessionsForOtherHostnames"             pkg/olium/vigtool
check "5 artifact synthesis"               "synthesizeAutopilotFindingArtifacts"   pkg/agent
check "5 whitebox evidence heuristic"      "source_file"                           pkg/agent/autopilot_context.go
check "6 section budgets (config)"         "section_max_turns"                     internal/config
check "7 project-name error propagated"    "cannot scope this run to a project"    pkg/cli
```

## Regression suite

```bash
# The whole patch stack's tests, one command:
go test ./pkg/olium/autopilot/ ./pkg/agent/ ./pkg/database/ ./internal/config/ ./pkg/cli/ \
  -run 'TestRunGrantsReportOnlyTurn|TestRunNoFinalReportTurnOptOut|TestBuildFinalReportPromptModeAware|TestRunHonoursConfiguredStallLimits|TestWriteBackTriageVerdicts|TestUpdateFindingStatusByHashForRuns|TestHasEvidenceRecognisesWhiteboxProvenance|TestDefaultAgentConfig_Olium|TestAutopilotUnknownProjectNameFailsFast' \
  -count=1
```

Failing-before evidence, per case: the child-run and unattributed-row tests were
each run against the unpatched call and failed with the live symptom
(`draft` instead of `triaged` / `false_positive`); the evidence-heuristic test
fails with `source_file` removed from the key list; the report-only turn is
covered by a pair (one asserts the re-entry, `TestRunNoFinalReportTurnOptOut`
reproduces the old stop-with-nothing); `TestAutopilotUnknownProjectNameFailsFast`
pins a state observed live — a bogus `--project-name` used to run to completion
and write into the Default Project. The stall-budget and config-surface tests
cover knobs that did not exist before the patch.

## 1. A stopped run reports what it verified

- **Symptom.** A run is killed by the stall guard ("no productive tool call
  across N consecutive sections") and ends with `findings: 0` even though the
  operator verified real issues — its evidence only ever lived in the transcript.
- **Root cause.** `ShouldRotate`/`StalledOut` → `halt.SetByBudget` →
  `iterCancel()`: the stop path had no reporting step, and nothing distinguished
  a stall stop (caps unspent) from a budget stop (caps spent).
- **Fix.** One report-only re-entry before accepting the stop.
  - `halt.go`: `HaltSourceStall`, `HaltSourceReport`, `SetByStall`, `SetByReport`.
  - `autopilot.go`: `Options.NoFinalReportTurn`; `finalReportTried`,
    `finalReportDue(src)`, `reenterForFinalReport(reason, ctrl)` next to
    `nextPrompt := initial`; both `break` sites in the outer loop call it; the
    stall halt uses `SetByStall`; `buildFinalReportPrompt(opts)` (mode-aware:
    `propose_candidate` in `enforced` mode, `report_finding` otherwise).
  - `section.go`: `SuppressStalls` + `SuppressStallGuard()` + `StalledOut()`
    returning false while suppressed; the loop's rotation `switch` ends the run
    when `finalReportTried` is set (`SetByReport`) instead of rotating into a
    fresh mission.
- **Trap this hit live.** Without the exemption the guard killed the rescue turn
  after a single read; without the rotation branch a turn-cap rotation halted the
  rescue mid-report as `context canceled`.
- **Tests.** `TestRunGrantsReportOnlyTurnOnSilentStop`,
  `TestRunNoFinalReportTurnOptOut`, `TestBuildFinalReportPromptModeAware`,
  `TestRunHonoursConfiguredStallLimits` (`pkg/olium/autopilot/final_report_test.go`).

## 2. Triage verdicts reach the run's child runs

- **Symptom.** Audit findings (imported by the child audit run) stay `draft`
  forever: invisible to `vigolium finding --verdict confirmed,triaged` and absent
  from every report, while the triage log shows it judged them.
- **Root cause.** Read scope ≠ write scope. `RunAutopilotTriage` reads
  `QueryFilters{ProjectUUID}` but `writeBackTriageVerdicts` wrote with
  `UpdateFindingStatusByHash(project, agenticScanUUID, …)`, and the audit's
  findings carry the *child* run uuid (`audit_agent.go`,
  `BuildFindingsWithSource(… r.agenticScanUUID …)`).
- **Fix.** `pkg/database/repository_findings.go`:
  `UpdateFindingStatusByHashForRuns(ctx, projectUUID, []uuids, includeUnattributed, hash, status)`.
  `pkg/agent/triage.go`: resolve `repo.GetChildAgenticScans(ctx, agenticScanUUID)`
  once and write across the run ∪ its children (child-lookup failure degrades to
  the parent uuid, never further).
- **Tests.** `TestUpdateFindingStatusByHashForRuns` (`pkg/database`),
  `TestWriteBackTriageVerdictsChildRuns`,
  `TestWriteBackTriageVerdictsNoChildrenFallback` (`pkg/agent`).

## 3. Triage verdicts reach rows with no run uuid

- **Symptom.** The triage calls a finding a false positive; the row keeps
  `triaged` and stays in reports. Observed live: five pre-scan rows.
- **Root cause.** Same asymmetry: pre-scan/passive findings are stored with
  `agentic_scan_uuid` NULL, which `agentic_scan_uuid IN (…)` can never match.
- **Fix.** `includeUnattributed` in the same repository method
  (`agentic_scan_uuid IN (…) OR agentic_scan_uuid IS NULL OR agentic_scan_uuid = ''`),
  passed as `true` from `writeBackTriageVerdicts`. Rows owned by a *different*
  run still never match, so run isolation is preserved. An empty run list keeps
  the old project-only scope (an unattributed flag must not narrow it).
- **Tests.** `TestWriteBackTriageVerdictsUnattributedRows` (`pkg/agent`) plus the
  unattributed case in `TestUpdateFindingStatusByHashForRuns`.

## 4. Auth-session failures are actionable

- **Symptom.** The brief says only "some discovered auth sessions were invalid
  and could not be repaired"; the operator then spends the run guessing session
  names (`auth_session_lookup: no session named "admin_backend"…`) that never
  existed, instead of probing the target.
- **Root cause.** `normalizeAutopilotSessionConfig` dropped invalid entries and
  reported nothing about them; `RepairInvalidSessionConfig` swallowed its
  failure; the lookup tool returned a bare miss; the brief repeated the opaque
  line.
- **Fix.**
  - `pkg/agent/autopilot_auth.go`: `invalidSessionWarningPrefix`,
    `formatInvalidSessionWarning` (names each dropped entry + its validation
    errors + the surviving sessions), `agentSessionNames`.
  - `pkg/agent/session_repair.go`: log the engine error, the parse failure with
    a ≤200-char `raw_output` snippet, and `validation_errors` on both paths.
  - `pkg/olium/vigtool/auth_session.go`: a miss lists the configured names
    (`joinSessionNamesWithHost`, `sessionsForOtherHostnames`).
  - `pkg/agent/autopilot_pipeline.go`: the brief lists usable session names and
    adds "browser session not captured — call `browser_auth` before probing
    authenticated routes" when the decision is required/recommended and nothing
    was captured (`usableSessionNames`, `browserSessionUncaptured`).
- **Deliberately not changed.** The harness does not auto-run browser auth on a
  `browser_recommended` decision; the operator has `browser_auth` (a
  progress-counting tool) and is now told explicitly to use it.
- **Tests.** `pkg/agent/autopilot_auth_warning_test.go`,
  `TestAuthSessionLookupUnknownNameListsConfiguredSessions`,
  `TestAuthSessionLookupUnknownHostnameListsProjectSessions` (`pkg/olium/vigtool`).

## 5. Findings artifacts are derived from the DB, after judging

- **Symptom.** `artifact missing: findings.json` / `dismissed.json` (and a
  degraded run) even though the findings are in the database.
- **Root cause.** The two files were a model obligation: `writeAutopilotArtifacts`
  writes brief/context/plan/manifest; `verifyAutopilotArtifacts` only reads the
  other two.
- **Fix.** `pkg/agent/autopilot_context.go`: `synthesizeAutopilotFindingArtifacts`,
  `loadAutopilotArtifactFindings` (selects the `request`/`response` columns the
  shared query omits), `artifactAbsent`, `AutopilotArtifactSynthesis`, and
  `AutopilotVerificationReport.FindingsSynthesized/DismissedSynthesized`.
  Scope = project + run ∪ children (+ scan uuid); `confirmed`/`triaged` →
  findings.json, `false_positive`/`accepted_risk` → dismissed.json, `draft` never.
  Written only when absent, so operator-authored content wins. Called in
  `RunAutonomous` **after** verify-before-promote and triage, so the artifact
  reflects the judged state. `hasEvidence` also accepts `source_file` — a
  whitebox finding's evidence is its code location.
- **Tests.** `pkg/agent/autopilot_artifact_synthesis_test.go`,
  `TestHasEvidenceRecognisesWhiteboxProvenance` (`pkg/agent/autopilot_evidence_test.go`).

## 6. Section and stall budgets are configurable

- **Symptom.** Runs are stopped after N unproductive turns with no way to adjust,
  on codebases where verification happens through `bash`.
- **Root cause.** `DefaultMaxTurnsPerSection = 40`, `DefaultStallTurns = 12`,
  `DefaultMaxStallRotations = 3` were Go consts, passed straight into the
  controller.
- **Fix.** `pkg/olium/autopilot`: `Options.MaxTurnsPerSection`, `StallTurns`,
  `MaxStallRotations` (zero → package default), honoured when constructing
  `NewSectionController`. `internal/config/agent.go`: `agent.olium.section_max_turns`,
  `.stall_turns`, `.stall_rotations` with defaults 40/12/3. Wired through
  `pkg/agent/autopilot_pipeline.go` (`AutopilotPipelineConfig`), the CLI
  (`pkg/cli/agent_autopilot_olium.go`) and the server
  (`pkg/server/handlers_agent_autopilot.go`).
- **Tests.** `TestDefaultAgentConfig_Olium` (`internal/config`) asserts the keys
  surface in `config ls olium`; `TestRunHonoursConfiguredStallLimits` proves the
  values reach the controller.

## 7. `--project-name` fails fast

- **Symptom.** A name that matches no project is silently ignored: findings land
  in the Default Project, triage fails with "project and agentic-scan UUID are
  required", and nothing in the log explains it.
- **Root cause.** `projectUUID, _ := resolveProjectUUID()` at four sites
  (`pkg/cli/agent_autopilot_olium.go` `runAutopilotOlium`; `pkg/cli/agent.go`
  agent-query; `pkg/cli/agent_autopilot.go` burp-bridge and gs:// blocks)
  discarded the resolution error.
- **Fix.** Propagate it: `cannot scope this run to a project: no project found
  with name "…"` (the other three sites carry context-specific messages).
- **Anchor for regressions.** `grep -rn 'resolveProjectUUID()' pkg/cli` — any
  `projectUUID, _ :=` is the bug back.
- **Test.** `TestAutopilotUnknownProjectNameFailsFast` (`pkg/cli`), which runs the
  real CLI through the package's child-process harness.

## Reapplying on a new release

```bash
cd ~/Projects/vigolium-vX.Y.Z
git cherry-pick ce8fcf20            # or: git rebase --onto vX.Y.Z 82a92a92 <branch>
```

Conflict hotspots (rebasing: expect these, they are the ones that touch hot code):

| File | Why it conflicts |
| --- | --- |
| `pkg/olium/autopilot/autopilot.go` | outer loop, `Options`, halt wiring |
| `pkg/olium/autopilot/section.go` | controller knobs and rotation decision |
| `pkg/agent/autopilot_pipeline.go` | pipeline tail ordering (artifacts after triage) |
| `pkg/agent/triage.go`, `pkg/database/repository_findings.go` | write-back scope |
| `internal/config/agent.go` | new `OliumConfig` fields and defaults |

After the rebase:

```bash
make test-unit                       # repo suite
go test ./pkg/olium/autopilot/ ./pkg/agent/ ./pkg/database/ ./internal/config/ ./pkg/cli/ -count=1
./vigolium-tuning.sh                 # config-side settings (run from the scan dir)
make build                           # never build to an ad-hoc path: version injection
vigolium config ls olium | grep stall_turns   # patch 6 present?
```

If a release fixes one item differently, delete that section here and its test
rather than keeping a second implementation of the same behaviour.
