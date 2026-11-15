package config_test

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/tobocop2/beehave/internal/config"
	"github.com/tobocop2/beehave/internal/model"
)

func TestConfig_AHostBlockReadsItsSlotClasses(t *testing.T) {
	t.Parallel()

	project, problems := loadProjectText(t, call(clusterProject+", host = "+hostCall(`, slots = {"mutation": 1, "bench": 2}`)))

	require.Empty(t, problems)
	require.Equal(t, map[string]int{"mutation": 1, "bench": 2}, project.Host.Slots)

	project, problems = loadProjectText(t, call(clusterProject+", host = "+hostCall("")))

	require.Empty(t, problems)
	require.Nil(t, project.Host.Slots, "a host block with no slots names no class")
}

func TestSlotCount_AHostThatProjectsShareRunsAsManyJobsAsTheLargestCountAllows(t *testing.T) {
	t.Parallel()

	projects := map[string]config.Project{
		"one":   {Project: model.Project{Name: "one", BuildHost: "gpu", Host: &model.HostConfig{Slots: map[string]int{"mutation": 1}}}},
		"two":   {Project: model.Project{Name: "two", BuildHost: "cpu", BuildHosts: []string{"cpu", "gpu"}, Host: &model.HostConfig{Slots: map[string]int{"mutation": 3, "bench": 2}}}},
		"local": {Project: model.Project{Name: "local", BuildHost: config.LocalHost}},
	}

	count, named := config.SlotCount(projects, "gpu", "mutation")
	require.True(t, named)
	require.Equal(t, 3, count)

	count, named = config.SlotCount(projects, "cpu", "mutation")
	require.True(t, named)
	require.Equal(t, 3, count)

	_, named = config.SlotCount(projects, "gpu", "fuzz")
	require.False(t, named, "no project names the class")

	_, named = config.SlotCount(projects, "tpu", "mutation")
	require.False(t, named, "no project declares the host")
}

func TestConfig_AHostBlockRefusesASlotCountThatIsNotAWholeNumberAboveZero(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		slots string
		words string
	}{
		{"zero", `{"mutation": 0}`, "`host.slots` class \"mutation\" must be above zero and at most 1048576; got 0"},
		{"negative", `{"bench": 2, "mutation": -1}`, "`host.slots` class \"mutation\" must be above zero and at most 1048576; got -1"},
		{"huge", `{"bench": 17179869184}`, "`host.slots` class \"bench\" must be above zero and at most 1048576; got 17179869184"},
		{"beyond 64 bits", `{"bench": 1180591620717411303424}`, "`host.slots` class \"bench\" must be above zero and at most 1048576"},
		{"text", `{"mutation": "1"}`, "`host.slots` class \"mutation\" holds a string, want a whole number above zero"},
		{"fraction", `{"mutation": 1.5}`, "`host.slots` class \"mutation\" holds a float, want a whole number above zero"},
		{"blank class", `{" ": 1}`, "`host.slots` has the class \" \". Use letters, digits"},
		{"class with a colon", `{"a:b": 1}`, "`host.slots` has the class \"a:b\""},
		{"not a dict", `["mutation"]`, "`host.slots`: got list, want a dict of class names to counts"},
		{"class that is not a name", `{1: 1}`, "`host.slots` has the key 1, want a class name"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			_, problems := loadProjectText(t, call(clusterProject+", host = "+hostCall(", slots = "+tc.slots)))

			require.Len(t, problems, 1)
			require.ErrorIs(t, problems[0], config.ErrRefused)
			require.ErrorContains(t, problems[0], tc.words)
		})
	}
}
