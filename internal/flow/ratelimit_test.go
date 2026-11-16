package flow

import (
	"encoding/json"
	"sync/atomic"
	"testing"
	"time"

	"github.com/dbos-inc/dbos-transact-golang/dbos"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/tobocop2/beehave/internal/agent"
)

// fixFlow is one fix lane.
const fixFlow = "def main(item):\n    agent(name = \"fix\", prompt = \"prompts/work.md\", model = \"m\")\n"

// limitReset is the time when the provider limit of the tests resets.
var limitReset = time.Date(2026, 11, 1, 5, 0, 0, 0, time.UTC)

// limited is a run that a rate limit stopped, as the runner reports it: the outcome limited, the limit, the session and the cost of the run.
func limited(reset time.Time, cost float64) agent.Result {
	return agent.Result{
		Outcome: agent.OutcomeLimited, ExitCode: 1, Session: "sess-limited", Note: "the provider refused more work at its rate limit five_hour",
		Limit: &agent.Limit{Kind: agent.LimitRate, Window: "five_hour", ResetsAt: reset}, Spend: agent.Spend{Cost: cost},
	}
}

// inTurn makes the fake agent end its runs with the results in order. The last result repeats.
func inTurn(h *harness, results ...agent.Result) {
	var calls atomic.Int32

	h.agents.result = func(AgentRequest) agent.Result {
		return results[min(int(calls.Add(1)), len(results))-1]
	}
}

// limitedHarness is a harness that runs one fix lane, with a wall clock that reads wall.
func limitedHarness(t *testing.T, wall time.Time) *harness {
	t.Helper()

	h := newHarness(t)
	h.flowIs(t, fixFlow)
	h.wall = func() time.Time { return wall }

	return h
}

// recordsOfTestRun reads the step records of the test run with their outputs.
func recordsOfTestRun(t *testing.T, engine *Engine) []dbos.StepInfo {
	t.Helper()

	steps, err := dbos.GetWorkflowSteps(engine.ctx, testRun, dbos.WithStepsLoadOutput(true))
	require.NoError(t, err)

	return steps
}

// sleepsIn counts the durable sleeps among the step records.
func sleepsIn(steps []dbos.StepInfo) int {
	count := 0

	for _, step := range steps {
		if step.StepName == sleepStep {
			count++
		}
	}

	return count
}

func TestRateLimit_AFixLaneThatHitsARateLimitWaitsThenPassesAndCountsOneLane(t *testing.T) {
	t.Parallel()

	h := limitedHarness(t, limitReset.Add(-30*time.Millisecond))
	inTurn(h, limited(limitReset, 0), exited)

	engine := h.engine(t)

	_, err := engine.Run(testItem, testProject)
	require.NoError(t, err)
	assert.Len(t, h.agents.seen(), 2, "the limited run, then the rerun after the reset")

	count, err := (&script{e: engine, ctx: engine.ctx, in: ItemInput{Item: testItem}}).countEarlier("fix")
	require.NoError(t, err)
	assert.Equal(t, 1, count, "the limited run and its rerun are one fix lane")
}

func TestRateLimit_ARestartDuringTheWaitSleepsTheRestAndStartsTheAgentOnceMore(t *testing.T) {
	t.Parallel()

	h := limitedHarness(t, limitReset.Add(-1500*time.Millisecond))
	inTurn(h, limited(limitReset, 0), exited)

	first := h.engine(t)
	done := runInBackground(first, testItem)

	waitForTheSleep(t, first)
	require.NoError(t, first.Shutdown())
	<-done
	require.Len(t, h.agents.seen(), 1, "the engine stopped in the wait")

	second := h.engine(t)
	client := h.client(t, testVersion)

	eventually(t, func() bool {
		steps, err := client.Steps(testRun)

		return err == nil && steps.State == runDone
	})

	assert.Len(t, h.agents.seen(), 2, "the replay reads the recorded phase and the limit, and starts the agent once after the reset")
	assert.Equal(t, 1, sleepsIn(recordsOfTestRun(t, second)), "the restart sleeps the rest of the one recorded sleep")
}

