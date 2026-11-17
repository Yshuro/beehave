package flow

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/tobocop2/beehave/internal/merge"
	"github.com/tobocop2/beehave/internal/review"
)

// cancelingForge cancels the context of the step right after the ready call and fails each read on a canceled context, as a forge does.
type cancelingForge struct {
	*fakeForge
	cancel context.CancelFunc
}

func (f *cancelingForge) Ready(ctx context.Context, project, item string) error {
	err := f.fakeForge.Ready(ctx, project, item)
	f.cancel()

	return err
}

func (f *cancelingForge) Pull(ctx context.Context, project, item string) (review.Standing, error) {
	if err := ctx.Err(); err != nil {
		return review.Standing{}, err
	}

	return f.fakeForge.Pull(ctx, project, item)
}

// drafting reaches the forge only on a live context, as a forge does. It records the deadline of each call that has one.
// With hang set, a live call waits until its context ends.
type drafting struct {
	mu        sync.Mutex
	reached   []int
	deadlines []time.Time
	hang      bool
}

func (d *drafting) SetDraft(ctx context.Context, _ string, pr int, draft bool) error {
	if at, ok := ctx.Deadline(); ok {
		d.mu.Lock()
		d.deadlines = append(d.deadlines, at)
		d.mu.Unlock()
	}

	if err := ctx.Err(); err != nil {
		return err
	}

	if d.hang {
		<-ctx.Done()

		return ctx.Err()
	}

	if draft {
		d.mu.Lock()
		d.reached = append(d.reached, pr)
		d.mu.Unlock()
	}

	return nil
}

func (d *drafting) Watch(context.Context, string, int) (review.Outcome, error) {
	return review.Outcome{}, nil
}

func cancelRig(t *testing.T) (*Engine, context.Context, *drafting) {
	t.Helper()

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

	harness := shipHarness(t)
	harness.forge.state.Head = gatedHead
	watch := &drafting{}
	engine := &Engine{deps: Deps{Forge: &cancelingForge{fakeForge: harness.forge, cancel: cancel}, Watch: watch}}

	return engine, ctx, watch
}

func TestReady_ACancelAfterTheReadyCallStillPutsThePullRequestBackToDraft(t *testing.T) {
	t.Parallel()

	engine, ctx, watch := cancelRig(t)

	_, err := engine.markReady(ctx, testProject, testItem, gatedHead)

	require.ErrorIs(t, err, context.Canceled)
	assert.Equal(t, []int{testPull}, watch.reached, "one return to draft reaches the forge although the step context is canceled")
}

func TestReady_AReturnToDraftThatHangsEndsAtItsLimitAndIsPartOfTheError(t *testing.T) {
	t.Parallel()

	engine, ctx, watch := cancelRig(t)
	watch.hang = true
	engine.deps.Clock.Draft = 20 * time.Millisecond

	before := time.Now()
	_, err := engine.markReady(ctx, testProject, testItem, gatedHead)

	require.ErrorIs(t, err, context.Canceled, "the failed read stays in the error")
	require.ErrorIs(t, err, context.DeadlineExceeded, "the hung return ends at its limit and is reported")
	require.ErrorContains(t, err, "cannot make pull request 7 a draft again")
	require.ErrorContains(t, err, "convert it to a draft by hand", "the error says what to do by hand")
	require.Len(t, watch.deadlines, 1)
	assert.False(t, watch.deadlines[0].Before(before.Add(20*time.Millisecond)), "the deadline comes from the clock override, not from the default")
	assert.Less(t, watch.deadlines[0].Sub(before), 5*time.Second, "the deadline is the override after the call start, far below the default of 30 seconds")
}

func TestReady_TheReturnToDraftEndsAtTheDefaultLimit(t *testing.T) {
	t.Parallel()

	engine, ctx, watch := cancelRig(t)

	before := time.Now()
	_, err := engine.markReady(ctx, testProject, testItem, gatedHead)
	after := time.Now()

	require.ErrorIs(t, err, context.Canceled)
	require.Len(t, watch.deadlines, 1)

	assert.Equal(t, 30*time.Second, draftTimeout, "the limit of the return to draft is 30 seconds")
	assert.False(t, watch.deadlines[0].Before(before.Add(draftTimeout)), "the deadline is at least the limit after the call")
	assert.False(t, watch.deadlines[0].After(after.Add(draftTimeout)), "the deadline is at most the limit after the call")
}

