#!/bin/sh
# Run gremlins on the lines that a change touches, and fail if no mutant was applied.
#
# Usage: scripts/mutation-pr.sh BASE
#   BASE is the branch or commit that the pull request targets, for example origin/integration/r1.
#   There is no default, because the base differs from one pull request to the next.
#   MUTATION_TIMEOUT is the time limit for the gremlins run, a positive whole number of seconds. The default is 1200.
#   A run that exceeds the limit fails. The script needs git, go, gremlins, jq and timeout (or gtimeout) on the path.
#   The "not applicable" result needs git only.
#
# The script changes to the module root and runs gremlins on the path ".". Gremlins matches the lines of the
# diff to mutants by file path. The diff has paths relative to the module root. Gremlins gives mutants paths
# relative to the folder that it was started on. The two forms match only when that folder is the module
# root. With a package path, every mutant is "skipped", nothing runs, and the exit code is 0.
#
# "Applied" means killed + lived + timed out, counted from the JSON report of gremlins.
# The run fails when gremlins exits with a non-zero status, when applied is 0 with a mutant on a changed line,
# or when a mutant lived.
# A run with lived mutants prints two lines for each, at most 50 mutants: "finding: path:line: a mutant lived: TYPE"
# and, after the counts line, the marker line "mutation-pr: lived path:line TYPE". A summary line follows, with the
# full count and the number of finding lines printed.
# The end marker "mutation-pr: end count=N printed=P" is the last "mutation-pr:" line of every run that prints a result
# line ("not applicable", "no candidate" or the counts line). N is the number of lived mutants, and P is the number of
# marker lines. A run that stops before its result line is a tool error and prints no end marker.
# A diff with no Go source change prints "not applicable" and exits 0. A git diff that fails is an error.
# A diff whose changed non-test lines hold no operator that gremlins mutates prints "no candidate: <reason>" and
# exits 0, with no counts line. The script reads that from a report whose mutants are all skipped, and only after it
# proves that the report matches the diff: each file of the report exists, and the mutants that the report does not
# skip are exactly the mutants on the changed lines of the diff, built the way gremlins builds them. An empty report,
# or one that does not match, fails.
set -eu

if [ $# -ne 1 ] || [ -z "$1" ]; then
	echo "usage: scripts/mutation-pr.sh BASE   (BASE is the branch the pull request targets, for example origin/integration/r1)" >&2
	exit 2
fi
base=$1
limit=${MUTATION_TIMEOUT:-1200}
# Only a positive whole number: 0 would remove the bound, and a word would fail inside timeout as a gremlins exit code.
case $limit in
'' | *[!0-9]* | 0*)
	echo "mutation-pr: MUTATION_TIMEOUT is \"$limit\". Set it to a positive whole number of seconds, such as 1200."
	exit 2
	;;
esac

# The end marker of a run with no lived mutant. A reader of the job log takes a log with no end marker as cut.
none_lived='mutation-pr: end count=0 printed=0'

root=$(cd "$(dirname "$0")/.." && pwd)
cd "$root"

git rev-parse --verify --quiet "$base^{commit}" >/dev/null || { echo "mutation-pr: $base is not a commit or branch"; exit 1; }
git merge-base "$base" HEAD >/dev/null 2>&1 || { echo "mutation-pr: no merge base between $base and HEAD (shallow clone?)"; exit 1; }

# An empty diff makes gremlins treat every line as changed and mutate the whole tree. Stop before that.
# The checks up to "not applicable" need git only, so a change with no Go source change needs no other tool.
changed=$(git diff --merge-base "$base" --name-only -- '*.go' ':!*_test.go') || { echo "mutation-pr: git diff against $base failed"; exit 1; }
if [ -z "$changed" ]; then
	echo "mutation-pr: not applicable: no Go source change against $base"
	echo "$none_lived"
	exit 0
fi

# gremlins names each mutant by its path relative to the folder that it starts on, and runs "git diff" in that
# folder, whose paths are relative to the top of the repository. The two match only when this folder is both the
# top of the repository and the module root. Check both, so that a path mismatch cannot occur.
[ -z "$(git rev-parse --show-prefix)" ] || { echo "mutation-pr: $root is not the top folder of its git repository"; exit 1; }
[ -f go.mod ] || { echo "mutation-pr: $root has no go.mod, so it is not the module root"; exit 1; }

