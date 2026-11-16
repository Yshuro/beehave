package flow

import (
	"fmt"
	"strconv"
	"testing"

	"github.com/dbos-inc/dbos-transact-golang/dbos"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// gateWaitFlow records a gate step, then waits for the owner.
const gateWaitFlow = "def main(item):\n    gate(name = \"g\")\n    wait(name = \"ask\", question = \"What now?\")\n"

// factsWaitFlow records a facts step, then waits for the owner.
const factsWaitFlow = "def main(item):\n    facts()\n    wait(name = \"ask\", question = \"What now?\")\n"

// buildWithModule is a client of another build whose module is at another version.
func buildWithModule(c *Client, module string, version int) *Client {
	c.version = versionWithModule(otherBuild, module, version)

	return c
}

func TestClient_ResumeOnThisBuildMovesARunThatRecordedNoStepOfTheModuleThatChanged(t *testing.T) {
	t.Parallel()

	harness, _, other := strandedHarness(t)
	buildWithModule(other, moduleGate, 2)
	expireLease(harness)

	require.NoError(t, other.ResumeOnThisBuild(testItem), "the run recorded agent steps and no gate step")

	row, err := other.row(testRun)
	require.NoError(t, err)
	assert.Equal(t, other.version, row.ApplicationVersion)
}

func TestClient_ResumeOnThisBuildRefusesARunThatRecordedAStepOfTheModuleThatChanged(t *testing.T) {
	t.Parallel()

	harness, _, other := strandedHarness(t)
	buildWithModule(other, moduleAgent, agentVersion+1)
	expireLease(harness)

	err := other.ResumeOnThisBuild(testItem)

	require.ErrorIs(t, err, errStranded)
	require.ErrorContains(t, err, fmt.Sprintf("agent module at version %d and this build has version %d", agentVersion, agentVersion+1))
	require.ErrorContains(t, err, "Start the item again")

	row, rowErr := other.row(testRun)
	require.NoError(t, rowErr)
	assert.Equal(t, testVersion, buildVersion(row.ApplicationVersion), "a refused run stays where it was")
}

func TestClient_ResumeOnThisBuildRefusesARunThatRecordedAGateStepWhenTheGateModuleChanged(t *testing.T) {
	t.Parallel()

	harness := newHarness(t)
	harness.flowIs(t, gateWaitFlow)
	harness.gate.results = []GateResult{{Passed: true, Head: gatedHead}}

	runInBackground(harness.engine(t), testItem)
	waitForTheOwner(t, harness)
	expireLease(harness)

	other := buildWithModule(harness.client(t, otherBuild), moduleGate, 2)

	err := other.ResumeOnThisBuild(testItem)

	require.ErrorIs(t, err, errStranded)
	require.ErrorContains(t, err, "gate module")
}

func TestClient_ResumeOnThisBuildMovesARunStoredUnderTheSingleVersionOfTheSteps(t *testing.T) {
	t.Parallel()

	harness, _, other := strandedHarness(t)
	_, err := sqlOf(t, harness).ExecContext(t.Context(), "UPDATE workflow_status SET application_version = ? WHERE workflow_uuid = ?", testVersion+"+"+stepsVersion, testRun)
	require.NoError(t, err)

	buildWithModule(other, moduleAgent, 1)
	expireLease(harness)

	require.NoError(t, other.ResumeOnThisBuild(testItem), "the single version stands for the agent module at version 1, and this build has the agent module at version 1")

	row, rowErr := other.row(testRun)
	require.NoError(t, rowErr)
	assert.Equal(t, other.version, row.ApplicationVersion)
}

func TestClient_ResumeOnThisBuildRefusesARunStoredUnderTheSingleVersionOfTheStepsWhenTheCoreModuleChanged(t *testing.T) {
	t.Parallel()

	harness := newHarness(t)
	harness.flowIs(t, factsWaitFlow)

	runInBackground(harness.engine(t), testItem)
	waitForTheOwner(t, harness)

	other := harness.client(t, otherBuild)
	_, err := sqlOf(t, harness).ExecContext(t.Context(), "UPDATE workflow_status SET application_version = ? WHERE workflow_uuid = ?", testVersion+"+"+stepsVersion, testRun)
	require.NoError(t, err)

	expireLease(harness)

	require.ErrorContains(t, other.ResumeOnThisBuild(testItem), "core module at version 1 and this build has version 3", "the facts result of the old build has the round_* fields and no vars field")
}

func TestClient_ResumeOnThisBuildRefusesARunStoredUnderTheSingleVersionWhenAUsedModuleChanged(t *testing.T) {
	t.Parallel()

	harness, _, other := strandedHarness(t)
	_, err := sqlOf(t, harness).ExecContext(t.Context(), "UPDATE workflow_status SET application_version = ? WHERE workflow_uuid = ?", testVersion+"+"+stepsVersion, testRun)
	require.NoError(t, err)

	expireLease(harness)

	require.ErrorContains(t, other.ResumeOnThisBuild(testItem), fmt.Sprintf("agent module at version 1 and this build has version %d", agentVersion))
}

func TestClient_ResumeOnThisBuildRefusesARunStoredUnderAMalformedOrRepeatingList(t *testing.T) {
	t.Parallel()

	for tail, want := range map[string]string{
		"core1.gate":  `entry "gate" of the step list "core1.gate"`,
		"core1.core2": `names the module core twice`,
	} {
		harness, _, other := strandedHarness(t)
		_, err := sqlOf(t, harness).ExecContext(t.Context(), "UPDATE workflow_status SET application_version = ? WHERE workflow_uuid = ?", testVersion+"+"+tail, testRun)
		require.NoError(t, err)

		expireLease(harness)

		err = other.ResumeOnThisBuild(testItem)
		require.ErrorIs(t, err, errStranded)
		require.ErrorContains(t, err, want)
	}
}

func TestClient_AStepListThatTheStoreCannotReadRefusesTheMoveWithTheWordsOfTheRead(t *testing.T) {
	t.Parallel()

	_, _, other := strandedHarness(t)
	last, err := other.newest(testItem)
	require.NoError(t, err)
	require.NoError(t, dbos.Shutdown(other.flow, shutdownTimeout))

	err = other.requireUsedModulesSame(testItem, last, map[string]int{moduleGate: 1}, map[string]int{moduleGate: 2})

	require.ErrorContains(t, err, "read the steps of "+last.Status.ID)
	require.NotErrorIs(t, err, errStranded, "a read that failed says nothing about the step list")
}

func TestClient_ResumeOnThisBuildMovesARunStoredUnderAListOfSomeModules(t *testing.T) {
	t.Parallel()

	harness, _, other := strandedHarness(t)
	_, err := sqlOf(t, harness).ExecContext(t.Context(), "UPDATE workflow_status SET application_version = ? WHERE workflow_uuid = ?", testVersion+"+core1.agent"+strconv.Itoa(agentVersion), testRun)
	require.NoError(t, err)

	buildWithModule(other, moduleGate, 2)
	expireLease(harness)

	require.NoError(t, other.ResumeOnThisBuild(testItem), "the stored list names the modules of the run, and the run used neither the missing ones nor the gate")
}

func TestClient_StepNamesReportsARunThatCannotBeRead(t *testing.T) {
	t.Parallel()

	closed, err := OpenClient(t.Context(), newHarness(t).opts, ReleaseDeps{})
	require.NoError(t, err)
	require.NoError(t, closed.Close())

	_, err = closed.stepNames(testRun)

	require.ErrorContains(t, err, "read the steps of "+testRun)
}
