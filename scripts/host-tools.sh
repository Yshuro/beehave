#!/bin/sh
# Install the system tools of a linux amd64 build host: the apt packages, the Go toolchain that go.mod names, uv and actionlint.
# scripts/host-setup.sh runs it. A host maker that owns its own idle delete (such as a SkyPilot task setup) can run it alone.
# It installs no tool that `make setup` installs. `apt-get update` and `apt-get install` run on each run; apt changes no package that is installed.
# Go, uv and actionlint are read first and installed only when they are not at their pins.
# A new Go unpacks into /usr/local/go.new and replaces /usr/local/go only after it unpacks, so a failed unpack keeps the old Go.
# The old Go moves to /usr/local/go.old for the swap and moves back when the swap fails or the new Go is not the pinned one.
# It deletes nothing outside these three folders and its own temporary folder.
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
go_staging=$go_root.new
go_backup=$go_root.old
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

# restore_go puts the old Go back when a failed swap left it in the backup and no Go in its place.
# It deletes the backup only when a Go is in place, so it never deletes the only copy.
restore_go() {
	if [ ! -e "$go_backup" ]; then return 0; fi
	if [ -e "$go_root" ]; then
		sudo rm -rf "$go_backup"
		return 0
	fi
	say "putting the old Go back from $go_backup"
	sudo mv "$go_backup" "$go_root" || fail "the old Go did not move back from $go_backup to $go_root. Move it back by hand"
}

install_go() {
	restore_go
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
	say "unpacking Go $want into $go_staging"
	sudo rm -rf "$go_staging"
	sudo mkdir -m 0755 "$go_staging"
	sudo tar -C "$go_staging" -xzf "$work/$file" || fail "the Go archive did not unpack. The Go in $go_root is unchanged"
	case $("$go_staging/go/bin/go" version 2>/dev/null || true) in
	"go version $want "*) ;;
	*) fail "the Go archive does not hold $want. The Go in $go_root is unchanged" ;;
	esac

	say "installing Go $want into $go_root"
	if [ -e "$go_root" ]; then
		sudo mv "$go_root" "$go_backup" || fail "the old Go did not move from $go_root to $go_backup. The old Go is kept in $go_root"
	fi
	if ! sudo mv "$go_staging/go" "$go_root"; then
		restore_go
		fail "the new Go did not move into $go_root. The old Go is back in place"
	fi
	case $("$go_root/bin/go" version 2>/dev/null || true) in
	"go version $want "*) ;;
	*) undo_swap ;;
	esac
	sudo rm -rf "$go_staging" "$go_backup"
}

# undo_swap stops the install when the Go in place is not the pinned one after the swap. The backup is not deleted yet,
# so the new Go moves back to the staging folder and the old Go moves back in place.
undo_swap() {
	[ -e "$go_backup" ] || fail "the Go archive did not install $want into $go_root"
	sudo mv "$go_root" "$go_staging/go" ||
		fail "the Go in $go_root was not $want after the swap, and it did not move out. The old Go is in $go_backup. Move it back by hand"
	restore_go
	fail "the Go in $go_root was not $want after the swap. The old Go is back in place"
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

PATH=$go_root/bin:$PATH
export PATH
work=$(mktemp -d)
trap 'rm -rf "$work"' EXIT

install_packages
install_go
# go install puts actionlint into the bin folder of the first GOPATH entry.
gopath=$(go env GOPATH)
PATH=${gopath%%:*}/bin:$PATH
install_uv
install_actionlint