command -v gremlins >/dev/null 2>&1 || { echo "mutation-pr: gremlins is not on the path"; exit 1; }
command -v jq >/dev/null 2>&1 || { echo "mutation-pr: jq is not on the path"; exit 1; }
if command -v timeout >/dev/null 2>&1; then
	limiter=timeout
elif command -v gtimeout >/dev/null 2>&1; then
	limiter=gtimeout
else
	echo "mutation-pr: neither timeout nor gtimeout is on the path"
	exit 1
fi

report=$(mktemp "${TMPDIR:-/tmp}/mutation-pr.XXXXXX")
trap 'rm -f "$report"' EXIT INT TERM

# gremlins runs its own "git diff" and drops the first part of each path, which it expects to be "a/" or "b/".
# With diff.noprefix that part is a folder of the path, and gremlins skips the mutants of each file in a subfolder.
# So give its git the two settings that keep "a/" and "b/", the prefixes that the diff of this script also has.
# They go after the settings that the environment already holds, and git lets the last one win.
git_config_count=${GIT_CONFIG_COUNT:-0}
export "GIT_CONFIG_KEY_$git_config_count=diff.noprefix" "GIT_CONFIG_VALUE_$git_config_count=false"
export "GIT_CONFIG_KEY_$((git_config_count + 1))=diff.mnemonicPrefix" "GIT_CONFIG_VALUE_$((git_config_count + 1))=false"
export GIT_CONFIG_COUNT=$((git_config_count + 2))

status=0
"$limiter" -k 30 "$limit" gremlins unleash --diff "$base" --output "$report" . || status=$?
if [ "$status" -eq 124 ] || [ "$status" -eq 137 ]; then
	echo "mutation-pr: the run exceeded MUTATION_TIMEOUT=${limit}s"
	exit 1
fi
if [ "$status" -ne 0 ]; then
	echo "mutation-pr: gremlins exited with status $status"
	exit "$status"
fi

# gremlins writes no report when it finds no mutant in the module, on a changed line or not.
[ -s "$report" ] || { echo "mutation-pr: gremlins wrote no report: it found no mutant in the module"; exit 1; }

# The report lists every mutant of the module, and marks "SKIPPED" each one that is not on a changed line.
# A path mismatch also marks every mutant skipped, so the report must prove its paths: each file that it names
# must be a file under the module root. Only then does "all skipped" mean "no candidate".
names=$(jq -r '.files[]?.file_name' "$report") || { echo "mutation-pr: the gremlins report is not readable"; exit 1; }
unknown=$(printf '%s\n' "$names" | while IFS= read -r name; do [ -z "$name" ] || [ -f "$name" ] || printf '%s ' "$name"; done)
if [ -n "$unknown" ]; then
	echo "mutation-pr: the gremlins report names files that are not under $root: ${unknown% }. Its paths do not match the diff."
	exit 1
fi

