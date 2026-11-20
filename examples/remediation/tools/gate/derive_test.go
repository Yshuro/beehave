package gate_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/tobocop2/beehave/examples/remediation/tools/check"
	"github.com/tobocop2/beehave/examples/remediation/tools/gate"
)

func derivedAll(t *testing.T, text string) gate.Derived {
	t.Helper()

	got, err := gate.Derive([]gate.File{{Path: "ci.yaml", Text: text}})
	require.NoError(t, err)

	return got
}

func derived(t *testing.T, text string) []gate.Step {
	t.Helper()

	return derivedAll(t, text).Steps
}

func refusedDerive(t *testing.T, text string) string {
	t.Helper()

	_, err := gate.Derive([]gate.File{{Path: "ci.yaml", Text: text}})

	var refusal *check.Refusal

	require.ErrorAs(t, err, &refusal)

	return refusal.Message
}

func TestDerive_AStepIsNamedByItsJobAndItsNameOrItsPlaceInTheJob(t *testing.T) {
	t.Parallel()

	steps := derived(t, "jobs:\n  b:\n    steps:\n      - run: one\n      - name: '***'\n        run: two\n      - name: Run  the Tests!\n        run: three\n")

	assert.Equal(t, []gate.Step{{Name: "b:run-1", Command: "one"}, {Name: "b:run-2", Command: "two"}, {Name: "b:run-the-tests", Command: "three"}}, steps)
}

func TestDerive_TheJobsAreInTheOrderOfTheFileAndTheirNamesAreNotSortedByTheMap(t *testing.T) {
	t.Parallel()

	steps := derived(t, "jobs:\n  zeta:\n    steps:\n      - run: z\n  alpha:\n    steps:\n      - run: a\n")

	assert.Equal(t, []gate.Step{{Name: "zeta:run-1", Command: "z"}, {Name: "alpha:run-1", Command: "a"}}, steps)
}

func TestDerive_ACommandOfSeveralLinesIsOneStepWithoutBlankLinesOrComments(t *testing.T) {
	t.Parallel()

	steps := derived(t, "jobs:\n  a:\n    steps:\n      - run: |\n          # install\n          go install x \\\n            -v\n\n          make   test\n")

	assert.Equal(t, []gate.Step{{Name: "a:run-1", Command: "go install x -v; make test"}}, steps)
}

func TestDerive_ARunStepWithNoCommandAndAStepWithNoRunAddNoStep(t *testing.T) {
	t.Parallel()

	got := derivedAll(t, "jobs:\n  a:\n    steps:\n      - uses: actions/checkout@v7\n      - run: \"\"\n      - run: make test\n")

	assert.Equal(t, []gate.Step{{Name: "a:run-3", Command: "make test"}}, got.Steps)
	assert.Empty(t, got.Refused)
}

func TestDerive_AnExpressionOfTheWorkflowInACommandDoesNotStopTheRead(t *testing.T) {
	t.Parallel()

	steps := derived(t, "jobs:\n  a:\n    steps:\n      - run: make test SLICE=${{ matrix.slice }}\n")

	assert.Equal(t, []gate.Step{{Name: "a:run-1", Command: "make test SLICE=${{ matrix.slice }}"}}, steps)
}

func TestDerive_ATwoStepsOfOneNameGetANumber(t *testing.T) {
	t.Parallel()

	steps := derived(t, "jobs:\n  a:\n    steps:\n      - name: Test\n        run: one\n      - name: Test\n        run: two\n      - name: Test\n        run: three\n")

	assert.Equal(t, []string{"a:test", "a:test-2", "a:test-3"}, []string{steps[0].Name, steps[1].Name, steps[2].Name})
}

func TestDerive_AFileThatIsNotAWorkflowIsRefusedWithItsPathAndTheReason(t *testing.T) {
	t.Parallel()

	for name, test := range map[string]struct{ text, want string }{
		"not_yaml":            {"jobs: [unclosed\n", "ci.yaml: not a workflow file"},
		"jobs_is_a_list":      {"jobs:\n  - a\n", "jobs must be a mapping of job ids to jobs"},
		"a_job_is_a_text":     {"jobs:\n  a: text\n", "job a:"},
		"a_step_is_a_text":    {"jobs:\n  a:\n    steps:\n      - text\n", "job a:"},
		"no_run_step":         {"jobs:\n  a:\n    steps:\n      - uses: actions/checkout@v7\n", "give no run step"},
		"no_jobs":             {"name: CI\n", "give no run step"},
		"only_a_refused_step": {"jobs:\n  a:\n    steps:\n      - name: Ship\n        run: gh release create v1\n", "step a:ship runs `gh release create v1`"},
	} {
		assert.Contains(t, refusedDerive(t, test.text), test.want, name)
	}
}

