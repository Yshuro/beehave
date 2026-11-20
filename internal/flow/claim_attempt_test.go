package flow

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// gatedLifecycle holds the first two releases until the test lets them go. The first one ends with an error, so the release leaves its cancel marker.
type gatedLifecycle struct {
	lifecycle

	calls    atomic.Int32
	entered  [2]chan struct{}
	gate     [2]chan struct{}
	returned chan struct{}
}

func newGatedLifecycle(inner lifecycle) *gatedLifecycle {
	g := &gatedLifecycle{lifecycle: inner, returned: make(chan struct{})}
	for i := range g.gate {
		g.entered[i], g.gate[i] = make(chan struct{}), make(chan struct{})
	}

	return g
}

func (g *gatedLifecycle) Release(ctx context.Context, project, item, owner string) error {
	call := int(g.calls.Add(1)) - 1
	if call > 1 {
		return g.lifecycle.Release(ctx, project, item, owner)
	}

	close(g.entered[call])

	select {
	case <-g.gate[call]:
	case <-ctx.Done():
	}

	if call == 0 {
		defer close(g.returned)

		return errFake
	}

	return g.lifecycle.Release(ctx, project, item, owner)
}

func TestBeat_ARenewalOfTheEarlierAttemptThatSaysTheClaimIsGoneLeavesTheResumedRunRunning(t *testing.T) {
	t.Parallel()

	harness := newHarness(t)
	harness.flowIs(t, waitFlow)

	engine := harness.engine(t)
	client := harness.client(t, testVersion)

	done := runInBackground(engine, testItem)
	waitOn(t, ownerAsks(t, harness))

	earlier := endedOf(engine, testRun)

	var later chan struct{}

	var armed atomic.Bool

	// The renewal of the first attempt is slow: the run is canceled and resumed while the tracker answers.
	harness.beats.fail(testOwner+testItem, "heartbeat for session-x failed: it holds no claim row", nil)
	harness.beats.hook(func() {
		if !armed.CompareAndSwap(true, false) {
			return
		}

		engine.cancels.stop(testRun)
		assert.Eventually(t, func() bool { return isClosed(earlier) }, soonBound, pollEvery, "the first attempt ends")

		later = resumeAndList(t, engine, client, earlier)

		assert.Eventually(t, func() bool {
			_, reason, early := engine.holds.distrust(testRun, engine.deps.Clock)

			return countOf(harness.life.seen(), "claim") == 2 && reason == "" && !early
		}, soonBound, pollEvery, "the resumed run holds a trusted claim")
	})
	armed.Store(true)

	harness.tick(t)

	require.Error(t, receive(t, done).err, "the first attempt ended at its stop")
	require.NotNil(t, later, "the run resumed while the tracker answered the renewal")

	_, reason, _ := engine.holds.distrust(testRun, engine.deps.Clock)
	require.Empty(t, reason, "the answer for the first attempt marked the claim of the resumed run lost")
	require.False(t, isClosed(later), "the answer for the first attempt stopped the resumed run")
}

func TestCancel_TheEndOfTheEarlierGiveBackKeepsTheRecordOfTheGiveBackOfTheResumedRun(t *testing.T) {
	t.Parallel()

	harness := newHarness(t)
	harness.flowIs(t, waitFlow)

	var gated *gatedLifecycle

	engine := harness.engineWith(t, func(e *Engine) {
		gated = newGatedLifecycle(e.deps.Lifecycle)
		e.deps.Lifecycle = gated
	})
	client := harness.client(t, testVersion)

	done := runInBackground(engine, testItem)
	waitOn(t, ownerAsks(t, harness))

	earlier := endedOf(engine, testRun)

	require.NoError(t, client.Cancel(testItem))
	harness.tick(t)
	require.Error(t, receive(t, done).err)
	receive(t, gated.entered[0])

	// The first give-back waits in its release. The run resumes and is canceled again, and the tick cuts the resumed attempt.
	require.NoError(t, client.Resume(testItem))
	soon(t, func() bool { later := endedOf(engine, testRun); return later != nil && later != earlier }, "the engine lists the resumed attempt")
	soon(t, func() bool { return countOf(harness.life.seen(), "claim") == 2 }, "the resumed run takes its claim")

	require.NoError(t, client.Cancel(testItem))
	harness.tick(t)
	receive(t, gated.entered[1])

	// The first give-back ends while the give-back of the resumed run is still under way.
	close(gated.gate[0])
	receive(t, gated.returned)

	assert.Never(t, func() bool { return engine.cancels.begin(&attempt{id: testRun}) }, 300*time.Millisecond, pollEvery, "the end of the first give-back cleared the record of the second one")
	close(gated.gate[1])
}
