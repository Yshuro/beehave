package config_test

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"
)

// movedSettings are the method settings that a project writes in vars, each with a value of the shape it had at the top of the project call.
var movedSettings = map[string]string{ //nolint:gosec // G101: token_rules is the name of a setting, not a credential
	"people": `{"owner": "ann"}`, "epics": `{"bugs": "E-1"}`, "gate_files": `["ci.yml"]`, "conventions": `["AGENTS.md"]`, "brief_rules": `["a rule"]`,
	"stacked_rules": `["a rule"]`, "limits": `["a limit"]`, "traps": `["a trap"]`, "token_rules": `["a rule"]`, "changelog": `"CHANGELOG.md"`,
	"board": `{"number": 3, "started": "In Progress", "merged": "Done"}`,
}

func TestLoadFolder_AMethodSettingAtTheTopOfAProjectIsRefusedAndSaysToMoveItUnderVars(t *testing.T) {
	t.Parallel()

	for key, value := range movedSettings {
		_, problems := loadProjectText(t, "\n"+call(localProject+", "+key+" = "+value))

		require.Len(t, problems, 1, key)
		require.Contains(t, problems[0].Error(), "p.star:2:", key)
		require.Contains(t, problems[0].Error(), "`"+key+"`", key)
		require.Contains(t, problems[0].Error(), "Move it into `vars`", key)
	}
}

func TestLoadFolder_TheBoardBlockCallIsNoLongerDefined(t *testing.T) {
	t.Parallel()

	_, problems := loadProjectText(t, "\n"+call(localProject+`, vars = {"board": board(number = 3, started = "a", merged = "b")}`))

	require.NotEmpty(t, problems)
	require.Contains(t, problems[0].Error(), "board")
}

func TestLoadFolder_TheMethodSettingsUnderVarsReachTheProfileAsWritten(t *testing.T) {
	t.Parallel()

	project, problems := loadProjectText(t, call(localProject+`, vars = {"traps": ["a trap"], "changelog": "CHANGELOG.md", "board": {"number": 3, "started": "In Progress", "merged": "Done"}}`))

	require.Empty(t, problems)
	require.Equal(t, map[string]any{
		"traps": []any{"a trap"}, "changelog": "CHANGELOG.md", "board": map[string]any{"number": json.Number("3"), "started": "In Progress", "merged": "Done"},
	}, project.Vars)
}
