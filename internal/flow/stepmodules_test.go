package flow

import (
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// versionWithModule is the application version of a build whose module is at another version than the table says.
func versionWithModule(build, module string, version int) string {
	table := moduleTable()
	parts := make([]string, len(table))

	for i, entry := range table {
		number := entry.version
		if entry.name == module {
			number = version
		}

		parts[i] = entry.name + strconv.Itoa(number)
	}

	return build + "+" + strings.Join(parts, ".")
}

func TestStepModuleTable_EveryStepKindOfTheModuleBelongsToExactlyOneModule(t *testing.T) {
	t.Parallel()

	owners := map[string][]string{}

	for _, module := range moduleTable() {
		for _, kind := range module.kinds {
			owners[kind] = append(owners[kind], module.name)
		}
	}

	for kind, modules := range owners {
		assert.Len(t, modules, 1, "the step kind %q is in the modules %v", kind, modules)
	}

	for _, kind := range StepModule().Kinds {
		assert.Contains(t, owners, kind, "the step kind %q has no module", kind)
	}
}

func TestStepModuleTable_NamesNoStepKindThatTheModuleDoesNotRecord(t *testing.T) {
	t.Parallel()

	recorded := StepModule().Kinds

	for _, module := range moduleTable() {
		for _, kind := range module.kinds {
			assert.True(t, kind == kindBatch || slices.Contains(recorded, kind), "the module %s names the kind %q, which no builtin records", module.name, kind)
		}
	}
}

func TestStepModuleTable_HoldsTheEightModulesOnceEach(t *testing.T) {
	t.Parallel()

	var names []string

	for _, module := range moduleTable() {
		names = append(names, module.name)
	}

	assert.Equal(t, []string{"core", "agent", "tracker", "forge", "land", "gate", "host", "round"}, names)
}

func TestApplicationVersion_HoldsOneVersionForEachModule(t *testing.T) {
	t.Parallel()

	assert.Equal(t, "v1+core3.agent3.tracker1.forge2.land3.gate4.host1.round1", applicationVersion("v1"))
	assert.Equal(t, "v1+core3.agent3.tracker1.forge2.land3.gate5.host1.round1", versionWithModule("v1", moduleGate, 5))
}

func TestStepModules_TheGateModuleIsAtVersionFour(t *testing.T) {
	t.Parallel()

	assert.Contains(t, strings.Split(moduleVersions(), "."), "gate4", "the gate step records a lost job and host_losses, the wait for its proof records gate:lost-start and gate:lost-look.<k>, "+
		"the owner wait records <call>/lost-ask.<n>, and the item run records a lost job with a proof as <call>/host-loss")
}

func TestParseVersions_TheSingleVersionOfTheStepsIsEveryModuleAtVersionOne(t *testing.T) {
	t.Parallel()

	got, ok := parseVersions(stepsVersion)

	require.True(t, ok)
	assert.Len(t, got, len(moduleTable()))

	for _, module := range moduleTable() {
		assert.Equal(t, 1, got[module.name], module.name)
	}
}

func TestParseVersions_ReadsAListAndRefusesAnOlderSingleVersionAndNoise(t *testing.T) {
	t.Parallel()

	got, ok := parseVersions("core2.gate3")
	require.True(t, ok)
	assert.Equal(t, map[string]int{"core": 2, "gate": 3}, got)

	for _, text := range []string{"star12", "", "core", "core1..gate1", "core99999999999999999999"} {
		_, ok = parseVersions(text)
		assert.False(t, ok, "%q is not readable", text)
	}
}

func TestReadVersions_NamesTheBadEntryAndTheModuleThatRepeats(t *testing.T) {
	t.Parallel()

	_, err := readVersions("core1.gate")
	require.ErrorContains(t, err, `entry "gate" of the step list "core1.gate"`)

	_, err = readVersions("core1.gate2.core3")
	require.ErrorContains(t, err, "names the module core twice")
}

func TestSplitVersion_KeepsAMalformedListOfModuleVersionsAsTheStepList(t *testing.T) {
	t.Parallel()

	for _, tail := range []string{"core1.gate", "core1.gatex", "core1.core2"} {
		build, steps := splitVersion("v1+" + tail)
		assert.Equal(t, "v1", build)
		assert.Equal(t, tail, steps)
	}
}

func TestSplitVersion_ReadsTheListOfModuleVersionsAndKeepsABuildTagThatLooksLikeOne(t *testing.T) {
	t.Parallel()

	build, steps := splitVersion("v1+rc1+core1.gate2")
	assert.Equal(t, "v1+rc1", build)
	assert.Equal(t, "core1.gate2", steps)

	build, steps = splitVersion("v1+rc1")
	assert.Equal(t, "v1+rc1", build, "rc1 has the shape of a module version, but no module is named rc")
	assert.Empty(t, steps)

	build, steps = splitVersion("+core1.gate2")
	assert.Empty(t, build, "a plus sign at the start has an empty build version before it")
	assert.Equal(t, "core1.gate2", steps)

	build, steps = splitVersion("v1+star30")
	assert.Equal(t, "v1", build)
	assert.Equal(t, "star30", steps)
}

func TestModulesUsed_AreTheModulesOfTheKindsThatTheStepsStartWith(t *testing.T) {
	t.Parallel()

	assert.Equal(t, []string{"tracker"}, modulesUsed([]string{"claim", "item_read:own#1", "DBOS.sleep", "tree", "item_comment:c#2"}))
	assert.Equal(t, []string{"core", "agent", "land"}, modulesUsed([]string{"agent:a#1/render", "wait:w#1", "batch:b.issues", "now#1"}))
	assert.Equal(t, []string{"gate", "round"}, modulesUsed([]string{"gate:g#1", "scope:g#1", "gate:tip@2"}))
	assert.Empty(t, modulesUsed(nil))
}

func TestChangedModule_OnlyAModuleThatTheRunUsedCounts(t *testing.T) {
	t.Parallel()

	was, _ := parseVersions(stepsVersion)
	now, _ := parseVersions(versionWithModule("", moduleGate, gateVersion+1)[1:])

	_, changed := changedModule(modulesUsed([]string{"item_read:own#1", "item_comment:c#2"}), was, now)
	assert.False(t, changed, "a run that recorded only tracker steps does not use the gate module")

	module, changed := changedModule(modulesUsed([]string{"item_read:own#1", "gate:g#1"}), was, now)
	assert.True(t, changed)
	assert.Equal(t, moduleGate, module)

	module, changed = changedModule([]string{moduleHost}, was, map[string]int{})
	assert.True(t, changed, "a module that this build does not hold has no version, so it differs")
	assert.Equal(t, moduleHost, module)
}

func TestDescribeModule_NamesTheVersionOrItsAbsence(t *testing.T) {
	t.Parallel()

	assert.Equal(t, "version 2", describeModule("gate", map[string]int{"gate": 2}))
	assert.Equal(t, "no version", describeModule("gate", map[string]int{}))
}
