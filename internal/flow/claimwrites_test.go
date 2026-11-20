package flow

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/dbos-inc/dbos-transact-golang/dbos"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// claimLoser loses the claim of the run of the test item, as the renewal does when the tracker says that the claim is gone.
type claimLoser struct{ engine *Engine }

func (l *claimLoser) lose() { l.engine.lose(runID(testItem, 1), "a test lost the claim") }

// loseCommand is the program name that makes the fake command runner lose the claim, at the point of the flow where the test wants the loss.
const loseCommand = "lose"

// loseWhenAsked makes the command runner of the harness lose the claim when a flow runs the command named loseCommand.
func loseWhenAsked(h *harness, l *claimLoser) {
	h.commands.before = func(_ context.Context, request CommandRequest) error {
		if request.Run[0] == loseCommand {
			l.lose()
		}

		return nil
	}
}

// stepWrite is one write that a step of a workflow file makes: the call, and what the fake of the forge or of the tracker recorded.
type stepWrite struct {
	name    string
	call    string
	written func(h *harness, w *fakeWriter) []string
}

func stepWrites() []stepWrite {
	return []stepWrite{
		{"pr_comment", `pr_comment(text = "hello", key = "k", name = "c")`, func(h *harness, _ *fakeWriter) []string { return h.forge.notes }},
		{"pr_open", `pr_open(title = "t", base = "main", name = "o")`, func(h *harness, _ *fakeWriter) []string { return h.forge.opened }},
		{"item_create", `item_create(title = "t", key = "k", name = "i")`, func(_ *harness, w *fakeWriter) []string { return w.created }},
		{"item_status", `item_status(id = "n-1", status = "closed", name = "s")`, func(_ *harness, w *fakeWriter) []string { return w.statuses }},
		{"item_comment", `item_comment(id = "n-1", text = "hello", key = "k", name = "c")`, func(_ *harness, w *fakeWriter) []string { return w.notes }},
	}
}

// writeAfter runs the call after a command that loses the claim when lost is true. It returns what the fakes recorded for the call, and the error of the run.
func writeAfter(t *testing.T, w stepWrite, lost bool) ([]string, error) {
	t.Helper()

	h := newHarness(t)
	writer := newFakeWriter()
	h.writer = writer
	loser := &claimLoser{}
	loseWhenAsked(h, loser)

	loser.engine = launchedWith(t, h, func(*Deps) {})

	before := `command(name = "before", run = ["` + loseCommand + `"])`
	if !lost {
		before = `command(name = "before", run = ["keep"])`
	}

	_, err := loser.engine.RunFile(testItem, testProject, writeFlow(t, bodyFlow(before+"\n"+w.call)))

	return w.written(h, writer), err
}

func TestClaimWrites_AStepWritesWhileTheRunHoldsItsClaim(t *testing.T) {
	t.Parallel()

	for _, w := range stepWrites() {
		t.Run(w.name, func(t *testing.T) {
			t.Parallel()

			written, err := writeAfter(t, w, false)

			require.NoError(t, err)
			assert.Len(t, written, 1, "control: with the claim kept, the write reaches the fake")
		})
	}
}

func TestClaimWrites_AStepThatWritesAfterTheRunLostItsClaimWritesNothing(t *testing.T) {
	t.Parallel()

	for _, w := range stepWrites() {
		t.Run(w.name, func(t *testing.T) {
			t.Parallel()

			written, err := writeAfter(t, w, true)

			require.ErrorIs(t, err, errClaimLost)
			assert.Empty(t, written, "the claim was lost before the write, so nothing reaches the forge or the tracker")
		})
	}
}

func TestClaimWrites_AMergeOfARunThatNeverHeldAClaimLandsNothing(t *testing.T) {
	t.Parallel()

	rig := newBatchRig(t)
	deps := rig.deps()
	deps.Clock.Grace = 20 * time.Millisecond

	engine, err := NewEngine(t.Context(), rig.h.opts, deps)
	require.NoError(t, err)
	require.NoError(t, engine.Launch())
	t.Cleanup(func() { require.NoError(t, engine.Shutdown()) })

	// The merge runs as a child of its run and can wait behind a gate for a long time, so the claim is read again when the child reaches the merge call.
	handle, err := dbos.RunWorkflow(engine.ctx, engine.exclusive, ExclusiveInput{Kind: kindMerge, Run: "item-12-r9", Project: testProject, Item: "1", Head: rig.heads["1"]},
		dbos.WithQueue(engine.exclusiveQueue), dbos.WithQueuePartitionKey(testProject))
	require.NoError(t, err)

	out, err := handle.GetResult()

	require.NoError(t, err, "the exclusive run records a failed call as its failure")
	require.NotNil(t, out.Failure)
	assert.Contains(t, out.Failure.Text, errClaimLost.Error())
	assert.Empty(t, rig.merger.asked, "the run holds no claim, so the merger is never asked")
}

