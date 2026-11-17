package flow

import (
	"cmp"
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/dbos-inc/dbos-transact-golang/dbos"

	"github.com/tobocop2/beehave/internal/config"
	gatepkg "github.com/tobocop2/beehave/internal/gate"
	"github.com/tobocop2/beehave/internal/host"
	"github.com/tobocop2/beehave/internal/model"
)

const (
	// lostPoll is the time between two looks for the proof that a lost gate job can no longer run.
	lostPoll = time.Minute
	// gateLostStartName is the step that records when the wait for the proof started.
	gateLostStartName = "gate:lost-start"
	// gateLostLookName starts the name of each look for the proof: gate:lost-look.<k>, from 0.
	gateLostLookName = "gate:lost-look"
	// hostLossKey is the field of a gate result that holds the record of a lost job.
	hostLossKey = "host_loss"
	// noHostReader is the note of a proof that a build with no host keeper cannot read.
	noHostReader = "this build cannot read the host"
)

// LostJob is the host job of a gate whose wait ended as lost: the link to the host stayed down, so nobody can tell where the job stands.
// Kind is "gate". Host is the host of the HostKey of the call, which is the host the job ran on. Advice is the text of the lost error,
// which ends with the cancel command of the adapter. Required is the list of the gate. Since is the time the wait for a proof started.
// Proof is the proof that the last look read, and At is the time of that look. Limit is true when the wait reached host.lost_job_wait with no proof.
type LostJob struct {
	Kind     string          `json:"kind"`
	Job      string          `json:"job"`
	Host     string          `json:"host"`
	Head     string          `json:"head,omitempty"`
	Since    time.Time       `json:"since"`
	At       time.Time       `json:"at"`
	Advice   string          `json:"advice"`
	Required []string        `json:"required,omitempty"`
	Proof    model.LostProof `json:"proof"`
	Limit    bool            `json:"limit,omitempty"`
}

// gateFinish is the record of the step that waits for the host job of a project gate: the outputs of the gate, or the job that the wait lost.
// A loss is a value and not an error, because a step error is replayed as text and a later step could not read it.
type gateFinish struct {
	Outputs map[string]any `json:"outputs,omitempty"`
	Lost    *LostJob       `json:"lost,omitempty"`
}

// lostLook is the record of one look for the proof.
type lostLook struct {
	Proof model.LostProof `json:"proof"`
	At    time.Time       `json:"at"`
	Limit bool            `json:"limit,omitempty"`
}

// lostJobOf is the lost job of the project gate of in, from the job that its first step recorded and the lost error of the wait.
func lostJobOf(in ExclusiveInput, start gateStart, lost error) *LostJob {
	return &LostJob{
		Kind: kindGate, Job: start.Job.ID, Host: strings.TrimPrefix(in.HostKey, hostKeyPrefix), Head: start.Job.Head, Advice: lost.Error(), Required: start.Required,
	}
}

// endLost waits for the proof that the lost job of a gate can no longer run, and stores the job in out.
// A proof gives the void outputs of the gate. A wait that reaches host.lost_job_wait with no proof fails the gate with the lost error,
// rebuilt from its recorded text so that errors.Is finds host.ErrLost and the text names it once.
func (e *Engine) endLost(ctx dbos.Context, in ExclusiveInput, lost LostJob, out *ExclusiveOutput) (map[string]any, error) {
	held, err := e.holdLost(ctx, in, lost)
	if err != nil {
		return nil, err
	}

	out.Lost = &held

	if held.Limit {
		lost := rebuiltError{text: held.Advice, kinds: []error{host.ErrLost}}

		return nil, fmt.Errorf("no proof that job %s on host %s can no longer run came within host.lost_job_wait: %w", held.Job, held.Host, lost)
	}

	return voidOutputs(held), nil
}

// holdLost is the timed wait for the proof, in the run that holds the host slot and the exclusive slot of the project, so no other gate starts
// on a host whose job may still run. Each look is a recorded step that calls LostProof once, and a durable sleep comes between two looks,
// so a restart replays the earlier looks. A look reads the limit from the config of the engine that runs it. The wait submits no job.
// A run that is canceled during the wait reads the proof once more, see lostStopped.
func (e *Engine) holdLost(ctx dbos.Context, in ExclusiveInput, lost LostJob) (LostJob, error) {
	since, err := runStep(e, ctx, gateLostStartName, func(context.Context) (time.Time, error) { return e.deps.Clock.reading(), nil })
	if err != nil {
		return lost, fmt.Errorf("record the start of the wait for the lost job %s: %w", lost.Job, err)
	}

	lost.Since = since

	for look := 0; ; look++ {
		seen, err := e.lookThenSleep(ctx, in.Project, lost, look)
		if err != nil {
			return lost, e.lostStopped(ctx, in, lost, err)
		}

		if seen.ends() {
			lost.Proof, lost.At, lost.Limit = seen.Proof, seen.At, seen.Limit

			return lost, nil
		}
	}
}

