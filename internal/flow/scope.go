package flow

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"regexp"
	"slices"
	"strings"

	"github.com/dbos-inc/dbos-transact-golang/dbos"
)

// The recorded steps that keep the scope of a pull request. The first gate of an item fixes the scope. A merge that merges checks it.
const (
	scopeStep      = "scope"
	scopeCheckStep = "scope-check"
)

// errScopeRefused ends an item whose pull request closes an issue that is new since the first review and has no owner line.
var errScopeRefused = errors.New("the pull request closes an issue that is new since its first review")

// keepForm is the one form of an owner line: the word keep, then issue numbers with a leading #, separated by commas.
var keepForm = regexp.MustCompile(`(?i)^\s*keep\s+#\d+(?:\s*,\s*#\d+)*\s*$`)

// issueNumber finds one number in an owner line.
var issueNumber = regexp.MustCompile(`\d+`)

// scopeRead is what one step of the scope records: the closing issues of the pull request, or Found false when the item has no pull request yet.
type scopeRead struct {
	Found  bool  `json:"found"`
	Issues []int `json:"issues"`
}

// fixScope records the issues that the pull request of the item closes at the first review, which is the first gate that finds a pull request.
// A later gate of the run keeps that set and reads nothing. A replay reads the recorded set, so it holds the same scope.
func (s *script) fixScope(label string) error {
	if s.fixed {
		return nil
	}

	read, err := countedStep(s, scopeStep, label, s.firstRead)
	if err != nil {
		return fmt.Errorf("%s of item %s: %w", scopeStep, s.in.Item, err)
	}

	if read.Found {
		s.fixed, s.scope = true, read.Issues
	}

	return nil
}

// firstRead is the body of the scope step. A new run of the item (a restart after a cancel, a send-back round) takes the scope of the newest earlier run that fixed one,
// so the issues of the first review stay the baseline. The forge is read only when no earlier run fixed a scope.
func (s *script) firstRead(ctx context.Context) (scopeRead, error) {
	earlier, err := s.earlierScope()
	if err != nil || earlier.Found {
		return earlier, err
	}

	return s.readClosing(ctx)
}

// earlierScope returns the scope of the newest earlier run of the item that recorded one, or a read with Found false.
func (s *script) earlierScope() (scopeRead, error) {
	ids, err := s.earlierRuns()
	if err != nil {
		return scopeRead{}, err
	}

	for _, id := range ids {
		if read, err := recordedScope(s.e.ctx, id); err != nil || read.Found {
			return read, err
		}
	}

	return scopeRead{}, nil
}

// earlierRuns lists the ids of the other runs of the item, the newest attempt first. It filters on no state and no attempt number, so a canceled, failed, live or newer run is in the list.
// A caller that counts work over these runs counts a concurrent run as spent: that errs on the safe side for a cap.
func (s *script) earlierRuns() ([]string, error) {
	prefix := runPrefix + s.in.Item + attemptMarker

	rows, err := dbos.ListWorkflows(s.e.ctx, dbos.WithFilterWorkflowIDPrefix(prefix), dbos.WithFilterName(itemName))
	if err != nil {
		return nil, fmt.Errorf("list the earlier runs of item %s: %w", s.in.Item, err)
	}

	// The prefix of item 12 also matches a run of item 12-r2, whose tail is not a number: only a numbered id is an attempt of this item.
	attempts := map[string]int{}

	for _, row := range rows {
		if attempt, numbered := attemptOf(row.ID, prefix); numbered && row.ID != workflowID(s.ctx) {
			attempts[row.ID] = attempt
		}
	}

	ids := slices.Collect(maps.Keys(attempts))
	slices.SortFunc(ids, func(a, b string) int { return attempts[b] - attempts[a] })

	return ids, nil
}

// recordedScope returns the scope that the scope step of a run recorded. Found is false when the run recorded none: it never found a pull request.
func recordedScope(ctx dbos.Context, id string) (scopeRead, error) {
	steps, err := dbos.GetWorkflowSteps(ctx, id, dbos.WithStepsLoadOutput(true))
	if err != nil {
		return scopeRead{}, fmt.Errorf("read the steps of %s: %w", id, err)
	}

	for _, step := range steps {
		if read := scopeOutput(step); read.Found {
			return read, nil
		}
	}

	return scopeRead{}, nil
}

// scopeOutput returns what a scope step recorded, or a read with Found false for any other step and for a step that failed.
// The store keeps the output of a step as JSON text.
func scopeOutput(step dbos.StepInfo) scopeRead {
	text, isText := step.Output.(string)
	named := strings.HasPrefix(step.StepName, scopeStep+"#") || strings.HasPrefix(step.StepName, scopeStep+":")

	var read scopeRead
	if !isText || !named || step.Error != nil || json.Unmarshal([]byte(text), &read) != nil {
		return scopeRead{}
	}

	return read
}

// readClosing reads the closing issues of the pull request of the item, sorted and without a repeat.
func (s *script) readClosing(ctx context.Context) (scopeRead, error) {
	issues, found, err := s.e.deps.Forge.ClosingIssues(ctx, s.in.Project, s.in.Item)
	if err != nil {
		return scopeRead{}, fmt.Errorf("read the issues that the pull request of item %s closes: %w", s.in.Item, err)
	}

	return scopeRead{Found: found, Issues: sortedIssues(issues)}, nil
}

// sortedIssues returns the numbers in order, without a repeat. The result is never nil, so a recorded empty set stays a set.
func sortedIssues(numbers []int) []int {
	out := append([]int{}, numbers...)
	slices.Sort(out)

	return slices.Compact(out)
}
