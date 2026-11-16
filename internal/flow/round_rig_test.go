package flow

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/tobocop2/beehave/internal/agent"
	"github.com/tobocop2/beehave/internal/model"
	"github.com/tobocop2/beehave/internal/review"
)

var errNoWorktree = errors.New("the rig could not make the worktree of an item")

// barrierLimit is the longest a child waits at a barrier for the other members. A member that never arrives is a mistake of the test.
const barrierLimit = 30 * time.Second

// readsBeforeReview is the number of the read of the pull request that comes just before the review of a child: the intake is the first read.
const readsBeforeReview = 2

// barrier holds each member until every member has arrived. The children of a round pass the barrier together, so they end in the same poll of gather and the round lands them as one batch.
// A caller that is not a member passes at once. onOpen runs once, when the last member arrives and before any member passes.
type barrier struct {
	stalled func(item string) // called when a member waited the whole limit

	mu      sync.Mutex
	members []string
	arrived map[string]bool
	opened  bool
	open    chan struct{}
	onOpen  []func()
}

func newBarrier(members []string, stalled func(item string)) *barrier {
	return &barrier{members: members, stalled: stalled, arrived: map[string]bool{}, open: make(chan struct{})}
}

// check opens the barrier when every member has arrived. The caller holds the lock.
func (b *barrier) check() {
	if b.opened || len(b.arrived) < len(b.members) {
		return
	}

	b.opened = true

	for _, hook := range b.onOpen {
		hook()
	}

	close(b.open)
}

// arrive records the item as arrived and returns at once. It reports whether the item is a member.
func (b *barrier) arrive(item string) bool {
	b.mu.Lock()
	defer b.mu.Unlock()

	if !slices.Contains(b.members, item) {
		return false
	}

	b.arrived[item] = true
	b.check()

	return true
}

func (b *barrier) wait(ctx context.Context, item string) {
	if !b.arrive(item) {
		return
	}

	select {
	case <-b.open:
	case <-ctx.Done():
	case <-time.After(barrierLimit):
		b.stalled(item)
	}
}

// children is what the round rig knows about the child runs: the head of the branch of each item, the items that reached the gate and the review in order,
// and the two barriers that line the children of a batch up.
type children struct {
	heads  map[string]string // the head of each item; an item with no branch of its own is at the tip of the base
	base   string
	gate   *barrier
	review *barrier

	mu       sync.Mutex
	gated    []string
	reviewed []string
	readAt   []string // the items that were reviewed at the first read of the integration pull request
}

func (c *children) head(item string) string {
	return cmp.Or(c.heads[item], c.base)
}

// started returns the items whose child reached the gate, in sorted order.
func (c *children) started() []string {
	c.mu.Lock()
	defer c.mu.Unlock()

	items := slices.Clone(c.gated)
	slices.Sort(items)

	return items
}

// atIntegration records the items that are reviewed now, the first time the round reads the integration pull request.
func (c *children) atIntegration() {
	c.mu.Lock()
	defer c.mu.Unlock()

	if c.readAt == nil {
		c.readAt = slices.Clone(c.reviewed)
	}
}

// reviewedAtIntegration returns the items that were reviewed when the round first read the integration pull request.
func (c *children) reviewedAtIntegration() []string {
	c.mu.Lock()
	defer c.mu.Unlock()

	return slices.Clone(c.readAt)
}

// itemTrees gives each item its own worktree of the repository, at the head of its branch.
type itemTrees struct {
	repo repoGit
	kids *children
	root string

	mu   sync.Mutex
	dirs map[string]string
}

// tree makes the worktree of the item on the first call and returns its folder.
func (t *itemTrees) tree(ctx context.Context, item string) (string, error) {
	t.mu.Lock()
	defer t.mu.Unlock()

	if dir, made := t.dirs[item]; made {
		return dir, nil
	}

	dir := filepath.Join(t.root, "tree-"+item)
	if res := t.repo.Run(ctx, []string{"worktree", "add", "-q", "--detach", dir, t.kids.head(item)}); res.ExitCode != 0 {
		return "", fmt.Errorf("%w: item %s: %s", errNoWorktree, item, res.Stderr)
	}

	t.dirs[item] = dir

	return dir, nil
}

