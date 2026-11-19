package flow

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/tobocop2/beehave/internal/agent"
)

// restartSteps is the order that starts a stopped item again: a resume replays the recorded stop, so the resume comes first, then the cancel, then the run.
func restartSteps(item string) string {
	return "run beehave resume " + item + ", then beehave cancel " + item + ", then beehave run " + item
}

func assertNamesTheRestartSteps(t *testing.T, err error) {
	t.Helper()

	require.Error(t, err)
	assert.Contains(t, err.Error(), "REFUSE")
	assert.Contains(t, err.Error(), restartSteps(testItem), "the text names resume, then cancel, then run")
	assert.Contains(t, err.Error(), "A plain beehave resume replays this stop", "the text says why a plain resume is not the instruction")
}

func TestRestartText_ACapStopOfALocalQueueOrResearchExampleNamesResumeThenCancelThenRun(t *testing.T) {
	t.Parallel()

	for _, guard := range capGuards {
		if guard.example == "swarm" {
			continue
		}

		t.Run(guard.step, func(t *testing.T) {
			t.Parallel()

			rig, _, file := guardRig(t, guard)

			_, err := rig.h.engine(t).RunFile(testItem, testProject, file)

			assertNamesTheRestartSteps(t, err)
		})
	}
}

func TestRestartText_ACappedPlanOfTheRemediationExampleNamesResumeThenCancelThenRun(t *testing.T) {
	t.Parallel()

	rig, file := capRig(t, func(int) string { return "approve" }, func(r AgentRequest) bool { return r.Model == "opus" }, agent.OutcomeTurnCap)

	_, err := runPipeline(rig.h.engine(t), file)

	assertNamesTheRestartSteps(t, err)
}

func TestRestartText_ACappedImplementOfTheRemediationExampleNamesResumeThenCancelThenRun(t *testing.T) {
	t.Parallel()

	rig, file := capRig(t, func(int) string { return "approve" }, func(r AgentRequest) bool { return r.Model == "sonnet" && r.Resume == "" }, agent.OutcomeTurnCap)

	_, err := runPipeline(rig.h.engine(t), file)

	assertNamesTheRestartSteps(t, err)
}

func TestRestartText_ACappedReviewOfTheReviewLoopNamesResumeThenCancelThenRun(t *testing.T) {
	t.Parallel()

	rig, file := capRig(t, func(int) string { return "changes" }, nthCall(3), agent.OutcomeBudgetCap)

	_, err := runPipeline(rig.h.engine(t), file)

	assertNamesTheRestartSteps(t, err)
}

func TestRestartText_ACappedSettleOfTheRemediationExampleNamesResumeThenCancelThenRun(t *testing.T) {
	t.Parallel()

	settle := 2 + len(capModels(2)) + 1
	rig, file := capRig(t, func(int) string { return "changes" }, nthCall(settle), agent.OutcomeTurnCap)
	rig.setRound("fix_rounds", oneFixRound)
	engine := rig.h.engine(t)
	done := pipelineInBackground(engine, file)

	waitOn(t, ownerAsks(t, rig.h))
	require.NoError(t, rig.h.client(t, testVersion).Answer(testItem, "park all"))
	outcome := receive(t, done)

	assertNamesTheRestartSteps(t, outcome.err)
}

// waitStopLine is the one stop that a resume does not replay: a wait that timed out asks again after the resume (the test research-desk-recover shows it).
const waitStopLine = "no answer about day"

// restartSites are the workflow files of the examples and of the library that the scan below reads.
var restartSites = []string{"../../examples", "../workflow/lib"}

func TestRestartText_NoStopTextInTheExamplesOrTheLibraryTellsTheOwnerToRunAPlainResume(t *testing.T) {
	t.Parallel()

	scanned := 0

	for _, root := range restartSites {
		require.NoError(t, filepath.WalkDir(root, func(path string, entry fs.DirEntry, err error) error {
			require.NoError(t, err)

			if entry.IsDir() || !strings.HasSuffix(path, ".star") {
				return nil
			}

			body, readErr := os.ReadFile(path) //nolint:gosec // the test reads the example files of the repository
			require.NoError(t, readErr)

			scanned++

			for number, line := range strings.Split(string(body), "\n") {
				if !strings.Contains(line, "REFUSE") || !strings.Contains(line, "beehave resume") || strings.Contains(line, waitStopLine) {
					continue
				}

				where := fmt.Sprintf("%s:%d", path, number+1)
				assert.Contains(t, line, "beehave cancel", where+": a stop text that names resume names cancel after it")
				assert.Less(t, strings.Index(line, "beehave resume"), strings.Index(line, "beehave cancel"), where+": resume comes before cancel")
				assert.Less(t, strings.Index(line, "beehave cancel"), strings.Index(line, "beehave run"), where+": cancel comes before run")
			}

			return nil
		}))
	}

	assert.Greater(t, scanned, 10, "the scan reads the workflow files")
}
