package merge_test

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/tobocop2/beehave/internal/merge"
)

func TestClosingReferences_ListsEachClosingKeywordAndIgnoresAPlainNumber(t *testing.T) {
	t.Parallel()

	text := "Fixes #12 and resolves other/repo#3.\nSee #99.\nCloses https://github.com/Owner/Repo/issues/7"

	assert.Equal(t, []string{"#12", "other/repo#3", "owner/repo#7"}, merge.ClosingReferences(text))
	assert.Empty(t, merge.ClosingReferences("Part of #5, no keyword."))
}
