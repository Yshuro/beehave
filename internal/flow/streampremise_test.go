package flow

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/dbos-inc/dbos-transact-golang/dbos"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// These tests pin the behavior of the DBOS streams that the cost of a failed agent run relies on:
//   - An entry written in the workflow body outlives a rewind to a step before it, and the rerun appends its own entry.
//   - A replay that finds the step record of a write appends no second entry.
//   - A snapshot read from inside a step of an open run returns the entries at once.
//
// One more premise rests on the DBOS source, not on a test: a write from the workflow body and its step record commit in one
// transaction, so no entry is committed without its record. In DBOS v1.8.0, WriteStream runs the write through runAsTxn, which
// checks the record, writes the entry and records the step in one database transaction.

// premiseKey is the stream that the premise tests write.
const premiseKey = "premise"

// premiseWait is the longest that a premise test waits for a run before it fails instead of hanging.
const premiseWait = 20 * time.Second

// premiseContext opens a DBOS context on a fresh store of a harness, registers the workflow as an item run and launches it.
func premiseContext[R any](t *testing.T, flow dbos.Workflow[int, R]) dbos.Context {
	t.Helper()

	state, err := openStore(newHarness(t).opts)
	require.NoError(t, err)

	t.Cleanup(func() { _ = state.log.Close() })

	flowCtx, err := dbos.NewContext(t.Context(), dbos.Config{
		AppName: appName, DatabaseURL: state.url, ApplicationVersion: applicationVersion(testVersion), ExecutorID: executorName,
	})
	require.NoError(t, err)

	dbos.RegisterWorkflow(flowCtx, flow, dbos.WithWorkflowName(itemName))
	require.NoError(t, dbos.Launch(flowCtx))
	t.Cleanup(func() { require.NoError(t, dbos.Shutdown(flowCtx, shutdownTimeout)) })

	return flowCtx
}

// resultWithin waits for the result of a run, and fails the test when the run takes longer than premiseWait.
func resultWithin[R any](t *testing.T, handle dbos.WorkflowHandle[R]) (R, error) {
	t.Helper()

	type outcome struct {
		value R
		err   error
	}

	done := make(chan outcome, 1)

	go func() {
		value, err := handle.GetResult()
		done <- outcome{value, err}
	}()

	select {
	case got := <-done:
		return got.value, got.err
	case <-time.After(premiseWait):
		require.FailNow(t, "the run did not end in time")

		var zero R

		return zero, nil
	}
}

var errPremiseFirstRun = errors.New("the first run fails after the write")

func TestDBOSStream_AnEntryWrittenInTheWorkflowBodyOutlivesARewindToAStepBeforeIt(t *testing.T) {
	t.Parallel()

	var runs atomic.Int32

	// Step 0 runs before the write, step 1 is the write, step 2 fails on the first run only. Each run writes its own number.
	flow := func(ctx dbos.Context, _ int) (int, error) {
		if _, err := dbos.RunAsStep(ctx, func(context.Context) (int, error) { return 0, nil }, dbos.WithStepName("before")); err != nil {
			return 0, err
		}

		run := int(runs.Add(1))
		if err := dbos.WriteStream(ctx, premiseKey, run); err != nil {
			return 0, err
		}

		return dbos.RunAsStep(ctx, func(context.Context) (int, error) {
			if run == 1 {
				return 0, errPremiseFirstRun
			}

			return run, nil
		}, dbos.WithStepName("after"))
	}

	flowCtx := premiseContext(t, flow)

	handle, err := dbos.RunWorkflow(flowCtx, flow, 0, dbos.WithWorkflowID("premise-rewind"))
	require.NoError(t, err)

	_, err = resultWithin(t, handle)
	require.ErrorIs(t, err, errPremiseFirstRun)

	rewound, err := dbos.RewindWorkflow[int](flowCtx, "premise-rewind", dbos.WithRewindStartStep(0))
	require.NoError(t, err)

	got, err := resultWithin(t, rewound)
	require.NoError(t, err)
	assert.Equal(t, 2, got, "the rewind reran the write and the step after it")

	entries, _, err := dbos.ReadStream[int](flowCtx, "premise-rewind", premiseKey, dbos.WithReadStreamSnapshot())
	require.NoError(t, err)
	assert.Equal(t, []int{1, 2}, entries, "the entry of the first run outlives the rewind, and the rerun appends its own")
}

func TestDBOSStream_AReplayThatFindsTheRecordOfAWriteAppendsNoSecondEntry(t *testing.T) {
	t.Parallel()

	var runs atomic.Int32

	// Step 0 runs before the write, step 1 is the write, step 2 fails on the first run only. Each run offers its own number to the write.
	flow := func(ctx dbos.Context, _ int) (int, error) {
		if _, err := dbos.RunAsStep(ctx, func(context.Context) (int, error) { return 0, nil }, dbos.WithStepName("before")); err != nil {
			return 0, err
		}

		run := int(runs.Add(1))
		if err := dbos.WriteStream(ctx, premiseKey, run); err != nil {
			return 0, err
		}

		return dbos.RunAsStep(ctx, func(context.Context) (int, error) {
			if run == 1 {
				return 0, errPremiseFirstRun
			}

			return run, nil
		}, dbos.WithStepName("after"))
	}

	flowCtx := premiseContext(t, flow)

	handle, err := dbos.RunWorkflow(flowCtx, flow, 0, dbos.WithWorkflowID("premise-replay"))
	require.NoError(t, err)

	_, err = resultWithin(t, handle)
	require.ErrorIs(t, err, errPremiseFirstRun)

	// The rewind to step 2 keeps the record of the write at step 1, so the replay does not run the write again.
	replayed, err := dbos.RewindWorkflow[int](flowCtx, "premise-replay", dbos.WithRewindStartStep(2))
	require.NoError(t, err)

	got, err := resultWithin(t, replayed)
	require.NoError(t, err)
	assert.Equal(t, 2, got, "the body ran again and offered a second entry")

	entries, _, err := dbos.ReadStream[int](flowCtx, "premise-replay", premiseKey, dbos.WithReadStreamSnapshot())
	require.NoError(t, err)
	assert.Equal(t, []int{1}, entries, "the replay finds the step record of the write and appends no second entry")
}

func TestDBOSStream_ASnapshotReadInsideAStepOfAnOpenRunReturnsTheEntriesAtOnce(t *testing.T) {
	t.Parallel()

	var flowCtx dbos.Context

	// The run writes two entries, then reads its own stream from inside a step while it is open. A read that blocks until the run ends never returns.
	flow := func(ctx dbos.Context, _ int) (int, error) {
		for _, value := range []int{7, 3} {
			if err := dbos.WriteStream(ctx, premiseKey, value); err != nil {
				return 0, err
			}
		}

		id, err := dbos.GetWorkflowID(ctx)
		if err != nil {
			return 0, err
		}

		return dbos.RunAsStep(ctx, func(context.Context) (int, error) {
			entries, _, err := dbos.ReadStream[int](flowCtx, id, premiseKey, dbos.WithReadStreamSnapshot())

			sum := 0
			for _, entry := range entries {
				sum += entry
			}

			return sum, err
		}, dbos.WithStepName("read"))
	}

	flowCtx = premiseContext(t, flow)

	handle, err := dbos.RunWorkflow(flowCtx, flow, 0, dbos.WithWorkflowID("premise-snapshot"))
	require.NoError(t, err)

	got, err := resultWithin(t, handle)
	require.NoError(t, err)
	assert.Equal(t, 10, got, "the read returns both entries while the run is open")
}
