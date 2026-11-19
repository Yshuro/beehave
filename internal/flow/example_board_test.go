package flow

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/tobocop2/beehave/internal/merge"
)

// boardVars is the key `board` of the vars of a project. An empty owner leaves the key out, as a project file does.
func boardVars(owner string) map[string]any {
	board := map[string]any{"number": json.Number("3"), "started": "In Progress", "merged": "Done"}
	if owner != "" {
		board["owner"] = owner
	}

	return board
}

// setBoard makes the vars of the project of the rig hold a board.
func setBoard(rig *agentRig, owner string) {
	rig.project.Vars["board"] = boardVars(owner)
}

// roundBoard gives the project of a round rig the board of the tests.
func roundBoard(rig *roundRig, owner string) {
	rig.project.Vars["board"] = boardVars(owner)
}

func TestExampleBoard_AProjectWithABoardMovesTheCardAtTheLaneStartAndReadsItBackAndTheChildMovesNoMergedCard(t *testing.T) {
	t.Parallel()

	rig, file := exampleRig(t, func(int) string { return "approve" })
	setBoard(rig, "acme")

	_, err := runPipeline(rig.h.engine(t), file)

	require.NoError(t, err)
	assert.Equal(t, []string{testProject + "|" + testItem + "|3|acme|0|In Progress"}, rig.h.forge.placed, "the merged column is the round's: the child never merges")
	assert.Equal(t, 1, rig.h.forge.cardReads, "the move reads the card back")
}

func TestExampleBoard_AProjectWithABoardThatLosesTheWriteStopsTheRunAtTheLaneStart(t *testing.T) {
	t.Parallel()

	rig, file := exampleRig(t, func(int) string { return "approve" })
	setBoard(rig, "")
	rig.h.forge.dropCards = true
	rig.h.forge.cardStatus = "Todo"

	_, err := runPipeline(rig.h.engine(t), file)

	require.ErrorContains(t, err, `shows "Todo"`)
	assert.Len(t, rig.h.agents.requests, 2, "only the plan and the implement agents ran: the run stops before the review loop")
}

func TestExampleBoard_ABoardInTheVarsThatLacksAColumnStopsTheRunAtTheLaneStartAndNamesTheKey(t *testing.T) {
	t.Parallel()

	rig, file := exampleRig(t, func(int) string { return "approve" })
	board := boardVars("")
	delete(board, "merged")
	rig.project.Vars["board"] = board

	_, err := runPipeline(rig.h.engine(t), file)

	require.ErrorContains(t, err, "vars.board has no merged")
	assert.Empty(t, rig.h.forge.placed)
}

func TestExampleBoard_ALandRunWithABoardMovesOneCardForEachMergedItem(t *testing.T) {
	t.Parallel()

	rig := newRoundRig(t, "1 3")
	roundBoard(rig, "acme")

	_, err := rig.run(t)

	require.NoError(t, err)
	assert.Equal(t, []string{
		testProject + "|" + testItem + "|3|acme|1|Done",
		testProject + "|" + testItem + "|3|acme|3|Done",
	}, doneMoves(rig))
	assert.Equal(t, len(rig.h.forge.placed), rig.h.forge.cardReads, "each move reads the card back")
}

// doneMoves returns the moves of a card to the merged column. Each child also moves a card to the started column; the land run makes the others.
func doneMoves(rig *roundRig) []string {
	var moves []string

	for _, move := range rig.h.forge.placed {
		if strings.HasSuffix(move, "|Done") {
			moves = append(moves, move)
		}
	}

	return moves
}

func TestExampleBoard_ALandRunMovesTheCardOfTheMergedItemAndNotTheCardOfARejectedItem(t *testing.T) {
	t.Parallel()

	rig := newRoundRig(t, "1 3")
	roundBoard(rig, "acme")
	rig.merger.script["1"] = []merge.Result{{Stop: merge.Refused, Lines: []string{"REFUSE: has the label hold"}, Refusal: []string{"REFUSE: has the label hold"}}}

	_, err := rig.run(t)

	require.NoError(t, err)
	assert.Equal(t, []string{testProject + "|1|hold"}, heldOf(rig.writer), "item 1 is rejected and held")
	assert.Equal(t, []string{testProject + "|" + testItem + "|3|acme|3|Done"}, doneMoves(rig), "only the merged item 3 moves")
}

func TestExampleBoard_ALandRunWithNoBoardMovesNoCard(t *testing.T) {
	t.Parallel()

	rig := newRoundRig(t, "1 3")

	_, err := rig.run(t)

	require.NoError(t, err)
	assert.Equal(t, []string{"1", "3"}, rig.merger.landed(), "the members end in turn, so they land in list order")
	assert.Empty(t, rig.h.forge.placed)
}

func TestExampleBoard_AProjectWithNoBoardMakesNoBoardCall(t *testing.T) {
	t.Parallel()

	rig, file := exampleRig(t, func(int) string { return "approve" })

	result, err := runPipeline(rig.h.engine(t), file)

	require.NoError(t, err)
	assert.True(t, childOf(t, result).Approved)
	assert.Empty(t, rig.h.forge.placed)
	assert.Zero(t, rig.h.forge.cardReads)
}
