package flow

import (
	"context"
	"fmt"

	"go.starlark.net/starlark"

	"github.com/tobocop2/beehave/internal/workflow"
)

// prHistoryKind is the step kind and the builtin name of pr_history, and pullsField is the field of a result that lists pull requests.
const (
	prHistoryKind = "pr_history"
	pullsField    = "pulls"
)

// prHistory reads the pull requests of the item of the run in every state, with the closing comment of each closed or merged one.
// The read is a recorded step, so a replay returns the list of the first read. A failed read ends the run: no later step works from a guess.
func (s *script) prHistory(b *starlark.Builtin, args starlark.Tuple, kwargs []starlark.Tuple) (starlark.Value, error) { //nolint:ireturn // the builtin contract of starlark-go
	var label string

	if err := workflow.Unpack(b.Name(), args, kwargs, map[string]any{nameKeyword: &label}); err != nil {
		return nil, err
	}

	return s.durable(prHistoryKind, label, func(ctx context.Context, _ string) (stepResult, error) {
		pulls, err := s.e.deps.Forge.History(ctx, s.in.Project, s.in.Item)
		if err != nil {
			return stepResult{}, fmt.Errorf("read the pull requests of item %s: %w", s.in.Item, err)
		}

		return stepResult{Outputs: prHistoryOutputs(pulls)}, nil
	})
}
