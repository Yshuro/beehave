package flow

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/tobocop2/beehave/internal/agent"
)

// These tests run the review loop of the example and read the diff range that each review prompt names.

// diffRange is the line of the review prompt that names the range from since to head.
func diffRange(since, head string) string {
	return "The diff range of this review is `" + since + "..." + head + "`. Read the diff with `git diff " + since + "..." + head + "` and its commits with `git log " + since + ".." + head + "`."
}

// gateTheLastHead makes every gate of the rig judge the head of the last commit, as a real gate judges the head it is given.
func gateTheLastHead(rig *committingRig) {
	inner := rig.h.agents.result
	rig.h.agents.result = func(request AgentRequest) agent.Result {
		result := inner(request)

		rig.mu.Lock()
		defer rig.mu.Unlock()

		if len(rig.heads) > 0 {
			rig.h.gate.mu.Lock()
			for i := range rig.h.gate.results {
				rig.h.gate.results[i].Head = rig.heads[len(rig.heads)-1]
			}
			rig.h.gate.mu.Unlock()
		}

		return result
	}
}

func TestExample_TheReviewPromptReceivesTheDiffRangeOfItsItem(t *testing.T) {
	t.Parallel()

	base, file := exampleRig(t, func(review int) string {
		if review == 1 {
			return "changes"
		}

		return "approve"
	})
	rig := newCommittingRig(t, base, implementMarker, fixMarker)
	gateTheLastHead(rig)

	_, err := runPipeline(rig.h.engine(t), file)
	require.NoError(t, err)
	require.Len(t, rig.heads, 2, "the implement step and one fix committed")

	var prompts []string

	for _, request := range rig.h.agents.requests {
		if strings.Contains(request.Prompt, reviewMarker) {
			prompts = append(prompts, request.Prompt)
		}
	}

	implemented, fixed := rig.heads[0], rig.heads[1]

	require.Len(t, prompts, 3, "the scoped review of the implementer, the scoped review of the fix and the full review")
	assert.Contains(t, prompts[0], diffRange("origin/integration", implemented), "the scoped review of the implementer reads the diff from the base")
	assert.Contains(t, prompts[1], diffRange(implemented, fixed), "the scoped review of the fix reads the diff of the fix alone")
	assert.Contains(t, prompts[2], fullReview)
	assert.Contains(t, prompts[2], diffRange("origin/integration", fixed), "the full review reads the diff of the whole pull request")
}
