# Vigolium fork (paswolf) — local workflow. `just` lists the recipes.
#
#   main        upstream mirror + local infra commits
#   <stack>     ONE stacked branch: your patches = everything upstream does not have.
#               It is the only branch you build from, and the only thing that gets
#               rebased onto a new upstream release.
#   patches     generated `git format-patch` series (own repo). Never hand-edit.
#   release     `just release` = linux/amd64+arm64 tarballs on a GitHub release.
#
# Release flow: `just upgrade`, then push when you are happy (the recipe prints how).

set shell := ["bash", "-uc"]

stack         := "local/stable"
patches       := `realpath -m ../vigolium-patches`
upstream      := "upstream"
fork          := "origin"
fork_repo     := "paswolf/vigolium"
upstream_repo := "vigolium/vigolium"

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

# add the upstream remote on a fresh clone (a fork clone only has origin)
bootstrap:
    @git remote | grep -qx {{upstream}} || git remote add {{upstream}} git@github.com:{{upstream_repo}}.git
    @git config merge.ours.driver true
    @echo "remotes: $(git remote | tr '\n' ' ')"

# fetch upstream refs and tags
sync: bootstrap
    git fetch {{upstream}} --prune --tags
    @echo "upstream tip: $(git log --format='%h %s' -1 {{upstream}}/main)"

# make main a pristine copy of upstream again (drops local commits on main)
reset-main:
    git co main
    git reset --hard {{upstream}}/main
    @echo "main == upstream/main. Publish with: git push --force-with-lease origin main"

# rebase the stack onto the newest upstream release tag
rebase: _clean
    #!/usr/bin/env bash
    set -euo pipefail
    base=$(git describe --tags --abbrev=0 "{{upstream}}/main")
    git co "{{stack}}"
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
    echo "  just release          # linux/amd64+arm64 binaries on a GitHub release"

# push the stack and its tags to your fork (a rebase rewrites history, so lease)
push: _clean
    git push --force-with-lease {{fork}} {{stack}} --tags

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

# ─── release ────────────────────────────────────────────────────────────────
# Downloadable paswolf release: linux/amd64 + linux/arm64 tarballs, gated by
# `make release-verify`, attached to a GitHub release on {{fork_repo}}.
#
#   just release-build        # artifacts only, no network
#   just release              # build, then create the GitHub release
#
# Artifacts mirror `make public-release` under build/dist/paswolf (inside the
# git-ignored build/dist): vigolium_<version>_<os>_<arch>.tar.gz + checksums.txt
# + metadata.json. The build loop is spelled out here instead of calling
# public-release because that target ends with an R2 `mc cp` upload this fork
# does not use.
paswolf_tag     := `git tag -l '*-paswolf.*' --sort=-v:refname | head -1`
# Override with VIGOLIUM_RELEASE_TARGETS as needed (tar.gz platforms: linux,
# darwin; windows would need the .zip branch from `make public-release`).
release_targets := env("VIGOLIUM_RELEASE_TARGETS", "linux/amd64 linux/arm64")
release_dist    := "build/dist/paswolf"

