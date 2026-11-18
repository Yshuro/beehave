package flow

import (
	"sync"
	"testing"
	"time"

	"github.com/dbos-inc/dbos-transact-golang/dbos"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestWaitOn_AMessageOnATopicTheRunNeverReceivesIsNotAPendingMessage(t *testing.T) {
	t.Parallel()

	harness := newHarness(t)
	harness.agents.gate = make(chan struct{})
	release := sync.OnceFunc(func() { close(harness.agents.gate) })
	t.Cleanup(release)
	harness.flowIs(t, "def main(item):\n    wait(name = \"ask\", question = \"What now?\", timeout = \"1h\")\n    agent(name = \"work\", prompt = \"prompts/work.md\", model = \"m\")\n")
	engine := harness.engine(t)
	client := harness.client(t, testVersion)

	done := runInBackground(engine, testItem)
	waitOn(t, ownerAsksWithADeadline(t, client))

	run := runOfClient(t, client)

	pending, err := run.messagePending()
	require.NoError(t, err)
	assert.False(t, pending, "no message was sent")

	require.NoError(t, dbos.Send(client.flow, testRun, "x", "a topic that no receive of the run reads"))

	pending, err = run.messagePending()
	require.NoError(t, err)
	assert.False(t, pending, "a message on a topic the run never receives does not wake its receive")
	assert.Contains(t, run.parked(time.Now()), "waits in step", "so the run is parked")

	// The run waits on the owner topic, so a message on the voice topic is one that the product receives on and this run does not consume.
	require.NoError(t, dbos.Send(client.flow, testRun, "x", voiceTopic))

	pending, err = run.messagePending()
	require.NoError(t, err)
	assert.True(t, pending, "a message on a topic that the product receives on is pending")

	require.NoError(t, client.Answer(testItem, "go on"))
	release()
	require.NoError(t, receive(t, done).err)
}
