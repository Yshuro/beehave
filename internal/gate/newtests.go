package gate

import (
	"strconv"
	"strings"

	"github.com/tobocop2/beehave/internal/model"
)

const (
	// markerCap is the most name lines and the most marker lines that the job of a candidate prints. The end marker carries the full counts.
	markerCap = 50

	// baseCache is the folder, below the work root of the host, that holds one parsed base report for each base commit.
	baseCache = "beehave-base-reports"

	// newTestMarker starts the line that names one new test: the class and the name follow.
	newTestMarker = "beehave-new-test "

	// failedTestMarker starts the line of a new test that failed or was skipped on the head: the kind, the class and the name follow.
	failedTestMarker = "beehave-new-test-failed "

	// newTestsEnd starts the end marker of the new-tests check: "new=<count> failed=<count>".
	newTestsEnd = "beehave-new-tests-end "

	// newTestsError starts the line that says why the new-tests check could not compare.
	newTestsError = "beehave-new-tests-error "

	// badNameExit is the exit code of junitAwk for a report with a test name or class name that holds a tab or a newline. The tab splits the fields
	// of the parsed report and the newline splits its lines, so such a name cannot be compared. The job then writes an error line and no end marker, and a cache keeps no report that holds such a name.
	badNameExit = "3"

	// The names of the steps that the job of a candidate runs apart. Each writes a stepMarker line.
	stepHeadGate  = model.StepHeadGate
	stepSecrets   = model.StepSecrets
	stepBaseSuite = model.StepBaseSuite
)

// junitAwk reads the JUnit files that its arguments name and prints one line for each test case: the class, the name and the result, split by tabs.
// The result is pass, fail or skipped. A test case is keyed by class and name, so a subtest is its own test. The reader is awk only: each
// record is one tag, which the record separator "<" makes, and a skipped or failure tag belongs to the test case that is open.
const junitAwk = `BEGIN { RS = "<" }
function attr(s, k) {
	if (match(s, "[ \t\n]" k "=\"[^\"]*\"")) {
		return substr(s, RSTART + length(k) + 3, RLENGTH - length(k) - 4)
	}
	return ""
}
function emit() {
	if (index(cn nm, "\t") || index(cn nm, "\n")) bad = 1; else print cn "\t" nm "\t" st
	open = 0
}
/^testcase[ \t\n]/ {
	if (open) emit()
	cn = attr($0, "classname"); nm = attr($0, "name"); st = "pass"; open = 1
	if ($0 ~ /\/>[ \t\r\n]*$/) emit()
	next
}
/^skipped[ \t\n\/>]/ { if (open) st = "skipped"; next }
/^(failure|error)[ \t\n\/>]/ { if (open) st = "fail"; next }
/^\/testcase/ { if (open) emit() }
END { if (bad) exit ` + badNameExit + ` }`

// compareAwk reads the parsed base report, then the parsed head report. It prints a name line for each test of the head that the base lacks,
// a marker line for each of those that did not pass, and the end marker. Both kinds of line stop at markerCap, and the end marker counts them all.
const compareAwk = `FNR == NR { seen[$1 "\t" $2] = 1; next }
{
	if (($1 "\t" $2) in seen) next
	n++
	if (n <= cap) print "` + newTestMarker + `" $1 " " $2
	if ($3 != "pass") { f++; if (f <= cap) print "` + failedTestMarker + `" $3 " " $1 " " $2 }
}
END { print "` + newTestsEnd + `new=" n + 0 " failed=" f + 0 }`

// candidateWord is the word of the job of a candidate head, after the checkout. It runs the head gate, the secret scan and the base suite apart,
// each in its own subshell, and a failed step never stops a later one. Then it prints the new-tests check, and last the step marker lines.
// The job exits with the exit code of the head gate. A project with no test_command runs no base suite, so only a cached base report can serve.
func candidateWord(project model.Project, pins []Pin, base, clone string) string {
	cache := model.ShellQuote(filepathDir(clone) + "/" + baseCache)
	glob := globWord(project.TestReport)
	before, after := reportChecks(project.TestReport)
	headGate := strings.Join(append(append(before, gateWords(project, pins)...), after...), " && ")

	parts := []string{
		`mb=$(git merge-base HEAD ` + model.ShellQuote("origin/"+base) + ` 2>/dev/null)`,
		stepWord("headrc", "( "+headGate+" )"),
		stepWord("secretrc", `( [ -n "$mb" ] && `+limited(project, `gitleaks git --no-banner --log-opts="$mb..HEAD" .`)+` )`),
		"cache=" + cache + `; mkdir -p "$cache"`,
		baseSuite(project, glob),
		"{ " + `awk ` + model.ShellQuote(junitAwk) + ` ` + glob + ` > ../head.tsv; jrc=$?; }`,
		`if [ "$jrc" -eq ` + badNameExit + ` ]; then echo "` + newTestsError + `a test name in the head report holds a tab or a newline, so the new tests cannot be named"; ` +
			`elif [ -n "$mb" ] && [ -s "$cache/$mb" ]; then awk -F '\t' -v cap=` + strconv.Itoa(markerCap) + ` ` + model.ShellQuote(compareAwk) + ` "$cache/$mb" ../head.tsv; ` +
			`else echo "` + newTestsError + `no base report for $mb"; fi`,
		closingMarkers(),
		`[ "$headrc" -eq 0 ]`,
	}

	return "{ " + strings.Join(parts, "; ") + "; }"
}