# The embedded helpers are per-target go:embed blobs, so every target's
# vigolium-audit blob is staged before its compile and the host blob is restored
# afterwards — the same reason upstream builds cross targets sequentially.
#
# Build and package the release for TAG (newest *-paswolf.* tag by default).
release-build tag=paswolf_tag:
    #!/usr/bin/env bash
    set -euo pipefail
    tag="{{tag}}"
    if [ -z "$tag" ]; then
        echo "no *-paswolf.* tag found: create one with 'just tag' (or pass a tag)" >&2
        exit 1
    fi
    git rev-parse -q --verify "refs/tags/$tag" >/dev/null || { echo "no such tag: $tag" >&2; exit 1; }
    ver="${tag#v}"
    dist="{{release_dist}}"
    targets="{{release_targets}}"

    echo "==> $tag: staging the embedded audit + jstangle blobs for all targets"
    make ensure-audit-dist ensure-jstangle-dist

    # Reuse the Makefile's own stamp flags so these binaries report the same
    # version/commit/build time as `make build` (version from pkg/cli/version.go).
    stamp=$(make -s --no-print-directory \
        --eval='print-ldflags: ; @echo $(LDFLAGS_INNER)' print-ldflags)

    mkdir -p "$dist"
    rm -f "$dist"/vigolium_*.tar.gz "$dist"/checksums.txt "$dist"/metadata.json
    stage="$dist/.stage"
    rm -rf "$stage"; mkdir -p "$stage"
    trap 'rm -rf "$stage"; make -s --no-print-directory restage-host-audit >/dev/null 2>&1 || true' EXIT

    for target in $targets; do
        goos="${target%/*}"; goarch="${target#*/}"
        echo "==> $goos/$goarch"
        bash build/scripts/stage-audit-blob.sh "$goos" "$goarch"
        GOOS="$goos" GOARCH="$goarch" CGO_ENABLED=0 \
            go build -ldflags "$stamp" -o "$stage/vigolium" ./cmd/vigolium
        cp LICENSE THIRD_PARTY_NOTICES.md "$stage/"
        name="vigolium_${ver}_${goos}_${goarch}.tar.gz"
        COPYFILE_DISABLE=1 tar --no-xattrs -czf "$dist/$name" -C "$stage" \
            vigolium LICENSE THIRD_PARTY_NOTICES.md
        rm -f "$stage/vigolium" "$stage/LICENSE" "$stage/THIRD_PARTY_NOTICES.md"
        printf '    %-52s %s\n' "$name" "$(du -h "$dist/$name" | cut -f1)"
    done

    (cd "$dist" && if command -v shasum >/dev/null 2>&1; then
        shasum -a 256 vigolium_*.tar.gz > checksums.txt
    else
        sha256sum vigolium_*.tar.gz > checksums.txt
    fi)
    printf '{\n  "version": "%s",\n  "commit": "%s",\n  "build_time": "%s"\n}\n' \
        "$tag" "$(git rev-parse --short HEAD)" "$(date -u +%Y-%m-%dT%H:%M:%SZ)" \
        > "$dist/metadata.json"

    # PUBLIC_TARGETS (not TARGETS) is what the Makefile forwards to the gate.
    make -s release-verify VERSION="$tag" PUBLIC_TARGETS="$targets" DIST="$dist"
    echo "==> $dist"

# Never replaces an existing release: package managers and users pin the archive
# hashes. The tag must already be pushed (just push) and tests are gated earlier
# in `just upgrade` before the tag is cut.
#
# Build and create the GitHub release on the fork for TAG (newest by default).
release tag=paswolf_tag: _clean
    #!/usr/bin/env bash
    set -euo pipefail
    tag="{{tag}}"
    command -v gh >/dev/null 2>&1 || { echo "gh (GitHub CLI) is required" >&2; exit 1; }
    if [ -z "$tag" ]; then
        echo "no *-paswolf.* tag found: create one with 'just tag' (or pass a tag)" >&2
        exit 1
    fi
    current=$(git branch --show-current)
    if [ "$current" != "{{stack}}" ]; then
        echo "releases are cut from {{stack}}, currently on '$current'" >&2
        exit 1
    fi
    git ls-remote --exit-code --tags "{{fork}}" "refs/tags/$tag" >/dev/null 2>&1 || {
        echo "tag $tag is not on {{fork}} yet: run 'just push' first" >&2
        exit 1
    }
    if gh release view "$tag" --repo "{{fork_repo}}" >/dev/null 2>&1; then
        echo "release $tag already exists on {{fork_repo}}; cut a new tag instead of replacing assets" >&2
        exit 1
    fi

    just release-build "$tag"

    dist="{{release_dist}}"
    targets="{{release_targets}}"
    base=$(git describe --tags --abbrev=0 "{{upstream}}/main")
    notes=$(mktemp)
    trap 'rm -f "$notes"' EXIT
    {
        printf 'Paswolf build of upstream %s — %s — with the matching embedded vigolium-audit and jstangle helpers.\n\n' "$base" "$targets"
        printf '### Patches on top of `%s`\n\n' "$base"
        git log --format='- `%h` %s' "$base..{{stack}}"
        printf '\nVerify the downloads: `sha256sum -c checksums.txt`\n'
    } > "$notes"

    gh release create "$tag" "$dist"/vigolium_*.tar.gz "$dist"/checksums.txt "$dist"/metadata.json \
        --repo "{{fork_repo}}" --title "vigolium $tag" --notes-file "$notes"

# start a PR-sized branch off upstream with one commit cherry-picked from the stack
pr-branch name commit: _clean
    git co -b {{name}} {{upstream}}/main
    git cherry-pick {{commit}}
    @echo "review, then: just pr-push {{name}}"

pr-push name:
    git push -u {{fork}} {{name}}
    @echo "open the PR against upstream (not your fork's default branch):"
    @echo "  https://github.com/{{upstream_repo}}/compare/main...{{fork_repo}}:{{name}}?expand=1"
