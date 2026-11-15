package beadstest

import (
	"context"
	"net"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
)

func TestReachable_GivesUpWhenNothingListensBeforeTheContextEnds(t *testing.T) {
	t.Parallel()

	// Nothing can listen on port 0, so no other test can take it between the pick and the dial.
	address := net.JoinHostPort("127.0.0.1", "0")

	ctx, cancel := context.WithTimeout(t.Context(), time.Millisecond)
	defer cancel()

	assert.False(t, reachable(ctx, address))
}
