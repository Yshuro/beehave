package flow

import (
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// scopeBody gates the item once, which fixes its scope, then merges the pull request of the item.
func (r *batchRig) scopeBody() string {
	return "gate(name = \"g\")\n" + r.call("1", "")
}

// runItemOne runs the file as item 1, the item of the pull request that the call merges, so the gate of the run fixes the scope of that member.
func (r *batchRig) runItemOne(t *testing.T, engine *Engine) <-chan runOutcome {
	t.Helper()

	done := make(chan runOutcome, 1)
	file := writeFlow(t, bodyFlow(r.scopeBody()))

	go func() {
		result, err := engine.RunFile("1", testProject, file)
		done <- runOutcome{result: result, err: err}
	}()

	return done
}

func TestMerge_AMemberThatClosesANewIssueWithoutAnOwnerLineIsRefusedAndLandsNothing(t *testing.T) {
	t.Parallel()

	rig := newBatchRig(t)
	rig.h.forge.closing = [][]int{{12}, {12, 13}}
	engine := rig.engine(t)
	done := rig.runItemOne(t, engine)

	waitOn(t, ownerAsks(t, rig.h))
	require.NoError(t, rig.h.client(t, testVersion).Answer("1", "no"))

	got := receive(t, done)

	require.NoError(t, got.err)
	assert.Empty(t, rig.merger.landed(), "the refused member is not merged")
	require.Len(t, rig.h.commands.ran(), 1)
	assert.Contains(t, rig.h.commands.ran()[0], "the pull request closes an issue that is new since its first review")
}

func TestMerge_AnOwnerLineThatNamesTheNewIssueLetsTheMemberLand(t *testing.T) {
	t.Parallel()

	rig := newBatchRig(t)
	rig.h.forge.closing = [][]int{{12}, {13, 12, 13}}
	engine := rig.engine(t)
	done := rig.runItemOne(t, engine)

	waitOn(t, ownerAsks(t, rig.h))
	require.NoError(t, rig.h.client(t, testVersion).Answer("1", "keep #13"))

	require.NoError(t, receive(t, done).err)
	assert.Equal(t, []string{"1"}, rig.merger.landed())
}

func TestMerge_AMemberWhoseIssuesDidNotGrowAsksNoQuestion(t *testing.T) {
	t.Parallel()

	rig := newBatchRig(t)
	rig.h.forge.closing = [][]int{{12, 13}, {12}}

	_, err := rig.engine(t).RunFile("1", testProject, writeFlow(t, bodyFlow(rig.scopeBody())))

	require.NoError(t, err)
	assert.Equal(t, []string{"1"}, rig.merger.landed())
}

func TestMerge_AMemberWithNoScopeOfItsRunHasNothingToCompare(t *testing.T) {
	t.Parallel()

	rig := newBatchRig(t)
	rig.h.forge.closing = [][]int{{12}, {12, 13}}

	_, err := rig.walk(t, rig.call("1", ""))

	require.NoError(t, err)
	assert.Equal(t, []string{"1"}, rig.merger.landed(), "a run with no first review reads no issue and asks nothing")
	assert.Zero(t, rig.h.forge.closingReads, "nothing is read when there is no scope to compare")
}

func TestMerge_AMemberWhoseIssueReadFailsEndsTheCallAndLandsNothing(t *testing.T) {
	t.Parallel()

	rig := newBatchRig(t)
	rig.h.forge.closing = [][]int{{12}}
	rig.h.forge.closingErr, rig.h.forge.closingOK = errors.New("the forge is down"), 1

	_, err := rig.engine(t).RunFile("1", testProject, writeFlow(t, bodyFlow(rig.scopeBody())))

	require.ErrorContains(t, err, "scope-check of item 1")
	assert.Empty(t, rig.merger.landed())
}
