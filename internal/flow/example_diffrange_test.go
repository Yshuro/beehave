package flow

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// These tests run the review loop of the example and read the diff range that each review prompt names.

// diffRange is the line of the review prompt that names the range from since to head.
func diffRange(since, head string) string {
	return "The diff range of this review is `" + since + "..." + head + "`. Read the diff with `git diff " + since + "..." + head + "` and its commits with `git log " + since + ".." + head + "`."
}

func TestExample_TheReviewPromptReceivesTheDiffRangeOfItsItem(t *testing.T) {
	t.Parallel()

	base, file := exampleRig(t, func(review int) string {
		if review == 1 {
			return "changes"
		}

		return "approve"
	})
	// The rig has one gate result and two commits, so the committing rig must give that one result the head of the fix.
	rig := newCommittingRig(t, base, implementMarker, fixMarker)

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
