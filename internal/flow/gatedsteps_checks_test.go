package flow

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestRecordedChecks_ARecordThatIsNotAChecksObjectReadsAsNotComplete(t *testing.T) {
	t.Parallel()

	assert.Equal(t, GateChecks{}, recordedChecks("not a checks object"))
	assert.Equal(t, GateChecks{}, recordedChecks(make(chan int)), "a value that does not encode reads the same")
	assert.Equal(t, GateChecks{New: 2, Complete: true}, recordedChecks(map[string]any{"new": 2, "complete": true}))
}
