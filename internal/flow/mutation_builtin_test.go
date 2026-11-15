package flow

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	gatepkg "github.com/tobocop2/beehave/internal/gate"
	"github.com/tobocop2/beehave/internal/host/ssh"
	"github.com/tobocop2/beehave/internal/host/sshtest"
	"github.com/tobocop2/beehave/internal/model"
)

// mutationCommandCall is one call of the mutator: the host it got and the job.
type mutationCommandCall struct {
	host string
	job  MutationJob
}

// sshMutator runs the command of a mutation job on the fake ssh host and gives back the log that the test scripted for the job.
type sshMutator struct {
	host *ssh.Host
	m    *sshtest.Machine
	log  string

	mu    sync.Mutex
	calls []mutationCommandCall
}

func (s *sshMutator) Mutate(ctx context.Context, _, _, host string, job MutationJob) (MutationRun, error) {
	s.mu.Lock()
	s.calls = append(s.calls, mutationCommandCall{host: host, job: job})
	s.mu.Unlock()

	id, err := s.host.Submit(ctx, model.JobSpec{Name: "mutation", Command: job.Command})
	if err != nil {
		return MutationRun{}, fmt.Errorf("submit the mutation job: %w", err)
	}

	s.m.Units[string(id)].Log = s.log

	tail, err := s.host.Log(ctx, id)
	if err != nil {
		return MutationRun{}, fmt.Errorf("read the log of the mutation job: %w", err)
	}

	return MutationRun{Failed: true, Log: tail}, nil
}

func (s *sshMutator) recorded() []mutationCommandCall {
	s.mu.Lock()
	defer s.mu.Unlock()

	return append([]mutationCommandCall(nil), s.calls...)
}

// builtinRun walks a workflow whose body is the statements, over an engine whose mutation runner is the mutator, in the item folder of the tree.
func builtinRun(t *testing.T, tree reviewTree, mutator mutator, chooseHost bool, body string) (ItemResult, *harness, error) {
	t.Helper()

	h := newHarness(t)
	h.trees = &fakeTrees{dir: tree.dir}

	deps := h.deps()
	deps.Mutation = mutator
	deps.MutationSlots = map[string]int{"box": 1}

	if chooseHost {
		deps.Hosts = &upHosts{started: true}
		body = "host_up(name = \"start\", host = \"box\")\n" + body
	} else {
		deps.Facts = func(string) model.Project { return model.Project{Name: testProject, BuildHost: "box"} }
	}

	engine, err := NewEngine(t.Context(), h.opts, deps)
	require.NoError(t, err)
	require.NoError(t, engine.Launch())
	t.Cleanup(func() { require.NoError(t, engine.Shutdown()) })

	result, err := engine.RunFile(testItem, testProject, writeFlow(t, bodyFlow(body)))

	return result, h, err
}

