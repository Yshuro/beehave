package flow

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/tobocop2/beehave/internal/merge"
)

// slowGate holds each gate until the test lets it end, and tells the test when it started.
type slowGate struct {
	started chan struct{}
	release chan struct{}
}

func (slowGate) Submit(context.Context, string, string, string, string) (GateJob, error) {
	return GateJob{}, nil
}

func (g slowGate) Run(ctx context.Context, _, _, _ string, _ GateJob) (GateResult, error) {
	g.started <- struct{}{}

	select {
	case <-g.release:
	case <-ctx.Done():
	}

	return GateResult{Passed: true, Head: gatedHead, Steps: commandStep(true)}, nil
}

func TestExclusive_AMergeOfARepositoryStartsOnlyAfterTheGateOfThatRepositoryEnds(t *testing.T) {
	t.Parallel()

	gated := slowGate{started: make(chan struct{}, 1), release: make(chan struct{})}
	h := mergeHarness(t, merge.Result{Stop: merge.Done})
	h.gate.via = gated
	merged := make(chan struct{}, 1)
	h.merge.over = func(context.Context, string, MergeSettings) (merge.Result, error) {
		merged <- struct{}{}

		return merge.Result{Stop: merge.Done}, nil
	}

	engine := h.engine(t)
	gateFile := writeFlow(t, bodyFlow("gate(name = \"check\")\n"))
	mergeFile := writeFlow(t, shipFlow("merge = True, "))

	var group sync.WaitGroup

	ended := make(chan error, 2)

	group.Go(func() {
		_, err := engine.RunFile("gate-item", testProject, gateFile)
		ended <- err
	})

	receive(t, gated.started)

	group.Go(func() {
		_, err := engine.RunFile("merge-item", testProject, mergeFile)
		ended <- err
	})

	waitOn(t, condition(waitsInTheQueue(engine, 1)))

	// The queue has no recorded poll, so the check spans three polls of the queue. It is a listed wall-time site.
	require.Never(t, func() bool { return len(merged) > 0 }, 3*storePoll, 100*time.Millisecond, "the merge started while the gate of the same repository was running")

	close(gated.release)
	receive(t, merged)

	group.Wait()
	require.NoError(t, receive(t, ended))
	require.NoError(t, receive(t, ended))
	require.Len(t, h.merge.asked, 1)
	assert.Contains(t, h.merge.asked[0], testProject)
}

func TestExclusive_AGateOfOneProjectDoesNotHoldTheMergeOfAnotherProject(t *testing.T) {
	t.Parallel()

	gated := slowGate{started: make(chan struct{}, 1), release: make(chan struct{})}
	h := mergeHarness(t, merge.Result{Stop: merge.Done})
	h.gate.via = gated
	merged := make(chan struct{}, 1)
	h.merge.over = func(context.Context, string, MergeSettings) (merge.Result, error) {
		merged <- struct{}{}

		return merge.Result{Stop: merge.Done}, nil
	}

	engine := h.engine(t)
	gateFile := writeFlow(t, bodyFlow("gate(name = \"check\")\n"))
	mergeFile := writeFlow(t, shipFlow("merge = True, "))
	ended := make(chan error, 2)

	go func() {
		_, err := engine.RunFile("gate-item", "project-a", gateFile)
		ended <- err
	}()

	receive(t, gated.started)

	go func() {
		_, err := engine.RunFile("merge-item", "project-b", mergeFile)
		ended <- err
	}()

	receive(t, merged)

	close(gated.release)
	require.NoError(t, receive(t, ended))
	require.NoError(t, receive(t, ended))
}

type cancelGate struct {
	started chan struct{}
	runs    *atomic.Int32
}

func (cancelGate) Submit(context.Context, string, string, string, string) (GateJob, error) {
	return GateJob{}, nil
}

func (g cancelGate) Run(ctx context.Context, _, _, _ string, _ GateJob) (GateResult, error) {
	if g.runs.Add(1) == 1 {
		g.started <- struct{}{}
		hang(ctx.Done())

		return GateResult{}, ctx.Err()
	}

	return GateResult{Passed: true, Head: gatedHead, Steps: commandStep(true)}, nil
}

func TestExclusive_ACancelWhileTheGateRunsThenAResumeEndsTheItemWithOneMerge(t *testing.T) {
	t.Parallel()

	h := mergeHarness(t, merge.Result{Stop: merge.Done})
	gated := cancelGate{started: make(chan struct{}, 1), runs: &atomic.Int32{}}
	deps := h.deps()
	deps.Gate = gated

	engine, err := NewEngine(t.Context(), h.opts, deps)
	require.NoError(t, err)
	require.NoError(t, engine.Launch())
	t.Cleanup(func() { require.NoError(t, engine.Shutdown()) })

	file := writeFlow(t, bodyFlow("gate(name = \"check\")\n"+shipCall("merge = True, ")))
	ended := make(chan error, 1)

	go func() {
		_, err := engine.RunFile(testItem, testProject, file)
		ended <- err
	}()

	receive(t, gated.started)

	client := h.client(t, testVersion)
	require.NoError(t, client.Cancel(testItem))
	h.tick(t)

	require.Error(t, receive(t, ended))

	assert.Empty(t, h.merge.asked, "no merge ran before the gate ended")
	result, err := resumeAndJoin(t, engine, client)

	require.NoError(t, err)
	assert.Equal(t, "merged", result.Merge)
	assert.EqualValues(t, 2, gated.runs.Load(), "the canceled gate ran again once")
	assert.Len(t, h.merge.asked, 1, "the merge ran once")
}

func TestExclusive_ABatchMergeOfARepositoryStartsOnlyAfterTheGateOfThatRepositoryEnds(t *testing.T) {
	t.Parallel()

	rig := newBatchRig(t)
	gated := slowGate{started: make(chan struct{}, 1), release: make(chan struct{})}
	deps := rig.deps()
	deps.Gate = gated

	engine, err := NewEngine(t.Context(), rig.h.opts, deps)
	require.NoError(t, err)
	require.NoError(t, engine.Launch())
	t.Cleanup(func() { require.NoError(t, engine.Shutdown()) })

	var group sync.WaitGroup

	ended := make(chan error, 2)

	group.Go(func() {
		_, err := engine.RunFile("gate-item", testProject, writeFlow(t, bodyFlow("gate(name = \"check\")\n")))
		ended <- err
	})

	receive(t, gated.started)

	group.Go(func() {
		_, err := engine.RunFile("batch-item", testProject, writeFlow(t, bodyFlow(rig.call("1 3", `gate_run = ["check-all"], `))))
		ended <- err
	})

	waitOn(t, condition(waitsInTheQueue(engine, 1)))

	// The queue has no recorded poll, so the check spans three polls of the queue. It is a listed wall-time site.
	require.Never(t, func() bool { return len(rig.h.commands.ran()) > 0 || len(rig.merger.landed()) > 0 }, 3*storePoll, 100*time.Millisecond, "the batch started or merged while the gate of the repository ran")

	close(gated.release)
	group.Wait()
	require.NoError(t, receive(t, ended))
	require.NoError(t, receive(t, ended))
	assert.Equal(t, []string{"1", "3"}, rig.merger.landed(), "the batch merged after the gate ended")
}
