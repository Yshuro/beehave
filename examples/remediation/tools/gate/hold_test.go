package gate_test

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/tobocop2/beehave/examples/remediation/tools/gate"
)

func TestHold_AMapThatNamesEachDerivedCommandToGateStepsHasNoDefect(t *testing.T) {
	t.Parallel()

	steps := []gate.Step{{Name: "a:vet", Command: "make vet"}, {Name: "a:tools", Command: "go install x@v1"}, {Name: "b:vet", Command: "make vet"}}

	assert.Empty(t, gate.Hold(steps, []string{"vet"}, gate.Map{"make vet": {"vet"}, "go install x@v1": {}}))
}

func TestHold_EachDefectIsOneLineInTheOrderUnmappedStaleUnknown(t *testing.T) {
	t.Parallel()

	steps := []gate.Step{{Name: "a:test", Command: "make test-real-tools"}, {Name: "a:vet", Command: "make vet"}}
	held := gate.Map{"make test": {"test"}, "make vet": {"vet", "lint"}, "make lint": {"lint", "test"}}

	assert.Equal(t, []string{
		"unmapped a:test: make test-real-tools",
		"stale make lint",
		"stale make test",
		"unknown-step lint",
	}, gate.Hold(steps, []string{"test", "vet"}, held))
}

func TestHold_AKeyMatchesOnlyTheExactCommandText(t *testing.T) {
	t.Parallel()

	steps := []gate.Step{{Name: "a:gov", Command: "make governance-verify"}}

	for name, key := range map[string]string{
		"a trailing space": "make governance-verify ",
		"a prefix":         "make governance",
		"a longer command": "make governance-verify all",
		"another case":     "make Governance-verify",
	} {
		assert.Contains(t, gate.Hold(steps, []string{"g"}, gate.Map{key: {"g"}}), "unmapped a:gov: make governance-verify", name)
	}
}
