#!/bin/sh
# Make a linux amd64 machine ready as a beehave build host for the ssh provider, then prove it. See docs/design/hosts.md.
# Run it on the host, as the login user, from a clone of the repository at the commit under test. A second run changes nothing.
# It installs no watchdog: it refuses to start and to finish unless the named self-delete watchdog unit is active, enabled and Restart=always.
# It installs the metadata guard unit, which rejects every user except root on the TCP path to the instance metadata server, and it fails
# unless the login user then gets no answer from that server.
# It touches no heartbeat. It deletes nothing outside /usr/local/go, /usr/local/go.new, /usr/local/go.old and its own temporary folder.
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
env_file=$HOME/.config/environment.d/zz-beehave.conf
guard_unit=metadata-guard.service
guard_rules=$sysroot/etc/beehave/metadata-guard.nft
guard_unit_file=$sysroot/etc/systemd/system/$guard_unit
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
# It also refuses a single quote. The by-hand command of check_work_root_folder puts the path in single quotes, and a control
# character includes a newline, so the printed command runs nothing but the mkdir and the chown when the owner pastes it.
check_work_root() {
	case $work_root in
	'') fail "--work-root is required. $usage" ;;
	*[%\$]*) fail "the work root $work_root must not hold a percent or dollar sign, which systemd and the shell would expand" ;;
	*[[:cntrl:]]*) fail 'the work root must hold no control character' ;;
	*\'*) fail "the work root $work_root must not hold a single quote, which the command that the script prints for the owner of the parent cannot carry" ;;
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
# systemd reads the environment.d files in file name order and the last PATH line wins. The name zz- sorts after every file
# that has a number prefix, so this file is the last one on any distribution, unless a file with a letter prefix after zz sets PATH.
set_unit_path() {
	content="PATH=$go_root/bin:$gobin:\${PATH}
BEEHAVE_BD_132=$gobin/bd"
	if [ -f "$env_file" ] && [ "$(cat "$env_file")" = "$content" ]; then
		say "$env_file is up to date"
	else
		mkdir -p "$(dirname "$env_file")"
		printf '%s\n' "$content" >"$env_file.tmp"
		mv "$env_file.tmp" "$env_file"
		systemctl --user daemon-reload
		say "wrote $env_file"
	fi
	check_unit_path
}

# check_unit_path fails unless the PATH of the user manager starts with the Go folders, so every job unit finds the tools.
check_unit_path() {
	unit_path=$(systemctl --user show-environment | sed -n 's/^PATH=//p')
	case $unit_path in
	"$go_root/bin:$gobin:"*) ;;
	*) fail "the PATH of the user manager does not start with $go_root/bin:$gobin. An environment.d file that sorts after $(basename "$env_file") sets PATH. Find it with grep -l '^PATH=' in ~/.config/environment.d, /etc/environment.d, /run/environment.d, /usr/local/lib/environment.d and /usr/lib/environment.d, and remove its PATH line or rename it" ;;
	esac
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

