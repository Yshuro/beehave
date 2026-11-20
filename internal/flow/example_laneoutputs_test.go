package flow

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// These tests run the review loop of the example and read what it does with the outputs that a lane writes.

// laneReportCheck is the step that checks the report of a lane.
const laneReportCheck = "command:candidate-lane-report#3"

// reportRuns returns the requests of the example rig that ran `remedy report`.
func reportRuns(rig *agentRig) []CommandRequest {
	rig.h.commands.mu.Lock()
	defer rig.h.commands.mu.Unlock()

	var runs []CommandRequest

	for _, request := range rig.h.commands.requests {
		if len(request.Run) > 1 && request.Run[0] == "remedy" && request.Run[1] == "report" {
			runs = append(runs, request)
		}
	}

	return runs
}

// replyToReports answers the calls of `remedy report` in order, and the last answer repeats. Every other command gets an empty success.
func replyToReports(rig *agentRig, outputs ...CommandOutput) {
	next := 0

	rig.h.commands.reply = func(request CommandRequest) (CommandOutput, error) {
		if len(request.Run) < 2 || request.Run[0] != "remedy" || request.Run[1] != "report" {
			return CommandOutput{}, nil
		}

		out := outputs[min(next, len(outputs)-1)]
		next++

		return out, nil
	}
}

// reportCall is the run line that the check of a lane report must have for the head.
func reportCall(head string) []string {
	return []string{"remedy", "report", "/dev/stdin", "--head", head}
}

// agentIndex returns the place, among the agent requests of the rig, of the first request whose prompt holds the phrase. It is -1 for none.
func agentIndex(rig *agentRig, phrase string) int {
	for i, request := range rig.h.agents.requests {
		if strings.Contains(request.Prompt, phrase) {
			return i
		}
	}

	return -1
}

func TestExample_TheReportOfTheImplementLaneIsCheckedAtTheHeadOfTheLane(t *testing.T) {
	t.Parallel()

	base, file := exampleRig(t, func(int) string { return "approve" })
	rig := newCommittingRig(t, base, implementMarker, fixMarker)

	_, err := runPipeline(rig.h.engine(t), file)
	require.NoError(t, err)
	require.Len(t, rig.heads, 1, "only the implement step committed")

	runs := reportRuns(rig.agentRig)
	require.Len(t, runs, 1, "the clean implementer makes one candidate, and its report is checked once")
	assert.Equal(t, reportCall(rig.heads[0]), runs[0].Run, "the check judges the report against the head of the implementer")
	assert.Equal(t, cleanLaneReport("implement"), runs[0].Stdin, "the report that the implement step wrote is the stdin of the check")
}

func TestExample_TheReportOfTheLastFixIsCheckedAtTheHeadOfThatFix(t *testing.T) {
	t.Parallel()

	base, file := exampleRig(t, approveAfter(1))
	rig := newCommittingRig(t, base, implementMarker, fixMarker)

	_, err := runPipeline(rig.h.engine(t), file)
	require.NoError(t, err)
	require.Len(t, rig.heads, 2, "the implement step and one fix committed")

	runs := reportRuns(rig.agentRig)
	require.Len(t, runs, 1, "the candidate comes from the fix, so the report of the fix is the one that is checked")
	assert.Equal(t, reportCall(rig.heads[1]), runs[0].Run, "the check names the head of the fix, not the head of the implementer")
	assert.Equal(t, cleanLaneReport("fix"), runs[0].Stdin, "the report that the fix wrote is the stdin of the check")
}

func TestExample_ALaneReportWithAMissingTableIsSentBackBeforeTheFullReview(t *testing.T) {
	t.Parallel()

	base, file := exampleRig(t, func(int) string { return "approve" })
	rig := newCommittingRig(t, base, implementMarker, fixMarker)
	replyToReports(rig.agentRig, CommandOutput{RC: 1, Stdout: "no ## Hunks section\n"}, CommandOutput{RC: 0})

	result, err := runPipeline(rig.h.engine(t), file)
	require.NoError(t, err)
	require.Len(t, rig.heads, 2, "the implement step and the fix that the defect sent back")

	fixes := fixPrompts(rig.agentRig)
	require.Len(t, fixes, 1, "the defect sends the lane back once")
	assert.Contains(t, fixes[0], "The report of the lane has a defect. Each line is one finding:\nno ## Hunks section", "the lines of the tool are the findings of the fix")
	assert.Less(t, agentIndex(rig.agentRig, fixMarker), agentIndex(rig.agentRig, fullReview), "no full review ran before the fix: the defective head is never judged")
	assert.Equal(t, []string{"opus", "sonnet", "opus", "sonnet", "opus", "opus"}, rig.models(), "the plan, the implement step, its scoped review, the fix, its scoped review, then the one full review of the fixed head")

	runs := reportRuns(rig.agentRig)
	require.Len(t, runs, 2)
	assert.Equal(t, reportCall(rig.heads[0]), runs[0].Run)
	assert.Equal(t, reportCall(rig.heads[1]), runs[1].Run, "the second candidate is checked at the head of the fix")
	assert.True(t, childOf(t, result).Approved)
}

func TestExample_ACleanLaneReportAddsNoFixRoundAndNoReview(t *testing.T) {
	t.Parallel()

	base, file := exampleRig(t, func(int) string { return "approve" })
	rig := newCommittingRig(t, base, implementMarker, fixMarker)
	replyToReports(rig.agentRig, CommandOutput{RC: 0, Stdout: "OK: the report is whole\n"})

	_, err := runPipeline(rig.h.engine(t), file)
	require.NoError(t, err)

	assert.Equal(t, []string{"opus", "sonnet", "opus", "opus"}, rig.models(), "the base list of agent calls: the plan, the implement step, its scoped review and the full review")
	assert.Empty(t, fixPrompts(rig.agentRig), "a clean report sends nothing back")
	assert.Len(t, reportRuns(rig.agentRig), 1)
}

func TestExample_AReportCheckThatCannotRunStopsTheItem(t *testing.T) {
	t.Parallel()

	base, file := exampleRig(t, func(int) string { return "approve" })
	replyToReports(base, CommandOutput{RC: 2, Stdout: "remedy report: cannot read the report\n"})

	result, err := runPipeline(base.h.engine(t), file)
	require.NoError(t, err)

	require.NotNil(t, result.Stopped, "the check that cannot run ends the item")
	assert.Equal(t, laneReportCheck, result.Stopped.Step)
	assert.Equal(t, 2, result.Stopped.RC)
	assert.Equal(t, []string{"opus", "sonnet", "opus"}, base.models(), "the plan, the implement step and its scoped review ran, and no full review started")
	assert.Equal(t, -1, agentIndex(base, fullReview))
}
