package gate_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/tobocop2/beehave/examples/remediation/tools/check"
	"github.com/tobocop2/beehave/examples/remediation/tools/gate"
)

func derived(t *testing.T, text string) []gate.Step {
	t.Helper()

	steps, err := gate.Derive([]gate.File{{Path: "ci.yaml", Text: text}})
	require.NoError(t, err)

	return steps
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

	steps := derived(t, "jobs:\n  a:\n    steps:\n      - uses: actions/checkout@v7\n      - run: \"\"\n      - run: make test\n")

	assert.Equal(t, []gate.Step{{Name: "a:run-3", Command: "make test"}}, steps)
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
		"not_yaml":               {"jobs: [unclosed\n", "ci.yaml: not a workflow file"},
		"jobs_is_a_list":         {"jobs:\n  - a\n", "jobs must be a mapping of job ids to jobs"},
		"a_job_is_a_text":        {"jobs:\n  a: text\n", "job a:"},
		"no_run_step":            {"jobs:\n  a:\n    steps:\n      - uses: actions/checkout@v7\n", "hold no run step"},
		"no_jobs":                {"name: CI\n", "hold no run step"},
		"a_line_is_not_shell":    {"jobs:\n  a:\n    steps:\n      - name: Bad\n        run: if then fi )\n", "the step a:bad has a line that is not shell (if then fi ))"},
		"an_action_that_deploys": {"jobs:\n  a:\n    steps:\n      - uses: actions/deploy-pages@v4\n", "ci.yaml: the step a:run-1 publishes (deploy: uses actions/deploy-pages)"},
		"a_step_that_releases":   {"jobs:\n  a:\n    steps:\n      - name: Ship\n        run: gh release create v1\n", "ci.yaml: the step a:ship publishes (release: gh release create). Take the step out of the gate files"},
	} {
		assert.Contains(t, refusedDerive(t, test.text), test.want, name)
	}
}

func TestDerive_AStepThatPublishesIsRefusedWhereverItSitsInTheJobAndTheFile(t *testing.T) {
	t.Parallel()

	first := "jobs:\n  a:\n    steps:\n      - run: npm publish\n      - run: make test\n"
	last := "jobs:\n  a:\n    steps:\n      - run: make test\n  b:\n    steps:\n      - run: |\n          make build\n          make test\n          twine upload dist/*\n"

	assert.Contains(t, refusedDerive(t, first), "the step a:run-1 publishes")
	assert.Contains(t, refusedDerive(t, last), "the step b:run-1 publishes (package publish: twine upload)")
}

func TestDerive_EveryFileIsReadAndEveryDefectIsNamed(t *testing.T) {
	t.Parallel()

	_, err := gate.Derive([]gate.File{
		{Path: "a.yaml", Text: "jobs: [x\n"},
		{Path: "b.yaml", Text: "jobs:\n  s:\n    steps:\n      - run: npm publish\n"},
	})

	var refusal *check.Refusal

	require.ErrorAs(t, err, &refusal)
	assert.Contains(t, refusal.Message, "a.yaml: not a workflow file")
	assert.Contains(t, refusal.Message, "b.yaml: the step s:run-1 publishes (package publish: npm publish)")
}
