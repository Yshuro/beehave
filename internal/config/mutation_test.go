package config_test

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/tobocop2/beehave/internal/config"
	"github.com/tobocop2/beehave/internal/model"
)

func TestLoadFolder_TheMutationBlockGivesItsCommandToTheProfile(t *testing.T) {
	t.Parallel()

	project, problems := loadProjectText(t, call(localProject+`, mutation = mutation(command = "make mutation-pr BASE=origin/{base}")`))

	require.Empty(t, problems)
	require.Equal(t, "make mutation-pr BASE=origin/{base}", project.MutationCommand)
}

func TestLoadFolder_AProjectWithNoMutationBlockHasNoMutationCommand(t *testing.T) {
	t.Parallel()

	project, problems := loadProjectText(t, call(localProject))

	require.Empty(t, problems)
	require.Empty(t, project.MutationCommand)
}

func TestLoadFolder_ABlankMutationCommandIsRefused(t *testing.T) {
	t.Parallel()

	_, problems := loadProjectText(t, call(localProject+`, mutation = mutation(command = " ")`))

	require.NotEmpty(t, problems)
	require.Contains(t, problems[0].Error(), "`mutation.command` must be a non-empty string")
}

func TestLoadFolder_ARoundOfTheLaneKindImplNeedsTheMutationBlock(t *testing.T) {
	t.Parallel()

	_, problems := loadProjectText(t, call(localProject+", rounds = rounds("+roundBase+`, label = "x")`))

	require.NotEmpty(t, problems)
	require.Contains(t, problems[0].Error(), "a round of the lane kind impl needs the project to set `mutation`")
}

func TestMutationSlots_EachHostHasTheLargestCapOfTheProjectsThatNameIt(t *testing.T) {
	t.Parallel()

	projects := map[string]config.Project{
		"a": {Project: model.Project{BuildHost: "box", BuildHosts: []string{"box", "far"}, Host: &model.HostConfig{MutationSlots: 3}}},
		"b": {Project: model.Project{BuildHost: "box", Host: &model.HostConfig{MutationSlots: 2}}},
		"c": {Project: model.Project{BuildHost: "local"}},
	}

	require.Equal(t, map[string]int{"box": 3, "far": 3, "local": 1}, config.MutationSlots(projects))
}

func TestLoadFolder_APapersRoundNeedsNoMutationBlock(t *testing.T) {
	t.Parallel()

	_, problems := loadProjectText(t, call(localProject+", rounds = rounds("+roundBase+`, label = "x", lane_kind = "papers", allow_paths = ["docs"])`))

	require.Empty(t, problems)
}
