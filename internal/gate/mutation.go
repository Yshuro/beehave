package gate

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/tobocop2/beehave/internal/config"
	"github.com/tobocop2/beehave/internal/model"
)

// ErrNoMutationHost is the cause when a project that builds on this machine asks for a mutation run.
var ErrNoMutationHost = errors.New("the mutation step has no build host")

const (
	// mutationPrefix starts every result line of the mutation command of a project.
	mutationPrefix = "mutation-pr: "

	// mutationJobName names the host job of the mutation command.
	mutationJobName = "mutation-"
)

// Survivor is one mutant that lives on a changed line, as the mutation command prints it.
type Survivor struct {
	File    string
	Line    int
	Mutator string
}

// MutationLog is what the engine reads from the log of a mutation job: only the result lines and the end marker of the command.
// A field that the log does not hold keeps its zero value, and the Has fields say which lines were there.
type MutationLog struct {
	// NotApplicable and NoCandidate hold the reason of the line of that name, and the Has fields say whether the line was there.
	NotApplicable, NoCandidate       string
	HasNotApplicable, HasNoCandidate bool
	// HasCounts is true when the log holds the counts line.
	HasCounts                      bool
	Applied, Killed, Lived, TimedO int
	// Survivors are the lived lines in log order.
	Survivors []Survivor
	// HasEnd is true when the log holds the end marker. Count and Printed are its numbers.
	HasEnd         bool
	Count, Printed int
	// Other holds the first result line that is no known line. The command prints one after a failed counts line, so it is an error only when no known result line is there.
	Other string
}

// HasResult is true when the log holds a result line that the command prints: not applicable, no candidate, or the counts.
func (l MutationLog) HasResult() bool { return l.HasNotApplicable || l.HasNoCandidate || l.HasCounts }

// ReadMutation reads the result lines and the end marker of the mutation command from the log of its job. It finds each line by its prefix, not by its place:
// the stderr of the job, the `make` error line and the done marker of the gate can follow the end marker. The last end marker counts.
func ReadMutation(log string) MutationLog {
	var read MutationLog

	for line := range strings.SplitSeq(log, "\n") {
		if rest, found := strings.CutPrefix(strings.TrimRight(line, "\r"), mutationPrefix); found {
			read.take(rest)
		}
	}

	return read
}

// take reads one result line, without its prefix.
func (l *MutationLog) take(line string) {
	switch {
	case strings.HasPrefix(line, "not applicable:"):
		l.NotApplicable, l.HasNotApplicable = strings.TrimSpace(strings.TrimPrefix(line, "not applicable:")), true
	case strings.HasPrefix(line, "no candidate:"):
		l.NoCandidate, l.HasNoCandidate = strings.TrimSpace(strings.TrimPrefix(line, "no candidate:")), true
	case strings.HasPrefix(line, "applied="):
		l.takeCounts(line)
	case strings.HasPrefix(line, "lived "):
		l.takeSurvivor(strings.TrimPrefix(line, "lived "))
	case strings.HasPrefix(line, "end "):
		l.takeEnd(strings.TrimPrefix(line, "end "))
	case l.Other == "":
		l.Other = line
	}
}

// takeCounts reads "applied=N killed=K lived=L timed_out=T". A part that does not read gives 0.
func (l *MutationLog) takeCounts(line string) {
	l.HasCounts = true

	for _, field := range strings.Fields(line) {
		key, value, _ := strings.Cut(field, "=")
		number := atoiOr(value, 0)

		switch key {
		case "applied":
			l.Applied = number
		case "killed":
			l.Killed = number
		case "lived":
			l.Lived = number
		case "timed_out":
			l.TimedO = number
		}
	}
}

// takeSurvivor reads "<file>:<line> <mutator>". A line that does not read is the first unknown line.
func (l *MutationLog) takeSurvivor(rest string) {
	place, mutator, _ := strings.Cut(rest, " ")
	at := strings.LastIndex(place, ":")

	number, err := strconv.Atoi(place[at+1:])
	if at <= 0 || err != nil || number < 1 {
		if l.Other == "" {
			l.Other = "lived " + rest
		}

		return
	}

	l.Survivors = append(l.Survivors, Survivor{File: place[:at], Line: number, Mutator: strings.TrimSpace(mutator)})
}

// takeEnd reads "count=N printed=P". An end marker with a part that does not read is not an end marker.
func (l *MutationLog) takeEnd(rest string) {
	numbers := map[string]int{"count": -1, "printed": -1}

	for _, field := range strings.Fields(rest) {
		key, value, _ := strings.Cut(field, "=")

		if number, err := strconv.Atoi(value); err == nil && number >= 0 {
			if _, known := numbers[key]; known {
				numbers[key] = number
			}
		}
	}

	if numbers["count"] >= 0 && numbers["printed"] >= 0 {
		l.HasEnd, l.Count, l.Printed = true, numbers["count"], numbers["printed"]
	}
}

