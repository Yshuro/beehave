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

// coreVersion is the version of the core module. Version 2 added the vars field to the result of facts. Version 3 removed the round_* fields from it.
const coreVersion = 3

// agentVersion is the version of the agent module. Version 2 added the limit field to the phase output, and the limit step and the later looks at the caps
// that an agent call records when a provider limit stops its run. Version 3 added the start_head field to the phase input, the session and diff_stat fields
// to the record of a lost step, and the resumed field to the phase output. Version 4 added the tree_start field to the phase input.
// Version 5 writes the cost of a failed run to the stream of failed runs in place of its spent step.
const agentVersion = 5

// landVersion is the version of the land module. Version 2 added the approved field to each member in the recorded input of the batch steps.
// Version 3 added the refusal field to the result of a merge, which the reason of a batch member reads.
// Version 4 records the host job of the project gate of a batch in its own step (batch_gate:job) before the gate waits, records a lost job
// that has a proof in the item run (batch:<call>.gate#<n>/host-loss, and @2 for the rerun), and adds host_loss and host_losses to the result of batch_merge.
const landVersion = 4

// forgeVersion is the version of the forge module. Version 2 added the pr_state step.
const forgeVersion = 2

// gateVersion is the version of the gate module. Version 3 records a lost host job in the gate step, then the wait for its proof (gate:lost-start, gate:lost-look.<k>
// with a wait field), and in the item run the read of the proof after each answer of the owner (<call>/lost-ask.<n>).
// At the wait limit the host job run ends with no failure and records the lost job with its wait.
// Version 4 records a lost job that has a proof in the item run (<call>/host-loss), and the gate step records host_losses, the count of the host losses of the item tree.
// Version 5 added the revert field to the results of gate and before_review, and the revert run to the checks of a candidate gate.
const gateVersion = 5

// roundVersion is the version of the round module. Version 2 added the step of the engine that makes the worktree at the start of every round item.
// No builtin records that step, so a move to this build compares the round module for every run of a round item (requireSameRoundModule).
const roundVersion = 2

// stepModule is one module of the table: its version and the step kinds that it records.
type stepModule struct {
	name    string
	version int
	kinds   []string
}

// moduleTable assigns each recorded step kind to its module. The order is the order of the application version.
func moduleTable() []stepModule {
	return []stepModule{
		{name: moduleCore, version: coreVersion, kinds: []string{"command", "earlier_calls", envKeyword, "facts", "gather", "now", "spawn", "wait", "write"}},
		{name: moduleAgent, version: agentVersion, kinds: []string{agentStepName, loginStepName}},
		{name: moduleTracker, version: 1, kinds: []string{"item_assign", "item_comment", "item_create", "item_read", "item_status"}},
		{name: moduleForge, version: forgeVersion, kinds: []string{"board_sync", "pr_assign", "pr_comment", "pr_open", prStateKind}},
		{name: moduleLand, version: landVersion, kinds: []string{kindBatch, mergeKey, kindPreflight}},
		{name: moduleGate, version: gateVersion, kinds: []string{"before_review", kindGate}},
		{name: moduleHost, version: 1, kinds: []string{"host_teardown", "host_up"}},
		{name: moduleRound, version: roundVersion, kinds: []string{kindQueue, scopeStep, scopeCheckStep}},
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

// moduleVersions is the list of module versions of this build, such as core3.agent5.tracker1.forge2.land4.gate5.host1.round2.
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
	versions, err := readVersions(steps)

	return versions, err == nil
}

// readVersions is parseVersions with the reason: the error names the bad entry of a list, or the module that a list names twice.
func readVersions(steps string) (map[string]int, error) {
	if steps == stepsVersion {
		return allModulesAtOne(), nil
	}

	versions := map[string]int{}

	for part := range strings.SplitSeq(steps, ".") {
		name, number, ok := moduleVersionOf(part)
		if !ok {
			return nil, fmt.Errorf("the entry %q of the step list %q is not the name of a module and its version, such as gate2", part, steps)
		}

		if _, twice := versions[name]; twice {
			return nil, fmt.Errorf("the step list %q names the module %s twice", steps, name)
		}

		versions[name] = number
	}

	return versions, nil
}

// allModulesAtOne is the list that the single version of the steps stands for.
func allModulesAtOne() map[string]int {
	versions := map[string]int{}

	for _, module := range moduleTable() {
		versions[module.name] = 1
	}

	return versions
}

// moduleListShape matches a text of entries that each have a name and an optional number, joined by dots.
var moduleListShape = regexp.MustCompile(`^[a-z]+[0-9]*(\.[a-z]+[0-9]*)*$`)

// looksLikeModuleList is true for a text that starts with the name of a module and has the shape of a list of module versions, even when an entry is
// malformed or a module repeats. A build tag such as rc1 does not start with the name of a module, so it is not a list.
func looksLikeModuleList(text string) bool {
	if !moduleListShape.MatchString(text) {
		return false
	}

	first, _, _ := strings.Cut(text, ".")
	first = strings.TrimRight(first, "0123456789")

	return slices.ContainsFunc(moduleTable(), func(module stepModule) bool { return module.name == first })
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

	kind = kind[:strings.IndexAny(kind+":", ":#@")]

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
