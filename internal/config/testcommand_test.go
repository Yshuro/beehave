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

func TestLoadFolder_TestPathsLoadIntoTheProject(t *testing.T) {
	t.Parallel()

	project, problems := loadProjectText(t, call(localProject+`, test_paths = ["**/*_test.go", "**/testdata/**"]`))

	require.Empty(t, problems)
	require.Equal(t, []string{"**/*_test.go", "**/testdata/**"}, project.TestPaths)
}

func TestLoadFolder_AnEmptyTestPathIsRefused(t *testing.T) {
	t.Parallel()

	_, problems := loadProjectText(t, call(localProject+`, test_paths = ["tests/**", ""]`))

	require.NotEmpty(t, problems)
	require.Contains(t, problems.Error(), "test_paths")
}