// ends is true for a look that ends the wait: it read a proof, or it came at the limit.
func (l lostLook) ends() bool {
	return l.Proof.Kind != "" || l.Limit
}

// lookThenSleep is look number k of the wait, a recorded step. A look that does not end the wait is followed by the durable sleep before the next look.
func (e *Engine) lookThenSleep(ctx dbos.Context, project string, lost LostJob, look int) (lostLook, error) {
	seen, err := runStep(e, ctx, gateLostLookName+"."+strconv.Itoa(look), func(c context.Context) (lostLook, error) { return e.lookLost(c, project, lost) })
	if err != nil || seen.ends() {
		return seen, err
	}

	_, err = dbos.Sleep(ctx, cmp.Or(e.deps.Clock.LostPoll, lostPoll))

	return seen, err
}

// lookLost reads the proof once, then the clock and the limit. A look with no proof at or after Since plus the limit ends the wait.
func (e *Engine) lookLost(ctx context.Context, project string, lost LostJob) (lostLook, error) {
	proof, err := e.lostProof(ctx, project, lost)
	if err != nil {
		return lostLook{}, err
	}

	now := e.deps.Clock.reading()

	return lostLook{Proof: proof, At: now, Limit: proof.Kind == "" && !now.Before(lost.Since.Add(e.lostJobWait(project)))}, nil
}

// lostProof asks the host keeper for the proof. A build with no host keeper has no proof, and the note says so.
func (e *Engine) lostProof(ctx context.Context, project string, lost LostJob) (model.LostProof, error) {
	if e.deps.Hosts == nil {
		return model.LostProof{Note: noHostReader}, nil
	}

	proof, err := e.deps.Hosts.LostProof(ctx, project, lost.Host, lost.Job)
	if err != nil {
		return proof, fmt.Errorf("read the proof that the lost job %s on host %s can no longer run: %w", lost.Job, lost.Host, err)
	}

	return proof, nil
}

// lostJobWait is host.lost_job_wait of the project as the config of this engine holds it. A project with no host block waits for the default.
func (e *Engine) lostJobWait(project string) time.Duration {
	if e.deps.Facts != nil {
		if block := e.deps.Facts(project).Host; block != nil {
			return block.LostJobWait
		}
	}

	return config.DefaultLostJobWait
}

// lostStopped ends a wait that failed. When the run was canceled (a newer head, or a person) and the engine still runs, it reads the proof once more
// under a context that the cancel does not reach, so the stop of the job is tried once more. A read with no proof is logged: the job can still run.
// It never cancels the job: a cancel waits for the job and banks its work.
func (e *Engine) lostStopped(ctx dbos.Context, in ExclusiveInput, lost LostJob, err error) error {
	if ctx.Err() == nil || e.ctx.Err() != nil {
		return err
	}

	proof, readErr := e.lostProof(context.WithoutCancel(ctx), in.Project, lost)
	if readErr != nil || proof.Kind == "" {
		e.logger.WarnContext(ctx, "the gate stopped while its lost job may still run on the host: the last stop of the job gave no proof",
			"item", in.Item, "job", lost.Job, "host", lost.Host, "note", proof.Note, "error", readErr)
	}

	return err
}

// voidOutputs is the result of a gate whose lost job has a proof: it did not pass, and every step of its list is unknown, so the workflow reads a void gate.
// host_loss holds the record of the loss. A gate with no list gives the one step of its command.
func voidOutputs(lost LostJob) map[string]any {
	names := lost.Required
	if len(names) == 0 {
		names = []string{gatepkg.StepCommand}
	}

	steps := make([]GateStep, len(names))
	for i, name := range names {
		steps[i] = GateStep{Name: name, Status: unknownStatus}
	}

	report := fmt.Sprintf("the gate job was lost on %s and the proof is %s", lost.Host, lost.Proof.Kind)
	outputs := gateOutputs(false, lost.Head, "", report, steps, lost.Required, nil)
	outputs[hostLossKey] = map[string]any{
		"kind": lost.Kind, "job": lost.Job, "host": lost.Host, "head_or_commit": lost.Head, "proof": lost.Proof.Kind, "note": lost.Proof.Note, "at": lost.At.UTC().Format(time.RFC3339),
	}

	return outputs
}
