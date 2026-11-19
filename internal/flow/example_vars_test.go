package flow

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// These tests run the check of the method settings that the example file makes over the vars of the project. The engine keeps vars a free map,
// so the example is the one place that reads the keys and the one place that checks them.

// goodMethodVars holds every method key that the example reads, each with a valid value.
func goodMethodVars() map[string]any {
	vars := roundVars(roundSettings())
	vars["people"] = map[string]any{"owner": "ann", "idle_hours": "48"}
	vars["epics"] = map[string]any{"api": "42"}
	vars["gate_files"] = []any{".github/workflows/ci.yaml"}
	vars["conventions"] = []any{"AGENTS.md"}
	vars["brief_rules"] = []any{"name the tempting wrong fix"}
	vars["stacked_rules"] = []any{"stack on the parent branch"}
	vars["limits"] = []any{"never run the reset command"}
	vars["traps"] = []any{"the cache hides a stale build"}
	vars["token_rules"] = []any{"never print a token"}
	vars["changelog"] = "docs/CHANGELOG.md"
	vars["board"] = boardVars("acme")

	return vars
}

// varsWith returns the good method vars with the key set to the value.
func varsWith(key string, value any) map[string]any {
	vars := goodMethodVars()
	vars[key] = value

	return vars
}

func TestExampleVars_AGoodSetOfMethodSettingsPassesTheCheck(t *testing.T) {
	t.Parallel()

	shown, err := settingsOf(t, goodMethodVars())

	require.NoError(t, err)
	assert.Contains(t, shown, "tobocop2", "the settings of the round come back")
}

func TestExampleVars_AProjectThatSetsNoMethodSettingPassesTheCheck(t *testing.T) {
	t.Parallel()

	_, err := settingsOf(t, roundVars(roundSettings()))

	require.NoError(t, err)
}

func TestExampleVars_AChangelogInAFormThatGitNeverPrintsIsRefusedAndNamesTheKey(t *testing.T) {
	t.Parallel()

	for name, value := range map[string]any{
		"a leading dot slash": "./CHANGELOG.md",
		"an absolute path":    "/etc/CHANGELOG.md",
		"a parent segment":    "../CHANGELOG.md",
		"an inner parent":     "docs/../CHANGELOG.md",
		"a trailing slash":    "docs/",
		"an empty text":       "",
		"a blank text":        "   ",
		"a number":            json.Number("5"),
		"a list":              []any{"CHANGELOG.md"},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			got := refusalOf(t, varsWith("changelog", value))

			assert.Contains(t, got, "REFUSE: vars.changelog ")
		})
	}
}

func TestExampleVars_AListSettingThatIsNotAListOfNonBlankTextIsRefusedAndNamesTheKey(t *testing.T) {
	t.Parallel()

	for _, key := range []string{"gate_files", "conventions", "brief_rules", "stacked_rules", "limits", "traps", "token_rules"} {
		for name, value := range map[string]any{
			"a text":                  "abc",
			"a dict":                  map[string]any{"a": "b"},
			"a number":                json.Number("5"),
			"a list with a blank":     []any{"a", ""},
			"a list with a blank run": []any{"   "},
			"a list with a number":    []any{json.Number("3")},
			"a list with a nesting":   []any{[]any{"a"}},
		} {
			t.Run(key+" "+name, func(t *testing.T) {
				t.Parallel()

				got := refusalOf(t, varsWith(key, value))

				assert.Contains(t, got, "REFUSE: vars."+key+" ")
			})
		}
	}
}

func TestExampleVars_ADictSettingThatIsNotADictOfNonBlankTextIsRefusedAndNamesTheKey(t *testing.T) {
	t.Parallel()

	for _, key := range []string{"people", "epics"} {
		for name, value := range map[string]any{
			"a list":            []any{"a"},
			"a text":            "ann",
			"a blank name":      map[string]any{" ": "ann"},
			"a blank value":     map[string]any{"owner": ""},
			"a number as value": map[string]any{"owner": json.Number("3")},
		} {
			t.Run(key+" "+name, func(t *testing.T) {
				t.Parallel()

				got := refusalOf(t, varsWith(key, value))

				assert.Contains(t, got, "REFUSE: vars."+key+" ")
			})
		}
	}
}

func TestExampleVars_ABoardThatIsNotANumberedCardColumnPairIsRefusedAndNamesTheKey(t *testing.T) {
	t.Parallel()

	for name, tc := range map[string]struct {
		board any
		key   string
	}{
		"a text instead of a dict": {"3", "board"},
		"a number of 0":            {map[string]any{"number": json.Number("0"), "started": "In Progress", "merged": "Done"}, "board.number"},
		"a number as text":         {map[string]any{"number": "3", "started": "In Progress", "merged": "Done"}, "board.number"},
		"a blank started column":   {map[string]any{"number": json.Number("3"), "started": " ", "merged": "Done"}, "board.started"},
		"a merged column of a int": {map[string]any{"number": json.Number("3"), "started": "In Progress", "merged": json.Number("1")}, "board.merged"},
		"an owner of a number":     {map[string]any{"number": json.Number("3"), "started": "In Progress", "merged": "Done", "owner": json.Number("1")}, "board.owner"},
		"a key that is no setting": {map[string]any{"number": json.Number("3"), "started": "In Progress", "merged": "Done", "column": "x"}, "board.column"},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			got := refusalOf(t, varsWith("board", tc.board))

			assert.Contains(t, got, "REFUSE: vars."+tc.key+" ")
		})
	}
}

func TestExampleVars_ABoardWithAMissingColumnIsRefusedAndNamesTheKey(t *testing.T) {
	t.Parallel()

	for _, key := range []string{"number", "started", "merged"} {
		board := boardVars("")
		delete(board, key)

		got := refusalOf(t, varsWith("board", board))

		assert.Contains(t, got, "vars.board has no "+key)
	}
}

func TestExampleVars_AProjectWithABadChangelogStopsTheItemRunBeforeAnyAgentStarts(t *testing.T) {
	t.Parallel()

	rig, file := exampleRig(t, func(int) string { return "approve" })
	rig.project.Vars["changelog"] = "./CHANGELOG.md"

	_, err := runPipeline(rig.h.engine(t), file)

	require.ErrorContains(t, err, "REFUSE: vars.changelog ")
	assert.Empty(t, rig.h.agents.requests, "the refusal comes before the plan agent")
	assert.Empty(t, rig.h.forge.placed)
}

func TestExampleVars_AProjectWithABadChangelogStopsTheRoundBeforeItStartsAChild(t *testing.T) {
	t.Parallel()

	rig := newRoundRig(t, "21")
	rig.project.Vars["changelog"] = "/etc/CHANGELOG.md"

	_, err := rig.run(t)

	require.ErrorContains(t, err, "REFUSE: vars.changelog ")
	assert.Empty(t, rig.kids.started(), "the round starts no child")
	assert.Empty(t, rig.h.agents.seen(), "the round runs no agent")
}
