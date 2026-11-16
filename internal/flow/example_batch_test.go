package flow

import (
	"context"
	"encoding/json"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/tobocop2/beehave/internal/batch"
	"github.com/tobocop2/beehave/internal/merge"
	"github.com/tobocop2/beehave/internal/preflight"
)

// remoteBranches is a preflight reader over the branches on the remote: the head of each branch, and no entry for a branch that is gone.
// It gives the stops that the real check gives for a branch that is gone and for a head that is not the head the call expects.
type remoteBranches struct {
	mu    sync.Mutex
	heads map[string]string
	asked []string

	// before runs at the start of each call, with no lock held. The round rig puts its barrier here: the preflight holds no queue slot.
	before func(ctx context.Context, item string)
}

func (r *remoteBranches) Preflight(ctx context.Context, _, item string, req preflight.Request, _ string) (preflight.Result, error) {
	if r.before != nil {
		r.before(ctx, item)
	}

	r.mu.Lock()
	defer r.mu.Unlock()

	r.asked = append(r.asked, item)

	head, present := r.heads[item]

	switch {
	case !present:
		return preflight.Result{Stop: preflight.BranchGone, Lines: []string{"STOP: the head branch of item " + item + " no longer exists"}}, nil
	case req.ExpectHead != "" && req.ExpectHead != head:
		return preflight.Result{Stop: preflight.HeadMoved, Lines: []string{"STOP: head moved to " + head}}, nil
	}

	return preflight.Result{Stop: preflight.Unchanged}, nil
}

// heldOf returns the items that the run put on hold.
func heldOf(w *fakeWriter) []string {
	w.mu.Lock()
	defer w.mu.Unlock()

	var held []string

	for _, status := range w.statuses {
		if strings.HasSuffix(status, "|hold") {
			held = append(held, status)
		}
	}

	return held
}

// notesWithKey returns the comments of the run that carry the key.
func notesWithKey(w *fakeWriter, key string) []string {
	w.mu.Lock()
	defer w.mu.Unlock()

	var notes []string

	for _, note := range w.notes {
		// The key of a comment is scoped to the run, so the text tells a merge record from a rejection.
		if strings.Contains(note, "Merged into") == (key == "merged") {
			notes = append(notes, note)
		}
	}

	return notes
}

// gitLines returns the git commands that the run made, in order.
func gitLines(rig *roundRig) []string {
	var lines []string

	for _, line := range rig.h.commands.ran() {
		if strings.HasPrefix(line, "git fetch") || strings.HasPrefix(line, "git diff") {
			lines = append(lines, line)
		}
	}

	return lines
}

func TestExampleBatch_TwoApprovedItemsLandInOneBatchWithOneGateRun(t *testing.T) {
	t.Parallel()

	rig := newRoundRig(t, "1 3")

	_, err := rig.run(t)

	require.NoError(t, err)
	assert.ElementsMatch(t, []string{"1", "3"}, rig.merger.landed(), "the two children end together, so the batch has no fixed order")
	assert.Len(t, rig.gate.ran(), 1, "two green members take one gate run")
	assert.Equal(t, "README\none.txt\nthree.txt", rig.mainFiles(t))

	for _, given := range rig.merger.given {
		assert.Equal(t, "hold", given.Hold, "the guards of the merge of one item apply")
		assert.Equal(t, []string{shipOwner}, given.Logins)
		assert.Equal(t, []string{shipMail}, given.Emails)
		assert.Len(t, given.ForbidText, 4)
		assert.True(t, given.Approved, "each member carries the approval of the review of its child run")
	}
}

func TestExampleBatch_AnItemThatAGuardRefusesIsHeldAndTheOtherItemLands(t *testing.T) {
	t.Parallel()

	rig := newRoundRig(t, "1 3")
	rig.merger.script["1"] = []merge.Result{{Stop: merge.Refused, Lines: []string{"REFUSE: has the label hold"}, Refusal: []string{"REFUSE: has the label hold"}}}

	_, err := rig.run(t)

	require.NoError(t, err)
	assert.Equal(t, "README\nthree.txt", rig.mainFiles(t), "item 3 lands and item 1 does not")
	assert.Equal(t, []string{testProject + "|1|hold"}, heldOf(rig.writer))
	require.Len(t, notesWithKey(rig.writer, "rejected"), 1)
	assert.Contains(t, notesWithKey(rig.writer, "rejected")[0], "REFUSE: has the label hold")
}

