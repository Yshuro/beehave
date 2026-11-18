package flow

import (
	"cmp"
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"strings"

	"github.com/dbos-inc/dbos-transact-golang/dbos"
	"go.starlark.net/starlark"
	"go.starlark.net/starlarkstruct"

	"github.com/tobocop2/beehave/internal/model"
	"github.com/tobocop2/beehave/internal/preflight"
	"github.com/tobocop2/beehave/internal/workflow"
)

// The states of a row of the queue.
const (
	queueReady   = "ready"
	queueRunning = "running"
	queueDone    = "done"
	queueFailed  = "failed"
	queueHold    = "hold"
)

// foreignReason is the reason that the run of a foreign review ends with, and pullMerged is the state of a merged pull request.
const (
	foreignReason = "foreign"
	pullMerged    = "MERGED"
)

// holdStep is the name of the step that reads the hold of an item at the start of a run that spawn started.
const holdStep = "hold:start"

var errNoQueue = &absentError{message: "this build cannot read the open work. Use a build that sets a queue reader", step: "read the open work of the project"}

// queueReader reads the open work of a project: the open items of its tracker and the open pull requests of its forge.
type queueReader interface {
	OpenItems(ctx context.Context, project string) ([]model.Item, error)
	OpenPulls(ctx context.Context, project string) ([]model.PullRequest, error)
	// PullState reads the state of one pull request, open or not: OPEN, CLOSED or MERGED.
	PullState(ctx context.Context, project string, pr int) (string, error)
}

// queueRow is one open item of the queue, as the queue step records it. PR is 0 when the item has no open pull request.
// Parent is the container of the item, empty for loose work. Author and ChangesRequested come from the open pull request of the item's branch with the lowest number; Author is empty when there is none.
type queueRow struct {
	Item             string `json:"item"`
	State            string `json:"state"`
	Severity         string `json:"severity"`
	PR               int    `json:"pr"`
	Parent           string `json:"parent"`
	Author           string `json:"author"`
	ChangesRequested bool   `json:"changes_requested"`
}

// queued is the output of the queue step: the rows in the order of the queue, and the number of free slots of the items queue.
type queued struct {
	Rows []queueRow `json:"rows"`
	Free int        `json:"free"`
}

// joinedPull is what the open pull requests of one branch say about the item of that branch.
type joinedPull struct {
	number           int
	held             bool
	author           string
	changesRequested bool
}

// queue reads the open items and the open pull requests and returns one row for each item, with its state, and the number of free slots.
// The read is one recorded step, so a replay returns the first answer. A read that fails ends the step: the call returns no guess.
func (s *script) queue(b *starlark.Builtin, args starlark.Tuple, kwargs []starlark.Tuple) (starlark.Value, error) { //nolint:ireturn // the builtin contract of starlark-go
	var hold, name, tracked string

	if err := workflow.Unpack(b.Name(), args, kwargs, map[string]any{nameKeyword: &name, holdKeyword: &hold, "label": &tracked}); err != nil {
		return nil, err
	}

	if s.e.deps.Queue == nil {
		return nil, errNoQueue
	}

	got, err := countedStep(s, "queue", name, func(c context.Context) (queued, error) {
		return s.e.readQueue(c, s.in.Project, hold, tracked)
	})
	if err != nil {
		return nil, err
	}

	return queuedValue(got), nil
}

// readQueue reads the tracker and the forge, then the runs of each item, and sorts the rows by severity and item id.
// A tracked label other than "" keeps only the items that carry that tracker label. The rows hold no labels, so a workflow cannot make this choice itself.
func (e *Engine) readQueue(ctx context.Context, project, holdLabel, tracked string) (queued, error) {
	items, err := e.deps.Queue.OpenItems(ctx, project)
	if err != nil {
		return queued{}, fmt.Errorf("read the open items of project %s: %w", project, err)
	}

	items = withLabel(items, tracked)

	pulls, err := e.deps.Queue.OpenPulls(ctx, project)
	if err != nil {
		return queued{}, fmt.Errorf("read the open pull requests of project %s: %w", project, err)
	}

	// The slots are read before the rows. A run that ended before the slots were read shows as not running in the rows, so a caller can tell a slot
	// that is free from a slot that a run left a moment ago. In the other order, a run could end between the two reads and show as running with a free slot.
	free, err := e.freeSlots()
	if err != nil {
		return queued{}, err
	}

	rows, err := e.queueRows(ctx, items, joinPulls(pulls, holdLabel))

	return queued{Rows: rows, Free: free}, err
}

