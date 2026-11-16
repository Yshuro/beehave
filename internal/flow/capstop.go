package flow

import (
	"cmp"
	"context"
	"fmt"
	"strconv"
	"time"

	"github.com/dbos-inc/dbos-transact-golang/dbos"

	"github.com/tobocop2/beehave/internal/agent"
)

// The scopes of a cap that stops an agent step.
const (
	scopeRun  = "run"
	scopeTree = "tree"
)

// CapStop is a cap that holds an agent step back. The step waits for the owner to raise the cap, so the run stays open and no step fails.
// The view shows it on the run and on the line of the held step.
type CapStop struct {
	// Scope is the cap that stopped the step: run (agents.max_run_budget_usd) or tree (agents.max_tree_budget_usd).
	Scope string `json:"scope"`
	// Cap is the dollars of the cap that the check read from the config.
	Cap float64 `json:"cap_usd"`
	// Spent is the dollars that the check found spent.
	Spent float64 `json:"spent_usd"`
	// Note says which key to raise.
	Note string `json:"note"`
}

// text is the note of the stop for the human view of the status.
func (c CapStop) text() string {
	return "stopped at the " + c.Scope + " cap $" + dollars(c.Cap) + ", spent $" + dollars(c.Spent)
}

// capCheck is what one check of the caps records: the stop, or nothing when the step may start.
type capCheck struct {
	Stop *CapStop `json:"stop,omitempty"`
}

// capLimit names a cap for the text of its stop.
type capLimit struct {
	scope string
	// subject is what the cap counts, and key the config key that sets it.
	subject, key, env string
	limit             float64
}

// holdForCaps holds an agent step back while the run cap or the tree cap stops it. Each look is a recorded step, and a stop is followed by a durable sleep,
// so the run stays open and a restart replays the earlier looks. The look that follows reads the caps of the config of the engine that runs it,
// so a raised cap plus a restart lets the step start. A look that passed stays a pass on replay.
// The looks of one try go on from look, the number of the first look of this hold, so every run of the try records looks under new names.
// It returns the number of the next look.
func (s *script) holdForCaps(name string, try, look int) (int, error) {
	for ; ; look++ {
		check, err := runStep(s.e, s.ctx, name+"/budget"+strconv.Itoa(try)+"."+strconv.Itoa(look), func(context.Context) (capCheck, error) {
			return s.checkCaps(name)
		})
		if err != nil || check.Stop == nil {
			return look + 1, err
		}

		if err := s.waitForCap(); err != nil {
			return look + 1, err
		}
	}
}

// waitForCap is the durable sleep between two looks at a cap that stopped a step.
func (s *script) waitForCap() error {
	if _, err := dbos.Sleep(s.ctx, cmp.Or(s.e.deps.Clock.CapWait, capWaitFloor)); err != nil {
		return fmt.Errorf("wait for a cap of item %s to be raised: %w", s.in.Item, err)
	}

	return nil
}

// runCap is the cap of one item run, as the config of the engine sets it.
func (e *Engine) runCap() capLimit {
	return capLimit{scopeRun, "one item run", "agents.max_run_budget_usd", "BEEHAVE_AGENTS_MAX_RUN_BUDGET_USD", e.maxRunBudget}
}

// treeCap is the cap of the item tree, as the config of the engine sets it.
func (e *Engine) treeCap() capLimit {
	return capLimit{scopeTree, "the item tree", "agents.max_tree_budget_usd", "BEEHAVE_AGENTS_MAX_TREE_BUDGET_USD", e.maxTreeBudget}
}

// checkCaps reads both caps and the sums they count. A cap of 0 is not read.
func (s *script) checkCaps(name string) (capCheck, error) {
	if run := s.e.runCap(); run.limit > 0 {
		if stop, err := overCap(name, run, s.spent); err != nil || stop != nil {
			return capCheck{Stop: stop}, err
		}
	}

	tree := s.e.treeCap()
	if tree.limit <= 0 {
		return capCheck{}, nil
	}

	spent, err := s.treeSpend()
	if err != nil {
		return capCheck{}, err
	}

	stop, err := overCap(name, tree, spent)

	return capCheck{Stop: stop}, err
}

