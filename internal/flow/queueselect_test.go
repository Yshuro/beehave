package flow

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/tobocop2/beehave/internal/model"
)

func selectorItems() []model.Item {
	tagged := queueItem("21", model.SeverityP1)
	tagged.Labels = []string{"beehave-round", "bug"}

	other := queueItem("22", model.SeverityP1)
	other.Labels = []string{"bug"}

	return []model.Item{tagged, other, queueItem("23", model.SeverityP1)}
}

func TestQueue_ALabelSelectorTakesOnlyTheReadyRowsWithTheLabel(t *testing.T) {
	t.Parallel()

	h, _ := queueHarness(t, selectorItems())
	h.facts = func(string) model.Project {
		return model.Project{Rounds: model.Rounds{Base: "integration/s1", Label: "beehave-round"}}
	}

	_, err := h.engine(t).RunFile(testItem, testProject, writeFlow(t, queueFlow(`name = "q"`)))

	require.NoError(t, err)
	assert.Equal(t, []string{"21:ready:P1:0", "free=2"}, seenBy(t, h), "a ready row without the label stays out of the queue")
}

func TestQueue_AnItemListSelectorTakesOnlyTheListedRowsAndLeavesAReadyRowOutsideTheListUntaken(t *testing.T) {
	t.Parallel()

	h, _ := queueHarness(t, selectorItems())
	h.facts = func(string) model.Project {
		return model.Project{Rounds: model.Rounds{Base: "integration/s1", Items: []string{"23"}}}
	}

	_, err := h.engine(t).RunFile(testItem, testProject, writeFlow(t, queueFlow(`name = "q"`)))

	require.NoError(t, err)
	assert.Equal(t, []string{"23:ready:P1:0", "free=2"}, seenBy(t, h))
}

func TestQueue_AProjectWithNoRoundsBlockQueuesEveryRow(t *testing.T) {
	t.Parallel()

	h, _ := queueHarness(t, selectorItems())
	h.facts = func(string) model.Project { return model.Project{} }

	_, err := h.engine(t).RunFile(testItem, testProject, writeFlow(t, queueFlow(`name = "q"`)))

	require.NoError(t, err)
	assert.Equal(t, []string{"21:ready:P1:0", "22:ready:P1:0", "23:ready:P1:0", "free=2"}, seenBy(t, h))
}

func TestFactsOutputs_TheRoundFieldsAreAlwaysPresentAndEmptyWhenTheProjectHasNoRounds(t *testing.T) {
	t.Parallel()

	empty := factsOutputs(model.Project{})

	assert.Empty(t, empty["round_base"])
	assert.Equal(t, 0, empty["round_lane_cap"])
	assert.Equal(t, false, empty["round_merge"])
	assert.Equal(t, []string{}, empty["round_base_reds"])
	assert.Equal(t, []string{}, empty["round_items"])

	set := factsOutputs(model.Project{Rounds: model.Rounds{Base: "integration/s1", Merge: true, LaneCap: 3, Label: "x", Items: []string{"1"}}})

	assert.Equal(t, "integration/s1", set["round_base"])
	assert.Equal(t, 3, set["round_lane_cap"])
	assert.Equal(t, true, set["round_merge"])
	assert.Equal(t, "x", set["round_label"])
	assert.Equal(t, []string{"1"}, set["round_items"])
}

func TestFacts_TheRoundSettingsReachTheWorkflowAsFlatFieldsAndAreEmptyWhenTheProjectHasNoRounds(t *testing.T) {
	t.Parallel()

	const body = "def main(item):\n    f = facts()\n    fail(\"R=%s/%d/%r/%s/%d/%d\" % (f.round_base, f.round_lane_cap, f.round_merge, f.round_label, len(f.round_items), len(f.round_base_reds)))\n"

	h := newHarness(t)
	h.facts = func(string) model.Project {
		return model.Project{Rounds: model.Rounds{Base: "integration/s1", LaneCap: 4, Merge: true, Label: "x", BaseReds: []string{"a", "b"}}}
	}
	h.flowIs(t, body)

	outcome := <-runInBackground(h.engine(t), testItem)

	require.ErrorContains(t, outcome.err, "R=integration/s1/4/True/x/0/2")

	none := newHarness(t)
	none.flowIs(t, body)

	outcome = <-runInBackground(none.engine(t), testItem)

	require.ErrorContains(t, outcome.err, "R=/0/False//0/0")
}
