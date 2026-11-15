package flow

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"strconv"
	"strings"
	"time"

	"github.com/dbos-inc/dbos-transact-golang/dbos"
	"go.starlark.net/starlark"

	"github.com/tobocop2/beehave/internal/config"
	gatepkg "github.com/tobocop2/beehave/internal/gate"
	"github.com/tobocop2/beehave/internal/model"
	"github.com/tobocop2/beehave/internal/workflow"
)

const (
	mutationName      = "mutation"
	mutationQueueName = "mutation"
	mutationJobName   = "mutation_job"

	// The results that a mutation call records in outcome.
	outcomeTested        = "tested"
	outcomeNotApplicable = "not applicable"
	outcomeNoCandidate   = "no candidate"
	outcomeNoneApplied   = "none applied"
	outcomeCutLog        = "cut log"
	outcomeToolError     = "tool error"
	outcomeNoResult      = "no result"
)

// MutationJob is one mutation run on a build host: the head to check out and the command to run in the clone. Limit is zero for the run limit of the host.
type MutationJob struct {
	Head    string        `json:"head"`
	Command string        `json:"command"`
	Limit   time.Duration `json:"limit,omitempty"`
}

// MutationRun is what a mutation job left: the tail of its log, whether its command exited nonzero, and the reason of a job that gave no result.
type MutationRun struct {
	Log    string `json:"log,omitempty"`
	Failed bool   `json:"failed,omitempty"`
	Note   string `json:"note,omitempty"`
}

// mutator runs the mutation command of a project as a job on its build host. A nil Mutation means the build cannot run it: a mutation call fails.
// host is the build host that the run recorded in host_up. An empty host is the default build host of the project.
type mutator interface {
	Mutate(ctx context.Context, project, item, host string, job MutationJob) (MutationRun, error)
}

var errNoMutator = &absentError{message: "this build cannot run the mutation command. Use a build that sets a mutation runner", step: "run the mutation command"}

// mutationInput is the input of the child run that holds one mutation job on the mutation queue.
type mutationInput struct {
	Project string      `json:"project"`
	Item    string      `json:"item"`
	Host    string      `json:"host,omitempty"`
	Job     MutationJob `json:"job"`
}

// mutationOutput is what the child run returned. Failure is set when the job failed: the run itself ends with no error, see Failure.
type mutationOutput struct {
	Run     MutationRun `json:"run"`
	Failure *Failure    `json:"failure,omitempty"`
}

// mutationCall is the arguments of a mutation call.
type mutationCall struct {
	base, head, command, label string
	limit                      time.Duration
}

// mutationArguments reads the keywords of a mutation call. `{base}` in the command stands for the base branch.
func mutationArguments(b *starlark.Builtin, args starlark.Tuple, kwargs []starlark.Tuple) (call mutationCall, err error) {
	var limit string

	if err = workflow.Unpack(b.Name(), args, kwargs, map[string]any{
		baseKeyword: &call.base, headKeyword: &call.head, builtinCommand: &call.command, "run_timeout": &limit, nameKeyword: &call.label,
	}); err != nil {
		return call, err
	}

	if !model.ValidBase(call.base) {
		return call, fmt.Errorf("mutation: base is %q. Write a branch name such as main: letters, digits, `.`, `_` and `-`, with `/` between parts", call.base)
	}

	call.command = strings.ReplaceAll(call.command, "{base}", call.base)
	call.limit, err = workflow.Duration("run_timeout", limit)

	return call, err
}

// mutation runs the mutation command of the project as a job on the mutation queue of the build host, and records what the log of the job says.
// The job runs apart from the gate: it takes no slot of the host queue and no turn of the exclusive queue, because it reads one commit and writes nothing back.
// The call keeps the lived mutants that sit on a line that the diff of the head against the base changes. A refusal is a result: ok is false and the findings say why.
func (s *script) mutation(b *starlark.Builtin, args starlark.Tuple, kwargs []starlark.Tuple) (starlark.Value, error) { //nolint:ireturn // the builtin contract of starlark-go
	call, err := mutationArguments(b, args, kwargs)
	if err != nil {
		return nil, err
	}

	ran, err := s.e.queueMutation(s, call)
	if err != nil {
		return nil, fmt.Errorf("mutation %q could not run: %w", call.label, err)
	}

	return s.durable(mutationName, call.label, func(ctx context.Context, _ string) (stepResult, error) {
		changed, err := s.changedByHead(ctx, call)
		if err != nil {
			return stepResult{}, err
		}

		return stepResult{Outputs: judgeMutation(ran, changed).outputs()}, nil
	})
}

