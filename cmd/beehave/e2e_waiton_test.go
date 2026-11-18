package main

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// failures records the failure texts of a wait, as ts.Fatalf does for a script.
type failures struct{ texts []string }

func (f *failures) fail(format string, args ...any) {
	f.texts = append(f.texts, fmt.Sprintf(format, args...))
}

// runsOf writes a runs folder with one record for each pid and returns it.
func runsOf(t *testing.T, pids ...int) string {
	t.Helper()

	tmp := t.TempDir()
	folder := e2eRunsFolder(tmp)
	require.NoError(t, os.MkdirAll(folder, e2eFolderMode))

	for _, pid := range pids {
		require.NoError(t, os.WriteFile(filepath.Join(folder, strconv.Itoa(pid)), nil, e2eFileMode))
	}

	return folder
}

// endedPid is the pid of a process that ran and was waited for.
func endedPid(t *testing.T) int {
	t.Helper()

	command := exec.CommandContext(t.Context(), "true")
	require.NoError(t, command.Run())

	return command.Process.Pid
}

func TestWaitOn_ReturnsWhenTheFactIsRecorded(t *testing.T) {
	t.Parallel()

	var got failures

	reads := 0

	waitOn(got.fail, e2eWaiting{what: "the file", runs: runsOf(t, os.Getpid()), bound: time.Minute, found: func() bool {
		reads++

		return reads == 3
	}})

	assert.Equal(t, 3, reads)
	assert.Empty(t, got.texts)
}

// recordsOnce is a fact that is absent and, at its first read, records a run that began and ended during the wait.
func recordsOnce(t *testing.T, folder string, reads *int) func() bool {
	t.Helper()

	return func() bool {
		*reads++

		if *reads == 1 {
			require.NoError(t, os.WriteFile(filepath.Join(folder, strconv.Itoa(endedPid(t))), nil, e2eFileMode))
		}

		return false
	}
}

func TestWaitOn_FailsAtOnceWhenARunBeganAndEndedDuringTheWaitAndTheFactIsAbsent(t *testing.T) {
	t.Parallel()

	var got failures

	reads := 0
	folder := runsOf(t)

	waitOn(got.fail, e2eWaiting{what: "the file", runs: folder, bound: time.Minute, state: func() string { return "item-7-r1 running" }, found: recordsOnce(t, folder, &reads)})

	assert.Equal(t, 2, reads, "one read records the run, one follows the end")
	assert.Equal(t, []string{"every run of the script ended and the file is not there; item-7-r1 running"}, got.texts)
}

func TestWaitOn_ReturnsAFactThatTheEndOfTheRunWrote(t *testing.T) {
	t.Parallel()

	var got failures

	reads := 0
	folder := runsOf(t)
	record := recordsOnce(t, folder, &reads)

	waitOn(got.fail, e2eWaiting{what: "the file", runs: folder, bound: time.Minute, found: func() bool { return record() || reads == 2 }})

	assert.Empty(t, got.texts)
}

func TestWaitOn_FailsAtOnceWhenTheEndedChannelIsClosedAndTheFactIsAbsent(t *testing.T) {
	t.Parallel()

	var got failures

	ended := make(chan struct{})
	close(ended)

	waitOn(got.fail, e2eWaiting{what: "the file", ended: ended, bound: time.Minute, found: func() bool { return false }})

	assert.Equal(t, []string{"the run ended and the file is not there"}, got.texts)
}

func TestWaitOn_IgnoresARunThatEndedBeforeTheWaitBegan(t *testing.T) {
	t.Parallel()

	var got failures

	waitOn(got.fail, e2eWaiting{what: "the file", runs: runsOf(t, endedPid(t)), bound: 2 * e2ePoll, found: func() bool { return false }})

	require.Len(t, got.texts, 1)
	assert.Contains(t, got.texts[0], "waited 200ms for the file")
}

func TestWaitOn_KeepsWaitingWhileAnotherRunLives(t *testing.T) {
	t.Parallel()

	var got failures

	reads := 0
	folder := runsOf(t, os.Getpid())

	waitOn(got.fail, e2eWaiting{what: "the file", runs: folder, bound: 3 * e2ePoll, found: recordsOnce(t, folder, &reads)})

	require.Len(t, got.texts, 1)
	assert.Contains(t, got.texts[0], "waited 300ms for the file")
}

func TestRunsEndedSince_NeedsANewRecordAndNoLiveProcess(t *testing.T) {
	t.Parallel()

	ended := endedPid(t)
	none := map[string]bool{}

	assert.False(t, runsEndedSince(filepath.Join(t.TempDir(), "none"), none), "no folder")
	assert.False(t, runsEndedSince(runsOf(t), none), "no record")
	assert.False(t, runsEndedSince(runsOf(t, os.Getpid()), none), "a live process")
	assert.True(t, runsEndedSince(runsOf(t, ended), none), "an ended process")
	assert.False(t, runsEndedSince(runsOf(t, ended), map[string]bool{strconv.Itoa(ended): true}), "an ended process of before")
	assert.False(t, runsEndedSince(runsOf(t, ended, os.Getpid()), none), "an ended process beside a live one")

	folder := runsOf(t, ended)
	require.NoError(t, os.WriteFile(filepath.Join(folder, "not-a-pid"), nil, e2eFileMode))
	assert.False(t, runsEndedSince(folder, none), "a record that is no pid")
}

