#!/bin/sh
# Make a linux amd64 machine ready as a beehave build host for the ssh provider, then prove it. See docs/design/hosts.md.
# Run it on the host, as the login user, from a clone of the repository at the commit under test. A second run changes nothing.
# It installs no watchdog: it refuses to start and to finish unless the named self-delete watchdog unit is active, enabled and Restart=always.
# It names no cloud and touches no heartbeat. It deletes nothing outside /usr/local/go, /usr/local/go.new, /usr/local/go.old and its own temporary folder.
# It refuses a work root that is not its canonical path, a system folder, and an existing work root that the login user does not own.
# It makes the last folder of a missing work root only, as the login user and without sudo, in a folder that the login user owns.
# It never changes the owner or the mode of a folder.
# The last line of a pass is HOST-SETUP-DONE.
#
# Usage: sh scripts/host-setup.sh --work-root <absolute path> --watchdog <systemd unit>
# HOST_SETUP_SYSROOT is a folder that stands for / in the tests of the script. It is empty on a host.
set -eu

usage='usage: sh scripts/host-setup.sh --work-root <absolute path> --watchdog <systemd unit>'
min_systemd=240
landlock_major=6
landlock_minor=7

root=$(cd "$(dirname "$0")/.." && pwd)
sysroot=${HOST_SETUP_SYSROOT:-}
go_root=$sysroot/usr/local/go
lsm_file=$sysroot/sys/kernel/security/lsm
env_file=$HOME/.config/environment.d/60-beehave.conf
work_root=
watchdog=

fail() {
	printf 'host-setup: %s\n' "$*" >&2
	exit 1
}

say() { printf 'host-setup: %s\n' "$*"; }

read_args() {
	while [ $# -gt 0 ]; do
		[ $# -ge 2 ] || fail "$1 needs a value. $usage"
		case $1 in
		--work-root) work_root=$2 ;;
		--watchdog) watchdog=$2 ;;
		*) fail "unknown argument $1. $usage" ;;
		esac
		shift 2
	done
}

