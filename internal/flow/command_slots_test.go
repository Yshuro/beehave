package flow

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"sync"
	"testing"

	"github.com/dbos-inc/dbos-transact-golang/dbos"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// slotJobs holds each host job of a test until the test lets it go. The jobs report their start on started, and the log keeps the order of the starts and the ends.
type slotJobs struct {
	mu      sync.Mutex
	log     []string
	started chan string
	release map[string]chan struct{}
}

func newSlotJobs(programs ...string) *slotJobs {
	jobs := &slotJobs{started: make(chan string, len(programs)), release: map[string]chan struct{}{}}

	for _, program := range programs {
		jobs.release[program] = make(chan struct{})
	}

	return jobs
}

// hold is the before hook of the fake commands: the job records its start, then waits for its release or for its context.
func (j *slotJobs) hold(ctx context.Context, request CommandRequest) error {
	program := request.Run[0]

	j.mu.Lock()
	j.log = append(j.log, "start "+program)
	j.mu.Unlock()

	j.started <- program

	select {
	case <-j.release[program]:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// let records the end of a job, then releases it. The record comes first, so no job that waited for the slot of this one can log its start before the end.
func (j *slotJobs) let(program string) {
	j.mu.Lock()
	j.log = append(j.log, "end "+program)
	j.mu.Unlock()

	close(j.release[program])
}

func (j *slotJobs) order() []string {
	j.mu.Lock()
	defer j.mu.Unlock()

	return slices.Clone(j.log)
}

// nextStart returns the program of the next job that starts, and fails the test when the test ends first.
func (j *slotJobs) nextStart(t *testing.T) string {
	t.Helper()

	select {
	case program := <-j.started:
		return program
	case <-t.Context().Done():
		require.FailNow(t, "the test ended before another job started")

		return ""
	}
}

// slotFlow is the workflow file of one item: one host command of the program, in the slot class when the class is not empty.
func slotFlow(program, class string) string {
	if class == "" {
		return bodyFlow(fmt.Sprintf("command(name = \"job\", run = [%q], host = \"box\")", program))
	}

	return bodyFlow(fmt.Sprintf("command(name = \"job\", run = [%q], host = \"box\", slots = %q)", program, class))
}

// runSlotItem runs the workflow file of an item in the background and returns the channel of its end.
func runSlotItem(t *testing.T, engine *Engine, item, text string) <-chan error {
	t.Helper()

	file := writeFlow(t, text)
	ended := make(chan error, 1)

	go func() {
		_, err := engine.RunFile(item, testProject, file)
		ended <- err
	}()

	return ended
}

// enqueuedJobs counts the host jobs that wait in a queue.
func enqueuedJobs(t *testing.T, engine *Engine) int {
	t.Helper()

	rows, err := dbos.ListWorkflows(engine.ctx, dbos.WithFilterName(hostJobName), dbos.WithFilterStatus(dbos.WorkflowStatusEnqueued))
	require.NoError(t, err)

	return len(rows)
}

func TestReopen_ACanceledSlotJobGoesBackToTheQueueOfItsClassInThePartitionOfItsHost(t *testing.T) {
	t.Parallel()

	job := func(in ExclusiveInput) dbos.WorkflowStatus {
		text, err := json.Marshal(in)
		require.NoError(t, err)

		return dbos.WorkflowStatus{Name: hostJobName, Input: string(text)}
	}

	queue, partition, err := gateRewindTarget(job(ExclusiveInput{Kind: kindCommand, HostKey: hostKeyPrefix + "box", Request: CommandRequest{SlotClass: "mutation"}}))
	require.NoError(t, err)
	assert.Equal(t, "slots:mutation", queue)
	assert.Equal(t, "host:box", partition)

	queue, partition, err = gateRewindTarget(job(ExclusiveInput{Kind: kindGate, HostKey: hostKeyPrefix + "box"}))
	require.NoError(t, err)
	assert.Equal(t, hostQueueName, queue, "a build job of a gate keeps the host queue")
	assert.Equal(t, "host:box", partition)
}

func TestCommand_ASlotCommandRunsOnTheHostOfItsGrantAndRecordsItsOutput(t *testing.T) {
	t.Parallel()

	h := newHarness(t)
	h.commands.reply = func(CommandRequest) (CommandOutput, error) { return CommandOutput{Stdout: "killed 3", RC: 2}, nil }

	var asked []string

	h.slots = func(project, cluster, class string) (SlotGrant, error) {
		asked = append(asked, project+"/"+cluster+"/"+class)

		return SlotGrant{Host: "box", Count: 1}, nil
	}

	_, err := walkBody(t, h, "r = command(name = \"mutate\", run = [\"x\"], host = \"build\", slots = \"mutation\")\nif r.rc == 2 and r.stdout == \"killed 3\":\n    command(name = \"saw\", run = [\"saw-the-output\"])\n")

	require.NoError(t, err)
	assert.Equal(t, []string{testProject + "//mutation"}, asked, "the call asks for the class of its project and the host that host_up chose, empty here")
	assert.Equal(t, []string{"x", "saw-the-output"}, h.commands.ran())
	assert.Equal(t, "mutation", h.commands.requests[0].SlotClass)
}

func TestCommand_ASlotClassTheBuildCannotGrantIsRefusedAtTheCall(t *testing.T) {
	t.Parallel()

	tests := map[string]struct {
		slots func(project, cluster, class string) (SlotGrant, error)
		body  string
		words string
	}{
		"the build has no slot classes": {nil, `command(run = ["x"], host = "box", slots = "mutation")`, "the build has no slot classes"},
		"the host names no such class": {
			func(_, _, class string) (SlotGrant, error) {
				return SlotGrant{}, fmt.Errorf("host %q names no slot class %q", "box", class)
			},
			`command(run = ["x"], host = "box", slots = "mutation")`, `host "box" names no slot class "mutation"`,
		},
		"no host":    {nil, `command(run = ["x"], slots = "mutation")`, "slots cannot be used without host"},
		"blank name": {nil, `command(run = ["x"], host = "box", slots = " ")`, "slots must be the name of a slot class"},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			h := newHarness(t)
			h.slots = tc.slots

			_, err := walkBody(t, h, tc.body)

			require.ErrorContains(t, err, tc.words)
			assert.Empty(t, h.commands.ran(), "a refused call runs nothing")
		})
	}
}

