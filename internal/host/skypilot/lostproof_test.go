package skypilot

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestHost_ASkyPilotLostJobGivesNoProof(t *testing.T) {
	t.Parallel()

	h, _, sky, ssh := newHost(t, hostConfig(placement()))

	proof, err := h.LostProof(t.Context(), "1")

	require.NoError(t, err, "no proof is not an error")
	assert.Empty(t, proof.Kind)
	assert.Contains(t, proof.Note, "#401", "the note names the issue that adds the proof")
	assert.Empty(t, sky.Calls, "the read sends nothing: a list of the cloud is no proof")
	assert.Empty(t, ssh.Calls)
}
