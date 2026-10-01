#!/usr/bin/env bash
# Prints the next release tag, derived from the Conventional Commits made since
# the latest vX.Y.Z tag reachable from HEAD. Prints nothing when HEAD is already
# released, so the caller can skip the release.
#
#   feat!: / fix(x)!: / "BREAKING CHANGE:" in a body -> major
#   feat: / feat(x):                                   -> minor
#   anything else (fix, chore, docs, merge commits)    -> patch
#
# With no tag yet, the first release is v0.1.0.
set -euo pipefail

initial=v0.1.0

last=$(git describe --tags --abbrev=0 --match 'v[0-9]*.[0-9]*.[0-9]*' 2>/dev/null || true)
if [[ -z "$last" ]]; then
	echo "$initial"
	exit 0
fi

range="$last..HEAD"
if [[ -z "$(git rev-list "$range")" ]]; then
	exit 0
fi

if [[ ! "$last" =~ ^v([0-9]+)\.([0-9]+)\.([0-9]+)$ ]]; then
	echo "next-version: cannot parse tag $last" >&2
	exit 1
fi
major=${BASH_REMATCH[1]} minor=${BASH_REMATCH[2]} patch=${BASH_REMATCH[3]}

subjects=$(git log --format=%s "$range")
bodies=$(git log --format=%b "$range")

if grep -Eq '^[a-z]+(\([^)]*\))?!:' <<<"$subjects" || grep -Eq '^BREAKING[ -]CHANGE:' <<<"$bodies"; then
	echo "v$((major + 1)).0.0"
elif grep -Eq '^feat(\([^)]*\))?:' <<<"$subjects"; then
	echo "v$major.$((minor + 1)).0"
else
	echo "v$major.$minor.$((patch + 1))"
fi
