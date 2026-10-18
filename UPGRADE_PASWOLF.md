# Upgrading this fork to a new upstream release

`local/stable` carries the patch stack; upstream's files are never edited in
ways that turn into merge conflicts — in particular `CHANGELOG.md`, which is
kept byte-identical to the base release (see "Changelogs").

Companion files: `CHANGELOG_PASWOLF.md` (what this fork changes, per tag) and
`docs/development/local-agent-pipeline-patches.md` (why each patch exists and
which invariants a rebase must preserve). Everything below is git and `make`; no
model calls, no API cost. `<old>`/`<new>` are release tags (`v0.5.3`, `v0.5.4`).

## What lives where

| ref | what it is |
| --- | --- |
| `upstream/main` | upstream's branch; `just sync` refreshes it. Rebases and PR branches use a *release tag* reachable from it, never a local `main`. |
| `main` | the fork's mirror. Keep it pristine (`just reset-main`); it is only a reference. |
| `local/stable` | **the stack**: every local patch, in order, on the newest release tag. The only branch to build from, and the fork's default branch. |
| PR branches | throwaway branches cut from `upstream/main` with one commit cherry-picked off the stack — one per upstream PR. |

Upstream PRs are independent, never stacked: a maintainer can take or reject
them one at a time, and a merged patch simply drops out of the stack at the next
rebase. Local-only commits (justfile, scripts, this doc, the changelog) are
never cherry-picked into a PR.

## The justfile

`just` with no arguments lists these. The model: `just upgrade` for a new
release, `just pr-branch` for upstream work; everything else is a step of those.

| recipe | what it does |
| --- | --- |
| `status` | branch, newest upstream tag, stack size, tag list, dirty flag |
| `sync` | `bootstrap` (adds the `upstream` remote and the `merge=ours` driver) + fetch upstream with tags |
| `rebase` | rebase the stack onto the newest upstream release tag (refuses a dirty tree) |
| `test` / `test-full` | `make test-unit` / `make test` |
| `tag` | `v<release>-paswolf.<n>` on the stack tip |
| `export-series` | regenerate the `format-patch` series into `../vigolium-patches` |
| `verify` | apply the series to the release tag and assert the result equals the stack |
| `upgrade` | `sync → rebase → test → tag → export-series → verify`, fail-fast |
| `push` | force-with-lease `local/stable` + tags to the fork (a rebase rewrites history) |
| `publish` | commit + push the regenerated series repo |
| `pr-branch <name> <sha>` / `pr-push <name>` | cut a PR branch off `upstream/main`; push it and print the upstream compare URL |
| `reset-main` | reset `main` to `upstream/main` |

## Changelogs

- **Upstream's `CHANGELOG.md` is never edited here.** `.gitattributes` contains
  `CHANGELOG.md merge=ours`, and `just bootstrap` installs the driver
  (`merge.ours.driver=true`). During a rebase "ours" is the new base, so the
  file silently takes upstream's version instead of becoming a conflict.
- **`CHANGELOG_PASWOLF.md` is ours.** One section per tag, written when you cut
  it, describing what the fork changes relative to that upstream release.

## Applying a new upstream release

### 0. Pre-flight

```bash
cd ~/Projects/vigolium
git co local/stable
git status -sb                 # clean tree — `just rebase` refuses otherwise
git branch backup/pre-<new>    # free rollback ref
just verify                    # the series currently matches the stack
cp ~/.vigolium/database-vgnm.sqlite ~/.vigolium/database-vgnm.sqlite.bak
```

The database copy is not paranoia: a new binary may migrate the SQLite schema on
first run, and that is not reversible.

### 1. Fetch and size up the change

```bash
just sync                      # bootstrap + fetch upstream --prune --tags
just status                    # "upstream tip … (<new>)"
git log --oneline <old>..<new>
git diff --stat <old>..<new> -- pkg/agent pkg/olium internal/config pkg/cli
```

The `--stat` shows the files the patches touch — where the rebase will fight —
and whether upstream fixed something the stack carries. A patch that is now
upstream disappears during the rebase (patch-id matching); that is the system
working. If it happens, delete its section from
`docs/development/local-agent-pipeline-patches.md` and note it in
`CHANGELOG_PASWOLF.md`.

### 2. Rebase

```bash
just rebase                    # onto `git describe --tags --abbrev=0 upstream/main`
```

The base is the **release tag**, not a moving `main`, so it is deterministic.
Conflicts are the normal mechanism: resolve, `git add`, `git rebase --continue`;
`git rebase --abort` returns to the pre-rebase state.

Conflict hotspots (the per-patch sections carry the invariants to preserve):

| File | Why it conflicts |
| --- | --- |
| `pkg/olium/autopilot/autopilot.go` | outer loop, `Options`, halt wiring |
| `pkg/olium/autopilot/section.go` | controller knobs and rotation decision |
| `pkg/agent/autopilot_pipeline.go` | pipeline tail ordering (artifacts after triage) |
| `pkg/agent/triage.go`, `pkg/database/repository_findings.go` | write-back scope |
| `internal/config/agent.go` | new `OliumConfig` fields and defaults |

Then confirm nothing vanished by accident:

```bash
git log --oneline <new>..local/stable
just status                    # "= N commit(s) on top of <new>"
```

### 3. Test, tag, build

```bash
just test                      # make test-unit
just test-full                 # once per base bump
make lint
just tag                       # v<new>-paswolf.1 — tag before building
make build                     # never a bare `go build`: version injection
```

A failing test here is information, not noise: if it is in a package the stack
touches, a patch and the release disagree. Fix it before the version gets
history.

Smoke the binary, including the config surface:

```bash
vigolium version
scripts/apply-settings.sh --verify
```

`--verify` is the cheap regression test for config-schema drift: a renamed or
removed key fails loudly, and `config set` refuses unknown keys. Fix
`scripts/vigolium-settings.env` then, while the release is fresh.

### 4. Export, prove, publish

```bash
just export-series
just verify                    # am clean AND tree == stack; do not publish otherwise
just push
just publish                   # series repo: ../vigolium-patches
```

### Shortcut

```bash
just upgrade
```

is `sync → rebase → test → tag → export-series → verify` with fail-fast
semantics. Keep step 0 around it, and run `just push` / `just publish` after.

## Gotchas, learned the hard way

- **`just` may not exist mid-rebase.** The justfile arrives with a commit near
  the end of the stack, so during early conflicts the worktree has no recipe to
  run. Resolve with plain git until the rebase finishes.
- **Publishing after a rebase needs a force-push.** The rewritten commits
  diverge from the fork's branch; `just push` uses `--force-with-lease`, which
  refuses if someone else moved the branch in the meantime. Never `--force`
  bare.
- **GitHub Actions is disabled on the fork** (Settings → Actions → General →
  "Disable actions"), and no workflow file is patched: the CI-dispatch commit
  that used to gate them is not carried. Workflows belong to upstream — do not
  re-enable Actions and do not edit `.github/workflows/*`. There are no repo
  secrets, and none should be added.
- **Never use GitHub's "Sync fork" button.** The fork's default branch is the
  patched one; its discard option force-resets it to upstream.
- **Never merge origin into `local/stable`.** That fuses the old stack into the
  rebased one and duplicates every patch. Sync by fetching; publish by pushing.
- **Don't edit `CHANGELOG.md`.** Put the entry in `CHANGELOG_PASWOLF.md`.

## Rollback

`backup/pre-<new>` is the pre-upgrade state; `git co backup/pre-<new>` gets you
there. To republish it, move `local/stable` back and force-with-lease push — or
simply stay on the backup branch until the next attempt.
