// Package roundsync brings the trunk into the round branch when the trunk moves. The forge does each merge, so nothing is checked out.
package roundsync

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"

	"github.com/tobocop2/beehave/internal/forge"
	"github.com/tobocop2/beehave/internal/model"
)

const (
	// listLimit is the most pull requests one list reads.
	listLimit = 10
	// shortLength is the number of characters of the trunk tip in the name of the sync branch.
	shortLength = 8
	// ReasonRefused is the reason of a sync that the forge refuses for the workflow permission.
	ReasonRefused = "sync-refused"
)

// State is how a call ended.
type State string

// The states of a call.
const (
	Current   State = "current"
	Synced    State = "synced"
	Conflict  State = "conflict"
	Refused   State = "refused"
	Landed    State = "landed"
	Stale     State = "stale"
	HeadMoved State = "head-moved"
)

// Forge is the forge the sync works on.
type Forge interface {
	PRView(ctx context.Context, pr int, fields ...string) (model.PullRequest, error)
	PRList(ctx context.Context, limit int, args ...string) ([]model.PullRequest, error)
	BranchRef(ctx context.Context, branch string) (sha string, found bool, err error)
	CreateBranch(ctx context.Context, branch, sha string) error
	CreatePR(ctx context.Context, req model.NewPull) (int, error)
	MergePR(ctx context.Context, pr int, sha string) error
	CommitsAhead(ctx context.Context, base, head string) (int, error)
	MergeBranch(ctx context.Context, base, head string) (string, error)
}

// Request is one check: the round base and the trunk.
type Request struct {
	Base  string
	Trunk string
}

// LandRequest is one landing: the pull request, the head that the gate ran on and the base tip that the sync recorded.
type LandRequest struct {
	PR      int
	Head    string
	BaseTip string
}

// Result is how a call ended. A field that the state does not use is empty.
// Source is the branch whose merge into the sync branch conflicted: the trunk or the round base.
type Result struct {
	State   State
	Branch  string
	Source  string
	PR      int
	Head    string
	BaseTip string
	Reason  string
}

// Stops says that the workflow must not go on: the pull request is not landed and the call is a refusal.
func (r Result) Stops() bool {
	return r.State == Stale || r.State == HeadMoved
}

// Line is the one line of output: state first, then each field that is set.
func (r Result) Line() string {
	fields := []string{"state=" + string(r.State)}

	for _, field := range []struct{ key, value string }{
		{"branch", r.Branch}, {"source", r.Source}, {"head", r.Head}, {"base_tip", r.BaseTip}, {"reason", r.Reason},
	} {
		if field.value != "" {
			fields = append(fields, field.key+"="+field.value)
		}
	}

	if r.PR > 0 {
		fields = append(fields, "pr="+strconv.Itoa(r.PR))
	}

	return strings.Join(fields, " ")
}

// Branch is the name of the sync branch: it holds the round base and the first characters of the trunk tip.
func Branch(base, trunkTip string) string {
	return "sync/" + base + "/" + trunkTip[:min(len(trunkTip), shortLength)]
}

// syncer holds the state of one check.
type syncer struct {
	ctx   context.Context
	forge Forge
	req   Request
}

// Check finds out whether the trunk has commits that the round base lacks. If it has, the check merges the trunk into the sync branch and opens the pull request of the sync.
// A trunk with nothing new writes nothing. A conflict leaves the sync branch at the round base tip and opens no pull request.
func Check(ctx context.Context, forge Forge, req Request) (Result, error) {
	s := &syncer{ctx: ctx, forge: forge, req: req}

	ahead, err := forge.CommitsAhead(ctx, req.Base, req.Trunk)
	if err != nil {
		return Result{}, err
	}

	if ahead == 0 {
		return Result{State: Current}, nil
	}

	return s.sync()
}

func (s *syncer) sync() (Result, error) {
	baseTip, trunkTip, err := s.tips(s.req.Base, s.req.Trunk)
	if err != nil {
		return Result{}, err
	}

	branch := Branch(s.req.Base, trunkTip)
	if err := s.ensureBranch(branch, baseTip); err != nil {
		return Result{}, err
	}

	if stop, done, err := s.mergeAll(branch, baseTip); done || err != nil {
		return stop, err
	}

	return s.open(branch, baseTip)
}

// tip is the tip of a branch. A branch that is missing is an error.
func (s *syncer) tip(branch string) (string, error) {
	sha, found, err := s.forge.BranchRef(s.ctx, branch)
	if err != nil {
		return "", err
	}

	if !found {
		return "", forge.Refuse("cannot find branch %s", branch)
	}

	return sha, nil
}