func TestRateLimit_TheWaitIsOneDurableSleepUntilTheRecordedReset(t *testing.T) {
	t.Parallel()

	h := limitedHarness(t, limitReset.Add(-30*time.Millisecond))
	inTurn(h, limited(limitReset, 0.5), exited)

	engine := h.engine(t)

	_, err := engine.Run(testItem, testProject)
	require.NoError(t, err)

	steps := recordsOfTestRun(t, engine)
	assert.Equal(t, 1, sleepsIn(steps))

	var record map[string]any

	for _, step := range steps {
		if step.StepName == "agent:fix#1/limit1.0" {
			text, ok := step.Output.(string)
			require.True(t, ok, "the limit record is JSON text")
			require.NoError(t, json.Unmarshal([]byte(text), &record))
		}
	}

	require.NotNil(t, record, "the limited run left a limit record")
	assert.Equal(t, limitReset.Format(time.RFC3339), record["wait_until"])
	assert.Equal(t, map[string]any{"kind": "rate", "window": "five_hour", "resets_at": limitReset.Format(time.RFC3339)}, record["limit"])
	assert.Equal(t, testLogin, record["login"])
	assert.Equal(t, "sess-limited", record["session"])
	assert.InDelta(t, 0.5, record[costField], 0)
}

func TestRateLimit_AResetInThePastWaitsTheLeastTimeThenRunsAgain(t *testing.T) {
	t.Parallel()

	h := limitedHarness(t, limitReset.Add(time.Hour))
	inTurn(h, limited(limitReset, 0), exited)

	engine := h.engine(t)

	_, err := engine.Run(testItem, testProject)

	require.NoError(t, err)
	assert.Len(t, h.agents.seen(), 2)
	assert.Equal(t, 1, sleepsIn(recordsOfTestRun(t, engine)), "one least wait before the rerun")
}

func TestRateLimit_ALimitThatNoReadingDatesFailsTheStepAndNamesTheLimit(t *testing.T) {
	t.Parallel()

	h := limitedHarness(t, limitReset)
	inTurn(h, limited(time.Time{}, 0))

	engine := h.engine(t)

	_, err := engine.Run(testItem, testProject)

	require.ErrorContains(t, err, "the agent step of item "+testItem+" stopped at the rate limit five_hour of login "+testLogin+", and the agent gives no reset time")
	require.ErrorContains(t, err, "Run beehave resume "+testItem+" when the limit is gone")
	assert.Len(t, h.agents.seen(), 1, "no rerun without a time to wait for")

	steps, err := h.client(t, testVersion).Steps(testRun)
	require.NoError(t, err)

	for _, line := range steps.Steps {
		assert.Nil(t, line.LimitWait, "the failed line of %s waits for nothing", line.Name)
	}
}

func TestRateLimit_TwoLimitsInARowWithAResetInThePastFailTheStep(t *testing.T) {
	t.Parallel()

	h := limitedHarness(t, limitReset.Add(time.Hour))
	inTurn(h, limited(limitReset, 0))

	_, err := h.engine(t).Run(testItem, testProject)

	require.ErrorContains(t, err, "the agent step of item "+testItem+" stopped at the rate limit five_hour of login "+testLogin+" twice in a row with a reset time that has passed")
	require.ErrorContains(t, err, "Run beehave resume "+testItem+" when the limit is gone")
	assert.Len(t, h.agents.seen(), 2, "one rerun after the first past reset, none after the second")
}

func TestRateLimit_AResumeAfterALimitWithNoResetTimeRunsTheAgentAgain(t *testing.T) {
	t.Parallel()

	h := limitedHarness(t, limitReset)
	inTurn(h, limited(time.Time{}, 0), exited)

	engine := h.engine(t)

	_, err := engine.Run(testItem, testProject)
	require.ErrorContains(t, err, "gives no reset time")

	_, err = resumeAndJoin(t, engine, h.client(t, testVersion))

	require.NoError(t, err, "the resume runs the agent again, and the limit is gone")
	assert.Len(t, h.agents.seen(), 2, "the limited run, then the run of the resume")
}

func TestRateLimit_AResumeAfterTwoPastResetsInARowRunsTheAgentAgain(t *testing.T) {
	t.Parallel()

	h := limitedHarness(t, limitReset.Add(time.Hour))
	inTurn(h, limited(limitReset, 0), limited(limitReset, 0), exited)

	engine := h.engine(t)

	_, err := engine.Run(testItem, testProject)
	require.ErrorContains(t, err, "twice in a row")

	_, err = resumeAndJoin(t, engine, h.client(t, testVersion))

	require.NoError(t, err, "the resume runs the agent again, and the limit is gone")
	assert.Len(t, h.agents.seen(), 3, "two limited runs, then the run of the resume")
}

