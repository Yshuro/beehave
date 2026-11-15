package config_test

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestLoadFolder_ATestCommandLoadsIntoTheProject(t *testing.T) {
	t.Parallel()

	project, problems := loadProjectText(t, call(localProject+`, test_command = "make test"`))

	require.Empty(t, problems)
	require.Equal(t, "make test", project.TestCommand)
}

func TestLoadFolder_AProjectWithoutATestCommandHasNone(t *testing.T) {
	t.Parallel()

	project, problems := loadProjectText(t, call(localProject))

	require.Empty(t, problems)
	require.Empty(t, project.TestCommand)
}

func TestLoadFolder_AnEmptyTestCommandIsRefused(t *testing.T) {
	t.Parallel()

	_, problems := loadProjectText(t, call(localProject+`, test_command = ""`))

	require.NotEmpty(t, problems)
	require.Contains(t, problems.Error(), "test_command")
}
