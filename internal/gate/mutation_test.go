package gate_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/tobocop2/beehave/internal/gate"
	"github.com/tobocop2/beehave/internal/model"
)

const fullMutationLog = "build output\r\n" +
	"mutation-pr: applied=5 killed=3 lived=2 timed_out=0\r\n" +
	"mutation-pr: lived internal/a/b.go:12 ARITHMETIC_BASE\n" +
	"mutation-pr: lived c.go:7 CONDITIONALS_NEGATION\n" +
	"mutation-pr: end count=2 printed=2\n" +
	"make: *** [mutation-pr] Error 1\n"

func TestReadMutation_ACompleteLogGivesTheCountsTheSurvivorsAndTheEnd(t *testing.T) {
	t.Parallel()

	read := gate.ReadMutation(fullMutationLog)

	assert.True(t, read.HasResult())
	assert.True(t, read.HasCounts)
	assert.Equal(t, 5, read.Applied)
	assert.Equal(t, 2, read.Lived)
	assert.Equal(t, []gate.Survivor{{File: "internal/a/b.go", Line: 12, Mutator: "ARITHMETIC_BASE"}, {File: "c.go", Line: 7, Mutator: "CONDITIONALS_NEGATION"}}, read.Survivors)
	assert.True(t, read.HasEnd)
	assert.Equal(t, 2, read.Count)
	assert.Equal(t, 2, read.Printed)
	assert.Empty(t, read.Other)
}

func TestReadMutation_ANotApplicableLineAndANoCandidateLineKeepTheirReason(t *testing.T) {
	t.Parallel()

	read := gate.ReadMutation("mutation-pr: not applicable: no Go file changed\nmutation-pr: no candidate: no changed line\n")

	assert.True(t, read.HasNotApplicable)
	assert.Equal(t, "no Go file changed", read.NotApplicable)
	assert.True(t, read.HasNoCandidate)
	assert.Equal(t, "no changed line", read.NoCandidate)
	assert.True(t, read.HasResult())
}

func TestReadMutation_ALogWithNoResultLineHasNoResultAndKeepsNoUnknownLine(t *testing.T) {
	t.Parallel()

	read := gate.ReadMutation("go: downloading x\nmake: *** error\n")

	assert.False(t, read.HasResult())
	assert.Empty(t, read.Other)
}

func TestReadMutation_TheFirstUnknownResultLineIsKept(t *testing.T) {
	t.Parallel()

	read := gate.ReadMutation("mutation-pr: gremlins crashed\nmutation-pr: second\n")

	assert.Equal(t, "gremlins crashed", read.Other)
	assert.False(t, read.HasResult())
}

func TestReadMutation_ALivedLineThatDoesNotReadIsAnUnknownLine(t *testing.T) {
	t.Parallel()

	for _, bad := range []string{"lived nocolon MUT", "lived a.go:x MUT", "lived a.go:0 MUT", "lived :4 MUT"} {
		read := gate.ReadMutation("mutation-pr: " + bad + "\n")

		assert.Empty(t, read.Survivors, bad)
		assert.Equal(t, bad, read.Other, bad)
	}
}

func TestReadMutation_AnEndMarkerWithAPartThatDoesNotReadIsNoEndMarker(t *testing.T) {
	t.Parallel()

	for _, bad := range []string{"end count=2", "end count=2 printed=x", "end count=-1 printed=1", "end printed=1"} {
		assert.False(t, gate.ReadMutation("mutation-pr: "+bad+"\n").HasEnd, bad)
	}
}

func TestReadMutation_TheLastEndMarkerCounts(t *testing.T) {
	t.Parallel()

	read := gate.ReadMutation("mutation-pr: end count=1 printed=1\nmutation-pr: end count=4 printed=3\n")

	assert.Equal(t, 4, read.Count)
	assert.Equal(t, 3, read.Printed)
}

