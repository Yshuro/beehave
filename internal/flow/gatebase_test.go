package flow

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const candidateGate = "g = gate(name = \"first\", base = \"main\")\ncommand(run = [\"show\", \",\".join(g.new_tests), str(g.checks[\"new\"]), str(g.checks[\"complete\"])])\n"

func TestBeforeReview_ACandidateGateSubmitsTheBaseAndRecordsTheNewTestNamesOfItsChecks(t *testing.T) {
	t.Parallel()

	checks := &GateChecks{NewTests: []string{"pkg TestA", "pkg TestA/case"}, New: 2, Complete: true}
	h := reuseHarness(t, []GateResult{{Passed: true, Head: gatedHead, Tree: "tree-1", Steps: stepsPass, Checks: checks}}, nil)

	_, err := walkText(t, h, bodyFlow(candidateGate))

	require.NoError(t, err)
	assert.Equal(t, []string{"main"}, h.gate.bases)
	require.Len(t, h.commands.ran(), 1)
	assert.Contains(t, h.commands.ran()[0], "pkg TestA,pkg TestA/case")
	assert.Contains(t, h.commands.ran()[0], "True")
}

func TestBeforeReview_AGateWithNoBaseSubmitsNoBaseAndHasNoNewTests(t *testing.T) {
	t.Parallel()

	h := reuseHarness(t, []GateResult{{Passed: true, Head: gatedHead, Tree: "tree-1", Steps: stepsPass}}, nil)

	_, err := walkText(t, h, bodyFlow("g = gate(name = \"first\")\ncommand(run = [\"show\", str(len(g.new_tests)), str(g.checks[\"complete\"])])\n"))

	require.NoError(t, err)
	assert.Equal(t, []string{""}, h.gate.bases)
	assert.Contains(t, h.commands.ran()[0], "0")
	assert.Contains(t, h.commands.ran()[0], "False")
}

func TestBeforeReview_ACandidateGateReusesNoEarlierPassBecauseThePassHasNoChecks(t *testing.T) {
	t.Parallel()

	h := reuseHarness(t, []GateResult{{Passed: true, Head: gatedHead, Tree: "tree-1", Steps: stepsPass}}, []GateKey{gateKey("k", gatedHead), gateKey("k", gatedHead)})

	_, err := walkText(t, h, bodyFlow("a = gate(name = \"first\")\nb = gate(name = \"second\", base = \"main\")\ncommand(run = [\"show\", b.head])\n"))

	require.NoError(t, err)
	assert.Equal(t, 2, h.gate.calls, "the candidate gate ran its own job")
	assert.Equal(t, []string{"", "main"}, h.gate.bases)
}

func TestGateCall_APinBaseReachesTheKeyAndTheJobAndTurnsOnNoCandidateCheck(t *testing.T) {
	t.Parallel()

	h := reuseHarness(t, []GateResult{{Passed: true, Head: gatedHead, Tree: "tree-1", Steps: stepsPass}}, nil)

	_, err := walkText(t, h, bodyFlow("a = gate(name = \"first\", pin_base = \"integration/s1\")\nb = gate(name = \"second\")\ncommand(run = [\"show\", b.head])\n"))

	require.NoError(t, err)
	assert.Equal(t, []string{"integration/s1", ""}, h.gate.keyPinBases, "the key of the first gate is read at the pin base, and the second names none")
	assert.Equal(t, []string{"integration/s1", ""}, h.gate.pinBases, "the job of the first gate is submitted with the pin base")
	assert.Equal(t, []string{"", ""}, h.gate.bases, "a pin base is no base: the gate runs no candidate check")
}

func TestBeforeReview_TheKeyOfAGateIsReadAtTheBaseOfTheCallAndAGateWithNoBaseReadsItAtNoBase(t *testing.T) {
	t.Parallel()

	h := reuseHarness(t, []GateResult{{Passed: true, Head: gatedHead, Tree: "tree-1", Steps: stepsPass}}, nil)

	_, err := walkText(t, h, bodyFlow("a = gate(name = \"first\")\nb = gate(name = \"second\", base = \"integration/s1\")\ncommand(run = [\"show\", b.head])\n"))

	require.NoError(t, err)
	assert.Equal(t, []string{"", "integration/s1"}, h.gate.keyBases)
}
