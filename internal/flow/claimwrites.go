package flow

import (
	"context"

	trackerstore "github.com/tobocop2/beehave/internal/store"
)

// forgeWrites are the writes of a step to the pull request of its item.
type forgeWrites interface {
	Comment(ctx context.Context, project, item, key, text string) (number int, err error)
	Open(ctx context.Context, project, item string, req OpenRequest) (OpenResult, error)
}

// holdsClaim returns errClaimLost, with the reason, unless the run of the script holds a trusted claim now.
func (s *script) holdsClaim() error {
	return s.e.claimCheck(workflowID(s.ctx), s.in.Item)
}

// claimedForge is the forge that a step writes through. Each write asks for the claim of the run at the call, so a claim lost since the step began stops the write.
type claimedForge struct {
	forge forgeWrites
	holds func() error
}

func (c claimedForge) Comment(ctx context.Context, project, item, key, text string) (int, error) {
	if err := c.holds(); err != nil {
		return 0, err
	}

	return c.forge.Comment(ctx, project, item, key, text)
}

func (c claimedForge) Open(ctx context.Context, project, item string, req OpenRequest) (OpenResult, error) {
	if err := c.holds(); err != nil {
		return OpenResult{}, err
	}

	return c.forge.Open(ctx, project, item, req)
}

// claimedWriter is the tracker writer that a step writes through. Each write asks for the claim of the run at the call.
type claimedWriter struct {
	writer writer
	holds  func() error
}

func (c claimedWriter) Create(ctx context.Context, project, key string, item trackerstore.NewItem) (id string, created bool, err error) {
	if err := c.holds(); err != nil {
		return "", false, err
	}

	return c.writer.Create(ctx, project, key, item)
}

func (c claimedWriter) SetStatus(ctx context.Context, project, id string, status trackerstore.Status) error {
	if err := c.holds(); err != nil {
		return err
	}

	return c.writer.SetStatus(ctx, project, id, status)
}

func (c claimedWriter) Comment(ctx context.Context, project, id, key, text string) error {
	if err := c.holds(); err != nil {
		return err
	}

	return c.writer.Comment(ctx, project, id, key, text)
}

// forgeWrites is the forge of the run for the writes of a step.
func (s *script) forgeWrites() forgeWrites { //nolint:ireturn // the claimed forge hides the forge of the engine behind its write methods
	return claimedForge{forge: s.e.deps.Forge, holds: s.holdsClaim}
}

// trackerWrites is the tracker writer of the run for the writes of a step.
func (s *script) trackerWrites() writer { //nolint:ireturn // the claimed writer hides the writer of the engine behind its write methods
	return claimedWriter{writer: s.e.deps.Writer, holds: s.holdsClaim}
}
