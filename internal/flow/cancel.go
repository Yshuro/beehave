package flow

import (
	"context"
	"errors"
	"sync"
	"time"

	"github.com/dbos-inc/dbos-transact-golang/dbos"
)

const (
	questionKey  = "question"
	othersKey    = "others"
	othersStep   = "others"
	mergeKey     = "merge"
	tickInterval = 2 * time.Second
)

// openRun is a run this process executes: the cancel function of its context, the channel that closes when its workflow function returns, and its input.
type openRun struct {
	id     string
	cancel context.CancelFunc
	ended  chan struct{}
	in     ItemInput
	// gate is true for a gate of the exclusive queue: a cancel stops its context, and there is no claim or worktree to give back.
	gate bool
}

// cancels holds the runs this process executes, by run id.
type cancels struct {
	mu   sync.Mutex
	runs map[string]openRun
	// releasing holds the attempts whose give-back is under way, so no second give-back starts for one run.
	// A resumed run reuses the run id, so the record names the attempt: the end of an earlier give-back leaves the record of a later one.
	releasing map[attempt]bool
}

// begin marks a canceled run that this process neither executes nor releases, and reports whether the caller must release it.
// The mark names no channel: the run is not an attempt of this process.
func (c *cancels) begin(id string) bool {
	c.mu.Lock()
	defer c.mu.Unlock()

	if _, local := c.runs[id]; local || c.releases(id) {
		return false
	}

	c.markReleasing(attempt{id: id})

	return true
}

// releases reports whether a give-back of any attempt of the run is under way. The caller holds the lock.
func (c *cancels) releases(id string) bool {
	for a := range c.releasing {
		if a.id == id {
			return true
		}
	}

	return false
}

// markReleasing records a give-back under way. The caller holds the lock.
func (c *cancels) markReleasing(a attempt) {
	if c.releasing == nil {
		c.releasing = map[attempt]bool{}
	}

	c.releasing[a] = true
}

// finish ends the record of the give-back of an attempt and leaves the record of any other.
func (c *cancels) finish(a attempt) {
	c.mu.Lock()
	defer c.mu.Unlock()

	delete(c.releasing, a)
}

func (c *cancels) add(id string, open openRun) {
	c.mu.Lock()
	defer c.mu.Unlock()

	if c.runs == nil {
		c.runs = map[string]openRun{}
	}

	c.runs[id] = open
}

// attempt names one attempt of a run: its run id and the channel that its own listing created. A resumed run reuses the run id and lists a new channel,
// so an attempt that has ended no longer matches the entry of the attempt that replaced it.
type attempt struct {
	id    string
	ended chan struct{}
}

// current reports whether the attempt is the one listed under its run id. The caller holds the lock.
func (c *cancels) current(a attempt) bool {
	open, found := c.runs[a.id]

	return found && open.ended == a.ended
}

// attempts lists the attempts this process executes.
func (c *cancels) attempts() []attempt {
	c.mu.Lock()
	defer c.mu.Unlock()

	list := make([]attempt, 0, len(c.runs))
	for id, open := range c.runs {
		list = append(list, attempt{id: id, ended: open.ended})
	}

	return list
}

// endedAttempts lists the attempts whose workflow function has returned.
func (c *cancels) endedAttempts() []attempt {
	c.mu.Lock()
	defer c.mu.Unlock()

	var list []attempt

	for id, open := range c.runs {
		select {
		case <-open.ended:
			list = append(list, attempt{id: id, ended: open.ended})
		default:
		}
	}

	return list
}

// stop calls the cancel function of the run and keeps it listed: a tick drops it after it ended. A run that this process does not execute is left alone.
func (c *cancels) stop(id string) {
	c.mu.Lock()
	defer c.mu.Unlock()

	if open, found := c.runs[id]; found {
		open.cancel()
	}
}

// forget drops each attempt in list that is still the one listed under its run id. A resumed run that replaced an attempt stays listed.
func (c *cancels) forget(list []attempt) {
	c.mu.Lock()
	defer c.mu.Unlock()

	for _, a := range list {
		if c.current(a) {
			delete(c.runs, a.id)
		}
	}
}

// cancel calls the cancel function of each attempt in list that is still the one this process lists under its run id, and forgets those runs.
// An attempt that a resumed run replaced is left alone. It returns the runs it cut, so the caller releases each one once.
func (c *cancels) cancel(list []attempt) []openRun {
	c.mu.Lock()
	defer c.mu.Unlock()

	var cut []openRun

	for _, a := range list {
		if c.current(a) {
			cut = append(cut, c.cut(a.id))
		}
	}

	return cut
}

// cut calls the cancel function of the run and forgets it. The caller holds the lock.
func (c *cancels) cut(id string) openRun {
	open := c.runs[id]
	open.cancel()
	delete(c.runs, id)

	if !open.gate {
		c.markReleasing(attempt{id: id, ended: open.ended})
	}

	return open
}

// track runs body with a context that the tick can cancel. The run stays in the list of local runs until a tick drops it.
func (e *Engine) track(parent dbos.Context, in ItemInput, body func(dbos.Context) (ItemResult, error)) (ItemResult, error) {
	runCtx, cancel := dbos.WithCancel(parent)
	defer cancel()

	if id, err := dbos.GetWorkflowID(parent); err == nil {
		ended := make(chan struct{})
		e.cancels.add(id, openRun{id: id, cancel: cancel, ended: ended, in: in})

		defer close(ended)
	}

	return body(runCtx)
}

