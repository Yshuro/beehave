package main

import (
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/tobocop2/beehave/internal/agent"
	"github.com/tobocop2/beehave/internal/agent/claude"
	"github.com/tobocop2/beehave/internal/model"
)

func TestSessionTap_RecordsASkillOnlyWhenItsCallHasAResultWithNoError(t *testing.T) {
	t.Parallel()

	tap := newSessionTap(claude.New(nil), agent.DefaultPrices(), nil)

	for _, line := range []string{
		`{"type":"assistant","message":{"content":[{"type":"tool_use","id":"toolu_1","name":"Skill","input":{"skill":"review"}}]}}`,
		`{"type":"user","message":{"content":[{"type":"tool_result","tool_use_id":"toolu_1","is_error":true,"content":"<tool_use_error>Unknown skill: review</tool_use_error>"}]}}`,
		`{"type":"assistant","message":{"content":[{"type":"tool_use","id":"toolu_2","name":"Skill","input":{"skill":"beehave-skills:review"}}]}}`,
		`{"type":"user","message":{"content":[{"type":"tool_result","tool_use_id":"toolu_2","content":"Launching skill: beehave-skills:review"}]}}`,
		`{"type":"assistant","message":{"content":[{"type":"tool_use","id":"toolu_3","name":"Skill","input":{"skill":"oracle"}}]}}`,
	} {
		_, err := tap.Write([]byte(line + "\n"))
		require.NoError(t, err)
	}

	assert.Equal(t, []string{"review"}, tap.skillCalls(),
		"the Unknown skill call and the call with no result do not count; the plugin skill counts by its plain name")
}

func TestSandboxPlan_TheSkillsOfTheStepComeFromTheConfigFolderOfTheRunner(t *testing.T) {
	t.Parallel()

	r := newSandboxRig(t)
	r.touch(t, filepath.Join(r.home, "login", "skills", "review", "SKILL.md"))
	r.touch(t, filepath.Join(r.home, "login", "skills", "other", "SKILL.md"))

	req := request()
	req.Sandbox = &model.Sandbox{Skills: []string{"review"}}

	_, err := r.f.agents.Run(t.Context(), req)
	require.NoError(t, err)
	require.NotNil(t, r.f.spec.Sandbox)
	assert.Equal(t, []string{filepath.Join(r.home, "login", "skills", "review")}, r.f.spec.Sandbox.Skills, "only the skill that the step names")
	assert.Contains(t, r.f.spec.Sandbox.Deny, filepath.Join(r.home, "login"), "the config folder of the runner stays denied")
}

func TestSandboxPlan_ASkillThatTheConfigFolderDoesNotHoldStopsTheStepBeforeItStarts(t *testing.T) {
	t.Parallel()

	r := newSandboxRig(t)
	r.touch(t, filepath.Join(r.home, "login", "skills", "empty", "notes.md"))

	for _, name := range []string{"review", "empty"} {
		req := request()
		req.Sandbox = &model.Sandbox{Skills: []string{name}}

		got, err := r.f.agents.Run(t.Context(), req)

		require.NoError(t, err)
		assert.Equal(t, agent.OutcomeNotStarted, got.Outcome, name)
		assert.Contains(t, got.Note, "skill "+name, name)
		assert.Contains(t, got.Note, filepath.Join(r.home, "login", "skills", name), name)
		assert.Contains(t, got.Note, "did not start", name)
	}
}

func TestSandboxPlan_ASkillOnARunnerWithNoConfigFolderStopsTheStep(t *testing.T) {
	t.Parallel()

	r := newSandboxRig(t)
	r.f.agents.runners[0].ConfigDir = ""

	req := request()
	req.Sandbox = &model.Sandbox{Skills: []string{"review"}}

	got, err := r.f.agents.Run(t.Context(), req)

	require.NoError(t, err)
	assert.Equal(t, agent.OutcomeNotStarted, got.Outcome)
	assert.Contains(t, got.Note, "skill review")
	assert.Contains(t, got.Note, "config folder")
}