func TestExampleBatch_APushAfterTheReviewRefusesOnlyThatItem(t *testing.T) {
	t.Parallel()

	rig := newRoundRig(t, "1 3")
	rig.pushAfterReviews("1", strings.Repeat("e", 40)) // somebody pushed to the branch of item 1 after the review approved it

	_, err := rig.run(t)

	require.NoError(t, err)
	assert.Equal(t, []string{"3"}, rig.merger.landed(), "the pushed item never reaches the merge")
	assert.Equal(t, "README\nthree.txt", rig.mainFiles(t))
	assert.Equal(t, []string{testProject + "|1|hold"}, heldOf(rig.writer))
	require.Len(t, notesWithKey(rig.writer, "rejected"), 1)
	assert.Contains(t, notesWithKey(rig.writer, "rejected")[0], "head moved")
}

func TestExampleBatch_AMemberLandsAtTheHeadTheReviewApprovedAndNotTheHeadOnTheRemote(t *testing.T) {
	t.Parallel()

	rig := newRoundRig(t, "1 3")

	_, err := rig.run(t)

	require.NoError(t, err)
	assert.Equal(t, []string{rig.heads["1"], rig.heads["3"]}, rig.merger.pinned(), "each member merges at its pin")
}

func TestExampleBatch_AMissingBranchIsReportedAndTheOtherItemLands(t *testing.T) {
	t.Parallel()

	rig := newRoundRig(t, "1 3")
	rig.pushAfterReviews("1", "") // the branch of item 1 is deleted after the review approved it

	_, err := rig.run(t)

	require.NoError(t, err)
	assert.Equal(t, []string{"3"}, rig.merger.landed())
	assert.Equal(t, []string{testProject + "|1|hold"}, heldOf(rig.writer))
	require.Len(t, notesWithKey(rig.writer, "rejected"), 1)
	assert.Contains(t, notesWithKey(rig.writer, "rejected")[0], "no longer exists")
}

func TestExampleBatch_AnItemWithNoChangelogEntryIsHeldWhenTheProjectDeclaresOneAndTheOtherItemLands(t *testing.T) {
	t.Parallel()

	rig := newRoundRig(t, "1 3")
	rig.project.Changelog = "CHANGELOG.md"

	previous := rig.h.commands.reply
	rig.h.commands.reply = func(request CommandRequest) (CommandOutput, error) {
		if request.Run[1] == "diff" && rig.inLandPhase() && strings.HasSuffix(request.Run[len(request.Run)-1], rig.heads["1"]) {
			return CommandOutput{Stdout: "one.txt\n"}, nil
		}

		if request.Run[1] == "diff" {
			return CommandOutput{Stdout: "three.txt\nCHANGELOG.md\n"}, nil
		}

		return previous(request)
	}

	_, err := rig.run(t)

	require.NoError(t, err)
	assert.Equal(t, []string{"3"}, rig.merger.landed(), "the item with no entry never reaches the merge")
	assert.Equal(t, []string{testProject + "|1|hold"}, heldOf(rig.writer))
	require.Len(t, notesWithKey(rig.writer, "rejected"), 1)
	assert.Contains(t, notesWithKey(rig.writer, "rejected")[0], "the project declares the changelog CHANGELOG.md")
	lines := gitLines(rig)
	require.GreaterOrEqual(t, len(lines), 4)
	assert.Equal(t, []string{
		"git fetch origin " + rig.heads["1"], "git diff --name-only origin/main..." + rig.heads["1"],
		"git fetch origin " + rig.heads["3"], "git diff --name-only origin/main..." + rig.heads["3"],
	}, lines[len(lines)-4:], "after the checks of the children, each member is fetched, then read from the base to its own head")
}

func TestExampleBatch_TheChangelogChecksOfTwoItemsRecordTheirOwnSteps(t *testing.T) {
	t.Parallel()

	rig := newRoundRig(t, "1 3")
	rig.project.Changelog = "CHANGELOG.md"

	previous := rig.h.commands.reply
	rig.h.commands.reply = func(request CommandRequest) (CommandOutput, error) {
		if request.Run[0] == "git" {
			return CommandOutput{Stdout: "CHANGELOG.md\n"}, nil
		}

		return previous(request)
	}

	_, err := rig.run(t)
	require.NoError(t, err)

	joined := strings.Join(stepNames(t, rig.last), " ")
	for _, id := range []string{"1", "3"} {
		assert.Contains(t, joined, "changelog-fetch-"+id+"#")
		assert.Contains(t, joined, "changed-paths-"+id+"#")
	}
}

