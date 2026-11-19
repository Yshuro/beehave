package remedy_test

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/tobocop2/beehave/examples/remediation/tools/remedy"
	"github.com/tobocop2/beehave/internal/model"
)

// boardWorld is a forge with a project board. It keeps the status of each card that was placed, and a lost write leaves the card where it was.
type boardWorld struct {
	forgeWorld
	cards     map[int]string
	placed    []model.Card
	placeErr  error
	loseWrite bool
}

func (w *boardWorld) PlaceCard(_ context.Context, card model.Card) error {
	w.placed = append(w.placed, card)

	if w.placeErr != nil {
		return w.placeErr
	}

	if !w.loseWrite {
		w.cards[card.Number] = card.Status
	}

	return nil
}

func (w *boardWorld) CardStatus(_ context.Context, card model.Card) (string, error) {
	return w.cards[card.Number], nil
}

func boardDeps(forge remedy.Forge) remedy.Deps {
	return remedy.Deps{
		Git:   origin("git@github.com:owner/name.git", nothing),
		Dir:   ".",
		Forge: func(string) (remedy.Forge, error) { return forge, nil },
	}
}

func TestBoard_APullRequestIsPlacedOnTheColumnAndReadBack(t *testing.T) {
	t.Parallel()

	world := &boardWorld{cards: map[int]string{}}

	code, out, errOut := run(t, boardDeps(world), "board", "--board", "3", "--owner", "acme", "--status", "In Progress", "--pr", "12")

	assert.Equal(t, remedy.ExitPass, code)
	assert.Equal(t, "card 12 is on \"In Progress\" of the board 3\n", out)
	assert.Empty(t, errOut)
	assert.Equal(t, []model.Card{{Board: 3, Owner: "acme", Number: 12, Pull: true, Status: "In Progress"}}, world.placed)
}

func TestBoard_AnIssueIsPlacedAsAnIssueCardAndTheSameCallAgainChangesNothing(t *testing.T) {
	t.Parallel()

	world := &boardWorld{cards: map[int]string{}}

	for range 2 {
		code, _, _ := run(t, boardDeps(world), "board", "--board", "3", "--status", "Done", "--issue", "7")
		assert.Equal(t, remedy.ExitPass, code)
	}

	assert.Equal(t, map[int]string{7: "Done"}, world.cards)
	assert.False(t, world.placed[0].Pull)
}

func TestBoard_AReadBackThatShowsAnotherColumnIsARefusal(t *testing.T) {
	t.Parallel()

	world := &boardWorld{cards: map[int]string{12: "Todo"}, loseWrite: true}

	code, out, errOut := run(t, boardDeps(world), "board", "--board", "3", "--status", "Done", "--pr", "12")

	assert.Equal(t, remedy.ExitRefused, code)
	assert.Empty(t, out)
	assert.Contains(t, errOut, `the board 3 shows "Todo" for the card after the write; wanted "Done"`)
}

func TestBoard_AFailedWriteIsARefusalThatNamesTheError(t *testing.T) {
	t.Parallel()

	world := &boardWorld{cards: map[int]string{}, placeErr: errors.New("no column Done")}

	code, _, errOut := run(t, boardDeps(world), "board", "--board", "3", "--status", "Done", "--pr", "12")

	assert.Equal(t, remedy.ExitRefused, code)
	assert.Contains(t, errOut, "no column Done")
}

func TestBoard_AForgeWithNoBoardIsARefusal(t *testing.T) {
	t.Parallel()

	code, _, errOut := run(t, boardDeps(&forgeWorld{}), "board", "--board", "3", "--status", "Done", "--pr", "12")

	assert.Equal(t, remedy.ExitRefused, code)
	assert.Contains(t, errOut, "keeps no project board")
}

func TestBoard_ABadArgumentShowsTheUsage(t *testing.T) {
	t.Parallel()

	for name, args := range map[string][]string{
		"no board":      {"board", "--status", "Done", "--pr", "1"},
		"no status":     {"board", "--board", "3", "--pr", "1"},
		"no card":       {"board", "--board", "3", "--status", "Done"},
		"two cards":     {"board", "--board", "3", "--status", "Done", "--pr", "1", "--issue", "2"},
		"a stray word":  {"board", "--board", "3", "--status", "Done", "--pr", "1", "more"},
		"a blank state": {"board", "--board", "3", "--status", " ", "--pr", "1"},
	} {
		code, _, errOut := run(t, boardDeps(&boardWorld{cards: map[int]string{}}), args...)

		assert.Equal(t, remedy.ExitRefused, code, name)
		assert.Contains(t, errOut, "usage: remedy board", name)
	}
}

func TestBoard_AnOriginThatNamesNoRepositoryAndAForgeThatCannotOpenAreRefusals(t *testing.T) {
	t.Parallel()

	args := []string{"board", "--board", "3", "--status", "Done", "--pr", "1"}

	noOrigin := boardDeps(&boardWorld{})
	noOrigin.Git = origin("", nothing)

	code, _, _ := run(t, noOrigin, args...)
	assert.Equal(t, remedy.ExitRefused, code)

	noForge := boardDeps(&boardWorld{})
	noForge.Forge = func(string) (remedy.Forge, error) { return nil, errors.New("no token") }

	code, _, errOut := run(t, noForge, args...)
	assert.Equal(t, remedy.ExitRefused, code)
	assert.Contains(t, errOut, "no token")
}
