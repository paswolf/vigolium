# Local agent-pipeline patches (applied on top of v0.5.3)

This file documents the local patch stack applied to this checkout for the
autopilot/scan pipeline. It is **not** upstream work: it exists so the fixes
survive a rebase onto a newer release, and so it is obvious when a release has
made one redundant.

- Baseline: `82a92a92` (`v0.5.3` plus the earlier `agent.olium.max_turns` fix)
- Patch commits: `ce8fcf20` — *"Fixed some weird behaviour causing the scan to degrade"*
  (code fixes) → `77725f2a` — *"test(agent): lock the pipeline fixes and document
  reapplying them"* (tests + this guide). Patch 8 below is queued on top of `77725f2a`.
- Diff: `git show ce8fcf20` / `git diff 82a92a92..ce8fcf20 --stat`
- Config side (separate, not in git): `vigolium-tuning.sh` in the scan working
  directory applies `agent.olium.*` settings via `vigolium config set`. If a
  release does not know a key, `config set` fails — that is the signal that the
  matching code patch is missing too.

Everything below is written in terms of **symbols and strings**, not line
numbers, because line numbers do not survive a rebase.

## Config side (not in git)

The code patches change behaviour; these settings are what the runs actually need.
On a fresh instance they are `vigolium config set <key> <value>` (the same values
the `vigolium-tuning.sh` script in the scan working directory applies, plus the
keys patch 6 added):

```bash
# engine budgets (the last three are only settable because of patch 6)
vigolium config set agent.olium.max_turns 400
vigolium config set agent.olium.call_timeout_sec 3600
vigolium config set agent.olium.section_max_turns 40
vigolium config set agent.olium.stall_turns 20
vigolium config set agent.olium.stall_rotations 5
# operator mode: shadow lands findings directly, so a stall-stop is not fatal
vigolium config set agent.olium.autopilot_mode shadow
# DB context injected into every prompt (defaults are 50/100/20/50)
vigolium config set agent.context_limits.max_findings 200
vigolium config set agent.context_limits.max_endpoints 400
vigolium config set agent.context_limits.max_high_risk 60
vigolium config set agent.context_limits.min_risk_score 30
# keep 4xx bookkeeping records; keep the jstangle admission ceiling valid
vigolium config set discovery.jstangle.persist_not_found_emit_statuses 400,405,415,422
vigolium config set discovery.jstangle.hard_input_mb 16
# template coverage in every profile (source-based runs skip the pre-scan, and the
# agent only reaches the template phase if it asks for it)
vigolium config set scanning_strategy.lite.known_issue_scan true
vigolium config set scanning_strategy.balanced.known_issue_scan true
vigolium config set scanning_strategy.deep.known_issue_scan true
```

Two notes from this instance: `agent.olium.max_concurrent` is 4 here (the tuning
script suggests 7 — raise it only if the endpoint tolerates the extra in-flight
calls), and `scanning_pace.max_duration: 1h` bounds native phases, with the
known-issue phase taking half of it (`duration_factor=0.5` → 30m).

## What the patches encode (preserve these invariants when rebasing)

1. **Progress is target-facing.** The stall detector counts only tools that
   touch the target or record a result (`productiveTools`), plus a `bash` turn
   that speaks HTTP to an in-scope host (patch 9). Reading source, grepping for a
   URL, or querying a database through `bash` is real work but counts as *zero*
   progress — so a policy that stops a run on "no progress" must not also throw
   the run's evidence away.
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
check "8 artifact shapes accepted"         "Accept both documented shapes"         pkg/agent/autopilot_context.go
check "9 target-facing bash counts"        "productiveCall"                       pkg/olium/autopilot
check "10 warning vs error label"          "runMessageLabel"                      pkg/cli
check "10 linked-record evidence"          "http_record_uuids"                    pkg/agent/autopilot_context.go
```

## Regression suite

```bash
# The whole patch stack's tests, one command:
go test ./pkg/olium/autopilot/ ./pkg/agent/ ./pkg/database/ ./internal/config/ ./pkg/cli/ \
  -run 'TestRunGrantsReportOnlyTurn|TestRunNoFinalReportTurnOptOut|TestBuildFinalReportPromptModeAware|TestRunHonoursConfiguredStallLimits|TestWriteBackTriageVerdicts|TestUpdateFindingStatusByHashForRuns|TestHasEvidenceRecognisesWhiteboxProvenance|TestDefaultAgentConfig_Olium|TestAutopilotUnknownProjectNameFailsFast|TestCountEvidenceBackedFindingsAcceptsPlainStringEntries|TestProductiveCall|TestRunCountsTargetFacingBashAsProgress|TestRunStillStallsOnNonTargetBash|TestRunMessageLabel' \
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

## 8. The artifact check accepts the operator's shapes

- **Symptom.** A run that produced both artifacts still ends degraded with
  `failed to parse dismissed.json as findings artifact`.
