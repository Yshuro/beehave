package flow

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/dbos-inc/dbos-transact-golang/dbos"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// handForge is the forge side of a hand-off in a test. Each item has a script of views: a read takes the next one and the last one repeats.
type handForge struct {
	mu       sync.Mutex
	views    map[string][]PullView
	reads    map[string]int
	drafts   []string
	pullErr  error
	draftErr map[bool]error
	// same holds the heads that have the tree of the pinned head. treeErr is the error of the tree read.
	same    map[string]bool
	treeErr error
	// onRead, when set, runs at each read with the number of the read of that item, counted from 1.
	onRead func(item string, read int)
	// overran holds the item whose pull request the watch read more than handReadCap times.
	overran string
}

// handReadCap is the most reads of one pull request that a test of the hand-off allows before the test fails.
const handReadCap = 200

func (f *handForge) pull(item string) (PullView, error) {
	f.mu.Lock()
	f.reads[item]++
	read, script := f.reads[item], f.views[item]
	hook := f.onRead
	f.mu.Unlock()

	if hook != nil {
		hook(item, read)
	}

	// A watch that no owner ends reads for ever. The read past the cap closes the pull request so that the call ends, and the test fails with the item.
	if read > handReadCap {
		f.mu.Lock()
		f.overran = item
		f.mu.Unlock()

		return PullView{State: "CLOSED"}, nil
	}

	if f.pullErr != nil {
		return PullView{}, f.pullErr
	}

	return script[min(read, len(script))-1], nil
}

func (f *handForge) setDraft(item string, draft bool) error {
	f.mu.Lock()
	defer f.mu.Unlock()

	if err := f.draftErr[draft]; err != nil {
		return err
	}

	f.drafts = append(f.drafts, fmt.Sprintf("%s:%t", item, draft))

	return nil
}

func (f *handForge) marked() []string {
	f.mu.Lock()
	defer f.mu.Unlock()

	return append([]string(nil), f.drafts...)
}

func (h *repoHost) Pull(_ context.Context, _, item, _ string) (PullView, error) {
	return h.pr.pull(item)
}

func (h *repoHost) SetDraft(_ context.Context, _, item, _ string, draft bool) error {
	return h.pr.setDraft(item, draft)
}

// handRig is a batch rig whose host serves the pull requests of a hand-off. Each view is written as state, draft flag and head.
type handRig struct {
	*batchRig
}

func newHandRig(t *testing.T) *handRig {
	t.Helper()

	rig := &handRig{batchRig: newBatchRig(t)}
	rig.host.pr = &handForge{views: map[string][]PullView{}, reads: map[string]int{}, draftErr: map[bool]error{}}
	rig.h.facts = notifyFacts(notifyProgram)

	t.Cleanup(func() {
		rig.host.pr.mu.Lock()
		defer rig.host.pr.mu.Unlock()

		assert.Empty(t, rig.host.pr.overran, "the watch read the pull request of this item more than %d times: it waits for an owner who does not merge", handReadCap)
	})

	return rig
}

func (r *handRig) script(item string, views ...PullView) {
	r.host.pr.views[item] = views
}

// view makes a view of the pull request of an item at its own head, or at the head given.
func (r *handRig) view(item, state string, draft bool, head ...string) PullView {
	at := r.heads[item]
	if len(head) > 0 {
		at = head[0]
	}

	return PullView{State: state, Draft: draft, Head: at}
}

// body writes a hand-off call over the listed items and a command that reports the result.
func (r *handRig) body(items, extra string) string {
	var prs []string

	for _, item := range strings.Fields(items) {
		prs = append(prs, `struct(item = "`+item+`", head = "`+r.heads[item]+`")`)
	}

	return `out = merge(prs = [` + strings.Join(prs, ", ") + `], base = "main", repo = "o/r", hand_off = True, ` + extra + `name = "b")
command(name = "report", run = ["report", ",".join(out.merged), ",".join([m.item + "@" + m.head for m in out.moved]), ",".join([r.item + ":" + r.reason for r in out.rejected]), str(out.gates)])
`
}

