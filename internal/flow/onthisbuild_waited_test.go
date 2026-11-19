package flow

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// waitThenFail is a workflow file that waits and then fails, so the run ends failed after it received the answer.
func waitThenFail(arguments string) string {
	return "def main(item):\n    wait(name = \"ask\", question = \"What now?\"" + arguments + ")\n    fail(\"stop\")\n"
}

// waitedRun runs a workflow file that waits, answers it, and leaves the failed run with the receive of the answer as its only record of the wait.
// The named steps of the wait are deleted, as a run that an older build recorded holds none of them.
func waitedRun(t *testing.T, flowText, from string) (*harness, *Client) {
	t.Helper()

	harness := newHarness(t)
	harness.flowIs(t, flowText)
	engine := harness.engine(t)
	client := harness.client(t, testVersion)
	done := runInBackground(engine, testItem)

	waitOn(t, ownerAsks(t, harness))
	require.NoError(t, client.AnswerAs(testItem, from, "go"))
	require.Error(t, receive(t, done).err, "the file fails after the wait")
	require.NoError(t, engine.Shutdown())

	_, err := sqlOf(t, harness).ExecContext(t.Context(), "DELETE FROM operation_outputs WHERE workflow_uuid = ? AND function_name LIKE 'wait:%'", testRun)
	require.NoError(t, err)

	other := buildWithModule(harness.client(t, otherBuild), moduleCore, coreVersion+1)

	names, err := other.stepNames(testRun)
	require.NoError(t, err)
	require.Contains(t, names, "DBOS.recv", "the run recorded the receive of the answer")

	for _, name := range names {
		_, named := kindOfStep(name)
		require.False(t, named, "the run recorded no named step of any module, and %q is one: %v", name, names)
	}

	return harness, other
}

func TestClient_ResumeOnThisBuildRefusesARunThatOnlyWaitedWhenTheCoreModuleChanged(t *testing.T) {
	t.Parallel()

	forms := map[string]struct{ arguments, from string }{
		"plain wait":            {"", ""},
		"wait by id":            {", to = [\"ana\"]", "ana"},
		"wait with a deadline":  {", to = [\"ana\"], timeout = \"1h\"", "ana"},
		"plain wait, a timeout": {", timeout = \"1h\"", ""},
	}

	for name, form := range forms {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			_, other := waitedRun(t, waitThenFail(form.arguments), form.from)

			err := other.ResumeOnThisBuild(testItem)

			require.ErrorIs(t, err, errStranded)
			require.ErrorContains(t, err, "core module")

			row, rowErr := other.row(testRun)
			require.NoError(t, rowErr)
			assert.Equal(t, testVersion, buildVersion(row.ApplicationVersion), "a refused run stays where it was")
		})
	}
}

func TestClient_ResumeOnThisBuildMovesARunThatRecordedNothingOfCoreAndDidNotWaitWhenTheCoreModuleChanged(t *testing.T) {
	t.Parallel()

	harness := newHarness(t)
	harness.agents.err = errFake
	engine := harness.engine(t)

	_, err := engine.Run(testItem, testProject)
	require.Error(t, err)
	require.NoError(t, engine.Shutdown())

	harness.agents.mu.Lock()
	harness.agents.err = nil
	harness.agents.mu.Unlock()

	other := buildWithModule(harness.client(t, otherBuild), moduleCore, coreVersion+1)

	names, namesErr := other.stepNames(testRun)
	require.NoError(t, namesErr)
	require.NotContains(t, names, "DBOS.recv", "the run did not wait")
	require.NotContains(t, modulesUsed(names), moduleCore, "the run recorded no step of the core module")

	require.NoError(t, other.ResumeOnThisBuild(testItem))

	row, rowErr := other.row(testRun)
	require.NoError(t, rowErr)
	assert.Equal(t, other.version, row.ApplicationVersion)
}