// changedByHead reads the lines that the head of the call changed since its base.
func (s *script) changedByHead(ctx context.Context, call mutationCall) (lineSet, error) {
	dir, err := s.e.deps.Trees.Ensure(ctx, s.in.Project, s.in.Item)
	if err != nil {
		return nil, fmt.Errorf("find the folder of item %s: %w", s.in.Item, err)
	}

	diff, err := git(ctx, dir, "diff", "-U0", "--no-renames", call.base+"..."+call.head)
	if err != nil {
		return nil, fmt.Errorf("read the lines that the head changed since the base: %w", err)
	}

	return changedLines(diff), nil
}

// queueMutation runs the mutation job as a child run on the mutation queue and waits for it. The queue of the build host holds the job.
// A stopped run cancels the job. The error is the error that the job returned, rebuilt from the failure that the child recorded.
func (e *Engine) queueMutation(s *script, call mutationCall) (MutationRun, error) {
	if e.deps.Mutation == nil {
		return MutationRun{}, errNoMutator
	}

	handle, err := e.enqueueMutation(s, call)
	if err != nil {
		return MutationRun{}, err
	}

	done, err := awaitResult(s.ctx, e.ctx, handle, e.deps.Clock.resultOptions())
	if err != nil {
		if s.ctx.Err() != nil && e.ctx.Err() == nil {
			e.stopJob(s.ctx, ExclusiveInput{Item: s.in.Item}, handle.GetWorkflowID())
		}

		return MutationRun{}, err
	}

	return done.Run, done.Failure.err(e.knownErrors())
}

// enqueueMutation puts the job on the mutation queue of its build host. The queue caps the host at its slots, so the job takes any free slot.
func (e *Engine) enqueueMutation(s *script, call mutationCall) (dbos.WorkflowHandle[mutationOutput], error) {
	in := mutationInput{Project: s.in.Project, Item: s.in.Item, Host: s.hostChosen, Job: MutationJob{Head: call.head, Command: call.command, Limit: call.limit}}

	host := e.mutationHost(in)

	queue, found := e.mutationQueues[host]
	if !found {
		return nil, fmt.Errorf("build host %q has no mutation queue: the config declares no project that builds on it", host)
	}

	handle, err := dbos.RunWorkflow(s.ctx, e.mutationRun, in, dbos.WithQueue(queue))
	if err != nil {
		return nil, fmt.Errorf("queue the mutation job of item %s: %w", s.in.Item, err)
	}

	return handle, nil
}

// mutationHost is the build host of a job: the host that the run chose, else the default host of the project, else this machine.
func (e *Engine) mutationHost(in mutationInput) string {
	if in.Host != "" {
		return in.Host
	}

	if e.deps.Facts != nil {
		if host := e.deps.Facts(in.Project).BuildHost; host != "" {
			return host
		}
	}

	return config.LocalHost
}

// mutationRun is the child run of one mutation job. The failure of the job is its output, so the parent run rebuilds the error from it.
func (e *Engine) mutationRun(ctx dbos.Context, in mutationInput) (mutationOutput, error) {
	run, err := runStep(e, ctx, mutationJobName, func(c context.Context) (MutationRun, error) {
		return e.deps.Mutation.Mutate(c, in.Project, in.Item, in.Host, in.Job)
	})

	return mutationOutput{Run: run, Failure: e.failureOf(err)}, nil
}

// registerMutationQueues adds one mutation queue for each build host that Deps.MutationSlots lists, and one for this machine. The queue runs at most
// `host.mutation_slots` jobs at once: DBOS takes one concurrency number for a whole queue, so each host needs its own queue.
func (e *Engine) registerMutationQueues() error {
	slots := maps.Clone(e.deps.MutationSlots)
	if slots == nil {
		slots = map[string]int{}
	}

	if _, found := slots[config.LocalHost]; !found {
		slots[config.LocalHost] = 1
	}

	e.mutationQueues = make(map[string]dbos.Queue, len(slots))

	var failures []error

	for host, count := range slots {
		failures = append(failures, e.registerMutationQueue(host, count))
	}

	return errors.Join(failures...)
}

