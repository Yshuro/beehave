package flow

import (
	"context"
	"maps"
	"strings"

	"go.starlark.net/starlark"
	"go.starlark.net/starlarkstruct"

	"github.com/tobocop2/beehave/internal/model"
	"github.com/tobocop2/beehave/internal/workflow"
)

// repoKeyword is the keyword `repo` of a merge call and the field `repo` of the facts.
const repoKeyword = "repo"

// facts returns the facts of the project of the run as a struct. The step records them, so a replay returns the facts the run started with, not the ones in the file now.
func (s *script) facts(b *starlark.Builtin, args starlark.Tuple, kwargs []starlark.Tuple) (starlark.Value, error) { //nolint:ireturn // the builtin contract of starlark-go
	if err := workflow.Unpack(b.Name(), args, kwargs, map[string]any{}); err != nil {
		return nil, err
	}

	fields, err := s.durableFields("facts", "", map[string]any{varsField: map[string]any{}}, func(context.Context, string) (stepResult, error) {
		return stepResult{Outputs: factsOutputs(s.Project())}, nil
	})
	if err != nil {
		return nil, err
	}

	// The settings that the project file wrote stay as written. Every call builds a fresh value, so the freeze is local to the caller.
	fields[varsField].Freeze()

	return starlarkstruct.FromStringDict(starlarkstruct.Default, fields), nil
}

// varsField is the field `vars` of the facts: the free settings of the project file.
const varsField = "vars"

// factsOutputs holds the fields of the facts struct. An empty fact is an empty list, an empty dict or empty text, never a missing field, so a prompt can range over it.
func factsOutputs(p model.Project) map[string]any {
	facts := map[string]any{
		repoKeyword: p.Repo, "source": p.Source, "forge": string(p.Forge), "tracker": string(p.Tracker), "base_branch": p.BaseBranch, "gate": gateText(p), "gate_steps": gateStepNames(p), "build_host": p.BuildHost,
		"people": orEmptyDict(p.People), "epics": orEmptyDict(p.Epics),
		"gate_files": orEmptyList(p.GateFiles), "conventions": orEmptyList(p.Conventions), "brief_rules": orEmptyList(p.BriefRules), "stacked_rules": orEmptyList(p.StackedRules),
		"limits": orEmptyList(p.Limits), "traps": orEmptyList(p.Traps), "token_rules": orEmptyList(p.TokenRules), "changelog": p.Changelog, varsField: orEmptyVars(p.Vars),
		"board_number": p.Board.Number, "board_owner": p.Board.Owner, "board_started": p.Board.Started, "board_merged": p.Board.Merged,
	}

	maps.Copy(facts, roundOutputs(p.Rounds))

	return facts
}

// roundOutputs holds the round_* fields of the facts struct. A key that the file leaves out is empty text, an empty list, 0 or false.
func roundOutputs(r model.Rounds) map[string]any {
	return map[string]any{
		"round_base": r.Base, "round_next_base": r.NextBase, "round_owner": r.Owner, "round_owner_email": r.OwnerEmail, "round_source_checkout": r.SourceCheckout,
		"round_lane_cap": r.LaneCap, "round_fix_rounds": r.FixRounds, "round_integration_pr": r.IntegrationPR, "round_merge": r.Merge,
		"round_acceptance": r.Acceptance, "round_base_result": r.BaseResult, "round_base_reds": orEmptyList(r.BaseReds), "round_base_reds_file": r.BaseRedsFile,
		"round_lane_kind": r.LaneKind, "round_allow_paths": orEmptyList(r.AllowPaths), "round_wait_hours": r.WaitHours,
	}
}

// gateText is what a prompt shows as the gate of the project: the gate string, or the gate list as "name: command" joined by "; ", in order.
// A project has one of the two, so the text is never empty for a project that has a gate.
func gateText(p model.Project) string {
	if len(p.GateSteps) == 0 {
		return p.Gate
	}

	parts := make([]string, 0, len(p.GateSteps))

	for _, spec := range p.GateSteps {
		parts = append(parts, spec.Name+": "+spec.Command)
	}

	return strings.Join(parts, "; ")
}

// gateStepNames is the names of the gate list of the project, in order. A project with a gate string has none.
func gateStepNames(p model.Project) []string {
	names := make([]string, 0, len(p.GateSteps))

	for _, spec := range p.GateSteps {
		names = append(names, spec.Name)
	}

	return names
}

func orEmptyList(items []string) []string {
	if items == nil {
		return []string{}
	}

	return items
}

func orEmptyVars(items map[string]any) map[string]any {
	if items == nil {
		return map[string]any{}
	}

	return items
}

func orEmptyDict(items map[string]string) map[string]string {
	if items == nil {
		return map[string]string{}
	}

	return items
}
