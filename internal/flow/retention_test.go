package flow

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// finishedRun runs one item to the end and stops the engine, so the next Launch applies the retention to it.
func finishedRun(t *testing.T, harness *harness) {
	t.Helper()

	first, err := NewEngine(t.Context(), harness.opts, harness.deps())
	require.NoError(t, err)
	require.NoError(t, first.Launch())

	_, err = first.Run(testItem, testProject)
	require.NoError(t, err)
	require.NoError(t, first.Shutdown())

	ended := time.Now().UnixMilli()

	waitOn(t, condition(func() bool { return time.Now().UnixMilli() > ended }))
}

func TestLaunch_TheRetentionOfTheOptionsSetsTheAgeAtWhichAFinishedRunIsDeleted(t *testing.T) {
	t.Parallel()

	harness := newHarness(t)
	finishedRun(t, harness)

	harness.opts.Retention = time.Nanosecond
	launchOn(t, harness, harness.deps())

	rows, err := harness.client(t, testVersion).Status()
	require.NoError(t, err)
	assert.Empty(t, rows, "a run older than the retention of the options is deleted")
}

func TestLaunch_AFinishedRunStaysWhileTheRetentionOfTheOptionsIsLonger(t *testing.T) {
	t.Parallel()

	harness := newHarness(t)
	finishedRun(t, harness)

	harness.opts.Retention = time.Hour
	launchOn(t, harness, harness.deps())

	rows, err := harness.client(t, testVersion).Status()
	require.NoError(t, err)
	assert.Len(t, rows, 1)
}

func TestLaunch_OptionsWithNoRetentionKeepARunThatIsYoungerThanTheDefault(t *testing.T) {
	t.Parallel()

	harness := newHarness(t)
	finishedRun(t, harness)

	harness.opts.Retention = 0
	launchOn(t, harness, harness.deps())

	rows, err := harness.client(t, testVersion).Status()
	require.NoError(t, err)
	assert.Len(t, rows, 1, "a run of a few milliseconds is far younger than the default")
}

func TestLaunch_KeepRunsDeletesNothingWhateverTheAge(t *testing.T) {
	t.Parallel()

	harness := newHarness(t)
	finishedRun(t, harness)

	harness.opts.KeepRuns = true
	deps := harness.deps()
	deps.Clock.Retain = time.Nanosecond
	launchOn(t, harness, deps)

	rows, err := harness.client(t, testVersion).Status()
	require.NoError(t, err)
	assert.Len(t, rows, 1, "a store that keeps its runs deletes none, so item_read of an earlier run and earlier_calls keep their data")
}

func TestLaunch_OptionsWithNoRetentionKeepARunOfSixDaysAndDeleteOneOfEight(t *testing.T) {
	t.Parallel()

	harness := newHarness(t)
	finishedRun(t, harness)

	harness.opts.Retention = 0
	client := harness.client(t, testVersion)
	started := time.Now()

	sixDays := harness.deps()
	sixDays.Clock.Wall = func() time.Time { return started.Add(6 * 24 * time.Hour) }
	stop := launchOn(t, harness, sixDays)

	rows, err := client.Status()
	require.NoError(t, err)
	assert.Len(t, rows, 1, "a run that finished 6 days ago stays under the default of 7 days")

	stop()

	eightDays := harness.deps()
	eightDays.Clock.Wall = func() time.Time { return started.Add(8 * 24 * time.Hour) }
	launchOn(t, harness, eightDays)

	rows, err = client.Status()
	require.NoError(t, err)
	assert.Empty(t, rows, "a run that finished 8 days ago is deleted under the default of 7 days")
}
