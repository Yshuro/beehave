#!/bin/sh
# Install the tools of a linux amd64 build host. `system` runs as root and installs the apt packages, the Go toolchain that go.mod names and uv.
# `user` runs as the job user, with no sudo, and installs actionlint into the Go folder of that user.
# scripts/host-setup.sh runs both. A host maker that owns its own idle delete (such as a SkyPilot task setup) can run `system` alone.
# It installs no tool that `make setup` installs. `apt-get update` and `apt-get install` run on each run; apt changes no package that is installed.
# Go, uv and actionlint are read first and installed only when they are not at their pins.
# A new Go unpacks into /usr/local/go.new and replaces /usr/local/go only after it unpacks, so a failed unpack keeps the old Go.
# The old Go moves to /usr/local/go.old for the swap and moves back when the swap fails or the new Go is not the pinned one.
# A stray /usr/local/go.old with no /usr/local/go moves back first, before the script reads the pin from go.mod.
# /usr/local/go.old is deleted only when the Go in /usr/local/go prints the pinned version.
# It deletes nothing outside these three folders and its own temporary folder.
# A new uv version needs a new sha256: take it from the digest of the asset in the release (`gh release view <tag> -R astral-sh/uv --json assets`).
#
# Usage: sh scripts/host-tools.sh system|user   (from any folder; it reads go.mod next to the scripts folder)
# HOST_SETUP_SYSROOT is a folder that stands for / in the tests of the script. It is empty on a host.
set -eu

uv_version=0.13.6
uv_sha256=f29c3e94ffabd7e6f3ba521dba169a9b7ec12100bf3257087f0f24f6123f3645
actionlint_version=v1.7.12

root=$(cd "$(dirname "$0")/.." && pwd -P)
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
		build-essential git make curl ca-certificates jq shellcheck bats nftables
}

# go_pin prints the toolchain version of go.mod, such as go1.27.4. A `go` line alone is not a full version, so it refuses.
go_pin() {
	line=$(grep -m 1 -E '^toolchain go[0-9]' "$root/go.mod") ||
		fail "$root/go.mod has no toolchain line, so the Go version is not a full version. Add a line such as \"toolchain go1.27.4\""
	printf '%s\n' "${line#toolchain }"
}

# is_pinned_go <go command>: succeed when the command runs and prints the pinned version.
is_pinned_go() {
	case $("$1" version 2>/dev/null || true) in
	"go version $want "*) return 0 ;;
	esac
	return 1
}

# restore_go puts the old Go back when a failed swap left it in the backup and no Go in its place.
# It deletes the backup only when the Go in place prints the pinned version. When another Go is in place, it stops and deletes neither,
# because the backup can be the only Go that runs.
restore_go() {
	if [ ! -e "$go_backup" ]; then return 0; fi
	return_stray_go
	if [ ! -e "$go_backup" ]; then return 0; fi
	is_pinned_go "$go_root/bin/go" ||
		fail "the Go in $go_root is not $want, and an old Go is in $go_backup. This script deletes neither folder. Move or delete them by hand, so that the Go that runs is in $go_root and $go_backup is gone. Then run this again"
	sudo rm -rf "$go_backup"
}

# return_stray_go puts a stray backup back when no Go is in place. It needs no pin, so it runs before anything reads go.mod.
return_stray_go() {
	if [ -e "$go_backup" ] && [ ! -e "$go_root" ]; then
		say "putting the old Go back from $go_backup"
		sudo mv "$go_backup" "$go_root" || fail "the old Go did not move back from $go_backup to $go_root. Move it back by hand"
	fi
}

install_go() {
	return_stray_go
	want=$(go_pin)
	restore_go
	if is_pinned_go "$go_root/bin/go"; then
		say "Go $want is installed"
		return 0
	fi

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
	is_pinned_go "$go_staging/go/bin/go" || fail "the Go archive does not hold $want. The Go in $go_root is unchanged"

	say "installing Go $want into $go_root"
	if [ -e "$go_root" ]; then
		sudo mv "$go_root" "$go_backup" || fail "the old Go did not move from $go_root to $go_backup. The old Go is kept in $go_root"
	fi
	if ! sudo mv "$go_staging/go" "$go_root"; then
		restore_go
		fail "the new Go did not move into $go_root. The old Go is back in place"
	fi
	is_pinned_go "$go_root/bin/go" || undo_swap
	sudo rm -rf "$go_staging" "$go_backup"
}

# undo_swap stops the install when the Go in place is not the pinned one after the swap. The backup is not deleted yet,
# so the new Go moves back to the staging folder and the old Go moves back in place.
undo_swap() {
	[ -e "$go_backup" ] || fail "the Go archive did not install $want into $go_root"
	sudo mv "$go_root" "$go_staging/go" ||
		fail "the Go in $go_root was not $want after the swap, and it did not move out. The old Go is in $go_backup. Move it back by hand. A second run deletes neither folder"
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

stage=${1:-}
case $stage in
system | user) ;;
*) fail "name the stage: sh scripts/host-tools.sh system (as root: apt, Go, uv) or sh scripts/host-tools.sh user (as the job user: actionlint)" ;;
esac

if [ "$(uname -s)" != Linux ] || [ "$(uname -m)" != x86_64 ]; then
	fail 'the pinned archives are for linux amd64 only'
fi

PATH=$go_root/bin:$PATH
export PATH
work=$(mktemp -d)
trap 'rm -rf "$work"' EXIT

case $stage in
system)
	install_packages
	install_go
	install_uv
	;;
user)
	[ -x "$go_root/bin/go" ] || fail "there is no Go in $go_root. Run the root stage of scripts/host-setup.sh first"
	# go install puts actionlint into the bin folder of the first GOPATH entry.
	gopath=$(go env GOPATH)
	PATH=${gopath%%:*}/bin:$PATH
	install_actionlint
	;;
esac