func TestReadMutation_ACountsLinePartThatDoesNotReadGivesZero(t *testing.T) {
	t.Parallel()

	read := gate.ReadMutation("mutation-pr: applied=x killed=2 lived=1 timed_out=1 other=9\n")

	assert.True(t, read.HasCounts)
	assert.Zero(t, read.Applied)
	assert.Equal(t, 1, read.Lived)
}

func mutationProject(buildHost string) model.Project {
	return model.Project{Name: "demo", BuildHost: buildHost}
}

func mutationRequest(buildHost string) gate.MutationRequest {
	return gate.MutationRequest{Project: mutationProject(buildHost), CloneURL: cloneURL, Head: headA, Command: "make mutation-pr BASE=origin/main"}
}

// mutationGate is a gate whose host lookup returns remote, and counts the lookups.
func mutationGate(remote gate.Remote, err error, lookups *int) gate.Gate {
	return gate.New(nil, nil, func(model.Project) (gate.Remote, error) {
		*lookups++

		return remote, err
	})
}

func TestMutate_AProjectThatBuildsOnThisMachineIsRefusedBeforeTheHostLookup(t *testing.T) {
	t.Parallel()

	for _, host := range []string{"", "local"} {
		lookups := 0
		g := mutationGate(nil, errors.New("host lookup ran"), &lookups)

		_, err := g.Mutate(context.Background(), mutationRequest(host))

		require.ErrorIs(t, err, gate.ErrNoMutationHost, host)
		assert.Zero(t, lookups, "the refusal comes before the host lookup")
	}
}

func TestMutate_ARequestWithNoCloneURLIsRefusedBeforeTheHostLookup(t *testing.T) {
	t.Parallel()

	lookups := 0
	g := mutationGate(nil, errors.New("host lookup ran"), &lookups)
	request := mutationRequest("cluster")
	request.CloneURL = ""

	_, err := g.Mutate(context.Background(), request)

	require.ErrorIs(t, err, gate.ErrNoCloneURL)
	assert.Zero(t, lookups)
}

func TestMutate_AHostLookupThatFailsNamesTheProjectAndTheHost(t *testing.T) {
	t.Parallel()

	lookups := 0
	cause := errors.New("no such host")
	g := mutationGate(nil, cause, &lookups)

	_, err := g.Mutate(context.Background(), mutationRequest("cluster"))

	require.ErrorIs(t, err, cause)
	assert.ErrorContains(t, err, "project demo builds on cluster")
}

func TestMutate_ASucceededJobGivesItsLogAndSubmitsTheCommandInAFreshClone(t *testing.T) {
	t.Parallel()

	remote := logRemote{fakeRemote: &fakeRemote{state: model.JobSucceeded}, log: fullMutationLog}
	lookups := 0
	request := mutationRequest("cluster")
	request.Limit = 90 * time.Second

	run, err := mutationGate(remote, nil, &lookups).Mutate(context.Background(), request)

	require.NoError(t, err)
	assert.Equal(t, gate.MutationRun{Log: fullMutationLog}, run)
	require.Len(t, remote.submitted, 1)
	assert.Equal(t, "mutation-demo", remote.submitted[0].Name)
	assert.Contains(t, remote.submitted[0].Command, "git checkout --detach "+headA)
	assert.Contains(t, remote.submitted[0].Command, "timeout 90 sh -c 'make mutation-pr BASE=origin/main'")
}

func TestMutate_ACommandThatRunsWithNoLimitOfTheRequestHasNoTimeoutWord(t *testing.T) {
	t.Parallel()

	remote := logRemote{fakeRemote: &fakeRemote{state: model.JobSucceeded}}
	lookups := 0

	_, err := mutationGate(remote, nil, &lookups).Mutate(context.Background(), mutationRequest("cluster"))

	require.NoError(t, err)
	assert.NotContains(t, remote.submitted[0].Command, "timeout")
}

