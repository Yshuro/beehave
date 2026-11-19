package flow

import (
	"context"

	"github.com/tobocop2/beehave/internal/review"
)

// OpenRequest is what the pr_open step asks of the forge. Issue is the issue number of the item, or 0 for none: the forge adds the closing line for it to the body.
// Parent is the id of the item that this one depends on, or empty.
type OpenRequest struct {
	Title  string
	Body   string
	Issue  int
	Base   string
	Parent string
}

// CardRequest is what the board_sync step asks of the forge. Board is the number of the GitHub Project and Owner is its owner (empty for the owner of the repository).
// Issue is the issue to put on the board, or 0 for the pull request of the item. Status is the name of the column.
type CardRequest struct {
	Board  int
	Owner  string
	Issue  int
	Status string
}

// OpenResult is the pull request that Open found or made. Stack is the id of its native stack, or empty when it is in none.
type OpenResult struct {
	Number  int
	Created bool
	Stack   string
}

// forge reads and writes the pull request of an item. A failed read is an error, never an empty standing.
type forge interface {
	Pull(ctx context.Context, project, item string) (review.Standing, error)
	// ClosingIssues reads the issues that merging the pull request of the item closes, as the forge lists them. Found is false when the item has no pull request yet.
	ClosingIssues(ctx context.Context, project, item string) (issues []int, found bool, err error)
	// Comment adds a comment to the pull request of the item and returns its number. The same key on the same pull request adds one comment.
	// An item with no pull request is an error. So is a project with no forge.
	Comment(ctx context.Context, project, item, key, text string) (number int, err error)
	// Open finds the open pull request on the branch of the item, or opens a draft one, and makes its body and stack right. The same call again opens no second pull request.
	// A project with no forge, and a forge that cannot open pull requests, are errors.
	Open(ctx context.Context, project, item string, req OpenRequest) (OpenResult, error)
	// PlaceCard puts the card of the pull request of the item (or of the issue in req) on the board and sets its status column, and returns the number of the pull request or issue.
	// The same call again changes nothing. ReadCard reads the status column of the same card from the board, empty when the card has none.
	// A project with no forge, and a forge that cannot write a board, are errors. Both calls refuse a card with no pull request or issue behind it.
	PlaceCard(ctx context.Context, project, item string, req CardRequest) (number int, err error)
	ReadCard(ctx context.Context, project, item string, req CardRequest) (status string, err error)
}
