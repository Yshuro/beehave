package flow

import (
	"context"
	"encoding/json"

	"go.starlark.net/starlark"
	"go.starlark.net/starlarkstruct"

	gatepkg "github.com/tobocop2/beehave/internal/gate"
)

// gatedCall is one gate call of a run: the head it gated, as the Starlark value of the record, and the checks its log gave.
type gatedCall struct {
	head starlark.Value
	// checks is what the log of the gate job gave. A gate with no checks holds the zero value, which is not Complete.
	checks GateChecks
}

// requiredSteps are the names of the steps that the gate list of the project gives now. A project with a gate string has no list, and a gate records none.
// The gate calls it and records the names in the required field of its result.
func (e *Engine) requiredSteps(project string) []string {
	if e.deps.Facts == nil {
		return nil
	}

	facts := e.deps.Facts(project)
	if len(facts.GateSteps) == 0 {
		return nil
	}

	return gatepkg.StepNames(facts)
}

// recordGate records the outputs of a gate call as its own step, so a replay reads them and runs no gate again, and remembers the head and checks of the call.
// A replay makes the same gate calls in the same order, so it fills the same list.
func (s *script) recordGate(label string, outputs map[string]any) (starlark.Value, error) { //nolint:ireturn // the builtin contract of starlark-go
	fields, err := s.durableFields("gate", label, nil, func(context.Context, string) (stepResult, error) {
		return stepResult{Outputs: outputs}, nil
	})
	if err != nil {
		return nil, err
	}

	s.gated = append(s.gated, gatedCall{head: fields["head"], checks: recordedChecks(outputs["checks"])})

	return starlarkstruct.FromStringDict(starlarkstruct.Default, fields), nil
}

// recordedChecks reads the checks of a gate record. A record that holds none, or anything else, reads as the zero value: not Complete.
func recordedChecks(raw any) GateChecks {
	var checks GateChecks

	data, err := json.Marshal(raw)
	if err != nil || json.Unmarshal(data, &checks) != nil {
		return GateChecks{}
	}

	return checks
}
