package config

import (
	"fmt"
	"maps"
	"regexp"
	"slices"

	"go.starlark.net/starlark"
	"go.starlark.net/syntax"
)

// className is the name of a slot class. It is a plain word, because the engine names the queue of the class with it.
var className = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]*$`)

// slotTable is the slots argument of the host block: a dict of slot class names to counts. The range of the counts is checked later, with the position of the block.
type slotTable map[string]starlark.Int

// Unpack stores a dict of class names to whole numbers. It refuses any other value, and a count that is not a whole number names its class.
func (t *slotTable) Unpack(v starlark.Value) error {
	dict, ok := v.(*starlark.Dict)
	if !ok {
		return fmt.Errorf("`host.slots`: got %s, want a dict of class names to counts", v.Type())
	}

	*t = slotTable{}

	for _, item := range dict.Items() {
		class, count, err := slotEntry(item)
		if err != nil {
			return err
		}

		(*t)[class] = count
	}

	return nil
}

// slotEntry reads one entry of the dict: a class name and a whole number.
func slotEntry(item starlark.Tuple) (string, starlark.Int, error) {
	class, isText := starlark.AsString(item[0])
	if !isText {
		return "", starlark.Int{}, fmt.Errorf("`host.slots` has the key %s, want a class name", item[0].String())
	}

	count, isInt := item[1].(starlark.Int)
	if !isInt {
		return "", starlark.Int{}, fmt.Errorf("`host.slots` class %q holds a %s, want a whole number above zero", class, item[1].Type())
	}

	return class, count, nil
}

// slotsOf checks the slot classes of a host block and returns them. A block that names none gives nil.
func slotsOf(at syntax.Position, table slotTable) (map[string]int, error) {
	if len(table) == 0 {
		return nil, nil //nolint:nilnil // a host that names no class has no slot map
	}

	slots := make(map[string]int, len(table))

	for _, class := range slices.Sorted(maps.Keys(table)) {
		if !className.MatchString(class) {
			return nil, refuseAt(at, "`host.slots` has the class %q. Use letters, digits, dot, underscore and hyphen, and start with a letter or a digit.", class)
		}

		count, fits := table[class].Int64()
		if !fits || count < 1 || count > maxCount {
			return nil, refuseAt(at, "`host.slots` class %q must be above zero and at most %d; got %s. Fix the value.", class, maxCount, table[class])
		}

		slots[class] = int(count)
	}

	return slots, nil
}

// SlotCount returns the number of jobs of one slot class that a build host runs at once, and whether any project names the class on the host.
// Projects share a host by name, so a host that two projects declare runs as many jobs as the project with the larger count allows.
func SlotCount(projects map[string]Project, cluster, class string) (int, bool) {
	count := 0

	for _, project := range projects {
		if project.Host == nil {
			continue
		}

		if _, err := project.HostFor(cluster); err == nil {
			count = max(count, project.Host.Slots[class])
		}
	}

	return count, count > 0
}