func TestHandOff_AMergedPullRequestEndsTheWaitAndTheOwnerIsToldOnce(t *testing.T) {
	t.Parallel()

	rig := newHandRig(t)
	rig.script("1", rig.view("1", "OPEN", true), rig.view("1", "OPEN", false), rig.view("1", "OPEN", false), rig.view("1", "MERGED", false))

	_, err := rig.walk(t, rig.body("1", ""))

	require.NoError(t, err)
	assert.Equal(t, []string{"1:false"}, rig.host.pr.marked(), "the draft is marked ready once and nothing is drafted again")
	assert.Equal(t, 4, rig.host.pr.reads["1"], "one read to mark it ready, then one read for each round of the watch")
	assert.Len(t, notifyRequests(rig.h), 1, "the owner is told once for the whole wait")
	assert.Equal(t, []string{"report 1   0"}, filterReport(rig.h.commands.ran()))
	assert.Empty(t, rig.merger.landed(), "a hand-off lands nothing")
	assert.Empty(t, rig.gate.ran(), "a hand-off runs no gate")
}

func TestHandOff_AHeadThatMovesAfterTheReadyMarkPutsThePullRequestBackToDraftAndReturnsTheNewHead(t *testing.T) {
	t.Parallel()

	rig := newHandRig(t)
	rig.script("1", rig.view("1", "OPEN", true), rig.view("1", "OPEN", false, "moved1"))

	_, err := rig.walk(t, rig.body("1", ""))

	require.NoError(t, err)
	assert.Equal(t, []string{"1:false", "1:true"}, rig.host.pr.marked(), "marked ready, then back to draft")
	assert.Equal(t, []string{"report  1@moved1 1:the head moved to moved1 after the head " + rig.heads["1"] + " was marked ready 0"}, filterReport(rig.h.commands.ran()))
	assert.Len(t, notifyRequests(rig.h), 1)
}

func TestHandOff_AHeadThatMovedBeforeTheReadyMarkMarksNothingReady(t *testing.T) {
	t.Parallel()

	rig := newHandRig(t)
	rig.script("1", rig.view("1", "OPEN", true, "moved1"))

	_, err := rig.walk(t, rig.body("1", ""))

	require.NoError(t, err)
	assert.Empty(t, rig.host.pr.marked(), "a draft at another head is not marked ready and is already a draft")
	assert.Empty(t, notifyRequests(rig.h), "no member waits, so no owner is told")
	assert.Equal(t, []string{"report  1@moved1 1:the head moved to moved1 after the head " + rig.heads["1"] + " was marked ready 0"}, filterReport(rig.h.commands.ran()))
}

func TestHandOff_AClosedPullRequestLeavesTheWatchWithItsReason(t *testing.T) {
	t.Parallel()

	rig := newHandRig(t)
	rig.script("1", rig.view("1", "OPEN", true), rig.view("1", "CLOSED", false))

	_, err := rig.walk(t, rig.body("1", ""))

	require.NoError(t, err)
	assert.Equal(t, []string{"1:false"}, rig.host.pr.marked(), "a closed pull request is not drafted")
	assert.Equal(t, []string{"report   1:the pull request is closed 0"}, filterReport(rig.h.commands.ran()))
}

func TestHandOff_TwoMembersWaitTogetherAndTheOneThatMovedDoesNotEndTheOther(t *testing.T) {
	t.Parallel()

	rig := newHandRig(t)
	rig.script("1", rig.view("1", "OPEN", true), rig.view("1", "OPEN", false), rig.view("1", "MERGED", false))
	rig.script("2", rig.view("2", "OPEN", false), rig.view("2", "OPEN", false, "moved2"))

	_, err := rig.walk(t, rig.body("1 2", ""))

	require.NoError(t, err)
	assert.Equal(t, []string{"1:false", "2:true"}, rig.host.pr.marked(), "a ready pull request is not marked again, and the moved one is drafted")
	assert.Len(t, notifyRequests(rig.h), 1, "one notice for the whole call")
	assert.Equal(t, []string{"report 1 2@moved2 2:the head moved to moved2 after the head " + rig.heads["2"] + " was marked ready 0"}, filterReport(rig.h.commands.ran()))
}

