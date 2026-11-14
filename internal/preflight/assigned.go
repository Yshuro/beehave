package preflight

import (
	"context"
	"fmt"
	"regexp"
	"strconv"

	"github.com/tobocop2/beehave/internal/merge"
)

// linkPattern finds a link from a pull request body to an issue: a keyword, then #N, owner/repo#N or the URL of the issue.
var linkPattern = regexp.MustCompile(`(?i)\b(?:closes|fixes|resolves|part of|refs?|related(?: to)?)\b:?\s+(?:https://github\.com/([\w.-]+/[\w.-]+)/issues/|([\w.-]+/[\w.-]+)?#)(\d+)`)

// link is an issue that a pull request names. An empty repo is the repository of the pull request.
type link struct {
	repo   string
	number int
}

func (l link) String() string {
	return l.repo + "#" + strconv.Itoa(l.number)
}

// links returns the issues the body names, once each, in the order of the body.
func links(body string) []link {
	var found []link

	seen := map[link]bool{}

	for _, match := range linkPattern.FindAllStringSubmatch(body, -1) {
		number, err := strconv.Atoi(match[3])
		if err != nil {
			continue
		}

		next := link{repo: match[1] + match[2], number: number}
		if !seen[next] {
			seen[next] = true
			found = append(found, next)
		}
	}

	return found
}

// heldStop is the stop for an item that somebody else holds: an assignee first, then the author of an open pull request.
func heldStop(ctx context.Context, r Reader, v view, req Request) (Stop, []string, error) {
	if stop, lines, err := assignedStop(ctx, r, v, req); err != nil || stop != Unchanged {
		return stop, lines, err
	}

	stop, lines := claimStop(v, req.Own)

	return stop, lines, nil
}

// assignedStop is the stop for an item or a linked issue that a login outside own holds. It reads each issue once, on the repository of the issue.
// The item comes first, then the links in the order of the body. A failed read is an error, never a clean item.
func assignedStop(ctx context.Context, r Reader, v view, req Request) (Stop, []string, error) {
	if len(req.Own) == 0 {
		return Unchanged, nil, nil
	}

	holder, where, err := holderOf(ctx, r, v, req.Own)
	if err != nil || holder == "" {
		return Unchanged, nil, err
	}

	stop, lines := assignedLines(v, holder, where)

	return stop, lines, nil
}

// holderOf returns the first foreign login on the pull request or on a link, and where it holds. Both are empty when nobody foreign holds.
func holderOf(ctx context.Context, r Reader, v view, own []string) (holder, where string, err error) {
	if holder = foreign(v.assignees, own); holder != "" {
		return holder, fmt.Sprintf("#%d", v.pr), nil
	}

	for _, next := range links(v.body) {
		logins, err := r.IssueAssignees(ctx, next.repo, next.number)
		if err != nil {
			return "", "", &ReadError{PR: v.pr, Link: next.String(), Cause: err}
		}

		if holder = foreign(logins, own); holder != "" {
			return holder, "the linked issue " + next.String(), nil
		}
	}

	return "", "", nil
}

// foreign returns the first login that is not in own, or empty.
func foreign(logins, own []string) string {
	for _, login := range logins {
		if !merge.IsOwn(login, own) {
			return login
		}
	}

	return ""
}

// assignedLines is the stop for a holder. knows is yes only when the holder opened the pull request or reviews it.
func assignedLines(v view, holder, where string) (Stop, []string) {
	knows := "no"
	if merge.IsOwn(holder, []string{v.author}) || merge.IsOwn(holder, v.reviewers) {
		knows = "yes"
	}

	return Assigned, []string{
		fmt.Sprintf("STOP: %s is assigned to '%s', who is not on the list of own logins. The item is claimed. knows=%s", where, holder, knows),
		"  Recovery: leave the item to the holder and move to the next item. To take it, ask the holder first.",
	}
}
