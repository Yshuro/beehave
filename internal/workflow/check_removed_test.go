package workflow_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestCheck_ABuiltinThatTheAPIRemovedIsRefusedAsAnUndefinedNameAndNamed(t *testing.T) {
	t.Parallel()

	tests := map[string]string{
		"item_assign": `item_assign(id = "1", assignee = "alice")`,
		"pr_assign":   `pr_assign(assignee = "alice", linked = True)`,
	}

	for removed, call := range tests {
		t.Run(removed, func(t *testing.T) {
			t.Parallel()

			got := checkText(t, "def main(item):\n    "+call+"\n")

			require.Len(t, got, 1)
			assert.Contains(t, got[0], "undefined: "+removed)
		})
	}
}
