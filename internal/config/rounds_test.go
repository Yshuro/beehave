package config_test

import (
	"maps"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/tobocop2/beehave/internal/model"
)

const roundBase = `base = "integration/s1", owner = "tobocop2", lane_cap = 4, fix_rounds = 5`

func roundsBlock(extra string) string {
	return "\n" + call(localProject+", mutation = mutation(command = \"make mutation-pr BASE=origin/{base}\"), rounds = rounds("+roundBase+extra+")")
}

func TestLoadFolder_TheRoundsBlockGivesEveryKeyToTheProfile(t *testing.T) {
	t.Parallel()

	project, problems := loadProjectText(t, roundsBlock(`, next_base = "integration/s2", owner_email = "o@example.com", source_checkout = "/src", integration_pr = 7, merge = True, acceptance = "a.sh", base_result = "b.md", base_reds = ["TestX"], base_reds_file = "reds.md", lane_kind = "papers", allow_paths = ["docs"], wait_hours = 24, label = "beehave-round"`))

	require.Empty(t, problems)
	require.Equal(t, model.Rounds{
		Base: "integration/s1", NextBase: "integration/s2", Owner: "tobocop2", OwnerEmail: "o@example.com", SourceCheckout: "/src",
		LaneCap: 4, FixRounds: 5, IntegrationPR: 7, WaitHours: 24, Merge: true, Acceptance: "a.sh", BaseResult: "b.md", BaseRedsFile: "reds.md", LaneKind: "papers",
		BaseReds: []string{"TestX"}, AllowPaths: []string{"docs"}, Label: "beehave-round",
	}, project.Rounds)
}

func TestLoadFolder_ARoundsBlockOfTheRequiredKeysAndALabelTakesTheDefaults(t *testing.T) {
	t.Parallel()

	project, problems := loadProjectText(t, roundsBlock(`, label = "beehave-round"`))

	require.Empty(t, problems)
	require.Equal(t, model.Rounds{Base: "integration/s1", Owner: "tobocop2", LaneCap: 4, FixRounds: 5, LaneKind: "impl", BaseRedsFile: "base-reds.md", Label: "beehave-round"}, project.Rounds)
}

func TestLoadFolder_AProjectWithNoRoundsBlockHasNoRounds(t *testing.T) {
	t.Parallel()

	project, problems := loadProjectText(t, call(localProject))

	require.Empty(t, problems)
	require.Equal(t, model.Rounds{}, project.Rounds)
}

func TestLoadFolder_AnEmptyNextBaseAndAnEmptyAcceptanceAreValuesAndLeaveTheRoundWithoutACutOrAnAcceptance(t *testing.T) {
	t.Parallel()

	project, problems := loadProjectText(t, roundsBlock(`, label = "x", next_base = "", acceptance = ""`))

	require.Empty(t, problems)
	require.Empty(t, project.Rounds.NextBase)
	require.Empty(t, project.Rounds.Acceptance)
}

func TestLoadFolder_AnItemListSelectsTheListedItems(t *testing.T) {
	t.Parallel()

	project, problems := loadProjectText(t, roundsBlock(`, items = ["12", "bd-3"]`))

	require.Empty(t, problems)
	require.Equal(t, []string{"12", "bd-3"}, project.Rounds.Items)
	require.Empty(t, project.Rounds.Label)
}

func TestLoadFolder_ARoundsBlockWithBadValuesIsRefusedWithTheKeyAndTheFix(t *testing.T) {
	t.Parallel()

	for name, test := range map[string]struct{ extra, want string }{
		"no selector":             {``, "a round needs an item selector: set label or items"},
		"both selectors":          {`, label = "x", items = ["1"]`, "a round takes one item selector: set label or items, not both"},
		"an empty items list":     {`, items = []`, "a round needs an item selector"},
		"a blank label":           {`, label = " "`, "`rounds.label` must be a non-empty string"},
		"an item that is no id":   {`, items = ["not an id"]`, "`rounds.items` lists \"not an id\""},
		"a round id as an item":   {`, items = ["round/x"]`, "`rounds.items` lists \"round/x\""},
		"wait_hours zero":         {`, label = "x", wait_hours = 0`, "`rounds.wait_hours` is 0"},
		"integration_pr negative": {`, label = "x", integration_pr = -1`, "`rounds.integration_pr` is -1"},
		"an unknown lane kind":    {`, label = "x", lane_kind = "code"`, "`rounds.lane_kind` is \"code\". Write impl, papers or design"},
		"papers with no paths":    {`, label = "x", lane_kind = "papers"`, "`rounds.allow_paths` is empty"},
		"design with no paths":    {`, label = "x", lane_kind = "design"`, "`rounds.allow_paths` is empty"},
		"a blank base_reds_file":  {`, label = "x", base_reds_file = " "`, "`rounds.base_reds_file` must be a non-empty string"},
		"a blank base reds entry": {`, label = "x", base_reds = [" "]`, "`rounds.base_reds` lists a blank string"},
		"a text merge":            {`, label = "x", merge = "yes"`, "rounds"},
		"an unknown key":          {`, label = "x", color = "red"`, "color"},
	} {
		_, problems := loadProjectText(t, roundsBlock(test.extra))

		require.NotEmpty(t, problems, name)
		require.Contains(t, problems[0].Error(), "p.star:2:", name)
		require.Contains(t, problems[0].Error(), test.want, name)
	}
}

