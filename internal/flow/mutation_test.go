package flow

import (
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/tobocop2/beehave/internal/host/hosttest"
	"github.com/tobocop2/beehave/internal/host/ssh"
	"github.com/tobocop2/beehave/internal/host/sshtest"
	"github.com/tobocop2/beehave/internal/model"
)

const lived = "mutation-pr: lived internal/a/a.go:10 ARITHMETIC_BASE\n"

const livedSummary = "mutation-pr: 1 mutant(s) lived. Kill each one with a test.\n"

var changedA = lineSet{"internal/a/a.go": {{8, 12}}}

func counts(applied, livedCount int) string {
	return "mutation-pr: applied=" + strconv.Itoa(applied) + " killed=" + strconv.Itoa(applied-livedCount) + " lived=" + strconv.Itoa(livedCount) + " timed_out=0\n"
}

func TestMutation_ASurvivorOnAChangedLineIsOneFinding(t *testing.T) {
	t.Parallel()

	got := judgeMutation(MutationRun{Failed: true, Log: counts(4, 1) + lived + "mutation-pr: end count=1 printed=1\n"}, changedA)

	assert.Equal(t, []string{"internal/a/a.go:10 a mutant survived: ARITHMETIC_BASE. The tests do not fail when this change is made"}, got.findings)
	assert.Equal(t, []string{"internal/a/a.go:10 ARITHMETIC_BASE"}, got.survivors)
	assert.Equal(t, 1, got.survived)
	assert.Equal(t, false, got.outputs()["ok"])
}

func TestMutation_ARunWithNoMutantAppliedIsRefused(t *testing.T) {
	t.Parallel()

	for _, failed := range []bool{false, true} {
		got := judgeMutation(MutationRun{Failed: failed, Log: counts(0, 0) + "mutation-pr: end count=0 printed=0\n"}, changedA)

		assert.Equal(t, outcomeNoneApplied, got.outcome)
		assert.Equal(t, []string{"the mutation run applied no mutant; the command may not cover the diff"}, got.findings)
	}
}

func TestMutation_ALivedMutantOnALineTheDiffDoesNotChangeGivesNoFinding(t *testing.T) {
	t.Parallel()

	got := judgeMutation(MutationRun{Failed: true, Log: counts(4, 1) + lived + "mutation-pr: end count=1 printed=1\n"}, lineSet{"internal/a/a.go": {{20, 30}}})

	assert.Empty(t, got.findings)
	assert.Equal(t, 0, got.survived)
	assert.Equal(t, true, got.outputs()["ok"])
}

func TestMutation_ALogWithNoEndMarkerOrAWrongPrintedValueIsACutLog(t *testing.T) {
	t.Parallel()

	for name, log := range map[string]string{
		"no end marker":                   counts(4, 1) + lived,
		"no end marker after the summary": counts(4, 1) + livedSummary + lived,
		"wrong printed":                   counts(4, 1) + livedSummary + lived + "mutation-pr: end count=1 printed=2\n",
	} {
		got := judgeMutation(MutationRun{Failed: true, Log: log}, changedA)

		assert.Equal(t, outcomeCutLog, got.outcome, name)
		require.Len(t, got.findings, 1, name)
		assert.Contains(t, got.findings[0], "cut", name)
	}
}

func TestMutation_ALogWithLinesAfterTheEndMarkerPasses(t *testing.T) {
	t.Parallel()

	log := counts(4, 0) + "mutation-pr: end count=0 printed=0\nmake: *** [mutation-pr] Error 1\nbeehave-gate-done\n"

	got := judgeMutation(MutationRun{Log: log}, changedA)

	assert.Equal(t, outcomeTested, got.outcome)
	assert.Empty(t, got.findings)
}

func TestMutation_AToolErrorLineWithNoEndMarkerIsAToolErrorAndNotACutLog(t *testing.T) {
	t.Parallel()

	got := judgeMutation(MutationRun{Failed: true, Log: "mutation-pr: gremlins is not on the path\n"}, changedA)

	assert.Equal(t, outcomeToolError, got.outcome)
	assert.Contains(t, got.findings[0], "gremlins is not on the path")
}

func TestMutation_AToolErrorLineAfterTheCountsLineWithNoEndMarkerIsAToolErrorAndNotACutLog(t *testing.T) {
	t.Parallel()

	got := judgeMutation(MutationRun{Failed: true, Log: counts(4, 1) + "mutation-pr: the gremlins report is not readable\n"}, changedA)

	assert.Equal(t, outcomeToolError, got.outcome)
	require.Len(t, got.findings, 1)
	assert.Contains(t, got.findings[0], "the gremlins report is not readable")
}

