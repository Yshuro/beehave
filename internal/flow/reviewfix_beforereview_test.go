package flow

import (
	"bytes"
	"errors"
	"os/exec"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/tobocop2/beehave/internal/model"
)

// gitReply answers each command of git with the real git in the folder, and any other command with an empty output.
func gitReply(t *testing.T, dir string) func(CommandRequest) (CommandOutput, error) {
	t.Helper()

	return func(request CommandRequest) (CommandOutput, error) {
		if request.Run[0] != "git" {
			return CommandOutput{}, nil
		}

		cmd := exec.CommandContext(t.Context(), "git", request.Run[1:]...)
		cmd.Dir = dir
		cmd.Env = append(cmd.Environ(), "GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_SYSTEM=/dev/null")

		var stdout, stderr bytes.Buffer

		cmd.Stdout, cmd.Stderr = &stdout, &stderr

		err := cmd.Run()

		var exit *exec.ExitError
		if errors.As(err, &exit) {
			return CommandOutput{Stdout: stdout.String(), Stderr: stderr.String(), RC: exit.ExitCode()}, nil
		}

		return CommandOutput{Stdout: stdout.String(), Stderr: stderr.String()}, err
	}
}

// libReviewFlow gates the head, judges it with the function of the library and shows ok and the findings.
func libReviewFlow(base, extra string) string {
	return "load(\"@beehave//review_fix.star\", \"before_review\")\n\n" + bodyFlow(
		"g = gate(name = \"g\", base = \"main\")\n"+
			"r = before_review(g, base = \""+base+"\", "+extra+"name = \"r\")\n"+
			"command(run = [\"show\", str(r.ok), \"|\".join(r.findings)])\n")
}

// reviewShown runs the flow with real git in the folder of the tree and returns the line that the flow showed.
func reviewShown(t *testing.T, tree reviewTree, checks *GateChecks, base, extra string) (string, error) {
	t.Helper()

	h := reviewHarness(t, tree, checks)
	h.commands.reply = gitReply(t, tree.dir)

	_, err := walkText(t, h, libReviewFlow(base, extra))

	for _, line := range h.commands.ran() {
		if strings.HasPrefix(line, "show ") {
			return line, err
		}
	}

	return "", err
}

func TestReviewFixBeforeReview_ACleanHeadIsOk(t *testing.T) {
	t.Parallel()

	tree := reviewTreeOf(t, "src/a.go", "add a")

	shown, err := reviewShown(t, tree, cleanChecks(), tree.base, `allow = ["src/**"], `)

	require.NoError(t, err)
	assert.Equal(t, "show True ", shown)
}

func TestReviewFixBeforeReview_AHeadThatIsNotADescendantOfTheBaseIsRefused(t *testing.T) {
	t.Parallel()

	tree := reviewTreeOf(t, "src/a.go", "add a")

	shown, err := reviewShown(t, tree, cleanChecks(), tree.other, "")

	require.NoError(t, err)
	assert.Contains(t, shown, "show False")
	assert.Contains(t, shown, "is not a descendant of the base "+tree.other)
}

func TestReviewFixBeforeReview_AChangedPathOutsideTheAllowedPathsIsARefusal(t *testing.T) {
	t.Parallel()

	tree := reviewTreeOf(t, "docs/a.md", "add a")

	shown, err := reviewShown(t, tree, cleanChecks(), tree.base, `allow = ["src/**", "./pkg/*.go"], `)

	require.NoError(t, err)
	assert.Contains(t, shown, "show False")
	assert.Contains(t, shown, "1 path(s) outside allow [src/**, ./pkg/*.go]: docs/a.md")
}

func TestReviewFixBeforeReview_APathInsideAnAllowedGlobIsNotAFinding(t *testing.T) {
	t.Parallel()

	tree := reviewTreeOf(t, "pkg/a.go", "add a")

	shown, err := reviewShown(t, tree, cleanChecks(), tree.base, `allow = ["./pkg/*.go"], `)

	require.NoError(t, err)
	assert.Equal(t, "show True ", shown, "the leading ./ of a glob is dropped, as for allow_paths")
}

func TestReviewFixBeforeReview_NoAllowAllowsEveryPath(t *testing.T) {
	t.Parallel()

	tree := reviewTreeOf(t, "docs/a.md", "add a")
	h := reviewHarness(t, tree, cleanChecks())
	h.commands.reply = gitReply(t, tree.dir)

	_, err := walkText(t, h, libReviewFlow(tree.base, ""))

	require.NoError(t, err)

	for _, line := range h.commands.ran() {
		assert.NotContains(t, line, "git diff", "a call with no allow lists no path")
	}
}

