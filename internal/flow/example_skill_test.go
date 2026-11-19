package flow

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/tobocop2/beehave/internal/agent"
)

func TestExample_AnImplementStepWhoseAgentReportsNoSkillStopsTheItemBeforeTheGate(t *testing.T) {
	t.Parallel()

	rig, file := exampleRig(t, func(int) string { return "approve" })

	reporting := rig.h.agents.result
	rig.h.agents.result = func(request AgentRequest) agent.Result {
		result := reporting(request)
		result.Skills = nil

		return result
	}

	_, err := runPipeline(rig.h.engine(t), file)

	require.Error(t, err)
	assert.Contains(t, err.Error(), "REFUSE")
	assert.Contains(t, err.Error(), "did not call the skill "+exampleSkill(t))
	assert.Zero(t, rig.h.gate.calls, "no gate runs when the implement step reported no skill")
	assert.Equal(t, []string{"opus", "sonnet"}, rig.models(), "the plan step and the implement step ran, and no review ran after them")
}

func TestExample_AFullReviewThatApprovesWithAnOpenFindingIsAChangesVerdictAndSendsTheRunToAFix(t *testing.T) {
	t.Parallel()

	rig, file := exampleRigWithFindings(t, func(int) string { return "approve" }, func(review int) string {
		if review == 2 {
			return `[{"class":"minor","text":"rename x"}]`
		}

		return "[]"
	})

	result, err := runPipeline(rig.h.engine(t), file)

	require.NoError(t, err)
	assert.Equal(t, []string{"opus", "sonnet", "opus", "opus", "sonnet", "opus", "opus"}, rig.models(), "the scoped review approved, the full review approved with a finding open and sent the run to a fix, then the next scoped review and full review approved")
	assert.True(t, childOf(t, result).Approved)
}
