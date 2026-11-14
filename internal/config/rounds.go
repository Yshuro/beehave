package config

import (
	"errors"
	"maps"
	"slices"

	"go.starlark.net/starlark"
	"go.starlark.net/syntax"

	"github.com/tobocop2/beehave/internal/model"
)

// defaultBaseRedsFile is the file of the known base reds when the block leaves `base_reds_file` out.
const defaultBaseRedsFile = "base-reds.md"

// rawRounds is a rounds block as written. At is the position of its call.
type rawRounds struct {
	At                                             syntax.Position
	Base, Owner                                    text
	LaneCap, FixRounds                             whole
	NextBase, OwnerEmail, SourceCheckout           text
	IntegrationPR, WaitHours                       whole
	Merge                                          bool
	Acceptance, BaseResult, BaseRedsFile, LaneKind text
	BaseReds, AllowPaths, Items                    paths
	Label                                          text
}

// roundsSpec is the schema of the rounds constructor. The required keys come first.
func roundsSpec() spec {
	return spec{"rounds", []string{
		"base", "owner", "lane_cap", "fix_rounds",
		"next_base?", "owner_email?", "source_checkout?", "integration_pr?", "merge?", "acceptance?", "base_result?", "base_reds?", "base_reds_file?", "lane_kind?", "allow_paths?", "wait_hours?", "label?", "items?",
	}}
}

// newRounds is the constructor of the rounds block.
func newRounds(at syntax.Position, kwargs []starlark.Tuple) (*rawRounds, error) {
	raw := &rawRounds{At: at}
	err := roundsSpec().unpack(at, kwargs,
		&raw.Base, &raw.Owner, &raw.LaneCap, &raw.FixRounds,
		&raw.NextBase, &raw.OwnerEmail, &raw.SourceCheckout, &raw.IntegrationPR, &raw.Merge, &raw.Acceptance, &raw.BaseResult, &raw.BaseReds, &raw.BaseRedsFile, &raw.LaneKind, &raw.AllowPaths, &raw.WaitHours, &raw.Label, &raw.Items)

	return raw, err
}

// roundsOf checks the rounds block and returns it. A project with no block has no rounds.
func roundsOf(raw rawProject) (model.Rounds, error) {
	block := raw.Rounds
	if block == nil {
		return model.Rounds{}, nil
	}

	if err := errors.Join(roundTexts(block), roundNumbers(block), roundLane(block), roundSelector(block)); err != nil {
		return model.Rounds{}, err
	}

	return block.rounds(), nil
}

// rounds returns the settings of the block, with the defaults of the keys that the block leaves out.
func (block *rawRounds) rounds() model.Rounds {
	rounds := model.Rounds{
		Base: block.Base.Value, NextBase: block.NextBase.Value, Owner: block.Owner.Value, OwnerEmail: block.OwnerEmail.Value, SourceCheckout: block.SourceCheckout.Value,
		LaneCap: block.LaneCap.Value, FixRounds: block.FixRounds.Value, IntegrationPR: block.IntegrationPR.Value, WaitHours: block.WaitHours.Value, Merge: block.Merge,
		Acceptance: block.Acceptance.Value, BaseResult: block.BaseResult.Value, BaseRedsFile: block.BaseRedsFile.Value, LaneKind: block.LaneKind.Value,
		BaseReds: slices.Clone(block.BaseReds), AllowPaths: slices.Clone(block.AllowPaths), Label: block.Label.Value, Items: slices.Clone(block.Items),
	}

	if !block.LaneKind.Set {
		rounds.LaneKind = model.LaneImpl
	}

	if !block.BaseRedsFile.Set {
		rounds.BaseRedsFile = defaultBaseRedsFile
	}

	return rounds
}