# check_work_root refuses a path that the adapter refuses: systemd expands a percent sign, and a dollar sign is no path.
check_work_root() {
	case $work_root in
	'') fail "--work-root is required. $usage" ;;
	*[%\$]*) fail "the work root $work_root must not hold a percent or dollar sign, which systemd and the shell would expand" ;;
	*[[:cntrl:]]*) fail 'the work root must hold no control character' ;;
	/*) ;;
	*) fail "the work root $work_root must be an absolute path" ;;
	esac
	check_work_root_folder
}

is_system_folder() {
	case $1 in
	/ | /bin | /boot | /dev | /etc | /home | /lib | /lib32 | /lib64 | /libx32 | /media | /mnt | /opt | /proc | /root | /run | /sbin | /snap | /srv | /sys | /tmp | /usr | /var) return 0 ;;
	esac
	return 1
}

# check_work_root_folder refuses a work root that is not its canonical path, a system folder, and an existing work root that is not a
# folder of the login user. A canonical path has no . or .. part, no doubled or trailing slash and no link, so the path names exactly
# the folder that the script reads or makes. A missing work root needs a parent folder that exists and that the login user owns.
check_work_root_folder() {
	canonical=$(realpath -m -- "$work_root") || fail "the work root $work_root cannot be read"
	[ "$canonical" = "$work_root" ] ||
		fail "the work root $work_root is not a canonical path: it names $canonical. Name the folder with no . or .. part, no doubled or trailing slash and no link"
	if is_system_folder "$work_root"; then
		fail "the work root $work_root is a system folder. Name a folder of its own, such as /home/<user>/jobs"
	fi

	if [ -e "$work_root" ] || [ -L "$work_root" ]; then
		[ -d "$work_root" ] || fail "the work root $work_root is not a folder"
		[ "$(stat -c %u -- "$work_root")" = "$(id -u)" ] ||
			fail "the work root $work_root is not owned by the login user. Name a folder of the login user, or a folder that does not exist yet"
		[ -w "$work_root" ] || fail "the login user cannot write to the work root $work_root"
		return 0
	fi

	parent=$(dirname -- "$work_root")
	[ -d "$parent" ] ||
		fail "the work root $work_root is missing, and its parent $parent is not a folder. Make the parent folder first, or name a work root whose parent exists"
	[ "$(stat -c %u -- "$parent")" = "$(id -u)" ] ||
		fail "the parent $parent of the work root $work_root is not owned by the login user, so the script does not make the work root. The owner of $parent makes it by hand: sudo mkdir -m 0755 -- '$work_root' && sudo chown $(id -un):$(id -gn) -- '$work_root'"
}

check_watchdog_name() {
	case $watchdog in
	'') fail "--watchdog is required: name the systemd unit of the self-delete watchdog. $usage" ;;
	-*) fail "the watchdog $watchdog is not a systemd unit name: it starts with a dash" ;;
	*[!A-Za-z0-9:_.@-]*) fail "the watchdog $watchdog is not a systemd unit name" ;;
	esac
}

# refuse stops before any change.
refuse() {
	[ "$(id -u)" != 0 ] || fail 'run this as the login user of the jobs, not as root'
	if [ "$(uname -s)" != Linux ] || [ "$(uname -m)" != x86_64 ]; then
		fail 'a build host is linux amd64 only'
	fi
	sudo -n true 2>/dev/null || fail 'the login user needs sudo without a password for the installs'
	if [ ! -f "$root/go.mod" ] || [ ! -f "$root/Makefile" ]; then
		fail "$root holds no go.mod and Makefile. Run this from a clone of the repository"
	fi
	check_work_root
	check_watchdog_name
}

watchdog_state() {
	active=$(systemctl is-active "$watchdog" 2>/dev/null || true)
	enabled=$(systemctl is-enabled "$watchdog" 2>/dev/null || true)
	restart=$(systemctl show -p Restart --value "$watchdog" 2>/dev/null || true)
	printf '%s %s Restart=%s\n' "$active" "$enabled" "$restart"
}

check_watchdog() {
	state=$(watchdog_state)
	[ "$state" = 'active enabled Restart=always' ] ||
		fail "no self-delete watchdog runs on this host ($watchdog: $state). Make the host with the host maker that installs it, then run this again"
	say "watchdog $watchdog: $state"
}

wait_user_manager() {
	tries=0
	while [ "$tries" -lt 60 ]; do
		case $(systemctl --user is-system-running 2>/dev/null || true) in
		running | degraded) return 0 ;;
		esac
		tries=$((tries + 1))
		sleep 1
	done
	fail 'the user manager of the login user did not start within 60 seconds. Read systemctl --user status for the cause'
}

turn_on_linger() {
	if [ "$(loginctl show-user "$user" -p Linger)" != Linger=yes ]; then
		say "turning on linger for $user"
		sudo loginctl enable-linger "$user"
	fi
	wait_user_manager
}

# go_bin prints the bin folder of the first GOPATH entry, where go install and make contract-tools-install put the tools.
go_bin() {
	gopath=$("$go_root/bin/go" env GOPATH) || fail 'go env GOPATH failed'
	[ -n "$gopath" ] || fail 'go env GOPATH printed nothing'
	printf '%s/bin\n' "${gopath%%:*}"
}

# set_unit_path writes the PATH of the user manager, so a job finds the tools. A daemon-reload keeps the running units.
set_unit_path() {
	content="PATH=$go_root/bin:$gobin:\${PATH}
BEEHAVE_BD_132=$gobin/bd"
	if [ -f "$env_file" ] && [ "$(cat "$env_file")" = "$content" ]; then
		say "$env_file is up to date"
		return 0
	fi

	mkdir -p "$(dirname "$env_file")"
	printf '%s\n' "$content" >"$env_file.tmp"
	mv "$env_file.tmp" "$env_file"
	systemctl --user daemon-reload
	say "wrote $env_file"
}

# make_work_root makes a missing work root as the login user. It reads the folder again first, and it leaves an existing one as it is.
# It uses no sudo and no chown, so it can make a folder only where the login user can, and it changes no owner.
# It makes the last folder only, with mkdir and no -p. mkdir fails on a path that exists, so a folder or a link that appears there
# between the check and the mkdir makes the step fail instead of taking it over.
make_work_root() {
	check_work_root_folder
	if [ ! -e "$work_root" ]; then
		say "making the work root $work_root"
		mkdir -m 0755 -- "$work_root"
	fi
	[ -d "$work_root/.beehave" ] || mkdir -- "$work_root/.beehave"
}

install_project_tools() {
	PATH=$go_root/bin:$gobin:$PATH
	BEEHAVE_BD_132=$gobin/bd
	export PATH BEEHAVE_BD_132
	make setup
	make contract-tools-install
}

# make_pin <variable>: print the version that a `<variable> := v<version>` line of the Makefile pins.
make_pin() {
	line=$(grep -m 1 -E "^$1 :=" "$root/Makefile") || fail "the Makefile has no $1 line"
	value=${line#*:= }
	printf '%s\n' "${value#v}"
}

# script_pin <variable>: print the version that a `<variable>=<version>` line of scripts/host-tools.sh pins.
script_pin() {
	line=$(grep -m 1 -E "^$1=" "$root/scripts/host-tools.sh") || fail "scripts/host-tools.sh has no $1 line"
	value=${line#*=}
	printf '%s\n' "${value#v}"
}

go_pin() {
	line=$(grep -m 1 -E '^toolchain go[0-9]' "$root/go.mod") || fail "$root/go.mod has no toolchain line"
	printf '%s\n' "${line#toolchain go}"
}

# module_version <tool>: the command that prints the module line of a Go binary, for a tool whose first version line is not its own version.
module_version() { printf '%s\n' "go version -m \"\$(command -v $1)\" | grep -m 1 -E '^[[:space:]]+mod[[:space:]]'"; }

# tools prints one line per tool, split by tabs: the name, the command that prints its version, and the pin ("-" for none).
tools() {
	go_v=$(go_pin)
	lint_v=$(make_pin GOLANGCI_LINT_VERSION)
	rulez_v=$(make_pin AI_RULEZ_VERSION)
	gotestsum_v=$(make_pin GOTESTSUM_VERSION)
	gitleaks_v=$(make_pin GITLEAKS_VERSION)
	gremlins_v=$(make_pin GREMLINS_VERSION)
	uv_v=$(script_pin uv_version)
	actionlint_v=$(script_pin actionlint_version)
	printf '%s\t%s\t%s\n' \
		go 'go version' "$go_v" \
		golangci-lint 'golangci-lint version' "$lint_v" \
		ai-rulez 'ai-rulez --version' "$rulez_v" \
		gotestsum 'gotestsum --version' "$gotestsum_v" \
		gitleaks 'gitleaks version' "$gitleaks_v" \
		gremlins 'gremlins --version' "$gremlins_v" \
		go-test-coverage "$(module_version go-test-coverage)" - \
		govulncheck "$(module_version govulncheck)" - \
		deadcode "$(module_version deadcode)" - \
		dolt 'dolt version' - \
		bd 'bd --version' - \
		jq 'jq --version' - \
		uv 'uv --version' "$uv_v" \
		shellcheck 'shellcheck --version | grep -m 1 "^version:"' - \
		actionlint 'actionlint --version' "$actionlint_v" \
		bats 'bats --version' -
}

# in_unit <command>: run a command as a transient user unit, the way the ssh adapter runs a job, so it gets the PATH of the user manager.
# The unit reads no input: --pipe gives it the input of the caller, which is the tool list of prove_tools.
in_unit() {
	encoded=$(printf '%s' "$1" | base64 | tr -d '\n')
	systemd-run --user --wait --pipe --quiet --collect -p WorkingDirectory="$root" \
		sh -c "eval \"\$(printf %s $encoded | base64 -d)\"" </dev/null
}

# prove_tool <name> <command> <pin>: fail unless the tool runs in a user unit, at its pin when it has one. Print its record line.
prove_tool() {
	out=$(in_unit "$2" 2>&1) || fail "$1 does not run in a user unit: $(printf '%s\n' "$out" | head -n 1)"
	first=$(printf '%s\n' "$out" | head -n 1)
	if [ "$3" != - ] && ! printf '%s\n' "$first" | grep -Eo '[0-9]+(\.[0-9]+)+' | grep -qxF "$3"; then
		fail "$1 is not at its pin $3: a user unit found \"$first\". Run this again after the pin is installed"
	fi
	printf 'tool %s %s\n' "$1" "$first"
}

prove_tools() {
	pins=$(tools)
	tab=$(printf '\t')
	printf '%s\n' "$pins" | while IFS=$tab read -r name check pin; do
		prove_tool "$name" "$check" "$pin"
	done
}

systemd_version() { systemd-run --version | head -n 1 | cut -d ' ' -f 2; }

# check_up runs the reads of the adapter's Up, so a pass here means Up passes.
check_up() {
	[ "$(loginctl show-user "$user" -p Linger)" = Linger=yes ] || fail "linger is still off for $user"
	case $(systemctl --user is-system-running 2>/dev/null || true) in
	running | degraded) ;;
	*) fail 'the user manager does not run' ;;
	esac
	version=$(systemd_version)
	case $version in
	'' | *[!0-9]*) fail "systemd-run --version gave no version: $version" ;;
	esac
	[ "$version" -ge "$min_systemd" ] || fail "systemd $version is older than $min_systemd, which the ssh adapter needs"
	[ -d "$work_root" ] || fail "the work root $work_root does not exist"
}

# landlock prints "landlock pass", or "landlock warning <lsm list> <kernel>" when the kernel cannot sandbox an agent run.
landlock() {
	lsm=$(cat "$lsm_file" 2>/dev/null || echo none)
	kernel=$(uname -r)
	release=${kernel%%-*}
	major=${release%%.*}
	minor=${release#*.}
	minor=${minor%%.*}
	case ",$lsm," in
	*,landlock,*)
		if [ "$major" -gt "$landlock_major" ] || { [ "$major" -eq "$landlock_major" ] && [ "$minor" -ge "$landlock_minor" ]; }; then
			echo 'landlock pass'
			return 0
		fi
		;;
	esac
	printf 'landlock warning %s %s\n' "$lsm" "$kernel"
}

write_record() {
	record=$work_root/.beehave/host-setup.record
	{
		printf 'at=%s\n' "$(date -u +%Y-%m-%dT%H:%M:%SZ)"
		printf 'commit=%s\n' "$(git -C "$root" rev-parse HEAD)"
		printf 'kernel=%s\n' "$(uname -r)"
		printf 'systemd=%s\n' "$(systemd_version)"
		landlock
		printf 'watchdog %s %s\n' "$watchdog" "$(watchdog_state)"
		printf '%s\n' "$1"
	} >"$record.tmp"
	mv "$record.tmp" "$record"
	cat "$record"
}

read_args "$@"
refuse
check_watchdog
user=$(id -un)
sh "$root/scripts/host-tools.sh"
gobin=$(go_bin)
turn_on_linger
set_unit_path
make_work_root
install_project_tools
proof=$(prove_tools)
check_up
write_record "$proof"
check_watchdog
echo HOST-SETUP-DONE
