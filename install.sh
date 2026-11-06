#!/bin/sh
# Install whoas from a GitHub release.
#
#   curl -fsSL https://raw.githubusercontent.com/S-kkipie/whoas/main/install.sh | sh
#
# Environment:
#   WHOAS_VERSION       release tag to install (default: the latest release), e.g. v0.3.0
#   WHOAS_INSTALL_DIR   where to put the binary (default: $HOME/.local/bin)
#
# Linux and macOS, amd64 and arm64. The archive is checked against the
# release's checksums.txt before anything is installed. On WSL the Windows
# build (whoas.exe) is installed next to whoas too: it is the bridge that keeps
# the master key in Windows Credential Manager.

set -eu

REPO="S-kkipie/whoas"

say() { printf 'whoas-install: %s\n' "$*" >&2; }
die() { printf 'whoas-install: error: %s\n' "$*" >&2; exit 1; }
has() { command -v "$1" >/dev/null 2>&1; }

# fetch URL: print the body on stdout
fetch() {
	if has curl; then
		curl -fsSL "$1"
	else
		wget -qO- "$1"
	fi
}

# download URL FILE (the caller handles a failure)
download() {
	say "downloading $1"
	if has curl; then
		curl -fsSL -o "$2" "$1"
	else
		wget -q -O "$2" "$1"
	fi
}

sha256() {
	if has sha256sum; then
		sha256sum "$1" | cut -d ' ' -f 1
	elif has shasum; then
		shasum -a 256 "$1" | cut -d ' ' -f 1
	else
		die "need sha256sum or shasum to verify the download"
	fi
}

# verify FILE CHECKSUMS: FILE's sha256 must match its line in CHECKSUMS
verify() {
	name=$(basename "$1")
	want=$(awk -v n="$name" '$2 == n { print $1 }' "$2")
	[ -n "$want" ] || die "$name is not listed in checksums.txt"
	got=$(sha256 "$1")
	[ "$got" = "$want" ] || die "checksum mismatch for $name (expected $want, got $got)"
	say "verified $name (sha256 $got)"
}

latest_tag() {
	tag=$(fetch "https://api.github.com/repos/$REPO/releases/latest" 2>/dev/null |
		sed -n 's/.*"tag_name"[[:space:]]*:[[:space:]]*"\([^"]*\)".*/\1/p' | head -n 1) || tag=""
	if [ -z "$tag" ] && has curl; then
		# API unreachable or rate-limited: follow the releases/latest redirect
		tag=$(curl -fsSLI -o /dev/null -w '%{url_effective}' "https://github.com/$REPO/releases/latest" 2>/dev/null |
			sed -n 's|.*/releases/tag/||p') || tag=""
	fi
	[ -n "$tag" ] || die "could not resolve the latest release; set WHOAS_VERSION (e.g. WHOAS_VERSION=v0.3.0)"
	printf '%s\n' "$tag"
}

# install_file SRC DIR NAME: atomic replace, so a running whoas agent is no obstacle
install_file() {
	cp "$1" "$2/.$3.tmp.$$"
	chmod 0755 "$2/.$3.tmp.$$"
	mv -f "$2/.$3.tmp.$$" "$2/$3"
}

main() {
	has curl || has wget || die "need curl or wget"
	has tar || die "need tar"

	case "$(uname -s)" in
		Linux) os=linux ;;
		Darwin) os=darwin ;;
		*) die "unsupported OS: $(uname -s) (whoas runs on Linux, WSL and macOS)" ;;
	esac
	case "$(uname -m)" in
		x86_64 | amd64) arch=amd64 ;;
		aarch64 | arm64) arch=arm64 ;;
		*) die "unsupported architecture: $(uname -m) (release builds: amd64, arm64)" ;;
	esac

	wsl=0
	if [ "$os" = linux ] && [ -r /proc/version ] && grep -qi microsoft /proc/version; then
		wsl=1
		has unzip || die "WSL detected: unzip is needed to install whoas.exe (the Windows Credential Manager bridge). Install it (Debian/Ubuntu: sudo apt-get install unzip) and run this again"
	fi

	tag=${WHOAS_VERSION:-}
	if [ -z "$tag" ]; then
		tag=$(latest_tag)
	fi
	case "$tag" in
		v*) ;;
		*) tag="v$tag" ;;
	esac
	version=${tag#v}

	dir=${WHOAS_INSTALL_DIR:-$HOME/.local/bin}
	mkdir -p "$dir" || die "cannot create $dir"
	[ -w "$dir" ] || die "$dir is not writable (set WHOAS_INSTALL_DIR)"

	tmp=$(mktemp -d 2>/dev/null || mktemp -d -t whoas)
	trap 'rm -rf "$tmp"' EXIT
	trap 'exit 130' INT TERM

	base="https://github.com/$REPO/releases/download/$tag"
	say "installing whoas $tag ($os/$arch) into $dir"
	download "$base/checksums.txt" "$tmp/checksums.txt" ||
		die "no release $tag at https://github.com/$REPO/releases (or the download failed)"

	archive="whoas_${version}_${os}_${arch}.tar.gz"
	download "$base/$archive" "$tmp/$archive" || die "download failed: $base/$archive"
	verify "$tmp/$archive" "$tmp/checksums.txt"
	mkdir "$tmp/unix"
	tar -xzf "$tmp/$archive" -C "$tmp/unix"
	[ -f "$tmp/unix/whoas" ] || die "$archive does not contain whoas"

	if [ "$wsl" = 1 ]; then
		zip="whoas_${version}_windows_${arch}.zip"
		download "$base/$zip" "$tmp/$zip" || die "download failed: $base/$zip"
		verify "$tmp/$zip" "$tmp/checksums.txt"
		mkdir "$tmp/win"
		unzip -q "$tmp/$zip" -d "$tmp/win"
		[ -f "$tmp/win/whoas.exe" ] || die "$zip does not contain whoas.exe"
	fi

	install_file "$tmp/unix/whoas" "$dir" whoas
	say "installed $dir/whoas"
	if [ "$wsl" = 1 ]; then
		install_file "$tmp/win/whoas.exe" "$dir" whoas.exe
		say "installed $dir/whoas.exe (WSL: master key in Windows Credential Manager)"
	fi

	"$dir/whoas" version >&2 || die "$dir/whoas does not run on this machine"

	case ":$PATH:" in
		*":$dir:"*)
			found=$(command -v whoas 2>/dev/null || true)
			if [ -n "$found" ] && [ "$found" != "$dir/whoas" ]; then
				say "warning: $found comes first on your PATH and shadows $dir/whoas"
			fi
			;;
		*)
			say "$dir is not on your PATH; add this to your shell rc:"
			# shellcheck disable=SC2016 # print $PATH literally
			printf '\n    export PATH="%s:$PATH"\n\n' "$dir" >&2
			;;
	esac
	say "next: whoas doctor"
}

main "$@"
