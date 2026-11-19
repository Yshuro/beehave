package flow

import (
	"context"
	"errors"

	"go.starlark.net/starlark"

	"github.com/tobocop2/beehave/internal/workflow"
)

// prComment adds a comment to the pull request of the item. The key makes the call safe to repeat: the same key on the same pull request adds one comment.
// The forge reads the comments of the pull request before it posts, so a crash between the post and the step record does not post twice.
func (s *script) prComment(b *starlark.Builtin, args starlark.Tuple, kwargs []starlark.Tuple) (starlark.Value, error) { //nolint:ireturn // the builtin contract of starlark-go
	text, key, label, err := commentArguments(b, args, kwargs)
	if err != nil {
		return nil, err
	}

	return s.durable("pr_comment", label, func(ctx context.Context, _ string) (stepResult, error) {
		number, err := s.e.deps.Forge.Comment(ctx, s.in.Project, s.in.Item, s.scoped(key), text)

		return stepResult{Outputs: prCommentOutputs(number)}, err
	})
}

// commentArguments unpacks the arguments of a pr_comment call and refuses a text that is empty, matches forbid_text or has a closing keyword outside the integration pull request.
func commentArguments(b *starlark.Builtin, args starlark.Tuple, kwargs []starlark.Tuple) (text, key, label string, err error) {
	var integration bool

	rules, err := unpackPosting(b, args, kwargs, map[string]any{
		textKeyword: &text, keyKeyword: &key, integrationKeyword: &integration, nameKeyword: &label,
	})
	if err != nil {
		return "", "", "", err
	}

	posted := postedText{textKeyword, text}
	err = errors.Join(workflow.NotEmpty(textKeyword, text), workflow.NotEmpty(keyKeyword, key), rules.refuse(posted), refuseClosing(integration, posted))

	return text, key, label, err
}

// prOpen opens the draft pull request of the item once, sets its body and links it into the native stack of its parent. The forge finds an open pull request
// on the branch of the item first, so a resume after a lost record opens no second one. The body is the text plus a closing line that the forge picks from the base.
func (s *script) prOpen(b *starlark.Builtin, args starlark.Tuple, kwargs []starlark.Tuple) (starlark.Value, error) { //nolint:ireturn // the builtin contract of starlark-go
	var req OpenRequest

	var integration bool

	label, rules, err := unpackOpen(b, args, kwargs, &req, &integration)
	if err != nil {
		return nil, err
	}

	posted := []postedText{{"title", req.Title}, {"body", req.Body}}
	if err = errors.Join(req.check(), rules.refuse(posted...), refuseClosing(integration, posted...)); err != nil {
		return nil, err
	}

	return s.durable("pr_open", label, func(ctx context.Context, _ string) (stepResult, error) {
		opened, err := s.e.deps.Forge.Open(ctx, s.in.Project, s.in.Item, req)

		return stepResult{Outputs: openOutputs(opened)}, err
	})
}

// unpackOpen unpacks the arguments of a pr_open call into req and integration. It returns the label of the call and the forbid_text patterns.
func unpackOpen(b *starlark.Builtin, args starlark.Tuple, kwargs []starlark.Tuple, req *OpenRequest, integration *bool) (label string, rules textRules, err error) {
	rules, err = unpackPosting(b, args, kwargs, map[string]any{
		titleKeyword: &req.Title, baseKeyword: &req.Base, bodyKeyword: &req.Body, "issue": &req.Issue, parentKeyword: &req.Parent, integrationKeyword: integration, nameKeyword: &label,
	})

	return label, rules, err
}

// check refuses a request with no title, no base or a negative issue number.
func (r OpenRequest) check() error {
	if r.Issue < 0 {
		return errors.New("issue must be an issue number, or 0 for none")
	}

	return errors.Join(workflow.NotEmpty("title", r.Title), workflow.NotEmpty(baseKeyword, r.Base))
}