func TestLoadFolder_ARoundsCountBelowOneIsRefusedWithTheKey(t *testing.T) {
	t.Parallel()

	for key, want := range map[string]string{"lane_cap": "`rounds.lane_cap` is 0", "fix_rounds": "`rounds.fix_rounds` is -2"} {
		caps, fixes := "1", "1"
		if key == "lane_cap" {
			caps = "0"
		} else {
			fixes = "-2"
		}

		_, problems := loadProjectText(t, "\n"+call(localProject+`, rounds = rounds(base = "b", owner = "o", lane_cap = `+caps+`, fix_rounds = `+fixes+`, label = "x")`))

		require.NotEmpty(t, problems, key)
		require.Contains(t, problems[0].Error(), want, key)
	}
}

func TestLoadFolder_ARoundsCountAtItsLimitIsAcceptedAndOnePastItIsRefused(t *testing.T) {
	t.Parallel()

	for key, limit := range map[string]int{"lane_cap": 1, "fix_rounds": 1, "wait_hours": 1, "integration_pr": 0} {
		build := func(value int) string {
			counts := map[string]string{"lane_cap": "1", "fix_rounds": "1", "wait_hours": "1", "integration_pr": "0"}
			counts[key] = strconv.Itoa(value)

			return "\n" + call(localProject+`, mutation = mutation(command = "m"), rounds = rounds(base = "b", owner = "o", label = "x", lane_cap = `+counts["lane_cap"]+`, fix_rounds = `+counts["fix_rounds"]+
				`, wait_hours = `+counts["wait_hours"]+`, integration_pr = `+counts["integration_pr"]+`)`)
		}

		_, atLimit := loadProjectText(t, build(limit))
		require.Empty(t, atLimit, key)

		_, pastLimit := loadProjectText(t, build(limit-1))
		require.NotEmpty(t, pastLimit, key)
		require.Contains(t, pastLimit[0].Error(), "`rounds."+key+"` is "+strconv.Itoa(limit-1), key)
	}
}

func TestLoadFolder_ARoundsBlockThatLeavesOutARequiredKeyIsRefused(t *testing.T) {
	t.Parallel()

	for _, key := range []string{"base", "owner", "lane_cap", "fix_rounds"} {
		keys := map[string]string{"base": `base = "b"`, "owner": `owner = "o"`, "lane_cap": `lane_cap = 1`, "fix_rounds": `fix_rounds = 1`}
		delete(keys, key)

		args := slices.Sorted(maps.Values(keys))

		_, problems := loadProjectText(t, "\n"+call(localProject+`, rounds = rounds(label = "x", `+strings.Join(args, ", ")+")"))

		require.NotEmpty(t, problems, key)
		require.Contains(t, problems[0].Error(), key, key)
	}
}

func TestLoadFolder_ARoundsBaseThatCannotMakeARoundIdIsRefused(t *testing.T) {
	t.Parallel()

	for _, base := range []string{"a b", "/x", "x/", "a//b", ".."} {
		_, problems := loadProjectText(t, "\n"+call(localProject+`, rounds = rounds(base = "`+base+`", owner = "o", lane_cap = 1, fix_rounds = 1, label = "x")`))

		require.NotEmpty(t, problems, base)
		require.Contains(t, problems[0].Error(), "`rounds.base`", base)
	}
}

func TestLoadFolder_ASecondRoundsBlockIsRefused(t *testing.T) {
	t.Parallel()

	_, problems := loadProjectText(t, "_a = rounds("+roundBase+", label = \"x\")\n"+call(localProject+", rounds = rounds("+roundBase+`, label = "x")`))

	require.NotEmpty(t, problems)
}

func TestLoadFolder_ARoundsBlockWithABlankBaseOrOwnerIsRefusedWithTheKey(t *testing.T) {
	t.Parallel()

	for key, block := range map[string]string{
		"rounds.base":  `base = " ", owner = "o"`,
		"rounds.owner": `base = "b", owner = ""`,
	} {
		_, problems := loadProjectText(t, "\n"+call(localProject+", rounds = rounds("+block+`, lane_cap = 1, fix_rounds = 1, label = "x")`))

		require.NotEmpty(t, problems, key)
		require.Contains(t, problems[0].Error(), "`"+key+"` must be a non-empty string", key)
	}
}

func TestLoadFolder_AnExplicitImplLaneKindNeedsNoAllowedPaths(t *testing.T) {
	t.Parallel()

	project, problems := loadProjectText(t, roundsBlock(`, label = "x", lane_kind = "impl"`))

	require.Empty(t, problems)
	require.Equal(t, "impl", project.Rounds.LaneKind)
}
