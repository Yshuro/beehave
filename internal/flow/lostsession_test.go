package flow

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/dbos-inc/dbos-transact-golang/dbos"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/tobocop2/beehave/internal/agent"
)

// nameSession makes the fake agent name the session id before it counts as in flight. An empty id names none.
func nameSession(harness *harness, session string) {
	harness.agents.mu.Lock()
	defer harness.agents.mu.Unlock()

	harness.agents.named = session
}

func TestPhase_TheSessionIdIsSavedAsAnEventWhileTheAgentRuns(t *testing.T) {
	t.Parallel()

	harness := newHarness(t)
	nameSession(harness, "s-lost")
	harness.agents.gate = make(chan struct{})
	engine := harness.engine(t)
	client := harness.client(t, testVersion)

	done := runInBackground(engine, testItem)
	eventually(t, func() bool { return harness.agents.running() == 1 })

	phases, err := dbos.ListWorkflows(client.flow, dbos.WithFilterName(phaseName))
	require.NoError(t, err)
	require.Len(t, phases, 1)

	session, err := dbos.GetEvent[string](client.flow, phases[0].ID, sessionEvent, 0)
	require.NoError(t, err)
	assert.Equal(t, "s-lost", session, "the phase holds the session id while the agent still runs")

	close(harness.agents.gate)
	require.NoError(t, (<-done).err)
}

func TestPhase_ALostStepThatNamedASessionResumesItOnTheReplay(t *testing.T) {
	t.Parallel()

	harness := newHarness(t)
	nameSession(harness, "s-lost")
	loseAgent(t, harness, 33*time.Minute)
	nameSession(harness, "")

	_, err := harness.engine(t).Run(testItem, testProject)
	require.NoError(t, err)

	requests := harness.agents.seen()
	require.Len(t, requests, 2)
	assert.Nil(t, requests[0].Lost, "a first start follows no loss")
	require.NotNil(t, requests[1].Lost)
	assert.Equal(t, "s-lost", requests[1].Lost.Session)
	assert.Contains(t, requests[1].Lost.Continue, "after 33 minutes")
	assert.Contains(t, requests[1].Lost.Note, "This step ran before and was lost after 33 minutes.")
	assert.Equal(t, "Work on the item.\n", requests[1].Prompt, "the note goes with the lost run, not into the prompt")
}

func TestPhase_ALostStepThatNamedNoSessionGetsTheNoteAndTheDiffSummary(t *testing.T) {
	t.Parallel()

	harness := newHarness(t)
	loseAgent(t, harness, 33*time.Minute)
	// The lost agent committed one file before the engine stopped.
	commitIn(t, harness.trees.dir, "done.txt")

	_, err := harness.engine(t).Run(testItem, testProject)
	require.NoError(t, err)

	requests := harness.agents.seen()
	require.Len(t, requests, 2)
	assert.Nil(t, requests[1].Lost, "a loss before any session id has no session to resume")
	assert.Contains(t, requests[1].Prompt, "This step ran before and was lost after 33 minutes.")
	assert.Contains(t, requests[1].Prompt, "done.txt")
	assert.Contains(t, requests[1].Prompt, "1 file changed")
	assert.Contains(t, requests[1].Prompt, "git status")
}

func TestPhase_AFailedStepThatResumesStartsAFreshSession(t *testing.T) {
	t.Parallel()

	harness := newHarness(t)
	nameSession(harness, "s-failed")
	harness.agents.result = func(AgentRequest) agent.Result {
		return agent.Result{Outcome: agent.OutcomeExited, ExitCode: 3, Note: "bad exit"}
	}
	engine := harness.engine(t)
	client := harness.client(t, testVersion)

	_, err := engine.Run(testItem, testProject)
	require.Error(t, err)

	harness.agents.mu.Lock()
	harness.agents.result, harness.agents.named = nil, ""
	harness.agents.mu.Unlock()

	_, err = resumeAndJoin(t, engine, client)
	require.NoError(t, err)

	requests := harness.agents.seen()
	require.Len(t, requests, 2)
	assert.Nil(t, requests[1].Lost, "a failed step was not lost, so its session is not resumed")
	assert.Empty(t, requests[1].Resume)
	assert.Equal(t, "Work on the item.\n", requests[1].Prompt)
}

