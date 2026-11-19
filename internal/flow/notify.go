package flow

import (
	"context"
	"slices"
	"strings"
	"time"

	"github.com/dbos-inc/dbos-transact-golang/dbos"
)

const (
	// notifyStep is the recorded step that runs the notify command of the project once for each wait. A replay reads the record and runs nothing.
	notifyStep = "wait:notify"
	// notifyTimeout bounds the notify command. The runner kills a command that passes it.
	notifyTimeout = 30 * time.Second
)

// announce runs the notify command of the project when a wait opens: the configured list, then the item id and the question, each as one argument.
// The step is recorded at every wait, with or without a notify command, so the steps of a run do not depend on the config of the process that resumes it.
// A notify that fails, cannot start or hangs is logged and never fails the wait. The step error is a cancel or a refused record, and the receive that follows reports it.
func (e *Engine) announce(ctx dbos.Context, project, item, question string) {
	_, _ = runStep(e, ctx, notifyStep, func(c context.Context) (struct{}, error) { //nolint:errcheck // the receive of the wait reports a cancel or a refused record
		if e.deps.Facts == nil {
			return struct{}{}, nil
		}

		notify := e.deps.Facts(project).Notify
		if len(notify) == 0 {
			return struct{}{}, nil
		}

		e.warnNotify(c, item, CommandRequest{Project: project, Item: item, Run: append(slices.Clone(notify), item, e.redact(question)), Timeout: notifyTimeout})

		return struct{}{}, nil
	})
}

// warnNotify runs the notify request and logs a command that did not run or exited non-zero.
func (e *Engine) warnNotify(ctx context.Context, item string, request CommandRequest) {
	out, err := e.deps.Commands.Run(ctx, request)

	switch {
	case err != nil:
		e.logger.WarnContext(ctx, "the notify command did not run; the wait still opens", "item", item, "error", err)
	case out.RC != 0:
		e.logger.WarnContext(ctx, "the notify command failed; the wait still opens", "item", item, "exit", out.RC, "stderr", lastLine(out.Stderr))
	}
}

// lastLine is the last non-empty line of text.
func lastLine(text string) string {
	lines := strings.Split(strings.TrimSpace(text), "\n")

	return strings.TrimSpace(lines[len(lines)-1])
}
