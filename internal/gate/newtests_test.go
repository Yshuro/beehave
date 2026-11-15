package gate

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/tobocop2/beehave/internal/model"
)

const reportScript = `#!/bin/sh
mkdir -p reports
{ echo '<testsuites><testsuite>'
  while read -r kind cls name; do
    case $kind in
      pass) echo "<testcase classname=\"$cls\" name=\"$name\" time=\"0\"></testcase>";;
      skip) echo "<testcase classname=\"$cls\" name=\"$name\"><skipped message=\"s\"/></testcase>";;
      fail) echo "<testcase classname=\"$cls\" name=\"$name\"><failure message=\"f\">x</failure></testcase>";;
    esac
  done < tests.txt
  echo '</testsuite></testsuites>'; } > reports/j.xml
`

// shellRepo makes a repository on main with the given tests, then a branch with the head tests. It returns the repository and the head commit.
func shellRepo(t *testing.T, baseTests, headTests string) (repo, head string) {
	t.Helper()

	repo = t.TempDir()
	git := func(args ...string) string {
		out, err := exec.CommandContext(t.Context(), "git", append([]string{"-C", repo, "-c", "user.name=t", "-c", "user.email=t@t"}, args...)...).CombinedOutput() //nolint:gosec // test helper
		require.NoError(t, err, string(out))

		return strings.TrimSpace(string(out))
	}

	require.NoError(t, os.WriteFile(filepath.Join(repo, "mkreport.sh"), []byte(reportScript), 0o600))
	require.NoError(t, os.WriteFile(filepath.Join(repo, "tests.txt"), []byte(baseTests), 0o600))
	git("init", "-q", "-b", "main")
	git("add", "mkreport.sh", "tests.txt")
	git("commit", "-q", "-m", "base")
	git("checkout", "-q", "-b", "feature")
	require.NoError(t, os.WriteFile(filepath.Join(repo, "tests.txt"), []byte(headTests), 0o600))
	git("commit", "-q", "-am", "head")

	return repo, git("rev-parse", "HEAD")
}

// runCandidate runs the job of a candidate head in a real shell, with a stub gitleaks that exits with scan. The folder is the work root of the host.
func runCandidate(t *testing.T, root, repo, head string, scan int) string {
	t.Helper()

	bin := filepath.Join(root, "bin")
	require.NoError(t, os.MkdirAll(bin, 0o750))
	require.NoError(t, os.WriteFile(filepath.Join(bin, "gitleaks"), []byte(fmt.Sprintf("#!/bin/sh\nexit %d\n", scan)), 0o700)) //nolint:gosec // a stub program

	project := model.Project{Name: "demo", Gate: "sh mkreport.sh", TestReport: "reports/*.xml", TestCommand: "sh mkreport.sh"}
	command := hostCommand(project, nil, repo, head, root+"/beehave-gate-demo-"+head, "main")
	job := exec.CommandContext(t.Context(), "sh", "-c", command) //nolint:gosec // the job text under test
	job.Env = []string{"PATH=" + bin + string(os.PathListSeparator) + "/usr/bin:/bin", "HOME=" + t.TempDir()}

	// The job exits with the exit of the head gate, and the tests read the log, so the exit status is not the result here.
	out, err := job.CombinedOutput()
	if err != nil {
		var exit *exec.ExitError

		require.ErrorAs(t, err, &exit, string(out))
	}

	return string(out)
}

func TestBeforeReview_TheJobComparesTheHeadReportWithTheBaseReportPerTestAndCachesTheBase(t *testing.T) {
	t.Parallel()

	repo, head := shellRepo(t, "pass a TestOld\n", "pass a TestOld\npass a TestNew\npass a TestNew/case\nskip a TestSkipped\n")
	root := t.TempDir()

	out := runCandidate(t, root, repo, head, 0)
	checks := ReadChecks(out)

	require.True(t, checks.Complete, out)
	require.Equal(t, 3, checks.New)
	require.Equal(t, []string{"a TestNew", "a TestNew/case", "a TestSkipped"}, checks.NewTests)
	require.Equal(t, []string{"skipped a TestSkipped"}, checks.Failed)
	require.NotNil(t, checks.Secrets)
	require.Equal(t, 0, *checks.Secrets)
	require.Contains(t, out, stepMarker+" "+stepBaseSuite+" rc=0")

	again := runCandidate(t, root, repo, head, 0)

	require.NotContains(t, again, stepMarker+" "+stepBaseSuite, "the second job reads the cache")
	require.Equal(t, 3, ReadChecks(again).New)
}