// heldRun is a run of the test item that holds its claim and waits in a command, so a test can lose the claim or queue a merge while the run is open.
type heldRun struct {
	engine   *Engine
	release  chan struct{}
	done     chan error
	waiting  chan struct{} // closed when the command starts to wait
	canceled chan struct{} // closed when the context of the waiting command ends
	once     sync.Once
}

// startHeldRun starts the run on an engine of deps and returns when the run holds its claim.
func startHeldRun(t *testing.T, h *harness, deps Deps) *heldRun {
	t.Helper()

	engine, err := NewEngine(t.Context(), h.opts, deps)
	require.NoError(t, err)
	require.NoError(t, engine.Launch())
	t.Cleanup(func() { require.NoError(t, engine.Shutdown()) })

	r := &heldRun{engine: engine, release: make(chan struct{}), done: make(chan error, 1), canceled: make(chan struct{}), waiting: make(chan struct{})}
	h.commands.before = func(ctx context.Context, request CommandRequest) error {
		if request.Run[0] != "wait" {
			return nil
		}

		close(r.waiting)

		select {
		case <-r.release:
			return nil
		case <-ctx.Done():
			close(r.canceled)

			return ctx.Err()
		}
	}

	file := writeFlow(t, bodyFlow(`command(name = "w", run = ["wait"])`))

	go func() {
		_, err := engine.RunFile(testItem, testProject, file)
		r.done <- err
	}()

	require.Eventually(t, func() bool { return len(engine.holds.snapshot()) == 1 }, 10*time.Second, 10*time.Millisecond)

	select {
	case <-r.waiting:
	case <-time.After(10 * time.Second):
		require.Fail(t, "the command of the run never started")
	}

	return r
}

// finish lets the waiting command end and returns the error of the run.
func (r *heldRun) finish() error {
	r.once.Do(func() { close(r.release) })

	return <-r.done
}

// queueMerge queues a merge child that names the run and returns the failure that the child recorded.
func queueMerge(t *testing.T, engine *Engine, run, head string) *Failure {
	t.Helper()

	handle, err := dbos.RunWorkflow(engine.ctx, engine.exclusive, ExclusiveInput{Kind: kindMerge, Run: run, Project: testProject, Item: "1", Head: head},
		dbos.WithQueue(engine.exclusiveQueue), dbos.WithQueuePartitionKey(testProject))
	require.NoError(t, err)

	out, err := handle.GetResult()
	require.NoError(t, err, "the exclusive run records a failed call as its failure")

	return out.Failure
}

func TestClaimWrites_AMergeOfARunThatHeldItsClaimAndLostItLandsNothing(t *testing.T) {
	t.Parallel()

	rig := newBatchRig(t)
	run := startHeldRun(t, rig.h, rig.deps())

	run.engine.lose(runID(testItem, 1), "a test lost the claim")
	failure := queueMerge(t, run.engine, runID(testItem, 1), rig.heads["1"])
	require.Error(t, run.finish(), "the run ends on the loss")

	require.NotNil(t, failure)
	assert.Contains(t, failure.Text, errClaimLost.Error())
	assert.Empty(t, rig.merger.asked, "the run held its claim and lost it, so the merger is never asked")
}

func TestClaimWrites_AMergeChildWithNoRunIsRefusedWhileOneRunIsOpen(t *testing.T) {
	t.Parallel()

	rig := newBatchRig(t)
	deps := rig.deps()
	deps.Clock.Grace = 20 * time.Millisecond
	run := startHeldRun(t, rig.h, deps)

	// A child that an older build recorded names no run. The one open run is no claim of the child.
	failure := queueMerge(t, run.engine, "", rig.heads["1"])
	require.NoError(t, run.finish())

	require.NotNil(t, failure)
	assert.Contains(t, failure.Text, errClaimLost.Error())
	assert.Empty(t, rig.merger.asked)
}

func TestClaimWrites_ACommandStepIsCancelledWhenTheRunLosesItsClaim(t *testing.T) {
	t.Parallel()

	h := newHarness(t)
	run := startHeldRun(t, h, h.deps())

	run.engine.lose(runID(testItem, 1), "a test lost the claim")

	select {
	case <-run.canceled:
	case <-time.After(10 * time.Second):
		require.Fail(t, "the command kept running after the claim was lost")
	}

	require.ErrorIs(t, run.finish(), errClaimLost)
}

