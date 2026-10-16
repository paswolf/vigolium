#!/usr/bin/env bash
# apply-settings.sh — put this machine's vigolium config into the state
# scripts/vigolium-settings.env describes, or show how it differs from vanilla.
#
#   ./apply-settings.sh              apply the settings file  (~1 min: one
#                                    `vigolium config set` per key)
#   ./apply-settings.sh --check      paired diff: vanilla -> current
#   ./apply-settings.sh --verify     do the current settings match the file?
#   ./apply-settings.sh --dry-run    print the commands, change nothing
#   ./apply-settings.sh --file F     use another settings file
#   ./apply-settings.sh --help
#
# Exit codes: 0 ok, 1 failure or drift, 3 = --check found differences.
#
# --check builds a throwaway config in a temporary HOME, so it never touches
# your real config or database.
#
# Secrets are not in the settings file. Set them once per machine:
#   vigolium config set agent.olium.custom_provider.api_key '<key>'
set -uo pipefail

here=$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)
file="$here/vigolium-settings.env"
mode=apply

usage() {
    sed -n '2,19p' "$0" | sed 's/^# \{0,1\}//'
    echo
    echo "settings file: $file  ($(pairs 2>/dev/null | wc -l) keys)"
}

# vigolium's first-run update check stalls on a fresh HOME (no
# ~/.vigolium/update-check.json yet), so bound every call.
vg() {
    if command -v timeout >/dev/null 2>&1; then timeout 60 vigolium "$@"; else vigolium "$@"; fi
}

# Settings file -> "key=value" lines: comments and blanks dropped, surrounding
# whitespace trimmed, surrounding double quotes removed (the file quotes values
# so that [lists] survive; `config set` must not receive the quotes).
pairs() {
    grep -E '^[A-Za-z][A-Za-z0-9_.-]*=' "$file" | awk -F= '{
        v = substr($0, index($0, "=") + 1)
        sub(/[[:space:]]+#.*$/, "", v)
        gsub(/^[[:space:]]+|[[:space:]]+$/, "", v)
        if (v ~ /^".*"$/) v = substr(v, 2, length(v) - 2)
        print $1 "=" v
    }'
}

# Effective settings of the config in $HOME, as "key=value" lines.
effective() {
    vg config ls -j 2>/dev/null |
        jq -r '.. | objects | select(has("key") and has("value")) | "\(.key)=\(.value)"' | sort
}

# Same, for a fresh install in a temporary HOME (never touches the real one).
vanilla() {
    local tmp; tmp=$(mktemp -d)
    mkdir -p "$tmp/.vigolium"
    # subshell: the HOME override must not leak back into the caller
    ( HOME="$tmp" vg config ls -j 2>/dev/null ) |
        jq -r '.. | objects | select(has("key") and has("value")) | "\(.key)=\(.value)"' | sort
    rm -rf "$tmp"
}

# paired_diff <vanilla-file> <current-file>  -> "key  vanilla -> current"
paired_diff() {
    awk -F= '
        NR == FNR { van[$1] = substr($0, index($0, "=") + 1); next }
        { cur[$1] = substr($0, index($0, "=") + 1) }
        END {
            for (k in van) if (!(k in cur)) printf "%-46s %-26s -> (missing)\n", k, van[k]
            for (k in cur) {
                c = (k in van) ? van[k] : "(unset)"
                if (cur[k] != c) printf "%-46s %-26s -> %s\n", k, c, cur[k]
            }
        }' "$1" "$2" | sort
}

# verify: every key in the settings file must equal the effective value.
verify() {
    command -v jq >/dev/null 2>&1 || { echo "verify needs jq" >&2; return 1; }
    local want have drift=0 total=0
    want=$(pairs); have=$(effective)
    while IFS= read -r line; do
        [ -n "$line" ] || continue
        total=$((total + 1))
        key=${line%%=*}; val=${line#*=}
        got=$(printf '%s\n' "$have" | grep -F "$key=" | head -1 | cut -d= -f2-)
        if [ "$got" != "$val" ]; then
            printf '  drift %-46s wanted %-22s have %s\n' "$key" "$val" "${got:-(missing)}"
            drift=$((drift + 1))
        fi
    done <<< "$want"
    if [ "$drift" -eq 0 ]; then
        echo "verified: all $total key(s) match the settings file"
        return 0
    fi
    echo "WARNING: $drift of $total key(s) did not stick"
    return 1
}

while [ $# -gt 0 ]; do
    case "$1" in
        --check)      mode=check ;;
        --verify)     mode=verify ;;
        --dry-run)    mode=dry ;;
        --file)       shift; file=${1:-} ;;
        --file=*)     file=${1#--file=} ;;
        -h|--help)    usage; exit 0 ;;
        *)            echo "apply-settings: unknown argument '$1' (--help)" >&2; exit 1 ;;
    esac
    shift
done

command -v vigolium >/dev/null 2>&1 || { echo "apply-settings: vigolium not on PATH" >&2; exit 1; }
[ -r "$file" ] || { echo "apply-settings: cannot read $file" >&2; exit 1; }

case "$mode" in
check)
    command -v jq >/dev/null 2>&1 || { echo "apply-settings: --check needs jq" >&2; exit 1; }
    cur=$(mktemp); van=$(mktemp)
    effective > "$cur"; vanilla > "$van"
    echo "vanilla: $(wc -l < "$van") keys   current: $(wc -l < "$cur") keys"
    out=$(paired_diff "$van" "$cur")
    n=$(printf '%s\n' "$out" | grep -c . || true)
    echo "differences: $n"
    echo
    printf '%s\n' "$out"
    rm -f "$cur" "$van"
    [ "$n" -eq 0 ] || exit 3
    ;;

verify)
    verify || exit 1
    ;;

dry)
    echo "# would apply $(pairs | wc -l) setting(s) to ${HOME}/.vigolium/vigolium-configs.yaml"
    pairs | while IFS= read -r line; do
        printf 'vigolium config set %s %s\n' "${line%%=*}" "${line#*=}"
    done
    ;;

apply)
    ok=0; bad=0
    while IFS= read -r line; do
        [ -n "$line" ] || continue
        key=${line%%=*}; val=${line#*=}
        if out=$(vg config set "$key" "$val" 2>&1); then
            ok=$((ok + 1)); printf '  ok   %-46s %s\n' "$key" "$val"
        else
            bad=$((bad + 1)); printf '  FAIL %-46s %s\n' "$key" "$val"
            printf '       %s\n' "$(printf '%s\n' "$out" | tail -1)"
        fi
    done < <(pairs)
    echo
    echo "applied $ok setting(s), $bad failed"
    echo
    verify || true
    echo
    echo "secrets are not in the settings file; set them once per machine:"
    echo "  vigolium config set agent.olium.custom_provider.api_key '<key>'"
    [ "$bad" -eq 0 ] || exit 1
    ;;
esac
