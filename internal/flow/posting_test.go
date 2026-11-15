package flow

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// closingCalls holds, for each posting step of a pull request, a call with a closing keyword in each text it posts.
var closingCalls = map[string]string{
	"pr_comment":    `pr_comment(text = "Fixes #12", key = "k")`,
	"pr_open title": `pr_open(title = "Closes #12", base = "main")`,
	"pr_open body":  `pr_open(title = "fine", base = "main", body = "a\nResolves #12")`,
}

func TestPosting_AClosingKeywordOutsideTheIntegrationPullRequestIsRefused(t *testing.T) {
	t.Parallel()

	for name, call := range closingCalls {
		h, f := prHarness(t)

		_, err := walkBody(t, h, call+"\n")

		require.ErrorContains(t, err, "Only the integration pull request may close an issue", name)
		require.ErrorContains(t, err, "#12", name)
		assert.Empty(t, f.notes, name)
		assert.Empty(t, f.opened, name)
	}
}

func TestPosting_AClosingKeywordIsPostedForTheIntegrationPullRequestAndAPlainNumberIsPostedAlways(t *testing.T) {
	t.Parallel()

	h, f := prHarness(t)

	_, err := walkBody(t, h, `pr_comment(text = "Fixes #12", key = "a", integration = True)
pr_comment(text = "Part of #12", key = "b")
pr_open(title = "Closes #12", base = "main", integration = True)
`)

	require.NoError(t, err)
	assert.Len(t, f.notes, 2)
	assert.Len(t, f.opened, 1)
}
