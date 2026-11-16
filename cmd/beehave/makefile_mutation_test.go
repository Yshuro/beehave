package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// makefile returns the absolute path of the Makefile of the repository.
func makefile(t *testing.T) string {
	t.Helper()

	path, err := filepath.Abs(filepath.Join("..", "..", "Makefile"))
	require.NoError(t, err)

	return path
}

func TestMakefile_SetupInstallsGremlinsAtThePinnedVersion(t *testing.T) {
	t.Parallel()

	if _, err := exec.LookPath("make"); err != nil {
		t.Skip("make is not on the path")
	}

	out, err := exec.CommandContext(t.Context(), "make", "-n", "-C", filepath.Dir(makefile(t)), "setup").CombinedOutput() //nolint:gosec // G204: the folder is the root of this repository
	require.NoError(t, err, string(out))
	assert.Contains(t, string(out), "go install github.com/go-gremlins/gremlins/cmd/gremlins@v0.6.0\n")
}

func TestMakefile_MutationRefusesAPackagePatternWithDots(t *testing.T) {
	t.Parallel()

	if _, err := exec.LookPath("make"); err != nil {
		t.Skip("make is not on the path")
	}

	fake := t.TempDir()
	ran := filepath.Join(fake, "ran")
	require.NoError(t, os.WriteFile(filepath.Join(fake, "gremlins"), []byte("#!/bin/sh\ntouch "+ran+"\n"), 0o700)) //nolint:gosec // the fake must be executable

	command := exec.CommandContext(t.Context(), "make", "-s", "-C", t.TempDir(), "-f", makefile(t), "mutation", "PKG=./internal/...") //nolint:gosec // G204: the arguments are constants of this test and its temporary folders
	// The path holds the fake gremlins first, then the system folders for sh and for the core count that the Makefile reads (sysctl is in /usr/sbin on macOS).
	command.Env = []string{"PATH=" + fake + ":/usr/bin:/bin:/usr/sbin:/sbin"}

	out, err := command.CombinedOutput()
	require.Error(t, err, string(out))
	assert.Contains(t, string(out), `PKG=./internal/... holds "...": gremlins reads it as a folder name and mutates nothing.`)
	assert.NoFileExists(t, ran, "gremlins must not start")
}
