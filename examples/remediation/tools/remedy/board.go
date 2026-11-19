package remedy

import (
	"context"
	"errors"
	"flag"
	"io"
	"strings"

	"github.com/tobocop2/beehave/internal/forge"
	"github.com/tobocop2/beehave/internal/model"
)

const boardSyntax = "board --board <number> --status <column> (--pr <number> | --issue <number>) [--owner <login>]"

// errNoBoard says that the forge of the repository keeps no project board.
var errNoBoard = errors.New("the forge of this repository keeps no project board")

// boardFlags are the values of the flags of board.
type boardFlags struct {
	board, pr, issue int
	owner, status    string
}

// parseBoard reads the flags of board. It shows the usage and gives false for a bad argument: no board, no status, or not exactly one of --pr and --issue.
func parseBoard(args []string, errOut io.Writer) (boardFlags, bool) {
	var given boardFlags

	flags := flag.NewFlagSet("remedy board", flag.ContinueOnError)
	flags.SetOutput(errOut)
	flags.IntVar(&given.board, "board", 0, "the number of the GitHub Project")
	flags.StringVar(&given.owner, "owner", "", "the login that owns the project; empty for the owner of the repository")
	flags.StringVar(&given.status, "status", "", "the name of the status column")
	flags.IntVar(&given.pr, "pr", 0, "the number of the pull request to put on the board")
	flags.IntVar(&given.issue, "issue", 0, "the number of the issue to put on the board")

	if flags.Parse(args) != nil || flags.NArg() > 0 || given.board <= 0 || strings.TrimSpace(given.status) == "" || (given.pr > 0) == (given.issue > 0) {
		say(errOut, "usage: remedy %s\n", boardSyntax)

		return given, false
	}

	return given, true
}

// card is the card that the flags name.
func (f boardFlags) card() model.Card {
	if f.pr > 0 {
		return model.Card{Board: f.board, Owner: f.owner, Number: f.pr, Pull: true, Status: f.status}
	}

	return model.Card{Board: f.board, Owner: f.owner, Number: f.issue, Status: f.status}
}

// boardCommand puts the card on the project board, sets its status column, and reads the card back. A read-back that shows another status is exit code 2,
// so a board that lost the write, or a column that the board lacks, stops the run instead of passing as done. The same call again changes nothing.
func boardCommand(ctx context.Context, deps Deps, s streams, args []string) int {
	given, ok := parseBoard(args, s.err)
	if !ok {
		return ExitRefused
	}

	repo, err := originRepo(ctx, deps)
	if err != nil {
		return refused(s, "board", err)
	}

	opened, err := deps.Forge(repo)
	if err != nil {
		return refused(s, "board", err)
	}

	boarder, ok := opened.(forge.Boarder)
	if !ok {
		return refused(s, "board", errNoBoard)
	}

	card := given.card()

	if err := boarder.PlaceCard(ctx, card); err != nil {
		return refused(s, "board", err)
	}

	shown, err := boarder.CardStatus(ctx, card)
	if err != nil {
		return refused(s, "board", err)
	}

	if shown != card.Status {
		say(s.err, "remedy board: the board %d shows %q for the card after the write; wanted %q\n", card.Board, shown, card.Status)

		return ExitRefused
	}

	say(s.out, "card %d is on %q of the board %d\n", card.Number, shown, card.Board)

	return ExitPass
}
