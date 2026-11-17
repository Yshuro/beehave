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

	_, err := engine.markReady(ctx, testProject, testItem, gatedHead)

	require.ErrorIs(t, err, context.Canceled, "the failed read stays in the error")
	require.ErrorIs(t, err, context.DeadlineExceeded, "the hung return ends at its limit and is reported")
	require.ErrorContains(t, err, "put pull request 7 back to draft")
	require.ErrorContains(t, err, "gh pr ready --undo 7", "the error names the step to do by hand")
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
	require.ErrorContains(t, err, "gh pr ready --undo 7")
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
	require.ErrorContains(t, err, "gh pr ready --undo <number>")

	_, drafts := h.watch.seen()
	assert.Empty(t, drafts, "no draft call is made with no number")
}
