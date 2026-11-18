#!/bin/sh
# Run one test binary for "go test -exec" and record its exit code for scripts/mutation-pr.sh.
#
# Usage: GOFLAGS=-exec=/path/to/mutation-exec.sh go test ...   (go test calls: mutation-exec.sh TESTBINARY ARGS...)
#   MUTATION_ROOT is the module root that gremlins copies. MUTATION_EXIT_LOG is the file that receives the records.
#   Without both variables, the script only runs the test binary.
#
# Gremlins runs "go test" in a copy of the module, with one mutant applied, and keeps only its status: LIVED covers
# exit 0, a kill by a signal and every exit code other than 1 and 2. This script sees the exit code of the test binary.
# It finds the mutated file by comparing the files of the package in the copy with the files of MUTATION_ROOT, and it
# appends one record for each differing file: path, line, exit code and first failing line, separated by tabs.
# A test run in the module itself (the coverage run of gremlins) differs from nothing and leaves no record.
# The script exits with the exit code of the test binary, and prints the output of the test binary unchanged.
set -u

if [ -z "${MUTATION_ROOT:-}" ] || [ -z "${MUTATION_EXIT_LOG:-}" ]; then
	exec "$@"
fi

out="$MUTATION_EXIT_LOG.out.$$"
trap 'rm -f "$out"' EXIT INT TERM

rc=0
"$@" >"$out" 2>&1 || rc=$?
cat "$out"

# The first line of the output that names a failure. A run that fails with none of those words gives its first line.
text=
if [ "$rc" -ne 0 ]; then
	text=$(grep -m 1 -E '^[[:space:]]*(--- FAIL|panic:|fatal error:|FAIL)' "$out" || true)
	[ -n "$text" ] || text=$(head -n 1 "$out")
	text=$(printf '%s' "$text" | tr '\t\n' '  ')
fi

# The copy of the module is the nearest folder above the package that holds a go.mod.
copy=$PWD
while [ ! -f "$copy/go.mod" ] && [ "$copy" != / ]; do
	copy=$(dirname "$copy")
done
rel=${PWD#"$copy"}
rel=${rel#/}
original=$MUTATION_ROOT
[ -z "$rel" ] || original=$MUTATION_ROOT/$rel

for file in "$PWD"/*.go; do
	name=${file##*/}
	[ -f "$original/$name" ] || continue
	header=$(diff "$original/$name" "$file" | head -n 1)
	[ -n "$header" ] || continue
	# The header of a change reads "12c12" or "12,13c12,13". The line is the number before the first letter or comma.
	line=${header%%[acd,]*}
	path=$name
	[ -z "$rel" ] || path=$rel/$name
	printf '%s\t%s\t%s\t%s\n' "$path" "$line" "$rc" "$text" >>"$MUTATION_EXIT_LOG"
done

exit "$rc"
