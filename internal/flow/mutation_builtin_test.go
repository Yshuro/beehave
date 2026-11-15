package flow

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/dbos-inc/dbos-transact-golang/dbos"
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
func builtinRun(t *testing.T, tree reviewTree, mutator mutator, chooseHost bool, body string, tweaks ...func(*Deps)) (*Engine, *harness, error) {
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

	for _, tweak := range tweaks {
		tweak(&deps)
	}

	engine, err := NewEngine(t.Context(), h.opts, deps)
	require.NoError(t, err)
	require.NoError(t, engine.Launch())
	t.Cleanup(func() { require.NoError(t, engine.Shutdown()) })

	_, err = engine.RunFile(testItem, testProject, writeFlow(t, bodyFlow(body)))

	return engine, h, err
}

// jobsOnQueue counts the mutation jobs that the queue named queue ran or holds.
func jobsOnQueue(t *testing.T, engine *Engine, queue string) int {
	t.Helper()

	rows, err := dbos.ListWorkflows(engine.ctx, dbos.WithFilterQueueName(queue), dbos.WithFilterName(mutationJobName))
	require.NoError(t, err)

	return len(rows)
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

			engine, h, err := builtinRun(t, tree, mutator, tc.chooseHost, call)
			require.NoError(t, err)

			assert.Equal(t, 1, jobsOnQueue(t, engine, "mutation:box"), "the job ran on the mutation queue of the build host")
			assert.Equal(t, 0, jobsOnQueue(t, engine, "mutation:local"), "no job ran on the queue of this machine")

			assert.Equal(t, []string{tc.want}, h.commands.ran())

			calls := mutator.recorded()
			require.Len(t, calls, 1, "one job ran")
			assert.Equal(t, map[bool]string{true: "box", false: ""}[tc.chooseHost], calls[0].host, "the host argument of Mutate is the chosen host, and empty when no host was chosen")
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

func TestMutation_ACallThatCannotRunFailsTheStepWithItsReason(t *testing.T) {
	t.Parallel()

	tree := reviewTreeOf(t, "internal/a/a.go", "add a")
	call := func(base, head string) string {
		return "mutation(name = \"m\", base = \"" + base + "\", head = \"" + head + "\", command = \"make mutation-pr\")\n"
	}

	tests := []struct {
		name    string
		mutator mutator
		tweak   func(*Deps)
		byFacts bool
		base    string
		head    string
		want    string
	}{
		{name: "a base that is no branch name", mutator: failingMutator{}, base: "a b", head: tree.head, want: "mutation: base is"},
		{name: "no mutation runner", base: tree.base, head: tree.head, want: "this build cannot run the mutation command"},
		{
			name: "a build host with no queue", mutator: failingMutator{}, base: tree.base, head: tree.head, byFacts: true, want: `build host "stranger" has no mutation queue`,
			tweak: func(d *Deps) { d.Facts = func(string) model.Project { return model.Project{BuildHost: "stranger"} } },
		},
		{
			name: "an item with no folder", mutator: okMutator{}, base: tree.base, head: tree.head, want: "find the folder of item",
			tweak: func(d *Deps) { d.Trees = &fakeTrees{err: errFake} },
		},
		{name: "a head that the folder does not hold", mutator: okMutator{}, base: tree.base, head: "0000000000000000000000000000000000000000", want: "read the lines that the head changed since the base"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			tweak := tc.tweak
			if tweak == nil {
				tweak = func(*Deps) {}
			}

			_, h, err := builtinRun(t, tree, tc.mutator, !tc.byFacts, call(tc.base, tc.head)+"command(run = [\"after\"])\n", tweak)

			require.ErrorContains(t, err, tc.want)
			assert.Empty(t, h.commands.ran(), "the steps after the refused call do not run")
		})
	}
}

func TestMutation_AJobThatTheQueueRefusesFailsWithTheItem(t *testing.T) {
	t.Parallel()

	h := newHarness(t)
	deps := h.deps()
	deps.Mutation = okMutator{}
	deps.MutationSlots = map[string]int{"box": 1}

	engine, err := NewEngine(t.Context(), h.opts, deps)
	require.NoError(t, err)
	require.NoError(t, engine.Launch())
	require.NoError(t, engine.Shutdown())

	_, err = engine.enqueueMutation(&script{e: engine, in: ItemInput{Project: testProject, Item: "7"}, hostChosen: "box", ctx: engine.ctx}, mutationCall{head: "A", command: "make mutation-pr"})

	require.ErrorContains(t, err, "queue the mutation job of item 7")
}

// okMutator answers every job with a log that holds one lived mutant on the line that the head adds.
type okMutator struct{}

func (okMutator) Mutate(context.Context, string, string, string, MutationJob) (MutationRun, error) {
	return MutationRun{Failed: true, Log: counts(4, 1) + "mutation-pr: lived internal/a/a.go:1 ARITHMETIC_BASE\nmutation-pr: end count=1 printed=1\n"}, nil
}

func TestMutation_ARunThatStopsWhileItWaitsCancelsItsMutationJob(t *testing.T) {
	t.Parallel()

	tree := reviewTreeOf(t, "internal/a/a.go", "add a")
	started := &atomic.Int32{}

	h := newHarness(t)
	deps := h.deps()
	release := make(chan struct{})
	deps.Mutation = holdingMutator{started: started, release: release}
	deps.MutationSlots = map[string]int{"box": 1}

	engine, err := NewEngine(t.Context(), h.opts, deps)
	require.NoError(t, err)
	require.NoError(t, engine.Launch())
	t.Cleanup(func() { require.NoError(t, engine.Shutdown()) })
	t.Cleanup(func() { close(release) })

	runCtx, stop := dbos.WithTimeout(engine.ctx, time.Hour)
	defer stop()

	failed := make(chan error, 1)

	go func() {
		_, queueErr := engine.queueMutation(&script{e: engine, in: ItemInput{Project: testProject, Item: "7"}, hostChosen: "box", ctx: runCtx}, mutationCall{head: tree.head, command: "make mutation-pr"})
		failed <- queueErr
	}()

	eventually(t, func() bool { return started.Load() == 1 })
	stop()

	require.Error(t, <-failed)

	require.Eventually(t, func() bool {
		rows, listErr := dbos.ListWorkflows(engine.ctx, dbos.WithFilterName(mutationJobName), dbos.WithFilterStatus(dbos.WorkflowStatusCancelled))

		return listErr == nil && len(rows) == 1
	}, 10*time.Second, pollEvery, "the mutation job is canceled")
}

func TestMutation_AMutationQueueThatCannotBeRegisteredFailsWithTheHost(t *testing.T) {
	t.Parallel()

	h := newHarness(t)

	engine, err := NewEngine(t.Context(), h.opts, h.deps())
	require.NoError(t, err)
	require.NoError(t, engine.Launch())
	require.NoError(t, engine.Shutdown())

	require.ErrorContains(t, engine.registerMutationQueue("box", 1), "register the mutation queue of host box")
}