func TestExampleBatch_AnItemWhoseHeadCannotBeFetchedIsHeldWithTheReasonAndTheOtherItemLands(t *testing.T) {
	t.Parallel()

	rig := newRoundRig(t, "1 3")
	rig.project.Changelog = "CHANGELOG.md"

	previous := rig.h.commands.reply
	rig.h.commands.reply = func(request CommandRequest) (CommandOutput, error) {
		if request.Run[1] == "fetch" && request.Run[len(request.Run)-1] == rig.heads["1"] {
			return CommandOutput{RC: 1, Stderr: "fatal: no such ref\n"}, nil
		}

		if request.Run[0] == "git" {
			return CommandOutput{Stdout: "CHANGELOG.md\n"}, nil
		}

		return previous(request)
	}

	_, err := rig.run(t)

	require.NoError(t, err, "a code of 1 is a result, not a stop of the run")
	assert.Equal(t, []string{"3"}, rig.merger.landed(), "the item that cannot be checked never reaches the merge")
	assert.Equal(t, []string{testProject + "|1|hold"}, heldOf(rig.writer))
	require.Len(t, notesWithKey(rig.writer, "rejected"), 1)
	assert.Contains(t, notesWithKey(rig.writer, "rejected")[0], "could not be fetched")
	assert.Contains(t, notesWithKey(rig.writer, "rejected")[0], "fatal: no such ref")
	diff := "git diff --name-only origin/main..." + rig.heads["1"]
	diffs := slices.DeleteFunc(gitLines(rig), func(line string) bool { return line != diff })
	assert.Len(t, diffs, 1, "the one diff of item 1 is the check of its child, before the batch: no diff runs after the failed fetch")
}

func TestExampleBatch_AnItemWhoseChangedPathsCannotBeReadIsHeldWithTheReasonAndTheOtherItemLands(t *testing.T) {
	t.Parallel()

	rig := newRoundRig(t, "1 3")
	rig.project.Changelog = "CHANGELOG.md"

	previous := rig.h.commands.reply
	rig.h.commands.reply = func(request CommandRequest) (CommandOutput, error) {
		if rig.inLandPhase() && request.Run[1] == "diff" && strings.HasSuffix(request.Run[len(request.Run)-1], rig.heads["1"]) {
			return CommandOutput{RC: 1, Stderr: "fatal: bad revision\n"}, nil
		}

		if request.Run[0] == "git" {
			return CommandOutput{Stdout: "CHANGELOG.md\n"}, nil
		}

		return previous(request)
	}

	_, err := rig.run(t)

	require.NoError(t, err, "a code of 1 is a result, not a stop of the run")
	assert.Equal(t, []string{"3"}, rig.merger.landed(), "the item that cannot be checked never reaches the merge")
	assert.Equal(t, []string{testProject + "|1|hold"}, heldOf(rig.writer))
	require.Len(t, notesWithKey(rig.writer, "rejected"), 1)
	assert.Contains(t, notesWithKey(rig.writer, "rejected")[0], "changed paths of item 1 could not be read")
	assert.Contains(t, notesWithKey(rig.writer, "rejected")[0], "fatal: bad revision")
}

func TestExampleBatch_EveryItemGetsTheStopsOfOneItemBeforeTheBatch(t *testing.T) {
	t.Parallel()

	rig := newRoundRig(t, "1 3")

	_, err := rig.run(t)

	require.NoError(t, err)
	require.GreaterOrEqual(t, len(rig.remote.asked), 2)
	assert.ElementsMatch(t, []string{"1", "3"}, rig.remote.asked[len(rig.remote.asked)-2:], "the last two reads of the remote are the stops of the two members, one read each, before the batch")
}

func TestExampleBatch_AMergeSettingThatIsOffLandsNothing(t *testing.T) {
	t.Parallel()

	rig := newRoundRig(t, "1 3")
	rig.setRound("merge", false)

	_, err := rig.run(t)

	require.NoError(t, err)
	assert.Empty(t, rig.merger.asked, "no member reaches the merge")
	assert.Empty(t, rig.gate.ran(), "no gate runs")
	assert.Len(t, rig.remote.asked, 4, "only the intake and the review loop of the two children read a branch; the batch reads none")
	assert.Empty(t, heldOf(rig.writer))
	assert.Equal(t, "README", rig.mainFiles(t))
}

