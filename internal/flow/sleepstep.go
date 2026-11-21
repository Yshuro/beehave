package flow

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/dbos-inc/dbos-transact-golang/dbos"
	"go.starlark.net/starlark"

	"github.com/tobocop2/beehave/internal/workflow"
)

const (
	sleepKind = "sleep"
	// sleepWake is the sub-step that records the wake time of a sleep call before the sleep starts.
	sleepWake = "/wake"
)

// sleep waits for the duration with the durable sleep of DBOS. The call records its wake time first, so a run that recovers during the sleep waits only
// for the time left. The result holds the seconds that this execution slept.
func (s *script) sleep(b *starlark.Builtin, args starlark.Tuple, kwargs []starlark.Tuple) (starlark.Value, error) { //nolint:ireturn // the builtin contract of starlark-go
	label, length, err := sleepArgs(b, args, kwargs)
	if err != nil {
		return nil, err
	}

	wake, err := runStep(s.e, s.ctx, s.nextName(sleepKind, label)+sleepWake, func(context.Context) (int64, error) {
		return time.Now().Add(length).UnixMilli(), nil
	})
	if err != nil {
		return nil, err
	}

	slept, err := dbos.Sleep(s.ctx, time.Until(time.UnixMilli(wake)))
	if err != nil {
		return nil, fmt.Errorf("sleep of item %s: %w", s.in.Item, err)
	}

	return s.durable(sleepKind, label, func(context.Context, string) (stepResult, error) {
		return stepResult{Outputs: sleepOutputs(slept)}, nil
	})
}

// sleepArgs reads the label and the duration of a sleep call with the rules of the check.
func sleepArgs(b *starlark.Builtin, args starlark.Tuple, kwargs []starlark.Tuple) (label string, length time.Duration, err error) {
	var duration string

	if err = workflow.Unpack(b.Name(), args, kwargs, map[string]any{"duration": &duration, nameKeyword: &label}); err != nil {
		return "", 0, err
	}

	length, err = workflow.Deadline("duration", duration)

	return label, length, err
}

// sleepOutputs are the result fields of a sleep: the seconds that this execution slept.
func sleepOutputs(slept time.Duration) map[string]any {
	return map[string]any{"slept": slept.Seconds()}
}

// sleepDeadline reads the wake time of the sleep that an open run is in. The sleep has recorded its wake time, and the sleep or the record of its end is the last record.
// It is the zero time for a run that does not sleep, and for a record that is not a time.
func sleepDeadline(records []dbos.StepInfo) time.Time {
	if len(records) > 0 && records[len(records)-1].StepName == sleepStep {
		records = records[:len(records)-1]
	}

	if len(records) == 0 {
		return time.Time{}
	}

	last := records[len(records)-1]
	if kind, _ := kindOfStep(last.StepName); kind != sleepKind || !strings.HasSuffix(last.StepName, sleepWake) {
		return time.Time{}
	}

	millis, err := strconv.ParseInt(strings.Trim(fmt.Sprint(last.Output), `"`), 10, 64)
	if err != nil {
		return time.Time{}
	}

	return time.UnixMilli(millis)
}
