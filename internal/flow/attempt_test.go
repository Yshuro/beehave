package flow

import (
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// resumeInsideBeat arms a hook that, inside the first renewal of the tick, stops the first attempt, resumes the run and waits for the resumed claim to be trusted.
// It returns the claim row of the resumed run as the hook read it.
func resumeInsideBeat(t *testing.T, harness *harness, engine *Engine, client *Client, earlier chan struct{}) *claimHold {
	t.Helper()

	var armed atomic.Bool

	resumed := &claimHold{}

	harness.beats.hook(func() {
		if !armed.CompareAndSwap(true, false) {
			return
		}

		stopRun(engine, testRun)
		assert.Eventually(t, func() bool { return isClosed(earlier) }, soonBound, pollEvery, "the first attempt ends")
		assert.Eventually(t, func() bool { open, err := engine.runOpen(testRun); return err == nil && !open }, soonBound, pollEvery, "the store records the end of the first attempt")

		resumeAndList(t, engine, client, earlier)

		assert.Eventually(t, func() bool {
			_, reason, early := engine.holds.distrust(testRun, engine.deps.Clock)

			return countOf(harness.life.seen(), "claim") == 2 && reason == "" && !early
		}, soonBound, pollEvery, "the resumed run holds a trusted claim")

		*resumed = claimRowOf(engine, testRun)
	})
	armed.Store(true)

	return resumed
}

func TestAttempt_AGoodRenewalOfTheEarlierAttemptLeavesTheTrustOfTheResumedClaim(t *testing.T) {
	t.Parallel()

	harness := newHarness(t)
	harness.flowIs(t, waitFlow)

	engine := harness.engine(t)
	client := harness.client(t, testVersion)

	done := runInBackground(engine, testItem)
	waitOn(t, ownerAsks(t, harness))

	resumed := resumeInsideBeat(t, harness, engine, client, endedOf(engine, testRun))

	harness.tick(t)
	require.Error(t, receive(t, done).err, "the first attempt ended at its stop")

	require.False(t, resumed.renewed.IsZero(), "the resumed claim was trusted inside the renewal")
	require.Equal(t, resumed.renewed, claimRowOf(engine, testRun).renewed, "the good renewal of the earlier attempt stamped the claim of the resumed run")
}

func TestAttempt_AFailedRenewalOfTheEarlierAttemptLeavesTheResumedClaimWithoutAFailure(t *testing.T) {
	t.Parallel()

	harness := newHarness(t)
	harness.flowIs(t, waitFlow)

	engine := harness.engine(t)
	client := harness.client(t, testVersion)

	done := runInBackground(engine, testItem)
	waitOn(t, ownerAsks(t, harness))

	// Only the token of the first take fails; the resumed run renews under its fresh token.
	harness.beats.fail(testOwner+testItem, "", errFake)
	resumed := resumeInsideBeat(t, harness, engine, client, endedOf(engine, testRun))

	harness.tick(t)
	require.Error(t, receive(t, done).err, "the first attempt ended at its stop")

	require.Empty(t, resumed.failure, "the resumed claim held no failure inside the renewal")
	require.Empty(t, claimRowOf(engine, testRun).failure, "the failed renewal of the earlier attempt wrote its failure into the claim of the resumed run")
}
