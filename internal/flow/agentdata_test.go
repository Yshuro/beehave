package flow

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/tobocop2/beehave/internal/agent"
)

// readFrom is a prompt that names the data file after the words "Read from ".
const readFrom = "Read from {{ .data_file }}\n"

// dataPath returns the path that a rendered prompt names after the words "Read from ".
func dataPath(prompt string) string {
	_, rest, _ := strings.Cut(prompt, "Read from ")
	path, _, _ := strings.Cut(rest, "\n")

	return path
}

// dataSeen is what the fake agent found at the start of each run: the text of the data file that its prompt names, and the mode of the file.
type dataSeen struct {
	mu    sync.Mutex
	texts []string
	modes []os.FileMode
}

// watch makes the fake agent of the rig read the data file that its prompt names when it starts, then end with result.
func (d *dataSeen) watch(rig *agentRig, result func() agent.Result) {
	rig.h.agents.result = func(request AgentRequest) agent.Result {
		path := dataPath(request.Prompt)
		raw, readErr := os.ReadFile(filepath.Clean(path))
		info, statErr := os.Stat(path)

		d.mu.Lock()
		defer d.mu.Unlock()

		if readErr == nil && statErr == nil {
			d.texts = append(d.texts, string(raw))
			d.modes = append(d.modes, info.Mode().Perm())
		}

		return result()
	}
}

func (d *dataSeen) all() ([]string, []os.FileMode) {
	d.mu.Lock()
	defer d.mu.Unlock()

	return append([]string(nil), d.texts...), append([]os.FileMode(nil), d.modes...)
}

const dataValue = `data = {"base_reds": ["lint-a"], "base": "main"}, `

func TestAgent_TheDataKeywordIsWrittenToTheDataFileThatThePromptNames(t *testing.T) {
	t.Parallel()

	rig := newAgentRig(t)
	seen := &dataSeen{}
	seen.watch(rig, func() agent.Result { return exited })

	_, err := rig.run(t, agentFlow(dataValue), readFrom)
	require.NoError(t, err)

	requests := rig.h.agents.seen()
	require.Len(t, requests, 1)

	assert.Equal(t, dataFile(rig.h.opts.StateDir, testItem, aStep), dataPath(requests[0].Prompt), "the prompt names the data file of the item and the step")
	assert.Equal(t, filepath.Join(rig.h.opts.StateDir, inputsFolder), filepath.Dir(dataPath(requests[0].Prompt)), "the file is in the inputs folder of the state folder")
	assert.NotEqual(t, outputFile(rig.h.opts.StateDir, testItem, aStep), requests[0].DataFile, "the data file is not the output file")
	assert.Equal(t, dataPath(requests[0].Prompt), requests[0].DataFile, "the request names the file that the prompt names")

	texts, modes := seen.all()
	assert.Equal(t, []string{`{"base":"main","base_reds":["lint-a"]}`}, texts, "the agent finds the value as JSON when it starts")
	assert.Equal(t, []os.FileMode{0o600}, modes)
}

func TestAgent_ACallWithNoDataGetsNoDataFile(t *testing.T) {
	t.Parallel()

	rig := newAgentRig(t)
	rig.reply("", exited)

	_, err := rig.run(t, agentFlow(""), "Read from [{{ .data_file }}]\n")
	require.NoError(t, err)

	requests := rig.h.agents.seen()
	require.Len(t, requests, 1)
	assert.Contains(t, requests[0].Prompt, "Read from []", "the prompt gets an empty data_file")
	assert.Empty(t, requests[0].DataFile)
	assert.NoFileExists(t, dataFile(rig.h.opts.StateDir, testItem, aStep))
}

func TestAgent_DataThatIsNotADictIsRefused(t *testing.T) {
	t.Parallel()

	rig := newAgentRig(t)
	rig.reply("", exited)

	_, err := rig.run(t, agentFlow(`data = ["lint-a"], `), readFrom)

	require.ErrorContains(t, err, "data must be a dict")
	assert.Empty(t, rig.h.agents.seen())
}

func TestAgent_ADataFileThatCannotBeWrittenFailsThePhaseBeforeTheAgentStarts(t *testing.T) {
	t.Parallel()

	rig := newAgentRig(t)
	rig.reply("", exited)
	engine := rig.h.engine(t)
	require.NoError(t, os.WriteFile(filepath.Join(rig.h.opts.StateDir, inputsFolder), nil, 0o600), "a file where the inputs folder goes")

	_, err := engine.RunFile(testItem, testProject, flowFile(t, agentFlow(dataValue), readFrom))

	require.ErrorContains(t, err, "write the data file")
	assert.Empty(t, rig.h.agents.seen())
}

