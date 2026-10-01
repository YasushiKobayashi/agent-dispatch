#!/usr/bin/env bash
# Exercises next-version.sh against throwaway repositories.
set -euo pipefail

script="$(cd "$(dirname "$0")" && pwd)/next-version.sh"
failures=0

repo() {
	dir=$(mktemp -d)
	git -C "$dir" init -q
	git -C "$dir" config user.email test@example.com
	git -C "$dir" config user.name test
	git -C "$dir" config commit.gpgsign false
	git -C "$dir" config tag.gpgsign false
}

commit() { git -C "$dir" commit -q --allow-empty -m "$1" ${2:+-m "$2"}; }

expect() {
	local name=$1 want=$2 got
	got=$(cd "$dir" && "$script")
	if [[ "$got" != "$want" ]]; then
		echo "FAIL $name: got '$got', want '$want'"
		failures=$((failures + 1))
	else
		echo "ok   $name"
	fi
	rm -rf "$dir"
}

repo; commit "chore: init"; expect "first release" v0.1.0

repo; commit "chore: init"; git -C "$dir" tag v0.1.0; expect "already released" ""

repo; commit "chore: init"; git -C "$dir" tag v0.1.0
commit "fix: a"; commit "docs: b"; expect "patch" v0.1.1

repo; commit "chore: init"; git -C "$dir" tag v0.1.3
commit "fix: a"; commit "feat(ghostty): b"; expect "minor resets patch" v0.2.0

repo; commit "chore: init"; git -C "$dir" tag v0.2.1
commit "feat(cli)!: drop flag"; expect "bang is major" v1.0.0

repo; commit "chore: init"; git -C "$dir" tag v1.2.3
commit "fix: a" "BREAKING CHANGE: config key renamed"; expect "footer is major" v2.0.0

repo; commit "chore: init"; git -C "$dir" tag v0.1.0
commit "Merge pull request #3 from x/feature"; expect "merge commit is patch" v0.1.1

repo; commit "chore: init"; git -C "$dir" tag v0.1.0
commit "docs: mention feat: in body" "feat: not a subject"; expect "feat only counts in the subject" v0.1.1

repo; commit "chore: init"; git -C "$dir" tag v0.9.0; commit "fix: a"; git -C "$dir" tag v0.9.1
commit "feat: b"; expect "latest tag wins" v0.10.0

exit "$failures"
