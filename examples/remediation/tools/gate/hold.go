package gate

import (
	"maps"
	"slices"
)

// The first words of the defect lines of a hold.
const (
	unmappedLine    = "unmapped "
	staleLine       = "stale "
	unknownStepLine = "unknown-step "
)

// Map is the gate_map of a project: each exact command text of a derived step, to the names of the gate steps that run it. An empty list says the command is not a check.
type Map map[string][]string

// Hold returns one line for each defect of the map against the derived steps and the gate step names of the round: a derived command that is no key, a key that no derived step has, then a name that is no gate step.
func Hold(steps []Step, gateSteps []string, held Map) []string {
	var lines []string

	derived := map[string]bool{}

	for _, step := range steps {
		derived[step.Command] = true

		if _, ok := held[step.Command]; !ok {
			lines = append(lines, unmappedLine+step.Name+": "+step.Command)
		}
	}

	for _, command := range slices.Sorted(maps.Keys(held)) {
		if !derived[command] {
			lines = append(lines, staleLine+command)
		}
	}

	return append(lines, unknownSteps(gateSteps, held)...)
}

// unknownSteps gives one line for each name of the map that is no gate step, sorted and once each.
func unknownSteps(gateSteps []string, held Map) []string {
	var names []string

	for _, list := range held {
		for _, name := range list {
			if !slices.Contains(gateSteps, name) {
				names = append(names, name)
			}
		}
	}

	slices.Sort(names)

	lines := []string(nil)
	for _, name := range slices.Compact(names) {
		lines = append(lines, unknownStepLine+name)
	}

	return lines
}