// afterReview makes the fake agents of the rig note the end of each review. The agent queue runs one agent for each login, so a review must not wait for another child:
// the barrier of the reviews only counts, and its hooks run when the last member has reviewed. The review is the last agent of an approved child.
func (c *children) afterReview(inner func(AgentRequest) agent.Result) func(AgentRequest) agent.Result {
	return func(request AgentRequest) agent.Result {
		result := inner(request)

		if strings.Contains(request.Prompt, reviewMarker) {
			c.mu.Lock()
			c.reviewed = append(c.reviewed, request.Item)
			c.mu.Unlock()

			c.review.arrive(request.Item)
		}

		return result
	}
}

// servedOnce shows each item of the queue to one read and then drops it, as the tracker does once the round lands the item.
type servedOnce struct {
	*fakeQueue

	seenMu sync.Mutex
	seen   map[string]bool
}

func (s *servedOnce) OpenItems(ctx context.Context, project string) ([]model.Item, error) {
	all, err := s.fakeQueue.OpenItems(ctx, project)

	s.seenMu.Lock()
	defer s.seenMu.Unlock()

	var fresh []model.Item

	for _, item := range all {
		if !s.seen[item.ID] {
			s.seen[item.ID] = true
			fresh = append(fresh, item)
		}
	}

	return fresh, err
}

// roundRig runs the round of the example (the entry main) over the batch rig. The queue lists the items once. Each item gets a child that approves
// the head the repository holds for it. The batch merger lands the approved heads.
type roundRig struct {
	*batchRig

	file    string
	kids    *children
	writer  *fakeWriter
	remote  *remoteBranches
	hosts   *fakeHosts
	q       *fakeQueue
	last    *Engine // the engine of the last run
	project model.Project
	answer  string   // the answer to the first wait of the round, STOP when empty
	answers []string // the answers to the waits in order, which win over answer
	// deadlines is the deadline of each wait that run answered, as the status showed it, in order.
	deadlines []time.Time
	// forge, when set, is the merger of the run in place of the land merger of the batch rig: the real merge over a forge fake.
	forge merger
}

// setRound sets the round setting `key` of the project of the rig before the run starts. A nil value removes the key.
func (r *roundRig) setRound(key string, value any) {
	setRoundOf(&r.project, key, value)
}

// newRoundRig makes a round over the items of the string. The round branch is main and the owner is shipOwner.
// A round runs the file of the example as it is. Each child runs the real pipeline over its own worktree, with fake agents that approve every item.
// An item that has a branch in the repository (1, 2 and 3) is at the head of that branch, and any other item is at the tip of main.
func newRoundRig(t *testing.T, items string) *roundRig {
	t.Helper()

	return newRoundRigOf(t, items)
}

// itemGate is the gate of a child run: it passes at the head of the branch of the item. The gate of the batch (the run of the round) is the gate of the batch rig.
type itemGate struct {
	*treeGate

	kids *children
}

func (g itemGate) Run(ctx context.Context, project, item, host string, job GateJob) (GateResult, error) {
	if item == testItem {
		return g.treeGate.Run(ctx, project, item, host, job)
	}

	g.kids.mu.Lock()
	g.kids.gated = append(g.kids.gated, item)
	g.kids.mu.Unlock()

	return GateResult{Passed: true, Head: g.kids.head(item), Tree: exampleTree}, nil
}

// afterReviews runs the hook once, when the review of every member of the batch has ended. No child reads a branch after its review. It is the moment after the review: a push to a branch, a branch that is gone.
func (r *roundRig) afterReviews(hook func()) {
	r.kids.review.mu.Lock()
	defer r.kids.review.mu.Unlock()

	r.kids.review.onOpen = append(r.kids.review.onOpen, hook)
}

// inLandPhase reports whether the round is landing the batch. The children never fetch, so the first fetch is the first check of a member of the batch.
// Call it from a reply of the command fake only: the reply runs under the lock of the fake, which this reads without taking it.
func (r *roundRig) inLandPhase() bool {
	return slices.ContainsFunc(r.h.commands.requests, func(request CommandRequest) bool {
		return len(request.Run) > 1 && request.Run[0] == "git" && request.Run[1] == "fetch"
	})
}