# The changed lines of each file, one "path start count" line for each hunk, built the way gremlins builds them
# from its own diff ("git diff --merge-base BASE", with context lines). For each hunk, the range starts at the
# first line after the leading context lines, and it holds one line for each added line. Each header gives the
# number of old and new lines that follow it, so the loop reads those lines as the hunk and never as a header.
# A "\ No newline" line is not counted in a header. Like the diff of gremlins, this diff finds renames and covers
# every file. The fixed prefixes keep the "+++ b/" line when diff.noprefix or diff.mnemonicPrefix is set.
diff=$(git diff --merge-base "$base" --no-color --no-ext-diff --src-prefix=a/ --dst-prefix=b/) || { echo "mutation-pr: git diff against $base failed"; exit 1; }
ranges=$(printf '%s\n' "$diff" | {
	file=
	oldleft=0
	newleft=0
	while IFS= read -r line; do
		# Each case pattern starts with "(": a command substitution ends at a bare ")" in the sh of macOS.
		case $line in ('\'*) continue ;; esac
		if [ "$oldleft" -gt 0 ] || [ "$newleft" -gt 0 ]; then
			case $line in
			('+'*) newleft=$((newleft - 1)) added=$((added + 1)) seen=1 ;;
			('-'*) oldleft=$((oldleft - 1)) seen=1 ;;
			(*)
				oldleft=$((oldleft - 1)) newleft=$((newleft - 1))
				[ "$seen" -eq 1 ] || lead=$((lead + 1))
				;;
			esac
			if [ "$oldleft" -le 0 ] && [ "$newleft" -le 0 ] && [ -n "$file" ] && [ "$added" -gt 0 ]; then
				printf '%s %s %s\n' "$file" $((start + lead)) "$added"
			fi
			continue
		fi
		case $line in
		('+++ b/'*) file=${line#'+++ b/'} ;;
		('+++ '*) file= ;;
		('@@ '*)
			old=${line#'@@ -'}
			old=${old%% *}
			new=${line#'@@ -'*' +'}
			new=${new%% *}
			case $old in (*,*) oldleft=${old#*,} ;; (*) oldleft=1 ;; esac
			case $new in (*,*) newleft=${new#*,} start=${new%,*} ;; (*) newleft=1 start=$new ;; esac
			lead=0 added=0 seen=0
			;;
		esac
	done
})

# gremlins skips each mutant that is not on a changed line of its own diff. A skipped mutant on a changed line
# of this diff, or a mutant off those lines that gremlins did not skip, means that the report does not match the diff.
mismatch=$(jq -r --arg ranges "$ranges" '
	($ranges | split("\n") | map(select(. != "") | split(" ") | {file: .[0], start: (.[1] | tonumber), count: (.[2] | tonumber)})) as $hunks
	| [.files[]? | .file_name as $file | .mutations[]? | (.line as $line | any($hunks[]; .file == $file and $line >= .start and $line < .start + .count)) as $changed
		| select(($changed and .status == "SKIPPED") or ((($changed) | not) and .status != "SKIPPED")) | "\($file):\(.line)"]
	| unique | join(" ")' "$report") || { echo "mutation-pr: the gremlins report is not readable"; exit 1; }
if [ -n "$mismatch" ]; then
	echo "mutation-pr: the gremlins report does not match the diff against $base: it skips a mutant on a changed line, or keeps one off the changed lines, at $mismatch"
	exit 1
fi

counts=$(jq -r '[.files[]?.mutations[]?.status] | [length, (map(select(. != "SKIPPED")) | length), (map(select(. == "KILLED")) | length), (map(select(. == "LIVED")) | length), (map(select(. == "TIMED OUT")) | length)] | @tsv' "$report") || { echo "mutation-pr: the gremlins report is not readable"; exit 1; }
total=$(echo "$counts" | cut -f1)
candidates=$(echo "$counts" | cut -f2)
killed=$(echo "$counts" | cut -f3)
lived=$(echo "$counts" | cut -f4)
timed=$(echo "$counts" | cut -f5)
applied=$((killed + lived + timed))

# "No candidate" is the only result line of its run. A reader of the job log takes a counts line with applied=0 as a
# failed run, so this path prints no counts line.
if [ "$total" -gt 0 ] && [ "$candidates" -eq 0 ]; then
	echo "mutation-pr: no candidate: no changed line of $(printf '%s' "$changed" | tr '\n' ' ') holds an operator that gremlins mutates; the report marks all $total mutant(s) of the module skipped"
	echo "$none_lived"
	exit 0
fi
echo "mutation-pr: applied=$applied killed=$killed lived=$lived timed_out=$timed"

if [ "$total" -eq 0 ]; then
	echo "mutation-pr: the gremlins report holds no mutant"
	echo "$none_lived"
	exit 1
fi

if [ "$applied" -eq 0 ]; then
	echo "mutation-pr: no mutant was applied. Check that the diff against $base touches covered Go code."
	echo "$none_lived"
	exit 1
fi
if [ "$lived" -eq 0 ]; then
	echo "$none_lived"
	exit 0
fi

# Each lived mutant, at most 50, gives two lines. The remediation workflow keeps the lines with the prefix "finding: ",
# as "finding: path:line: text". The engine step reads the marker lines, as "mutation-pr: lived path:line TYPE".
# The check above proved that every mutant that gremlins did not skip is on a changed line, so each lived mutant is in the diff.
jq -r '[.files[]? | .file_name as $file | .mutations[]? | select(.status == "LIVED") | {at: "\($file):\(.line)", type}] | .[:50]
	| (.[] | "finding: \(.at): a mutant lived: \(.type)"), (.[] | "mutation-pr: lived \(.at) \(.type)")' "$report" || { echo "mutation-pr: the gremlins report is not readable"; exit 1; }
printed=$lived
[ "$printed" -le 50 ] || printed=50
echo "mutation-pr: $lived mutant(s) lived, $printed finding line(s) printed. Kill each one with a test."
echo "mutation-pr: end count=$lived printed=$printed"
exit 1