# make_pin <variable>: print the version that a `<variable> := v<version>` line of the Makefile pins. A value `<module>@v<version>` pins the part after the @.
make_pin() {
	line=$(grep -m 1 -E "^$1 :=" "$root/Makefile") || fail "the Makefile has no $1 line"
	value=${line#*:= }
	value=${value##*@}
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

# module_version <tool>: the command that prints "<module> <version>" from the mod line of `go version -m`, for a tool that go install builds.
# A binary with no mod line prints "no mod line" and fails.
# The Go linker writes that line, so it names the pin of such a tool. The tool's own version text says "dev" or nothing.
module_version() {
	printf '%s\n' "go version -m \"\$(command -v $1)\" | awk -F '\\t' '\$2 == \"mod\" { print \$3, \$4; found = 1; exit } END { if (!found) { print \"no mod line\"; exit 1 } }'"
}

# tools prints one line per tool, split by tabs: the name, how its version is read (text: the first line of the command, mod: the mod line
# of the command), the command that prints its version, and the pin ("-" for none).
tools() {
	go_v=$(go_pin)
	lint_v=$(make_pin GOLANGCI_LINT_VERSION)
	rulez_v=$(make_pin AI_RULEZ_VERSION)
	gotestsum_v=$(make_pin GOTESTSUM_VERSION)
	gitleaks_v=$(make_pin GITLEAKS_VERSION)
	gremlins_v=$(make_pin GREMLINS_VERSION)
	coverage_v=$(make_pin GO_TEST_COVERAGE_VERSION)
	govulncheck_v=$(make_pin GOVULNCHECK_VERSION)
	deadcode_v=$(make_pin DEADCODE_VERSION)
	uv_v=$(script_pin uv_version)
	actionlint_v=$(script_pin actionlint_version)
	printf '%s\t%s\t%s\t%s\n' \
		go text 'go version' "$go_v" \
		golangci-lint mod "$(module_version golangci-lint)" "v$lint_v" \
		ai-rulez text 'ai-rulez --version' "$rulez_v" \
		gotestsum mod "$(module_version gotestsum)" "v$gotestsum_v" \
		gitleaks mod "$(module_version gitleaks)" "v$gitleaks_v" \
		gremlins mod "$(module_version gremlins)" "v$gremlins_v" \
		go-test-coverage mod "$(module_version go-test-coverage)" "v$coverage_v" \
		govulncheck mod "$(module_version govulncheck)" "v$govulncheck_v" \
		deadcode mod "$(module_version deadcode)" "v$deadcode_v" \
		dolt text 'dolt version' - \
		bd text 'bd --version' - \
		jq text 'jq --version' - \
		uv text 'uv --version' "$uv_v" \
		shellcheck text 'shellcheck --version | grep -m 1 "^version:"' - \
		actionlint mod "$(module_version actionlint)" "v$actionlint_v" \
		bats text 'bats --version' -
}

# in_unit <command>: run a command as a transient user unit, the way the ssh adapter runs a job, so it gets the PATH of the user manager.
# The unit reads no input: --pipe gives it the input of the caller, which is the tool list of prove_tools.
in_unit() {
	encoded=$(printf '%s' "$1" | base64 | tr -d '\n')
	systemd-run --user --wait --pipe --quiet --collect -p WorkingDirectory="$root" \
		sh -c "eval \"\$(printf %s $encoded | base64 -d)\"" </dev/null
}

# at_pin <kind> <first line> <pin>: succeed when the version that the first line shows is the pin. A mod line is "<module> <version>", and its
# version must equal the pin exactly, so a pseudo-version or (devel) is not the pin. A text line must hold the pin as a whole number.
at_pin() {
	case $1 in
	mod) [ "${2##* }" = "$3" ] ;;
	*) printf '%s\n' "$2" | grep -Eo '[0-9]+(\.[0-9]+)+' | grep -qxF "$3" ;;
	esac
}

# prove_tool <name> <kind> <command> <pin>: fail unless the tool runs in a user unit, at its pin when it has one. Print its record line.
prove_tool() {
	out=$(in_unit "$3" 2>&1) || fail "$1 does not run in a user unit: $(printf '%s\n' "$out" | head -n 1)"
	first=$(printf '%s\n' "$out" | head -n 1)
	if [ "$4" != - ] && ! at_pin "$2" "$first" "$4"; then
		fail "$1 is not at its pin ${4#v}: a user unit found \"$first\". Run this again after the pin is installed"
	fi
	printf 'tool %s %s\n' "$1" "$first"
}

prove_tools() {
	pins=$(tools)
	tab=$(printf '\t')
	printf '%s\n' "$pins" | while IFS=$tab read -r name kind check pin; do
		prove_tool "$name" "$kind" "$check" "$pin"
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

# guard_differs <source> <destination>: succeed when the destination is missing or differs from the source.
guard_differs() {
	[ ! -f "$2" ] || ! cmp -s "$1" "$2"
}

# install_guard installs the rules file and the unit that load it before the network and before any user session, as root, and starts it.
# A file that is already the same is not written again, and a unit that runs with the same files is not restarted.
install_guard() {
	changed=
	if guard_differs "$root/scripts/metadata-guard.nft" "$guard_rules"; then
		sudo install -D -m 0644 "$root/scripts/metadata-guard.nft" "$guard_rules"
		changed=1
	fi
	if guard_differs "$root/scripts/metadata-guard.service" "$guard_unit_file"; then
		sudo install -D -m 0644 "$root/scripts/metadata-guard.service" "$guard_unit_file"
		sudo systemctl daemon-reload
		changed=1
	fi
	sudo systemctl enable "$guard_unit"
	if [ -n "$changed" ] || ! systemctl is-active --quiet "$guard_unit"; then
		say "starting $guard_unit"
		sudo systemctl restart "$guard_unit"
	else
		say "$guard_unit is up to date"
	fi
}

# user_probe <url> and root_probe <url>: print the HTTP status that curl gets, or 000, and return the curl exit code.
# Any status, even 403, means the server answered.
user_probe() { curl -g -s -o /dev/null --max-time 5 -w '%{http_code}' "$1"; }
root_probe() { sudo curl -g -s -o /dev/null --max-time 5 -w '%{http_code}' "$1"; }

# check_guard fails unless the unit runs and is enabled, the rules are loaded, and the login user gets no answer from the metadata server over
# IPv4 or IPv6. A refused connection (curl 7) and a timeout (28) are no answer. Any other curl failure proves nothing, so it fails the script.
# It prints the record line, which holds the status that root gets, for a host that has a metadata server.
check_guard() {
	[ "$(systemctl is-active "$guard_unit" 2>/dev/null || true)" = active ] || fail "$guard_unit is not active"
	[ "$(systemctl is-enabled "$guard_unit" 2>/dev/null || true)" = enabled ] || fail "$guard_unit is not enabled, so a reboot would drop the rules"
	sudo nft list table inet beehave_metadata_guard >/dev/null || fail 'the metadata guard rules are not loaded'
	for url in http://169.254.169.254/ 'http://[fd20:ce::254]/'; do
		rc=0
		code=$(user_probe "$url") || rc=$?
		case $rc in
		7 | 28) ;;
		0) fail "the login user got HTTP $code from $url. A step of a gate can take the token of the machine. Read $guard_rules" ;;
		*) fail "the check of $url ended with curl exit code $rc, which proves nothing. Run curl -g --max-time 5 $url" ;;
		esac
	done
	code=$(root_probe http://169.254.169.254/) || true
	printf 'metadata user=refused root=%s\n' "$code"
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
		printf '%s\n' "$guard_line"
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
install_guard
gobin=$(go_bin)
turn_on_linger
set_unit_path
make_work_root
install_project_tools
proof=$(prove_tools)
check_up
guard_line=$(check_guard)
write_record "$proof"
check_watchdog
echo HOST-SETUP-DONE
