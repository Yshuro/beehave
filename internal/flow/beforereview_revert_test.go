package flow

import (
	"encoding/json"
	"testing"

	"github.com/dbos-inc/dbos-transact-golang/dbos"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/tobocop2/beehave/internal/model"
)

// revertChecks are clean checks whose revert run found the names passing on the base code, count of them in all.
func revertChecks(count int, names ...string) *GateChecks {
	checks := cleanChecks()
	checks.Revert, checks.RevertPassed, checks.RevertPassedCount = model.RevertFail, names, count

	return checks
}

func TestBeforeReview_ANewTestThatPassesOnTheBaseIsAFindingThatNamesIt(t *testing.T) {
	t.Parallel()

	tree := reviewTreeOf(t, "src/a.go", "add a")
	h := reviewHarness(t, tree, revertChecks(3, "pkg TestA"))

	body := reviewFlow(tree.base, "") + "command(run = [\"revert\", r.revert])\n"
	_, err := walkText(t, h, bodyFlow(body))

	require.NoError(t, err)
	require.Len(t, h.commands.ran(), 2)
	assert.Contains(t, h.commands.ran()[0], "show False")
	assert.Contains(t, h.commands.ran()[0], "the new test pkg TestA passes or is skipped on the base code. A test must fail without the change")
	assert.Contains(t, h.commands.ran()[0], "3 new tests pass or are skipped on the base code in all, and the log names the first 1")
	assert.Equal(t, "revert fail", h.commands.ran()[1])
}

func TestBeforeReview_ARevertRunThatProvesNothingIsAFindingWithItsReason(t *testing.T) {
	t.Parallel()

	cases := map[string]struct {
		problem, finds string
	}{
		"no test case":  {"the report of the revert run holds no test case, so the run proves nothing", "holds no test case"},
		"no test_paths": {"the project sets no test_command or no test_paths, so the new tests cannot run on the base code", "no test_paths"},
		"no end marker": {"", "no end marker of the revert run"},
	}

	for name, c := range cases {
		checks := revertChecks(0)
		checks.RevertProblem = c.problem

		findings := checkFindings(*checks)

		require.Len(t, findings, 1, name)
		assert.Contains(t, findings[0], "the revert run proves nothing", name)
		assert.Contains(t, findings[0], c.finds, name)
	}
}

func TestBeforeReview_AHeadWithNoNewTestGetsNoRevertFinding(t *testing.T) {
	t.Parallel()

	zero := 0
	findings := checkFindings(GateChecks{Complete: true, Secrets: &zero, Revert: model.RevertFail})

	assert.Equal(t, []string{"the head adds no new test. Add a test that fails without the change"}, findings)
}

func TestStatus_TheGateStepLineShowsTheRevertVerdict(t *testing.T) {
	t.Parallel()

	line := func(name string, checks *GateChecks) StepLine {
		data, err := json.Marshal(stepResult{Outputs: gateOutputs(true, "h1", "t1", "", stepsPass, nil, checks)})
		require.NoError(t, err)

		return callLine([]dbos.StepInfo{{StepName: name, Output: string(data)}}, name)
	}

	assert.Equal(t, model.RevertPass, line("gate:g#1", cleanChecks()).Revert)
	assert.Equal(t, model.RevertFail, line("gate:g#2", revertChecks(1, "pkg TestA")).Revert)
	assert.Equal(t, model.RevertFail, line("gate:g#3", &GateChecks{}).Revert, "a candidate gate with no revert run fails")
	assert.Equal(t, model.RevertFail, line("gate:g#4", nil).Revert, "a gate with no candidate checks proves nothing")

	data, err := json.Marshal(line("gate:g#1", cleanChecks()))
	require.NoError(t, err)
	assert.Contains(t, string(data), `"revert":"pass"`)
}

func TestBeforeReview_TheGateResultCarriesTheRevertVerdict(t *testing.T) {
	t.Parallel()

	assert.Equal(t, "pass", gateOutputs(true, "h", "t", "", nil, nil, cleanChecks())[revertField])
	assert.Equal(t, "fail", gateOutputs(true, "h", "t", "", nil, nil, nil)[revertField], "a gate with no candidate checks proves nothing")
}
