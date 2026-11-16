package batch

import (
	"context"
	"encoding/json"
	"sort"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// goldenInputs holds the shape of the recorded input of each batch step: the field names and the type of each value, and for a list the shape of its
// first element. A replay compares this input byte for byte with the input of the step now, so a change to a field name, a field type or a field of
// a member strands the runs that recorded the step. The steps belong to the land module of the package flow.
var goldenInputs = map[string]string{
	"heads": "[{approved:bool,head:string,item:string}]",
	"union": "{members:[{approved:bool,head:string,item:string}],tipCommit:string,tipTree:string}",
	"gate":  "{members:[{approved:bool,head:string,item:string}],tree:string}",
}

// shapeOf reduces recorded JSON to its shape, so two runs of one call have one shape whatever their values are.
func shapeOf(value any) string {
	switch v := value.(type) {
	case map[string]any:
		fields := make([]string, 0, len(v))

		for name, field := range v {
			fields = append(fields, name+":"+shapeOf(field))
		}

		sort.Strings(fields)

		return "{" + strings.Join(fields, ",") + "}"
	case []any:
		if len(v) == 0 {
			return "[]"
		}

		return "[" + shapeOf(v[0]) + "]"
	case string:
		return "string"
	case bool:
		return "bool"
	case float64:
		return "number"
	default:
		return "null"
	}
}

// recordedInputShape is the shape of the input in the record of the first step of a kind.
func recordedInputShape(t *testing.T, w *world, kind string) string {
	t.Helper()

	var env struct {
		Input any `json:"input"`
	}

	raw, ok := w.store.data["batch:q."+kind+"#1"]
	require.True(t, ok, "the batch recorded no step of the kind %s", kind)
	require.NoError(t, json.Unmarshal(raw, &env))

	return shapeOf(env.Input)
}

func TestRun_TheRecordedInputOfEachBatchStepIsTheGoldenShape(t *testing.T) {
	t.Parallel()

	w := newWorld()
	_, err := Run(context.Background(), w.deps(), members("a", "b"))
	require.NoError(t, err)

	for kind, golden := range goldenInputs {
		assert.Equal(t, golden, recordedInputShape(t, w, kind),
			"the recorded input of the batch step %s changed: bump the version of the land module in moduleTable (internal/flow/stepmodules.go) and update the golden input", kind)
	}
}
