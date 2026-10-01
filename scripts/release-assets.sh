#!/bin/sh
# Generates the shell completions and the man page that every release package
# ships, into completions/ and manpages/ at the repository root. GoReleaser runs
# it before building (see .goreleaser.yaml); both folders are gitignored.
#
# The man page comes from fang's hidden "man" command, which writes to the
# process's stdout directly, so it is captured by redirecting the process.
set -eu

cd "$(dirname "$0")/.."

tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT
go build -o "$tmp/study" ./cmd/study

rm -rf completions manpages
mkdir -p completions manpages
for shell in bash zsh fish; do
	"$tmp/study" completion "$shell" >"completions/study.$shell"
done
"$tmp/study" man | gzip -9n >manpages/study.1.gz

for f in completions/study.bash completions/study.zsh completions/study.fish manpages/study.1.gz; do
	if [ ! -s "$f" ]; then
		echo "release-assets: $f is empty" >&2
		exit 1
	fi
done