func TestClient_TheLostRecordHoldsTheSessionAndTheDiffSummary(t *testing.T) {
	t.Parallel()

	harness := newHarness(t)
	nameSession(harness, "s-lost")
	loseAgent(t, harness, 33*time.Minute)
	nameSession(harness, "")
	commitIn(t, harness.trees.dir, "done.txt")

	_, err := harness.engine(t).Run(testItem, testProject)
	require.NoError(t, err)

	client := harness.client(t, testVersion)
	lost := lostSteps(t, client)
	require.Len(t, lost, 1)

	phases, err := dbos.ListWorkflows(client.flow, dbos.WithFilterName(phaseName))
	require.NoError(t, err)

	var record LostStep

	for _, phase := range phases {
		steps, err := dbos.GetWorkflowSteps(client.flow, phase.ID, dbos.WithStepsLoadOutput(true))
		require.NoError(t, err)

		for _, step := range steps {
			if raw, isText := step.Output.(string); isText && step.StepName == lostStepPrefix+"1" {
				require.NoError(t, json.Unmarshal([]byte(raw), &record))
			}
		}
	}

	assert.Equal(t, "s-lost", record.Session)
	assert.Contains(t, record.DiffStat, "done.txt")
	assert.Contains(t, record.DiffStat, "1 file changed")
	assert.Contains(t, record.Note, record.DiffStat, "the note quotes the diff summary")
}

func TestLostDiff_ReportsAFolderThatCannotBeFoundAndADiffThatGitRefuses(t *testing.T) {
	t.Parallel()

	harness := newHarness(t)
	engine, err := NewEngine(t.Context(), harness.opts, Deps{Trees: &fakeTrees{err: errFake}})
	require.NoError(t, err)

	t.Cleanup(func() { assert.NoError(t, engine.Shutdown()) })

	summary, err := engine.lostDiff(t.Context(), PhaseInput{Item: testItem})
	require.NoError(t, err, "a folder with no start commit has no summary")
	assert.Empty(t, summary)

	_, err = engine.lostDiff(t.Context(), PhaseInput{Item: testItem, StartHead: "abc"})
	require.ErrorContains(t, err, "find the folder of item "+testItem)

	engine.deps.Trees = &fakeTrees{dir: t.TempDir()}
	_, err = engine.lostDiff(t.Context(), PhaseInput{Item: testItem, StartHead: "abc"})
	require.ErrorContains(t, err, "read the diff summary of the item folder")
}

func TestLoss_ALossStepThatCannotReadTheSessionOrTheDiffFails(t *testing.T) {
	t.Parallel()

	harness := newHarness(t)
	engine, err := NewEngine(t.Context(), harness.opts, Deps{Trees: &fakeTrees{err: errFake}})
	require.NoError(t, err)
	require.NoError(t, engine.Launch())

	in := PhaseInput{Item: testItem, StartHead: "abc"}

	_, err = engine.loss(in, "phase-none", time.Now())(t.Context())
	require.ErrorContains(t, err, "find the folder of item "+testItem, "a phase that saved no session reads the diff next")

	require.NoError(t, engine.Shutdown())

	_, err = engine.loss(in, "phase-none", time.Now())(t.Context())
	require.ErrorContains(t, err, "read the session of the lost agent step")
}

func TestSaveSession_AWriteThatFailsIsLoggedAndTheRunGoesOn(t *testing.T) {
	t.Parallel()

	harness := newHarness(t)
	engine := harness.engine(t)

	// A context that is no step of the library cannot hold an event.
	engine.saveSession(t.Context(), PhaseInput{Item: testItem})("s-1")

	log, err := os.ReadFile(filepath.Join(harness.opts.StateDir, logFileName))
	require.NoError(t, err)
	assert.Contains(t, string(log), "cannot save the session of the agent")
	assert.Contains(t, string(log), "session=s-1")
}

func TestPhaseOutput_RecordsWhetherTheRunResumedALostSession(t *testing.T) {
	t.Parallel()

	assert.True(t, phaseOutput(agent.Result{Resumed: true}).Resumed)
	assert.False(t, phaseOutput(agent.Result{}).Resumed)
}

func TestLostNote_SaysThatNoTrackedFileChangedForAnEmptySummary(t *testing.T) {
	t.Parallel()

	lost := LostStep{Started: time.Unix(0, 0), Lost: time.Unix(0, 0).Add(5 * time.Minute)}

	assert.Contains(t, lost.note(), "The engine found no change to tracked files since the step started.")
	assert.Contains(t, lost.continued(), "after 5 minutes")

	lost.DiffStat = " a.txt | 1 +\n 1 file changed, 1 insertion(+)"
	assert.Contains(t, lost.note(), "The engine read this summary of the changes since the step started:\n\n```\n"+lost.DiffStat+"\n```\n\n")
	assert.Contains(t, LostStep{Started: time.Unix(100, 0)}.continued(), "after an unknown time")
}
