package flow

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"os/exec"
	"strings"

	"go.starlark.net/starlark"

	"github.com/tobocop2/beehave/internal/merge"
	"github.com/tobocop2/beehave/internal/model"
	"github.com/tobocop2/beehave/internal/workflow"
)

const (
	allowKeyword = "allow"
	// secretsClean is the exit code of a secret scan that found nothing.
	secretsClean = 0
)

// errNoGateAtHead refuses a before_review call whose head no gate call of the run gave.
var errNoGateAtHead = errors.New("before_review needs a gate call with `base` in this run, at the same `head`. " +
	"Call gate(base = <base>) first, and pass the head that it returned. A gate without `base` runs no new-tests check and no secret scan")

// reviewVerdict is what before_review decides: the findings, the new tests, whether the head holds the base and the scope, and the closing keywords of its commits.
type reviewVerdict struct {
	findings []string
	newTests []string
	revert   model.RevertVerdict
	base     bool
	keywords []string
}

// beforeReview runs the checks that stand between the last fix and the review. It reads the checks that the gate job of the head gave (the new tests and the secret scan),
// and it checks the base, the changed paths and the closing keywords of the commits on the item folder. A refusal is a result: ok is false and the findings say why.
// A call with no gate call at the head is an error, because the call then has nothing to read.
func (s *script) beforeReview(b *starlark.Builtin, args starlark.Tuple, kwargs []starlark.Tuple) (starlark.Value, error) { //nolint:ireturn // the builtin contract of starlark-go
	call, err := reviewArguments(b, args, kwargs)
	if err != nil {
		return nil, err
	}

	gated, err := s.gateAt(call.head)
	if err != nil {
		return nil, err
	}

	return s.durable("before_review", call.label, func(ctx context.Context, _ string) (stepResult, error) {
		dir, err := s.e.deps.Trees.Ensure(ctx, s.in.Project, s.in.Item)
		if err != nil {
			return stepResult{}, fmt.Errorf("find the folder of item %s: %w", s.in.Item, err)
		}

		verdict, err := judgeHead(ctx, dir, call, gated.checks)

		return stepResult{Outputs: verdict.outputs()}, err
	})
}

// reviewCall is the arguments of a before_review call.
type reviewCall struct {
	base, head, label string
	allowed           scope
	integration       bool
}

// reviewArguments reads the keywords of a before_review call.
func reviewArguments(b *starlark.Builtin, args starlark.Tuple, kwargs []starlark.Tuple) (call reviewCall, err error) {
	var allow starlark.Value

	if err = workflow.Unpack(b.Name(), args, kwargs, map[string]any{
		baseKeyword: &call.base, headKeyword: &call.head, allowKeyword: &allow, integrationKeyword: &call.integration, nameKeyword: &call.label,
	}); err != nil {
		return call, err
	}

	call.allowed, err = readScope(allow)

	return call, err
}

// gateAt finds the newest gate call of this run at the head.
func (s *script) gateAt(head string) (gatedCall, error) {
	for i := len(s.gated) - 1; i >= 0; i-- {
		if s.gated[i].head == starlark.Value(starlark.String(head)) {
			return s.gated[i], nil
		}
	}

	return gatedCall{}, errNoGateAtHead
}

// judgeHead makes the verdict of before_review for the head. The revert verdict is the one of the gate record, which is never empty:
// gateOutputs records fail for a gate with no verdict.
func judgeHead(ctx context.Context, dir string, call reviewCall, checks GateChecks) (reviewVerdict, error) {
	verdict := reviewVerdict{
		findings: checkFindings(checks), newTests: append([]string{}, checks.NewTests...), revert: checks.Revert, keywords: []string{},
	}

	descends, err := descendsFrom(ctx, dir, call.base, call.head)
	if err != nil {
		return verdict, err
	}

	if !descends {
		verdict.findings = append(verdict.findings, fmt.Sprintf("the head %s is not a descendant of the base %s. Rebase the work on the base", call.head, call.base))

		return verdict, nil
	}

	verdict.base = true

	return verdict, verdict.holdHistory(ctx, dir, call)
}

// holdHistory checks the paths and the commit messages between the base and the head.
func (v *reviewVerdict) holdHistory(ctx context.Context, dir string, call reviewCall) error {
	if err := v.holdScope(ctx, dir, call.base, call.head, call.allowed); err != nil {
		return err
	}

	if call.integration {
		return nil
	}

	return v.holdKeywords(ctx, dir, call.base, call.head)
}