func TestMutation_AnUnknownLineAfterTheSummaryLineWithNoEndMarkerIsAToolError(t *testing.T) {
	t.Parallel()

	got := judgeMutation(MutationRun{Failed: true, Log: counts(4, 1) + livedSummary + "mutation-pr: the gremlins report is not readable\n"}, changedA)

	assert.Equal(t, outcomeToolError, got.outcome)
	require.Len(t, got.findings, 1)
	assert.Contains(t, got.findings[0], "the gremlins report is not readable")
}

func TestMutation_AnExitWithNoResultLineIsAToolErrorWhateverTheExitCode(t *testing.T) {
	t.Parallel()

	for _, failed := range []bool{false, true} {
		got := judgeMutation(MutationRun{Failed: failed, Log: "make: *** No rule to make target 'mutation-pr'.\n"}, changedA)

		assert.Equal(t, outcomeToolError, got.outcome)
	}
}

func TestMutation_SixtyLivedMutantsGiveFiftyFindingsAndTheTotalOfSixty(t *testing.T) {
	t.Parallel()

	var log strings.Builder

	log.WriteString(counts(80, 60))

	for line := 1; line <= 50; line++ {
		log.WriteString("mutation-pr: lived internal/a/a.go:" + strconv.Itoa(line) + " CONDITIONALS_NEGATION\n")
	}

	log.WriteString("mutation-pr: end count=60 printed=50\n")

	got := judgeMutation(MutationRun{Failed: true, Log: log.String()}, lineSet{"internal/a/a.go": {{1, 100}}})

	assert.Len(t, got.findings, 50)
	assert.Equal(t, 60, got.survived)
}

func TestMutation_ANoCandidateLinePassesWithItsReasonAndANotApplicableLineToo(t *testing.T) {
	t.Parallel()

	candidate := judgeMutation(MutationRun{Log: "mutation-pr: no candidate: the changed lines hold no operator\nmutation-pr: end count=0 printed=0\n"}, changedA)
	notApplicable := judgeMutation(MutationRun{Log: "mutation-pr: not applicable: no Go source change against origin/main\nmutation-pr: end count=0 printed=0\n"}, changedA)

	assert.Equal(t, outcomeNoCandidate, candidate.outcome)
	assert.Equal(t, "the changed lines hold no operator", candidate.reason)
	assert.Empty(t, candidate.findings)
	assert.Equal(t, outcomeNotApplicable, notApplicable.outcome)
	assert.Equal(t, "no Go source change against origin/main", notApplicable.reason)
	assert.Empty(t, notApplicable.findings)
}

func TestMutation_TheChangedLinesAreTheAddedLinesOfTheHunks(t *testing.T) {
	t.Parallel()

	diff := "diff --git a/a.go b/a.go\n--- a/a.go\n+++ b/a.go\n@@ -3,0 +4,2 @@\n+x\n+y\n@@ -9 +11 @@\n-z\n+w\n@@ -20,2 +21,0 @@\n-gone\n-gone\n"

	got := changedLines(diff)

	assert.True(t, got.has("a.go", 4))
	assert.True(t, got.has("a.go", 5))
	assert.True(t, got.has("a.go", 11))
	assert.False(t, got.has("a.go", 6))
	assert.False(t, got.has("a.go", 21))
}

func TestMutation_ASurvivorInAFileWithANonASCIIOrSpacedNameIsKept(t *testing.T) {
	t.Parallel()

	diff := "diff --git a/internal/é/a b.go b/internal/é/a b.go\n--- a/internal/é/a b.go\n+++ b/internal/é/a b.go\t\n@@ -0,0 +1 @@\n+x\n" +
		"diff --git a/q.go b/q.go\n--- a/q.go\n+++ \"b/q\\\"x.go\"\n@@ -0,0 +3 @@\n+x\n" +
		"diff --git \"a/\\303\\251.go\" \"b/\\303\\251.go\"\n--- /dev/null\n+++ \"b/\\303\\251.go\"\n@@ -0,0 +2 @@\n+x\n"

	got := changedLines(diff)

	assert.True(t, got.has("é.go", 2), "git quotes a non-ASCII name with octal escapes, and the reader gives the name back as bytes")
	assert.True(t, got.has("internal/é/a b.go", 1))
	assert.True(t, got.has("q\"x.go", 3))
}

