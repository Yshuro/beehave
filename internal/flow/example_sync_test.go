package flow

import (
	"context"
	"encoding/json"
	"os"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/tobocop2/beehave/internal/agent"
)

// These tests run the sync step of the round: the round merges a moved trunk into the round branch before the queue audit of a pass.

// syncReviewMarker is a phrase of the prompt of the sync review, so the fake agents can tell that review from the others.
const syncReviewMarker = "What the review of a sync must do"

// syncResolveMarker is a phrase of the prompt of the agent that resolves a sync conflict.
const syncResolveMarker = "What the sync step must do"

// syncBranch is the sync branch that the fake tool names.
const syncBranch = "sync/main/abcd1234"

// syncScript is the fake `remedy sync` of a round: each check call takes the next line, and the last line repeats. land is the line and the exit code of `remedy sync land`.
type syncScript struct {
	lines  []string
	land   CommandOutput
	calls  int
	head   string // the head of the sync branch that a synced line names
	tip    string // the round base tip that a synced line names
	review string // the verdict of the sync review
	rc     int    // the exit code of each check call
}

// syncedLine is the line of a clean sync that the fake tool prints.
func (s *syncScript) syncedLine() string {
	return "state=synced branch=" + syncBranch + " pr=7 head=" + s.head + " base_tip=" + s.tip
}

// isSync reports whether the request is a call of the sync tool, and whether it is the landing.
func isSync(request CommandRequest) (bool, bool) {
	if len(request.Run) < 2 || request.Run[0] != "remedy" || request.Run[1] != "sync" {
		return false, false
	}

	return true, len(request.Run) > 2 && request.Run[2] == "land"
}

// syncGate is the gate of the round rig that records the base of each gate of the round itself and can make that gate red.
type syncGate struct {
	gate

	mu    sync.Mutex
	bases []string
	red   bool
}

func (g *syncGate) Submit(ctx context.Context, project, item, host, base string) (GateJob, error) {
	if item == testItem {
		g.mu.Lock()
		g.bases = append(g.bases, base)
		g.mu.Unlock()
	}

	return g.gate.Submit(ctx, project, item, host, base)
}

func (g *syncGate) Run(ctx context.Context, project, item, host string, job GateJob) (GateResult, error) {
	result, err := g.gate.Run(ctx, project, item, host, job)
	if item == testItem && g.red {
		result.Passed = false
		result.Steps = []GateStep{{Name: "command", Status: failStatus}}
	}

	return result, err
}

// roundBases returns the base of each gate of the round itself, in order.
func (g *syncGate) roundBases() []string {
	g.mu.Lock()
	defer g.mu.Unlock()

	return slices.Clone(g.bases)
}

// withSync makes the command fake of the rig answer the sync tool with the script, and run a checkout of the round for real in the folder of the round.
func withSync(t *testing.T, rig *roundRig, script *syncScript) {
	t.Helper()

	script.head = rig.heads["3"]
	script.tip = rig.kids.base
	if script.land == (CommandOutput{}) {
		script.land = CommandOutput{Stdout: "state=landed pr=7 head=" + script.head + "\n"}
	}

	if script.review == "" {
		script.review = "approve"
	}

	previous := rig.h.commands.reply
	rig.h.commands.reply = func(request CommandRequest) (CommandOutput, error) {
		if call, land := isSync(request); call {
			return script.answer(land), nil
		}

		if len(request.Run) > 2 && request.Run[0] == "git" && request.Run[1] == "checkout" {
			return checkoutInRound(t, rig, request.Run[1:]), nil
		}

		return previous(request)
	}

	inner := rig.h.agents.result
	rig.h.agents.result = func(request AgentRequest) agent.Result {
		if strings.Contains(request.Prompt, syncReviewMarker) {
			require.NoError(t, os.WriteFile(request.OutputFile, []byte(`{"verdict":"`+script.review+`"}`), 0o600))
		}

		return inner(request)
	}
}

// answer is the output of one call of the sync tool.
func (s *syncScript) answer(land bool) CommandOutput {
	if land {
		return s.land
	}

	line := s.lines[min(s.calls, len(s.lines)-1)]
	s.calls++

	if line == "synced" {
		line = s.syncedLine()
	}

	return CommandOutput{Stdout: line + "\n", RC: s.rc}
}

