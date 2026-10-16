# scripts/

The vigolium-side tooling this workflow relies on. Two scripts, one settings
file, no upstream changes: everything here lives on the `local/stable` branch.

## apply-settings.sh — reproduce the scan configuration

A fresh vigolium install has defaults that are wrong for deep agentic scans:
32 turns per run, a 10-minute call timeout, a 25-worker pace, and a stall
detector that reads bash-based verification as "no progress". Those tweaked
values are what this repo's scans run on, so they are kept in a file instead of
in someone's shell history.

```bash
cd scripts
./apply-settings.sh --check     # paired diff: vanilla install -> this machine
./apply-settings.sh --dry-run   # the exact commands, changing nothing
./apply-settings.sh             # apply (~1 min: one `config set` per key)
./apply-settings.sh --verify    # do the current settings match the file?
```

`--check` materialises a pristine config in a temporary `HOME` and prints a
per-key diff, so it is safe to run against a live install. It exits `3` when the
two differ (they always will — that is the point of the file), `0` when they do
not. Apply takes a minute because each key is a separate `vigolium config set`;
`--verify` then re-reads the effective config and reports any key that did not
stick.

Secrets are deliberately absent from the settings file — `config set` stores
them in cleartext in `~/.vigolium/vigolium-configs.yaml`. Set them once per
machine:

```bash
vigolium config set agent.olium.custom_provider.api_key '<key>'
vigolium config set oast.token '<token>'
```

## vigolium-settings.env — the values, one per line

`KEY="value"` per line, `#` for comments. Every entry carries its vanilla
default and what the knob actually does, because vigolium's own docs mostly do
not. Sections: agent loop budget, provider, agent context limits, discovery,
spidering/pacing, scope and phases, and a "repair" block for keys an older
`config set` round-trip left null.

Things worth knowing before editing it:

- Unknown keys fail loudly (`key "…" not found: unknown config key`), so a typo
  in a key name cannot silently do nothing.
- Values are **not** validated consistently. `scope.cli_origin_mode` rejects
  values outside `all|relaxed|balanced|strict`, but `agent.olium.autopilot_mode`
  and `discovery.wordlists.tier` accept garbage and fall back to a default
  without complaint. Check those two by hand.
- `agent.olium.max_tokens` is honored by the Anthropic transports only; an
  OpenAI-compatible provider ignores it.

## check-findings.fish — independent re-check of existing findings

Re-judges findings that are already in the project with a *different* model,
without re-running the scan. Each finding is triaged alone (`vigolium agent
triage <id>`) from captured traffic, so a second opinion costs one agent run per
finding instead of a full scan.

```bash
./check-findings.fish                       # dry-run: render one triage prompt
./check-findings.fish --run --models accounts/fireworks/models/glm-5p3,accounts/fireworks/models/ember-1
./check-findings.fish --run --ids 9904,9918 --min-severity medium --models <id>
./check-findings.fish --apply-consensus     # apply what every model agreed on
```

It refuses to check findings with the model that wrote them, skips checks that
already have a verdict (so re-runs are free), and ends with a verdict-per-model
table. `--apply-consensus` then batches the agreements into `vigolium finding
mark` calls: unanimous confirmations, unanimous false positives, and a list of
disagreements for a human.

Discipline it enforces by construction: judging happens from captured traffic
(`--no-probe` default, `--no-write` default), so a check pass changes nothing
unless you pass `--probe` / `--write` / `--apply-consensus`.

Raw output lands in `finding-checks/<model>/<id>.json` (verdict + reasoning) and
`finding-checks/candidates.tsv`. Cost: roughly \$0.40 per check with a
cache-heavy model on a dense finding set; `--help` shows how to sum the real
token usage out of a run's transcript.

A note from practice: model agreement is not correctness. In one pass GLM called
a finding a false positive on solid source evidence while Ember confirmed it
(and vice versa on another finding), so the *disagreements* are the valuable
output — read them before trusting a clean table.
