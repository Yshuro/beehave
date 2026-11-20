package flow

import (
	"maps"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/tobocop2/beehave/internal/model"
)

// implementPrompt runs the example with the given project, whose round is that of the example rig with the settings of `round` set, and returns the text that the implement agent received.
func implementPrompt(t *testing.T, project model.Project, round map[string]any) string {
	t.Helper()

	rig, file := exampleRig(t, func(int) string { return "approve" })

	base := exampleProject()
	settings := base.Vars
	maps.Copy(settings, project.Vars)

	project.Name, project.Repo, project.BaseBranch, project.Vars = base.Name, base.Repo, base.BaseBranch, settings
	rig.project = project

	for key, value := range round {
		rig.setRound(key, value)
	}

	_, err := runPipeline(rig.h.engine(t), file)
	require.NoError(t, err)

	for _, request := range rig.h.agents.requests {
		if strings.Contains(request.Prompt, "Acceptance: the script decides") {
			return request.Prompt
		}
	}

	require.FailNow(t, "the run started no implement agent")

	return ""
}

func TestExampleProfile_TheBriefRulesAndTheGateAndTheLimitsOfTheProfileReachTheImplementAgent(t *testing.T) {
	t.Parallel()

	text := implementPrompt(t, model.Project{
		Gate: "make check-profile", Vars: map[string]any{"brief_rules": []any{"name the tempting wrong fix"}, "limits": []any{"never run the reset command"}},
	}, nil)

	require.Contains(t, text, "name the tempting wrong fix")
	require.Contains(t, text, "The gate is `make check-profile`.")
	require.Contains(t, text, "Hard limits, commands never run and real user data:\n\n- never run the reset command")
}

func TestExampleProfile_TheAcceptanceAndBaseResultLinesReachTheImplementAgent(t *testing.T) {
	t.Parallel()

	text := implementPrompt(t, model.Project{}, map[string]any{"acceptance": "/work/accept.sh", "base_result": "/work/base.out"})

	require.Contains(t, text, "ACCEPTANCE=/work/accept.sh BASE_RESULT=/work/base.out")
	require.Contains(t, text, "Base of this pull request: `integration`")
}

func TestExampleProfile_AnItemWithNoAcceptanceScriptSaysSo(t *testing.T) {
	t.Parallel()

	text := implementPrompt(t, model.Project{}, nil)

	require.Contains(t, text, "The run names no acceptance script")
	require.NotContains(t, text, "ACCEPTANCE=")
}

func TestExampleProfile_AProjectWithABuildHostSendsEveryBuildThereAndNeverToThisMachine(t *testing.T) {
	t.Parallel()

	text := implementPrompt(t, model.Project{BuildHost: "rig-1", Forge: "github"}, nil)

	require.Contains(t, text, "Run every build, test and lint on the build host. Never run one on this machine.")
	require.Contains(t, text, "Build host: `rig-1`.")
	require.Contains(t, text, "Make the first commit when the change is green")
	require.NotContains(t, text, "has no build host")
}

func TestExampleProfile_AProjectWithNoBuildHostAndNoForgeBuildsHereAndPushesNothing(t *testing.T) {
	t.Parallel()

	text := implementPrompt(t, model.Project{}, nil)

	require.Contains(t, text, "This project has no build host: run builds and tests on this machine.")
	require.Contains(t, text, "This project has no forge: commit on the branch and push nothing.")
	require.NotContains(t, text, "Never run one on this machine")
}