func TestBeforeReview_AFailedSecretScanLeavesTheLaterStepsRunning(t *testing.T) {
	t.Parallel()

	repo, head := shellRepo(t, "pass a TestOld\n", "pass a TestOld\npass a TestNew\n")
	root := t.TempDir()

	checks := ReadChecks(runCandidate(t, root, repo, head, 1))

	require.True(t, checks.Complete)
	require.Equal(t, 1, checks.New)
	require.NotNil(t, checks.Secrets)
	require.Equal(t, 1, *checks.Secrets)
}

func TestBeforeReview_AHeadWithNoNewTestGivesAZeroCount(t *testing.T) {
	t.Parallel()

	repo, head := shellRepo(t, "pass a TestOld\n", "pass a TestOld\n# no change\n")
	root := t.TempDir()

	checks := ReadChecks(runCandidate(t, root, repo, head, 0))

	require.True(t, checks.Complete)
	require.Zero(t, checks.New)
}

func TestBeforeReview_TheJobExitsWithTheExitOfTheHeadGate(t *testing.T) {
	t.Parallel()

	project := model.Project{Name: "demo", Gate: "false", TestReport: "r/*.xml"}
	command := hostCommand(project, nil, "/nowhere", "abc", "/tmp/beehave-gate-demo-abc", "main")

	require.Contains(t, command, `[ "$headrc" -eq 0 ]`)
	require.Contains(t, command, stepMarker+" "+stepHeadGate+" rc=$rc")
	require.Contains(t, command, stepMarker+" "+stepSecrets+" rc=$rc")
}

func TestBeforeReview_ALogOverSixteenKiBBeforeTheEndMarkerStillReadsItsNameLines(t *testing.T) {
	t.Parallel()

	var log strings.Builder

	log.WriteString(strings.Repeat("noise noise noise noise\n", 1000))
	log.WriteString(newTestMarker + "pkg TestA/case\n" + newTestMarker + "pkg TestB\n" + newTestsEnd + "new=2 failed=0\n" + cloneMarker + "\n")

	checks := ReadChecks(log.String())

	require.True(t, checks.Complete)
	require.Equal(t, []string{"pkg TestA/case", "pkg TestB"}, checks.NewTests)
	require.Equal(t, 2, checks.New)
}

func TestBeforeReview_ALogWithNoEndMarkerIsACutLog(t *testing.T) {
	t.Parallel()

	checks := ReadChecks(newTestMarker + "pkg TestA\nnoise\n")

	require.False(t, checks.Complete)
	require.Empty(t, checks.NewTests)
}

func TestBeforeReview_ATestThatPrintsTheEndMarkerEarlierChangesNothing(t *testing.T) {
	t.Parallel()

	log := newTestsEnd + "new=99 failed=0\n" + "noise\n" + newTestMarker + "pkg TestA\n" + newTestsEnd + "new=1 failed=0\n"

	checks := ReadChecks(log)

	require.Equal(t, 1, checks.New)
	require.Equal(t, []string{"pkg TestA"}, checks.NewTests)
}

func TestBeforeReview_TheEndMarkerCarriesTheFullCountPastTheNameLineCap(t *testing.T) {
	t.Parallel()

	repo, head := shellRepo(t, "pass a TestOld\n", "pass a TestOld\n"+manyTests(75))
	root := t.TempDir()

	checks := ReadChecks(runCandidate(t, root, repo, head, 0))

	require.Equal(t, 75, checks.New)
	require.Len(t, checks.NewTests, markerCap)
}

func manyTests(n int) string {
	var out strings.Builder

	for i := range n {
		fmt.Fprintf(&out, "pass a TestN%d\n", i)
	}

	return out.String()
}