// markCommand is the program of a step that must not run after the loss.
const markCommand = "mark"

func TestClaimWrites_AStepThatRunsAProgramAfterTheLossNeverRunsIt(t *testing.T) {
	t.Parallel()

	for _, builtin := range []string{"command", "write"} {
		call := builtin + `(name = "after", run = ["` + markCommand + `"])`

		t.Run(builtin, func(t *testing.T) {
			t.Parallel()

			h := newHarness(t)
			loser := &claimLoser{}
			loseWhenAsked(h, loser)
			loser.engine = launchedWith(t, h, func(*Deps) {})

			_, err := loser.engine.RunFile(testItem, testProject, writeFlow(t, bodyFlow(`command(name = "before", run = ["`+loseCommand+`"])`+"\n"+call)))

			require.ErrorIs(t, err, errClaimLost)
			assert.Equal(t, []string{loseCommand}, h.commands.ran(), "the program of the step that starts after the loss never runs: a command can write to the forge")
		})
	}
}

func TestClaimWrites_ACommandChildOfARunThatLostItsClaimNeverRunsItsProgram(t *testing.T) {
	t.Parallel()

	h := newHarness(t)
	run := startHeldRun(t, h, h.deps())

	run.engine.lose(runID(testItem, 1), "a test lost the claim")

	handle, err := dbos.RunWorkflow(run.engine.ctx, run.engine.exclusive, ExclusiveInput{Kind: kindCommand, Run: runID(testItem, 1), Project: testProject, Item: testItem, Request: CommandRequest{Project: testProject, Item: testItem, Run: []string{markCommand}}},
		dbos.WithQueue(run.engine.exclusiveQueue), dbos.WithQueuePartitionKey(testProject))
	require.NoError(t, err)

	out, err := handle.GetResult()
	require.NoError(t, err)
	require.Error(t, run.finish())

	require.NotNil(t, out.Failure)
	assert.Contains(t, out.Failure.Text, errClaimLost.Error())
	assert.NotContains(t, h.commands.ran(), markCommand)
}

func TestClaimWrites_ALossWhileTheMergeRunsCancelsTheContextOfTheMerge(t *testing.T) {
	t.Parallel()

	rig := newBatchRig(t)
	rig.merger.hold = make(chan context.Context, 1)
	run := startHeldRun(t, rig.h, rig.deps())

	handle, err := dbos.RunWorkflow(run.engine.ctx, run.engine.exclusive, ExclusiveInput{Kind: kindMerge, Run: runID(testItem, 1), Project: testProject, Item: "1", Head: rig.heads["1"]},
		dbos.WithQueue(run.engine.exclusiveQueue), dbos.WithQueuePartitionKey(testProject))
	require.NoError(t, err)

	var mergeCtx context.Context

	select {
	case mergeCtx = <-rig.merger.hold:
	case <-time.After(10 * time.Second):
		require.Fail(t, "the merge never started")
	}

	run.engine.lose(runID(testItem, 1), "a test lost the claim")

	select {
	case <-mergeCtx.Done():
	case <-time.After(10 * time.Second):
		require.Fail(t, "the merge kept running after the claim was lost: it runs under a context that the claim does not own")
	}

	_, err = handle.GetResult()
	require.NoError(t, err)
	require.Error(t, run.finish())
}

func TestClaimWrites_AMergeWhileTheRunHoldsItsClaimLands(t *testing.T) {
	t.Parallel()

	rig := newBatchRig(t)

	_, err := rig.walk(t, rig.call("1", ""))

	require.NoError(t, err)
	assert.Equal(t, []string{"1"}, rig.merger.landed(), "control: with the claim kept, the same call lands the member")
}

func TestClaimWrites_AHandOffThatLostItsClaimMarksNothingReady(t *testing.T) {
	t.Parallel()

	rig := newHandRig(t)
	rig.script("1", rig.view("1", "OPEN", true), rig.view("1", "OPEN", false), rig.view("1", "MERGED", false))

	loser := &claimLoser{}
	rig.host.pr.onRead = func(string, int) { loser.lose() }

	engine := rig.engine(t)
	loser.engine = engine

	_, err := engine.RunFile(testItem, testProject, writeFlow(t, bodyFlow(rig.body("1", ""))))

	require.ErrorIs(t, err, errClaimLost)
	assert.NotContains(t, rig.host.pr.marked(), "1:false", "the claim was lost before the ready mark, so the pull request is never marked ready (a return to draft is no ready mark)")
}