// withLabel returns the items that carry the tracker label. An empty label keeps every item.
func withLabel(items []model.Item, tracked string) []model.Item {
	if tracked == "" {
		return items
	}

	return slices.DeleteFunc(slices.Clone(items), func(item model.Item) bool { return !slices.Contains(item.Labels, tracked) })
}

// queueRows gives one row for each item, sorted by severity and then by item id.
func (e *Engine) queueRows(ctx context.Context, items []model.Item, joined map[string]joinedPull) ([]queueRow, error) {
	rows := make([]queueRow, 0, len(items))

	for _, item := range items {
		row, err := e.rowOf(ctx, item, joined[item.ID])
		if err != nil {
			return nil, err
		}

		rows = append(rows, row)
	}

	slices.SortFunc(rows, func(a, b queueRow) int {
		return cmp.Or(strings.Compare(a.Severity, b.Severity), strings.Compare(a.Item, b.Item))
	})

	return rows, nil
}

// joinPulls groups the open pull requests by their head branch, which is the id of the item. Of many pull requests on one branch, the lowest number counts,
// and one with the hold label holds the item. A pull request with no head branch joins no item.
func joinPulls(pulls []model.PullRequest, holdLabel string) map[string]joinedPull {
	joined := map[string]joinedPull{}

	for _, pull := range pulls {
		if pull.HeadRefName == nil {
			continue
		}

		one := joined[*pull.HeadRefName]
		if one.number == 0 || pull.Number < one.number {
			one.number = pull.Number
			one.author = pull.Author.Login
			one.changesRequested = pull.ReviewDecision != nil && *pull.ReviewDecision == string(model.ReviewChangesRequested)
		}

		one.held = one.held || (holdLabel != "" && hasLabel(pull, holdLabel))
		joined[*pull.HeadRefName] = one
	}

	return joined
}

// hasLabel reports whether the pull request carries the label.
func hasLabel(pull model.PullRequest, name string) bool {
	for _, l := range pull.Labels {
		if l.Name == name {
			return true
		}
	}

	return false
}

// rowOf gives the state of one item. It reads the last run with latestRun, as spawn does, so the queue and spawn agree on what is open, failed and done.
// A hold decides only for an item that spawn could start: the run of an item has its own state.
func (e *Engine) rowOf(ctx context.Context, item model.Item, pull joinedPull) (queueRow, error) {
	last, found, err := latestRun(e.ctx, item.ID)
	if err != nil {
		return queueRow{}, err
	}

	state, err := e.stateOf(ctx, last, found, item.Hold || pull.held)
	if err != nil {
		return queueRow{}, err
	}

	return rowWith(item, pull, state), nil
}

// stateOf is the state of an item from its last run, if it has one. A run that succeeded is done, unless it is owed another visit. held is the hold of the item.
func (e *Engine) stateOf(ctx context.Context, last run, found, held bool) (string, error) {
	switch {
	case found && !last.terminal():
		return queueRunning, nil
	case found && last.failed():
		return queueFailed, nil
	case found && last.Status.Status == dbos.WorkflowStatusSuccess:
		revisit, err := e.owesVisit(ctx, last)
		if err != nil {
			return "", err
		}

		if !revisit {
			return queueDone, nil
		}
	}

	return idleState(held), nil
}

// idleState is the state of an item with no run to settle: hold, or ready.
func idleState(held bool) string {
	if held {
		return queueHold
	}

	return queueReady
}

// foreignRecord is what the run of a foreign review records in its value: the reason, the number of the reviewed pull request and the findings it left open.
type foreignRecord struct {
	Reason   string            `json:"reason"`
	PR       int               `json:"pr"`
	Findings []json.RawMessage `json:"findings"`
}