func TestHandOff_AResumeDuringTheWaitMarksNothingReadyTwiceAndTellsTheOwnerNoMoreTimes(t *testing.T) {
	t.Parallel()

	rig := newHandRig(t)
	rig.script("1", rig.view("1", "OPEN", true), rig.view("1", "OPEN", false), rig.view("1", "MERGED", false))
	rig.host.pr.onRead = func(_ string, read int) {
		if read != 2 {
			return
		}

		if err := rig.h.client(t, testVersion).Cancel(testItem); err != nil {
			t.Error(err)
		}

		rig.h.tick(t)
	}
	file := writeFlow(t, bodyFlow(rig.body("1", "")))
	engine := rig.engine(t)

	_, err := engine.RunFile(testItem, testProject, file)

	require.Error(t, err)
	_, err = resumeAndJoin(t, engine, rig.h.client(t, testVersion))

	require.NoError(t, err)
	assert.Equal(t, []string{"1:false"}, rig.host.pr.marked(), "the resume replays the ready step and marks nothing ready again")
	assert.Len(t, notifyRequests(rig.h), 1, "the resume tells the owner no more times")
	assert.Equal(t, []string{"report 1   0"}, filterReport(rig.h.commands.ran()))
}

func TestHandOff_AReadThatFailsEndsTheCallAndNamesTheItem(t *testing.T) {
	t.Parallel()

	rig := newHandRig(t)
	rig.host.pr.pullErr = errors.New("forge is down")

	_, err := rig.walk(t, rig.body("1", ""))

	require.ErrorContains(t, err, "read the pull request of item 1: forge is down")
}

func TestHandOff_AReadyMarkThatFailsEndsTheCallAndNamesTheItem(t *testing.T) {
	t.Parallel()

	rig := newHandRig(t)
	rig.script("1", rig.view("1", "OPEN", true))
	rig.host.pr.draftErr[false] = errors.New("no permission")

	_, err := rig.walk(t, rig.body("1", ""))

	require.ErrorContains(t, err, "mark the pull request of item 1 ready: no permission")
}

func TestHandOff_ADraftCallThatFailsAfterAMovedHeadTellsTheOwnerToConvertByHand(t *testing.T) {
	t.Parallel()

	rig := newHandRig(t)
	rig.script("1", rig.view("1", "OPEN", false, "moved1"))
	rig.host.pr.draftErr[true] = errors.New("no permission")

	_, err := rig.walk(t, rig.body("1", ""))

	require.ErrorContains(t, err, "the head of item 1 moved to moved1. Cannot make its pull request a draft again. If it is open and ready for review, convert it to a draft by hand: no permission")
}

func TestHandOff_AKeywordOfALandingIsRefused(t *testing.T) {
	t.Parallel()

	cases := map[string]string{
		"hold":        `hold = "hold", `,
		"own":         `own = {"logins": ["me"]}, `,
		"forbid_text": `forbid_text = ["x"], `,
		"gate_run":    `gate_run = ["check"], `,
		"integration": `integration = 5, `,
	}

	for name, extra := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			rig := newHandRig(t)
			rig.script("1", rig.view("1", "OPEN", true))

			_, err := rig.walk(t, rig.body("1", extra))

			require.ErrorContains(t, err, "merge with `hand_off` lands nothing, so it takes no `"+name+"`")
			assert.Zero(t, rig.host.pr.reads["1"], "a refused call reads nothing")
		})
	}
}

// filterReport keeps the report commands of the flow and drops the notify commands.
func filterReport(ran []string) []string {
	var out []string

	for _, line := range ran {
		if strings.HasPrefix(line, "report ") {
			out = append(out, line)
		}
	}

	return out
}

