package scripts_test

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"

	"github.com/stretchr/testify/require"
)

// Each test copies mutation-pr.sh into <tmp>/scripts/ of a temporary git repository with a base commit and a head commit,
// because the script runs git diff in the folder above its own. Only gremlins is fake: a script on the path that writes a
// canned report and exits with a set code. The tests run the real jq and the real timeout.

const fakeGremlins = `#!/bin/sh
echo called > "$FAKE_CALLED"
out=""
while [ $# -gt 0 ]; do
	if [ "$1" = "--output" ]; then out=$2; fi
	shift
done
cp "$FAKE_REPORT" "$out"
exit "${FAKE_STATUS:-0}"
`

// mutationRun is what one run of the script left.
type mutationRun struct {
	output string
	exit   int
	called bool
}

// lines returns the lines that the script printed with its prefix, in order.
func (r mutationRun) lines() []string {
	var found []string

	for line := range strings.SplitSeq(r.output, "\n") {
		if strings.HasPrefix(line, "mutation-pr: ") {
			found = append(found, line)
		}
	}

	return found
}

// last returns the last line with the prefix.
func (r mutationRun) last(t *testing.T) string {
	t.Helper()

	lines := r.lines()
	require.NotEmpty(t, lines, r.output)

	return lines[len(lines)-1]
}

// write puts a file under root, in a folder that it makes first.
func write(t *testing.T, root *os.Root, name, text string) {
	t.Helper()

	if dir := filepath.Dir(name); dir != "." {
		require.NoError(t, root.MkdirAll(dir, 0o750))
	}

	require.NoError(t, root.WriteFile(name, []byte(text), 0o600))
}

// writeProgram writes a program that the script starts. Linux refuses to start a file that a process holds open for writing, and a child that another
// test forks while the file is open keeps the descriptor until its own exec. Fork holds the write side of syscall.ForkLock, so the read side that this
// function holds from the open to the close keeps every fork out of that span.
func writeProgram(t *testing.T, dir, name, text string) {
	t.Helper()

	root, err := os.OpenRoot(dir)
	require.NoError(t, err)

	defer func() { require.NoError(t, root.Close()) }()

	syscall.ForkLock.RLock()
	defer syscall.ForkLock.RUnlock()

	file, err := root.OpenFile(name, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o700)
	require.NoError(t, err)

	_, err = file.WriteString(text)
	require.NoError(t, err)
	require.NoError(t, file.Close())
}

// inRepo runs a command that the caller built from constants, in the repository folder.
func inRepo(t *testing.T, repo string, command *exec.Cmd) {
	t.Helper()

	command.Dir = repo
	out, err := command.CombinedOutput()
	require.NoError(t, err, string(out))
}

// toolDirs is the folders of the tools that the script runs, besides the system folders. Only gremlins is fake.
func toolDirs(t *testing.T) string {
	t.Helper()

	dirs := []string{"/usr/bin", "/bin"}

	for _, tool := range []string{"git", "jq", "timeout"} {
		path, err := exec.LookPath(tool)
		require.NoError(t, err)

		dirs = append(dirs, filepath.Dir(path))
	}

	return strings.Join(dirs, string(os.PathListSeparator))
}

// runScript builds a repository whose base commit holds the base files and whose head commit holds the head files, then runs the script on the base.
// report is the canned gremlins report, and status is the exit code of the fake gremlins.
func runScript(t *testing.T, base, head map[string]string, report string, status int) mutationRun {
	t.Helper()

	repo := t.TempDir()
	root, err := os.OpenRoot(repo)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, root.Close()) })

	script, err := os.ReadFile("mutation-pr.sh")
	require.NoError(t, err)
	write(t, root, filepath.Join("scripts", "mutation-pr.sh"), string(script))
	inRepo(t, repo, exec.CommandContext(t.Context(), "git", "init", "-q", "-b", "main"))

	for name, text := range base {
		write(t, root, name, text)
	}

	inRepo(t, repo, exec.CommandContext(t.Context(), "git", "add", "."))
	inRepo(t, repo, exec.CommandContext(t.Context(), "git", "-c", "user.name=t", "-c", "user.email=t@example.com", "-c", "commit.gpgsign=false", "commit", "-q", "-m", "base"))
	inRepo(t, repo, exec.CommandContext(t.Context(), "git", "tag", "base"))

	for name, text := range head {
		write(t, root, name, text)
	}

	inRepo(t, repo, exec.CommandContext(t.Context(), "git", "add", "."))
	inRepo(t, repo, exec.CommandContext(t.Context(), "git", "-c", "user.name=t", "-c", "user.email=t@example.com", "-c", "commit.gpgsign=false", "commit", "-q", "-m", "head"))

	bin := t.TempDir()
	writeProgram(t, bin, "gremlins", fakeGremlins)
	write(t, root, "report.json", report)

	called := filepath.Join(repo, "called")

	command := exec.CommandContext(t.Context(), "sh", "scripts/mutation-pr.sh", "base")
	command.Dir = repo
	command.Env = []string{
		"PATH=" + bin + string(os.PathListSeparator) + toolDirs(t), "HOME=" + repo,
		"FAKE_REPORT=" + filepath.Join(repo, "report.json"), "FAKE_CALLED=" + called, fmt.Sprintf("FAKE_STATUS=%d", status),
	}
	out, err := command.CombinedOutput()

	run := mutationRun{output: string(out)}

	var exitError *exec.ExitError
	if errors.As(err, &exitError) {
		run.exit = exitError.ExitCode()
	} else {
		require.NoError(t, err, run.output)
	}

	_, statErr := os.Stat(called)
	run.called = statErr == nil

	return run
}

