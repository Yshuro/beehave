package flow

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/dbos-inc/dbos-transact-golang/dbos"
	"go.starlark.net/starlark"
	"go.starlark.net/starlarkstruct"

	"github.com/tobocop2/beehave/internal/batch"
)

// watchEvery is the time between two reads of the ready pull requests of a hand-off. A day of waiting costs 24 reads and 24 sleeps in the durable history.
const watchEvery = time.Hour

// mergeOwner is the value of the merge event while a hand-off waits for the owner to merge.
const mergeOwner = "owner_merge"

// PullView is the state of a pull request that a hand-off reads. State is OPEN, CLOSED or MERGED, and Head is the commit at its tip.
type PullView struct {
	State string
	Draft bool
	Head  string
}

// handState says how one read of a member of a hand-off ended. Only handWaiting keeps the member in the watch.
type handState string

const (
	handWaiting handState = "waiting"
	handMerged  handState = "merged"
	handClosed  handState = "closed"
	handMoved   handState = "moved"
)

// handRead is the record of one read of a member. Head is the head that a handMoved read found, and Reason says why a member left the watch without a merge.
type handRead struct {
	State  handState `json:"state"`
	Head   string    `json:"head,omitempty"`
	Reason string    `json:"reason,omitempty"`
}

// handResult collects how the members of a hand-off ended.
type handResult struct {
	merged   []string
	rejected []batch.Outcome
	moved    []batch.Member
}

// handOff marks each member ready, tells the owner once, and reads the members until each one is merged, closed or drafted again because its head moved.
// A member that is not ready and open at its head never gets the ready mark. The wait between two reads is a durable sleep, so a restart resumes the watch.
func (s *script) handOff(call batchCall) (starlark.Value, error) { //nolint:ireturn // the builtin contract of starlark-go
	var out handResult

	pending, err := s.handLook(call, call.members, 0, &out)
	if err != nil {
		return nil, err
	}

	if len(pending) > 0 {
		s.handAsk()

		err = s.handWatch(call, pending, &out)
	}

	value, resultErr := handResultValue(out)

	return value, errors.Join(err, resultErr, show(s.ctx, s.in.Item, mergeKey, "", "that the wait for the owner is over"))
}

// handWatch reads the members that wait, round by round, until none waits. A durable sleep separates two rounds.
func (s *script) handWatch(call batchCall, pending []batch.Member, out *handResult) error {
	for round := 1; ; round++ {
		var err error
		if pending, err = s.handLook(call, pending, round, out); err != nil || len(pending) == 0 {
			return err
		}

		if _, err = dbos.Sleep(s.ctx, cmp.Or(s.e.deps.Clock.Watch, watchEvery)); err != nil {
			return fmt.Errorf("merge %q waits for the owner of item %s: %w", call.name, s.in.Item, err)
		}
	}
}

// handAsk shows that the run waits for the owner and runs the notify command of the project once.
// A cancel makes the event write fail, and the next read step of the watch reports the same cancel, so the write error is not returned here.
func (s *script) handAsk() {
	_ = show(s.ctx, s.in.Item, mergeKey, mergeOwner, "that it waits for the owner to merge") //nolint:errcheck // the next step of the watch reports a cancel

	s.e.announce(s.ctx, s.in.Project, s.in.Item, fmt.Sprintf("The pull request of item %s is ready. Merge it, or send it back.", s.in.Item))
}

// handLook reads each pending member as one recorded step and returns the members that still wait. Round 0 is the read that marks the member ready.
func (s *script) handLook(call batchCall, pending []batch.Member, round int, out *handResult) ([]batch.Member, error) {
	var waiting []batch.Member

	for _, m := range pending {
		read, err := runStep(s.e, s.ctx, handStepName(call.name, m.Item, round), func(c context.Context) (handRead, error) {
			return s.e.handRead(c, s.in.Project, call.settings.Repo, m, round == 0)
		}, dbos.WithStepMaxRetries(stepRetries))
		if err != nil {
			return nil, fmt.Errorf("merge %q: %w", call.name, err)
		}

		if out.add(m, read) {
			waiting = append(waiting, m)
		}
	}

	return waiting, nil
}