- **Root cause.** `countEvidenceBackedFindings` accepted only an array of
  *objects* or `{"findings": […]}`. The operator writes `dismissed.json` as a
  flat array of ruled-out reasons — plain strings — so a complete artifact was
  rejected outright (observed live: 9 dismissals reported as a parse failure).
- **Fix.** Parse into `[]any` (flat array or `findings` wrapper) and treat
  non-object entries as notes: they count where no evidence check applies (the
  dismissed artifact) and stay flagged as evidence-free in `findings.json`.
- **Tests.** `TestCountEvidenceBackedFindingsAcceptsPlainStringEntries`
  (`pkg/agent/autopilot_evidence_test.go`), written from the live failure
  (`count = 0`, warning `failed to parse dismissed.json …`).

## 9. Target-facing `bash` counts as progress

- **Symptom.** An operator verifies through the shell — `curl` with a harvested
  session cookie, replaying a captured request, probing a route by hand — and is
  stall-halted anyway ("no productive tool call across 3 consecutive sections"),
  losing the findings it was about to record. Observed live: two rotations at 12
  turns each, ~75 `bash` calls of which many were `curl` against the target, halt
  after ~36 turns.
- **Root cause.** `productiveTools` is a static tool-name set, and `bash` is in
  it only when the *command* is target-facing — information the set cannot see.
- **Fix.**
  - `pkg/olium/autopilot/rotation.go`: `productiveCall(name, args, hosts)`
    extends the set with a `bash` call whose command names both an HTTP client
    (`httpBashClients`) and an in-scope host; `bashTargetsHost`,
    `containsWord` (so `curl` matches `curl -s` but not `curl_to_json.py`),
    `progressTargetHosts` + `normalizeHostString` (target + `--scope`, loopback
    aliases included, ports/paths/userinfo/wildcards stripped).
  - `pkg/olium/autopilot/autopilot.go`: the loop stashes `ToolArgs` at
    `engine.EventToolExecStart` and decides with `productiveCall` at
    `EventToolExecEnd` — **`EventToolExecEnd` carries no args**, which is the
    detail that makes this patch more than a one-liner.
- **Deliberately still unproductive:** source reads, `grep` for a target URL,
  `docker exec … mysql` against the target's database, clients named only inside
  filenames. Widen `agent.olium.stall_turns` / `stall_rotations` for those.
- **Tests.** `TestProductiveCall`, `TestProgressTargetHosts`,
  `TestRunCountsTargetFacingBashAsProgress` (hermetic: a provider that only
  shells out to `curl --version`/a refused loopback port) and its negative twin
  `TestRunStillStallsOnNonTargetBash` (`pkg/olium/autopilot/progress_test.go`).

## 10. A finished run does not read as broken

- **Symptom.** `vigolium agent session <uuid>` prints `✖ Error` for a run whose
  status is `completed_with_warnings`, so a successful run (7h45m, 13 findings)
  looks failed. Two separate causes produced this impression.
- **Root cause / fix A — the heading.** A warned run keeps its warning list in
  the same field a failed run keeps its error (`agentic_scans.error_message`), and
  the detail view hard-coded the `Error` heading. `runMessageLabel` in
  `pkg/cli/agent_session.go` now picks `Warnings` (orange, `WarningSymbol`) for
  `completed_with_warnings` and `Error` otherwise — for the parent run and for
  child runs in the same view.
- **Root cause / fix B — the evidence heuristic.** `countEvidenceBackedFindings`
  only accepted an inlined `request`/`response`/`evidence`/`source_file`, so a
  finding that cites linked HTTP records (`http_record_uuids`) counted as
  evidence-free: the run reported "findings.json contains 19 entries without
  evidence references" when 7 of those 19 had linked records. `hasEvidence` now
  also accepts non-empty `http_record_uuids` / `evidence_record_uuids`
  (`pkg/agent/autopilot_context.go`), including `[]string`, and still rejects an
  empty list.
- **Tests.** `TestRunMessageLabel` (`pkg/cli/agent_session_label_test.go`) and the
  reference-list cases in `TestHasEvidenceRecognisesWhiteboxProvenance`
  (`pkg/agent/autopilot_evidence_test.go`). Against the real artifact of the run
  that surfaced this, the same counter went from `count=6 / 19 unsupported` to
  `count=13 / 12 unsupported`, and the surviving warning is the accurate one.
- **Prompt side (not code).** Only 1 of that run's 13 operator findings stored a
  request/response body and 6 cite a source file, so the report has 12 findings
  with no reference at all: `report_finding` should carry the request/response the
  operator observed, or the record it replayed (see the operator brief's
  Deliverable section).

## Reapplying on a new release

```bash
cd ~/Projects/vigolium-vX.Y.Z
git cherry-pick ce8fcf20 77725f2a   # plus any later patch commit; or:
                                    # git rebase --onto vX.Y.Z 82a92a92 <branch>
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
