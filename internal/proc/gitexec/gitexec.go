// Package gitexec builds the git commands that beehave runs in the trees it manages. Every such command runs with the hooks of the repository off, so a hook
// that the owner or the project put in a tree never runs as the owner. This package is the one place that names the git binary.
//
// A command from New or Command is local: it allows the file transport only and never fetches a missing object from a promisor remote, so no setting
// that an agent writes into the git config of a tree can send it to the network with the credential of the owner. Only NewRemote reaches a remote.
package gitexec

import (
	"context"
	"os"
	"os/exec"
	"slices"
	"time"

	"github.com/tobocop2/beehave/internal/model"
	"github.com/tobocop2/beehave/internal/proc"
)

// Binary is the name of the git program.
const Binary = "git"

// hooksOff is the setting that points the hooks folder at an empty place.
const hooksOff = "core.hooksPath=" + os.DevNull

// local returns the environment of a local git call. GIT_ALLOW_PROTOCOL allows the file transport only, and no git config overrides it.
// GIT_NO_LAZY_FETCH stops the fetch of a missing object from a promisor remote, so a read of a missing object fails. Git 2.45 and later and the
// 2024-05 security releases from 2.39.4 on honor it. An unpatched git older than 2.45 ignores it: no promisor reaches the network, but a file
// promisor still fetches, and an upload-pack program in the clone config then runs (the class of #727).
func local() []string {
	return []string{"GIT_ALLOW_PROTOCOL=file", "GIT_NO_LAZY_FETCH=1"}
}

// Args returns args with the setting that turns the hooks off in front of them.
func Args(args []string) []string {
	return append([]string{"-c", hooksOff}, args...)
}

// Runner runs git in a folder with the hooks off. Its fields are those of proc.ExecRunner. Env comes before the local environment, so it cannot widen it.
type Runner struct {
	proc.ExecRunner

	remote bool
}

// New returns a local runner for git in dir. A zero timeout means no limit.
func New(dir string, timeout time.Duration) Runner {
	return Runner{ExecRunner: proc.ExecRunner{Binary: Binary, Dir: dir, Timeout: timeout}}
}

// NewRemote returns a runner for git in dir that reaches a remote over any transport. Run it only where no agent writes the git config: in a repository
// that only the engine writes, with --git-dir set, or for a clone into a folder that does not exist yet.
func NewRemote(dir string, timeout time.Duration) Runner {
	runner := New(dir, timeout)
	runner.remote = true

	return runner
}

// Run runs git with the hooks off.
func (r Runner) Run(ctx context.Context, args []string) model.CommandResult {
	runner := r.ExecRunner
	if !r.remote {
		runner.Env = append(slices.Clone(r.Env), local()...)
	}

	return runner.Run(ctx, Args(args))
}

// Command returns a local git command in dir with the hooks off, in its own process group, with env on top of the environment of the process.
func Command(ctx context.Context, dir string, args []string, env ...string) *exec.Cmd {
	cmd := proc.GroupCommand(ctx, Binary, Args(args), dir)
	cmd.Env = append(append(cmd.Environ(), env...), local()...)

	return cmd
}

// ReadConfig returns the value that the git config of the repository in dir holds for key. It reads configuration and runs no hook, and the hooks setting
// must not reach it, so it does not use Args.
func ReadConfig(ctx context.Context, dir, key string) model.CommandResult {
	return proc.ExecRunner{Binary: Binary, Dir: dir, Env: local()}.Run(ctx, []string{"config", "--get", key})
}