// pushAfterReviews moves the branch of the item on the remote to head after the reviews, and a head of "" deletes the branch.
func (r *roundRig) pushAfterReviews(item, head string) {
	r.afterReviews(func() {
		r.remote.mu.Lock()
		defer r.remote.mu.Unlock()

		if head == "" {
			delete(r.remote.heads, item)

			return
		}

		r.remote.heads[item] = head
	})
}

// engine is the engine of the batch rig, with the gate of a child run in front of the gate of the batch.
func (r *roundRig) engine(t *testing.T) *Engine {
	t.Helper()

	return r.engineWith(t, func(*Deps) {})
}

// engineWith is engine with the change made to its collaborators, such as a clock of the test.
func (r *roundRig) engineWith(t *testing.T, change func(*Deps)) *Engine {
	t.Helper()

	deps := r.deps()
	deps.Gate = itemGate{treeGate: r.gate, kids: r.kids}

	if r.forge != nil {
		deps.Merge = r.forge
	}

	change(&deps)

	engine, err := NewEngine(t.Context(), r.h.opts, deps)
	require.NoError(t, err)
	require.NoError(t, engine.Launch())
	t.Cleanup(func() { require.NoError(t, engine.Shutdown()) })

	return engine
}

// holdBeforeReview returns the hook of a read of the pull request that holds the second read of each member at the barrier of the batch.
func (c *children) holdBeforeReview() func(ctx context.Context, item string) {
	var mu sync.Mutex

	reads := map[string]int{}

	return func(ctx context.Context, item string) {
		mu.Lock()
		reads[item]++
		before := reads[item] == readsBeforeReview
		mu.Unlock()

		if before {
			c.gate.wait(ctx, item)
		}
	}
}

// loginsOf gives each item its own login. The agent queue runs one agent for each login, so the reviews of the members run at the same time.
func loginsOf(items []string) map[string]string {
	logins := map[string]string{}
	for _, id := range items {
		logins[id] = "login-" + id
	}

	return logins
}

// listOf is the list setting of the words of the string, as the project vars hold a list.
func listOf(words string) []any {
	list := []any{}
	for _, word := range strings.Fields(words) {
		list = append(list, word)
	}

	return list
}

