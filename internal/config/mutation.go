package config

import (
	"go.starlark.net/starlark"
	"go.starlark.net/syntax"

	"github.com/tobocop2/beehave/internal/model"
)

// defaultMutationSlots is the number of mutation jobs for each build host when the host block leaves `mutation_slots` out.
const defaultMutationSlots = 1

// MutationSlots lists the build hosts that the projects declare, with the number of mutation jobs that each host runs at once. Projects share a host by name,
// so a host that two projects declare runs as many jobs as the project with the larger `mutation_slots` allows.
func MutationSlots(projects map[string]Project) map[string]int {
	slots := map[string]int{}

	for _, project := range projects {
		count := defaultMutationSlots
		if project.Host != nil {
			count = project.Host.MutationSlots
		}

		for _, name := range buildHostNames(project) {
			slots[name] = max(slots[name], count)
		}
	}

	if _, found := slots[LocalHost]; found {
		slots[LocalHost] = defaultMutationSlots
	}

	return slots
}

// buildHostNames lists the build hosts of a project: its `build_hosts`, else its one `build_host`.
func buildHostNames(project Project) []string {
	if len(project.BuildHosts) == 0 {
		return []string{project.BuildHost}
	}

	return project.BuildHosts
}

// rawMutation is a mutation block as written. At is the position of its call.
type rawMutation struct {
	At      syntax.Position
	Command text
}

// mutationSpec is the schema of the mutation constructor.
func mutationSpec() spec { return spec{"mutation", []string{"command"}} }

// newMutation is the constructor of the mutation block.
func newMutation(at syntax.Position, kwargs []starlark.Tuple) (*rawMutation, error) {
	raw := &rawMutation{At: at}
	err := mutationSpec().unpack(at, kwargs, &raw.Command)

	return raw, err
}

// mutationOf checks the mutation block and returns its command. A project with no block has no mutation command.
// A round of the lane kind impl changes code, so it needs the command: the round has a mutation entry on every candidate.
func mutationOf(raw rawProject) (string, error) {
	block := raw.Mutation
	if block == nil {
		return "", requireMutation(raw)
	}

	if err := requireText(block.At, "mutation.command", block.Command.Value); err != nil {
		return "", err
	}

	return block.Command.Value, nil
}

// requireMutation refuses a project whose rounds block has the lane kind impl and that has no mutation block.
func requireMutation(raw rawProject) error {
	if raw.Rounds == nil || (raw.Rounds.LaneKind.Set && raw.Rounds.LaneKind.Value != model.LaneImpl) {
		return nil
	}

	return refuseAt(raw.Rounds.At, "a round of the lane kind impl needs the project to set `mutation`. Add `mutation = mutation(command = \"make mutation-pr BASE=origin/{base}\")`.")
}
