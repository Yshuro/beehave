package flow

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

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

// drafting reaches the forge only on a live context, as a forge does. With hang set, a call that is live waits until its context ends.
type drafting struct {
	mu       sync.Mutex
	calls    int
	reached  []int
	hang     bool
	deadline time.Duration
}

func (d *drafting) SetDraft(ctx context.Context, _ string, pr int, draft bool) error {
	d.mu.Lock()
	d.calls++
	d.mu.Unlock()

	if err := ctx.Err(); err != nil {
		return err
	}

	if d.hang {
		if at, ok := ctx.Deadline(); ok {
			d.mu.Lock()
			d.deadline = time.Until(at)
			d.mu.Unlock()
		}

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
	assert.Positive(t, watch.deadline, "the return has a deadline")
	assert.LessOrEqual(t, watch.deadline, 20*time.Millisecond)
}