func newRoundRigOf(t *testing.T, items string) *roundRig {
	t.Helper()

	rig := newBatchRig(t)
	folder := t.TempDir()
	require.NoError(t, os.CopyFS(folder, os.DirFS(filepath.Join("..", "..", "examples", "remediation"))))

	file := filepath.Join(folder, "workflow.star")
	queued := strings.Fields(items)

	// The members of the barriers are the queued items that have a branch of their own: the items that the round lands as one batch.
	members := slices.DeleteFunc(slices.Clone(queued), func(id string) bool { return rig.heads[id] == "" })
	stalled := func(item string) {
		t.Errorf("the child of item %s waited for the other members of its batch: an item of the batch never reached the barrier", item)
	}
	kids := &children{heads: rig.heads, base: strings.TrimSpace(rig.repo.must(t, "rev-parse", "main")), gate: newBarrier(slices.Clone(members), stalled), review: newBarrier(slices.Clone(members), stalled)}
	rig.h.logins = fakeLogins{login: testLogin, byItem: loginsOf(members)}
	trees := &itemTrees{repo: rig.repo, kids: kids, root: t.TempDir(), dirs: map[string]string{}}
	rig.h.trees = &fakeTrees{per: trees.tree}
	rig.h.forge.state = review.Standing{Number: testPull, Head: kids.base}
	rig.h.forge.heads = rig.heads

	agents := &agentRig{h: rig.h}
	agents.answerLikeTheExample(exampleSkill(t, folder), func(int) string { return "approve" }, func(int) string { return "[]" }, func(int) string { return "" }, "", designReviewLimit)
	rig.h.agents.result = kids.afterReview(rig.h.agents.result)

	// A round over items selects them by the item list, so the queue rows need no label. A round over no item keeps the label of the example rig and reads an empty queue.
	r := &roundRig{batchRig: rig, file: file, kids: kids, project: exampleProject()}
	r.setRound("base", "main")
	r.setRound("owner", shipOwner)
	r.setRound("owner_email", shipMail)
	r.setRound("merge", true)

	if len(queued) > 0 {
		r.setRound("label", nil)
		r.setRound("items", listOf(items))
	}
	rig.h.facts = func(string) model.Project { return r.project }

	// Each child runs the mutation step of the example after its green gate, in the one slot of the class mutation. The command fake runs it clean.
	rig.h.mutation = exampleMutationCommand
	rig.h.slots = func(_, _, class string) (SlotGrant, error) {
		return SlotGrant{Host: "box", Count: map[string]int{"mutation": 1}[class]}, nil
	}

	var rows []model.Item
	for _, id := range queued {
		rows = append(rows, queueItem(id, model.SeverityP1))
	}

	// Each child reads its own item: the id is the id of the item.
	rig.h.items.byItem = map[string]map[string]string{}
	for _, id := range queued {
		rig.h.items.byItem[id] = map[string]string{"id": id, "title": "a title", "body": "a body"}
	}

	r.q = &fakeQueue{items: rows}
	rig.h.queue = &servedOnce{fakeQueue: r.q, seen: map[string]bool{}}
	rig.h.reader = &fakeReader{}

	// The round starts the host once and stops it in finish.
	r.hosts = &fakeHosts{host: &stubHost{}}
	rig.h.hosts = r.hosts

	// The barrier of the children is the second read of the pull request of an item: the read before the review (the first is the intake).
	// A read holds no slot of the exclusive queue or of the agent queue, so the children wait there without blocking each other. Each member has its own login,
	// so the reviews of the members run at the same time and end together, in one poll of gather.
	r.remote = &remoteBranches{heads: map[string]string{}, before: kids.holdBeforeReview()}

	for _, id := range queued {
		r.remote.heads[id] = kids.head(id)
	}

	for item, head := range rig.heads {
		r.remote.heads[item] = head
	}

	rig.h.preflight = r.remote

	r.writer = newFakeWriter()
	rig.h.writer = r.writer

	rig.h.commands.reply = func(request CommandRequest) (CommandOutput, error) {
		if len(request.Run) > 2 && request.Run[1] == "integration" {
			kids.atIntegration()

			return CommandOutput{Stdout: "pr=9 state=OPEN\n"}, nil
		}

		return CommandOutput{}, nil
	}

	return r
}

// run runs the round to its first wait for the owner, answers STOP, and returns the result. A round that stops with an error before the wait returns that error.
func (r *roundRig) run(t *testing.T) (ItemResult, error) {
	t.Helper()

	engine := r.engine(t)
	r.last = engine
	done := make(chan runOutcome, 1)

	go func() {
		result, err := engine.RunFile(testItem, testProject, r.file)
		done <- runOutcome{result: result, err: err}
	}()

	client := r.h.client(t, testVersion)
	owner := &answering{}
	deadline := time.After(eventTimeout)

	for {
		select {
		case out := <-done:
			return out.result, out.err
		case <-deadline:
			require.FailNow(t, "the round neither ended nor asked the owner")
		case <-time.After(pollEvery):
			r.answerOnce(t, client, owner)
		}
	}
}

// answering is what the loop of run knows about the waits that it answered: how many, and whether the last one still shows in the status.
type answering struct {
	given int
	open  bool
}

// answerOnce reads the status and answers a question that shows and that the loop has not answered yet.
func (r *roundRig) answerOnce(t *testing.T, client *Client, owner *answering) {
	t.Helper()

	infos, err := client.Status()
	if err != nil {
		return
	}

	at := slices.IndexFunc(infos, func(info RunInfo) bool { return info.Question != "" })
	if at < 0 {
		owner.open = false

		return
	}

	if owner.open {
		return
	}

	r.deadlines = append(r.deadlines, infos[at].Deadline)

	require.NoError(t, client.AnswerAs(testItem, shipOwner, r.answerFor(owner.given)))

	owner.given++
	owner.open = true
}

// answerFor is the answer to the nth wait of the round: the nth of answers, then answer for the first wait, and STOP for the rest.
func (r *roundRig) answerFor(n int) string {
	if n < len(r.answers) {
		return r.answers[n]
	}

	if n == 0 {
		return cmp.Or(r.answer, "STOP")
	}

	return "STOP"
}
