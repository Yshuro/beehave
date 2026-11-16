package remedy

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"regexp"
	"slices"
	"strings"

	"github.com/tobocop2/beehave/examples/remediation/tools/recount"
	"github.com/tobocop2/beehave/internal/model"
)

const integrationSyntax = "integration --base <round branch> --into <trunk> [--pr <number>]"

// listLimit is the most pull requests that one list call reads.
const listLimit = 100

// errFullList is the refusal of a list of merged pull requests that reached listLimit, so some can be missing from it.
var errFullList = errors.New("the list of merged pull requests is full")

// Integrator is the forge of the integration subcommand: the reads of the pull requests, the opening of a draft pull request and the write of a body.
// A forge that lacks it refuses the subcommand. Every write goes through it, so no tool calls the forge command line itself.
type Integrator interface {
	PRView(ctx context.Context, pr int, fields ...string) (model.PullRequest, error)
	PRList(ctx context.Context, limit int, args ...string) ([]model.PullRequest, error)
	CreatePR(ctx context.Context, req model.NewPull) (int, error)
	SetBody(ctx context.Context, pr int, body string) error
}

// integrationFlags are the values of the flags of integration.
type integrationFlags struct {
	base, into string
	pr         int
}

func parseIntegration(args []string, errOut io.Writer) (integrationFlags, bool) {
	var given integrationFlags

	flags := flag.NewFlagSet("remedy integration", flag.ContinueOnError)
	flags.SetOutput(errOut)
	flags.StringVar(&given.base, "base", "", "the round branch: the head of the integration pull request")
	flags.StringVar(&given.into, "into", "", "the trunk: the base of the integration pull request")
	flags.IntVar(&given.pr, "pr", 0, "the number of the integration pull request; 0 finds it, and opens it when there is none")

	if flags.Parse(args) != nil || flags.NArg() > 0 || given.base == "" || given.into == "" || given.pr < 0 {
		say(errOut, "usage: remedy %s\n", integrationSyntax)

		return given, false
	}

	return given, true
}

// integrationCommand finds the integration pull request, or opens it, writes a row for each pull request that merged into the round branch,
// and prints `pr=<n> state=<state>`. A failed read or write is exit code 2.
func integrationCommand(ctx context.Context, deps Deps, s streams, args []string) int {
	given, ok := parseIntegration(args, s.err)
	if !ok {
		return ExitRefused
	}

	pull, state, err := integrate(ctx, deps, given)
	if err != nil {
		return refused(s, "integration", err)
	}

	say(s.out, "pr=%d state=%s\n", pull, state)

	return ExitPass
}

// integrate opens the forge of the origin remote and runs the integration on it.
func integrate(ctx context.Context, deps Deps, given integrationFlags) (int, string, error) {
	repo, err := originRepo(ctx, deps)
	if err != nil {
		return 0, "", err
	}

	opened, err := deps.Forge(repo)
	if err != nil {
		return 0, "", err
	}

	forge, ok := opened.(Integrator)
	if !ok {
		return 0, "", errNoIntegrator
	}

	return integrateOn(ctx, forge, given)
}

// integrateOn finds the integration pull request or opens it, and brings its body up to the pull requests merged into the round branch.
func integrateOn(ctx context.Context, forge Integrator, given integrationFlags) (int, string, error) {
	pull, err := findIntegration(ctx, forge, given)
	if err != nil {
		return 0, "", err
	}

	if state := stateOf(pull); pull.Number != 0 && state != stateOpen {
		return pull.Number, state, nil
	}

	merged, err := mergedInto(ctx, forge, given.base)
	if err != nil {
		return 0, "", err
	}

	if pull.Number == 0 {
		return openIntegration(ctx, forge, given, merged)
	}

	return pull.Number, stateOpen, writeBody(ctx, forge, pull, merged)
}

