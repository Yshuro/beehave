package flow

import (
	"context"
	"strings"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/tobocop2/beehave/internal/model"
)

// These tests run the host loss rules of the round of the example: the count of the engine reaches the round through the child values,
// the landing and the sync gate, and the round holds for the owner at the limit.

// provingHosts is the host keeper of the round rig that proves every lost job stopped.
type provingHosts struct{ *fakeHosts }

func (provingHosts) LostProof(context.Context, string, string, string) (model.LostProof, error) {
	return model.LostProof{Kind: model.LostStopped}, nil
}

// lossGate loses the first runs of the gate of each item in lose, and sends every other run to the gate behind it.
type lossGate struct {
	gate

	mu   sync.Mutex
	lose map[string]int
}

func (g *lossGate) Run(ctx context.Context, project, item, host string, job GateJob) (GateResult, error) {
	g.mu.Lock()

	lost := g.lose[item] > 0
	if lost {
		g.lose[item]--
	}

	g.mu.Unlock()

	if lost {
		return GateResult{}, lostError()
	}

	return g.gate.Run(ctx, project, item, host, job)
}

// runLosing runs the round of the rig to its last wait. The gate of each child in lose loses its first runs, and the host proves each loss.
// The gate of the round itself (the landing and the sync) loses the waits that rig.gate.loseAt lists.
func runLosing(t *testing.T, rig *roundRig, lose map[string]int) {
	t.Helper()

	rig.h.hosts = provingHosts{rig.hosts}

	engine := rig.engineWith(t, func(deps *Deps) { deps.Gate = &lossGate{gate: deps.Gate, lose: lose} })
	rig.last = engine

	_, err := rig.runOn(t, engine)
	require.NoError(t, err)
}

// holdQuestion is the start of the question of round-wait for the reason host-loss.
const holdQuestion = "The engine counted "

func TestRound_FiveHostLossesOfOneRoundOneOnALandingGateHoldTheRoundForTheOwner(t *testing.T) {
	t.Parallel()

	rig := newRoundRig(t, "1")
	rig.gate.loseAt = []int{1}

	runLosing(t, rig, map[string]int{"1": 4})

	require.Len(t, rig.questions, 1, "the round asks once")
	assert.Contains(t, rig.questions[0], holdQuestion+"5 host losses", "four losses of the child gate and one of the landing gate")
	assert.NotContains(t, rig.questions[0], "did not merge the trunk")
	assert.Equal(t, []string{"1"}, rig.merger.landed(), "the batch gate ran again after its loss, and the batch landed")

	joined := strings.Join(stepNames(t, rig.last), " ")
	assert.Contains(t, joined, "wait:round-wait#")
	assert.NotContains(t, joined, "queue:round-queue-2#", "the held round starts no item")
	assert.NotContains(t, joined, "agent:queue-audit-2#")
	assert.NotContains(t, joined, "command:sync-2#", "the held round starts no sync")
}

func TestRound_AHostLossOnTheSyncGateRerunsAndIsNeverSyncRed(t *testing.T) {
	t.Parallel()

	rig := newRoundRig(t, "")
	script := &syncScript{lines: []string{"synced", "state=current"}}
	withSync(t, rig, script)
	rig.gate.loseAt = []int{1}

	runLosing(t, rig, nil)

	names := stepNames(t, rig.last)
	require.GreaterOrEqual(t, at(names, "gate:sync-gate-1#"), 0, names)
	require.GreaterOrEqual(t, at(names, "gate:sync-gate-1-host-1#"), 0, "the lost gate runs again under its own name")
	assert.Less(t, at(names, "gate:sync-gate-1#"), at(names, "gate:sync-gate-1-host-1#"))
	assert.GreaterOrEqual(t, at(names, "agent:sync-review-1#"), 0, "the rerun passed, so the sync head gets its review")
	assert.Len(t, syncLands(rig), 1, "and lands")

	require.Len(t, rig.questions, 1)
	assert.NotContains(t, rig.questions[0], "sync-red")
	assert.NotContains(t, rig.questions[0], holdQuestion, "one loss is below the limit")
}

func TestRound_AnAnswerToTheHostLossHoldWaitsAgainOnlyAfterANewLoss(t *testing.T) {
	t.Parallel()

	rig := newRoundRig(t, "1")
	rig.answers = []string{"CONTINUE", "CONTINUE", "STOP"}
	withSync(t, rig, &syncScript{lines: []string{"state=current", "synced", "state=current"}})
	rig.setRound("base", "main") // the pull request of the child has the base main

	// The gate of the round loses its first wait (the landing in pass 1) and its third wait (the sync gate of pass 2).
	rig.gate.loseAt = []int{1, 3}

	runLosing(t, rig, map[string]int{"1": 4})

	require.Len(t, rig.questions, 3)
	assert.Contains(t, rig.questions[0], holdQuestion+"5 host losses", "the fifth loss holds the round")
	assert.Contains(t, rig.questions[1], holdQuestion+"6 host losses", "the sixth loss, on the sync gate, is newer than the answer")
	assert.NotContains(t, rig.questions[2], holdQuestion, "no loss came after the second answer")
	assert.NotContains(t, rig.questions[2], "sync-")

	names := stepNames(t, rig.last)
	assert.Equal(t, -1, at(names, "gate:sync-gate-2-host-1#"), "a sync gate at the limit is not run again")
	assert.GreaterOrEqual(t, at(names, "command:sync-3#"), 0, "after the answer the round goes on")
}

func TestRound_AChildThatStoppedOnTheHostLossLimitHoldsTheRoundAndLandsNothing(t *testing.T) {
	t.Parallel()

	rig := newRoundRig(t, "1")

	runLosing(t, rig, map[string]int{"1": 100})

	require.Len(t, rig.questions, 1)
	assert.Contains(t, rig.questions[0], holdQuestion+"5 host losses", "the count of the child value reaches the round")
	assert.Empty(t, rig.merger.landed(), "the item is not approved")

	joined := strings.Join(stepNames(t, rig.last), " ")
	assert.NotContains(t, joined, "hold-", "the item is not at fault: no hold status")
	assert.NotContains(t, joined, "queue:round-queue-2#")
}