func TestHandOff_ACancelAfterTheReadyMarkStopsTheCallAndAResumeGoesOn(t *testing.T) {
	t.Parallel()

	rig := newHandRig(t)
	rig.script("1", rig.view("1", "OPEN", true), rig.view("1", "MERGED", false))
	rig.host.pr.onRead = func(_ string, read int) {
		if read != 1 {
			return
		}

		if err := rig.h.client(t, testVersion).Cancel(testItem); err != nil {
			t.Error(err)
		}

		rig.h.tick(t)
	}
	file := writeFlow(t, bodyFlow(rig.body("1", "")))
	engine := rig.engine(t)

	_, err := engine.RunFile(testItem, testProject, file)

	require.Error(t, err)
	_, err = resumeAndJoin(t, engine, rig.h.client(t, testVersion))

	require.NoError(t, err)
	assert.Equal(t, []string{"1:false"}, rig.host.pr.marked())
}

// SameTree answers from the table of the pull request fake: the head named in same holds the tree of the pinned head.
func (h *repoHost) SameTree(_ context.Context, _, _, _, _, now string) (bool, error) {
	return h.pr.same[now], h.pr.treeErr
}

// standing makes a view of an item with the facts that hold a pull request back.
func (r *handRig) standing(item string, draft bool, set func(*PullView)) PullView {
	v := r.view(item, "OPEN", draft)
	set(&v)

	return v
}

func TestHandOff_AStandingReasonHoldsTheReadyMarkAndTheCallNamesIt(t *testing.T) {
	t.Parallel()

	cases := map[string]struct {
		set    func(*PullView)
		reason string
	}{
		"a change request": {func(v *PullView) { v.ChangeRequests = 1 }, "a review asks for changes"},
		"a conflict":       {func(v *PullView) { v.Conflicting = true }, "it conflicts with its base branch"},
		"a thread":         {func(v *PullView) { v.Threads = 2 }, "2 review threads are unresolved"},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			rig := newHandRig(t)
			rig.script("1", rig.standing("1", true, tc.set))

			_, err := rig.walk(t, rig.body("1", ""))

			require.NoError(t, err)
			assert.Empty(t, rig.host.pr.marked(), "a draft with a reason to wait is not marked ready")
			assert.Empty(t, notifyRequests(rig.h), "no member waits, so no owner is told")
			assert.Equal(t, []string{"report   1:not marked ready: " + tc.reason + " 0"}, filterReport(rig.h.commands.ran()))
		})
	}
}

func TestHandOff_AReadyPullRequestWithAChangeRequestOrAConflictGoesBackToDraftAtOnce(t *testing.T) {
	t.Parallel()

	rig := newHandRig(t)
	rig.script("1", rig.standing("1", false, func(v *PullView) { v.ChangeRequests = 1; v.Conflicting = true }))

	_, err := rig.walk(t, rig.body("1", ""))

	require.NoError(t, err)
	assert.Equal(t, []string{"1:true"}, rig.host.pr.marked())
	assert.Equal(t, []string{"report   1:drafted again: a review asks for changes, it conflicts with its base branch 0"}, filterReport(rig.h.commands.ran()))
}

func TestHandOff_AChangeRequestOrAConflictDuringTheWatchPutsThePullRequestBackToDraft(t *testing.T) {
	t.Parallel()

	cases := map[string]struct {
		set    func(*PullView)
		reason string
	}{
		"a change request": {func(v *PullView) { v.ChangeRequests = 1 }, "a review asks for changes"},
		"a conflict":       {func(v *PullView) { v.Conflicting = true }, "it conflicts with its base branch"},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			rig := newHandRig(t)
			rig.script("1", rig.view("1", "OPEN", true), rig.view("1", "OPEN", false), rig.standing("1", false, tc.set))

			_, err := rig.walk(t, rig.body("1", ""))

			require.NoError(t, err)
			assert.Equal(t, []string{"1:false", "1:true"}, rig.host.pr.marked(), "marked ready, then back to draft")
			assert.Equal(t, []string{"report   1:drafted again: " + tc.reason + " 0"}, filterReport(rig.h.commands.ran()))
		})
	}
}