// cutFindings is the finding of a log with no end marker, or of a count of zero new tests. A cut log proves nothing.
func cutFindings(checks GateChecks) []string {
	switch {
	case !checks.Complete && checks.Problem != "":
		return []string{"the log of the gate job has no end marker, so the new-tests check is cut and proves nothing: " + checks.Problem}
	case !checks.Complete:
		return []string{"the log of the gate job has no end marker, so the new-tests check is cut and proves nothing"}
	case checks.New == 0:
		return []string{"the head adds no new test. Add a test that fails without the change"}
	}

	return nil
}

// checkFindings turns the checks that the gate job of the head gave into findings.
func checkFindings(checks GateChecks) []string {
	findings := append([]string{}, cutFindings(checks)...)

	for _, reason := range checks.Failed {
		findings = append(findings, "a new test failed a check: "+reason)
	}

	if checks.FailedCount > len(checks.Failed) {
		findings = append(findings, fmt.Sprintf("%d new tests failed a check in all, and the log names the first %d", checks.FailedCount, len(checks.Failed)))
	}

	findings = append(findings, revertFindings(checks)...)

	return append(findings, secretFinding(checks.Secrets)...)
}

// revertFindings are the findings of the revert run, for a head with new tests: one for each new test that passed or was skipped on the base
// code, one for the count past the names in the log, and one for a run that proves nothing. A head with no new test already has its finding.
func revertFindings(checks GateChecks) []string {
	if !checks.Complete || checks.New == 0 || checks.Revert == model.RevertPass {
		return nil
	}

	findings := []string{}

	for _, name := range checks.RevertPassed {
		findings = append(findings, "the new test "+name+" passes or is skipped on the base code. A test must fail without the change")
	}

	if checks.RevertPassedCount > len(checks.RevertPassed) {
		findings = append(findings, fmt.Sprintf("%d new tests pass or are skipped on the base code in all, and the log names the first %d", checks.RevertPassedCount, len(checks.RevertPassed)))
	}

	if len(findings) == 0 {
		findings = append(findings, "the revert run proves nothing: "+cmp.Or(checks.RevertProblem, "the log of the gate job has no end marker of the revert run"))
	}

	return findings
}

// secretFinding is the finding of the secret scan: none for a clean exit, one for any other exit or for a log that holds no single exit code.
func secretFinding(code *int) []string {
	switch {
	case code == nil:
		return []string{"the log of the gate job has no exit code of the secret scan, so the scan proves nothing"}
	case *code != secretsClean:
		return []string{fmt.Sprintf("the secret scan of the head exited with code %d. Remove the secret from every commit of the branch", *code)}
	}

	return nil
}

// descendsFrom reports whether the head has the base as an ancestor. A base or a head that git cannot find is an error: the call then knows nothing.
func descendsFrom(ctx context.Context, dir, base, head string) (bool, error) {
	_, err := git(ctx, dir, "merge-base", "--is-ancestor", base, head)

	var exit *exec.ExitError
	if errors.As(err, &exit) && exit.ExitCode() == 1 {
		return false, nil
	}

	if err != nil {
		return false, fmt.Errorf("compare the head %s with the base %s: %w", head, base, err)
	}

	return true, nil
}

// holdScope adds a finding for each path that the head changed since the merge base and that the scope does not allow. No scope allows every path.
func (v *reviewVerdict) holdScope(ctx context.Context, dir, base, head string, allowed scope) error {
	if !allowed.active() {
		return nil
	}

	out, err := git(ctx, dir, "diff", "--name-only", "--no-renames", "-z", base+"..."+head)
	if err != nil {
		return fmt.Errorf("list the paths that the head changed since the base: %w", err)
	}

	if bad := allowed.blocked(strings.Split(out, "\x00")); len(bad) > 0 {
		v.base = false
		v.findings = append(v.findings, fmt.Sprintf("the head changed %d path(s) outside allow [%s]: %s", len(bad), strings.Join(allowed.patterns, ", "), strings.Join(bad, ", ")))
	}

	return nil
}

// holdKeywords adds a finding for each closing keyword in a commit message between the base and the head.
func (v *reviewVerdict) holdKeywords(ctx context.Context, dir, base, head string) error {
	out, err := git(ctx, dir, "log", "--format=%B", base+".."+head)
	if err != nil {
		return fmt.Errorf("read the commit messages of the head: %w", err)
	}

	for _, ref := range merge.ClosingReferences(out) {
		v.keywords = append(v.keywords, ref)
		v.findings = append(v.findings, fmt.Sprintf("a commit message closes %s. Only the integration pull request may close an issue. Remove the keyword", ref))
	}

	return nil
}

// outputs are the result fields of before_review. A call is ok when it has no finding.
func (v reviewVerdict) outputs() map[string]any {
	return map[string]any{"ok": len(v.findings) == 0, "findings": v.findings, "new_tests": v.newTests, revertField: string(v.revert), "base": v.base, "keywords": v.keywords}
}