// mergedInto lists the pull requests that merged into the round branch. A full list can hold only a part of them,
// and a body made from it would leave out the rest with no sign, so a full list is a refusal.
func mergedInto(ctx context.Context, forge Integrator, base string) ([]model.PullRequest, error) {
	merged, err := forge.PRList(ctx, listLimit, "--state", "merged", "--base", base, "--json", "number,title,state,headRefName")
	if err != nil {
		return nil, err
	}

	if len(merged) >= listLimit {
		return nil, fmt.Errorf("%w: %s has %d or more merged pull requests. Split the round into smaller rounds, then run the tool again", errFullList, base, listLimit)
	}

	return merged, nil
}

// writeBody writes the body of the pull request again when a merged pull request adds a row or an issue to it.
func writeBody(ctx context.Context, forge Integrator, pull model.PullRequest, merged []model.PullRequest) error {
	body, changed := withRows(pull.Body, merged)
	if !changed {
		return nil
	}

	return forge.SetBody(ctx, pull.Number, body)
}

// findIntegration reads the pull request that the number names, or looks for the one from the round branch into the trunk: an open one first, then a merged one.
// A zero Number in the result means there is none.
func findIntegration(ctx context.Context, forge Integrator, given integrationFlags) (model.PullRequest, error) {
	if given.pr != 0 {
		pull, err := forge.PRView(ctx, given.pr, "number", "state", "body")
		pull.Number = given.pr

		return pull, err
	}

	found, err := forge.PRList(ctx, listLimit, "--state", "all", "--head", given.base, "--base", given.into, "--json", "number,state,body")
	if err != nil {
		return model.PullRequest{}, err
	}

	for _, want := range []string{stateOpen, "MERGED"} {
		if i := slices.IndexFunc(found, func(p model.PullRequest) bool { return stateOf(p) == want }); i >= 0 {
			return found[i], nil
		}
	}

	return model.PullRequest{}, nil
}

// openIntegration opens the draft pull request from the round branch into the trunk, with a table of the pull requests that merged so far.
func openIntegration(ctx context.Context, forge Integrator, given integrationFlags, merged []model.PullRequest) (int, string, error) {
	body, _ := withRows("", merged)
	name := given.base[strings.LastIndex(given.base, "/")+1:]

	number, err := forge.CreatePR(ctx, model.NewPull{Head: given.base, Base: given.into, Title: "Integration round " + name, Body: body})
	if err != nil {
		return 0, "", err
	}

	return number, stateOpen, nil
}

func stateOf(pull model.PullRequest) string {
	if pull.State == nil {
		return "UNKNOWN"
	}

	return *pull.State
}

var (
	headingFigure = regexp.MustCompile(`\(\d+( pull requests?)?\)`)
	closeLine     = regexp.MustCompile(`^(?:- |\| )Closes #(\d+)\b`)
	digits        = regexp.MustCompile(`^\d+$`)
)

const (
	stateOpen    = "OPEN"
	tableHeading = "## Pull requests merged into this branch (0 pull requests)"
	tableHead    = "| PR | Change |"
	tableRule    = "|---|---|"
	closeHeading = "## Issues this batch closes"
)

// withRows returns the body with one row for each merged pull request that its table lacks and one `- Closes #N` line for each issue that a merged pull request resolves
// and the body does not list, and says whether it changed. The item id of a pull request is its head branch, and an id of digits is the number of the issue.
// The table is the section of the first known heading, and a body with no such heading gets one at its end. The count in the heading is the count of the rows.
// The closes lines go at the end of the section of the closes heading, and a body with none gets the heading at its end.
func withRows(body string, merged []model.PullRequest) (string, bool) {
	lines := strings.Split(body, "\n")

	rows, issues := missingRows(lines, merged), missingIssues(lines, merged)
	if len(rows)+len(issues) == 0 {
		return body, false
	}

	if len(rows) > 0 {
		lines = addRows(lines, rows)
	}

	if len(issues) > 0 {
		lines = addIssues(lines, issues)
	}

	return strings.Join(lines, "\n"), true
}

