package flow

import (
	"errors"
	"strings"
	"testing"

	"github.com/dbos-inc/dbos-transact-golang/dbos"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestCapStop_TheStatusNoteNamesTheScopeTheCapAndTheSpend(t *testing.T) {
	t.Parallel()

	stop := CapStop{Scope: scopeTree, Cap: 5, Spent: 6.25}

	assert.Equal(t, "stopped at the tree cap $5, spent $6.25", stop.text())
}

func TestCapStop_ARunHeldAtACapShowsTheNoteInItsStatusLines(t *testing.T) {
	t.Parallel()

	rig := newAgentRig(t)
	rig.h.opts.MaxRunBudget = 5
	rig.reply("", costing(3))

	stop := holdOnCap(t, rig, capFile(t, threeFlow))
	require.NotNil(t, stop)

	notes := RunProgress{CapStop: stop}.Notes()

	assert.Equal(t, []string{"stopped at the run cap $5, spent $6"}, notes)
}

// stoppedTree returns a finished tree of three item runs whose engine is shut down, so that every read of the store fails.
func stoppedTree(t *testing.T) *Engine {
	t.Helper()

	rig := newAgentRig(t)
	engine := deepTree(t, rig)
	require.NoError(t, engine.Shutdown())

	return engine
}

// failingRead is a context of the engine whose store fails at one numbered read, counted over the reads of runs and of steps from 1.
// With empty set, the read finds no rows instead of failing.
type failingRead struct {
	dbos.Context

	at    int
	empty bool
	seen  int
}

func (f *failingRead) hit() bool {
	f.seen++

	return f.seen == f.at
}

func (f *failingRead) ListWorkflows(_ dbos.Client, opts ...dbos.ListWorkflowsOption) ([]dbos.WorkflowStatus, error) {
	if f.hit() {
		if f.empty {
			return nil, nil
		}

		return nil, errors.New("the store failed")
	}

	return dbos.ListWorkflows(f.Context, opts...)
}

func (f *failingRead) GetWorkflowSteps(_ dbos.Client, id string, opts ...dbos.GetWorkflowStepsOption) ([]dbos.StepInfo, error) {
	if f.hit() {
		return nil, errors.New("the store failed")
	}

	return dbos.GetWorkflowSteps(f.Context, id, opts...)
}

func TestCapStop_TheTreeSumReportsTheReadThatFailedAtEachLevel(t *testing.T) {
	t.Parallel()

	engine := deepTree(t, newAgentRig(t))
	root := runID("1", 1)

	// The sum of the root reads: steps of 1, children of 1, steps of 2, children of 2, steps of 3, children of 3.
	for read, words := range map[int]string{
		1: "read the steps of " + root,
		2: "list the item runs",
		3: "read the steps of " + runID("2", 1),
		4: "list the item runs",
		5: "read the steps of " + runID("3", 1),
		6: "list the item runs",
	} {
		_, err := runAndBelowSpend(&failingRead{Context: engine.ctx, at: read}, root)
		require.ErrorContains(t, err, words, "read %d", read)
		require.ErrorContains(t, err, "the store failed", "read %d", read)
	}

	spent, err := runAndBelowSpend(&failingRead{Context: engine.ctx}, root)
	require.NoError(t, err, "no read fails")
	assert.InDelta(t, 6.0, spent.Cost, 0)
}

func TestCapStop_TheClimbReportsAReadThatFailedAboveTheRun(t *testing.T) {
	t.Parallel()

	engine := deepTree(t, newAgentRig(t))

	// The climb from the grandchild reads the run, then its parent.
	_, err := rootRun(&failingRead{Context: engine.ctx, at: 1}, runID("3", 1))
	require.ErrorContains(t, err, "the store failed")

	_, err = rootRun(&failingRead{Context: engine.ctx, at: 2}, runID("3", 1))
	require.ErrorContains(t, err, "the store failed")
}

func TestCapStop_TheClimbEndsAtTheRunWhenNoItemRunIsAboveIt(t *testing.T) {
	t.Parallel()

	engine := deepTree(t, newAgentRig(t))

	// The run names a parent, but the read of the parent finds no item run: the run is the root.
	root, err := rootRun(&failingRead{Context: engine.ctx, at: 2, empty: true}, runID("3", 1))

	require.NoError(t, err)
	assert.Equal(t, runID("3", 1), root)
}

func TestCapStop_ARunOutsideAWorkflowCannotSumItsTree(t *testing.T) {
	t.Parallel()

	engine := deepTree(t, newAgentRig(t))
	s := &script{e: engine, ctx: engine.ctx, in: ItemInput{Item: "1"}}

	_, err := s.treeSpend()

	require.ErrorContains(t, err, "read the id of the run of item 1")
}

func TestCapStop_ARunWhoseClimbToTheRootFailsCannotSumItsTree(t *testing.T) {
	t.Parallel()

	rig := newAgentRig(t)
	opts := rig.h.opts
	state, err := openStore(opts)
	require.NoError(t, err)

	held, err := claimFolder(state.dir, applicationVersion(opts.Version), ModeRun)
	require.NoError(t, err)

	engine := newEngine(opts, rig.h.deps(), state, held, moduleRegistry(t))
	require.NoError(t, engine.open(t.Context(), opts, state))
	t.Cleanup(func() { require.NoError(t, engine.Shutdown()) })

	// The run is a real workflow, so its own id reads fine. The reads of the engine fail at the first read of the climb.
	sum := func(ctx dbos.Context, in ItemInput) (string, error) {
		failing := &Engine{ctx: &failingRead{Context: engine.ctx, at: 1}}
		s := &script{e: failing, ctx: ctx, in: in}

		_, err := s.treeSpend()
		if err == nil {
			return "", errors.New("the sum did not fail")
		}

		return err.Error(), nil
	}

	dbos.RegisterWorkflow(engine.ctx, sum, dbos.WithWorkflowName(itemName))
	require.NoError(t, dbos.Launch(engine.ctx))

	handle, err := dbos.RunWorkflow(engine.ctx, sum, ItemInput{Item: "1", Project: testProject}, dbos.WithWorkflowID(runID("1", 1)))
	require.NoError(t, err)

	words, err := handle.GetResult()
	require.NoError(t, err)
	assert.Contains(t, words, "the store failed")
}

func TestCapStop_AStepHeldForTwoLooksRecordsEachLookUnderItsOwnNumber(t *testing.T) {
	t.Parallel()

	rig := newAgentRig(t)
	rig.h.opts.MaxRunBudget = 5
	rig.reply("", costing(3))

	engine := rig.h.engine(t)
	done := runInBackground2(engine, capFile(t, threeFlow))

	var looks []string

	eventually(t, func() bool {
		records, err := dbos.GetWorkflowSteps(engine.ctx, testRun)
		if err != nil {
			return false
		}

		looks = looks[:0]

		for _, record := range records {
			if strings.Contains(record.StepName, "/budget") {
				looks = append(looks, record.StepName)
			}
		}

		return len(looks) >= 6
	})

	require.NoError(t, engine.Shutdown())
	<-done

	assert.Equal(t, []string{
		"agent:a#1/budget1.0", "agent:b#2/budget1.0",
		"agent:c#3/budget1.0", "agent:c#3/budget1.1", "agent:c#3/budget1.2", "agent:c#3/budget1.3",
	}, looks[:6], "the held step c records looks 0, 1, 2 and 3 in order")
}

func TestCapStop_AReadThatFailsReachesTheCallerWithItsWords(t *testing.T) {
	t.Parallel()

	engine := stoppedTree(t)

	_, err := runSpend(engine.ctx, runID("1", 1))
	require.ErrorContains(t, err, "read the steps of "+runID("1", 1))

	_, err = itemRuns(engine.ctx, dbos.WithFilterParentWorkflowID(runID("1", 1)))
	require.ErrorContains(t, err, "list the item runs")

	_, err = runAndBelowSpend(engine.ctx, runID("1", 1))
	require.ErrorContains(t, err, "read the steps of")

	_, err = rootRun(engine.ctx, runID("3", 1))
	require.ErrorContains(t, err, "list the item runs")
}
