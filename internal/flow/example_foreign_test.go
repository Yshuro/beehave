package flow

import (
	"encoding/json"
	"os"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/tobocop2/beehave/internal/model"
)

// These tests call foreign_followup of the example file directly. No entry of the file reaches it while the engine refuses every foreign pull request,
// so a test adds the entry `followup` to its own copy. The entry passes the value that a foreign review recorded for the pull request 41.
const followupEntry = `

def followup(item):
    return foreign_followup(json.decode(item.recorded))
`

// foreignPull is the pull request 41 of another author: it merged from the branch alice/patch into main. Neither is the item id or the round base.
const foreignPull = 41

// foreignFindings are the two findings that the foreign review recorded as open.
const foreignFindings = `[{"class":"blocking","text":"the retry never stops"},{"class":"should-fix","text":"the log names the token"}]`

// foreignFakes are the fakes of a follow-up test: the forge, the tracker and the preflight reader.
type foreignFakes struct {
	queue     *fakeQueue
	writer    *fakeWriter
	preflight *fakePreflight
}

// foreignRig is the example rig with the entry followup, a forge that holds the pull request 41 in the state `state`, a tracker that files items, and the round base integration.
func foreignRig(t *testing.T, state string) (*agentRig, foreignFakes, string) {
	t.Helper()

	rig, file := exampleRig(t, func(int) string { return "approve" })
	text, err := os.ReadFile(file) //nolint:gosec // the path is the example copy that this test made in its own folder
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(file, append(text, followupEntry...), 0o600)) //nolint:gosec // the example copy of this test

	held := model.PullRequest{Number: foreignPull, State: new(state), HeadRefName: new("alice/patch"), BaseRefName: new("main"), Author: model.Account{Login: "alice"}}
	fakes := foreignFakes{queue: &fakeQueue{held: []model.PullRequest{held}}, writer: newFakeWriter(), preflight: &fakePreflight{}}
	rig.h.queue, rig.h.writer, rig.h.preflight = fakes.queue, fakes.writer, fakes.preflight
	rig.setRound("base", "integration")

	return rig, fakes, file
}

// runFollowup runs the entry followup with the recorded value `recorded` as the item field recorded.
func runFollowup(t *testing.T, rig *agentRig, file, recorded string) ItemResult {
	t.Helper()

	rig.h.items.fields = map[string]string{"recorded": recorded}

	in, err := pinnedInput(testItem, testProject, file)
	require.NoError(t, err)

	in.Entry = "followup"
	result, err := rig.h.engine(t).run(in)
	require.NoError(t, err)

	return result
}

// recordedValue is the value that a foreign review of the pull request 41 records, with the findings `findings` (a JSON list).
func recordedValue(findings string) string {
	return `{"item":"` + testItem + `","head":"","approved":false,"reason":"foreign","pr":41,"findings":` + findings + `}`
}

func TestExampleForeign_TheFollowUpReadsTheRecordedPullRequestAndFilesOneIssue(t *testing.T) {
	t.Parallel()

	// The pull request merged from alice/patch into main: the branch is not the item id 12, and the base is not the round base integration.
	rig, fakes, file := foreignRig(t, "MERGED")

	result := runFollowup(t, rig, file, recordedValue(foreignFindings))

	assert.Equal(t, []int{foreignPull}, fakes.queue.stateAsked, "the step reads the state of the recorded number, once")
	require.Len(t, fakes.writer.created, 1, "one follow-up issue holds every open finding")
	assert.Contains(t, fakes.writer.created[0], "#41")
	assert.Contains(t, fakes.writer.created[0], "the retry never stops")
	assert.Contains(t, fakes.writer.created[0], "the log names the token")
	assert.Empty(t, rig.h.forge.opened, "the follow-up opens no pull request of its own")
	assert.Empty(t, fakes.preflight.asked, "the follow-up does not look the pull request up again")

	var id string

	require.NoError(t, json.Unmarshal(result.Value, &id))
	assert.Equal(t, "n-1", id, "the follow-up returns the id of the issue it filed")
}

func TestExampleForeign_AReviewWithNoOpenFindingsFilesNothing(t *testing.T) {
	t.Parallel()

	rig, fakes, file := foreignRig(t, "MERGED")

	result := runFollowup(t, rig, file, recordedValue("[]"))

	assert.Empty(t, fakes.writer.created, "a review with no open findings files nothing")
	assert.JSONEq(t, `""`, string(result.Value))
}

func TestExampleForeign_APullRequestThatHasNotMergedFilesNothing(t *testing.T) {
	t.Parallel()

	for _, state := range []string{"OPEN", "CLOSED"} {
		t.Run(strings.ToLower(state), func(t *testing.T) {
			t.Parallel()

			rig, fakes, file := foreignRig(t, state)

			runFollowup(t, rig, file, recordedValue(foreignFindings))

			assert.Equal(t, []int{foreignPull}, fakes.queue.stateAsked)
			assert.Empty(t, fakes.writer.created, "only a merge leaves the findings with no pull request to hold them")
		})
	}
}
