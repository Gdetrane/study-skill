#!/bin/sh
# Installs Lamplight's study binary from a release (ADR-0010):
#
#   curl -fsSL https://raw.githubusercontent.com/mordor-forge/lamplight/main/install.sh | sh
#
# It downloads the archive for this machine, checks it against the release's
# checksums.txt, and puts study in ~/.local/bin without sudo. Running it again
# upgrades. docs/release.md lists the files a release holds.
#
# Environment:
#   STUDY_VERSION       the release to install, such as v2.0.0 (default: the latest)
#   STUDY_INSTALL_DIR   the folder study goes in (default: $HOME/.local/bin)
#   STUDY_DOWNLOAD_URL  a folder of release files to download from instead of
#                       the GitHub release, as an https:// or file:// address,
#                       such as file:///path/to/dist
#
# Everything runs from main, called on the last line, so a download of this
# script that was cut short runs nothing.
set -eu

repo_url=https://github.com/mordor-forge/lamplight
go_install='go install github.com/mordor-forge/lamplight/v2/cmd/study@latest'

say() {
	printf '%s\n' "$*"
}

fail() {
	printf 'install.sh: %s\n' "$*" >&2
	exit 1
}

need() {
	command -v "$1" >/dev/null 2>&1 || fail "needs $1, which is not on your PATH"
}

usage() {
	say "Usage: sh install.sh"
	say ""
	say "Installs study from a Lamplight release into ~/.local/bin."
	say ""
	say "  STUDY_VERSION       the release to install, such as v2.0.0 (default: the latest)"
	say "  STUDY_INSTALL_DIR   the folder study goes in (default: \$HOME/.local/bin)"
	say "  STUDY_DOWNLOAD_URL  a folder of release files to download from instead of GitHub"
}

# platform sets os and arch to the names the release archives use.
platform() {
	system=$(uname -s)
	machine=$(uname -m)
	case $system in
	Linux) os=linux ;;
	Darwin) os=darwin ;;
	*) unsupported ;;
	esac
	case $machine in
	x86_64 | amd64) arch=amd64 ;;
	aarch64 | arm64) arch=arm64 ;;
	*) unsupported ;;
	esac
	# A shell that Rosetta translates reports x86_64 on Apple silicon.
	if [ "$os" = darwin ] && [ "$arch" = amd64 ] &&
		[ "$(sysctl -n sysctl.proc_translated 2>/dev/null || true)" = 1 ]; then
		arch=arm64
	fi
}

unsupported() {
	printf 'install.sh: there is no study build for %s on %s.\n' "$system" "$machine" >&2
	printf 'Releases cover Linux and macOS, on amd64 and arm64. With Go 1.26 or later, build it instead:\n  %s\n' "$go_install" >&2
	exit 1
}

# fetch downloads URL $1 into file $2. An https URL may only redirect to
# https; the other kind is a local file.
fetch() {
	case $1 in
	https://*) curl --proto '=https' --tlsv1.2 --retry 2 -fsSL -o "$2" "$1" ;;
	*) curl -fsSL -o "$2" "$1" ;;
	esac
}

# sha256 prints the SHA-256 of file $1. A Mac may have shasum and no sha256sum.
sha256() {
	if command -v sha256sum >/dev/null 2>&1; then
		sha256sum "$1" | awk '{ print $1 }'
	else
		shasum -a 256 "$1" | awk '{ print $1 }'
	fi
}

# put copies file $1 to $2 with mode $3. It writes beside $2 and renames, so
# $2 is never half-written, and a study that is running is replaced, not
# written into.
put() {
	staged=${2%/*}/.${2##*/}.install.$$
	if cp "$1" "$staged" && chmod "$3" "$staged" && mv -f "$staged" "$2"; then
		staged=
		return 0
	fi
	rm -f "$staged"
	staged=
	return 1
}

cleanup() {
	rm -rf "$tmp"
	[ -z "$staged" ] || rm -f "$staged"
}