// registerMutationQueue adds the mutation queue of one host, capped at count jobs, and keeps it.
func (e *Engine) registerMutationQueue(host string, count int) error {
	options := []dbos.QueueOption{dbos.WithGlobalConcurrency(count)}
	if poll := e.deps.Clock.Poll; poll > 0 {
		options = append(options, dbos.WithQueueBasePollingInterval(poll))
	}

	queue, err := dbos.RegisterQueue(e.ctx, mutationQueueName+":"+host, options...)
	if err != nil {
		return fmt.Errorf("register the mutation queue of host %s: %w", host, err)
	}

	e.mutationQueues[host] = queue

	return nil
}

// mutationVerdict is what a mutation call records.
type mutationVerdict struct {
	outcome, reason string
	applied         int
	survived        int
	survivors       []string
	findings        []string
}

// judgeMutation reads the log of a mutation job and decides. The command alone decides "not applicable" and "no candidate": the engine records each result and
// refuses a run that applied no mutant. A log with no result line is a tool error, whatever the exit code. A log whose end marker is missing, or whose
// `printed` value differs from the lived lines that it holds, is cut. A tool error wins over a cut log, because an early exit prints no end marker.
func judgeMutation(run MutationRun, changed lineSet) mutationVerdict {
	if run.Note != "" {
		return mutationVerdict{outcome: outcomeNoResult, findings: []string{run.Note}}
	}

	read := gatepkg.ReadMutation(run.Log)
	if verdict, decided := decideMutation(run, read); decided {
		return verdict
	}

	return survivorsOn(read, changed)
}

// decideMutation gives the verdict of a log that needs no diff to read: a refusal, "not applicable" or "no candidate". It reports false for a log of a run that tested.
func decideMutation(run MutationRun, read gatepkg.MutationLog) (mutationVerdict, bool) {
	if verdict, unfinished := unfinishedMutation(run, read); unfinished {
		return verdict, true
	}

	switch {
	case read.HasNotApplicable:
		return mutationVerdict{outcome: outcomeNotApplicable, reason: read.NotApplicable}, true
	case read.HasNoCandidate:
		return mutationVerdict{outcome: outcomeNoCandidate, reason: read.NoCandidate}, true
	case read.Applied == 0:
		return failedMutation(outcomeNoneApplied, "the mutation run applied no mutant; the command may not cover the diff"), true
	case run.Failed && read.Lived == 0:
		return failedMutation(outcomeToolError, "the mutation command failed and reported no lived mutant. Read the log of the job on the build host"), true
	}

	return mutationVerdict{}, false
}

// unfinishedMutation gives the verdict of a log of a run that did not finish: no result line, a tool error after the counts line, or a cut log.
// A tool error wins over a cut log. It reports false for a log of a run that finished.
func unfinishedMutation(run MutationRun, read gatepkg.MutationLog) (mutationVerdict, bool) {
	switch {
	case !read.HasResult():
		return failedMutation(outcomeToolError, "the mutation command gave no result line, so it did not run to a result"+quoted(read.Other)+". Read the log of the job on the build host"), true
	case run.Failed && read.Other != "" && !read.HasEnd:
		return failedMutation(outcomeToolError, "the mutation command failed after its counts line and printed no end marker"+quoted(read.Other)+". Read the log of the job on the build host"), true
	case !read.HasEnd || read.Printed != len(read.Survivors):
		return failedMutation(outcomeCutLog, "the log of the mutation job is cut: its end marker is missing, or it counts "+strconv.Itoa(read.Printed)+" printed mutants where the log holds "+strconv.Itoa(len(read.Survivors))+". A cut log proves nothing"), true
	}

	return mutationVerdict{}, false
}

// quoted is the text of an unknown result line for a message, or nothing.
func quoted(line string) string {
	if line == "" {
		return ""
	}

	return fmt.Sprintf(" (it printed %q)", line)
}

// failedMutation is the verdict of a refusal: no pass, and the reason is the one finding.
func failedMutation(outcome, finding string) mutationVerdict {
	return mutationVerdict{outcome: outcome, findings: []string{finding}}
}

