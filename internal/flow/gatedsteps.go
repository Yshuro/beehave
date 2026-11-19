package flow

import (
	"context"

	"go.starlark.net/starlark"
	"go.starlark.net/starlarkstruct"

	gatepkg "github.com/tobocop2/beehave/internal/gate"
)

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

// recordGate records the outputs of a gate call as its own step, so a replay reads them and runs no gate again.
func (s *script) recordGate(label string, outputs map[string]any) (starlark.Value, error) { //nolint:ireturn // the builtin contract of starlark-go
	fields, err := s.durableFields("gate", label, nil, func(context.Context, string) (stepResult, error) {
		return stepResult{Outputs: outputs}, nil
	})
	if err != nil {
		return nil, err
	}

	return starlarkstruct.FromStringDict(starlarkstruct.Default, fields), nil
}
