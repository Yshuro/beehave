package cli_test

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestCheckWorkflow_ExitsZeroForBothWorkflowsOfTheDeskExample(t *testing.T) {
	t.Parallel()

	for _, name := range []string{"workflow.star", "eval.star"} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			code, stdout, stderr := runCheckWorkflow(t, filepath.Join("..", "..", "examples", "desk", name))

			require.Equal(t, 0, code, stderr)
			require.Empty(t, stdout)
			require.Empty(t, stderr)
		})
	}
}

func TestDeskExample_TheScriptsPassTheirOwnTests(t *testing.T) {
	t.Parallel()

	runDeskScriptTests(t, t.TempDir())
}

func TestDeskExample_TheScriptsPassTheirOwnTestsInAFolderNamedWithTheCardDigits(t *testing.T) {
	t.Parallel()

	folder := filepath.Join(t.TempDir(), "TestDeskExample4111234567")
	require.NoError(t, os.Mkdir(folder, 0o700))

	runDeskScriptTests(t, folder)
}

func runDeskScriptTests(t *testing.T, tmpdir string) {
	t.Helper()

	for _, tool := range []string{"jq", "awk"} {
		if _, err := exec.LookPath(tool); err != nil {
			t.Skipf("the desk scripts need %s", tool)
		}
	}

	command := exec.CommandContext(t.Context(), "sh", filepath.Join("..", "..", "examples", "desk", "tests", "run.sh")) //nolint:gosec // a test runs the script of the example in its own temporary folder
	command.Env = append(command.Environ(), "TMPDIR="+tmpdir)
	out, err := command.CombinedOutput()

	require.NoError(t, err, string(out))
}
