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

// refusalFlow runs the review loop of the library on the base main with two rounds, and one prompt file for each kind of agent step.
// The text %s is the extra arguments of review_fix.
const refusalFlow = "load(\"@beehave//review_fix.star\", \"review_fix\")\n\n" +
	"def main(item):\n" +
	"    return pipeline(item)\n\n" +
	"def pipeline(item):\n" +
	"    impl = agent(name = \"implement\", prompt = \"prompts/impl.md\", model = \"sonnet\")\n" +
	"    loop = review_fix(item, impl, \"prompts/review.md\", \"prompts/fix.md\", base = \"main\", rounds = 2%s, steps = " + loopSteps + ")\n" +
	"    return {\"approved\": loop.review != None and loop.review.verdict == \"approve\", \"exhausted\": loop.exhausted, \"head\": loop.head}\n"

// refusalValue is what refusalFlow returns.
type refusalValue struct {
	Approved  bool   `json:"approved"`
	Exhausted bool   `json:"exhausted"`
	Head      string `json:"head"`
}

// refusalRun is what the loop of a test of this file did: the result, the prompts of the fix agents, the heads that the review agents read, and the gate.
type refusalRun struct {
	value   refusalValue
	fixes   []string
	reviews []string
	gate    *headGate
	names   []string
}

// workRig makes an item folder with the base commit on origin/main and a commit of the work above it. The work adds the files, each with a line of text.
// origin/main holds the commit of the base, so the head holds the base.
func workRig(t *testing.T, files ...string) *agentRig {
	t.Helper()

	rig := newAgentRig(t)
	rig.baseAt(t, "main")

	for _, file := range files {
		putUnder(t, rig.dir, file, "x\n")
		gitIn(t, rig.dir, "add", file)
	}

	gitIn(t, rig.dir, "commit", "-q", "-m", "add the files")

	return rig
}

// driveLoop runs the review loop of the library over the item folder of the rig with a gate that passes the head as it is, an agent that approves every review,
// and the given fix, which a fix agent runs in the folder. extra is added to the arguments of review_fix, as ", name = value".
func driveLoop(t *testing.T, rig *agentRig, extra string, fix func()) refusalRun {
	t.Helper()

	run := refusalRun{gate: &headGate{t: t, dir: rig.dir}}
	rig.h.gate.via = run.gate
	rig.h.gate.checks = cleanChecks()
	rig.h.facts = func(string) model.Project { return model.Project{Source: rig.dir} }
	rig.h.commands.reply = gitReply(t, rig.dir)

	rig.h.agents.result = func(request AgentRequest) agent.Result {
		switch {
		case strings.HasPrefix(request.Prompt, "FIX"):
			run.fixes = append(run.fixes, request.Prompt)
			fix()
		case strings.HasPrefix(request.Prompt, "REVIEW"):
			run.reviews = append(run.reviews, gitIn(t, rig.dir, "rev-parse", "HEAD"))
			rig.put(outputPath(request.Prompt), `{"verdict":"approve"}`)
		}

		return exited
	}

	file := writeFlow(t, strings.Replace(refusalFlow, "%s", extra, 1))
	for name, body := range map[string]string{"impl": "IMPL\n", "review": "REVIEW\nWrite to {{ .output_file }}\n", "fix": "FIX\n{{ .findings }}\n"} {
		require.NoError(t, os.MkdirAll(filepath.Join(filepath.Dir(file), "prompts"), 0o750))
		require.NoError(t, os.WriteFile(filepath.Join(filepath.Dir(file), "prompts", name+".md"), []byte(body), 0o600))
	}

	engine := rig.h.engine(t)
	result, err := runPipelineOf(engine, testItem, file)

	require.NoError(t, err)
	require.NoError(t, json.Unmarshal(result.Value, &run.value))

	run.names = stepNamesOf(t, engine, testRun)

	return run
}

// TestReviewFixBeforeReview_ARefusalSendsTheLoopToAFixRoundAndTheFixedHeadGetsItsReview drives the loop through the refusal of a head that does not hold the base.
// The checks before review refuse the first candidate, so no full review reads it, the fix agent gets the refusal as its finding, and the fixed head passes the checks and gets the one full review.
// A path outside allow and a closing keyword in a commit are refused the same way, but no fix can repair them: the scope of a fix agent refuses the change of a path outside allow,
// and a fix adds commits and never rewrites the history that holds the keyword.
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

	refused := gitIn(t, rig.dir, "rev-parse", "HEAD")

	run := driveLoop(t, rig, "", func() { gitIn(t, rig.dir, "merge", "-q", "--no-edit", "origin/main") })

	fixed := gitIn(t, rig.dir, "rev-parse", "HEAD")

	assert.True(t, run.value.Approved, "the review of the fixed head approves")
	assert.Equal(t, fixed, run.value.Head)
	require.Len(t, run.fixes, 1, "the refusal sends the loop to one fix round")
	assert.Contains(t, run.fixes[0], "The checks before review refused the head:")
	assert.Contains(t, run.fixes[0], "is not a descendant of the base origin/main. Rebase the work on the base")
	assert.Equal(t, []string{refused, fixed, fixed}, run.reviews, "the scoped reviews read the refused head and the fixed head, and the one full review reads the fixed head: no full review reads the refused head")
	assert.EqualValues(t, 2, run.gate.runs.Load(), "the gate runs once for the refused head and once for the fixed head")
	assert.Equal(t, 2, stepsNamed(run.names, "command:candidate-before-review-ancestor"), "the checks run once for each candidate")
}

// TestReviewFixBeforeReview_TheLoopPassesItsAllowToTheChecksBeforeReview drives the loop with allow = ["src/**"] over a head that changes docs/stray.md.
// The first candidate is refused for the path, the fix agent gets the finding, and no full review reads a head that still holds the path. A loop that drops allow
// before the call approves the head at once, with no fix round.
func TestReviewFixBeforeReview_TheLoopPassesItsAllowToTheChecksBeforeReview(t *testing.T) {
	t.Parallel()

	rig := workRig(t, "src/a.go", "docs/stray.md")

	run := driveLoop(t, rig, `, allow = ["src/**"]`, func() {
		putUnder(t, rig.dir, "src/b.go", "x\n")
		gitIn(t, rig.dir, "add", "src/b.go")
		gitIn(t, rig.dir, "commit", "-q", "-m", "add b")
	})

	require.NotEmpty(t, run.fixes, "the refusal sends the loop to a fix round")
	assert.Contains(t, run.fixes[0], "the head changed 1 path(s) outside allow [src/**]: docs/stray.md")
	assert.True(t, run.value.Exhausted, "no fix agent can remove a path outside allow, so the rounds end")
	assert.False(t, run.value.Approved, "no full review approves a head that holds the path")
}
