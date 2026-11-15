package flow

import (
	"slices"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/tobocop2/beehave/internal/model"
)

// exampleMutationCommand is the command that the example rig holds as vars.mutation_command.
var exampleMutationCommand = []string{"sh", "scripts/mutation-pr.sh"}

// factsWithMutation returns the facts of the run: the facts a test set, with the vars key mutation_command added when the test gave the harness a command.
// The added key never replaces a key of the vars that the test set.
func (h *harness) factsWithMutation() func(project string) model.Project {
	if h.mutation == nil {
		return h.facts
	}

	return func(project string) model.Project {
		out := model.Project{Name: project}
		if h.facts != nil {
			out = h.facts(project)
		}

		vars := map[string]any{}
		for key, value := range out.Vars {
			vars[key] = value
		}

		if _, set := vars[mutationKey]; !set {
			words := make([]any, 0, len(h.mutation))
			for _, word := range h.mutation {
				words = append(words, word)
			}

			vars[mutationKey] = words
		}

		out.Vars = vars

		return out
	}
}

const mutationKey = "mutation_command"

// mutationRuns returns the requests of the example rig that ran in the slot class mutation.
func mutationRuns(rig *agentRig) []CommandRequest {
	rig.h.commands.mu.Lock()
	defer rig.h.commands.mu.Unlock()

	var runs []CommandRequest

	for _, request := range rig.h.commands.requests {
		if request.SlotClass == "mutation" {
			runs = append(runs, request)
		}
	}

	return runs
}

// fixPrompts returns the prompts of the fix agents of the example rig.
func fixPrompts(rig *agentRig) []string {
	var prompts []string

	for _, request := range rig.h.agents.requests {
		if strings.Contains(request.Prompt, "The findings to fix.") {
			prompts = append(prompts, request.Prompt)
		}
	}

	return prompts
}

// replyInOrder answers the mutation runs of the rig with the given outputs in order, and answers every other command with an empty success.
func replyInOrder(rig *agentRig, outputs ...CommandOutput) {
	next := 0

	rig.h.commands.reply = func(request CommandRequest) (CommandOutput, error) {
		if request.SlotClass != "mutation" {
			return CommandOutput{}, nil
		}

		out := outputs[min(next, len(outputs)-1)]
		next++

		return out, nil
	}
}

func approveAfter(fixes int) func(int) string {
	return func(review int) string {
		if review <= fixes {
			return "changes"
		}

		return "approve"
	}
}

func TestReviewFix_ALivedMutantOnAChangedLineIsAFinding(t *testing.T) {
	t.Parallel()

	rig, file := exampleRig(t, func(int) string { return "approve" })
	replyInOrder(rig,
		CommandOutput{RC: 1, Stdout: "Starting the tool\ninternal/a/a.go:12: a mutant lived: ARITHMETIC_BASE\ninternal/b/b.go:7: a mutant lived: CONDITIONALS_NEGATION\nmutation-pr: 2 mutant(s) lived.\n"},
		CommandOutput{RC: 0, Stdout: "mutation-pr: applied=2 killed=2 lived=0 timed_out=0\n"},
	)

	result, err := rig.h.engine(t).RunFile(testItem, testProject, file)

	require.NoError(t, err)
	assert.Equal(t, "merged", result.Merge)

	runs := mutationRuns(rig)
	require.Len(t, runs, 2, "the mutation run follows each green gate")
	assert.Equal(t, append(slices.Clone(exampleMutationCommand), "origin/integration"), runs[0].Run, "the command of the project, then the base the change is judged against")
	assert.NotEmpty(t, runs[0].Host, "a slot class needs a host")

	fixes := fixPrompts(rig)
	require.Len(t, fixes, 1, "the lived mutants send the lane back once")
	assert.Contains(t, fixes[0], "internal/a/a.go:12: a mutant lived: ARITHMETIC_BASE")
	assert.Contains(t, fixes[0], "internal/b/b.go:7: a mutant lived: CONDITIONALS_NEGATION")
	assert.NotContains(t, fixes[0], "Starting the tool", "the noise of the host log is no finding when located lines exist")
	assert.Equal(t, []string{"opus", "sonnet", "sonnet", "opus"}, rig.models(), "the plan, the implement step, a fix, then the one review of the fixed head: no review judges a head with a lived mutant")
}

