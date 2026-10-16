#!/usr/bin/env fish
# check-findings.fish — re-check existing findings with other models, one at a
# time, without re-running the scan. Run with --help for the full flag list.

function usage
    set -l b (set_color blue); set -l d (set_color normal); set -l g (set_color brblack)
    echo "$b check-findings.fish $d— independently re-check findings with other models"
    echo
    echo "$b MODES $d"
    echo "   (no --run)              dry-run: render one triage prompt. No model call, no writes."
    echo "   --run                   check for real. Requires --models."
    echo
    echo "$b WHAT IT DOES $d"
    echo "   Lists the project's confirmed/triaged findings at or above --min-severity"
    echo "   and asks a model to judge each alone (vigolium agent triage): confirmed,"
    echo "   false_positive or inconclusive. Ends with a verdict-per-model table, and the"
    echo "   'vigolium finding mark' command for findings every model agreed on."
    echo
    echo "$b OPTIONS $d"
    echo "   --models A,B            model id(s), e.g. accounts/fireworks/models/glm-5p3"
    echo "                           required with --run; refuses the model that wrote the"
    echo "                           findings (deepseek-v4p1-flash)"
    echo "   --ids 9904,9918         check only these finding ids (default: all candidates)"
    echo "   --min-severity LEVEL    high (default) | critical | medium | low | info"
    echo "   --project UUID          default: the active project (~/.vigolium/active-project)"
    echo "   --duration D            10m (default). A bare number means minutes (20 = 20m);"
    echo "                           0 = no wall-clock limit (turn cap still applies)"
    echo "   --outdir DIR            finding-checks (default)"
    echo "   --only-operator         only findings from the operator agent (module_type=ai-agent)"
    echo "   --probe                 let the model re-probe the live target (may change demo"
    echo "                           state). Default is --no-probe: judge from captured traffic"
    echo "   --write                 let verdicts change the finding status. Default is"
    echo "                           --no-write: compare models without any status churn"
    echo "   --apply-consensus       after the table, apply the verdicts every model agreed on"
    echo "                           with one batched 'vigolium finding mark' per verdict;"
    echo "                           disagreements and inconclusives are listed for you"
    echo "   --by NAME               attribution for --apply-consensus (default independent-check)"
    echo "   -h, --help              this screen"
    echo
    echo "$b EXAMPLES $d"
    echo "   ./check-findings.fish"
    echo "   ./check-findings.fish --run --models accounts/fireworks/models/glm-5p3"
    echo "   ./check-findings.fish --run --ids 9904,9918,9905 --duration 0 --models m1,m2"
    echo "   ./check-findings.fish --run --only-operator --models m1 --probe --write"
    echo
    echo "$b OUTPUT $d"
    echo "   <outdir>/<model>/<id>.json   raw verdict document per check (resumable)"
    echo "   <outdir>/candidates.tsv      the list that was checked"
    echo
    echo "$b COST $d"
    echo "   every check gets its own agentic_scans row (mode=triage), but 0.5.3"
    echo "   leaves token_usage empty on those rows — read the run transcript:"
    echo "$g   jq -r '.message.usage? | select(.!=null) | [.input//0,.cacheRead//0,.output//0] | @tsv' \\"
    echo "$g     ~/.vigolium/agent-sessions/<run-uuid>/transcript-agent-triage-confirm.jsonl$d"
    echo "   sum the three columns over all rows; that reconciled with the provider"
    echo "   bill (GLM 5.3: ~\$0.40 per check, ~93% of the input cached)"
end

argparse -n check-findings 'h/help' 'run' 'write' 'probe' 'apply-consensus' \
    'models=' 'ids=' 'min-severity=' 'project=' 'duration=' 'outdir=' 'only-operator' 'by=' \
    -- $argv
or begin
    usage
    exit 2
end
if set -q _flag_h; or set -q _flag_help
    usage
    exit 0
end

set -l outdir $_flag_outdir
if test -z "$outdir"
    set outdir finding-checks
end

# A bare number is minutes: `--duration 20` is what people type, and a Go
# duration without a unit does not parse at all.
set -l duration $_flag_duration
if test -z "$duration"
    set duration 10m
else if string match -qr '^[0-9]+$' -- $duration
    set duration {$duration}m
end

set -l minsev $_flag_min_severity
test -z "$minsev"; and set minsev high

set -l P $_flag_project
if test -z "$P"
    set P (string trim -- (cat ~/.vigolium/active-project 2>/dev/null))
end
if test -z "$P"
    echo "check-findings: no --project given and no active project" >&2
    exit 1
end

set -l probe --no-probe
if set -q _flag_probe
    set probe
end
set -l write --no-write
if set -q _flag_write
    set write
end

# Only a *non-empty* id list filters. An empty-but-set variable must not.
set -l ids
if set -q _flag_ids
    set ids (string split , -- (string join , $_flag_ids))
end

mkdir -p $outdir
set -l manifest $outdir/candidates.tsv
set -l sel '.items[]'
if set -q _flag_only_operator
    set sel '.items[] | select(.module_type == "ai-agent")'
end
vigolium --project-uuid $P finding --min-severity $minsev --verdict confirmed,triaged -j \
    | jq -r "$sel | [.id, .severity, (.short // .module_id), .finding_hash, (.module_type // \"-\")] | @tsv" \
    > $manifest
