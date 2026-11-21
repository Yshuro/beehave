package flow

import (
	"errors"
	"testing"

	"github.com/dbos-inc/dbos-transact-golang/dbos"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const strangerFlow = `def main(item):
    done = gather([struct(item = "21", run = "item-21-r1")], name = "other")
    command(name = "after", run = ["after", done.results[0].error])
`

func TestGather_RefusesARunThatAUserStarted(t *testing.T) {
	t.Parallel()

	h := newHarness(t)
	engine := h.engine(t)

	_, err := engine.RunFile("21", testProject, writeFlow(t, bodyFlow(`command(name = "w", run = ["w"])`)))
	require.NoError(t, err)

	_, err = engine.RunFile(testItem, testProject, writeFlow(t, strangerFlow))

	require.ErrorContains(t, err, "run item-21-r1 is not a child of this run")
	assert.Empty(t, h.commands.ranBy(testItem), "the gather did not return")
}

func TestGather_RefusesARunThatAnotherRunSpawned(t *testing.T) {
	t.Parallel()

	h := newHarness(t)
	engine := h.engine(t)

	spawner := "def worker(item):\n    command(name = \"w\", run = [\"w\"])\n\ndef main(item):\n    gather(spawn(items = [\"21\"], entry = \"worker\"))\n"
	_, err := engine.RunFile("13", testProject, writeFlow(t, spawner))
	require.NoError(t, err)

	_, err = engine.RunFile(testItem, testProject, writeFlow(t, strangerFlow))

	require.ErrorContains(t, err, "run item-21-r1 is not a child of this run")
}

func TestGather_AcceptsARunThatIsNotInTheStoreAndReportsIt(t *testing.T) {
	t.Parallel()

	h := newHarness(t)

	flow := "def main(item):\n    done = gather([struct(item = \"99\", run = \"item-99-r1\")], name = \"none\")\n    command(name = \"after\", run = [\"after\", done.results[0].error])\n"
	_, err := h.engine(t).RunFile(testItem, testProject, writeFlow(t, flow))

	require.NoError(t, err)
	require.Len(t, h.commands.ranBy(testItem), 1)
	assert.Contains(t, h.commands.ranBy(testItem)[0], "is not in the store")
}

func TestGather_TheCheckOfChildrenReportsARunIdItCannotRead(t *testing.T) {
	t.Parallel()

	engine := newHarness(t).engine(t)

	err := (&script{e: engine, ctx: engine.ctx, in: ItemInput{Item: testItem}}).checkChildren([]spawnedRun{{Item: "21", Run: "item-21-r1"}})

	require.ErrorContains(t, err, "read the id of the run of item 12")
}

func TestGather_TheCheckOfChildrenReportsAReadOfTheRunsThatFailed(t *testing.T) {
	t.Parallel()

	engine := newHarness(t).opened(t)
	stopped := newHarness(t).engine(t)

	require.NoError(t, stopped.Shutdown())

	check := func(ctx dbos.Context, in ItemInput) (string, error) {
		s := &script{e: &Engine{ctx: stopped.ctx}, ctx: ctx, in: in}

		err := s.checkChildren([]spawnedRun{{Item: "21", Run: "item-21-r1"}})
		if err == nil {
			return "", errors.New("the check did not fail")
		}

		return err.Error(), nil
	}

	dbos.RegisterWorkflow(engine.ctx, check, dbos.WithWorkflowName(itemName))
	require.NoError(t, dbos.Launch(engine.ctx))

	handle, err := dbos.RunWorkflow(engine.ctx, check, ItemInput{Item: testItem, Project: testProject}, dbos.WithWorkflowID(testRun))
	require.NoError(t, err)

	words, err := handle.GetResult()
	require.NoError(t, err)
	assert.Contains(t, words, "read the runs of the spawned items")
}