// reportOf builds a gremlins report with the given mutants of one file: each is a status, a line and a mutator.
func reportOf(t *testing.T, file string, mutants ...[3]any) string {
	t.Helper()

	var mutations []map[string]any
	for _, mutant := range mutants {
		mutations = append(mutations, map[string]any{"status": mutant[0], "line": mutant[1], "column": 1, "type": mutant[2]})
	}

	data, err := json.Marshal(map[string]any{"files": []any{map[string]any{"file_name": file, "mutations": mutations}}})
	require.NoError(t, err)

	return string(data)
}

const (
	baseSource = "package a\n\nfunc add(a, b int) int {\n\treturn a\n}\n"
	opSource   = "package a\n\nfunc add(a, b int) int {\n\treturn a + b\n}\n"
	noOpSource = "package a\n\nfunc add(a, b int) int {\n\tx := call(a)\n\treturn x\n}\n"
)

func TestMutationPR_AChangeWithNoMutableOperatorIsNoCandidateAndRunsNoTool(t *testing.T) {
	t.Parallel()

	run := runScript(t, map[string]string{"a.go": baseSource}, map[string]string{"a.go": noOpSource}, `{"files":[]}`, 1)

	require.Equal(t, 0, run.exit, run.output)
	require.False(t, run.called, "the script decides no candidate from the diff, not from a report")
	require.Contains(t, run.output, "mutation-pr: no candidate: ")
	require.Equal(t, "mutation-pr: end count=0 printed=0", run.last(t))
}

func TestMutationPR_ADiffWithNoGoSourcePrintsNotApplicableThenTheEndMarker(t *testing.T) {
	t.Parallel()

	run := runScript(t, map[string]string{"a.go": baseSource, "README.md": "a\n"}, map[string]string{"README.md": "b\n"}, `{"files":[]}`, 1)

	require.Equal(t, 0, run.exit, run.output)
	require.Contains(t, run.output, "mutation-pr: not applicable: ")
	require.Equal(t, "mutation-pr: end count=0 printed=0", run.last(t))
}

func TestMutationPR_AMutableOperatorWithAReportOfNoMutantStillFails(t *testing.T) {
	t.Parallel()

	run := runScript(t, map[string]string{"a.go": baseSource}, map[string]string{"a.go": opSource}, `{"files":[]}`, 0)

	require.NotEqual(t, 0, run.exit, run.output)
	require.True(t, run.called)
	require.NotContains(t, run.output, "no candidate")
	require.Contains(t, run.output, "mutation-pr: applied=0 killed=0 lived=0 timed_out=0")
	require.Equal(t, "mutation-pr: end count=0 printed=0", run.last(t))
}

func TestMutationPR_OneLivedMutantPrintsItsLineAndTheEndMarkerWithCountOne(t *testing.T) {
	t.Parallel()

	report := reportOf(t, "a.go", [3]any{"LIVED", 4, "ARITHMETIC_BASE"}, [3]any{"KILLED", 4, "CONDITIONALS_NEGATION"})
	run := runScript(t, map[string]string{"a.go": baseSource}, map[string]string{"a.go": opSource}, report, 0)

	require.NotEqual(t, 0, run.exit, run.output)

	lines := run.lines()
	require.Equal(t, "mutation-pr: applied=2 killed=1 lived=1 timed_out=0", lines[0])
	require.Contains(t, lines, "mutation-pr: lived a.go:4 ARITHMETIC_BASE")
	require.Equal(t, "mutation-pr: end count=1 printed=1", lines[len(lines)-1])
}

func TestMutationPR_MoreThanFiftyLivedMutantsPrintFiftyLinesAndTheFullCount(t *testing.T) {
	t.Parallel()

	var mutants [][3]any
	for i := range 60 {
		mutants = append(mutants, [3]any{"LIVED", i + 1, "ARITHMETIC_BASE"})
	}

	run := runScript(t, map[string]string{"a.go": baseSource}, map[string]string{"a.go": opSource}, reportOf(t, "a.go", mutants...), 0)

	require.NotEqual(t, 0, run.exit, run.output)

	printed := 0

	for _, line := range run.lines() {
		if strings.HasPrefix(line, "mutation-pr: lived ") && !strings.Contains(line, "mutant(s)") {
			printed++
		}
	}

	require.Equal(t, 50, printed)
	require.Equal(t, "mutation-pr: end count=60 printed=50", run.last(t))
}

func TestMutationPR_ARunWithNoLivedMutantPrintsTheEndMarkerWithCountZero(t *testing.T) {
	t.Parallel()

	run := runScript(t, map[string]string{"a.go": baseSource}, map[string]string{"a.go": opSource}, reportOf(t, "a.go", [3]any{"KILLED", 4, "ARITHMETIC_BASE"}), 0)

	require.Equal(t, 0, run.exit, run.output)
	require.Equal(t, "mutation-pr: end count=0 printed=0", run.last(t))
}
