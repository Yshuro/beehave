package flow

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/tobocop2/beehave/internal/agent"
	"github.com/tobocop2/beehave/internal/exectest"
)

// linkedState makes a real state folder and a link to it, sets the link as the state folder of the rig, and returns the real folder.
func linkedState(t *testing.T, rig *agentRig) string {
	t.Helper()

	root, err := filepath.EvalSymlinks(t.TempDir())
	require.NoError(t, err)

	real := filepath.Join(root, "real-state")
	require.NoError(t, os.MkdirAll(real, 0o700))
	require.NoError(t, os.Symlink(real, filepath.Join(root, "link-state")))

	rig.h.opts.StateDir = filepath.Join(root, "link-state")

	return real
}

func TestAgentStep_AStateFolderThatIsALinkGivesTheAgentTheRealPathOfTheOutputFile(t *testing.T) {
	t.Parallel()

	rig := newAgentRig(t)
	target := linkedState(t, rig)
	rig.reply(`{"verdict":"approve"}`, exited)

	_, err := rig.run(t, outputsFlow("1"), "Write to {{ .output_file }}\n")
	require.NoError(t, err)

	want := filepath.Join(target, outputsFolder, "12.agent_3Aa_231.json")
	requests := rig.h.agents.seen()
	require.Len(t, requests, 1)
	assert.Equal(t, want, requests[0].OutputFile, "the request holds the real path, so the sandbox grant opens the file")
	assert.Equal(t, want, outputPath(requests[0].Prompt), "the prompt names the real path")
}

func TestAgentStep_ARefusedWriteIsNamedWhenTheStateFolderIsALink(t *testing.T) {
	t.Parallel()

	rig := newAgentRig(t)
	target := linkedState(t, rig)
	rig.h.agents.result = func(AgentRequest) agent.Result {
		denied := exited
		denied.Denials = []agent.Denial{{Tool: "Write", Target: filepath.Join(target, outputsFolder, "12.agent_3Aa_231.json")}}

		return denied
	}

	_, err := rig.run(t, outputsFlow("1"), "Write to {{ .output_file }}\n")

	require.ErrorContains(t, err, "the permission check of the agent CLI refused the Write call that writes it")
}

func TestAgentStep_TheOutputFileIsEmptyWhenTheAgentStarts(t *testing.T) {
	t.Parallel()

	rig := newAgentRig(t)
	require.NoError(t, os.MkdirAll(filepath.Dir(outputFile(rig.h.opts.StateDir, testItem, aStep)), 0o700))
	require.NoError(t, os.WriteFile(outputFile(rig.h.opts.StateDir, testItem, aStep), []byte(`{"verdict":"old"}`), 0o600))

	var sizes []int64

	rig.h.agents.result = func(request AgentRequest) agent.Result {
		info, err := os.Stat(request.OutputFile)
		if assert.NoError(t, err, "the file exists when the agent starts") {
			sizes = append(sizes, info.Size())
		}

		return exited
	}

	_, err := rig.run(t, outputsFlow("1"), "Write to {{ .output_file }}\n")

	require.ErrorContains(t, err, "the agent wrote no output file", "an empty file is the missing file")
	assert.Equal(t, []int64{0}, sizes, "the old content is gone and the file is empty")
}

func TestAgentStep_ACallWithNoDeclaredOutputsGetsNoOutputFile(t *testing.T) {
	t.Parallel()

	rig := newAgentRig(t)
	rig.h.agents.result = func(request AgentRequest) agent.Result {
		_, err := os.Stat(request.OutputFile)
		assert.ErrorIs(t, err, os.ErrNotExist)

		return exited
	}

	_, err := rig.run(t, agentFlow(""), "Write to {{ .output_file }}\n")

	require.NoError(t, err)
	assert.Len(t, rig.h.agents.seen(), 1)
}