// filepathDir is the folder that holds the clone folder: the work root of the host.
func filepathDir(clone string) string {
	return clone[:strings.LastIndex(clone, "/")]
}

// stepWord runs command and keeps its exit code in the shell variable rcVar. It writes no marker line: a step prints any amount of output, and
// the host reads only the tail of the log, so closingMarkers writes every marker after all other output.
func stepWord(rcVar, command string) string {
	return command + `; ` + rcVar + `=$?; echo`
}

// closingMarkers is the word that writes the step marker lines, after the new-tests check and the end marker, so they are the last lines of the log
// whatever the steps printed. The base suite step writes a line only when it ran: a job that read the cache has no base-suite code.
func closingMarkers() string {
	line := func(name, rcVar string) string { return `echo "` + stepMarker + " " + name + ` rc=$` + rcVar + `"` }

	return `echo; ` + line(stepHeadGate, "headrc") + `; ` + line(stepSecrets, "secretrc") + `; ` +
		`[ -z "${baserc+x}" ] || ` + line(stepBaseSuite, "baserc")
}

// baseSuite is the word that runs the test command on the merge base when the cache holds no report for it, parses the report that the run left,
// and stores it under a temporary name that it renames to the SHA name, so no reader sees a partial file. A red suite still leaves its report.
func baseSuite(project model.Project, glob string) string {
	if project.TestCommand == "" {
		return ":"
	}

	run := `( git worktree add --detach ../base "$mb" >/dev/null && cd ../base && ` + limited(project, "sh -c "+model.ShellQuote(project.TestCommand)) + ` )`
	store := `if ( cd ../base && awk ` + model.ShellQuote(junitAwk) + ` ` + glob + ` ) > "$cache/.tmp.$mb.$$" && [ -s "$cache/.tmp.$mb.$$" ]; then mv "$cache/.tmp.$mb.$$" "$cache/$mb"; fi`

	return `if [ -n "$mb" ] && [ ! -s "$cache/$mb" ]; then ` + stepWord("baserc", run) + `; ` + store + `; fi`
}

// Checks is what the engine reads from the log of a candidate job that passed: the new tests, the ones that failed a check, the exit code of
// the secret scan, and whether the end marker was there. NewTests and Failed are capped, and New and FailedCount are the full counts.
type Checks struct {
	NewTests    []string `json:"new_tests,omitempty"`
	Failed      []string `json:"failed,omitempty"`
	New         int      `json:"new"`
	FailedCount int      `json:"failed_count"`
	// Secrets is the exit code of the secret scan, or nil when the log has no single marker line for it.
	Secrets *int `json:"secrets_rc,omitempty"`
	// Complete is true when the log holds the end marker. A log with none was cut, or the job could not compare.
	Complete bool   `json:"complete"`
	Problem  string `json:"problem,omitempty"`
}

// ReadChecks reads the lines of the new-tests check from the log of a candidate job. The end marker is the last line with its prefix, and the
// name lines and marker lines are the run of such lines just above it, so a test that prints the prefix earlier changes nothing.
func ReadChecks(log string) Checks {
	lines := strings.Split(log, "\n")
	checks := Checks{}

	if codes := markerCodes(log)[stepSecrets]; len(codes) == 1 {
		code := atoiOr(codes[0], 1)
		checks.Secrets = &code
	}

	end := endMarker(lines, &checks)
	if end < 0 {
		return checks
	}

	checks.Complete = true
	checks.New, checks.FailedCount = readCounts(strings.TrimPrefix(lines[end], newTestsEnd))
	readRun(lines[:end], &checks)

	return checks
}

// readRun reads the name lines and marker lines in the run of such lines that ends the lines.
func readRun(lines []string, checks *Checks) {
	for i := len(lines) - 1; i >= 0; i-- {
		if name, found := strings.CutPrefix(lines[i], newTestMarker); found {
			checks.NewTests = append([]string{name}, checks.NewTests...)
		} else if reason, found := strings.CutPrefix(lines[i], failedTestMarker); found {
			checks.Failed = append([]string{reason}, checks.Failed...)
		} else {
			return
		}
	}
}

// endMarker is the index of the last end marker line, or -1. It also records the last error line of the job in checks.
func endMarker(lines []string, checks *Checks) int {
	end := -1

	for i, line := range lines {
		if strings.HasPrefix(line, newTestsEnd) {
			end = i
		}

		if reason, found := strings.CutPrefix(line, newTestsError); found {
			checks.Problem = reason
		}
	}

	return end
}

// readCounts reads "new=<n> failed=<n>". A part that does not read gives 0.
func readCounts(text string) (newTests, failed int) {
	for _, field := range strings.Fields(text) {
		key, value, _ := strings.Cut(field, "=")

		switch key {
		case "new":
			newTests = atoiOr(value, 0)
		case "failed":
			failed = atoiOr(value, 0)
		}
	}

	return newTests, failed
}