// tick lists the canceled runs once for each interval and cancels the ones this process executes. It renews the held claims on the same beat.
// A cancel from another process marks the run in the store only; the tick wakes the owner wait of the local copy.
func (e *Engine) tick(interval time.Duration, stop <-chan struct{}) {
	ticks := e.deps.Clock.Tick
	if ticks == nil {
		ticker := time.NewTicker(interval)
		defer ticker.Stop()

		ticks = ticker.C
	}

	for {
		select {
		case <-stop:
			return
		case <-ticks:
			e.cancelCancelled(stop)
			e.beatClaims()
		}
	}
}

// cancelCancelled cancels each local run that the store marks as canceled. The workflow cannot run its own release then,
// so the engine gives the item back once the run has ended and its agent groups are dead.
// A run stays listed after it ends: DBOS can end a canceled run before the tick lists it.
// The tick drops the runs that had ended before it listed, as the store marks a cancel before the run ends.
func (e *Engine) cancelCancelled(stop <-chan struct{}) {
	ended := e.cancels.endedAttempts()

	canceled, err := e.canceledAttempts(e.cancels.attempts())
	if err != nil {
		e.logger.Warn("cannot list the canceled runs", "error", err)

		return
	}

	cut := e.cancels.cancel(canceled)

	e.cancels.forget(ended)

	e.giveBackCut(cut, stop)
	e.giveBackStrayCancels()
}

// giveBackCut starts the give-back of each canceled run that has one. The give-back waits for the run to end and calls the tracker,
// so it runs beside the tick: a slow release delays no beat and no scan.
func (e *Engine) giveBackCut(cut []openRun, stop <-chan struct{}) {
	for _, open := range cut {
		if open.gate {
			continue
		}

		e.tickRun.Go(func() { e.giveBackWhenEnded(open, stop) })
	}
}

// giveBackStrayCancels gives back each canceled run that no run of this process executes: a run that beehave resume reopened for another build, or one that still waits in the queue.
// Its cancel marker and the claim of its resume stay until an engine gives them back, and the tick is the engine that runs now.
// A run that this process executes or releases is skipped. The release runs beside the tick, as the release of a local run does.
func (e *Engine) giveBackStrayCancels() {
	pending, err := pendingCancels(e.dir, e.logger)
	if err != nil {
		e.logger.Warn("cannot read the canceled runs", "error", err)

		return
	}

	for id, in := range pending {
		if e.cancels.begin(id) {
			e.tickRun.Go(func() { e.giveBackStray(openRun{id: id, in: in}) })
		}
	}
}

// giveBackStray gives back one canceled run. It leaves the marker of a run that is open and not canceled yet, because beehave cancel writes the marker first.
// It drops the marker of a run that ended without a cancel or has a newer run.
func (e *Engine) giveBackStray(open openRun) {
	defer e.cancels.finish(attempt{id: open.id})

	ctx, cancel := ReleaseDeps{Clock: e.deps.Clock}.limit(context.WithoutCancel(e.ctx))
	defer cancel()

	e.sweepOne(ctx, open, false)
}

// canceledAttempts lists which of the local attempts the store marks as canceled. It reads no other run, so the cost of a tick does not grow with the history of the store.
// The store row belongs to the attempt that the caller listed before the read, not to a resume that lists the same run id after it.
func (e *Engine) canceledAttempts(local []attempt) ([]attempt, error) {
	if len(local) == 0 {
		return nil, nil
	}

	marked, err := e.canceledRows(local)
	if err != nil {
		return nil, err
	}

	var canceled []attempt

	for _, a := range local {
		if marked[a.id] {
			canceled = append(canceled, a)
		}
	}

	return canceled, nil
}

// canceledRows reads the run ids among the local attempts that the store marks as canceled.
func (e *Engine) canceledRows(local []attempt) (map[string]bool, error) {
	ids := make([]string, 0, len(local))
	for _, a := range local {
		ids = append(ids, a.id)
	}

	rows, err := dbos.ListWorkflows(e.ctx, dbos.WithFilterStatus(dbos.WorkflowStatusCancelled), dbos.WithFilterWorkflowIDs(ids...))
	if err != nil {
		return nil, err
	}

	marked := make(map[string]bool, len(rows))
	for _, row := range rows {
		marked[row.ID] = true
	}

	return marked, nil
}

// giveBackWhenEnded waits until the workflow function of the run has returned, then gives the item back. A stop of the tick ends the wait.
// A run that is canceled and not released at the stop keeps its cancel marker, and the next start gives it back.
func (e *Engine) giveBackWhenEnded(open openRun, stop <-chan struct{}) {
	defer e.cancels.finish(attempt{id: open.id, ended: open.ended})

	select {
	case <-open.ended:
		e.giveBack(context.WithoutCancel(e.ctx), open)
	case <-stop:
	}
}

// giveBack forgets the agent groups of a canceled run, removes its worktree and releases its claim with the token the run holds now.
func (e *Engine) giveBack(ctx context.Context, open openRun) {
	e.endGroups(open.id)
	e.giveBackClaim(ctx, open)
}

// giveBackClaim removes the worktree of a canceled item and releases its claim. A run that never claimed has nothing to give back.
func (e *Engine) giveBackClaim(ctx context.Context, open openRun) {
	owner, err := currentOwner(e.ctx, e.dir, open.id)
	if err == nil {
		err = ReleaseDeps{Lifecycle: e.deps.Lifecycle, Trees: e.deps.Trees, Clock: e.deps.Clock, Repository: e.deps.Repository}.giveBack(ctx, e.logger, open.in.Project, open.in.Item, owner)
	}

	if err != nil && !errors.Is(err, errNoOwner) {
		e.logger.WarnContext(ctx, "cannot release the canceled item", "item", open.in.Item, "error", err)

		return
	}

	e.forget(open.id)
}
