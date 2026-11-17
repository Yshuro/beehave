package opencode

import (
	"context"

	"github.com/tobocop2/beehave/internal/model"
)

// Name is the name the OpenCode agent is chosen by.
const Name model.Agent = "opencode"

// ReadUsage returns an unknown reading, because OpenCode has no usage report. The pool never picks a runner whose usage is unknown.
func (Adapter) ReadUsage(context.Context, model.Runner) model.Reading {
	return model.Unknown("the " + string(Name) + " agent cannot report usage")
}