func TestRateLimit_TheCostOfARunThatFailedAtALimitCountsOnce(t *testing.T) {
	t.Parallel()

	h := limitedHarness(t, limitReset)
	inTurn(h, limited(time.Time{}, 0.7))

	engine := h.engine(t)

	_, err := engine.Run(testItem, testProject)
	require.ErrorContains(t, err, "gives no reset time")

	client := h.client(t, testVersion)

	steps, err := client.Steps(testRun)
	require.NoError(t, err)
	assert.InDelta(t, 0.7, steps.Spent.Cost, 1e-9, "the status view")

	tree, err := runSpend(engine.ctx, testRun)
	require.NoError(t, err)
	assert.InDelta(t, 0.7, tree.Cost, 1e-9, "the sum that the tree cap reads")

	days, err := client.Days(DaysSince(time.Now(), 1))
	require.NoError(t, err)
	require.Len(t, days, 1)
	assert.InDelta(t, 0.7, days[0].Spent.Cost, 1e-9, "the per-day sum")
}

func TestRateLimit_AStoreThatRefusesTheLimitRecordFailsTheStepAndTheCostOfTheLimitedRunCounts(t *testing.T) {
	t.Parallel()

	h := limitedHarness(t, limitReset.Add(-30*time.Millisecond))
	inTurn(h, limited(limitReset, 0.7), exited)

	engine := h.engine(t)

	_, err := sqlOf(t, h).ExecContext(t.Context(), "CREATE TRIGGER refuse_limit_record BEFORE INSERT ON operation_outputs "+
		"WHEN NEW.function_name = 'agent:fix#1/limit1.0' BEGIN SELECT RAISE(ABORT, 'the store refused the record'); END")
	require.NoError(t, err)

	_, err = engine.Run(testItem, testProject)

	require.ErrorContains(t, err, "the store refused the record")
	assert.Len(t, h.agents.seen(), 1, "no rerun after a failed limit record")

	tree, err := runSpend(engine.ctx, testRun)
	require.NoError(t, err)
	assert.InDelta(t, 0.7, tree.Cost, 1e-9, "the cost of the limited run is recorded by the step of the failed run")
}

func TestRateLimit_TheSleepLastsUntilTheRecordedReset(t *testing.T) {
	t.Parallel()

	h := limitedHarness(t, limitReset.Add(-time.Hour))
	inTurn(h, limited(limitReset, 0), exited)

	engine := h.engine(t)
	done := runInBackground(engine, testItem)

	waitForTheSleep(t, engine)

	var sleep dbos.StepInfo

	for _, step := range recordsOfTestRun(t, engine) {
		if step.StepName == sleepStep {
			sleep = step
		}
	}

	// The library records the end of the sleep as the end of its step.
	assert.InDelta(t, time.Hour.Seconds(), sleep.CompletedAt.Sub(sleep.StartedAt).Seconds(), time.Minute.Seconds(), "the sleep ends at the reset, one hour after the wall clock")

	require.NoError(t, engine.Shutdown())
	<-done
}

func TestRateLimit_TheCostOfTheLimitedRunCountsInTheRunCapAndTheTreeSum(t *testing.T) {
	t.Parallel()

	for _, scope := range []string{scopeRun, scopeTree} {
		t.Run(scope, func(t *testing.T) {
			t.Parallel()

			rig := newAgentRig(t)
			rig.h.wall = func() time.Time { return limitReset.Add(-30 * time.Millisecond) }

			if scope == scopeRun {
				rig.h.opts.MaxRunBudget = 5
			} else {
				rig.h.opts.MaxTreeBudget = 5
			}

			inTurn(rig.h, limited(limitReset, 6), exited)

			stop := holdOnCap(t, rig, capFile(t, bodyFlow(`agent(model = "m", prompt = "prompts/a.md", name = "a")`+"\n")))

			require.NotNil(t, stop)
			assert.Equal(t, scope, stop.Scope)
			assert.InDelta(t, 6.0, stop.Spent, 0, "the cost of the limited run is in the sum")
			assert.Len(t, rig.h.agents.seen(), 1, "the rerun waits at the cap")
		})
	}
}

func TestRateLimit_ALimitedRunUsesNoOutputTry(t *testing.T) {
	t.Parallel()

	rig := newAgentRig(t)
	rig.h.wall = func() time.Time { return limitReset.Add(-30 * time.Millisecond) }

	var calls atomic.Int32

	rig.h.agents.result = func(request AgentRequest) agent.Result {
		if calls.Add(1) == 1 {
			return limited(limitReset, 0)
		}

		rig.put(outputPath(request.Prompt), `{"verdict": "ok"}`)

		return exited
	}

	flow := bodyFlow("a = " + agentCallText(`outputs = ["verdict"], output_tries = 1, `) + `command(run = ["show", a.verdict])` + "\n")

	_, err := rig.run(t, flow, "Write to {{ .output_file }}\n")

	require.NoError(t, err)
	assert.Equal(t, []string{"show ok"}, rig.h.commands.ran())
	assert.Len(t, rig.h.agents.seen(), 2)
}

