package flow

import (
	"context"
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

func TestClaimWrites_AMergeThatWaitedInTheQueueForARunThatLostItsClaimLandsNothing(t *testing.T) {
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
