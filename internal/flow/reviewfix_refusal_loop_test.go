package flow

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/tobocop2/beehave/internal/agent"
	"github.com/tobocop2/beehave/internal/model"
	"github.com/tobocop2/beehave/internal/proc"
)

// headGate passes every run on the head of the item folder as it is when the run starts, so a commit of a fix agent is the head of the next candidate.
type headGate struct {
	t    *testing.T
	dir  string
	jobs atomic.Int32
	runs atomic.Int32
}

func (*headGate) Key(context.Context, string, string, string) (GateKey, error) {
	return GateKey{}, nil
}

func (g *headGate) Submit(context.Context, string, string, string, string) (GateJob, error) {
	return GateJob{ID: "job-" + strconv.Itoa(int(g.jobs.Add(1)))}, nil
}

func (g *headGate) Run(ctx context.Context, _, _, _ string, _ GateJob) (GateResult, error) {
	g.runs.Add(1)

	out, err := proc.GroupCommand(ctx, "git", []string{"rev-parse", "HEAD"}, g.dir).Output()
	if err != nil {
		return GateResult{}, err
	}

	head := strings.TrimSpace(string(out))

	return GateResult{Passed: true, Head: head, Tree: "tree-" + head, Steps: commandStep(true)}, nil
}

// refusalFlow runs the review loop of the library with the allowed paths src/**, and one prompt file for each kind of agent step.
const refusalFlow = "load(\"@beehave//review_fix.star\", \"review_fix\")\n\n" +
	"def main(item):\n" +
	"    return pipeline(item)\n\n" +
	"def pipeline(item):\n" +
	"    impl = agent(name = \"implement\", prompt = \"prompts/impl.md\", model = \"sonnet\")\n" +
	"    loop = review_fix(item, impl, \"prompts/review.md\", \"prompts/fix.md\", base = \"main\", rounds = 3, steps = " + loopSteps + ")\n" +
	"    return {\"approved\": loop.review != None and loop.review.verdict == \"approve\", \"head\": loop.head}\n"

// TestReviewFixBeforeReview_ARefusalSendsTheLoopToAFixRoundAndTheFixedHeadGetsItsReview drives the loop through the refusal of a head that changes a path outside allow.
// The checks before review refuse the first candidate, so no full review reads it, the fix agent gets the refusal as its finding, and the fixed head passes the checks and gets the one full review.
func TestReviewFixBeforeReview_ARefusalSendsTheLoopToAFixRoundAndTheFixedHeadGetsItsReview(t *testing.T) {
	t.Parallel()

	rig := newAgentRig(t)
	// The base moved on after the work started: origin/main holds a commit that the head does not hold.
	gitIn(t, rig.dir, "checkout", "-q", "-b", "moved")
	putUnder(t, rig.dir, "moved.txt", "m\n")
	gitIn(t, rig.dir, "add", "moved.txt")
	gitIn(t, rig.dir, "commit", "-q", "-m", "the base moves on")
	gitIn(t, rig.dir, "update-ref", "refs/remotes/origin/main", "HEAD")
	gitIn(t, rig.dir, "checkout", "-q", "-b", "work", rig.head)
	putUnder(t, rig.dir, "src/a.go", "package a\n")
	gitIn(t, rig.dir, "add", "src/a.go")
	gitIn(t, rig.dir, "commit", "-q", "-m", "add a")

	gate := &headGate{t: t, dir: rig.dir}
	rig.h.gate.via = gate
	rig.h.gate.checks = cleanChecks()
	rig.h.facts = func(string) model.Project { return model.Project{Source: rig.dir} }
	rig.h.commands.reply = gitReply(t, rig.dir)

	refused := gitIn(t, rig.dir, "rev-parse", "HEAD")

	var fixes, reviews []string

	rig.h.agents.result = func(request AgentRequest) agent.Result {
		switch {
		case strings.HasPrefix(request.Prompt, "FIX"):
			fixes = append(fixes, request.Prompt)
			gitIn(t, rig.dir, "merge", "-q", "--no-edit", "origin/main")
		case strings.HasPrefix(request.Prompt, "REVIEW"):
			reviews = append(reviews, gitIn(t, rig.dir, "rev-parse", "HEAD"))
			rig.put(outputPath(request.Prompt), `{"verdict":"approve"}`)
		}

		return exited
	}

	file := writeFlow(t, refusalFlow)
	for name, body := range map[string]string{"impl": "IMPL\n", "review": "REVIEW\nWrite to {{ .output_file }}\n", "fix": "FIX\n{{ .findings }}\n"} {
		require.NoError(t, os.MkdirAll(filepath.Join(filepath.Dir(file), "prompts"), 0o750))
		require.NoError(t, os.WriteFile(filepath.Join(filepath.Dir(file), "prompts", name+".md"), []byte(body), 0o600))
	}

	engine := rig.h.engine(t)
	result, err := runPipelineOf(engine, testItem, file)

	require.NoError(t, err)

	var value struct {
		Approved bool   `json:"approved"`
		Head     string `json:"head"`
	}

	require.NoError(t, json.Unmarshal(result.Value, &value))

	fixed := gitIn(t, rig.dir, "rev-parse", "HEAD")

	assert.True(t, value.Approved, "the review of the fixed head approves")
	assert.Equal(t, fixed, value.Head)
	require.Len(t, fixes, 1, "the refusal sends the loop to one fix round")
	assert.Contains(t, fixes[0], "The checks before review refused the head:")
	assert.Contains(t, fixes[0], "is not a descendant of the base origin/main. Rebase the work on the base")
	assert.Equal(t, []string{refused, fixed, fixed}, reviews, "the scoped reviews read the refused head and the fixed head, and the one full review reads the fixed head: no full review reads the refused head")
	assert.EqualValues(t, 2, gate.runs.Load(), "the gate runs once for the refused head and once for the fixed head")

	names := stepNamesOf(t, engine, testRun)
	assert.Equal(t, 2, stepsNamed(names, "command:candidate-before-review-ancestor"), "the checks run once for each candidate")
}