func TestMergeStep_AFailedMergePutsThePullRequestBackToDraft(t *testing.T) {
	t.Parallel()

	tests := map[string]struct {
		result merge.Result
		err    error
	}{
		"a_refused_merge":              {result: merge.Result{Stop: merge.Refused, Lines: []string{"checks are red"}}},
		"a_merge_that_cannot_be_asked": {err: errFake},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			h := mergeHarness(t, tc.result)
			h.merge.err = tc.err

			_, err := walkText(t, h, shipFlow("merge = True, "))

			require.Error(t, err)

			_, drafts := h.watch.seen()
			assert.Equal(t, []int{testPull}, drafts, "the pull request that was marked ready goes back to draft")
		})
	}
}

func TestMergeStep_AFailedMergeThatCannotReachTheDraftCallNamesBothFailuresAndTheStepByHand(t *testing.T) {
	t.Parallel()

	h := mergeHarness(t, merge.Result{Stop: merge.Refused, Lines: []string{"checks are red"}})
	h.watch.draftErr = errFake

	_, err := walkText(t, h, shipFlow("merge = True, "))

	require.ErrorContains(t, err, errMergeRefused.Error())
	require.ErrorIs(t, err, errFake)
	require.ErrorContains(t, err, "convert it to a draft by hand")
}

func TestMergeStep_AMergeThatWorksLeavesThePullRequestReady(t *testing.T) {
	t.Parallel()

	h := mergeHarness(t, merge.Result{Stop: merge.Done})

	_, err := walkText(t, h, shipFlow("merge = True, "))

	require.NoError(t, err)

	_, drafts := h.watch.seen()
	assert.Empty(t, drafts)
}

func TestMergeStep_AFailedMergeWhoseNumberCannotBeReadNamesBothFailuresAndTheStepByHand(t *testing.T) {
	t.Parallel()

	h := mergeHarness(t, merge.Result{Stop: merge.Refused, Lines: []string{"checks are red"}})
	h.forge.failFrom = 5 // the fifth read of the forge is the one that the return to draft makes after the failed merge

	_, err := walkText(t, h, shipFlow("merge = True, "))

	require.ErrorContains(t, err, errMergeRefused.Error())
	require.ErrorIs(t, err, errFake)
	require.ErrorContains(t, err, "convert it to a draft by hand")

	_, drafts := h.watch.seen()
	assert.Empty(t, drafts, "no draft call is made with no number")
}

// heldInTheWait runs an item into the wait of a queued merge and returns the harness, the engine and the channel of its run.
// The first poll queues the merge; the next poll of the resumed run merges it.
func heldInTheWait(t *testing.T) (*harness, *Engine, <-chan runOutcome) {
	t.Helper()

	harness, _ := queuedHarness(t, merge.Result{Stop: merge.Refused, Reason: merge.Queued}, merge.Result{Stop: merge.Done})
	harness.sleep = time.Minute

	engine, err := NewEngine(t.Context(), harness.opts, harness.deps())
	require.NoError(t, err)
	require.NoError(t, engine.Launch())

	done := runInBackground(engine, testItem)
	eventually(t, func() bool { return harness.merge.askedCount() == 1 })

	return harness, engine, done
}

func TestMergeStep_AStopOfTheEngineInTheWaitLeavesThePullRequestReadyAndTheResumeMergesIt(t *testing.T) {
	t.Parallel()

	harness, engine, done := heldInTheWait(t)

	require.NoError(t, engine.Shutdown())
	<-done

	_, drafts := harness.watch.seen()
	require.Empty(t, drafts, "an engine stop does not draft the pull request: the resumed run continues the merge")

	harness.sleep = time.Millisecond

	result, err := harness.engine(t).Run(testItem, testProject)

	require.NoError(t, err)
	assert.Equal(t, "merged", result.Merge)
	assert.Equal(t, 1, countOf(harness.life.seen(), "ready"), "the resumed run does not mark the pull request ready again, so it must still be ready")

	_, drafts = harness.watch.seen()
	assert.Empty(t, drafts)
}

func TestMergeStep_ACancelInTheWaitOfAQueuedMergePutsThePullRequestBackToDraft(t *testing.T) {
	t.Parallel()

	harness, _, done := heldInTheWait(t)

	require.NoError(t, harness.client(t, testVersion).Cancel(testItem))

	select {
	case outcome := <-done:
		require.Error(t, outcome.err)
	case <-time.After(eventTimeout):
		t.Fatal("the run did not end after the cancel")
	}

	_, drafts := harness.watch.seen()
	assert.Equal(t, []int{testPull}, drafts, "the owner canceled the run, so nothing resumes the merge and the pull request goes back to draft")
}