func TestAgentStep_AnEmptyOutputFileThatCannotBeMadeFailsTheStep(t *testing.T) {
	t.Parallel()

	if os.Geteuid() == 0 {
		t.Skip("root writes into a folder that has no write bit")
	}

	rig := newAgentRig(t)
	folder := filepath.Dir(outputFile(rig.h.opts.StateDir, testItem, aStep))
	require.NoError(t, os.MkdirAll(folder, 0o700))
	require.NoError(t, exectest.ChmodFolder(folder, 0o500))
	t.Cleanup(func() { assert.NoError(t, exectest.ChmodFolder(folder, 0o700)) })

	_, err := rig.run(t, outputsFlow("1"), "Write to {{ .output_file }}\n")

	require.ErrorContains(t, err, "make the empty output file")
	assert.Empty(t, rig.h.agents.seen())
}

func TestOpenStoreAt_AFolderThatCannotBeResolvedNamesTheFolderAndOpensNoLog(t *testing.T) {
	t.Parallel()

	state := t.TempDir()

	_, err := openStoreAt(state, func(string) (string, error) { return "", errors.New("boom") })

	require.ErrorContains(t, err, "resolve the state folder "+state+": boom")
	assert.NoFileExists(t, filepath.Join(state, logFileName), "no log file is opened for a folder that cannot be resolved")
}

func TestAgentStep_ARefusedWriteIsNamedUnderAnotherSpellingOfTheOutputPath(t *testing.T) {
	t.Parallel()

	spellings := map[string]func(link, real string) string{
		"through a link to the state folder": func(link, _ string) string {
			return filepath.Join(link, outputsFolder, "12.agent_3Aa_231.json")
		},
		"with a trailing slash": func(_, real string) string {
			return filepath.Join(real, outputsFolder, "12.agent_3Aa_231.json") + "/"
		},
		"with a dot segment": func(_, real string) string {
			return filepath.Join(real, outputsFolder) + "/./12.agent_3Aa_231.json"
		},
	}

	for name, spell := range spellings {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			rig := newAgentRig(t)
			real := linkedState(t, rig)
			denied := spell(filepath.Join(filepath.Dir(real), "link-state"), real)
			rig.h.agents.result = func(AgentRequest) agent.Result {
				refused := exited
				refused.Denials = []agent.Denial{{Tool: "Write", Target: denied}}

				return refused
			}

			_, err := rig.run(t, outputsFlow("1"), "Write to {{ .output_file }}\n")

			require.ErrorContains(t, err, "the permission check of the agent CLI refused the Write call that writes it")
		})
	}
}

func TestRefusedWrites_ADeniedPathThatCannotBeResolvedKeepsThePlainMessage(t *testing.T) {
	t.Parallel()

	file := filepath.Join(t.TempDir(), "out.json")
	denials := []agent.Denial{
		{Tool: "Write", Target: filepath.Join(t.TempDir(), "missing-folder", "out.json")},
		{Tool: "Bash", Target: "echo hi > out.json"},
		{Tool: "Edit", Target: "out.json"},
	}

	assert.Empty(t, refusedWrites(file, denials))
}

func TestRefusedWrites_ARelativePathIsNotTakenAsRelativeToThisProcess(t *testing.T) {
	t.Parallel()

	here, err := os.Getwd()
	require.NoError(t, err)

	denials := []agent.Denial{{Tool: "Write", Target: "out.json"}}

	assert.Empty(t, refusedWrites(filepath.Join(here, "out.json"), denials), "the agent does not run in the folder of this process")
}

func TestRefusedWrites_TheMacStateFolderUnderVarIsTheSameFileAsUnderPrivateVar(t *testing.T) {
	t.Parallel()

	real, err := filepath.EvalSymlinks("/var/tmp")
	if err != nil || real == "/var/tmp" {
		t.Skip("this system has no link from /var to another folder")
	}

	state, err := os.MkdirTemp("/var/tmp", "spelling")
	require.NoError(t, err)
	t.Cleanup(func() { assert.NoError(t, os.RemoveAll(state)) })

	require.NoError(t, os.MkdirAll(filepath.Join(state, outputsFolder), 0o700))

	resolved, err := filepath.EvalSymlinks(state)
	require.NoError(t, err)
	require.NotEqual(t, state, resolved, "the two spellings differ")

	denials := []agent.Denial{{Tool: "Write", Target: filepath.Join(state, outputsFolder, "out.json")}}

	assert.Contains(t, refusedWrites(filepath.Join(resolved, outputsFolder, "out.json"), denials), "refused the Write call", "the output folder is there and the file is not")
}