main() {
	case ${1:-} in
	'') ;;
	-h | --help)
		usage
		exit 0
		;;
	*)
		usage >&2
		exit 2
		;;
	esac

	need uname
	need curl
	need tar
	need mktemp
	need awk
	command -v sha256sum >/dev/null 2>&1 || command -v shasum >/dev/null 2>&1 ||
		fail "needs sha256sum or shasum, to check the download"

	platform
	archive=lamplight_${os}_${arch}.tar.gz

	version=${STUDY_VERSION:-latest}
	case $version in
	latest) base=$repo_url/releases/latest/download ;;
	*[!0-9A-Za-z.-]*) fail "STUDY_VERSION must be a release such as v2.0.0, not \"$version\"" ;;
	v*) base=$repo_url/releases/download/$version ;;
	*) base=$repo_url/releases/download/v$version ;;
	esac
	if [ -n "${STUDY_DOWNLOAD_URL:-}" ]; then
		base=${STUDY_DOWNLOAD_URL%/}
		case $base in
		https://* | file://*) ;;
		*) fail "STUDY_DOWNLOAD_URL must start with https:// or file://, not \"$base\"" ;;
		esac
	fi

	if [ -n "${STUDY_INSTALL_DIR:-}" ]; then
		dir=$STUDY_INSTALL_DIR
	elif [ -n "${HOME:-}" ]; then
		dir=$HOME/.local/bin
	else
		fail "HOME is not set: name the folder study goes in with STUDY_INSTALL_DIR"
	fi

	staged=
	tmp=$(mktemp -d)
	trap cleanup EXIT
	trap 'exit 1' HUP INT TERM

	say "Downloading $archive from $base"
	fetch "$base/checksums.txt" "$tmp/checksums.txt" ||
		fail "could not download $base/checksums.txt; nothing was installed"
	fetch "$base/$archive" "$tmp/$archive" ||
		fail "could not download $base/$archive; nothing was installed"

	want=$(awk -v name="$archive" '{ file = $2; sub(/^\*/, "", file) } file == name { print $1; exit }' "$tmp/checksums.txt")
	[ -n "$want" ] ||
		fail "checksums.txt does not list $archive, so the download cannot be checked; nothing was installed"
	got=$(sha256 "$tmp/$archive")
	[ "$got" = "$want" ] ||
		fail "$archive does not match its checksum (expected $want, got ${got:-none}); nothing was installed"

	mkdir "$tmp/files"
	tar -xzf "$tmp/$archive" -C "$tmp/files" || fail "could not unpack $archive; nothing was installed"
	[ -f "$tmp/files/study" ] || fail "$archive holds no study; nothing was installed"

	mkdir -p "$dir" || fail "could not create $dir; nothing was installed"
	dir=$(CDPATH='' cd -- "$dir" && pwd) || fail "could not open $dir; nothing was installed"
	dest=$dir/study
	[ ! -d "$dest" ] || fail "$dest is a folder; nothing was installed"
	put "$tmp/files/study" "$dest" 755 || fail "could not write $dest; nothing was installed"
	installed=$("$dest" --version 2>&1) || fail "installed $dest, but it does not run: $installed"
	say "Installed $dest ($installed)"

	# man looks beside each folder on PATH: for <prefix>/bin, in <prefix>/share/man.
	case $dir in
	*/bin)
		man1=${dir%/bin}/share/man/man1
		if [ -f "$tmp/files/manpages/study.1.gz" ]; then
			if mkdir -p "$man1" && put "$tmp/files/manpages/study.1.gz" "$man1/study.1.gz" 644; then
				say "Installed $man1/study.1.gz"
			else
				say "The man page was not installed: could not write $man1/study.1.gz"
			fi
		fi
		;;
	esac

	# Best effort: study is installed whether or not its completions are.
	# study gets no input, so it asks nothing: the input here may be the pipe
	# that sh is reading this script from.
	if ! "$dest" completion install </dev/null; then
		say "Shell completions were not installed. To install them later: $dest completion install"
	fi

	study=study
	case :${PATH:-}: in
	*:"$dir":* | *:"$dir"/:*)
		# The same file under another spelling of its path is not another study.
		first=$(command -v study 2>/dev/null) || first=
		if [ -n "$first" ] && [ "$first" != "$dest" ] &&
			command -v cmp >/dev/null 2>&1 && ! cmp -s "$first" "$dest"; then
			study=$dest
			say ""
			say "Another study comes first on your PATH: $first"
			say "Remove it, or put $dir before its folder."
		fi
		;;
	*)
		study=$dest
		shell=${SHELL:-}
		say ""
		case ${shell##*/} in
		fish)
			say "$dir is not on your PATH. Run this once in fish:"
			say "  fish_add_path \"$dir\""
			;;
		zsh)
			say "$dir is not on your PATH. Add this line to ~/.zshrc, then open a new shell:"
			say "  export PATH=\"$dir:\$PATH\""
			;;
		bash)
			say "$dir is not on your PATH. Add this line to ~/.bashrc, then open a new shell:"
			say "  export PATH=\"$dir:\$PATH\""
			;;
		*)
			say "$dir is not on your PATH. Add this line to your shell's startup file:"
			say "  export PATH=\"$dir:\$PATH\""
			;;
		esac
		;;
	esac

	say ""
	say "Next, install the lamplight skill and register study with Claude Code and Codex:"
	say "  $study setup"
}

main "$@"
