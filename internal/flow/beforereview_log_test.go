package flow

import (
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	gatepkg "github.com/tobocop2/beehave/internal/gate"
	"github.com/tobocop2/beehave/internal/host/hosttest"
	"github.com/tobocop2/beehave/internal/host/ssh"
	"github.com/tobocop2/beehave/internal/host/sshtest"
	"github.com/tobocop2/beehave/internal/model"
)

// tailBytes is the size of the log tail that the ssh host reads.
const tailBytes = 16384

// jobLog is the log of a candidate job: a head gate that printed a lot, the two step markers, the name lines and marker lines of the check, and the end marker.
func jobLog(noise int, secretsRC string, lines ...string) string {
	return strings.Repeat("go test output line of the head gate\n", noise) +
		"beehave-gate-step head-gate rc=0\n" + "beehave-gate-step secret-scan rc=" + secretsRC + "\n" + strings.Join(lines, "\n") + "\n"
}

// findingsOfLog gives the log to the fake machine as the log of a submitted job, reads it back through the ssh host, and returns the length that the host read
// and the findings of before_review for the checks that the engine reads from it.
func findingsOfLog(t *testing.T, log string) (read int, findings []string, checks GateChecks) {
	t.Helper()

	m := sshtest.New(hosttest.Working)
	n := 0

	h, err := ssh.New("gates", model.HostConfig{
		Provider: ssh.Name, MaxRun: 2 * time.Hour, StaleMinutes: 20, PrivateRemote: "private",
		Block: model.NewBlock("test", []model.BlockEntry{
			model.TextEntry(ssh.BlockUser, "lane"), model.TextEntry(ssh.BlockWorkRoot, sshtest.Root+"/work"), model.TextEntry(ssh.BlockKnownHosts, model.KnownHostsStrict),
		}),
	}, m.SSH(), ssh.WithNonce(func() string { n++; return "n" + strconv.Itoa(n) }))
	require.NoError(t, err)

	id, err := h.Submit(t.Context(), model.JobSpec{Name: "gate", Command: "x"})
	require.NoError(t, err)

	m.Units[string(id)].Log = log

	tail, err := h.Log(t.Context(), id)
	require.NoError(t, err)

	got := gatepkg.ReadChecks(tail)
	checks = GateChecks{NewTests: got.NewTests, Failed: got.Failed, New: got.New, FailedCount: got.FailedCount, Secrets: got.Secrets, Complete: got.Complete, Problem: got.Problem}

	return len(tail), checkFindings(checks), checks
}

func TestBeforeReview_ALogOverSixteenKiBBeforeTheEndMarkerPassesWithItsNewTestNames(t *testing.T) {
	t.Parallel()

	log := jobLog(1000, "0", "beehave-new-test pkg TestA", "beehave-new-test pkg TestA/case", "beehave-new-tests-end new=2 failed=0")
	require.Greater(t, len(log), tailBytes, "the log is longer than the tail that the host reads")

	read, findings, checks := findingsOfLog(t, log)

	assert.Equal(t, tailBytes, read)
	assert.Empty(t, findings)
	assert.Equal(t, []string{"pkg TestA", "pkg TestA/case"}, checks.NewTests, "a subtest is its own new test")
}

func TestBeforeReview_ASkippedNewTestInTheLogGivesOneMarkerLineAndIsRefused(t *testing.T) {
	t.Parallel()

	log := jobLog(1000, "0", "beehave-new-test pkg TestA", "beehave-new-test-failed skipped pkg TestA", "beehave-new-tests-end new=1 failed=1")

	_, findings, _ := findingsOfLog(t, log)

	assert.Equal(t, []string{"a new test failed a check: skipped pkg TestA"}, findings)
}

func TestBeforeReview_ALogWithNoEndMarkerIsACutLogAndZeroNewTestsIsRefused(t *testing.T) {
	t.Parallel()

	_, cut, _ := findingsOfLog(t, jobLog(1000, "0", "beehave-new-test pkg TestA"))
	_, zero, _ := findingsOfLog(t, jobLog(1000, "0", "beehave-new-tests-end new=0 failed=0"))

	require.Len(t, cut, 1)
	assert.Contains(t, cut[0], "no end marker")
	require.Len(t, zero, 1)
	assert.Contains(t, zero[0], "adds no new test")
}

func TestBeforeReview_ASecretScanThatFailedInTheLogIsRefusedEvenWithNewTests(t *testing.T) {
	t.Parallel()

	_, findings, _ := findingsOfLog(t, jobLog(1000, "1", "beehave-new-test pkg TestA", "beehave-new-tests-end new=1 failed=0"))

	require.Len(t, findings, 1)
	assert.Contains(t, findings[0], "secret scan of the head exited with code 1")
}