func TestMutate_AFailedJobGivesItsLogAndSaysItFailed(t *testing.T) {
	t.Parallel()

	remote := logRemote{fakeRemote: &fakeRemote{state: model.JobFailed}, log: "mutation-pr: applied=1 killed=0 lived=1 timed_out=0\n"}
	lookups := 0

	run, err := mutationGate(remote, nil, &lookups).Mutate(context.Background(), mutationRequest("cluster"))

	require.NoError(t, err)
	assert.True(t, run.Failed)
	assert.Equal(t, remote.log, run.Log)
}

func TestMutate_AJobThatDiedWentStaleOrRanPastItsLimitGivesANoteAndNoLog(t *testing.T) {
	t.Parallel()

	for _, state := range []model.JobState{model.JobDied, model.JobStale, model.JobTimedOut} {
		remote := logRemote{fakeRemote: &fakeRemote{state: state}, log: "unread"}
		lookups := 0

		run, err := mutationGate(remote, nil, &lookups).Mutate(context.Background(), mutationRequest("cluster"))

		require.NoError(t, err, state)
		assert.Empty(t, run.Log, state)
		assert.NotEmpty(t, run.Note, state)
		assert.NotContains(t, run.Note, "the gate", state)
	}
}

func TestMutate_AJobThatIsStillPendingHasNoVerdict(t *testing.T) {
	t.Parallel()

	lookups := 0

	_, err := mutationGate(logRemote{fakeRemote: &fakeRemote{state: model.JobPending}}, nil, &lookups).Mutate(context.Background(), mutationRequest("cluster"))

	require.ErrorIs(t, err, gate.ErrNoVerdict)
}

func TestMutate_AHostThatCannotReadALogHasNoVerdict(t *testing.T) {
	t.Parallel()

	lookups := 0

	_, err := mutationGate(&fakeRemote{state: model.JobSucceeded}, nil, &lookups).Mutate(context.Background(), mutationRequest("cluster"))

	require.ErrorIs(t, err, gate.ErrNoVerdict)
}

func TestMutate_ALogThatCannotBeReadIsAnError(t *testing.T) {
	t.Parallel()

	cause := errors.New("disk gone")
	lookups := 0

	_, err := mutationGate(logRemote{fakeRemote: &fakeRemote{state: model.JobSucceeded}, logErr: cause}, nil, &lookups).Mutate(context.Background(), mutationRequest("cluster"))

	require.ErrorIs(t, err, cause)
}

func TestMutate_ASubmitThatFailsAndAWorkRootThatFailsAreErrors(t *testing.T) {
	t.Parallel()

	submit := errors.New("scheduler down")
	root := errors.New("no work root")
	lookups := 0

	_, err := mutationGate(&fakeRemote{submitErr: submit}, nil, &lookups).Mutate(context.Background(), mutationRequest("cluster"))
	require.ErrorIs(t, err, submit)
	require.ErrorContains(t, err, "start the mutation run on cluster")

	_, err = mutationGate(&fakeRemote{rootErr: root}, nil, &lookups).Mutate(context.Background(), mutationRequest("cluster"))
	require.ErrorIs(t, err, root)
}

func TestMutate_AStoppedRunCancelsTheJobEvenThoughItsContextEnded(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	remote := &fakeRemote{stopOnContext: true}
	lookups := 0

	_, err := mutationGate(remote, nil, &lookups).Mutate(ctx, mutationRequest("cluster"))

	require.ErrorIs(t, err, context.Canceled)
	assert.Equal(t, []model.JobID{"job-7"}, remote.canceled)
	require.NoError(t, remote.cancelCtxErr, "the cancel runs on a context that the stop did not end")
}

func TestMutate_AWaitThatFailsWithTheRunStillLiveDoesNotCancelTheJob(t *testing.T) {
	t.Parallel()

	remote := &fakeRemote{waitErr: errors.New("link down")}
	lookups := 0

	_, err := mutationGate(remote, nil, &lookups).Mutate(context.Background(), mutationRequest("cluster"))

	require.ErrorContains(t, err, "wait for the mutation run on cluster")
	assert.Empty(t, remote.canceled)
}
