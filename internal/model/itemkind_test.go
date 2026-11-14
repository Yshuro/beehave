package model_test

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/tobocop2/beehave/internal/model"
)

func TestItemKind(t *testing.T) {
	t.Parallel()

	tests := map[string]string{
		"2021": "gh", "0": "gh", "proj-a1b.2": "bd", "a-b": "bd", "A_1-x_y.z-9": "bd",
		"": "", "abc": "", "-x": "", "a-": "", "12a": "", "a b-c": "", "a-b c": "", "a/b-c": "", "٣": "", "_a-b": "", "a.b-c": "", "é-b": "",
	}
	for item, want := range tests {
		assert.Equal(t, want, model.ItemKind(item), item)
	}
}

func TestItemKind_ARoundIdHoldsTheSlashesOfItsBase(t *testing.T) {
	t.Parallel()

	for _, item := range []string{"round/main", "round/integration/s1", "round/integration/r1.2_x-y"} {
		assert.Equal(t, model.KindRound, model.ItemKind(item), item)
	}
}

func TestItemKind_AMalformedRoundIdIsNoItem(t *testing.T) {
	t.Parallel()

	for _, item := range []string{"round/", "round//s1", "round/s1/", "round/../x", "round/./x", "round/a b", "round/a:b", "round", "Round/x", "round/-x", "round/a!", "round/a/b b", "round/ab!", "round/é"} {
		assert.Empty(t, model.ItemKind(item), item)
	}
}

func TestRounds_SelectsTheItemsThatTheLabelOrTheListNames(t *testing.T) {
	t.Parallel()

	byLabel := model.Rounds{Base: "b", Label: "beehave-round"}
	byList := model.Rounds{Base: "b", Items: []string{"12"}}
	tagged := model.Item{ID: "1", Labels: []string{"x", "beehave-round"}}

	assert.True(t, byLabel.Selects(tagged))
	assert.False(t, byLabel.Selects(model.Item{ID: "12"}), "an id in no list is not selected by a label round")
	assert.True(t, byList.Selects(model.Item{ID: "12"}))
	assert.False(t, byList.Selects(tagged), "a label does not select in a list round")
	assert.True(t, byLabel.Configured())
	assert.False(t, model.Rounds{}.Configured())
}