// checkoutInRound runs git with the words in the folder of the round, as the command step of the round does.
func checkoutInRound(t *testing.T, rig *roundRig, words []string) CommandOutput {
	t.Helper()

	dir, err := rig.h.trees.per(t.Context(), testItem)
	require.NoError(t, err)

	result := repoGit{binary: rig.repo.binary, dir: dir, home: rig.repo.home}.Run(t.Context(), words)

	return CommandOutput{Stdout: result.Stdout, RC: result.ExitCode}
}

// syncEngine is the engine of the round rig with the gate that records the gates of the round.
func syncEngine(t *testing.T, rig *roundRig) (*Engine, *syncGate) {
	t.Helper()

	var recorder *syncGate

	engine := rig.engineWith(t, func(deps *Deps) {
		recorder = &syncGate{gate: deps.Gate}
		deps.Gate = recorder
	})

	return engine, recorder
}

// runSync runs the round to its first wait, as run does, on the engine of syncEngine. red makes the gate of the round red.
func runSync(t *testing.T, rig *roundRig, red bool) (ItemResult, *syncGate, error) {
	t.Helper()

	engine, recorder := syncEngine(t, rig)
	recorder.red = red
	rig.last = engine

	result, err := rig.runOn(t, engine)

	return result, recorder, err
}

// at returns the place of the first step whose name starts with the prefix, or -1.
func at(names []string, prefix string) int {
	return slices.IndexFunc(names, func(name string) bool { return strings.HasPrefix(name, prefix) })
}

// syncLands returns the landing calls of the sync tool, words joined by a space.
func syncLands(rig *roundRig) []string {
	var lands []string

	for _, line := range commandsNamed(rig, "remedy") {
		if strings.HasPrefix(line, "remedy sync land") {
			lands = append(lands, line)
		}
	}

	return lands
}

func TestRoundSync_AMovedTrunkLandsThroughTheSyncPullRequestWhoseGateRunsOnTheMergedHeadBeforeTheNextItemStarts(t *testing.T) {
	t.Parallel()

	rig := newRoundRig(t, "1")
	script := &syncScript{lines: []string{"synced", "state=current"}}
	withSync(t, rig, script)

	_, recorder, err := runSync(t, rig, false)
	require.NoError(t, err)

	names := stepNames(t, rig.last)
	order := []string{"command:sync-1#", "gate:sync-gate-1#", "agent:sync-review-1#", "command:sync-land-1#", "agent:queue-audit-1#", "spawn:round-spawn-1#"}

	for i := 1; i < len(order); i++ {
		require.GreaterOrEqual(t, at(names, order[i-1]), 0, "the step %s ran: %v", order[i-1], names)
		assert.Less(t, at(names, order[i-1]), at(names, order[i]), "%s comes before %s", order[i-1], order[i])
	}

	assert.Equal(t, []string{"remedy sync --base main --trunk main"}, commandsNamed(rig, "remedy sync --base")[:1])
	assert.Equal(t, []string{"remedy sync land --pr 7 --head " + script.head + " --base-tip " + script.tip}, syncLands(rig))
	require.NotEmpty(t, recorder.roundBases())
	assert.Empty(t, recorder.roundBases()[0], "the gate of the sync head runs the gate command only, with no base")
}

func TestRoundSync_ASyncedLineLandsWhateverTheOrderOfItsFieldsAndKeepsAnEqualSignInAValue(t *testing.T) {
	t.Parallel()

	rig := newRoundRig(t, "")
	script := &syncScript{}
	withSync(t, rig, script)
	script.lines = []string{"base_tip=" + script.tip + " head=" + script.head + " note pr=7 branch=sync/a=b state=synced", "state=current"}

	_, _, err := runSync(t, rig, false)
	require.NoError(t, err)

	assert.Equal(t, []string{"git fetch origin sync/a=b"}, commandsNamed(rig, "git fetch origin"))
	assert.Equal(t, []string{"remedy sync land --pr 7 --head " + script.head + " --base-tip " + script.tip}, syncLands(rig))
}