// missingRows returns the table row of each merged pull request that the lines have no row for.
func missingRows(lines []string, merged []model.PullRequest) []string {
	var missing []string

	for _, pull := range merged {
		if !hasRow(lines, pull.Number) {
			missing = append(missing, fmt.Sprintf("| #%d | %s |", pull.Number, strings.ReplaceAll(pull.Title, "|", `\|`)))
		}
	}

	return missing
}

// missingIssues returns the closes line of each issue that a merged pull request resolves and the lines do not list. An issue shows once, however many pull requests name it.
func missingIssues(lines []string, merged []model.PullRequest) []string {
	listed := map[string]bool{}

	for _, line := range lines {
		if found := closeLine.FindStringSubmatch(line); found != nil {
			listed[found[1]] = true
		}
	}

	var missing []string

	for _, pull := range merged {
		if issue := issueOf(pull); issue != "" && !listed[issue] {
			listed[issue] = true
			missing = append(missing, "- Closes #"+issue)
		}
	}

	return missing
}

// issueOf returns the issue number that the head branch of the pull request names, or an empty text when the branch is not an item id of digits.
func issueOf(pull model.PullRequest) string {
	if pull.HeadRefName == nil || !digits.MatchString(*pull.HeadRefName) {
		return ""
	}

	return *pull.HeadRefName
}

// addRows puts the rows at the end of the table, makes the table when the body has none and sets the count of its heading.
func addRows(lines, rows []string) []string {
	at := slices.IndexFunc(lines, recount.HasHeading)
	if at < 0 {
		lines = append(strings.Split(strings.TrimRight(strings.Join(lines, "\n"), "\n"), "\n"), "", tableHeading)
		at = len(lines) - 1
	}

	last := lastLine(lines, at, func(l string) bool { return strings.HasPrefix(l, "|") })
	if last == at {
		header := []string{"", tableHead, tableRule}
		lines = slices.Insert(lines, at+1, header...)
		last = at + len(header)
	}

	lines = slices.Insert(lines, last+1, rows...)
	lines[at] = headingFigure.ReplaceAllString(lines[at], figureOf(recount.Count(strings.Join(lines, "\n")).Rows))

	return lines
}

// addIssues puts the closes lines at the end of the closes section, and makes the section at the end of the body when there is none.
func addIssues(lines, issues []string) []string {
	at := slices.Index(lines, closeHeading)
	if at < 0 {
		lines = append(strings.Split(strings.TrimRight(strings.Join(lines, "\n"), "\n"), "\n"), "", closeHeading, "")

		return append(lines, issues...)
	}

	return slices.Insert(lines, lastLine(lines, at, func(l string) bool { return strings.TrimSpace(l) != "" })+1, issues...)
}

// lastLine returns the index of the last line of the section of the heading at the index that the test accepts, or the index of the heading when none is.
func lastLine(lines []string, at int, accept func(string) bool) int {
	last := at

	for i := at + 1; i < sectionEnd(lines, at); i++ {
		if accept(lines[i]) {
			last = i
		}
	}

	return last
}

// sectionEnd returns the index of the next `## ` heading after the heading at the index, or the number of lines.
func sectionEnd(lines []string, at int) int {
	for i := at + 1; i < len(lines); i++ {
		if strings.HasPrefix(lines[i], "## ") {
			return i
		}
	}

	return len(lines)
}

// hasRow reports a pull request row for the number: `| #N |` or a link to the pull request.
func hasRow(lines []string, number int) bool {
	plain, linked := fmt.Sprintf("| #%d |", number), fmt.Sprintf("| [#%d](", number)

	return slices.ContainsFunc(lines, func(l string) bool { return strings.HasPrefix(l, plain) || strings.HasPrefix(l, linked) })
}

func figureOf(rows int) string {
	if rows == 1 {
		return "(1 pull request)"
	}

	return fmt.Sprintf("(%d pull requests)", rows)
}