// handStepName is the name of the read of a member in a round. Round 0 is the read that marks the member ready.
func handStepName(call, item string, round int) string {
	if round == 0 {
		return fmt.Sprintf("batch:%s.ready#%s", call, item)
	}

	return fmt.Sprintf("batch:%s.watch#%s@%d", call, item, round)
}

// add keeps the end of a member in the result. It reports whether the member still waits.
func (r *handResult) add(m batch.Member, read handRead) bool {
	switch read.State {
	case handMerged:
		r.merged = append(r.merged, m.Item)
	case handClosed:
		r.rejected = append(r.rejected, batch.Outcome{Item: m.Item, Reason: read.Reason})
	case handMoved:
		r.rejected = append(r.rejected, batch.Outcome{Item: m.Item, Reason: read.Reason})
		r.moved = append(r.moved, batch.Member{Item: m.Item, Head: read.Head})
	case handWaiting:
	}

	return read.State == handWaiting
}

// handRead reads the pull request of a member once. A merged pull request ends the member. A pull request that is not open ends it with a reason.
// A head that is not the head of the member puts the pull request back to draft. With ready set, a draft at the head of the member is marked ready.
func (e *Engine) handRead(ctx context.Context, project, repo string, m batch.Member, ready bool) (handRead, error) {
	view, err := e.deps.Batch.Pull(ctx, project, m.Item, repo)
	if err != nil {
		return handRead{}, fmt.Errorf("read the pull request of item %s: %w", m.Item, err)
	}

	switch {
	case view.State == "MERGED":
		return handRead{State: handMerged}, nil
	case view.State != "OPEN":
		return handRead{State: handClosed, Reason: "the pull request is " + strings.ToLower(view.State)}, nil
	case view.Head != m.Head:
		return e.handMoved(ctx, project, repo, m, view)
	case ready && view.Draft:
		if err := e.deps.Batch.SetDraft(ctx, project, m.Item, repo, false); err != nil {
			return handRead{}, fmt.Errorf("mark the pull request of item %s ready: %w", m.Item, err)
		}
	}

	return handRead{State: handWaiting}, nil
}

// handMoved puts a pull request whose head moved back to draft, unless it is a draft already, and returns the new head.
func (e *Engine) handMoved(ctx context.Context, project, repo string, m batch.Member, view PullView) (handRead, error) {
	if !view.Draft {
		if err := e.deps.Batch.SetDraft(ctx, project, m.Item, repo, true); err != nil {
			return handRead{}, fmt.Errorf("the head of item %s moved to %s. Cannot make its pull request a draft again. If it is open and ready for review, convert it to a draft by hand: %w", m.Item, view.Head, err)
		}
	}

	return handRead{State: handMoved, Head: view.Head, Reason: fmt.Sprintf("the head moved to %s after the head %s was marked ready", view.Head, m.Head)}, nil
}

// handResultValue builds the result of a hand-off: the fields of the result of a landing, with no landing in them.
func handResultValue(out handResult) (starlark.Value, error) { //nolint:ireturn // the builtin contract of starlark-go
	return batchResult(batch.Result{Merged: out.merged, Rejected: out.rejected}, batch.Report{}, nil, nil, out.moved)
}

// movedList turns the members whose head moved into a Starlark list of struct(item, head).
func movedList(moved []batch.Member) *starlark.List {
	out := make([]starlark.Value, len(moved))
	for i, m := range moved {
		out[i] = starlarkstruct.FromStringDict(starlarkstruct.Default, starlark.StringDict{itemName: starlark.String(m.Item), headKeyword: starlark.String(m.Head)})
	}

	return starlark.NewList(out)
}
