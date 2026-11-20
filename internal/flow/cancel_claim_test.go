package flow

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// commandLineClient opens the client of a command-line process of one build, over a lifecycle that the test chose.
func commandLineClient(t *testing.T, h *harness, version string, life lifecycle) *Client {
	t.Helper()

	opts := h.opts
	opts.Version = version

	client, err := OpenClient(t.Context(), opts, ReleaseDeps{Lifecycle: life, Beats: h.beats, Trees: h.trees, Clock: clock{Release: time.Second}})
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, client.Close()) })

	return client
}

// resumedAndCanceledRig fails the first run of the item in an engine that then stops, starts the serve engine, and moves that run to another build and cancels it,
// both from a command line with its own owner, while the serve engine holds the state folder. The serve engine never executed the run.
func resumedAndCanceledRig(t *testing.T) (harness *harness, serve *Engine) {
	t.Helper()

	harness = newHarness(t)
	harness.agents.err = errFake

	claims := sharedClaims(t, "first", "serve", "cli")
	cliLife := &claimingLife{fakeLifecycle: &fakeLifecycle{failing: map[string]bool{}}, claims: claims[2], owner: "cli"}

	first := harness.engineWith(t, func(e *Engine) {
		e.deps.Lifecycle = &claimingLife{fakeLifecycle: harness.life, claims: claims[0], owner: "first"}
	})

	_, err := first.Run(testItem, testProject)
	require.Error(t, err, "the first run fails at the agent")
	require.NoError(t, first.Shutdown())

	harness.agents.mu.Lock()
	harness.agents.err = nil
	harness.agents.mu.Unlock()

	serve = harness.engineWith(t, func(e *Engine) {
		e.deps.Lifecycle = &claimingLife{fakeLifecycle: harness.life, claims: claims[1], owner: "serve"}
	})

	cli := commandLineClient(t, harness, otherBuild, cliLife)
	require.NoError(t, cli.ResumeOnThisBuild(testItem))
	require.NoError(t, cli.CancelRun(testRun), "the cancel leaves the release to the engine that holds the state folder")
	require.FileExists(t, cancelPath(serve.dir, testRun), "the engine has not released the run yet")

	return harness, serve
}

func TestCancel_TheNextRunOfAnotherOwnerTakesTheItemAfterAResumeAndACancelFromTheCommandLine(t *testing.T) {
	t.Parallel()

	harness, serve := resumedAndCanceledRig(t)

	harness.tick(t)
	waitOn(t, condition(func() bool { return !fileExists(cancelPath(serve.dir, testRun)) }))

	result, err := serve.Run(testItem, testProject)

	require.NoError(t, err, "the claim of the resume does not refuse the next run")
	assert.Equal(t, ItemResult{Item: testItem}, result)
}

func TestCancel_AMarkerOfARunThatIsNotCanceledYetStaysForTheCancelThatIsAboutToCome(t *testing.T) {
	t.Parallel()

	harness := newHarness(t)
	engine := harness.engine(t)
	require.NoError(t, markCancel(engine.dir, "item-99-r1", ItemInput{Item: "99", Project: testProject}))

	engine.giveBackStray(openRun{id: "item-99-r1", in: ItemInput{Item: "99", Project: testProject}})

	assert.FileExists(t, cancelPath(engine.dir, "item-99-r1"))
	assert.Empty(t, harness.life.releasedBy())
}

func TestCancels_BeginRefusesARunThatThisProcessExecutesOrReleases(t *testing.T) {
	t.Parallel()

	var list cancels

	list.add("local", openRun{id: "local", cancel: func() {}})

	assert.False(t, list.begin("local"), "a local run is released by its own give-back")
	assert.True(t, list.begin("stray"))
	assert.False(t, list.begin("stray"), "a release under way is not started twice")

	list.finish("stray")

	assert.True(t, list.begin("stray"))

	cut := list.cancel([]string{"local"})

	require.Len(t, cut, 1)
	assert.False(t, list.begin("local"), "a cut run is released until its give-back ends")

	list.add("gate", openRun{id: "gate", cancel: func() {}, gate: true})
	require.Len(t, list.cancel([]string{"gate"}), 1)
	assert.True(t, list.begin("gate"), "a gate has no give-back, so a cut gate is not held")
}

func TestCancel_AnUnreadableCancelFolderIsLoggedAndTheTickGoesOn(t *testing.T) {
	t.Parallel()

	var logged bytes.Buffer

	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, cancelsDir), nil, ownerMode))

	engine := &Engine{dir: dir, logger: newLogger(&logged)}

	engine.giveBackStrayCancels()

	assert.Contains(t, logged.String(), "cannot read the canceled runs")
}
