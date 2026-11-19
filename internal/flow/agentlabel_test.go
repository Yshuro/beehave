package flow

import (
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestAgent_AReadOnlyStepRunsOnTheReviewQueue(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name, flow, full, other string
	}{
		{
			"a_read_only_step_waits_on_the_review_queue",
			"def main(item):\n    agent(name = \"work\", prompt = \"prompts/work.md\", model = \"m\", edits = False)\n",
			reviewsQueueName, queueName,
		},
		{
			"a_step_that_edits_waits_on_the_agents_queue",
			"def main(item):\n    agent(name = \"work\", prompt = \"prompts/work.md\", model = \"m\", edits = True)\n",
			queueName, reviewsQueueName,
		},
		{
			"a_label_never_picks_the_queue",
			"def main(item):\n    agent(name = \"work\", prompt = \"prompts/work.md\", model = \"m\", label = \"review\")\n",
			queueName, reviewsQueueName,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			harness := newHarness(t)
			harness.opts.MaxConcurrent, harness.opts.MaxReviewConcurrent = 2, 2
			harness.flowIs(t, test.flow)
			harness.logins = fakeLogins{byItem: map[string]string{"1": "a", "2": "b", "3": "c"}}
			harness.agents.gate = make(chan struct{})
			release := sync.OnceFunc(func() { close(harness.agents.gate) })
			t.Cleanup(release)

			engine := harness.engine(t)
			runs := []<-chan runOutcome{runInBackground(engine, "1"), runInBackground(engine, "2"), runInBackground(engine, "3")}

			waitOn(t, condition(func() bool { return harness.agents.running() == 2 }))
			waitOn(t, condition(func() bool { return enqueuedOn(t, engine, test.full) == 1 }))
			assert.Zero(t, enqueuedOn(t, engine, test.other))
			release()

			for _, run := range runs {
				require.NoError(t, receive(t, run).err)
			}

			assert.Equal(t, 2, harness.agents.maxTotal)
		})
	}
}

func TestAgent_ACallCarriesAFreeLabelIntoTheLedger(t *testing.T) {
	t.Parallel()

	rig := newAgentRig(t)
	rig.reply(`{}`, exited)

	flow := "def main(item):\n" +
		"    agent(model = \"m\", prompt = \"prompts/a.md\", name = \"first\", label = \"fix 2/6\")\n" +
		"    agent(model = \"m\", prompt = \"prompts/a.md\", name = \"look\", label = \"any words at all\", edits = False)\n" +
		"    agent(model = \"m\", prompt = \"prompts/a.md\", name = \"bare\")\n" +
		"    command(run = [\"lint\"], name = \"c\")\n"

	_, err := rig.run(t, flow, "x\n")
	require.NoError(t, err)

	lanes, err := rig.h.client(t, testVersion).Lanes(testItem)
	require.NoError(t, err)

	var got []string
	for _, lane := range lanes.Lanes {
		got = append(got, lane.Label)
	}

	assert.Equal(t, []string{"fix 2/6", "any words at all", ""}, got, "each agent call is a lane, a call with no label has an empty one, and the command is no lane")
}