func TestHandOff_AnUnresolvedThreadDuringTheWatchLeavesThePullRequestReady(t *testing.T) {
	t.Parallel()

	rig := newHandRig(t)
	rig.script("1", rig.view("1", "OPEN", true), rig.standing("1", false, func(v *PullView) { v.Threads = 3 }), rig.view("1", "MERGED", false))

	_, err := rig.walk(t, rig.body("1", ""))

	require.NoError(t, err)
	assert.Equal(t, []string{"1:false"}, rig.host.pr.marked(), "a thread of a ready pull request does not draft it")
	assert.Equal(t, []string{"report 1   0"}, filterReport(rig.h.commands.ran()))
}

func TestHandOff_AHeadRewrittenWithTheSameTreeIsNotAMove(t *testing.T) {
	t.Parallel()

	rig := newHandRig(t)
	rig.host.pr.same = map[string]bool{"rewrite1": true}
	rig.script("1", rig.view("1", "OPEN", true, "rewrite1"), rig.view("1", "OPEN", false, "rewrite1"), rig.view("1", "MERGED", false, "rewrite1"))

	_, err := rig.walk(t, rig.body("1", ""))

	require.NoError(t, err)
	assert.Equal(t, []string{"1:false"}, rig.host.pr.marked(), "the rewrite is marked ready and never drafted")
	assert.Equal(t, []string{"report 1   0"}, filterReport(rig.h.commands.ran()))
}

func TestHandOff_AHeadWithAnotherTreeIsAMoveEvenWhenOtherHeadsHoldTheSameTree(t *testing.T) {
	t.Parallel()

	rig := newHandRig(t)
	rig.host.pr.same = map[string]bool{"rewrite1": true}
	rig.script("1", rig.view("1", "OPEN", false, "other1"))

	_, err := rig.walk(t, rig.body("1", ""))

	require.NoError(t, err)
	assert.Equal(t, []string{"1:true"}, rig.host.pr.marked())
	assert.Contains(t, filterReport(rig.h.commands.ran())[0], "1@other1")
}

func TestHandOff_ATreeThatCannotBeReadEndsTheCallAndNamesTheItem(t *testing.T) {
	t.Parallel()

	rig := newHandRig(t)
	rig.host.pr.treeErr = errors.New("no tree")
	rig.script("1", rig.view("1", "OPEN", true, "rewrite1"))

	_, err := rig.walk(t, rig.body("1", ""))

	require.ErrorContains(t, err, "compare the tree of the head of item 1: no tree")
	assert.Empty(t, rig.host.pr.marked())
}

func TestHandOff_ACancelDuringTheWatchPutsTheReadyPullRequestBackToDraft(t *testing.T) {
	t.Parallel()

	rig := newHandRig(t)
	rig.h.sleep = time.Hour
	rig.script("1", rig.view("1", "OPEN", true), rig.view("1", "OPEN", false))
	engine := rig.engine(t)
	file := writeFlow(t, bodyFlow(rig.body("1", "")))
	ended := make(chan error, 1)

	go func() {
		_, err := engine.RunFile(testItem, testProject, file)
		ended <- err
	}()

	waitOn(t, inASleep(t, engine))
	require.NoError(t, rig.h.client(t, testVersion).Cancel(testItem))
	rig.h.tick(t)

	require.Error(t, receive(t, ended))
	assert.Equal(t, []string{"1:false", "1:true"}, rig.host.pr.marked(), "a ready pull request that nobody watches goes back to draft")
}

