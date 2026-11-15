package config

import (
	"encoding/json"
	"fmt"
	"math"

	"go.starlark.net/starlark"
)

// vars is the argument `vars`: a dict of the free settings of a workflow. The engine reads no key and no value.
// The facts of a run are a recorded JSON step, so a key must be a string and a value must be JSON data: None, a bool, a string, a number, a list, a tuple or a dict.
type vars map[string]any

// Unpack stores a dict as Go JSON values. It refuses any other value, and names the key of a value that a run cannot keep.
func (v *vars) Unpack(value starlark.Value) error {
	dict, ok := value.(*starlark.Dict)
	if !ok {
		return fmt.Errorf("got %s, want a dict", value.Type())
	}

	plain, err := plainDict(dict, "vars")
	if err != nil {
		return err
	}

	*v = plain

	return nil
}

// plain converts one value. A number keeps its Starlark spelling, so 1 stays a whole number and 1.5 stays a float. A tuple becomes a list.
func plain(value starlark.Value, at string) (any, error) {
	switch x := value.(type) {
	case starlark.NoneType:
		return nil, nil //nolint:nilnil // None is the nil JSON value
	case starlark.Bool, starlark.String, starlark.Int, starlark.Float:
		return plainScalar(x, at)
	case *starlark.List:
		return plainItems(x, at)
	case starlark.Tuple:
		return plainItems(x, at)
	case *starlark.Dict:
		return plainDict(x, at)
	}

	return nil, fmt.Errorf("%s holds a %s, want None, a bool, a string, a number, a list or a dict", at, value.Type())
}

func plainScalar(value starlark.Value, at string) (any, error) {
	switch x := value.(type) {
	case starlark.Bool:
		return bool(x), nil
	case starlark.String:
		return string(x), nil
	case starlark.Float:
		if math.IsInf(float64(x), 0) || math.IsNaN(float64(x)) {
			return nil, fmt.Errorf("%s holds %s, which JSON cannot keep", at, x.String())
		}

		return json.Number(x.String()), nil
	default:
		return json.Number(value.String()), nil
	}
}

func plainItems(seq starlark.Indexable, at string) (any, error) {
	items := make([]any, 0, seq.Len())

	for i := range seq.Len() {
		item, err := plain(seq.Index(i), fmt.Sprintf("%s[%d]", at, i))
		if err != nil {
			return nil, err
		}

		items = append(items, item)
	}

	return items, nil
}

func plainDict(dict *starlark.Dict, at string) (map[string]any, error) {
	out := make(map[string]any, dict.Len())

	for _, pair := range dict.Items() {
		key, isText := pair[0].(starlark.String)
		if !isText {
			return nil, fmt.Errorf("%s has the key %s of type %s, want a string key", at, pair[0].String(), pair[0].Type())
		}

		item, err := plain(pair[1], fmt.Sprintf("%s[%q]", at, string(key)))
		if err != nil {
			return nil, err
		}

		out[string(key)] = item
	}

	return out, nil
}
