package flow

import (
	"context"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// retryFile is a parent that spawns three children with retries, gathers them, and records the outcome and the attempts of each in a last command.
func retryFile(gatherArgs string) string {
	return `def worker(item):
    command(name = "w", run = ["w"])

def main(item):
    runs = spawn(items = ["21", "22", "23"], entry = "worker", retries = 1, name = "fan")
    done = gather(runs, ` + gatherArgs + `name = "fan")
    command(name = "after", run = ["after"] + [r.item + ":" + ("ok" if r.ok else "failed") + ":" + str(r.attempts) for r in done.results])
`
}

// failTimes makes the commands of one item fail for its first n commands. It returns the count of the commands of the item that started.
func failTimes(h *harness, item string, n int32) *atomic.Int32 {
	var seen atomic.Int32

	h.commands.before = func(_ context.Context, request CommandRequest) error {
		if request.Item == item && seen.Add(1) <= n {
			return errFake
		}

		return nil
	}

	return &seen
}

func TestSpawnRetries_AChildThatFailsOnceStartsAgainAsItsNextAttemptAndTheRaiseDoesNotFire(t *testing.T) {
	t.Parallel()

	h := newHarness(t)
	tries := failTimes(h, "22", 1)
	engine := h.engine(t)

	_, err := engine.RunFile(testItem, testProject, writeFlow(t, retryFile(`fail = "raise", `)))

	require.NoError(t, err, "a failure with retries left does not raise")
	assert.Equal(t, []string{"after 21:ok:1 22:ok:2 23:ok:1"}, h.commands.ranBy(testItem))
	assert.Equal(t, int32(2), tries.Load(), "the child ran its command in each attempt")

	last, _, err := latestRun(engine.ctx, "22")
	require.NoError(t, err)
	assert.Equal(t, 2, last.Attempt, "the retry is the next attempt of the item")
	assert.Equal(t, testRun, last.Status.ParentWorkflowID, "the retry is a child of the parent")
	assert.Equal(t, []string{"spawn:fan#1", "gather:fan#1", "gather:fan#1.retry1", "gather:fan#1.1", "command:after#1"}, stepNames(t, engine))
}

func TestSpawnRetries_AChildThatFailsEachAttemptIsAFailedOutcomeAfterItsRetries(t *testing.T) {
	t.Parallel()

	h := newHarness(t)
	tries := failTimes(h, "22", 5)

	_, err := h.engine(t).RunFile(testItem, testProject, writeFlow(t, retryFile(`fail = "return", `)))

	require.NoError(t, err)
	assert.Equal(t, []string{"after 21:ok:1 22:failed:2 23:ok:1"}, h.commands.ranBy(testItem))
	assert.Equal(t, int32(2), tries.Load(), "one retry, then no more")
}

// secondPickFails picks a login for each item, and fails the second pick of its item: the pick of the start again.
type secondPickFails struct {
	noReset

	item  string
	picks *atomic.Int32
}

func (p secondPickFails) Pick(_ context.Context, _, item string, _ []string, _ bool) (string, error) {
	if item == p.item && p.picks.Add(1) == 2 {
		return "", errFake
	}

	return testLogin, nil
}

func TestSpawnRetries_AStartAgainWhoseLoginCannotBePickedFailsTheParent(t *testing.T) {
	t.Parallel()

	h := newHarness(t)
	h.picker = secondPickFails{item: "22", picks: &atomic.Int32{}}
	tries := failTimes(h, "22", 5)

	_, err := h.engine(t).RunFile(testItem, testProject, writeFlow(t, retryFile(`fail = "return", `)))

	require.ErrorContains(t, err, errFake.Error())
	assert.Equal(t, int32(1), tries.Load(), "no second attempt started")
	assert.Empty(t, h.commands.ranBy(testItem))
}

func TestSpawnRetries_ThePlanOfAStartAgainReportsAStoppedEngine(t *testing.T) {
	t.Parallel()

	engine := newHarness(t).engine(t)
	require.NoError(t, engine.Shutdown())

	_, err := engine.planRestarts(t.Context(), ItemInput{Item: testItem, Project: testProject}, []spawnedRun{{Item: "21", Run: "item-21-r1", Retries: 1}}, []int{0})

	require.Error(t, err, "the plan reads the latest run of the item")
}

func TestSpawnRetries_ARaiseFiresWhenTheRetriesAreSpent(t *testing.T) {
	t.Parallel()

	h := newHarness(t)
	tries := failTimes(h, "22", 5)

	_, err := h.engine(t).RunFile(testItem, testProject, writeFlow(t, retryFile(`fail = "raise", `)))

	require.ErrorContains(t, err, errFake.Error())
	assert.Equal(t, int32(2), tries.Load())
	assert.Empty(t, h.commands.ranBy(testItem), "the raise ends the parent")
}