// owesVisit reports whether a run that ended with success is owed another visit: its last run ended with the reason foreign, it recorded open findings,
// and its recorded pull request merged. The state is read for the recorded number: the pull request is not looked up again.
// A value that is not a foreign record, or a run that recorded no value, owes none.
func (e *Engine) owesVisit(ctx context.Context, last run) (bool, error) {
	recorded, ok := foreignRecordOf(last)
	if !ok {
		return false, nil
	}

	project, err := last.project()
	if err != nil {
		return false, err
	}

	state, err := e.deps.Queue.PullState(ctx, project, recorded.PR)
	if err != nil {
		return false, fmt.Errorf("read the state of pull request %d for run %s: %w", recorded.PR, last.Status.ID, err)
	}

	return state == pullMerged, nil
}

// foreignRecordOf reads the foreign record in the value of a run. It is false when the run recorded no value, or a value that is not a foreign record with a pull request and open findings.
func foreignRecordOf(last run) (foreignRecord, bool) {
	var result ItemResult

	if json.Unmarshal([]byte(fmt.Sprint(last.Status.Output)), &result) != nil { // WithFilterLoadOutput returns the stored JSON as a string
		return foreignRecord{}, false
	}

	var recorded foreignRecord

	if json.Unmarshal(result.Value, &recorded) != nil {
		return foreignRecord{}, false
	}

	return recorded, recorded.Reason == foreignReason && recorded.PR != 0 && len(recorded.Findings) > 0
}

// rowWith joins the item, its pull request and its state into a row.
func rowWith(item model.Item, pull joinedPull, state string) queueRow {
	return queueRow{
		Item: item.ID, State: state, Severity: string(item.Severity), PR: pull.number,
		Parent: item.Parent, Author: pull.author, ChangesRequested: pull.changesRequested,
	}
}

// freeSlots is the cap of the items queue less the runs that the library holds on it as open. The library keeps the count: the engine keeps none.
func (e *Engine) freeSlots() (int, error) {
	open, err := dbos.ListWorkflows(e.ctx, dbos.WithFilterQueueName(itemsQueueName), dbos.WithFilterStatus(dbos.WorkflowStatusPending, dbos.WorkflowStatusEnqueued))
	if err != nil {
		return 0, fmt.Errorf("count the open runs of the %s queue: %w", itemsQueueName, err)
	}

	return max(0, e.maxConcurrent-len(open)), nil
}

// queuedValue makes the struct that queue returns: rows, a list of structs with the fields item, state, severity, pr, parent, author and changes_requested, and free.
func queuedValue(got queued) *starlarkstruct.Struct {
	rows := make([]starlark.Value, len(got.Rows))

	for i, row := range got.Rows {
		rows[i] = starlarkstruct.FromStringDict(starlarkstruct.Default, starlark.StringDict{
			itemName: starlark.String(row.Item), stateField: starlark.String(row.State), severityField: starlark.String(row.Severity), "pr": starlark.MakeInt(row.PR),
			parentKeyword: starlark.String(row.Parent), "author": starlark.String(row.Author), "changes_requested": starlark.Bool(row.ChangesRequested),
		})
	}

	return starlarkstruct.FromStringDict(starlarkstruct.Default, starlark.StringDict{"rows": starlark.NewList(rows), "free": starlark.MakeInt(got.Free)})
}

// holdAtStart stops a run that spawn started when its item is on hold in the tracker now. The tracker can change between the queue call and the start of the run.
// The read is a recorded step, so a replay takes the same branch. A build with no queue reader checks nothing.
func (s *script) holdAtStart() error {
	if s.in.Entry == "" || s.e.deps.Queue == nil {
		return nil
	}

	held, err := runStep(s.e, s.ctx, holdStep, func(c context.Context) (bool, error) {
		items, err := s.e.deps.Queue.OpenItems(c, s.in.Project)

		return slices.ContainsFunc(items, func(i model.Item) bool { return i.ID == s.in.Item && i.Hold }), err
	})
	if err != nil {
		return fmt.Errorf("read the hold of item %s: %w", s.in.Item, err)
	}

	if held {
		s.stopped = &Stopped{Step: "queue", RC: preflight.Held.Code(), Output: "item " + s.in.Item + " is on hold"}

		return errStopped
	}

	return nil
}