// ensureBranch cuts the sync branch at the round base tip when it does not exist. A repeat call finds it.
func (s *syncer) ensureBranch(branch, baseTip string) error {
	_, found, err := s.forge.BranchRef(s.ctx, branch)
	if err != nil || found {
		return err
	}

	return s.forge.CreateBranch(s.ctx, branch, baseTip)
}

// mergeAll brings the trunk and then the round base into the sync branch. A source that the branch already holds is not merged again.
// The round base goes in so the head that the gate runs on holds every commit of the base that the landing keeps.
// done is true when the call ends here, with the result to show.
func (s *syncer) mergeAll(branch, baseTip string) (result Result, done bool, err error) {
	for _, source := range []string{s.req.Trunk, s.req.Base} {
		ahead, err := s.forge.CommitsAhead(s.ctx, branch, source)
		if err != nil {
			return Result{}, false, err
		}

		if ahead == 0 {
			continue
		}

		if _, err := s.forge.MergeBranch(s.ctx, branch, source); err != nil {
			return s.refusal(err, branch, source, baseTip)
		}
	}

	return Result{}, false, nil
}

// refusal turns a failed merge of source into the result that ends the call. A conflict and a refusal for the workflow permission are states. Any other failure is an error.
func (s *syncer) refusal(cause error, branch, source, baseTip string) (Result, bool, error) {
	switch {
	case errors.Is(cause, forge.ErrMergeConflict):
		return Result{State: Conflict, Branch: branch, Source: source, BaseTip: baseTip}, true, nil
	case errors.Is(cause, forge.ErrWorkflowPermission):
		return Result{State: Refused, Branch: branch, BaseTip: baseTip, Reason: ReasonRefused}, true, nil
	default:
		return Result{}, false, cause
	}
}

// open finds the pull request of the sync branch, or opens it as a draft, and shows the head that the gate must run on.
func (s *syncer) open(branch, baseTip string) (Result, error) {
	number, err := s.pull(branch)
	if err != nil {
		return Result{}, err
	}

	head, err := s.tip(branch)
	if err != nil {
		return Result{}, err
	}

	return Result{State: Synced, Branch: branch, PR: number, Head: head, BaseTip: baseTip}, nil
}

func (s *syncer) pull(branch string) (int, error) {
	rows, err := s.forge.PRList(s.ctx, listLimit, "--state", "open", "--head", branch, "--base", s.req.Base, "--json", "number")
	if err != nil {
		return 0, err
	}

	if len(rows) > 0 {
		return rows[0].Number, nil
	}

	return s.forge.CreatePR(s.ctx, model.NewPull{
		Head:  branch,
		Base:  s.req.Base,
		Title: fmt.Sprintf("Merge %s into %s", s.req.Trunk, s.req.Base),
		Body:  fmt.Sprintf("Brings the commits of %s that %s lacks into the round.", s.req.Trunk, s.req.Base),
	})
}

// Land merges the pull request of a sync with a merge commit, pinned to the head that the gate ran on.
// It lands only when the round base is still at the tip that the sync recorded, so the base tree after the merge equals the gated tree.
// A moved base is the state Stale and a moved head is the state HeadMoved. Neither writes.
func Land(ctx context.Context, forge Forge, req LandRequest) (Result, error) {
	s := &syncer{ctx: ctx, forge: forge}

	pull, err := forge.PRView(ctx, req.PR, "state", "baseRefName", "headRefName")
	if err != nil {
		return Result{}, err
	}

	base, head, err := s.openNames(pull, req.PR)
	if err != nil {
		return Result{}, err
	}

	return s.land(req, base, head)
}

// openNames reads the base and head branch names of an open pull request.
func (s *syncer) openNames(pull model.PullRequest, number int) (base, head string, err error) {
	if pull.State == nil || *pull.State != "OPEN" || pull.BaseRefName == nil || pull.HeadRefName == nil {
		return "", "", forge.Refuse("pull request #%d is not an open pull request with a base and a head branch", number)
	}

	return *pull.BaseRefName, *pull.HeadRefName, nil
}

// tips reads the tips of two branches.
func (s *syncer) tips(first, second string) (firstTip, secondTip string, err error) {
	if firstTip, err = s.tip(first); err != nil {
		return "", "", err
	}

	if secondTip, err = s.tip(second); err != nil {
		return "", "", err
	}

	return firstTip, secondTip, nil
}

func (s *syncer) land(req LandRequest, base, head string) (Result, error) {
	baseTip, headTip, err := s.tips(base, head)
	if err != nil {
		return Result{}, err
	}

	result := Result{PR: req.PR, Head: headTip, BaseTip: baseTip, Branch: head}

	switch {
	case baseTip != req.BaseTip:
		result.State = Stale
	case headTip != req.Head:
		result.State = HeadMoved
	default:
		result.State = Landed
		err = s.forge.MergePR(s.ctx, req.PR, req.Head)
	}

	return result, err
}
