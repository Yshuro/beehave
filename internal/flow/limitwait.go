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

// waitOutLimit records the limit that stopped run number run of a try, adds the cost of that run to the sum of the run cap, and sleeps until the limit resets.
// passed tells whether the limit of the run before it in the same try had a reset time that had passed; the result tells the same of this limit.
// A limit with no reset time, or a second limit in a row whose reset time has passed, fails the call: no wait has an end.
func (s *script) waitOutLimit(name string, in PhaseInput, ended PhaseOutput, try, run int, passed bool) (bool, error) {
	record, err := runStep(s.e, s.ctx, name+"/limit"+strconv.Itoa(try)+"."+strconv.Itoa(run), func(context.Context) (limitRecord, error) {
		return s.judgeLimit(name, in, ended, passed)
	})
	if err != nil {
		s.countFailedRun(name, try, ended.Spend)

		return false, err
	}

	s.spent = s.spent.Plus(record.Spend)

	return record.Passed, s.waitForReset(record.WaitUntil)
}

// judgeLimit fixes the time to wait for after a limited run, or fails when no wait has an end.
func (s *script) judgeLimit(name string, in PhaseInput, ended PhaseOutput, passed bool) (limitRecord, error) {
	limit := *ended.Limit
	record := limitRecord{LimitWait: LimitWait{Limit: limit, Login: in.Login, WaitUntil: limit.ResetsAt}, Session: ended.Session, Spend: ended.Spend}

	switch {
	case limit.ResetsAt.IsZero():
		return record, fmt.Errorf("the agent step %s stopped at the %s of login %s, and the agent gives no reset time. Run beehave resume %s when the limit is gone",
			name, limit.Name(), in.Login, in.Item)
	case limit.ResetsAt.After(s.e.deps.Clock.wall()):
		return record, nil
	case passed:
		return record, fmt.Errorf("the agent step %s stopped at the %s of login %s twice in a row with a reset time that has passed (%s), so the reset time is wrong. "+
			"Run beehave resume %s when the limit is gone", name, limit.Name(), in.Login, limit.ResetsAt.Format(time.RFC3339), in.Item)
	}

	record.Passed = true

	return record, nil
}
