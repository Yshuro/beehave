package flow

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/tobocop2/beehave/internal/model"
)

// reviewTree is an item folder with a base commit, an unrelated commit on another branch, and a head one commit above the base.
type reviewTree struct {
	dir, base, other, head string
}

// reviewTreeOf makes the folder. The head commit adds the file with the message.
func reviewTreeOf(t *testing.T, file, message string) reviewTree {
	t.Helper()

	dir, base := gitTree(t)
	gitIn(t, dir, "checkout", "-q", "-b", "elsewhere")
	putUnder(t, dir, "elsewhere.txt", "x")
	gitIn(t, dir, "add", "elsewhere.txt")
	gitIn(t, dir, "commit", "-q", "-m", "elsewhere")

	other := gitIn(t, dir, "rev-parse", "HEAD")

	gitIn(t, dir, "checkout", "-q", "-b", "work", base)
	putUnder(t, dir, file, "x")
	gitIn(t, dir, "add", file)
	gitIn(t, dir, "commit", "-q", "-m", message)

	return reviewTree{dir: dir, base: base, other: other, head: gitIn(t, dir, "rev-parse", "HEAD")}
}

// cleanChecks are the checks of a gate job that found one new test and a clean secret scan.
func cleanChecks() *GateChecks {
	zero := 0

	return &GateChecks{NewTests: []string{"pkg TestA"}, New: 1, Complete: true, Secrets: &zero, Revert: model.RevertPass, RevertCases: 1}
}

// reviewHarness gates the head of the tree with the checks, and reads the item folder from the tree.
func reviewHarness(t *testing.T, tree reviewTree, checks *GateChecks) *harness {
	t.Helper()

	h := reuseHarness(t, []GateResult{{Passed: true, Head: tree.head, Tree: "tree-1", Steps: stepsPass, Checks: checks}}, nil)
	h.trees = &fakeTrees{dir: tree.dir}

	return h
}

// reviewFlow gates the head and runs before_review with the arguments. It shows ok, the findings, the base flag, the keywords and the new tests.
func reviewFlow(base, extra string) string {
	return "g = gate(name = \"g\", base = \"main\")\n" +
		"r = before_review(base = \"" + base + "\", head = g.head, " + extra + "name = \"r\")\n" +
		"command(run = [\"show\", str(r.ok), \"|\".join(r.findings), str(r.base), \",\".join(r.keywords), \",\".join(r.new_tests)])\n"
}

func TestBeforeReview_ACleanHeadIsOkAndShowsItsNewTests(t *testing.T) {
	t.Parallel()

	tree := reviewTreeOf(t, "src/a.go", "add a")
	h := reviewHarness(t, tree, cleanChecks())

	_, err := walkText(t, h, bodyFlow(reviewFlow(tree.base, `allow = ["src/**"], `)))

	require.NoError(t, err)
	assert.Equal(t, []string{"show True  True  pkg TestA"}, h.commands.ran())
}

func TestBeforeReview_AHeadThatIsNotADescendantOfTheBaseIsRefused(t *testing.T) {
	t.Parallel()

	tree := reviewTreeOf(t, "src/a.go", "add a")
	h := reviewHarness(t, tree, cleanChecks())

	_, err := walkText(t, h, bodyFlow(reviewFlow(tree.other, "")))

	require.NoError(t, err)
	require.Len(t, h.commands.ran(), 1)
	assert.Contains(t, h.commands.ran()[0], "show False")
	assert.Contains(t, h.commands.ran()[0], "is not a descendant of the base "+tree.other)
}

func TestBeforeReview_AChangedPathOutsideTheAllowedPathsIsARefusedBase(t *testing.T) {
	t.Parallel()

	tree := reviewTreeOf(t, "docs/a.md", "add a")
	h := reviewHarness(t, tree, cleanChecks())

	_, err := walkText(t, h, bodyFlow(reviewFlow(tree.base, `allow = ["src/**"], `)))

	require.NoError(t, err)
	require.Len(t, h.commands.ran(), 1)
	assert.Contains(t, h.commands.ran()[0], "outside allow [src/**]: docs/a.md")
	assert.Contains(t, h.commands.ran()[0], "docs/a.md False", "the base flag is false when a path is out of scope")
}

func TestBeforeReview_AClosingKeywordInACommitIsRefusedOutsideTheIntegrationPullRequest(t *testing.T) {
	t.Parallel()

	tree := reviewTreeOf(t, "src/a.go", "add a\n\nFixes #12")

	for _, c := range []struct {
		name, extra string
		refused     bool
	}{{"outside", "", true}, {"integration", "integration = True, ", false}} {
		h := reviewHarness(t, tree, cleanChecks())

		_, err := walkText(t, h, bodyFlow(reviewFlow(tree.base, c.extra)))

		require.NoError(t, err, c.name)
		require.Len(t, h.commands.ran(), 1, c.name)
		assert.Equal(t, c.refused, strings.Contains(h.commands.ran()[0], "show False"), c.name)
		assert.Equal(t, c.refused, strings.Contains(h.commands.ran()[0], "#12"), c.name)
	}
}

func TestBeforeReview_ACandidateWithNoNewTestOrASkippedNewTestOrACutLogOrADirtySecretScanIsRefused(t *testing.T) {
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
		"cut log":           {&GateChecks{Secrets: &none, Problem: "no base report"}, "no end marker"},
		"gate with no base": {nil, "no end marker"},
		"secret found":      {&GateChecks{NewTests: []string{"pkg TestA"}, New: 1, Complete: true, Secrets: &one}, "secret scan of the head exited with code 1"},
		"no scan code":      {&GateChecks{NewTests: []string{"pkg TestA"}, New: 1, Complete: true}, "no exit code of the secret scan"},
	}

	for name, c := range cases {
		tree := reviewTreeOf(t, "src/a.go", "add a")
		h := reviewHarness(t, tree, c.checks)

		_, err := walkText(t, h, bodyFlow(reviewFlow(tree.base, "")))

		require.NoError(t, err, name)
		require.Len(t, h.commands.ran(), 1, name)
		assert.Contains(t, h.commands.ran()[0], "show False", name)
		assert.Contains(t, h.commands.ran()[0], c.finds, name)
	}
}

func TestBeforeReview_ASubtestCountsAsOneNewTest(t *testing.T) {
	t.Parallel()

	tree := reviewTreeOf(t, "src/a.go", "add a")
	zero := 0
	h := reviewHarness(t, tree, &GateChecks{NewTests: []string{"pkg TestA/case"}, New: 1, Complete: true, Secrets: &zero, Revert: model.RevertPass, RevertCases: 1})

	_, err := walkText(t, h, bodyFlow(reviewFlow(tree.base, "")))

	require.NoError(t, err)
	assert.Equal(t, []string{"show True  True  pkg TestA/case"}, h.commands.ran())
}

func TestBeforeReview_ACallWithNoGateAtItsHeadIsAnError(t *testing.T) {
	t.Parallel()

	tree := reviewTreeOf(t, "src/a.go", "add a")
	h := reviewHarness(t, tree, cleanChecks())

	_, err := walkText(t, h, bodyFlow("before_review(base = \""+tree.base+"\", head = \"someotherhead\")\n"))

	require.ErrorContains(t, err, "before_review needs a gate call with `base` in this run")
}
