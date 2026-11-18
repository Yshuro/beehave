package flow

import (
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/tobocop2/beehave/internal/model"
)

func TestBeforeReview_AFolderThatCannotBeFoundIsAnErrorNamingTheItem(t *testing.T) {
	t.Parallel()

	tree := reviewTreeOf(t, "src/a.go", "add a")
	h := reviewHarness(t, tree, cleanChecks())
	h.trees = &fakeTrees{err: errors.New("no worktree")}

	_, err := walkText(t, h, bodyFlow(reviewFlow(tree.base, "")))

	require.ErrorContains(t, err, "find the folder of item")
	require.ErrorContains(t, err, "no worktree")
	assert.Empty(t, h.commands.ran())
}

func TestBeforeReview_TheReviewReadsTheGateAtItsHeadWhateverGateCameAfterIt(t *testing.T) {
	t.Parallel()

	tree := reviewTreeOf(t, "src/a.go", "add a")
	zero := 0
	later := &GateChecks{New: 5, Complete: true, Secrets: &zero}
	h := reuseHarness(t, []GateResult{
		{Passed: true, Head: tree.head, Tree: "tree-1", Steps: stepsPass, Checks: cleanChecks()},
		{Passed: true, Head: tree.other, Tree: "tree-2", Steps: stepsPass, Checks: later},
	}, nil)
	h.trees = &fakeTrees{dir: tree.dir}

	body := "g1 = gate(name = \"g1\", base = \"main\")\n" +
		"g2 = gate(name = \"g2\", base = \"main\")\n" +
		"r = before_review(base = \"" + tree.base + "\", head = g1.head, name = \"r\")\n" +
		"command(run = [\"show\", str(r.ok), \",\".join(r.new_tests)])\n"

	_, err := walkText(t, h, bodyFlow(body))

	require.NoError(t, err)
	assert.Equal(t, []string{"show True pkg TestA"}, h.commands.ran())
}

func TestBeforeReview_ACallWhoseHeadNoGateOfTheRunGaveIsAnErrorEvenAfterGates(t *testing.T) {
	t.Parallel()

	tree := reviewTreeOf(t, "src/a.go", "add a")
	h := reviewHarness(t, tree, cleanChecks())

	body := "g = gate(name = \"g\", base = \"main\")\n" +
		"before_review(base = \"" + tree.base + "\", head = \"someotherhead\", name = \"r\")\n"

	_, err := walkText(t, h, bodyFlow(body))

	require.ErrorContains(t, err, "before_review needs a gate call with `base` in this run")
}

func TestBeforeReview_ABaseThatGitCannotFindIsAnErrorAndNotARefusal(t *testing.T) {
	t.Parallel()

	tree := reviewTreeOf(t, "src/a.go", "add a")
	h := reviewHarness(t, tree, cleanChecks())

	_, err := walkText(t, h, bodyFlow(reviewFlow("no-such-base", "")))

	require.ErrorContains(t, err, "compare the head "+tree.head+" with the base no-such-base")
	assert.Empty(t, h.commands.ran())
}

func TestBeforeReview_APathListThatGitCannotReadIsAnErrorNamingThePaths(t *testing.T) {
	t.Parallel()

	tree := reviewTreeOf(t, "src/a.go", "add a")
	// git diff reads diff.context and git merge-base does not, so the descent check passes and the path list fails.
	gitIn(t, tree.dir, "config", "diff.context", "not-a-number")
	h := reviewHarness(t, tree, cleanChecks())

	_, err := walkText(t, h, bodyFlow(reviewFlow(tree.base, `allow = ["src/**"], `)))

	require.ErrorContains(t, err, "list the paths that the head changed since the base")
	assert.Empty(t, h.commands.ran())
}

func TestBeforeReview_ACommitLogThatGitCannotReadIsAnErrorNamingTheMessages(t *testing.T) {
	t.Parallel()

	tree := reviewTreeOf(t, "src/a.go", "add a")
	// git log reads log.abbrevCommit and git merge-base does not, so the descent check passes and the message read fails.
	gitIn(t, tree.dir, "config", "log.abbrevCommit", "not-a-bool")
	h := reviewHarness(t, tree, cleanChecks())

	_, err := walkText(t, h, bodyFlow(reviewFlow(tree.base, "")))

	require.ErrorContains(t, err, "read the commit messages of the head")
	assert.Empty(t, h.commands.ran())
}

func TestBeforeReview_MoreFailedNewTestsThanTheLogNamesIsAFindingWithBothCounts(t *testing.T) {
	t.Parallel()

	tree := reviewTreeOf(t, "src/a.go", "add a")
	zero := 0
	checks := &GateChecks{NewTests: []string{"pkg TestA"}, New: 1, Failed: []string{"skipped pkg TestA"}, FailedCount: 3, Complete: true, Secrets: &zero, Revert: model.RevertPass, RevertCases: 1}
	h := reviewHarness(t, tree, checks)

	_, err := walkText(t, h, bodyFlow(reviewFlow(tree.base, "")))

	require.NoError(t, err)
	require.Len(t, h.commands.ran(), 1)
	assert.Contains(t, h.commands.ran()[0], "show False")
	assert.Contains(t, h.commands.ran()[0], "3 new tests failed a check in all, and the log names the first 1")
}