// survivorsOn keeps the lived mutants on a line that the diff changes. A lived mutant on another line gives no finding.
// The command prints at most 50 lived lines, and count is the number that lived on a changed line by its own reading, so the total drops the lines that the engine dropped.
func survivorsOn(read gatepkg.MutationLog, changed lineSet) mutationVerdict {
	verdict := mutationVerdict{outcome: outcomeTested, applied: read.Applied, survivors: []string{}, findings: []string{}}
	dropped := 0

	for _, survivor := range read.Survivors {
		if !changed.has(survivor.File, survivor.Line) {
			dropped++

			continue
		}

		place := survivor.File + ":" + strconv.Itoa(survivor.Line)
		verdict.survivors = append(verdict.survivors, place+" "+survivor.Mutator)
		verdict.findings = append(verdict.findings, place+" a mutant survived: "+survivor.Mutator+". The tests do not fail when this change is made")
	}

	verdict.survived = max(read.Count-dropped, len(verdict.survivors))

	return verdict
}

// outputs are the result fields of mutation. A call is ok when it has no finding.
func (v mutationVerdict) outputs() map[string]any {
	return map[string]any{
		"ok": len(v.findings) == 0, "outcome": v.outcome, "reason": v.reason, "applied": v.applied, "survived": v.survived,
		"survivors": orEmptyList(v.survivors), outputFindings: orEmptyList(v.findings),
	}
}

// lineSet is the lines that a diff adds or changes, for each file.
type lineSet map[string][][2]int

// has reports whether the diff changes the line of the file.
func (l lineSet) has(file string, line int) bool {
	for _, span := range l[file] {
		if line >= span[0] && line <= span[1] {
			return true
		}
	}

	return false
}

// changedLines reads the lines that a `git diff -U0` adds or changes, for each file. A deleted file and a pure deletion add no line.
func changedLines(diff string) lineSet {
	changed := lineSet{}
	file := ""
	removing, adding := 0, 0

	for line := range strings.SplitSeq(diff, "\n") {
		switch {
		case removing > 0 && strings.HasPrefix(line, "-"):
			removing--
		case adding > 0 && strings.HasPrefix(line, "+"):
			adding--
		case strings.HasPrefix(line, "+++ "):
			file = headerPath(strings.TrimPrefix(line, "+++ "))
		case strings.HasPrefix(line, "@@ "):
			removing, adding = hunkCounts(line)
			changed.add(file, line)
		}
	}

	return changed
}

// add records the added side of a hunk header for the file. A hunk of no file, or one that adds no line, adds nothing.
func (l lineSet) add(file, header string) {
	if span, ok := addedSpan(header); ok && file != "" {
		l[file] = append(l[file], span)
	}
}

// headerPath reads the path of a "+++" header. A path with a space ends in a tab, a path with a special character is quoted, and "/dev/null" is no file.
func headerPath(text string) string {
	if text == "/dev/null" {
		return ""
	}

	if unquoted, err := strconv.Unquote(text); err == nil {
		text = unquoted
	} else if path, _, found := strings.Cut(text, "\t"); found {
		text = path
	}

	return strings.TrimPrefix(text, "b/")
}

// hunkCounts reads the count of removed lines and the count of added lines of a hunk header, "@@ -a,b +c,d @@": b and d. A missing count is 1.
// The section heading after the second "@@" is code, so the reader stops there.
func hunkCounts(header string) (removed, added int) {
	for index, field := range strings.Fields(header) {
		if index > 0 && field == "@@" {
			break
		}

		if rest, found := strings.CutPrefix(field, "-"); found {
			_, removed, _ = spanNumbers(rest)
		} else if rest, found := strings.CutPrefix(field, "+"); found {
			_, added, _ = spanNumbers(rest)
		}
	}

	return removed, added
}

// addedSpan reads the added side of a hunk header, "@@ -a,b +c,d @@": the lines c to c+d-1. A count of 0 is a pure deletion and adds none.
func addedSpan(header string) ([2]int, bool) {
	for field := range strings.FieldsSeq(header) {
		rest, found := strings.CutPrefix(field, "+")
		if !found {
			continue
		}

		first, length, ok := spanNumbers(rest)

		return [2]int{first, first + length - 1}, ok && length > 0
	}

	return [2]int{}, false
}

// spanNumbers reads "c" or "c,d" of a hunk header: the first line and the count of lines, which is 1 when the header gives none.
func spanNumbers(text string) (first, length int, ok bool) {
	start, count, hasCount := strings.Cut(text, ",")

	first, err := strconv.Atoi(start)
	if err != nil {
		return 0, 0, false
	}

	if !hasCount {
		return first, 1, true
	}

	length, err = strconv.Atoi(count)

	return first, length, err == nil
}
