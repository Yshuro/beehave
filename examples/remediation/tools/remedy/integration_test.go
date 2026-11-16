package remedy_test

import (
	"context"
	"slices"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/tobocop2/beehave/examples/remediation/tools/recount"
	"github.com/tobocop2/beehave/examples/remediation/tools/remedy"
	"github.com/tobocop2/beehave/internal/model"
)

const (
	roundBranch = "integration/s1"
	trunk       = "integration/r1"
)

// roundWorld is a forge for the integration subcommand. found is the answer to the lookup of the pull request from the round branch,
// merged is the list of pull requests that merged into the round branch, and view is the pull request that a given number reads.
// Every write is recorded: created holds the pull requests opened, and bodies holds each body written.
type roundWorld struct {
	forgeWorld
	found   []model.PullRequest
	merged  []model.PullRequest
	view    model.PullRequest
	nextPR  int
	listed  [][]string
	created []model.NewPull
	bodies  []string
}

func (w *roundWorld) PRView(context.Context, int, ...string) (model.PullRequest, error) {
	return w.view, nil
}

func (w *roundWorld) PRList(_ context.Context, _ int, args ...string) ([]model.PullRequest, error) {
	w.listed = append(w.listed, args)

	if slices.Contains(args, "merged") {
		return w.merged, nil
	}

	return w.found, nil
}

func (w *roundWorld) CreatePR(_ context.Context, req model.NewPull) (int, error) {
	w.created = append(w.created, req)

	return w.nextPR, nil
}

func (w *roundWorld) SetBody(_ context.Context, _ int, body string) error {
	w.bodies = append(w.bodies, body)

	return nil
}

func roundDeps(world *roundWorld) remedy.Deps {
	return remedy.Deps{
		Git:   origin("git@github.com:owner/name.git", nothing),
		Dir:   ".",
		Forge: func(string) (remedy.Forge, error) { return world, nil },
	}
}

func state(s string) *string { return &s }

func mergedPull(number int, title string) model.PullRequest {
	return model.PullRequest{Number: number, Title: title, State: state("MERGED")}
}

func integrationArgs(pr string) []string {
	return []string{"integration", "--base", roundBranch, "--into", trunk, "--pr", pr}
}

func TestIntegration_FindsThePullRequestFromTheRoundBranchIntoTheTrunkAndPrintsItsLine(t *testing.T) {
	t.Parallel()

	world := &roundWorld{found: []model.PullRequest{{Number: 7, State: state("OPEN"), Body: "## Pull requests merged into this branch (0 pull requests)\n\n| PR | Change |\n|---|---|\n"}}}

	code, out, errOut := run(t, roundDeps(world), integrationArgs("0")...)

	require.Equal(t, remedy.ExitPass, code, errOut)
	assert.Equal(t, "pr=7 state=OPEN\n", out)
	assert.Empty(t, world.created)
	assert.Contains(t, strings.Join(world.listed[0], " "), "--head "+roundBranch)
	assert.Contains(t, strings.Join(world.listed[0], " "), "--base "+trunk)
}

func TestIntegration_UsesTheGivenNumberAndLooksForNoOther(t *testing.T) {
	t.Parallel()

	world := &roundWorld{view: model.PullRequest{Number: 9, State: state("MERGED"), Body: "x"}}

	code, out, errOut := run(t, roundDeps(world), integrationArgs("9")...)

	require.Equal(t, remedy.ExitPass, code, errOut)
	assert.Equal(t, "pr=9 state=MERGED\n", out)
	assert.Empty(t, world.created)
	assert.Empty(t, world.listed)
}

func TestIntegration_CreatesThePullRequestWhenNoneExistsWithTheMergedRowsInItsBody(t *testing.T) {
	t.Parallel()

	world := &roundWorld{nextPR: 21, merged: []model.PullRequest{mergedPull(12, "fix the queue"), mergedPull(13, "add the wait")}}

	code, out, errOut := run(t, roundDeps(world), integrationArgs("0")...)

	require.Equal(t, remedy.ExitPass, code, errOut)
	assert.Equal(t, "pr=21 state=OPEN\n", out)
	require.Len(t, world.created, 1)
	assert.Equal(t, model.NewPull{Head: roundBranch, Base: trunk, Title: world.created[0].Title, Body: world.created[0].Body}, world.created[0])
	assert.Equal(t, recount.Tally{Rows: 2}, recount.Count(world.created[0].Body))
	assert.Empty(t, recount.Check(world.created[0].Title, world.created[0].Body))
	assert.Contains(t, world.created[0].Body, "| #12 | fix the queue |")
}

func TestIntegration_AddsOneRowForEachMergedPullRequestThatTheTableLacks(t *testing.T) {
	t.Parallel()

	body := "intro\n\n## Pull requests merged into this branch (1 pull request)\n\n| PR | Change |\n|---|---|\n| #12 | fix the queue |\n\n- Closes #5\n"
	world := &roundWorld{
		found:  []model.PullRequest{{Number: 7, State: state("OPEN"), Body: body}},
		merged: []model.PullRequest{mergedPull(12, "fix the queue"), mergedPull(13, "add the wait")},
	}

	code, out, errOut := run(t, roundDeps(world), integrationArgs("0")...)

	require.Equal(t, remedy.ExitPass, code, errOut)
	assert.Equal(t, "pr=7 state=OPEN\n", out)
	require.Len(t, world.bodies, 1)
	assert.Equal(t, recount.Tally{Rows: 2, Issues: 1}, recount.Count(world.bodies[0]))
	assert.Empty(t, recount.Check("a person's title", world.bodies[0]))
	assert.Contains(t, world.bodies[0], "intro")
	assert.Contains(t, world.bodies[0], "| #13 | add the wait |")
}

