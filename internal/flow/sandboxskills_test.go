package flow

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/tobocop2/beehave/internal/model"
)

func TestAgentStep_ASandboxCallGoesToTheAgentRequestWithTheSkillsItNames(t *testing.T) {
	t.Parallel()

	rig := newAgentRig(t)
	rig.reply("", exited)

	_, err := rig.run(t, agentFlow(`sandbox = sandbox(network = ["api.example.org"], skills = ["review", "oracle"]), `), "x")

	require.NoError(t, err)
	assert.Equal(t, &model.Sandbox{Network: []string{"api.example.org"}, Skills: []string{"review", "oracle"}}, sandboxSeen(t, rig))
}

func TestAgentStep_ASkillNameThatIsNoFolderNameStopsTheStepBeforeTheAgentStarts(t *testing.T) {
	t.Parallel()

	for extra, want := range map[string]string{
		`sandbox = sandbox(skills = "review"), `: "skills must be a list, not a text.",
		`sandbox = sandbox(skills = [3]), `:      "skills must hold strings only",
		`sandbox = sandbox(skills = ["a/b"]), `:  "is not the name of a skill",
		`sandbox = sandbox(skills = [".."]), `:   "is not the name of a skill",
		`sandbox = sandbox(skills = ["."]), `:    "is not the name of a skill",
		`sandbox = sandbox(skills = [""]), `:     "is not the name of a skill",
		`sandbox = sandbox(skills = ["a\\b"]), `: "is not the name of a skill",
		`sandbox = sandbox(read = []), `:         "Use one of: network, skills.",
		`sandbox = sandbox(skills = [3] + []), `: "skills must hold strings only",
	} {
		t.Run(extra, func(t *testing.T) {
			t.Parallel()

			rig := newAgentRig(t)

			_, err := rig.run(t, agentFlow(extra), "x")

			require.ErrorContains(t, err, want)
			assert.Empty(t, rig.h.agents.seen())
		})
	}
}

func TestRecalledSandbox_ReadsBackTheSkillsThatRecordSandboxWrote(t *testing.T) {
	t.Parallel()

	box := &model.Sandbox{Skills: []string{"review"}}

	raw, err := json.Marshal(recordSandbox(box))
	require.NoError(t, err)

	var round map[string]any
	require.NoError(t, json.Unmarshal(raw, &round))

	got, err := recalledSandbox(round)
	require.NoError(t, err)
	assert.Equal(t, box, got)
}