func TestCommand_AHostJobWaitsForAFreeSlotOfItsClass(t *testing.T) {
	t.Parallel()

	jobs := newSlotJobs("m1", "m2", "b1", "n1")
	h := newHarness(t)
	h.commands.before = jobs.hold
	h.slots = func(_, _, class string) (SlotGrant, error) {
		return SlotGrant{Host: "box", Count: map[string]int{"mutation": 1, "bench": 1}[class]}, nil
	}

	engine := h.engine(t)

	first := runSlotItem(t, engine, "11", slotFlow("m1", "mutation"))
	require.Equal(t, "m1", jobs.nextStart(t))

	second := runSlotItem(t, engine, "12", slotFlow("m2", "mutation"))
	eventually(t, func() bool { return enqueuedJobs(t, engine) == 1 })

	// A job of another class and a job of no class start while the only mutation slot is taken. The queue of the second mutation job saw its row before them,
	// so the starts of these two are the proof that the queue let the second mutation job wait and did not just fail to look yet.
	bench := runSlotItem(t, engine, "13", slotFlow("b1", "bench"))
	plain := runSlotItem(t, engine, "14", slotFlow("n1", ""))

	started := []string{jobs.nextStart(t), jobs.nextStart(t)}
	assert.ElementsMatch(t, []string{"b1", "n1"}, started, "a job of another class and a job of no class do not wait for the mutation slot")
	assert.ElementsMatch(t, []string{"start m1", "start b1", "start n1"}, jobs.order(), "the second mutation job has not started")
	assert.Equal(t, 1, enqueuedJobs(t, engine), "the second mutation job still waits in its queue")

	jobs.let("m1")
	require.NoError(t, <-first)

	assert.Equal(t, "m2", jobs.nextStart(t), "the second mutation job starts after the first ends")

	order := jobs.order()
	assert.Less(t, slices.Index(order, "end m1"), slices.Index(order, "start m2"), "the second job starts only after the first job ended")

	jobs.let("m2")
	jobs.let("b1")
	jobs.let("n1")

	for _, ended := range []<-chan error{second, bench, plain} {
		require.NoError(t, <-ended)
	}
}
