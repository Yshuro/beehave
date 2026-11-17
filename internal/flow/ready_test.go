package flow

import (
	"slices"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/tobocop2/beehave/internal/merge"
	"github.com/tobocop2/beehave/internal/model"
	"github.com/tobocop2/beehave/internal/review"
)

var standingChanges = []model.ChangeRequest{{Login: "rev", Commit: "ccc"}}

func TestRun_TheMergeStepRefusesAPullRequestThatIsNotReadyAndNamesTheReason(t *testing.T) {
	t.Parallel()

	tests := map[string]struct {
		state  review.Standing
		reason string
	}{
		"a_review_asks_for_changes":     {review.Standing{Number: testPull, ChangeRequests: standingChanges}, "pull request 7: a review asks for changes"},
		"the_pull_request_is_closed":    {review.Standing{Number: testPull, Closed: true}, "pull request 7: it is closed"},
		"a_thread_is_unresolved":        {review.Standing{Number: testPull, UnresolvedThreads: 2}, "pull request 7: 2 review threads are unresolved"},
		"the_branch_conflicts":          {review.Standing{Number: testPull, Conflicting: true}, "pull request 7: it conflicts with its base branch"},
		"two_reasons_stand":             {review.Standing{Number: testPull, ChangeRequests: standingChanges, Conflicting: true}, "a review asks for changes, it conflicts with its base branch"},
		"a_read_of_the_standing_failed": {review.Standing{Number: testPull, Unread: []string{"review threads"}}, "pull request 7: the review threads could not be read"},
		"a_push_after_the_pinned_head":  {review.Standing{Number: testPull, Head: "ccc"}, `moved to "ccc" after the review read "` + gatedHead + `"`},
	}

	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			harness := shipHarness(t)
			harness.forge.state = tt.state

			if harness.forge.state.Head == "" {
				harness.forge.state.Head = gatedHead
			}

			engine := harness.engine(t)

			_, err := engine.Run(testItem, testProject)

			require.ErrorIs(t, err, errNotReady)
			require.ErrorContains(t, err, tt.reason)
			assert.Equal(t, []string{"claim:12", "release:12"}, harness.life.seen(), "the pull request is not marked ready, and the claim is still released")
		})
	}
}

func TestRun_TheMergeStepRefusesWhenTheForgeCannotBeRead(t *testing.T) {
	t.Parallel()

	harness := shipHarness(t)
	harness.forge.err = errFake
	engine := harness.engine(t)

	_, err := engine.Run(testItem, testProject)

	require.ErrorIs(t, err, errFake)
	require.ErrorContains(t, err, "read the pull request of item 12")
	assert.Equal(t, []string{"claim:12", "release:12"}, harness.life.seen())
}

func TestRun_TheMergeStepMarksACleanPullRequestReady(t *testing.T) {
	t.Parallel()

	harness := shipHarness(t)
	engine := harness.engine(t)

	_, err := engine.Run(testItem, testProject)

	require.NoError(t, err)
	assert.Equal(t, []string{"claim:12", "ready:12", "release:12"}, harness.life.seen())
	assert.Equal(t, []string{testItem, testItem, testItem}, harness.forge.asked, "one read to mark ready, one for the head after it and one for the number the watch reads")
}

func TestRun_APushBetweenTheReadAndReadyPutsThePullRequestBackToDraftAndRefuses(t *testing.T) {
	t.Parallel()

	harness := shipHarness(t)
	harness.forge.state.Head = gatedHead
	harness.forge.pushAfterReady = "ddd"
	engine := harness.engine(t)

	_, err := engine.Run(testItem, testProject)

	require.ErrorIs(t, err, errNotReady)
	require.ErrorContains(t, err, `moved to "ddd" while it was marked ready`)

	_, drafts := harness.watch.seen()
	assert.Equal(t, []int{testPull}, drafts, "the unreviewed head is not left ready")
}

func TestReady_ACrashAfterTheReadyCallPinsTheSameHead(t *testing.T) {
	t.Parallel()

	h := mergeHarness(t, merge.Result{Stop: merge.Done})
	deps := h.deps()
	deps.Forge = &crashForge{forge: h.forge, crash: newCrash(t, h), on: "ready"}

	result := crashAndResume(t, h, deps, shipCall("merge = True, "))

	readyCalls := slices.DeleteFunc(h.life.seen(), func(step string) bool { return step != "ready:"+testItem })
	assert.Len(t, readyCalls, 2, "the cut-off ready step ran its call again")
	assert.Equal(t, []string{testProject + " " + testItem + " " + gatedHead}, h.merge.asked, "the merge ran once, at the head that the second ready step pinned")
	assert.Equal(t, "merged", result.Merge)
}

func TestRun_AFailedReadyCallFailsTheStepAndReadsNoHead(t *testing.T) {
	t.Parallel()

	harness := shipHarness(t)
	harness.forge.state.Head = gatedHead
	harness.life.failing["ready"] = true
	engine := harness.engine(t)

	_, err := engine.Run(testItem, testProject)

	require.ErrorIs(t, err, errFake)
	assert.Equal(t, []string{testItem}, harness.forge.asked, "the head is read again only after a ready call that worked")
}

func TestRun_APushAfterReadyThatCannotBeDraftedNamesBothFailures(t *testing.T) {
	t.Parallel()

	harness := shipHarness(t)
	harness.forge.state.Head = gatedHead
	harness.forge.pushAfterReady = "ddd"
	harness.watch.draftErr = errFake
	engine := harness.engine(t)

	_, err := engine.Run(testItem, testProject)

	require.ErrorIs(t, err, errFake)
	require.ErrorContains(t, err, "put pull request 7 back to draft")
}

func TestRun_AFailedReadOfTheHeadAfterReadyPutsThePullRequestBackToDraft(t *testing.T) {
	t.Parallel()

	harness := shipHarness(t)
	harness.forge.state.Head = gatedHead
	harness.forge.failFrom = 2
	engine := harness.engine(t)

	_, err := engine.Run(testItem, testProject)

	require.ErrorIs(t, err, errFake)

	_, drafts := harness.watch.seen()
	assert.Equal(t, []int{testPull}, drafts)
}
