package main

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/tobocop2/beehave/internal/config"
	"github.com/tobocop2/beehave/internal/exectest"
	"github.com/tobocop2/beehave/internal/flow"
	"github.com/tobocop2/beehave/internal/model"
	"github.com/tobocop2/beehave/internal/proc"
)

func TestDisposableTree_DoesNotRunThePostCheckoutHookOfTheRepository(t *testing.T) {
	t.Parallel()

	dir := repoWithCommit(t)
	marker := filepath.Join(t.TempDir(), "hook-ran")
	hook := filepath.Join(dir, ".git", "hooks", "post-checkout")
	require.NoError(t, os.MkdirAll(filepath.Dir(hook), 0o700))
	require.NoError(t, exectest.Write(hook, "#!/bin/sh\ntouch '"+marker+"'\n"))

	work, release, err := disposableTree(t.Context(), dir, t.TempDir())
	require.NoError(t, err)

	defer release()

	assert.DirExists(t, work)
	assert.NoFileExists(t, marker, "the worktree add ran the hook of the repository")
}

func putGitOnPath(t *testing.T, script string) {
	t.Helper()

	bin := t.TempDir()
	require.NoError(t, exectest.Write(filepath.Join(bin, "git"), script))
	t.Setenv("PATH", strings.Join([]string{bin, "/usr/bin", "/bin"}, string(os.PathListSeparator)))
}

func TestGitIn_AFailureKeepsTheTextOfGit(t *testing.T) { //nolint:paralleltest // t.Setenv changes PATH for the process
	putGitOnPath(t, "#!/bin/sh\necho 'fatal: not a repository' >&2\nexit 128\n")

	_, err := gitIn(t.Context(), t.TempDir(), "status")

	require.Error(t, err)
	assert.Contains(t, err.Error(), "exited with code 128")
	assert.Contains(t, err.Error(), "fatal: not a repository")
}

func TestGitIn_ATimeoutReadsAsATimeout(t *testing.T) { //nolint:paralleltest // t.Setenv changes PATH for the process
	putGitOnPath(t, "#!/bin/sh\nsleep 5\n")

	ctx, cancel := context.WithTimeout(t.Context(), 200*time.Millisecond)
	defer cancel()

	_, err := gitIn(ctx, t.TempDir(), "status")

	require.Error(t, err)
	assert.Contains(t, err.Error(), "timed out")
	assert.NotContains(t, err.Error(), "canceled")
}

func TestGitIn_ACancelReadsAsACancel(t *testing.T) { //nolint:paralleltest // t.Setenv changes PATH for the process
	putGitOnPath(t, "#!/bin/sh\nsleep 5\n")

	ctx, cancel := context.WithCancel(t.Context())
	time.AfterFunc(200*time.Millisecond, cancel)

	_, err := gitIn(ctx, t.TempDir(), "status")

	require.Error(t, err)
	assert.Contains(t, err.Error(), "was canceled")
}

func TestEveryPathToGitRunsWithTheHooksOff(t *testing.T) { //nolint:paralleltest // t.Setenv changes PATH for the process
	bin := t.TempDir()
	log := filepath.Join(bin, "calls")
	putGitOnPath(t, "#!/bin/sh\necho \"$@\" >> "+log+"\n")

	dir := t.TempDir()
	paths := map[string]func() model.CommandResult{
		"gitIn": func() model.CommandResult {
			out, err := gitIn(t.Context(), dir, "status")

			return model.CommandResult{Stdout: out, SetupErr: err}
		},
		"the item worktrees": func() model.CommandResult {
			return newWorktrees(nil, flow.Options{StateDir: dir}, nil).git(dir).Run(t.Context(), []string{"status"})
		},
		"the batch and gate": func() model.CommandResult {
			return newPrograms(config.Config{}, nil, adapters{}).local("git", dir, 0).Run(t.Context(), []string{"status"})
		},
	}

	for name, run := range paths {
		require.NoError(t, os.WriteFile(log, nil, 0o600))
		assert.NoError(t, run().SetupErr, name)
		data, err := os.ReadFile(log) //nolint:gosec // a file of the test
		require.NoError(t, err, name)
		assert.True(t, strings.HasPrefix(string(data), "-c core.hooksPath=/dev/null "), "%s ran git with %q", name, data)
	}
}

func TestGitFailure_AToolThatCannotStartNamesTheSetupError(t *testing.T) {
	t.Parallel()

	reason := gitFailure(t.Context(), model.CommandResult{ExitCode: proc.ExitCapFailed, SetupErr: errors.New("no count")})

	assert.Contains(t, reason, "could not start")
	assert.Contains(t, reason, "no count")
}

func TestGitIn_ReturnsWhenAChildOfGitKeepsTheOutputOpen(t *testing.T) {
	state := t.TempDir()
	pidFile := filepath.Join(state, "child.pid")
	release := filepath.Join(state, "release")
	// The child holds the output pipe of git until the test creates the release file. Git ends only after the child wrote its pid, so the child runs when git ends.
	script := "#!/bin/sh\n" +
		"sh -c 'echo $$ > \"$1\"; while [ ! -e \"$2\" ]; do sleep 0.05; done' child '" + pidFile + "' '" + release + "' &\n" +
		"while [ ! -s '" + pidFile + "' ]; do sleep 0.01; done\n" +
		"exit 0\n"
	bin := t.TempDir()
	require.NoError(t, exectest.Write(filepath.Join(bin, "git"), script))
	t.Setenv("PATH", strings.Join([]string{bin, "/usr/bin", "/bin"}, string(os.PathListSeparator)))
	t.Cleanup(func() { assert.NoError(t, os.WriteFile(release, nil, 0o600)) })

	_, err := gitIn(t.Context(), t.TempDir(), "status")

	require.Error(t, err, "the runner reports the pipe that the child kept open")

	text, readErr := os.ReadFile(pidFile) //nolint:gosec // a file of the test
	require.NoError(t, readErr)

	pid, convErr := strconv.Atoi(strings.TrimSpace(string(text)))
	require.NoError(t, convErr)
	require.NoError(t, syscall.Kill(pid, 0), "the call waited for the child that holds the output pipe: the child is gone")
}
