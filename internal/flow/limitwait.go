package flow

import (
	"context"
	"fmt"
	"strconv"
	"time"

	"github.com/tobocop2/beehave/internal/agent"
)

// LimitWait is a provider limit that holds an agent step until it resets. The step sleeps until WaitUntil, then runs the agent call again with the same login.
// The view shows it on the run and on the line of the held step.
type LimitWait struct {
	// Limit is the limit that the agent reported: its kind, its window and its reset time.
	Limit agent.Limit `json:"limit"`
	// Login is the login whose limit stopped the run. The rerun keeps it, because the session of the run lives in its config folder.
	Login string `json:"login"`
	// WaitUntil is the end of the sleep, fixed when the limit was recorded, so a replay sleeps until the same time.
	WaitUntil time.Time `json:"wait_until"`
}

// text is the note of the wait for the human view of the status.
func (w LimitWait) text() string {
	return "waits for the " + w.Limit.Name() + " of login " + w.Login + " until " + w.WaitUntil.Format(time.RFC3339)
}

// limitRecord is what the limit step of an agent call records after a run that a provider limit stopped: the wait, the session and the cost of the run.
// Passed is true when the reset time had passed when the step ran, so the wait is the least wait.
type limitRecord struct {
	LimitWait

	Session string `json:"session,omitempty"`
	agent.Spend

	Passed bool `json:"reset_passed,omitempty"`
}

// waitOutLimit records the limit that stopped run number run of a try, with the cost of that run that the caps read, and sleeps until the limit resets.
// It reports whether the reset time had passed, so the next run of the try fails its phase when its limit has a past reset time too.
func (s *script) waitOutLimit(name string, in PhaseInput, ended PhaseOutput, try, run int) (bool, error) {
	record, err := runStep(s.e, s.ctx, name+"/limit"+strconv.Itoa(try)+"."+strconv.Itoa(run), func(context.Context) (limitRecord, error) {
		limit := *ended.Limit

		return limitRecord{
			LimitWait: LimitWait{Limit: limit, Login: in.Login, WaitUntil: limit.ResetsAt}, Session: ended.Session, Spend: ended.Spend,
			Passed: !limit.ResetsAt.After(s.e.deps.Clock.wall()),
		}, nil
	})
	if err != nil {
		if s.running() {
			s.countFailedRun(name, try, ended.Spend)
		}

		return false, err
	}

	return record.Passed, s.waitForReset(record.WaitUntil)
}

// dateLimit gives a limit that carries no reset time the reset time that the usage read of the pool gives for the login of the run, so the wait goes on as if the agent had given it.
// The step records the dated limit, so a replay never reads the usage again. A read that knows no time leaves the limit undated, and the end of the phase fails it (see limitEnd).
func (e *Engine) dateLimit(ctx context.Context, in PhaseInput, out *PhaseOutput) error {
	if out.Outcome != agent.OutcomeLimited || out.Limit == nil || !out.Limit.ResetsAt.IsZero() {
		return nil
	}

	reset, known, err := e.deps.Logins.Reset(ctx, in.Project, in.Login)
	if err != nil {
		return fmt.Errorf("read the usage of login %s for the reset of the %s: %w. Run beehave resume %s when the usage read works", in.Login, out.Limit.Name(), err, in.Item)
	}

	if known {
		limit := *out.Limit
		limit.ResetsAt = reset
		out.Limit = &limit
	}

	return nil
}

// limitEnd fails the phase of a run that a provider limit stopped when no wait has an end: the limit gives no reset time, or it is the second limit
// in a row whose reset time has passed. The failed phase is the failed step, so beehave resume reopens the phase and runs the agent again.
func limitEnd(in PhaseInput, limit agent.Limit, now time.Time) error {
	switch {
	case limit.ResetsAt.IsZero():
		return fmt.Errorf("the agent step of item %s stopped at the %s of login %s, and neither the agent nor the usage read gives a reset time. Run beehave resume %s when the limit is gone",
			in.Item, limit.Name(), in.Login, in.Item)
	case in.ResetPassed && !limit.ResetsAt.After(now):
		return fmt.Errorf("the agent step of item %s stopped at the %s of login %s twice in a row with a reset time that has passed (%s), so the reset time is wrong. "+
			"Run beehave resume %s when the limit is gone", in.Item, limit.Name(), in.Login, limit.ResetsAt.Format(time.RFC3339), in.Item)
	}

	return nil
}
