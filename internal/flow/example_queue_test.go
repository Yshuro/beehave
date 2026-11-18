package flow

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/tobocop2/beehave/internal/model"
)

// These tests run the round of the example: the order of work of the queue pass and the children that it starts.

// startedChildren returns the items that the round started a child for, in sorted order: the children that reached the gate of the real pipeline.
func startedChildren(rig *roundRig) []string {
	return rig.kids.started()
}

func TestExampleQueue_ThePassStartsWorkInFlightBeforeNewWorkAndThenBySeverity(t *testing.T) {
	t.Parallel()

	rig := newRoundRig(t, "21 22 23 10 11")
	rig.setRound("merge", false)

	held := queueItem("10", model.SeverityP0)
	held.Hold = true
	rig.q.items = []model.Item{
		queueItem("21", model.SeverityP2), queueItem("22", model.SeverityP1), queueItem("23", model.SeverityP2),
		held, queueItem("11", model.SeverityP0),
	}
	rig.q.pulls = []model.PullRequest{queuePull(t, 8, "23"), queuePull(t, 9, "11", "hold")}

	_, err := rig.run(t)
	require.NoError(t, err)

	// The queue has two free slots. 23 has a pull request, so it starts first. 22 is P1 with a higher id than the P2 item 21, so severity alone puts it next.
	// 10 (tracker hold) and 11 (hold label on its pull request) are P0 and would outrank both, so only the ready filter keeps them out.
	assert.Equal(t, []string{"22", "23"}, startedChildren(rig), "the item with an open pull request starts first, then the most severe new item, and a held item never starts")
}

func TestExampleQueue_ARoundWithAnItemListStartsOnlyTheListedItems(t *testing.T) {
	t.Parallel()

	rig := newRoundRig(t, "23")
	rig.setRound("merge", false)
	rig.q.items = []model.Item{queueItem("21", model.SeverityP1), queueItem("22", model.SeverityP1), queueItem("23", model.SeverityP1)}

	_, err := rig.run(t)

	require.NoError(t, err)
	assert.Equal(t, []string{"23"}, startedChildren(rig), "the workflow filters the rows by the list, and the free slots would take two")
}

func TestExampleQueue_ARoundOverAnEmptyQueueStartsNoChild(t *testing.T) {
	t.Parallel()

	rig := newRoundRig(t, "")

	_, err := rig.run(t)

	require.NoError(t, err)
	assert.Empty(t, startedChildren(rig))
}

func TestExampleQueue_TheAuditRunsBeforeTheQueueReadOfEachPass(t *testing.T) {
	t.Parallel()

	rig := newRoundRig(t, "1 3")

	_, err := rig.run(t)

	require.NoError(t, err)

	joined := strings.Join(stepNames(t, rig.last), " ")
	assert.Less(t, strings.Index(joined, "agent:queue-audit-1#"), strings.Index(joined, "queue:round-queue-1#"), "the audit of a pass comes before its queue read")
	assert.Less(t, strings.Index(joined, "queue:round-queue-1#"), strings.Index(joined, "spawn:round-spawn-1#"))
}