func branchPull(number int, title, branch string) model.PullRequest {
	pull := mergedPull(number, title)
	pull.HeadRefName = &branch

	return pull
}

func TestIntegration_TwoMergedRowsThatResolveTwoIssuesGiveTwoClosesLinesAndASecondRunAddsNone(t *testing.T) {
	t.Parallel()

	body := "## Pull requests merged into this branch (0 pull requests)\n\n| PR | Change |\n|---|---|\n"
	world := &roundWorld{
		found:  []model.PullRequest{{Number: 7, State: state("OPEN"), Body: body}},
		merged: []model.PullRequest{branchPull(12, "fix the queue", "101"), branchPull(13, "add the wait", "102"), branchPull(14, "a loose change", "fix-queue")},
	}

	code, out, errOut := run(t, roundDeps(world), integrationArgs("0")...)

	require.Equal(t, remedy.ExitPass, code, errOut)
	assert.Equal(t, "pr=7 state=OPEN\n", out)
	require.Len(t, world.bodies, 1)
	assert.Equal(t, recount.Tally{Rows: 3, Issues: 2}, recount.Count(world.bodies[0]))
	assert.Contains(t, world.bodies[0], "## Issues this batch closes\n\n- Closes #101\n- Closes #102")
	assert.Contains(t, strings.Join(world.listed[1], " "), "headRefName")

	world.found[0].Body = world.bodies[0]

	code, _, errOut = run(t, roundDeps(world), integrationArgs("0")...)

	require.Equal(t, remedy.ExitPass, code, errOut)
	assert.Len(t, world.bodies, 1)
}

func TestIntegration_AnIssueTheBodyAlreadyListsIsNotListedAgain(t *testing.T) {
	t.Parallel()

	body := "## Pull requests merged into this branch (1 pull request)\n\n| PR | Change |\n|---|---|\n| #12 | fix the queue |\n\n## Issues this batch closes\n\n- Closes #101\n\n## Notes\n\ntext\n"
	world := &roundWorld{
		found:  []model.PullRequest{{Number: 7, State: state("OPEN"), Body: body}},
		merged: []model.PullRequest{branchPull(12, "fix the queue", "101"), branchPull(13, "add the wait", "102")},
	}

	code, _, errOut := run(t, roundDeps(world), integrationArgs("0")...)

	require.Equal(t, remedy.ExitPass, code, errOut)
	require.Len(t, world.bodies, 1)
	assert.Contains(t, world.bodies[0], "- Closes #101\n- Closes #102\n\n## Notes")
	assert.Equal(t, 1, strings.Count(world.bodies[0], "Closes #101"))
}

func TestIntegration_WritesNothingWhenTheTableHoldsEveryMergedRow(t *testing.T) {
	t.Parallel()

	body := "## Pull requests merged into this branch (1 pull request)\n\n| PR | Change |\n|---|---|\n| #12 | fix the queue |\n"
	world := &roundWorld{
		found:  []model.PullRequest{{Number: 7, State: state("OPEN"), Body: body}},
		merged: []model.PullRequest{mergedPull(12, "fix the queue")},
	}

	code, _, errOut := run(t, roundDeps(world), integrationArgs("0")...)

	require.Equal(t, remedy.ExitPass, code, errOut)
	assert.Empty(t, world.bodies)
}

func TestIntegration_ALandedPullRequestIsReportedAndItsBodyIsLeftAlone(t *testing.T) {
	t.Parallel()

	world := &roundWorld{
		found:  []model.PullRequest{{Number: 7, State: state("MERGED"), Body: "old"}},
		merged: []model.PullRequest{mergedPull(12, "fix the queue")},
	}

	code, out, errOut := run(t, roundDeps(world), integrationArgs("0")...)

	require.Equal(t, remedy.ExitPass, code, errOut)
	assert.Equal(t, "pr=7 state=MERGED\n", out)
	assert.Empty(t, world.bodies)
}

func TestIntegration_ABadArgumentIsARefusal(t *testing.T) {
	t.Parallel()

	for _, args := range [][]string{
		{"integration", "--into", trunk, "--pr", "0"},
		{"integration", "--base", roundBranch, "--pr", "0"},
		{"integration", "--base", roundBranch, "--into", trunk, "--pr", "-1"},
	} {
		code, out, errOut := run(t, roundDeps(&roundWorld{}), args...)

		assert.Equal(t, remedy.ExitRefused, code, args)
		assert.Empty(t, out)
		assert.Contains(t, errOut, "usage: remedy integration")
	}
}

func TestIntegration_AForgeThatCannotWriteIsARefusal(t *testing.T) {
	t.Parallel()

	code, out, errOut := run(t, cutDeps(&forgeWorld{}), integrationArgs("0")...)

	assert.Equal(t, remedy.ExitRefused, code)
	assert.Empty(t, out)
	assert.Contains(t, errOut, "remedy integration:")
}
