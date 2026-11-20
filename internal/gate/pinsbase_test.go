package gate_test

import (
	"context"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/tobocop2/beehave/internal/gate"
	"github.com/tobocop2/beehave/internal/model"
)

// refShell answers `git show origin/<ref>:<file>` with the pin text of that ref, and records each git show command. A ref that it does not hold gives exit code 128.
type refShell struct {
	pins     map[string]string
	commands []string
}

func (r *refShell) run(_ context.Context, _, command string) (string, int) {
	rest, found := strings.CutPrefix(command, "git show origin/")
	if !found {
		return "", 0
	}

	r.commands = append(r.commands, command)

	ref, _, _ := strings.Cut(rest, ":")
	if text, ok := r.pins[ref]; ok {
		return text, 0
	}

	return "fatal: invalid object name\n", 128
}

// roundPins is a project base that pins one version and a round base that pins another.
func roundPins() *refShell {
	return &refShell{pins: map[string]string{"main": "gofake 1.27.3\n", "integration/s1": "gofake 1.28.0\n"}}
}

func roundKey(t *testing.T, shell *refShell, base string) gate.Key {
	t.Helper()

	p := pinnedHostProject()
	p.BuildHost = "cluster"

	g := gate.New(shell.run, (&fakeTree{states: []gate.State{clean(headA)}}).read, nil)
	key, err := g.Key(context.Background(), gate.Request{Project: p, Dir: "/w", Base: base})
	require.NoError(t, err)

	return key
}

func TestKey_AGateOfARoundChildReadsThePinFileAtTheRoundBaseAndTheKeyChangesWithIt(t *testing.T) {
	t.Parallel()

	shell := roundPins()

	atProject := roundKey(t, shell, "")
	atRound := roundKey(t, shell, "integration/s1")

	require.NotEmpty(t, atProject.ID)
	require.NotEmpty(t, atRound.ID)
	assert.NotEqual(t, atProject.ID, atRound.ID)
	assert.Equal(t, []string{"git show origin/main:.tool-versions", "git show origin/integration/s1:.tool-versions"}, shell.commands)
}

func TestKey_AGateWithNoBaseReadsThePinFileAtTheBaseOfTheProject(t *testing.T) {
	t.Parallel()

	shell := roundPins()
	roundKey(t, shell, "")

	assert.Equal(t, []string{"git show origin/main:.tool-versions"}, shell.commands)
}

func TestKey_APinFileThatTheRoundBaseLacksGivesNoKeyAndDoesNotFallBackToTheProjectBase(t *testing.T) {
	t.Parallel()

	shell := &refShell{pins: map[string]string{"main": "gofake 1.27.3\n"}}

	assert.Empty(t, roundKey(t, shell, "integration/s1").ID)
}

func TestSubmit_AGateOfARoundChildPlansThePinnedToolsOfTheRoundBase(t *testing.T) {
	t.Parallel()

	for name, c := range map[string]struct{ base, version string }{
		"a round base":   {"integration/s1", "1.28.0"},
		"no base":        {"", "1.27.3"},
		"the trunk base": {"main", "1.27.3"},
	} {
		shell := roundPins()
		remote := &fakeRemote{state: model.JobSucceeded}
		g := gate.New(shell.run, (&fakeTree{states: []gate.State{clean(headA)}}).read, func(model.Project) (gate.Remote, error) { return remote, nil })

		job, err := g.Submit(context.Background(), gate.Request{Project: pinnedHostProject(), Dir: "/w", CloneURL: cloneURL, Base: c.base})
		require.NoError(t, err, name)
		require.NotNil(t, job.Plan.Pins, name)

		assert.Equal(t, []gate.Pin{{Tool: "gofake", Version: c.version}}, job.Plan.Pins.Tools, name)
	}
}

func TestSubmit_TheProbeSentToTheBuildHostForARoundChildHoldsTheVersionOfTheRoundBase(t *testing.T) {
	t.Parallel()

	remote := &fakeRemote{state: model.JobSucceeded}
	g := gate.New(roundPins().run, (&fakeTree{states: []gate.State{clean(headA)}}).read, func(model.Project) (gate.Remote, error) { return remote, nil })

	_, err := g.Submit(context.Background(), gate.Request{Project: pinnedHostProject(), Dir: "/w", CloneURL: cloneURL, Base: "integration/s1"})

	require.NoError(t, err)
	require.Len(t, remote.submitted, 1)
	assert.Contains(t, remote.submitted[0].Command, "1.28.0")
	assert.NotContains(t, remote.submitted[0].Command, "1.27.3")
}

func TestKey_EveryReadOfThePinFileOfALocalGateOfARoundChildIsAtTheRoundBase(t *testing.T) {
	t.Parallel()

	shell := roundPins()
	p := pinnedHostProject()
	p.BuildHost = "local"

	_, err := gate.New(shell.run, (&fakeTree{states: []gate.State{clean(headA)}}).read, nil).Key(context.Background(), gate.Request{Project: p, Dir: "/w", Base: "integration/s1"})

	require.NoError(t, err)
	assert.Equal(t, []string{"git show origin/integration/s1:.tool-versions", "git show origin/integration/s1:.tool-versions"}, shell.commands, "the key reads the text, and the installed-tool check reads the pinned tools")
}