// overCap is the stop of an agent step that may not start under a cap, or nil when it may. A sum that holds an unknown cost fails the step instead:
// the cap cannot be checked against a cost that is not known.
func overCap(name string, c capLimit, spent agent.Spend) (*CapStop, error) {
	switch {
	case spent.Unreadable:
		return nil, fmt.Errorf("the agent step %s does not start: the cap for %s is $%s, but a recorded cost of an earlier agent step cannot be read, "+
			"so the cap cannot be checked. Repair or remove that record, or set %s (or %s) to 0 for no cap", name, c.subject, dollars(c.limit), c.key, c.env)
	case spent.Unknown:
		return nil, fmt.Errorf("the agent step %s does not start: the cap for %s is $%s, but the cost is unknown for an earlier agent step "+
			"(its model has no price), so the cap cannot be checked. "+
			"Add a price for the model, or set %s (or %s) to 0 for no cap", name, c.subject, dollars(c.limit), c.key, c.env)
	case spent.Cost < c.limit:
		return nil, nil //nolint:nilnil // no stop is the answer: the step may start
	}

	return &CapStop{Scope: c.scope, Cap: c.limit, Spent: spent.Cost, Note: fmt.Sprintf("the agent step %s waits: the cap for %s is $%s and the agent steps of the item %s spent $%s. "+
		"The cap is checked when a step starts, so the last step can pass it by its own cost. "+
		"Raise %s (or %s) and restart beehave, or set it to 0 for no cap", name, c.subject, dollars(c.limit), c.scope, dollars(spent.Cost), c.key, c.env)}, nil
}

// treeSpend sums the recorded cost of the root run of the item tree and of every run below it. It reads the step records of each run, so a resume keeps the sum.
func (s *script) treeSpend() (agent.Spend, error) {
	root, err := s.e.treeRoot(s.ctx, s.in.Item)
	if err != nil {
		return agent.Spend{}, err
	}

	return runAndBelowSpend(s.e.ctx, root)
}

// treeRoot is the root run of the item tree of the run of ctx.
func (e *Engine) treeRoot(ctx dbos.Context, item string) (string, error) {
	id, err := dbos.GetWorkflowID(ctx)
	if err != nil {
		return "", fmt.Errorf("read the id of the run of item %s: %w", item, err)
	}

	// The reads go through the context of the engine, not the one of the run: a library call on the context of a run records a step of its own.
	return rootRun(e.ctx, id)
}

// runAndBelowSpend sums the cost of a run and of every item run below it.
func runAndBelowSpend(ctx dbos.Client, id string) (agent.Spend, error) {
	var spent agent.Spend

	err := walkTree(ctx, id, dbos.WithStepsLoadOutput(true), func(records []dbos.StepInfo) {
		for _, line := range stepLines(records, "", false, time.Time{}) {
			spent = spent.Plus(line.Spend)
		}
	})

	return spent, err
}

// walkTree passes the step records of a run, and then of every item run below it, to fold. It is the one walk of an item tree:
// the sum of the tree cap and the count of the host losses both fold over it. load says whether the records hold the outputs of the steps.
// A read that fails ends the walk with its error.
func walkTree(ctx dbos.Client, id string, load dbos.GetWorkflowStepsOption, fold func([]dbos.StepInfo)) error {
	records, err := dbos.GetWorkflowSteps(ctx, id, load)
	if err != nil {
		return fmt.Errorf("read the steps of %s: %w", id, err)
	}

	fold(records)

	children, err := itemRuns(ctx, dbos.WithFilterParentWorkflowID(id))
	if err != nil {
		return err
	}

	for _, child := range children {
		if err := walkTree(ctx, child.ID, load, fold); err != nil {
			return err
		}
	}

	return nil
}

// itemRuns lists the item runs that match the filter.
func itemRuns(ctx dbos.Client, filter dbos.ListWorkflowsOption) ([]dbos.WorkflowStatus, error) {
	rows, err := dbos.ListWorkflows(ctx, filter, dbos.WithFilterName(itemName))
	if err != nil {
		return nil, fmt.Errorf("list the item runs: %w", err)
	}

	return rows, nil
}

// rootRun climbs from a run to the highest item run above it. A run that no run spawned is its own root.
func rootRun(ctx dbos.Client, id string) (string, error) {
	for {
		rows, err := itemRuns(ctx, dbos.WithFilterWorkflowIDs(id))
		if err != nil || len(rows) == 0 || rows[0].ParentWorkflowID == "" {
			return id, err
		}

		above, err := itemRuns(ctx, dbos.WithFilterWorkflowIDs(rows[0].ParentWorkflowID))
		if err != nil || len(above) == 0 {
			return id, err
		}

		id = above[0].ID
	}
}
