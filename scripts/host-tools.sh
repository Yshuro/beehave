#!/bin/sh
# Install the system tools of a linux amd64 build host: the apt packages, the Go toolchain that go.mod names, uv and actionlint.
# scripts/host-setup.sh runs it. A host maker that owns its own idle delete (such as a SkyPilot task setup) can run it alone.
# It installs no tool that `make setup` installs. Each step reads the state first and changes only what differs, so a second run changes nothing.
# A new uv version needs a new sha256: take it from the digest of the asset in the release (`gh release view <tag> -R astral-sh/uv --json assets`).
#
# Usage: sh scripts/host-tools.sh   (from any folder; it reads go.mod next to the scripts folder)
# HOST_SETUP_SYSROOT is a folder that stands for / in the tests of the script. It is empty on a host.
set -eu

uv_version=0.13.6
uv_sha256=f29c3e94ffabd7e6f3ba521dba169a9b7ec12100bf3257087f0f24f6123f3645
actionlint_version=v1.7.12

root=$(cd "$(dirname "$0")/.." && pwd)
sysroot=${HOST_SETUP_SYSROOT:-}
go_root=$sysroot/usr/local/go
bin_dir=$sysroot/usr/local/bin

fail() {
	printf 'host-tools: %s\n' "$*" >&2
	exit 1
}

say() { printf 'host-tools: %s\n' "$*"; }

# version_of <command and arguments>: print the first number with dots in the output, or nothing.
version_of() { "$@" 2>/dev/null | grep -Eo '[0-9]+(\.[0-9]+)+' | head -n 1 || true; }

sha256_of() {
	if command -v sha256sum >/dev/null 2>&1; then
		sha256sum "$1" | cut -d ' ' -f 1
	else
		shasum -a 256 "$1" | cut -d ' ' -f 1
	fi
}

# check_sha <file> <sha256>: refuse a download whose checksum does not match.
check_sha() {
	[ "$(sha256_of "$1")" = "$2" ] || fail "the checksum of $(basename "$1") does not match the pin. Do not install it; check the pin and the download"
}

install_packages() {
	say 'installing the apt packages'
	sudo DEBIAN_FRONTEND=noninteractive apt-get update -q
	sudo DEBIAN_FRONTEND=noninteractive apt-get install -y -q --no-install-recommends \
		build-essential git make curl ca-certificates jq shellcheck bats
}

# go_pin prints the toolchain version of go.mod, such as go1.27.4. A `go` line alone is not a full version, so it refuses.
go_pin() {
	line=$(grep -m 1 -E '^toolchain go[0-9]' "$root/go.mod") ||
		fail "$root/go.mod has no toolchain line, so the Go version is not a full version. Add a line such as \"toolchain go1.27.4\""
	printf '%s\n' "${line#toolchain }"
}

install_go() {
	want=$(go_pin)
	case $("$go_root/bin/go" version 2>/dev/null || true) in
	"go version $want "*)
		say "Go $want is installed"
		return 0
		;;
	esac

	file=$want.linux-amd64.tar.gz
	curl -fsSL --retry 3 -o "$work/go.json" 'https://go.dev/dl/?mode=json&include=all'
	sha=$(jq -r --arg f "$file" '[.[].files[] | select(.filename == $f)][0].sha256 // empty' "$work/go.json")
	[ -n "$sha" ] || fail "go.dev lists no $file. Check the toolchain line of go.mod"
	curl -fsSL --retry 3 -o "$work/$file" "https://go.dev/dl/$file"
	check_sha "$work/$file" "$sha"
	say "installing Go $want into $go_root"
	sudo rm -rf "$go_root"
	sudo tar -C "$(dirname "$go_root")" -xzf "$work/$file"
	case $("$go_root/bin/go" version 2>/dev/null || true) in
	"go version $want "*) ;;
	*) fail "the Go archive did not install $want into $go_root" ;;
	esac
}

install_uv() {
	if [ "$(version_of uv --version)" = "$uv_version" ]; then
		say "uv $uv_version is installed"
		return 0
	fi

	name=uv-x86_64-unknown-linux-gnu
	curl -fsSL --retry 3 -o "$work/$name.tar.gz" "https://github.com/astral-sh/uv/releases/download/$uv_version/$name.tar.gz"
	check_sha "$work/$name.tar.gz" "$uv_sha256"
	tar -xzf "$work/$name.tar.gz" -C "$work"
	say "installing uv $uv_version into $bin_dir"
	sudo install -m 0755 "$work/$name/uv" "$work/$name/uvx" "$bin_dir/"
}

install_actionlint() {
	if [ "$(version_of actionlint --version)" = "${actionlint_version#v}" ]; then
		say "actionlint $actionlint_version is installed"
		return 0
	fi

	say "installing actionlint $actionlint_version"
	go install "github.com/rhysd/actionlint/cmd/actionlint@$actionlint_version"
}

if [ "$(uname -s)" != Linux ] || [ "$(uname -m)" != x86_64 ]; then
	fail 'the pinned archives are for linux amd64 only'
fi

PATH=$go_root/bin:$HOME/go/bin:$PATH
export PATH
work=$(mktemp -d)
trap 'rm -rf "$work"' EXIT

install_packages
install_go
install_uv
install_actionlint