func TestRecordRun_RecordsTheProcessOfRunAndServeOnly(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		args   []string
		record bool
	}{
		{[]string{"run", "7"}, true},
		{[]string{"serve"}, true},
		{[]string{"status"}, false},
		{nil, false},
	} {
		tmp := t.TempDir()
		require.NoError(t, recordRun(tmp, tc.args))

		_, err := os.Stat(filepath.Join(e2eRunsFolder(tmp), strconv.Itoa(os.Getpid())))
		assert.Equal(t, tc.record, err == nil, "args %v", tc.args)
	}
}

func TestRecordRun_RefusesAFolderItCannotMake(t *testing.T) {
	t.Parallel()

	file := filepath.Join(t.TempDir(), "file")
	require.NoError(t, os.WriteFile(file, nil, e2eFileMode))

	require.Error(t, recordRun(file, []string{"run"}))
}

func TestMergePR_EndsItsBarrierWithItsOwnContext(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	remote, work, dir := filepath.Join(root, "remote.git"), filepath.Join(root, "work"), filepath.Join(root, "forge")
	require.NoError(t, os.MkdirAll(work, e2eFolderMode))
	require.NoError(t, os.MkdirAll(dir, e2eFolderMode))

	hostGit(t, root, "init", "-q", "--bare", "-b", "main", remote)
	hostGit(t, work, "init", "-q", "-b", "main")
	require.NoError(t, os.WriteFile(filepath.Join(work, "a.txt"), []byte("a"), e2eFileMode))
	hostGit(t, work, "add", "a.txt")
	hostGit(t, work, "commit", "-q", "-m", "base")
	hostGit(t, work, "push", "-q", remote, "main")
	hostGit(t, work, "checkout", "-q", "-b", "feature")
	require.NoError(t, os.WriteFile(filepath.Join(work, "b.txt"), []byte("b"), e2eFileMode))
	hostGit(t, work, "add", "b.txt")
	hostGit(t, work, "commit", "-q", "-m", "feature")
	hostGit(t, work, "push", "-q", remote, "feature")

	// The barrier names a task that never writes its ledger line, so only the context of the merge can end the wait before the bound of 30 seconds.
	require.NoError(t, os.WriteFile(filepath.Join(dir, "pause-after-merge"), []byte("c\n"), e2eFileMode))

	ctx, cancel := context.WithTimeout(t.Context(), swarmBarrierBound/6)
	defer cancel()

	err := swarmForgeAt{remote: remote, dir: dir}.MergePR(ctx, 1, hostGit(t, work, "rev-parse", "HEAD"))

	require.ErrorIs(t, err, context.DeadlineExceeded)
	assert.FileExists(t, filepath.Join(dir, "merged-1"), "the merge is recorded before the barrier")
}

func TestPauseAfterMerge_EndsAtOnceWhenTheContextIsDone(t *testing.T) {
	t.Parallel()

	forge := barrierForge(t)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	require.ErrorIs(t, forge.pauseAfterMerge(ctx, time.Minute), context.Canceled)
}

func TestPauseAfterMerge_FailsAtTheBoundWhenNothingOpensTheBarrier(t *testing.T) {
	t.Parallel()

	require.ErrorIs(t, barrierForge(t).pauseAfterMerge(context.Background(), time.Millisecond), errBarrierClosed)
}

func TestPauseAfterMerge_ReturnsWhenTheLedgerHoldsTheTask(t *testing.T) {
	t.Parallel()

	forge := barrierForge(t)
	ledger := filepath.Join(forge.dir, "..", "home", ".swarm", "swarm-1")
	require.NoError(t, os.MkdirAll(ledger, e2eFolderMode))
	require.NoError(t, os.WriteFile(filepath.Join(ledger, "ledger.tsv"), []byte("1\tc\ttask\t0.1\n"), e2eFileMode))

	require.NoError(t, forge.pauseAfterMerge(context.Background(), time.Minute))
}

func TestPauseAfterMerge_HoldsNoBarrierWithoutTheFile(t *testing.T) {
	t.Parallel()

	forge := swarmForgeAt{dir: filepath.Join(t.TempDir(), "forge")}

	require.NoError(t, forge.pauseAfterMerge(context.Background(), time.Millisecond))
}

// barrierForge is a forge folder whose barrier file names the task c.
func barrierForge(t *testing.T) swarmForgeAt {
	t.Helper()

	dir := filepath.Join(t.TempDir(), "forge")
	require.NoError(t, os.MkdirAll(dir, e2eFolderMode))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "pause-after-merge"), []byte("c\n"), e2eFileMode))

	return swarmForgeAt{dir: dir}
}
