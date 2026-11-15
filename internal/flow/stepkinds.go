package flow

import (
	"errors"
	"fmt"
	"slices"
)

// The table of step kinds is the union of the kinds of the modules that the engine runs (step.Registry.Kinds): the start of the name
// `<kind>:<label>#<n>` of each counted step that a run records. script.name is the one place that names such a step, and it refuses a kind
// that is not in the table. A test holds the table of the module of this package under the golden key stepsVersion, so a rename, an addition
// or a removal of a kind fails that test until the version of the module that owns the kind is bumped in moduleTable (stepmodules.go).

var errUnknownStepKind = errors.New("the step kind is not in the table of step kinds")

// checkStepKind refuses a step named with a kind that the table does not list. The fault is in the build, not in a workflow:
// a new builtin must add its kind to its module, and so to the golden data, before it records a step.
func checkStepKind(kinds []string, kind string) error {
	if !slices.Contains(kinds, kind) {
		return fmt.Errorf("%w: %q. Add it to the kinds of its module and bump the version of that module in moduleTable (stepmodules.go)", errUnknownStepKind, kind)
	}

	return nil
}
