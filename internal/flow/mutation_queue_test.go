package flow

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"github.com/dbos-inc/dbos-transact-golang/dbos"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// holdingMutator holds every mutation job until the test lets it go, and counts the jobs that started.
type holdingMutator struct {
	started *atomic.Int32
	release chan struct{}
}

func (m holdingMutator) Mutate(ctx context.Context, _, _, _ string, _ MutationJob) (MutationRun, error) {
	m.started.Add(1)

	select {
	case <-m.release:
		return MutationRun{}, nil
	case <-ctx.Done():
		return MutationRun{}, ctx.Err()
	}
}

// mutationEngine starts an engine whose gate is gated and whose mutation runner is mutator. Every host has one mutation slot.
func mutationEngine(t *testing.T, gated supersedeGate, mutator holdingMutator) *Engine {
	t.Helper()

	return mutationEngineWithSlots(t, gated, mutator, 1)
}

// mutationEngineWithSlots is mutationEngine for a host with the given number of mutation slots.
func mutationEngineWithSlots(t *testing.T, gated supersedeGate, mutator holdingMutator, slots int) *Engine {
	t.Helper()

	h := newHarness(t)
	deps := h.deps()
	deps.Gate = gated
	deps.Mutation = mutator
	deps.MutationSlots = map[string]int{"box": slots}

	engine, err := NewEngine(t.Context(), h.opts, deps)
	require.NoError(t, err)
	dbos.RegisterWorkflow(engine.ctx, gateQueuer{engine}.queue, dbos.WithWorkflowName("test-queue-gate"))
	require.NoError(t, engine.Launch())
	t.Cleanup(func() { require.NoError(t, engine.Shutdown()) })

	return engine
}

// queueMutationJob queues one mutation job of an item on a host, as the mutation builtin does, and returns its handle.
func queueMutationJob(t *testing.T, engine *Engine, item, host string) dbos.WorkflowHandle[mutationOutput] {
	t.Helper()

	in := mutationInput{Project: testProject, Item: item, Host: host, Job: MutationJob{Head: "A", Command: "make mutation-pr"}}
	handle, err := dbos.RunWorkflow(engine.ctx, engine.mutationRun, in, dbos.WithQueue(engine.mutationQueues[host]))
	require.NoError(t, err)

	return handle
}

func TestHostQueue_AMutationJobNeverBlocksAGate(t *testing.T) {
	t.Parallel()

	t.Run("a gate that holds the host does not stop a mutation job on the same host", func(t *testing.T) {
		t.Parallel()

		gated := newSupersedeGate("")
		mutator := holdingMutator{started: &atomic.Int32{}, release: make(chan struct{})}
		engine := mutationEngine(t, gated, mutator)

		gate := queueHostGate(engine, "project-one", "1", "A", "box")
		eventually(t, startedCount(gated, 1))

		job := queueMutationJob(t, engine, "2", "box")
		eventually(t, func() bool { return mutator.started.Load() == 1 })

		close(gated.release)
		close(mutator.release)

		require.NoError(t, (<-gate).err)

		_, err := job.GetResult()
		require.NoError(t, err)
	})

	t.Run("a mutation job that holds its slot does not stop a gate on the same host", func(t *testing.T) {
		t.Parallel()

		gated := newSupersedeGate("")
		mutator := holdingMutator{started: &atomic.Int32{}, release: make(chan struct{})}
		engine := mutationEngine(t, gated, mutator)

		job := queueMutationJob(t, engine, "2", "box")
		eventually(t, func() bool { return mutator.started.Load() == 1 })

		gate := queueHostGate(engine, "project-one", "1", "A", "box")
		eventually(t, startedCount(gated, 1))
		assert.EqualValues(t, 1, mutator.started.Load(), "the mutation job still holds its slot while the gate runs")

		close(gated.release)
		close(mutator.release)

		require.NoError(t, (<-gate).err)

		_, err := job.GetResult()
		require.NoError(t, err)
	})
}

func TestHostQueue_AMutationJobWaitsBehindTheJobOnItsOwnSlot(t *testing.T) {
	t.Parallel()

	gated := newSupersedeGate("")
	mutator := holdingMutator{started: &atomic.Int32{}, release: make(chan struct{})}
	engine := mutationEngine(t, gated, mutator)

	first := queueMutationJob(t, engine, "1", "box")
	eventually(t, func() bool { return mutator.started.Load() == 1 })

	// The default is one slot for each host, so the second job of the host waits however long the first runs.
	second := queueMutationJob(t, engine, "2", "box")

	require.Never(t, func() bool { return mutator.started.Load() > 1 }, 3*storePoll, 100*time.Millisecond, "the second job waits for the slot")

	close(mutator.release)

	_, err := first.GetResult()
	require.NoError(t, err)
	_, err = second.GetResult()
	require.NoError(t, err)
	assert.EqualValues(t, 2, mutator.started.Load())
}

func TestHostQueue_AMutationJobTakesAnIdleSlot(t *testing.T) {
	t.Parallel()

	gated := newSupersedeGate("")
	mutator := holdingMutator{started: &atomic.Int32{}, release: make(chan struct{})}
	engine := mutationEngineWithSlots(t, gated, mutator, 2)

	// Both jobs name the same item, so a slot that follows the item gives both the same slot.
	first := queueMutationJob(t, engine, "1", "box")
	eventually(t, func() bool { return mutator.started.Load() == 1 })

	second := queueMutationJob(t, engine, "1", "box")
	eventually(t, func() bool { return mutator.started.Load() == 2 })

	close(mutator.release)

	_, err := first.GetResult()
	require.NoError(t, err)
	_, err = second.GetResult()
	require.NoError(t, err)
}

func TestHostQueue_AHostWithTwoSlotsRunsTwoMutationJobsAndHoldsAThird(t *testing.T) {
	t.Parallel()

	gated := newSupersedeGate("")
	mutator := holdingMutator{started: &atomic.Int32{}, release: make(chan struct{})}
	engine := mutationEngineWithSlots(t, gated, mutator, 2)

	jobs := []dbos.WorkflowHandle[mutationOutput]{queueMutationJob(t, engine, "1", "box"), queueMutationJob(t, engine, "2", "box"), queueMutationJob(t, engine, "3", "box")}
	eventually(t, func() bool { return mutator.started.Load() == 2 })

	require.Never(t, func() bool { return mutator.started.Load() > 2 }, 3*storePoll, 100*time.Millisecond, "the third job waits: the host has two slots")

	close(mutator.release)

	for _, job := range jobs {
		_, err := job.GetResult()
		require.NoError(t, err)
	}

	assert.EqualValues(t, 3, mutator.started.Load())
}

func TestHostQueue_AMutationJobOnAHostThatTheConfigDoesNotDeclareFails(t *testing.T) {
	t.Parallel()

	engine := mutationEngine(t, newSupersedeGate(""), holdingMutator{started: &atomic.Int32{}, release: make(chan struct{})})

	_, err := engine.enqueueMutation(&script{e: engine, in: ItemInput{Project: testProject, Item: "1"}, hostChosen: "stranger", ctx: engine.ctx}, mutationCall{head: "A", command: "make mutation-pr"})

	require.ErrorContains(t, err, `build host "stranger" has no mutation queue`)
}