// roundTexts refuses a blank owner, a base that cannot be part of a round id, a blank base reds file that the block sets, and a blank item of a list.
// An empty next_base, acceptance, base_result, owner_email or source_checkout is a value: the round does not cut, or has none.
func roundTexts(block *rawRounds) error {
	var problems []error

	if err := requireText(block.At, "rounds.base", block.Base.Value); err != nil {
		problems = append(problems, err)
	} else if !model.ValidBase(block.Base.Value) {
		problems = append(problems, refuseAt(block.At, "`rounds.base` is %q. Write a branch name such as integration/r2: letters, digits, `.`, `_` and `-`, with `/` between parts.", block.Base.Value))
	}

	if err := requireText(block.At, "rounds.owner", block.Owner.Value); err != nil {
		problems = append(problems, err)
	}

	if block.BaseRedsFile.Set {
		problems = append(problems, requireText(block.At, "rounds.base_reds_file", block.BaseRedsFile.Value))
	}

	return errors.Join(append(problems, roundLists(block)...)...)
}

// roundLists refuses a blank item in a list of the block.
func roundLists(block *rawRounds) []error {
	var problems []error

	lists := map[string]paths{"rounds.base_reds": block.BaseReds, "rounds.allow_paths": block.AllowPaths}

	for _, key := range slices.Sorted(maps.Keys(lists)) {
		if slices.ContainsFunc(lists[key], blank) {
			problems = append(problems, refuseAt(block.At, "`%s` lists a blank string. Write the entry or remove it.", key))
		}
	}

	return problems
}

// roundNumbers refuses a count below its smallest value.
func roundNumbers(block *rawRounds) error {
	var problems []error

	if block.LaneCap.Value < 1 {
		problems = append(problems, refuseAt(block.At, "`rounds.lane_cap` is %d. Write the number of lanes that run at once, 1 or more.", block.LaneCap.Value))
	}

	if block.FixRounds.Value < 1 {
		problems = append(problems, refuseAt(block.At, "`rounds.fix_rounds` is %d. Write the number of fix rounds for one item, 1 or more.", block.FixRounds.Value))
	}

	if block.WaitHours.Set && block.WaitHours.Value < 1 {
		problems = append(problems, refuseAt(block.At, "`rounds.wait_hours` is %d. Write the hours that the round waits for the owner, 1 or more.", block.WaitHours.Value))
	}

	if block.IntegrationPR.Value < 0 {
		problems = append(problems, refuseAt(block.At, "`rounds.integration_pr` is %d. Write the number of the integration pull request, or 0 to let the round find it.", block.IntegrationPR.Value))
	}

	return errors.Join(problems...)
}

// roundLane refuses a lane kind that is not impl, papers or design, and a papers or design round with no allowed paths.
func roundLane(block *rawRounds) error {
	if !block.LaneKind.Set {
		return nil
	}

	switch block.LaneKind.Value {
	case model.LaneImpl:
		return nil
	case model.LanePapers, model.LaneDesign:
		if len(block.AllowPaths) == 0 {
			return refuseAt(block.At, "`rounds.lane_kind` is %q and `rounds.allow_paths` is empty. List the paths that the lane may change.", block.LaneKind.Value)
		}

		return nil
	default:
		return refuseAt(block.At, "`rounds.lane_kind` is %q. Write impl, papers or design.", block.LaneKind.Value)
	}
}

// roundSelector refuses a block with no item selector, a block with both, and a selector that is blank or names an id that is not an item.
func roundSelector(block *rawRounds) error {
	hasLabel, hasItems := block.Label.Set, len(block.Items) > 0

	switch {
	case hasLabel && hasItems:
		return refuseAt(block.At, "a round takes one item selector: set label or items, not both.")
	case !hasLabel && !hasItems:
		return refuseAt(block.At, "a round needs an item selector: set label or items.")
	case hasLabel:
		return requireText(block.At, "rounds.label", block.Label.Value)
	}

	for _, item := range block.Items {
		if kind := model.ItemKind(item); kind == "" || kind == model.KindRound {
			return refuseAt(block.At, "`rounds.items` lists %q. Write an item id: a number, or a beads id such as bd-12.", item)
		}
	}

	return nil
}
