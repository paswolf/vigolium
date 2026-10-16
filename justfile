# Vigolium fork (paswolf) — local workflow. `just` lists the recipes.
#
#   main        upstream mirror + local infra commits
#   <stack>     ONE stacked branch: your patches = everything upstream does not have.
#               It is the only branch you build from, and the only thing that gets
#               rebased onto a new upstream release.
#   patches     generated `git format-patch` series (own repo). Never hand-edit.
#
# Release flow: `just upgrade`, then push when you are happy (the recipe prints how).

set shell := ["bash", "-uc"]

stack    := "local/stable"
patches  := `realpath -m ../vigolium-patches`
upstream := "upstream"
fork     := "origin"

# abort with a clear message when the worktree is dirty
_clean:
    @git diff --quiet && git diff --cached --quiet || { echo "worktree is dirty — commit or stash first"; exit 1; }

# where you are relative to upstream
status:
    #!/usr/bin/env bash
    set -euo pipefail
    base=$(git describe --tags --abbrev=0 "{{upstream}}/main")
    printf 'branch            %s\n' "$(git branch --show-current)"
    printf 'upstream tip      %s  (%s)\n' "$(git log --format=%h -1 {{upstream}}/main)" "$base"
    printf 'stack             %s  = %s commit(s) on top of %s\n' "{{stack}}" "$(git rev-list --count "$base..{{stack}}")" "$base"
    printf 'paswolf tags      %s\n' "$(git tag -l '*-paswolf.*' | tail -3 | tr '\n' ' ')"
    if git diff --quiet && git diff --cached --quiet; then printf 'worktree          clean\n'; else printf 'worktree          DIRTY\n'; fi

# fetch upstream refs and tags
sync:
    git fetch {{upstream}} --prune --tags
    @echo "upstream tip: $(git log --format='%h %s' -1 {{upstream}}/main)"

# make main a pristine copy of upstream again (drops local commits on main)
reset-main:
    git switch main
    git reset --hard {{upstream}}/main
    @echo "main == upstream/main. Publish with: git push --force-with-lease origin main"

# rebase the stack onto the newest upstream release tag
rebase: _clean
    #!/usr/bin/env bash
    set -euo pipefail
    base=$(git describe --tags --abbrev=0 "{{upstream}}/main")
    git switch "{{stack}}"
    git rebase "$base"
    echo "stack rebased onto $base"

# tests
test:
    make test-unit
test-full:
    make test

# tag the stack as v<upstream>-paswolf.<n>
tag:
    #!/usr/bin/env bash
    set -euo pipefail
    base=$(git describe --tags --abbrev=0 "{{upstream}}/main")
    n=$(git tag -l "${base}-paswolf.*" | sed 's/.*paswolf\.//' | sort -n | tail -1)
    t="${base}-paswolf.$(( ${n:-0} + 1 ))"
    git tag -a "$t" -m "paswolf patches on $base" "{{stack}}"
    echo "tagged $t"

# regenerate the patch series from the stack
export-series:
    #!/usr/bin/env bash
    set -euo pipefail
    base=$(git describe --tags --abbrev=0 "{{upstream}}/main")
    mkdir -p "{{patches}}"
    rm -f "{{patches}}"/*.patch
    git format-patch --base="$base" -o "{{patches}}" "$base..{{stack}}" >/dev/null
    ls -1 "{{patches}}"/*.patch

# prove the series applies to the release tag and reconstructs the stack
verify:
    #!/usr/bin/env bash
    set -euo pipefail
    base=$(git describe --tags --abbrev=0 "{{upstream}}/main")
    patches="{{patches}}"
    cleanup() {
        git worktree remove --force /tmp/vg-base 2>/dev/null || true
        git worktree remove --force /tmp/vg-tip 2>/dev/null || true
        git worktree prune
    }
    cleanup
    git worktree add --detach --force /tmp/vg-base "$base" >/dev/null
    if ! ( cd /tmp/vg-base && git am "$patches"/*.patch >/tmp/vg-am.log 2>&1 ); then
        echo "git am FAILED on $base:"
        tail -12 /tmp/vg-am.log
        ( cd /tmp/vg-base && git am --abort ) 2>/dev/null || true
        cleanup
        exit 1
    fi
    echo "git am on $base: clean"
    git worktree add --detach --force /tmp/vg-tip "{{stack}}" >/dev/null
    if diff -r -x .git -q /tmp/vg-base /tmp/vg-tip >/tmp/vg-diff.log 2>&1; then
        echo "series == stack: OK"
    else
        echo "series DIFFERS from stack:"
        head -10 /tmp/vg-diff.log
        cleanup
        exit 1
    fi
    cleanup

# the whole new-release flow
upgrade: sync _clean
    #!/usr/bin/env bash
    set -euo pipefail
    just rebase
    just test
    just tag
    just export-series
    just verify
    echo
    echo "next:"
    echo "  git push origin {{stack}} --tags"
    echo "  just publish"

# push the stack and its tags to your fork
push: _clean
    git push {{fork}} {{stack}} --tags

# commit + push the regenerated series repo
publish:
    #!/usr/bin/env bash
    set -euo pipefail
    t=$(git tag -l '*-paswolf.*' | tail -1)
    cd "{{patches}}"
    if ! git diff --quiet || ! git diff --quiet --staged; then
        git add -A
        git commit -m "series for ${t:-update}"
    fi
    git push

# start a PR-sized branch off upstream with one commit cherry-picked from the stack
pr-branch name commit: _clean
    git switch -c {{name}} {{upstream}}/main
    git cherry-pick {{commit}}
    @echo "review, then: just pr-push {{name}}"

pr-push name:
    git push -u {{fork}} {{name}}