func TestExampleBatch_OneApprovedItemLandsAsABatchOfOne(t *testing.T) {
	t.Parallel()

	rig := newRoundRig(t, "3")

	_, err := rig.run(t)

	require.NoError(t, err)
	assert.Equal(t, []string{"3"}, rig.merger.landed())
}

func TestExampleBatch_TheIntegrationPullRequestOfTheRoundReachesTheReadAfterTheBatch(t *testing.T) {
	t.Parallel()

	rig := newRoundRig(t, "1 3")
	rig.setRound("integration_pr", json.Number("7"))

	_, err := rig.run(t)

	require.NoError(t, err)
	assert.Equal(t, []string{testProject + " main 7 1,3"}, rig.host.issueReads())
}

func TestExampleBatch_ARoundWithNoIntegrationPullRequestReadsWithNumberZero(t *testing.T) {
	t.Parallel()

	rig := newRoundRig(t, "1 3")

	_, err := rig.run(t)

	require.NoError(t, err)
	assert.Equal(t, []string{testProject + " main 0 1,3"}, rig.host.issueReads())
}

func TestExampleBatch_AFindingStopsTheRunWithARefuseForTheOwner(t *testing.T) {
	t.Parallel()

	rig := newRoundRig(t, "1 3")
	rig.host.report = batch.Report{Findings: []string{"issue #1 of item 1 is not closed after the batch landed"}}

	_, err := rig.run(t)

	require.Error(t, err)
	assert.Contains(t, err.Error(), "REFUSE")
	assert.Contains(t, err.Error(), "issue #1 of item 1 is not closed after the batch landed")
	assert.Contains(t, err.Error(), "by hand")
}

func TestExampleBatch_AnUnprovenMemberStopsTheRunWithARefuseThatNamesTheItemAndTheReason(t *testing.T) {
	t.Parallel()

	rig := newRoundRig(t, "1 3")
	rig.host.unproven = []batch.Outcome{{Item: "3", Reason: "its branch holds another commit now"}}

	_, err := rig.run(t)

	require.Error(t, err)
	assert.Contains(t, err.Error(), "REFUSE")
	assert.Contains(t, err.Error(), "item 3: its branch holds another commit now")
}

func TestExampleBatch_ANoteIsPrintedAsAnIssueCheckThatDidNotRun(t *testing.T) {
	t.Parallel()

	rig := newRoundRig(t, "1 3")
	rig.host.report = batch.Report{Note: "the base is not the default branch"}

	_, err := rig.run(t)

	require.NoError(t, err)
	assert.Empty(t, notesWithKey(rig.writer, "rejected"), "the round has no item issue for the note, so it prints it in the run record")
}

func TestExampleBatch_AReportWithNoFindingAndNoNoteSaysNothing(t *testing.T) {
	t.Parallel()

	rig := newRoundRig(t, "1 3")

	_, err := rig.run(t)

	require.NoError(t, err)
	assert.Empty(t, notesWithKey(rig.writer, "rejected"))
}

func TestExampleBatch_EachMergedItemIsRecordedAsMergedAndClosed(t *testing.T) {
	t.Parallel()

	rig := newRoundRig(t, "1 3")

	_, err := rig.run(t)

	require.NoError(t, err)
	assert.Equal(t, []string{testProject + "|1|closed", testProject + "|3|closed"}, closedOf(rig.writer))
	assert.Len(t, notesWithKey(rig.writer, "merged"), 2)
}

// closedOf returns the items that the run closed.
func closedOf(w *fakeWriter) []string {
	w.mu.Lock()
	defer w.mu.Unlock()

	var closed []string

	for _, status := range w.statuses {
		if strings.HasSuffix(status, "|closed") {
			closed = append(closed, status)
		}
	}

	return closed
}

func TestExampleSwarm_TheLandCallPassesTheIntegrationAndActsOnFindingsAndNote(t *testing.T) {
	t.Parallel()

	text := string(mustRead(t, filepath.Join("..", "..", "examples", "swarm", "workflow.star")))

	assert.Contains(t, text, `base = env("BASE")`)
	assert.Contains(t, text, `repo = env("REPO")`)
	assert.Contains(t, text, "out.merged")
	assert.Contains(t, text, "out.rejected")
	assert.Contains(t, text, "out.deferred")
	assert.Contains(t, text, `"Not landed: " + rejected.reason`)
	assert.Contains(t, text, "the forge could not settle")
}
