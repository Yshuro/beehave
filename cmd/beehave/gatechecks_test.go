package main

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/tobocop2/beehave/internal/flow"
	"github.com/tobocop2/beehave/internal/host"
	"github.com/tobocop2/beehave/internal/model"
)

// loggedHost is a host whose job log holds the lines of a new-tests check.
type loggedHost struct {
	fakeHost

	log string
}

func (l *loggedHost) Log(context.Context, model.JobID) (string, error) { return l.log, nil }

func TestProjectGate_ACandidateGateGivesTheChecksOfItsLogToTheEngine(t *testing.T) {
	t.Parallel()

	hosted := &loggedHost{
		fakeHost: fakeHost{state: model.JobSucceeded},
		log: "beehave-gate-step secret-scan rc=0\n" +
			"beehave-new-test pkg TestA\n" +
			"beehave-new-test-failed skipped pkg TestB\n" +
			"beehave-new-tests-end new=2 failed=1\n",
	}
	gate := projectGate{
		projects: clusterProject(),
		programs: programs{
			trees: treeAt("/tree"),
			local: gateRunner(&gateCalls{}, model.CommandResult{Stdout: "abc123"}, model.CommandResult{}),
			hosts: func(string, string) (host.Host, error) { return hosted, nil },
		},
		forge: forgeAt("https://example.test/repo.git"),
	}

	job, err := gate.Submit(t.Context(), "p", "1", "", "main")
	require.NoError(t, err)

	got, err := gate.Run(t.Context(), "p", "1", "", job)

	require.NoError(t, err)

	zero := 0

	require.NotNil(t, got.Checks)
	assert.Equal(t, flow.GateChecks{
		NewTests: []string{"pkg TestA"}, Failed: []string{"skipped pkg TestB"}, New: 2, FailedCount: 1, Secrets: &zero, Complete: true,
	}, *got.Checks)
}