func TestMutation_ASectionHeadingWithAPlusFieldDoesNotChangeTheCountsOfTheHunk(t *testing.T) {
	t.Parallel()

	diff := "diff --git a/a.go b/a.go\n--- a/a.go\n+++ b/a.go\n@@ -0,0 +1,2 @@ x := a +b -c\n+x\n+++ b/other.go\n@@ -9 +11 @@\n-z\n+w\n"

	got := changedLines(diff)

	removed, added := hunkCounts("@@ -4,3 +5,2 @@ x := a +b -c")

	assert.Equal(t, 3, removed)
	assert.Equal(t, 2, added)
	assert.True(t, got.has("a.go", 2))
	assert.False(t, got.has("other.go", 11), "the second added line starts with two pluses and is a line, not a file header")
}

func TestMutation_AnAddedLineThatStartsWithTwoPlusesIsNotAFileHeader(t *testing.T) {
	t.Parallel()

	diff := "diff --git a/a.go b/a.go\n--- a/a.go\n+++ b/a.go\n@@ -0,0 +1,2 @@\n+x\n+++ b/other.go\n@@ -9 +11 @@\n-z\n+w\n"

	got := changedLines(diff)

	assert.True(t, got.has("a.go", 2))
	assert.True(t, got.has("a.go", 11))
	assert.False(t, got.has("other.go", 11))
}

// fakeSSHHost is an ssh host over the fake machine. A job that the host takes has the log that the test puts in m.Units.
func fakeSSHHost(t *testing.T) (*ssh.Host, *sshtest.Machine) {
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

	return h, m
}

func TestMutation_TheMutationStepThroughTheFakeSSHHostReadsOneLivedMutantFromTheJobLog(t *testing.T) {
	t.Parallel()

	h, m := fakeSSHHost(t)

	id, err := h.Submit(t.Context(), model.JobSpec{Name: "mutation", Command: "make mutation-pr BASE=origin/main"})
	require.NoError(t, err)

	m.Units[string(id)].Log = strings.Repeat("gremlins output line\n", 2000) + counts(4, 1) + lived + "mutation-pr: end count=1 printed=1\nbeehave-gate-done\n"

	tail, err := h.Log(t.Context(), id)
	require.NoError(t, err)

	got := judgeMutation(MutationRun{Failed: true, Log: tail}, changedA)

	assert.Equal(t, []string{"internal/a/a.go:10 a mutant survived: ARITHMETIC_BASE. The tests do not fail when this change is made"}, got.findings)
}

func TestMutation_ARunWithANoteIsNoResultAndKeepsTheNote(t *testing.T) {
	t.Parallel()

	got := judgeMutation(MutationRun{Note: "the host gave up", Log: counts(4, 1) + lived + "mutation-pr: end count=1 printed=1\n"}, changedA)

	assert.Equal(t, outcomeNoResult, got.outcome)
	assert.Equal(t, []string{"the host gave up"}, got.findings)
}

func TestMutation_AFailedRunWithMutantsAppliedAndNoneLivedIsAToolError(t *testing.T) {
	t.Parallel()

	got := judgeMutation(MutationRun{Failed: true, Log: counts(4, 0) + "mutation-pr: end count=0 printed=0\n"}, changedA)

	assert.Equal(t, outcomeToolError, got.outcome)
	assert.Equal(t, []string{"the mutation command failed and reported no lived mutant. Read the log of the job on the build host"}, got.findings)
	assert.Equal(t, false, got.outputs()["ok"])
}

func TestMutation_AHeaderForNoFileNamesNoFileAndAMalformedHunkHeaderAddsNoLine(t *testing.T) {
	t.Parallel()

	assert.Empty(t, headerPath("/dev/null"))

	for _, header := range []string{"@@ -1 @@", "@@ -1 +x @@", "@@ -1 +2,y @@"} {
		_, ok := addedSpan(header)

		assert.False(t, ok, header)
	}
}

func TestMutation_TheBuildHostOfAJobIsTheChosenHostThenTheProjectDefaultThenThisMachine(t *testing.T) {
	t.Parallel()

	projectHost := func(string) model.Project { return model.Project{BuildHost: "box"} }
	noHost := func(string) model.Project { return model.Project{} }

	tests := []struct {
		name   string
		facts  func(string) model.Project
		chosen string
		want   string
	}{
		{name: "the chosen host wins", facts: projectHost, chosen: "gpu", want: "gpu"},
		{name: "the project default", facts: projectHost, want: "box"},
		{name: "a project with no build host", facts: noHost, want: "local"},
		{name: "no facts at all", want: "local"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			engine := &Engine{deps: Deps{Facts: tc.facts}}

			assert.Equal(t, tc.want, engine.mutationHost(mutationInput{Project: "p", Host: tc.chosen}))
		})
	}
}