// MutationRequest is one mutation run: the project, the clone URL, the head to check out and the command to run in the clone.
// Limit is the time that the command may run, and zero means the run limit of the host.
type MutationRequest struct {
	Project  model.Project
	CloneURL string
	Head     string
	Command  string
	Limit    time.Duration
}

// MutationRun is what a finished mutation job left. Log is the tail of its log, and it is empty when the job gave no result.
// Failed is true for a command that exited nonzero. Note says why a job that died, went stale or ran past its limit gave no result.
type MutationRun struct {
	Log    string
	Failed bool
	Note   string
}

// Mutate runs the mutation command of the project on the build host, in a fresh clone checked out at the head, and returns its log.
// A project that builds here has no host job, so it is refused. The job holds the command and nothing else: the engine reads the lines that it prints.
func (g Gate) Mutate(ctx context.Context, request MutationRequest) (MutationRun, error) {
	project := request.Project

	if err := checkMutation(request); err != nil {
		return MutationRun{}, err
	}

	host, err := g.hosts(project)
	if err != nil {
		return MutationRun{}, fmt.Errorf("project %s builds on %s: %w", project.Name, project.BuildHost, err)
	}

	id, err := submitMutation(ctx, host, request)
	if err != nil {
		return MutationRun{}, err
	}

	return awaitMutation(ctx, host, project, id)
}

// submitMutation clones the head on the build host and starts the mutation job there.
func submitMutation(ctx context.Context, host Remote, request MutationRequest) (model.JobID, error) {
	project := request.Project

	clone, err := cloneOn(ctx, host, project, request.Head)
	if err != nil {
		return "", err
	}

	id, err := host.Submit(ctx, model.JobSpec{Name: mutationJobName + project.Name, Command: mutationCommand(project, request, clone)})
	if err != nil {
		return "", fmt.Errorf("start the mutation run on %s: %w", project.BuildHost, err)
	}

	return id, nil
}

// checkMutation refuses a request that no build host can run: a project that builds on this machine, or one with no repository URL to clone.
func checkMutation(request MutationRequest) error {
	project := request.Project

	if project.BuildHost == "" || project.BuildHost == config.LocalHost {
		return fmt.Errorf("%w: project %s builds on this machine, and the mutation step runs as a job on a build host. Set `build_host`", ErrNoMutationHost, project.Name)
	}

	if request.CloneURL == "" {
		return fmt.Errorf("%w: project %s has no repository URL to clone on %s", ErrNoCloneURL, project.Name, project.BuildHost)
	}

	return nil
}

// mutationCommand is the command line of the mutation job: the clone and checkout of the gate, then the command of the project under the run limit.
// The command is a shell word, so `make mutation-pr BASE=origin/main` runs as written.
func mutationCommand(project model.Project, request MutationRequest, clone string) string {
	limit := project
	if request.Limit > 0 {
		limit.Host = &model.HostConfig{MaxRun: request.Limit}
	}

	words := cloneWords(request.CloneURL, request.Head, clone)

	return strings.Join(append(words, limited(limit, "sh -c "+model.ShellQuote(request.Command))), " && ")
}

// awaitMutation waits for the job and reads its log. A job that succeeded or failed has a log to read. A job that died, went stale or ran past its limit
// gave no result. A stopped run stops the job, so the host is free.
func awaitMutation(ctx context.Context, host Remote, project model.Project, id model.JobID) (MutationRun, error) {
	state, err := host.Wait(ctx, id)
	if err != nil {
		if ctx.Err() != nil {
			err = fmt.Errorf("%w: %w", err, stopJob(ctx, host, project.BuildHost, id))
		}

		return MutationRun{}, fmt.Errorf("wait for the mutation run on %s: %w", project.BuildHost, err)
	}

	switch state {
	case model.JobSucceeded, model.JobFailed:
		return readMutationLog(ctx, host, project, id, state)
	case model.JobDied, model.JobStale, model.JobTimedOut:
		return MutationRun{Note: strings.ReplaceAll(abortNote(project.BuildHost, state), "the gate", "the mutation run")}, nil
	case model.JobPending, model.JobRunning, model.JobCanceled, model.JobUnknown, model.JobLost:
	}

	return MutationRun{}, fmt.Errorf("%w: the mutation job on %s is %s. Read the job in the scheduler of the host", ErrNoVerdict, project.BuildHost, state)
}

// readMutationLog reads the log of a job that ended with a log to read.
func readMutationLog(ctx context.Context, host Remote, project model.Project, id model.JobID, state model.JobState) (MutationRun, error) {
	reader, ok := host.(LogReader)
	if !ok {
		return MutationRun{}, fmt.Errorf("%w: the build host %s cannot give the log of the mutation job", ErrNoVerdict, project.BuildHost)
	}

	log, err := reader.Log(ctx, id)
	if err != nil {
		return MutationRun{}, fmt.Errorf("read the log of the mutation job on %s: %w", project.BuildHost, err)
	}

	return MutationRun{Log: log, Failed: state == model.JobFailed}, nil
}