func TestRoundSync_AConflictThatTheAgentDoesNotResolveStopsTheRoundInRoundWaitWithTheReason(t *testing.T) {
	t.Parallel()

	rig := newRoundRig(t, "")
	rig.setRound("wait_hours", json.Number("1"))
	withSync(t, rig, &syncScript{lines: []string{"state=conflict branch=" + syncBranch + " source=main base_tip=abc"}})

	engine := waitOverEngine(t, rig)

	in, err := pinnedInput(testItem, testProject, rig.file)
	require.NoError(t, err)

	in.Entry = "round"
	result, err := engine.run(in)

	require.NoError(t, err)
	assert.Nil(t, result.Stopped)

	var value struct {
		State  string `json:"state"`
		Reason string `json:"reason"`
	}

	require.NoError(t, json.Unmarshal(result.Value, &value))
	assert.Equal(t, "waiting", value.State)
	assert.Equal(t, "sync-conflict", value.Reason)

	joined := strings.Join(stepNames(t, engine), " ")
	assert.Contains(t, joined, "agent:sync-resolve-1#", "one agent step resolves the conflict")
	assert.Contains(t, joined, "command:sync-again-1#", "the round calls the tool again after the agent")
	assert.Contains(t, joined, "wait:round-wait#")
	assert.NotContains(t, joined, "agent:queue-audit-1#", "no item builds on the stale base")
	assert.NotContains(t, joined, "queue:round-queue-1#")
	assert.NotContains(t, joined, "gate:")

	resolve := slices.IndexFunc(rig.h.agents.requests, func(request AgentRequest) bool { return strings.Contains(request.Prompt, syncResolveMarker) })
	require.GreaterOrEqual(t, resolve, 0, "the resolve agent ran with the sync prompt")
	assert.Contains(t, rig.h.agents.requests[resolve].Prompt, syncBranch)
	assert.Contains(t, rig.h.agents.requests[resolve].Prompt, "origin/main", "the agent merges the source that the line names")
}

func TestRoundSync_ATrunkMergeThatAddsNoTestLandsItsSyncBecauseTheSyncGateRunsNoNewTestsCheck(t *testing.T) {
	t.Parallel()

	rig := newRoundRig(t, "")
	script := &syncScript{lines: []string{"synced", "state=current"}}
	withSync(t, rig, script)

	_, recorder, err := runSync(t, rig, false)
	require.NoError(t, err)

	assert.Equal(t, []string{""}, recorder.roundBases(), "one gate of the round, with no base: the gate command only, no new-tests check")
	assert.Len(t, syncLands(rig), 1, "the sync lands")
	require.Len(t, rig.questions, 1)
	assert.NotContains(t, rig.questions[0], "sync-")
}

func TestRoundSync_ARefusedSyncWaitsWithTheReasonAndAfterTheOwnerLandsItTheRoundFindsNoTrunkMoveAndGoesOn(t *testing.T) {
	t.Parallel()

	rig := newRoundRig(t, "")
	rig.answers = []string{"CONTINUE", "STOP"}
	withSync(t, rig, &syncScript{lines: []string{"state=refused reason=sync-refused", "state=current"}})

	result, _, err := runSync(t, rig, false)

	require.NoError(t, err)
	assert.Nil(t, result.Stopped, "a refusal is no failed step")
	require.Len(t, rig.questions, 2)
	assert.Contains(t, rig.questions[0], "sync-refused")
	assert.NotContains(t, rig.questions[1], "sync-")

	joined := strings.Join(stepNames(t, rig.last), " ")
	assert.NotContains(t, joined, "agent:queue-audit-1#", "the refused pass builds nothing")
	assert.Contains(t, joined, "command:sync-2#", "the next pass checks the trunk again")
	assert.Contains(t, joined, "agent:queue-audit-2#", "the trunk is current, so the round goes on")
	assert.Empty(t, syncLands(rig))
}

func TestRoundSync_ARedGateOnTheSyncHeadStopsTheRoundWithTheReasonAndLandsNothing(t *testing.T) {
	t.Parallel()

	rig := newRoundRig(t, "")
	withSync(t, rig, &syncScript{lines: []string{"synced"}})

	_, _, err := runSync(t, rig, true)

	require.NoError(t, err)
	require.Len(t, rig.questions, 1)
	assert.Contains(t, rig.questions[0], "sync-red")
	assert.Empty(t, syncLands(rig))

	joined := strings.Join(stepNames(t, rig.last), " ")
	assert.NotContains(t, joined, "agent:sync-review-1#", "a red gate gets no review")
	assert.NotContains(t, joined, "agent:queue-audit-1#")
}