func TestReviewFixBeforeReview_AClosingKeywordInACommitIsARefusalThatNamesTheCommit(t *testing.T) {
	t.Parallel()

	cases := map[string]struct {
		message string
		refused bool
	}{
		"fixes_number":      {"add a\n\nFixes #12", true},
		"closes_cross_repo": {"add a\n\ncloses owner/repo#3", true},
		"resolved_colon":    {"add a\n\nResolved: #7", true},
		"issue_url":         {"add a\n\nfix https://github.com/owner/repo/issues/9", true},
		"keyword_in_header": {"fix #5", true},
		"plain_number":      {"add a\n\nsee #12", false},
		"word_ends_in_fix":  {"add a\n\nsuffix #12", false},
		"keyword_no_issue":  {"add a\n\nfixes the build", false},
	}

	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			tree := reviewTreeOf(t, "src/a.go", c.message)

			shown, err := reviewShown(t, tree, cleanChecks(), tree.base, "")

			require.NoError(t, err)

			if c.refused {
				assert.Contains(t, shown, "show False")
				assert.Contains(t, shown, "the commit "+tree.head[:7])
				assert.Contains(t, shown, "has a closing keyword. Only the integration pull request may close an issue")
			} else {
				assert.Equal(t, "show True ", shown)
			}
		})
	}
}

func TestReviewFixBeforeReview_ACandidateWithNoNewTestOrASkippedNewTestOrACutLogOrADirtySecretScanIsRefused(t *testing.T) {
	t.Parallel()

	one, none := 1, 0
	cases := map[string]struct {
		checks *GateChecks
		finds  string
	}{
		"zero new tests": {&GateChecks{Complete: true, Secrets: &none}, "adds no new test"},
		"skipped new test": {
			&GateChecks{NewTests: []string{"pkg TestA"}, New: 1, Failed: []string{"skipped pkg TestA"}, FailedCount: 1, Complete: true, Secrets: &none},
			"a new test failed a check: skipped pkg TestA",
		},
		"cut log":           {&GateChecks{Secrets: &none, Problem: "no base report"}, "the new-tests check is cut and proves nothing: no base report"},
		"gate with no base": {nil, "no end marker"},
		"secret found":      {&GateChecks{NewTests: []string{"pkg TestA"}, New: 1, Complete: true, Secrets: &one}, "secret scan of the head exited with code 1"},
		"no scan code":      {&GateChecks{NewTests: []string{"pkg TestA"}, New: 1, Complete: true}, "no exit code of the secret scan"},
		"more failed than named": {
			&GateChecks{NewTests: []string{"pkg TestA"}, New: 1, Failed: []string{"skipped pkg TestA"}, FailedCount: 3, Complete: true, Secrets: &none, Revert: model.RevertPass, RevertCases: 1},
			"3 new tests failed a check in all, and the log names the first 1",
		},
	}

	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			tree := reviewTreeOf(t, "src/a.go", "add a")

			shown, err := reviewShown(t, tree, c.checks, tree.base, "")

			require.NoError(t, err)
			assert.Contains(t, shown, "show False")
			assert.Contains(t, shown, c.finds, name)
		})
	}
}

func TestReviewFixBeforeReview_ASubtestCountsAsOneNewTest(t *testing.T) {
	t.Parallel()

	tree := reviewTreeOf(t, "src/a.go", "add a")
	zero := 0

	shown, err := reviewShown(t, tree, &GateChecks{NewTests: []string{"pkg TestA/case"}, New: 1, Complete: true, Secrets: &zero, Revert: model.RevertPass, RevertCases: 1}, tree.base, "")

	require.NoError(t, err)
	assert.Equal(t, "show True ", shown)
}

func TestReviewFixBeforeReview_TheFunctionJudgesTheGateResultItGetsWhateverGateCameAfterIt(t *testing.T) {
	t.Parallel()

	tree := reviewTreeOf(t, "src/a.go", "add a")
	zero := 0
	h := reuseHarness(t, []GateResult{
		{Passed: true, Head: tree.head, Tree: "tree-1", Steps: stepsPass, Checks: cleanChecks()},
		{Passed: true, Head: tree.other, Tree: "tree-2", Steps: stepsPass, Checks: &GateChecks{Complete: true, Secrets: &zero}},
	}, nil)
	h.trees = &fakeTrees{dir: tree.dir}
	h.commands.reply = gitReply(t, tree.dir)

	_, err := walkText(t, h, "load(\"@beehave//review_fix.star\", \"before_review\")\n\n"+bodyFlow(
		"g1 = gate(name = \"g1\", base = \"main\")\n"+
			"g2 = gate(name = \"g2\", base = \"main\")\n"+
			"r = before_review(g1, base = \""+tree.base+"\", name = \"r\")\n"+
			"command(run = [\"show\", str(r.ok)])\n"))

	require.NoError(t, err)
	assert.Contains(t, h.commands.ran(), "show True", "the second gate has no new test, and the first one is judged")
}