func TestReviewFix_AMutationRunThatFailsSendsTheLaneBack(t *testing.T) {
	t.Parallel()

	for name, out := range map[string]CommandOutput{
		"it prints no line":      {RC: 2},
		"it prints a note":       {RC: 1, Stdout: "mutation-pr: no mutant was applied. Check that the diff touches covered Go code.\n"},
		"it prints blank lines":  {RC: 3, Stdout: "\n  \n"},
		"it prints one finding ": {RC: 1, Stdout: "x.go:1: a mutant lived: INCREMENT_DECREMENT\n"},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			rig, file := exampleRig(t, func(int) string { return "approve" })
			replyInOrder(rig, out, CommandOutput{RC: 0})

			_, err := rig.h.engine(t).RunFile(testItem, testProject, file)

			require.NoError(t, err)
			require.Len(t, mutationRuns(rig), 2)

			fixes := fixPrompts(rig)
			require.Len(t, fixes, 1, "a failed run is not a pass")

			if line := strings.TrimSpace(out.Stdout); line != "" {
				assert.Contains(t, fixes[0], line)
			} else {
				assert.Contains(t, fixes[0], "The mutation command exited with code", "a failed run with no finding line still names the failure")
			}
		})
	}
}

func TestReviewFix_ARunWithNoMutationCommandInTheVarsIsRefusedBeforeAnyAgentStarts(t *testing.T) {
	t.Parallel()

	rig, file := exampleRig(t, func(int) string { return "approve" })
	rig.h.mutation = nil

	_, err := rig.h.engine(t).RunFile(testItem, testProject, file)

	require.Error(t, err)
	assert.Contains(t, err.Error(), "vars.mutation_command")
	assert.Contains(t, err.Error(), "project file")
	assert.Empty(t, rig.h.agents.requests, "the refusal comes before the plan agent")
}

func TestReviewFix_AMutationCommandThatIsNotAListOfWordsIsRefused(t *testing.T) {
	t.Parallel()

	for name, value := range map[string]any{"a string": "make mutation-pr", "an empty list": []any{}, "a list with a number": []any{"sh", 3}, "a list with an empty word": []any{"sh", ""}} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			rig, file := exampleRig(t, func(int) string { return "approve" })
			rig.h.mutation = nil
			rig.h.facts = func(project string) model.Project {
				return model.Project{Name: project, Vars: map[string]any{mutationKey: value}}
			}

			_, err := rig.h.engine(t).RunFile(testItem, testProject, file)

			require.Error(t, err)
			assert.Contains(t, err.Error(), "vars.mutation_command")
			assert.Empty(t, rig.h.agents.requests)
		})
	}
}

func TestReviewFix_ACleanMutationRunLetsTheReviewJudgeTheHead(t *testing.T) {
	t.Parallel()

	rig, file := exampleRig(t, approveAfter(0))
	replyInOrder(rig, CommandOutput{RC: 0, Stdout: "mutation-pr: applied=3 killed=3 lived=0 timed_out=0\n"})

	result, err := rig.h.engine(t).RunFile(testItem, testProject, file)

	require.NoError(t, err)
	assert.Equal(t, "merged", result.Merge)
	assert.Len(t, mutationRuns(rig), 1)
	assert.Empty(t, fixPrompts(rig))
	assert.Equal(t, []string{"opus", "sonnet", "opus"}, rig.models(), "the plan, the implement step and one review")
}

func TestBuiltins_NoneIsNamedMutation(t *testing.T) {
	t.Parallel()

	for _, name := range moduleRegistry(t).Names() {
		assert.NotContains(t, strings.ToLower(name), "mutation", "the engine owns no mutation concept: the workflow runs the project's command")
	}
}
