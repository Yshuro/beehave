package model_test

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/tobocop2/beehave/internal/model"
)

func TestReservedGateSteps_NamesTheFourStepsOfACandidateJob(t *testing.T) {
	t.Parallel()

	assert.Equal(t, []string{"head-gate", "secret-scan", "base-suite", "revert-run"}, model.ReservedGateSteps())
}