// The first run of the agent fails, and the data file is gone before the resume. The resumed run computes its state folder elsewhere,
// so a file written from the value of today would land there; the agent finds the recorded bytes at the recorded path.
func TestAgent_AReplayWritesTheDataFileFromTheRecordedValue(t *testing.T) {
	t.Parallel()

	rig := newAgentRig(t)
	seen := &dataSeen{}
	results := []agent.Result{{Outcome: agent.OutcomeExited, ExitCode: 1, Note: "the first run fails"}, exited}

	var mu sync.Mutex

	seen.watch(rig, func() agent.Result {
		mu.Lock()
		defer mu.Unlock()

		next := results[0]
		results = results[1:]

		return next
	})

	file := flowFile(t, agentFlow(dataValue), readFrom)
	first := rig.h.engine(t)

	_, err := first.RunFile(testItem, testProject, file)
	require.Error(t, err, "the first run of the agent fails")

	recorded := dataFile(rig.h.opts.StateDir, testItem, aStep)
	require.NoError(t, os.Remove(recorded))
	require.NoError(t, first.Shutdown())

	next := rig.h.engineWith(t, func(e *Engine) { e.dir = t.TempDir() })

	_, err = resumeAndJoin(t, next, rig.h.client(t, testVersion))
	require.NoError(t, err)

	texts, _ := seen.all()
	assert.Equal(t, []string{`{"base":"main","base_reds":["lint-a"]}`, `{"base":"main","base_reds":["lint-a"]}`}, texts, "each start finds the recorded bytes")

	requests := rig.h.agents.seen()
	require.Len(t, requests, 2)
	assert.Equal(t, recorded, requests[1].DataFile, "the replay writes the path of the record")
	assert.NoFileExists(t, dataFile(next.dir, testItem, aStep), "the replay does not use the path the name function gives today")
}

// A replay whose phase never started builds the phase input from the recall: the data file must be the one that the recorded prompt names, not
// the path that the state folder of the replay gives.
func TestAgent_TheRecallTakesTheDataFileAndTheTextThatTheRenderStepRecorded(t *testing.T) {
	t.Parallel()

	recorded := promptData{File: "/old/state/inputs/7.agent_3Aa_231.json", Text: `{"base":"main"}`}

	raw, err := json.Marshal(recordData(recorded))
	require.NoError(t, err)

	var outputs map[string]any
	require.NoError(t, json.Unmarshal(raw, &outputs), "the replay reads the record as the store gives it back")

	a := agentArgs{file: filepath.Join(t.TempDir(), "outputs", "out.json"), dataFile: "/new/state/inputs/7.agent_3Aa_231.json", record: "{}"}
	require.NoError(t, a.recall(outputs))
	assert.Equal(t, recorded.File, a.dataFile)
	assert.Equal(t, recorded.Text, a.record)

	none := agentArgs{file: a.file, dataFile: "/computed.json", record: "{}"}
	require.NoError(t, none.recall(map[string]any{}))
	assert.Empty(t, none.dataFile, "a record with no data is a call with no data")
	assert.Empty(t, none.record)

	require.ErrorContains(t, a.recall(map[string]any{recordedPromptData: "{"}), "read the recorded data of the step")
}

func TestAgent_TheDataFileGetsMode0600AlsoWhenAnOlderFileHasAnotherMode(t *testing.T) {
	t.Parallel()

	file := filepath.Join(t.TempDir(), "inputs", "7.agent_3Aa_231.json")
	require.NoError(t, os.MkdirAll(filepath.Dir(file), 0o700))
	require.NoError(t, os.WriteFile(file, []byte("old"), 0o600))
	require.NoError(t, os.Chmod(file, 0o200), "an older file that its owner cannot read")

	require.NoError(t, writeData(PhaseInput{DataFile: file, Data: `{"a":1}`}))

	info, err := os.Stat(file)
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0o600), info.Mode().Perm())

	text, err := os.ReadFile(filepath.Clean(file))
	require.NoError(t, err)
	assert.JSONEq(t, `{"a":1}`, string(text))
}