func TestMutation_TheBuiltinRunsTheCommandOnTheHostAndGivesOneFindingFromTheJobLog(t *testing.T) {
	t.Parallel()

	const finding = "show False internal/a/a.go:1 a mutant survived: ARITHMETIC_BASE. The tests do not fail when this change is made 1"

	cases := []struct {
		name       string
		chooseHost bool
		lived      string
		useOther   bool
		want       string
	}{
		{name: "the host that host_up chose", chooseHost: true, lived: "internal/a/a.go:1", want: finding},
		{name: "the default build host of the project", lived: "internal/a/a.go:1", want: finding},
		{name: "a base on another branch still gives the line that the head adds", chooseHost: true, lived: "internal/a/a.go:1", useOther: true, want: finding},
		{name: "a base on another branch gives no finding for a line that only the other branch changed", chooseHost: true, lived: "shared.go:1", useOther: true, want: "show True  0"},
		{name: "a lived mutant on a line the diff does not change", chooseHost: true, lived: "internal/a/a.go:9", want: "show True  0"},
		{name: "a lived mutant in a file the head does not change", chooseHost: true, lived: "elsewhere.txt:1", want: "show True  0"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			tree := reviewTreeOf(t, "internal/a/a.go", "add a")
			base := tree.base

			if tc.useOther {
				tree = divergedTree(t)
				base = tree.other
			}

			host, machine := fakeSSHHost(t)
			mutator := &sshMutator{host: host, m: machine, log: strings.Repeat("gremlins output line\n", 2000) + counts(4, 1) +
				"mutation-pr: lived " + tc.lived + " ARITHMETIC_BASE\n" + "mutation-pr: end count=1 printed=1\nbeehave-gate-done\n"}

			call := "r = mutation(name = \"m\", base = \"" + base + "\", head = \"" + tree.head + "\", command = \"make mutation-pr BASE=origin/{base}\")\n" +
				"command(run = [\"show\", str(r.ok), \"|\".join(r.findings), str(r.survived)])\n"

			_, h, err := builtinRun(t, tree, mutator, tc.chooseHost, call)
			require.NoError(t, err)

			assert.Equal(t, []string{tc.want}, h.commands.ran())

			calls := mutator.recorded()
			require.Len(t, calls, 1, "one job ran")
			assert.Equal(t, map[bool]string{true: "box", false: ""}[tc.chooseHost], calls[0].host, "the host argument of Mutate is the chosen host, and empty when no host was chosen. This does not show which queue ran the job")
			assert.Equal(t, "make mutation-pr BASE=origin/"+base, calls[0].job.Command, "the base of the call stands in for {base}")
			assert.Equal(t, tree.head, calls[0].job.Head)
		})
	}
}

// divergedTree is an item folder whose base holds shared.go. The branch "elsewhere" (other) changes line 1 of shared.go, and the head adds internal/a/a.go
// and leaves shared.go as the base has it. The diff `other..head` reads line 1 of shared.go as changed by the head, and `other...head` does not.
func divergedTree(t *testing.T) reviewTree {
	t.Helper()

	dir, _ := gitTree(t)
	putUnder(t, dir, "shared.go", "a\n")
	gitIn(t, dir, "add", "shared.go")
	gitIn(t, dir, "commit", "-q", "-m", "base")

	base := gitIn(t, dir, "rev-parse", "HEAD")

	gitIn(t, dir, "checkout", "-q", "-b", "elsewhere")
	putUnder(t, dir, "shared.go", "z\n")
	gitIn(t, dir, "add", "shared.go")
	gitIn(t, dir, "commit", "-q", "-m", "elsewhere")

	other := gitIn(t, dir, "rev-parse", "HEAD")

	gitIn(t, dir, "checkout", "-q", "-b", "work", base)
	putUnder(t, dir, "internal/a/a.go", "x")
	gitIn(t, dir, "add", "internal/a/a.go")
	gitIn(t, dir, "commit", "-q", "-m", "add a")

	return reviewTree{dir: dir, base: base, other: other, head: gitIn(t, dir, "rev-parse", "HEAD")}
}

// failingMutator fails every job with the error.
type failingMutator struct{ mutateErr error }

func (f failingMutator) Mutate(context.Context, string, string, string, MutationJob) (MutationRun, error) {
	return MutationRun{}, f.mutateErr
}

func TestMutation_AFailedMutateFailsTheStepWithItsError(t *testing.T) {
	t.Parallel()

	tree := reviewTreeOf(t, "internal/a/a.go", "add a")
	mutateErr := fmt.Errorf("%w: project p builds on this machine", gatepkg.ErrNoMutationHost)

	_, h, err := builtinRun(t, tree, failingMutator{mutateErr: mutateErr}, true,
		"mutation(name = \"m\", base = \""+tree.base+"\", head = \""+tree.head+"\", command = \"make mutation-pr\")\ncommand(run = [\"after\"])\n")

	require.Error(t, err)
	require.ErrorIs(t, err, gatepkg.ErrNoMutationHost, "the error keeps its identity across the child run")
	assert.Empty(t, h.commands.ran(), "the steps after the failed mutation do not run")
}