func TestHandOff_ACancelThatCannotDraftTheReadyPullRequestTellsTheOwnerToConvertByHand(t *testing.T) {
	t.Parallel()

	rig := newHandRig(t)
	rig.h.sleep = time.Hour
	rig.script("1", rig.view("1", "OPEN", true), rig.view("1", "OPEN", false))
	rig.host.pr.draftErr[true] = errors.New("no permission")
	engine := rig.engine(t)
	file := writeFlow(t, bodyFlow(rig.body("1", "")))
	ended := make(chan error, 1)

	go func() {
		_, err := engine.RunFile(testItem, testProject, file)
		ended <- err
	}()

	waitOn(t, inASleep(t, engine))
	require.NoError(t, rig.h.client(t, testVersion).Cancel(testItem))
	rig.h.tick(t)

	require.Error(t, receive(t, ended))

	notices := notifyRequests(rig.h)
	require.Len(t, notices, 2, "the notice of the wait and the notice of the draft that failed")
	assert.Contains(t, notices[1].Run[len(notices[1].Run)-1], "cannot make the pull request of item 1 a draft again. If it is open and ready for review, convert it to a draft by hand: no permission")
}

func TestHandOff_AWatchSleepsForTheTimeOfTheClockBetweenTwoReads(t *testing.T) {
	t.Parallel()

	rig := newHandRig(t)
	rig.h.sleep = 3 * time.Hour
	rig.script("1", rig.view("1", "OPEN", true), rig.view("1", "OPEN", false))
	engine := rig.engine(t)
	file := writeFlow(t, bodyFlow(rig.body("1", "")))

	go func() { _, _ = engine.RunFile(testItem, testProject, file) }() //nolint:errcheck // the test cancels the run below

	waitOn(t, inASleep(t, engine))

	steps, err := dbos.GetWorkflowSteps(engine.ctx, testRun)
	require.NoError(t, err)

	i := slices.IndexFunc(steps, func(s dbos.StepInfo) bool { return s.StepName == "DBOS.sleep" })
	require.GreaterOrEqual(t, i, 0)
	assert.InDelta(t, (3 * time.Hour).Seconds(), steps[i].CompletedAt.Sub(steps[i].StartedAt).Seconds(), 30, "the recorded wake time is the time of the clock after the start of the sleep")

	require.NoError(t, rig.h.client(t, testVersion).Cancel(testItem))
	rig.h.tick(t)
}

func TestHandOff_APullRequestThatSomeoneDraftedDuringTheWatchStaysInTheWatch(t *testing.T) {
	t.Parallel()

	rig := newHandRig(t)
	rig.script("1", rig.view("1", "OPEN", true), rig.view("1", "OPEN", false), rig.standing("1", true, func(v *PullView) { v.ChangeRequests = 1 }), rig.view("1", "MERGED", false))

	_, err := rig.walk(t, rig.body("1", ""))

	require.NoError(t, err)
	assert.Equal(t, []string{"1:false"}, rig.host.pr.marked(), "a draft in the watch is left as it is, and nothing marks it ready again")
	assert.Equal(t, []string{"report 1   0"}, filterReport(rig.h.commands.ran()))
}

func TestHandOff_ACancelDraftsOnlyTheMembersThatHaveNotEnded(t *testing.T) {
	t.Parallel()

	rig := newHandRig(t)
	rig.h.sleep = time.Hour
	rig.script("1", rig.view("1", "OPEN", true), rig.view("1", "MERGED", false))
	rig.script("2", rig.view("2", "OPEN", true), rig.view("2", "OPEN", false))
	rig.script("3", rig.standing("3", true, func(v *PullView) { v.Conflicting = true }))
	engine := rig.engine(t)
	file := writeFlow(t, bodyFlow(rig.body("1 2 3", "")))
	ended := make(chan error, 1)

	go func() {
		_, err := engine.RunFile(testItem, testProject, file)
		ended <- err
	}()

	waitOn(t, inASleep(t, engine))
	require.NoError(t, rig.h.client(t, testVersion).Cancel(testItem))
	rig.h.tick(t)

	require.Error(t, receive(t, ended))
	assert.Equal(t, []string{"1:false", "2:false", "2:true"}, rig.host.pr.marked(), "the merged member needs no draft call and the other one gets it")
}