func TestReviewFixBeforeReview_AGitCommandThatFailsIsAnErrorAndNotARefusal(t *testing.T) {
	t.Parallel()

	cases := map[string]struct {
		config, base, extra, want string
	}{
		"base git cannot find": {"", "no-such-base", "", "could not run git merge-base --is-ancestor no-such-base"},
		// git diff reads diff.context and git merge-base does not, so the descent check passes and the path list fails.
		"path list": {"diff.context", "", `allow = ["src/**"], `, "could not run git diff"},
		// git log reads log.abbrevCommit and git merge-base does not, so the descent check passes and the message read fails.
		"commit log": {"log.abbrevCommit", "", "", "could not run git log"},
	}

	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			tree := reviewTreeOf(t, "src/a.go", "add a")
			if c.config != "" {
				gitIn(t, tree.dir, "config", c.config, "not-valid")
			}

			base := c.base
			if base == "" {
				base = tree.base
			}

			shown, err := reviewShown(t, tree, cleanChecks(), base, c.extra)

			require.ErrorContains(t, err, c.want)
			assert.Empty(t, shown, "a failed git command refuses nothing: the item stops")
		})
	}
}

func TestReviewFixBeforeReview_ANewTestThatPassesOnTheBaseIsAFindingThatNamesIt(t *testing.T) {
	t.Parallel()

	tree := reviewTreeOf(t, "src/a.go", "add a")

	shown, err := reviewShown(t, tree, revertChecks(3, "pkg TestA"), tree.base, "")

	require.NoError(t, err)
	assert.Contains(t, shown, "show False")
	assert.Contains(t, shown, "the new test pkg TestA passes or is skipped on the base code. A test must fail without the change")
	assert.Contains(t, shown, "3 new tests pass or are skipped on the base code in all, and the log names the first 1")
}

func TestReviewFixBeforeReview_ARevertRunThatProvesNothingIsAFindingWithItsReason(t *testing.T) {
	t.Parallel()

	cases := map[string]struct {
		problem, finds string
	}{
		"no test case":  {"the report of the revert run holds no test case, so the run proves nothing", "holds no test case"},
		"no test_paths": {"the project sets no test_command or no test_paths, so the new tests cannot run on the base code", "no test_paths"},
		"no end marker": {"", "no end marker of the revert run"},
	}

	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			tree := reviewTreeOf(t, "src/a.go", "add a")
			checks := revertChecks(0)
			checks.RevertProblem = c.problem

			shown, err := reviewShown(t, tree, checks, tree.base, "")

			require.NoError(t, err)
			assert.Contains(t, shown, "the revert run proves nothing: ")
			assert.Contains(t, shown, c.finds)
		})
	}
}

func TestReviewFixBeforeReview_AHeadWithNoNewTestGetsNoRevertFinding(t *testing.T) {
	t.Parallel()

	tree := reviewTreeOf(t, "src/a.go", "add a")
	zero := 0

	shown, err := reviewShown(t, tree, &GateChecks{Complete: true, Secrets: &zero, Revert: model.RevertFail}, tree.base, "")

	require.NoError(t, err)
	assert.Equal(t, "show False the head adds no new test. Add a test that fails without the change", shown)
}

// logChecks reads a job log into the checks that the engine records, and returns them with the length that the ssh host read.
func logChecks(t *testing.T, log string) (read int, checks GateChecks) {
	t.Helper()

	read, _, checks = findingsOfLog(t, log)

	return read, checks
}

func TestReviewFixBeforeReview_ALogOverSixteenKiBBeforeTheEndMarkerPassesWithItsNewTestNames(t *testing.T) {
	t.Parallel()

	log := jobLog(1000, "0", "beehave-new-test pkg TestA", "beehave-new-test pkg TestA/case", "beehave-new-tests-end new=2 failed=0", revertClean)
	require.Greater(t, len(log), tailBytes, "the log is longer than the tail that the host reads")

	read, checks := logChecks(t, log)
	tree := reviewTreeOf(t, "src/a.go", "add a")

	shown, err := reviewShown(t, tree, &checks, tree.base, "")

	require.NoError(t, err)
	assert.Equal(t, tailBytes, read)
	assert.Equal(t, []string{"pkg TestA", "pkg TestA/case"}, checks.NewTests, "a subtest is its own new test")
	assert.Equal(t, "show True ", shown)
}

func TestReviewFixBeforeReview_TheChecksOfARealLogAreFindings(t *testing.T) {
	t.Parallel()

	cases := map[string]struct {
		log, finds string
	}{
		"skipped new test": {
			jobLog(1000, "0", "beehave-new-test pkg TestA", "beehave-new-test-failed skipped pkg TestA", "beehave-new-tests-end new=1 failed=1", revertClean),
			"a new test failed a check: skipped pkg TestA",
		},
		"no end marker": {jobLog(1000, "0", "beehave-new-test pkg TestA"), "no end marker"},
		"zero new":      {jobLog(1000, "0", "beehave-new-tests-end new=0 failed=0"), "adds no new test"},
		"secret scan":   {jobLog(1000, "1", "beehave-new-test pkg TestA", "beehave-new-tests-end new=1 failed=0", revertClean), "secret scan of the head exited with code 1"},
	}

	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			_, checks := logChecks(t, c.log)
			tree := reviewTreeOf(t, "src/a.go", "add a")

			shown, err := reviewShown(t, tree, &checks, tree.base, "")

			require.NoError(t, err)
			assert.Contains(t, shown, "show False")
			assert.Contains(t, shown, c.finds)
		})
	}
}
