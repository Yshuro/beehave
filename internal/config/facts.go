package config

import (
	"go.starlark.net/starlark"
	"go.starlark.net/syntax"
)

// movedKey reports whether a key was an argument of project() before the free settings moved into vars.
func movedKey(key string) bool {
	switch key {
	case "people", "epics", "gate_files", "conventions", "brief_rules", "stacked_rules", "limits", "traps", "token_rules", "changelog", "board":
		return true
	}

	return false
}

// refuseMovedKeys refuses a call that still writes a moved key at the top, and names the key and the fix.
func refuseMovedKeys(at syntax.Position, kwargs []starlark.Tuple) error {
	for _, pair := range kwargs {
		key, _ := starlark.AsString(pair[0])

		if movedKey(key) {
			return refuseAt(at, "`%s` is no argument of project(). Move it into `vars`: vars = {%q: ...}.", key, key)
		}
	}

	return nil
}