func TestRateLimit_TheStatusOfAWaitingRunNamesTheLoginAndTheReset(t *testing.T) {
	t.Parallel()

	h := limitedHarness(t, limitReset.Add(-time.Hour))
	inTurn(h, limited(limitReset, 0), exited)

	engine := h.engine(t)
	client := h.client(t, testVersion)
	done := runInBackground(engine, testItem)

	var steps RunSteps

	eventually(t, func() bool {
		var err error

		steps, err = client.Steps(testRun)

		return err == nil && steps.LimitWait != nil
	})

	note := "waits for the rate limit five_hour of login " + testLogin + " until " + limitReset.Format(time.RFC3339)
	assert.Contains(t, steps.Notes(), note)
	assert.Equal(t, runRunning, steps.State, "the run stays open while it waits")

	var line StepLine

	for _, step := range steps.Steps {
		if step.Name == "agent:fix#1" {
			line = step
		}
	}

	require.NotNil(t, line.LimitWait, "the line of the waiting step holds the limit")
	assert.Equal(t, LimitWait{Limit: *limited(limitReset, 0).Limit, Login: testLogin, WaitUntil: limitReset}, *line.LimitWait)

	infos, err := client.Status()
	require.NoError(t, err)
	require.Len(t, infos, 1)
	assert.Contains(t, infos[0].Notes(), note, "the list of runs shows the wait too")

	require.NoError(t, engine.Shutdown())
	<-done
}

func TestRateLimit_TheLookAtTheCapsAfterALimitEndsItsWaitOnTheLine(t *testing.T) {
	t.Parallel()

	record, err := json.Marshal(limitRecord{LimitWait: LimitWait{Limit: *limited(limitReset, 0).Limit, Login: testLogin, WaitUntil: limitReset}, Spend: agent.Spend{Cost: 2}})
	require.NoError(t, err)

	var line StepLine

	require.True(t, line.decodeAgentSub("limit1.0", string(record)))
	require.NotNil(t, line.LimitWait)
	assert.InDelta(t, 2.0, line.Cost, 0)

	require.True(t, line.decodeAgentSub("budget1.1", "{}"))
	assert.Nil(t, line.LimitWait, "the rerun has started: the step no longer waits")
	assert.InDelta(t, 2.0, line.Cost, 0, "the cost of the limited run stays on the line")
}

func TestRateLimit_ALimitRecordThatCannotBeReadMarksTheCostUnknown(t *testing.T) {
	t.Parallel()

	var line StepLine

	assert.False(t, line.decodeAgentSub("limit1.0", "not json"))
	assert.True(t, line.Unknown, "a cost that cannot be read is never 0 for the caps")
	assert.True(t, line.Unreadable)
	assert.Nil(t, line.LimitWait)
}

func TestRateLimit_ALimitedRunWithNoLimitFailsThePhase(t *testing.T) {
	t.Parallel()

	err := phaseEnd(PhaseInput{Item: testItem}, PhaseOutput{Outcome: agent.OutcomeLimited, ExitCode: 1, Note: "no limit"}, limitReset)

	require.ErrorContains(t, err, "ended as limited (exit 1): no limit")
	require.NoError(t, phaseEnd(PhaseInput{Item: testItem}, PhaseOutput{Outcome: agent.OutcomeLimited, Limit: limited(limitReset, 0).Limit}, limitReset.Add(-time.Hour)))
}

func TestRateLimit_AStoredPhaseWithoutALimitDecodesAsBefore(t *testing.T) {
	t.Parallel()

	var stored PhaseOutput

	require.NoError(t, json.Unmarshal([]byte(`{"outcome":"exited","exit_code":0,"note":"","session":"s","cost_usd":1}`), &stored))
	assert.Nil(t, stored.Limit, "a phase that agent version 1 recorded has no limit")

	plain, err := json.Marshal(PhaseOutput{Outcome: agent.OutcomeExited})
	require.NoError(t, err)
	assert.NotContains(t, string(plain), `"limit"`, "a phase with no limit keeps the form of version 1")

	withLimit, err := json.Marshal(PhaseOutput{Outcome: agent.OutcomeLimited, Limit: limited(limitReset, 0).Limit})
	require.NoError(t, err)
	assert.Contains(t, string(withLimit), `"limit":{"kind":"rate","window":"five_hour","resets_at":"2026-11-01T05:00:00Z"}`)
}
