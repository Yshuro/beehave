package flow

import (
	"bytes"
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/tobocop2/beehave/internal/model"
)

func projectVars() map[string]any {
	return map[string]any{"a": json.Number("1"), "b": []any{"x"}, "c": map[string]any{"d": true}, "e": json.Number("1.5")}
}

func TestFacts_TheProjectVarsComeBackAsWritten(t *testing.T) {
	t.Parallel()

	h := newHarness(t)
	h.facts = func(project string) model.Project { return model.Project{Name: project, Vars: projectVars()} }
	h.flowIs(t, "def main(item):\n    f = facts()\n    fail(\"V=%r\" % f.vars)\n")

	outcome := <-runInBackground(h.engine(t), testItem)

	require.ErrorContains(t, outcome.err, `V={"a": 1, "b": ["x"], "c": {"d": True}, "e": 1.5}`)
}

func TestFacts_AProjectWithNoVarsGetsAnEmptyDict(t *testing.T) {
	t.Parallel()

	h := newHarness(t)
	h.flowIs(t, "def main(item):\n    f = facts()\n    fail(\"V=%r/%s\" % (f.vars, type(f.vars)))\n")

	outcome := <-runInBackground(h.engine(t), testItem)

	require.ErrorContains(t, outcome.err, "V={}/dict")
}

func TestFacts_TheVarsOfTheFactsAreFrozen(t *testing.T) {
	t.Parallel()

	h := newHarness(t)
	h.facts = func(project string) model.Project { return model.Project{Name: project, Vars: projectVars()} }
	h.flowIs(t, "def main(item):\n    f = facts()\n    f.vars[\"b\"].append(\"y\")\n")

	outcome := <-runInBackground(h.engine(t), testItem)

	require.ErrorContains(t, outcome.err, "frozen")
}

func TestDryRun_TheFactsHoldTheVarsOfTheProjectAndNothingElse(t *testing.T) {
	t.Parallel()

	var out bytes.Buffer

	file := dryFile(t, "def main(item):\n    f = facts()\n    fail(\"V=%r/P=%r/R=%r\" % (f.vars, f.people, f.repo))\n")
	err := DryRun(t.Context(), file, "12", map[string]string{}, &out, WithVars(projectVars()))

	require.Error(t, err)
	assert.Contains(t, out.String(), `V={"a": 1, "b": ["x"], "c": {"d": True}, "e": 1.5}/P={}/R=""`)
}
