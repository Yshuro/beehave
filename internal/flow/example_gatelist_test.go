package flow

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// These tests run the gate list step of the example: the round derives its gate list from the gate files of the project.

const (
	ciFile   = ".github/workflows/ci.yaml"
	lintFile = ".github/workflows/lint.yaml"
)

func TestExampleProfile_TheGateListOfTheRoundIsDerivedFromTheGateFiles(t *testing.T) {
	t.Parallel()

	rig := newRoundRig(t, "")
	rig.project.Vars["gate_files"] = []any{ciFile, lintFile}

	_, err := rig.run(t)

	require.NoError(t, err)
	assert.Equal(t, []string{"remedy gatelist --gate " + rig.project.Gate + " " + ciFile + " " + lintFile}, commandsNamed(rig, "remedy gatelist"))
}

func TestExampleProfile_AGateThatLacksADerivedStepStopsTheRoundBeforeItsFirstPass(t *testing.T) {
	t.Parallel()

	rig := newRoundRig(t, "")
	rig.project.Vars["gate_files"] = []any{ciFile}

	previous := rig.h.commands.reply
	rig.h.commands.reply = func(request CommandRequest) (CommandOutput, error) {
		if len(request.Run) > 1 && request.Run[1] == "gatelist" {
			return CommandOutput{RC: 2, Stdout: "FAIL: the gate of the round lacks derived steps: ship:test"}, nil
		}

		return previous(request)
	}

	result, err := rig.run(t)

	require.NoError(t, err)
	require.NotNil(t, result.Stopped)
	assert.Equal(t, 2, result.Stopped.RC)
	assert.Contains(t, result.Stopped.Output, "lacks derived steps")
	assert.Empty(t, commandsNamed(rig, "remedy sync"), "no pass ran after the refusal")
}

func TestExampleProfile_AProjectWithNoGateFilesDerivesNoGateList(t *testing.T) {
	t.Parallel()

	rig := newRoundRig(t, "")

	_, err := rig.run(t)

	require.NoError(t, err)
	assert.Empty(t, commandsNamed(rig, "remedy gatelist"))
}