func TestRoundSync_AReviewThatDoesNotApproveTheSyncStopsTheRoundWithTheReasonAndLandsNothing(t *testing.T) {
	t.Parallel()

	rig := newRoundRig(t, "")
	withSync(t, rig, &syncScript{lines: []string{"synced"}, review: "changes"})

	_, _, err := runSync(t, rig, false)

	require.NoError(t, err)
	require.Len(t, rig.questions, 1)
	assert.Contains(t, rig.questions[0], "sync-review")
	assert.Empty(t, syncLands(rig))
	assert.NotContains(t, strings.Join(stepNames(t, rig.last), " "), "agent:queue-audit-1#")
}

func TestRoundSync_ALandingOnAMovedBaseGoesOnAndTheNextPassSyncsAgain(t *testing.T) {
	t.Parallel()

	for _, state := range []string{"stale", "head-moved"} {
		t.Run(state, func(t *testing.T) {
			t.Parallel()

			rig := newRoundRig(t, "")
			rig.answers = []string{"CONTINUE", "STOP"}
			withSync(t, rig, &syncScript{lines: []string{"synced"}, land: CommandOutput{RC: 2, Stdout: "state=" + state + "\n"}})

			result, _, err := runSync(t, rig, false)

			require.NoError(t, err)
			assert.Nil(t, result.Stopped)

			joined := strings.Join(stepNames(t, rig.last), " ")
			assert.Contains(t, joined, "agent:queue-audit-1#", "the pass goes on")
			assert.Contains(t, joined, "command:sync-land-2#", "the next pass syncs and lands again")
		})
	}
}

func TestRoundSync_ALineOrAnExitCodeTheRoundDoesNotKnowStopsTheRun(t *testing.T) {
	t.Parallel()

	cases := map[string]struct {
		script *syncScript
		want   string
	}{
		"unknown_state":         {&syncScript{lines: []string{"state=odd"}}, "the state \"odd\", which the round does not know"},
		"landing_with_no_state": {&syncScript{lines: []string{"synced"}, land: CommandOutput{RC: 2, Stdout: "REFUSE: the forge refused the merge\n"}}, "the landing of the sync pull request 7"},
		"synced_with_no_head":   {&syncScript{lines: []string{"state=synced branch=" + syncBranch + " pr=7"}}, "the synced line of sync-1 has no head, base_tip"},
		"conflict_no_source":    {&syncScript{lines: []string{"state=conflict branch=" + syncBranch}}, "the conflict line of sync-1 has no source"},
		"check_exit_one_empty":  {&syncScript{lines: []string{""}, rc: 1}, "the check sync-1 ended with the exit code 1"},
		"check_not_found":       {&syncScript{lines: []string{""}, rc: 127}, "the check sync-1 ended with the exit code 127"},
		"head_not_in_the_clone": {&syncScript{lines: []string{"state=synced branch=" + syncBranch + " pr=7 head=0123456789abcdef0123456789abcdef01234567 base_tip=abc"}}, "the step sync-checkout-1 could not put the folder"},
	}

	for name, c := range cases {
		script, want := c.script, c.want
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			rig := newRoundRig(t, "")
			withSync(t, rig, script)

			_, _, err := runSync(t, rig, false)

			require.Error(t, err)
			assert.Contains(t, err.Error(), want)
			assert.Empty(t, rig.questions, "the run stops before any wait")
		})
	}
}

func TestRoundSync_AnExitTwoOfTheCheckStopsTheRoundWithTheLineOfTheTool(t *testing.T) {
	t.Parallel()

	rig := newRoundRig(t, "")
	previous := rig.h.commands.reply
	rig.h.commands.reply = func(request CommandRequest) (CommandOutput, error) {
		if call, _ := isSync(request); call {
			return CommandOutput{RC: 2, Stdout: "REFUSE: the forge answered 502"}, nil
		}

		return previous(request)
	}

	result, err := rig.run(t)

	require.NoError(t, err)
	require.NotNil(t, result.Stopped)
	assert.Equal(t, 2, result.Stopped.RC)
	assert.Contains(t, result.Stopped.Output, "502")
}
