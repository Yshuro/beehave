package flow

import (
	"fmt"
	"regexp"
	"slices"
	"strconv"
	"strings"
)

// The builtins of this package belong to eight modules that move out of it one by one. Each module has a version of its recorded steps.
// The application version holds all eight, so a change to the steps of one module strands only the runs that recorded a step of that module.
// A change to a builtin that changes its recorded steps bumps the version of its module in moduleTable, and no other.
const (
	moduleCore    = "core"
	moduleAgent   = "agent"
	moduleTracker = "tracker"
	moduleForge   = "forge"
	moduleLand    = "land"
	moduleGate    = "gate"
	moduleHost    = "host"
	moduleRound   = "round"
)

// Step kinds that no other file names as a constant.
const (
	kindPreflight = "preflight"
	kindQueue     = "queue"
	// kindBatch starts the names of the steps of batch_merge, which has no counted step of its own.
	kindBatch = "batch"
)

// stepModule is one module of the table: its version and the step kinds that it records.
type stepModule struct {
	name    string
	version int
	kinds   []string
}

// moduleTable assigns each recorded step kind to its module. The order is the order of the application version.
func moduleTable() []stepModule {
	return []stepModule{
		{name: moduleCore, version: 1, kinds: []string{"command", "earlier_calls", envKeyword, "facts", "gather", "now", "spawn", "wait", "write"}},
		{name: moduleAgent, version: 1, kinds: []string{agentStepName, loginStepName}},
		{name: moduleTracker, version: 1, kinds: []string{"item_assign", "item_comment", "item_create", "item_read", "item_status"}},
		{name: moduleForge, version: 1, kinds: []string{"board_sync", "pr_assign", "pr_comment", "pr_open"}},
		{name: moduleLand, version: 1, kinds: []string{kindBatch, mergeKey, kindPreflight}},
		{name: moduleGate, version: 1, kinds: []string{"before_review", kindGate}},
		{name: moduleHost, version: 1, kinds: []string{"host_teardown", "host_up"}},
		{name: moduleRound, version: 1, kinds: []string{kindQueue, scopeStep, scopeCheckStep}},
	}
}

// moduleOfKind returns the module that records a step kind, and false for a kind that no module records.
func moduleOfKind(kind string) (string, bool) {
	for _, module := range moduleTable() {
		if slices.Contains(module.kinds, kind) {
			return module.name, true
		}
	}

	return "", false
}

// moduleVersions is the list of module versions of this build, such as core1.agent1.tracker1.forge1.land1.gate1.host1.round1.
func moduleVersions() string {
	table := moduleTable()
	parts := make([]string, len(table))

	for i, module := range table {
		parts[i] = module.name + strconv.Itoa(module.version)
	}

	return strings.Join(parts, ".")
}

// moduleVersionPart matches one module and its version in a stored list of module versions.
var moduleVersionPart = regexp.MustCompile(`^([a-z]+)([0-9]+)$`)

// parseVersions reads the steps part of a stored application version as one version for each module.
// The single version of the builtin steps that came before the modules (stepsVersion) stands for every module at version 1.
// Any other single version (such as star13), a part that is not the version of a module of the table, and an empty text are not readable.
// A build tag such as rc1 has the shape of a module version, so only the names of the table decide.
func parseVersions(steps string) (map[string]int, bool) {
	versions := map[string]int{}

	if steps == stepsVersion {
		for _, module := range moduleTable() {
			versions[module.name] = 1
		}

		return versions, true
	}

	for part := range strings.SplitSeq(steps, ".") {
		name, number, ok := moduleVersionOf(part)
		if !ok {
			return nil, false
		}

		versions[name] = number
	}

	return versions, true
}

// moduleVersionOf reads one part of a stored list, such as gate2. The name must be a module of the table.
func moduleVersionOf(part string) (name string, number int, ok bool) {
	match := moduleVersionPart.FindStringSubmatch(part)
	if match == nil || !slices.ContainsFunc(moduleTable(), func(module stepModule) bool { return module.name == match[1] }) {
		return "", 0, false
	}

	number, err := strconv.Atoi(match[2])

	return match[1], number, err == nil
}

// kindOfStep returns the step kind that a recorded step name starts with. The names are `<kind>:<label>#<n>`, with a sub-step after a slash
// and a round after an at sign. A name that starts with no kind of a module, such as claim or DBOS.sleep, is not a step of a module.
func kindOfStep(name string) (string, bool) {
	kind, _, _ := strings.Cut(name, "/")

	if at := strings.IndexAny(kind, ":#@"); at >= 0 {
		kind = kind[:at]
	}

	_, known := moduleOfKind(kind)

	return kind, known
}

// modulesUsed returns the modules that recorded one of the steps, in the order of the table.
func modulesUsed(names []string) []string {
	used := map[string]bool{}

	for _, name := range names {
		if kind, known := kindOfStep(name); known {
			module, _ := moduleOfKind(kind)
			used[module] = true
		}
	}

	var ordered []string

	for _, module := range moduleTable() {
		if used[module.name] {
			ordered = append(ordered, module.name)
		}
	}

	return ordered
}

// changedModule returns the first of the modules whose version in was is not the version in now. A module that a list does not hold has no version, so it differs.
func changedModule(modules []string, was, now map[string]int) (string, bool) {
	for _, module := range modules {
		old, hadOld := was[module]
		current, hasCurrent := now[module]

		if !hadOld || !hasCurrent || old != current {
			return module, true
		}
	}

	return "", false
}

// describeModule names the version of a module in a list, for a message.
func describeModule(module string, versions map[string]int) string {
	if number, ok := versions[module]; ok {
		return fmt.Sprintf("version %d", number)
	}

	return "no version"
}
