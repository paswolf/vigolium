# paswolf changelog

Changes this fork carries on top of upstream. Upstream's `CHANGELOG.md` is kept
byte-identical to the release named in each section below and is **never** edited
here: `.gitattributes` marks it `merge=ours`, so a rebase keeps upstream's copy
instead of asking you to merge it (the driver is installed by `just bootstrap`).

Sections are named after the tags `just tag` produces (`v<upstream>-paswolf.<n>`).
Add a section when a tag is cut; keep entries in upstream's style (Added / Fixed
/ Changed), one bullet per behaviour a user can observe.

## v0.5.4-paswolf.1 — base `v0.5.4`

### Added

- `agent.olium.section_max_turns` / `agent.olium.stall_turns` / `agent.olium.stall_rotations` tune the durable-autopilot section budgets (defaults 40 / 12 / 3) instead of pinning them to package constants.
- Autopilot grants a *stopped* run one report-only turn. When the stall guard stops a run, or the engine gives up on its own, and nothing was reported, the loop re-enters once with a reset context, a mission that points at the run transcript and requires the finding tool for everything verified, plus the `findings.json` / `dismissed.json` artifacts. The turn is exempt from the stall guard (recovering evidence by reading looks unproductive to it) and ends the run when it reaches its section limit. Opt out with `Options.NoFinalReportTurn`.

### Fixed

- The `--burp-bridge-url` project-resolution error is wrapped as a usage error, matching the `usage_error_lint_test.go` rule 0.5.4 added; without it `make test-unit` failed on the rebased stack.
- `agent.olium.max_turns` now applies to non-autopilot engine runs too (agent query and triage), not only the autopilot loop.
- Autopilot triage wrote its verdicts scoped to the run's own UUID, so findings imported by a child audit run stayed `draft` — unreachable by `vigolium finding --verdict confirmed,triaged` and absent from reports. The same scope also never matched rows stored without any run UUID (pre-scan and passive findings), so their verdicts were dropped silently while the rows kept the scanner's status. The write-back now spans the run, its child runs, and unattributed rows — rows owned by a *different* run are still left alone.
- `findings.json` / `dismissed.json` were a model obligation: when the operator did not write them the run warned "artifact missing" even though the findings were in the database. They are now synthesized from the DB once verify-before-promote and triage have finished, so the artifact reflects the run's judged findings; operator-authored files still win.
- An invalid discovered auth session produced only "some discovered auth sessions were invalid and could not be repaired" — no names, no reasons — which sent the operator hunting for sessions that do not exist. The warning now names the dropped entries, their validation errors and the usable sessions; the repair failure is logged with its cause; and `auth_session_lookup` on an unknown name lists the configured sessions.
- `--project-name` that names no project was silently ignored on the autopilot and agent-query paths: the run wrote its findings into the Default Project and the post-run triage failed for want of a project UUID, with nothing in the log explaining why. Resolution errors are now propagated (`cannot scope this run to a project: ...`) instead of being discarded.
- A run that finished with warnings printed its warning list under an `✖ Error` heading in `vigolium agent session`, so a successful 7h45m run with 13 findings read as broken. The heading now follows the status (`Warnings` vs `Error`).
- The autopilot artifact check counted a finding as evidence-free unless it inlined a request/response body, so findings that cite linked HTTP records — the common shape — were reported as unsupported. Non-empty `http_record_uuids` / `evidence_record_uuids` now count as evidence references.
- A `bash` turn that reaches the target with an HTTP client (`curl`, `wget`, `httpie`, `httpx`, `hurl`, `ncat`, `netcat`) and names the run's target or a scope host now counts as progress for the autopilot stall detector, so shell-driven probing — replaying a request, reusing a harvested session cookie — no longer reads as a stall and no longer stops the run. Source reads, URL greps and `docker exec` database queries still count as no progress.
- The autopilot artifact check rejected `dismissed.json` written as a flat array of strings — the shape the operator actually produces — and reported a complete run as degraded with `failed to parse dismissed.json as findings artifact`. Artifact entries may now be any JSON value: string entries count where no evidence check applies (the dismissed artifact) and are still flagged as evidence-free in `findings.json`.

### Local workflow (not upstream-bound)

- `justfile` — upstream sync, stack rebase, tagging, patch-series export/verify, PR prep.
- `scripts/apply-settings.sh` + `scripts/vigolium-settings.env` — reproduce and diff the scan configuration; `scripts/check-findings.fish` — independent re-checks of existing findings; see `scripts/README.md`.
- `UPGRADE_PASWOLF.md` — the update procedure. `docs/development/local-agent-pipeline-patches.md` — per-patch rationale and the invariants a rebase must preserve.
