package flow

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/dbos-inc/dbos-transact-golang/dbos"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/tobocop2/beehave/internal/model"
)

// lossCount is the field of a gate result and of the status of a run that counts the host losses of the tree.
const lossCount = "host_losses"

// lostTwiceFlow is a parent that spawns item 21 and then item 22, one after the other. The gate of each child runs on the host gpu-b.
const lostTwiceFlow = `def worker(item):
    host_up(name = "start", host = "gpu-b")
    gate(name = "check")

def main(item):
    gather(spawn(items = ["21"], entry = "worker"), fail = "raise")
    gather(spawn(items = ["22"], entry = "worker"), fail = "raise")
`

// lastRunOf is the id of the last run of item.
func lastRunOf(t *testing.T, engine *Engine, item string) string {
	t.Helper()

	last, found, err := latestRun(engine.ctx, item)
	require.NoError(t, err)
	require.True(t, found, "item %s has no run", item)

	return last.Status.ID
}

// gateRecordOf is the recorded outputs of the gate call gate:check#1 of a run, as the record holds them.
func gateRecordOf(t *testing.T, engine *Engine, id string) map[string]any {
	t.Helper()

	steps, err := dbos.GetWorkflowSteps(engine.ctx, id, dbos.WithStepsLoadOutput(true))
	require.NoError(t, err)

	for _, step := range steps {
		if step.StepName != "gate:check#1" {
			continue
		}

		text, isText := step.Output.(string)
		require.True(t, isText)

		var gate recorded[map[string]any]

		require.NoError(t, json.Unmarshal([]byte(text), &gate))

		return gate.Outputs
	}

	t.Fatalf("the run %s has no gate call", id)

	return nil
}

// statusJSONOf is what beehave status <run id> --json prints for a run, as a JSON object.
func statusJSONOf(t *testing.T, h *harness, id string) map[string]any {
	t.Helper()

	view, err := h.client(t, testVersion).Steps(id)
	require.NoError(t, err)

	text, err := json.Marshal(view)
	require.NoError(t, err)

	var shown map[string]any

	require.NoError(t, json.Unmarshal(text, &shown))

	return shown
}

func TestHostLoss_TheRootRunCountsLossesOfItsChildRuns(t *testing.T) {
	t.Parallel()

	h, hosts, _ := lostHarness(t, time.Hour)
	hosts.proofs = []model.LostProof{{Kind: model.LostStopped}}
	engine := h.engine(t)

	_, err := engine.RunFile(testItem, testProject, writeFlow(t, lostTwiceFlow))
	require.NoError(t, err)

	first, second := lastRunOf(t, engine, "21"), lastRunOf(t, engine, "22")

	assert.Contains(t, stepNamesOf(t, engine, first), "gate:check#1/host-loss", "the item run records the loss as its own step")
	assert.Contains(t, stepNamesOf(t, engine, second), "gate:check#1/host-loss", "the item run records the loss as its own step")
	assert.EqualValues(t, 1, gateRecordOf(t, engine, first)[lossCount], "the first loss of the tree")
	assert.EqualValues(t, 2, gateRecordOf(t, engine, second)[lossCount], "the second result counts the loss of the first child and its own")

	for _, id := range []string{testRun, first, second} {
		assert.EqualValues(t, 2, statusJSONOf(t, h, id)[lossCount], "status of %s shows the losses of the tree of the run", id)
	}
}

func TestHostLoss_TheRecordAndTheCountFailOutsideARun(t *testing.T) {
	t.Parallel()

	engine := idleEngine(t, newHarness(t))

	_, err := engine.recordLoss(engine.ctx, testItem, "gate:check#1", voidOutputs(LostJob{Kind: kindGate, Job: "job-1", Host: "gpu-b"}))

	require.ErrorContains(t, err, "record the lost job of the gate: ")

	_, err = engine.treeHostLosses(engine.ctx, testItem)

	require.ErrorContains(t, err, "read the id of the run of item 12", "the count needs the run to find its tree")
}

func TestHostLoss_TheCountOfTheStatusReportsAReadOfTheTreeThatFailed(t *testing.T) {
	t.Parallel()

	engine := deepTree(t, newAgentRig(t))
	c := &Client{flow: &failingRead{Context: engine.ctx, at: 1}}

	err := c.addHostLosses(runID("3", 1), &RunProgress{})

	require.ErrorContains(t, err, "the store failed", "the climb to the root fails")

	// The climb from the root reads the root once, then the walk reads its steps.
	c.flow = &failingRead{Context: engine.ctx, at: 2}
	err = c.addHostLosses(runID("1", 1), &RunProgress{})

	require.ErrorContains(t, err, "read the steps of "+runID("1", 1), "the walk from the root fails")
}

func TestHostLoss_AGateThatLostNothingCountsNoneAndStatusOmitsTheCount(t *testing.T) {
	t.Parallel()

	h := newHarness(t)
	engine := h.engine(t)

	_, err := engine.RunFile(testItem, testProject, writeFlow(t, bodyFlow("gate(name = \"check\")\n")))
	require.NoError(t, err)

	outputs := gateRecordOf(t, engine, testRun)
	assert.EqualValues(t, 0, outputs[lossCount], "a gate that lost nothing counts no loss")
	assert.Equal(t, map[string]any{}, outputs[hostLossKey])
	assert.NotContains(t, statusJSONOf(t, h, testRun), lossCount, "status omits a count of 0")
}
