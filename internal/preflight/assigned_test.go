package preflight_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/tobocop2/beehave/internal/preflight"
)

var own = []string{"owner"}

func TestPreflight_AMaintainerClaimOnTheItemStopsBesideClaimed(t *testing.T) {
	t.Parallel()

	p := open()
	p.author = "owner"
	p.assigned = []string{"maintainer"}

	f := p.fake(t)
	result := check(t, f, preflight.Request{PR: 1, Own: own})

	assert.Equal(t, preflight.Assigned, result.Stop)
	assert.Equal(t, 11, result.Stop.Code())
	assert.Equal(t, "ASSIGNED", result.Stop.String())
	require.Len(t, result.Lines, 2)
	assert.Contains(t, result.Lines[0], "'maintainer'")
	assert.Contains(t, result.Lines[1], "Recovery:")
	assert.Empty(t, f.asked, "a holder on the item stops before any linked read")

	mine := open()
	mine.author, mine.assigned = "owner", []string{"Owner"}
	assert.True(t, check(t, mine.fake(t), preflight.Request{PR: 1, Own: own}).OK(), "an own login holds the item")

	unchecked := check(t, p.fake(t), preflight.Request{PR: 1})
	assert.True(t, unchecked.OK(), "an empty own list checks no assignee")
}

func TestPreflight_AForeignAuthorWhoHoldsNothingStaysClaimed(t *testing.T) {
	t.Parallel()

	p := open()
	p.author = "someone"

	assert.Equal(t, preflight.Claimed, check(t, p.fake(t), preflight.Request{PR: 1, Own: own}).Stop)

	p.assigned = []string{"someone"}
	assert.Equal(t, preflight.Assigned, check(t, p.fake(t), preflight.Request{PR: 1, Own: own}).Stop)
}

func TestPreflight_AClaimOnALinkedIssueStopsTheItem(t *testing.T) {
	t.Parallel()

	p := open()
	p.author = "owner"
	p.body = "Closes #7\nPart of #8"

	f := p.fake(t)
	f.issues["#8"] = []string{"maintainer"}

	result := check(t, f, preflight.Request{PR: 1, Own: own})

	assert.Equal(t, preflight.Assigned, result.Stop)
	assert.Contains(t, result.Lines[0], "the linked issue #8")
	assert.Equal(t, []string{"#7", "#8"}, f.asked, "each link is read once, in the order of the body")

	f.issues["#8"] = []string{"owner"}
	assert.True(t, check(t, f, preflight.Request{PR: 1, Own: own}).OK())
}

func TestPreflight_EveryLinkKeywordIsReadOnceEach(t *testing.T) {
	t.Parallel()

	p := open()
	p.author = "owner"
	p.body = "Closes #1, fixes #2. Resolves: #3\nPart of #4 and Refs #5, related #6, Related to #7. Closes #1 again. see #9. Fix #10. prefixes #11, subrefs #12"

	f := p.fake(t)
	check(t, f, preflight.Request{PR: 1, Own: own})

	assert.Equal(t, []string{"#1", "#2", "#3", "#4", "#5", "#6", "#7"}, f.asked)
}

func TestPreflight_ALinkToAnotherRepositoryIsReadOnThatRepository(t *testing.T) {
	t.Parallel()

	p := open()
	p.author = "owner"
	p.body = "Fixes acme/tools#12\nRefs https://github.com/acme/site/issues/3"

	f := p.fake(t)
	f.issues["acme/site#3"] = []string{"maintainer"}

	result := check(t, f, preflight.Request{PR: 1, Own: own})

	assert.Equal(t, []string{"acme/tools#12", "acme/site#3"}, f.asked)
	assert.Equal(t, preflight.Assigned, result.Stop)
	assert.Contains(t, result.Lines[0], "acme/site#3")
}

func TestPreflight_AFailedReadOfALinkedIssueIsNotClean(t *testing.T) {
	t.Parallel()

	p := open()
	p.author = "owner"
	p.body = "Closes #7"

	f := p.fake(t)
	f.unread = map[string]bool{"#7": true}

	_, err := preflight.Check(context.Background(), f, preflight.Request{PR: 1, Own: own}, "hold")

	require.ErrorIs(t, err, errDown)
	assert.Equal(t, "cannot read the assignees of #7, linked from #1. Check the access to that repository, then run again.", err.Error())
}

func TestPreflight_ALinkNumberThatOverflowsIsAReadErrorNamingTheLink(t *testing.T) {
	t.Parallel()

	p := open()
	p.author = "owner"
	p.body = "Closes #99999999999999999999"

	f := p.fake(t)

	_, err := preflight.Check(context.Background(), f, preflight.Request{PR: 1, Own: own}, "hold")

	var read *preflight.ReadError

	require.ErrorAs(t, err, &read)
	assert.Equal(t, "#99999999999999999999", read.Link)
	assert.Contains(t, err.Error(), "#99999999999999999999")
	assert.Empty(t, f.asked, "no link is read when one cannot be parsed")
}

func TestPreflight_AnAssigneeListThatIsMissingIsNotClean(t *testing.T) {
	t.Parallel()

	f := &fake{view: `{"state":"OPEN","isDraft":false,"headRefName":"feature","labels":[]}`}

	_, err := preflight.Check(context.Background(), f, preflight.Request{PR: 1, Own: own}, "hold")

	var read *preflight.ReadError

	require.ErrorAs(t, err, &read)
	assert.Equal(t, "cannot read #1", err.Error())
}

func TestPreflight_AHolderWhoKnowsTheItemIsClassifiedKnows(t *testing.T) {
	t.Parallel()

	tests := map[string]struct {
		author    string
		reviewers []string
		requested []string
		holder    string
		knows     string
	}{
		"the_holder_opened_it":                {"Maintainer", nil, nil, "maintainer", "knows=yes"},
		"the_holder_reviews_it":               {"owner", []string{"other", "MAINTAINER"}, nil, "maintainer", "knows=yes"},
		"the_holder_was_only_asked_to_review": {"owner", nil, []string{"other", "MAINTAINER"}, "maintainer", "knows=yes"},
		"the_holder_is_a_stranger":            {"owner", []string{"other"}, []string{"third"}, "maintainer", "knows=no"},
		"nobody_reviews":                      {"owner", nil, nil, "maintainer", "knows=no"},
	}

	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			p := open()
			p.author, p.reviewers, p.requested, p.body = tt.author, tt.reviewers, tt.requested, "Closes #7"

			f := p.fake(t)
			f.issues["#7"] = []string{tt.holder}

			result := check(t, f, preflight.Request{PR: 1, Own: own})

			require.Equal(t, preflight.Assigned, result.Stop)
			assert.Contains(t, result.Lines[0], tt.knows)
		})
	}
}