or begin
    echo "check-findings: could not list findings for project $P" >&2
    exit 1
end

set -l n (count (cat $manifest))
echo "project $P · $n candidate(s) at severity >= $minsev · outdir $outdir"

if not set -q _flag_run; and not set -q _flag_apply_consensus
    set -l first (string split \t -- (head -n1 $manifest))
    echo "dry-run: rendering the triage prompt for #$first[1] ($first[2] $first[3])"
    vigolium --project-uuid $P agent triage $first[1] --dry-run
    echo
    echo "nothing was sent and nothing was written. --run --models <id,...> checks for real; --help lists the flags."
    exit 0
end

if not set -q _flag_models
    echo "check-findings: --run needs --models, e.g. --models accounts/fireworks/models/glm-5p3 (see --help)" >&2
    exit 1
end
set -l models (string split , -- (string join , $_flag_models))
for m in $models
    if string match -q '*deepseek-v4p1-flash*' -- $m
        echo "check-findings: refusing to check findings with the model that wrote them ($m)" >&2
        exit 1
    end
end

for m in $models
    set -l mname (string split , -- (string replace -a '/' ',' -- $m))[-1]
    set -l mdir $outdir/(string replace -a '/' '_' -- $m)
    mkdir -p $mdir
    echo "== $mname"
    set -l tocheck 0
    for line in (cat $manifest)
        set -l f (string split \t -- $line)
        set -l id $f[1]
        if test (count $ids) -gt 0; and not contains -- $id $ids
            continue
        end
        set -l out $mdir/$id.json
        if test -f $out; and jq -e '.items[0].verdict' $out >/dev/null 2>&1
            echo "   skip #$id (already checked)"
            continue
        end
        set tocheck (math $tocheck + 1)
        echo "   → #$id $f[2] $f[3]"
        vigolium --project-uuid $P agent triage $id \
            --provider openai-compatible --model $m \
            --max-duration $duration $probe $write -j > $out
        or echo "     ✖ check failed for #$id (see $out)"
    end
    if test $tocheck -eq 0
        echo "   nothing to check (all cached, or no id matched)"
    end
end

echo
set -l confirmed_hashes
set -l fp_hashes
set -l open_items
printf '%-7s %-9s %-34s' id severity claim
for m in $models
    printf ' %-18s' (string split , -- (string replace -a '/' ',' -- $m))[-1]
end
echo
for line in (cat $manifest)
    set -l f (string split \t -- $line)
    if test (count $ids) -gt 0; and not contains -- $f[1] $ids
        continue
    end
    set -l verdicts
    for m in $models
        set -l mdir $outdir/(string replace -a '/' '_' -- $m)
        set -a verdicts (jq -r 'if .ok == false then "failed" else (.items[0].verdict // "no verdict") end' $mdir/$f[1].json 2>/dev/null; or echo not-run)
    end
    printf '%-7s %-9s %-34s' $f[1] $f[2] (string sub -l 32 -- $f[3])
    for v in $verdicts
        printf ' %-18s' $v
    end
    echo
    set -l uniq (printf '%s\n' $verdicts | sort -u)
    if test (count $uniq) -eq 1
        switch $uniq[1]
            case confirmed
                set -a confirmed_hashes $f[4]
            case false_positive
                set -a fp_hashes $f[4]
            case '*'
                set -a open_items "#$f[1] $f[2] → $uniq[1]"
        end
    else
        set -a open_items "#$f[1] $f[2] → "(string join / $verdicts)
    end
end

set -l by independent-check
if set -q _flag_by
    set by (string join ' ' $_flag_by)
end

echo
if test (count $confirmed_hashes) -eq 0; and test (count $fp_hashes) -eq 0; and test (count $open_items) -eq 0
    echo "no verdicts recorded yet — nothing to apply"
    exit 0
end

set -l applyit 0
if set -q _flag_apply_consensus
    set applyit 1
end

if test (count $confirmed_hashes) -gt 0
    set -l cmd vigolium --project-uuid $P finding mark $confirmed_hashes --as confirmed --by $by --note "independent check: every model agreed on confirmed"
    echo "unanimous confirmed — $(count $confirmed_hashes) finding(s):"
    echo "  "(string join ' ' -- (string escape -- $cmd))
    if test $applyit -eq 1
        $cmd; or echo "  ✖ mark failed"
    end
end
if test (count $fp_hashes) -gt 0
    set -l cmd vigolium --project-uuid $P finding mark $fp_hashes --as false_positive --by $by --note "independent check: every model agreed on false positive"
    echo "unanimous false positive — $(count $fp_hashes) finding(s):"
    echo "  "(string join ' ' -- (string escape -- $cmd))
    if test $applyit -eq 1
        $cmd; or echo "  ✖ mark failed"
    end
end
if test (count $open_items) -gt 0
    echo
    echo "needs a human look — $(count $open_items) finding(s) with no agreement"
    echo "  (re-run those ids with --probe, or check them with a second model):"
    for x in $open_items
        echo "    $x"
    end
end
if test "$apply_consensus" = no
    echo
    echo "add --apply-consensus to run the mark commands above"
end