func TestDerive_AStepThatTheListCannotHoldIsNamedWithItsReasonAndTheOtherStepsStay(t *testing.T) {
	t.Parallel()

	got := derivedAll(t, "jobs:\n  a:\n    steps:\n      - name: Bad\n        run: if then fi )\n      - name: Lint\n        uses: golangci/golangci-lint-action@v8\n      - run: make test\n")

	assert.Equal(t, []gate.Step{{Name: "a:run-3", Command: "make test"}}, got.Steps)
	require.Len(t, got.Refused, 2)
	assert.Equal(t, "step a:bad", got.Refused[0].Name)
	assert.Contains(t, got.Refused[0].Reason, "the gate cannot read")
	assert.Equal(t, "step a:lint", got.Refused[1].Name)
	assert.Contains(t, got.Refused[1].Reason, "uses golangci/golangci-lint-action@v8, an action that is neither a checkout nor a setup")
}

func TestDerive_EveryFileIsReadAndEveryDefectIsNamed(t *testing.T) {
	t.Parallel()

	_, err := gate.Derive([]gate.File{
		{Path: "a.yaml", Text: "jobs: [x\n"},
		{Path: "b.yaml", Text: "jobs: [y\n"},
	})

	var refusal *check.Refusal

	require.ErrorAs(t, err, &refusal)
	assert.Contains(t, refusal.Message, "a.yaml: not a workflow file")
	assert.Contains(t, refusal.Message, "b.yaml: not a workflow file")
}

func TestDerive_AJobThatCanWriteOrReadsASecretIsLeftOutWhole(t *testing.T) {
	t.Parallel()

	for name, text := range map[string]string{
		"job_write":     "jobs:\n  rel:\n    permissions:\n      contents: write\n    steps:\n      - run: make test\n  t:\n    steps:\n      - run: make test\n",
		"job_write_all": "jobs:\n  rel:\n    permissions: write-all\n    steps:\n      - run: make test\n  t:\n    steps:\n      - run: make test\n",
		"job_env":       "jobs:\n  rel:\n    env:\n      TOKEN: ${{ secrets.TOKEN }}\n    steps:\n      - run: make test\n  t:\n    steps:\n      - run: make test\n",
	} {
		got, err := gate.Derive([]gate.File{{Path: "w.yaml", Text: text}})

		require.NoError(t, err, name)
		assert.Equal(t, []gate.Step{{Name: "t:run-1", Command: "make test"}}, got.Steps, name)
		require.Len(t, got.Refused, 1, name)
		assert.Equal(t, "job w.yaml:rel", got.Refused[0].Name, name)
	}
}

func TestDerive_AFileThatCanWriteGivesNoStep(t *testing.T) {
	t.Parallel()

	message := refusedDerive(t, "permissions:\n  id-token: write\njobs:\n  t:\n    steps:\n      - run: make test\n")

	assert.Contains(t, message, "job ci.yaml:t has a write permission")
}

func TestDerive_ARepeatedStepNameFromAnotherFileStaysUnique(t *testing.T) {
	t.Parallel()

	got, err := gate.Derive([]gate.File{
		{Path: "a.yaml", Text: "jobs:\n  t:\n    steps:\n      - run: one\n"},
		{Path: "b.yaml", Text: "jobs:\n  t:\n    steps:\n      - run: two\n"},
	})

	require.NoError(t, err)
	assert.Equal(t, []string{"t:run-1", "t:run-1-2"}, []string{got.Steps[0].Name, got.Steps[1].Name})
}

func TestMissing_ADerivedStepThatTheGateTextDoesNotHoldIsNamed(t *testing.T) {
	t.Parallel()

	steps := []gate.Step{{Name: "a:fmt", Command: "make fmt"}, {Name: "a:test", Command: "make test"}}

	assert.Equal(t, []string{"a:test"}, gate.Missing(steps, "fmt: make fmt; lint: make lint"))
	assert.Empty(t, gate.Missing(steps, "fmt: make fmt; test: make test"))
}
