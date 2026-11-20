package gate

import (
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"strings"

	"go.yaml.in/yaml/v3"

	"github.com/tobocop2/beehave/internal/check/shell"
	"github.com/tobocop2/beehave/internal/model"
)

const (
	stepJoin       = "; "
	unnamedStep    = "run-"
	nameJoin       = ":"
	nameSeparator  = "-"
	expressionText = "EXPR"
)

var (
	expressionPart = regexp.MustCompile(`\$\{\{.*?\}\}`)
	nonNameRun     = regexp.MustCompile(`[^a-z0-9]+`)
)

// File is a CI workflow file: its path and its text.
type File struct {
	Path, Text string
}

// workflowStep is one step of a job of a CI workflow file.
type workflowStep struct {
	Name string            `yaml:"name"`
	Uses string            `yaml:"uses"`
	Run  string            `yaml:"run"`
	With map[string]string `yaml:"with"`
}

// workflowJob is one job of a CI workflow file, with the id it has in the file.
type workflowJob struct {
	ID    string
	Steps []workflowStep
}

// workflowJobs holds the jobs of a workflow file in file order, which a map would lose.
type workflowJobs []workflowJob

func (j *workflowJobs) UnmarshalYAML(node *yaml.Node) error {
	if node.Kind != yaml.MappingNode {
		return errors.New("jobs must be a mapping of job ids to jobs")
	}

	for i := 0; i+1 < len(node.Content); i += 2 {
		var body struct {
			Steps []workflowStep `yaml:"steps"`
		}

		if err := node.Content[i+1].Decode(&body); err != nil {
			return fmt.Errorf("job %s: %w", node.Content[i].Value, err)
		}

		*j = append(*j, workflowJob{ID: node.Content[i].Value, Steps: body.Steps})
	}

	return nil
}

type workflowFile struct {
	Jobs workflowJobs `yaml:"jobs"`
}

// Derive returns the gate steps of the CI workflow files: one step for each run step of each job, in file order. It refuses a file that is not a workflow, a run step that is not shell, a step that publishes, and files with no run step.
func Derive(files []File) ([]Step, error) {
	var (
		steps    []Step
		problems []string
	)

	for _, file := range files {
		got, bad := deriveFile(file)
		steps = append(steps, got...)
		problems = append(problems, bad...)
	}

	if len(problems) == 0 && len(steps) == 0 {
		problems = append(problems, "the workflow files hold no run step. Name the CI workflow files that hold the gate commands")
	}

	if len(problems) > 0 {
		return nil, listRefusal(problems, nil)
	}

	return uniqueNames(steps), nil
}

func deriveFile(file File) ([]Step, []string) {
	var workflow workflowFile
	if err := yaml.Unmarshal([]byte(file.Text), &workflow); err != nil {
		return nil, []string{fmt.Sprintf("%s: not a workflow file: %v", file.Path, err)}
	}

	var (
		steps    []Step
		problems []string
	)

	for _, job := range workflow.Jobs {
		got, bad := deriveJob(file.Path, job)
		steps = append(steps, got...)
		problems = append(problems, bad...)
	}

	return steps, problems
}

func deriveJob(path string, job workflowJob) ([]Step, []string) {
	var (
		steps    []Step
		problems []string
	)

	for i, step := range job.Steps {
		name := stepNameOf(job.ID, step, i+1)

		if problem := stepProblem(step); problem != "" {
			problems = append(problems, fmt.Sprintf("%s: the step %s %s", path, name, problem))
		} else if command := runCommand(step.Run); command != "" {
			steps = append(steps, Step{name, command})
		}
	}

	return steps, problems
}

// stepProblem says why a step cannot be a gate step: it publishes, or the gate cannot read what it runs.
func stepProblem(step workflowStep) string {
	hit, unreadable := publishOf(step)

	switch {
	case unreadable != "":
		return "has " + unreadable
	case hit != "":
		return "publishes (" + hit + "). Take the step out of the gate files: the gate list holds steps that check, never steps that release"
	}

	return ""
}

// stepNameOf names a gate step by its job and its step name, or by its place in the job when the step has no name.
func stepNameOf(job string, step workflowStep, place int) string {
	slug := strings.Trim(nonNameRun.ReplaceAllString(strings.ToLower(step.Name), nameSeparator), nameSeparator)
	if slug == "" {
		slug = unnamedStep + strconv.Itoa(place)
	}

	return job + nameJoin + slug
}

// runCommand joins the lines of a run step to one command. A blank line and a comment line add nothing.
func runCommand(run string) string {
	var lines []string

	for _, line := range strings.Split(strings.ReplaceAll(run, "\\\n", " "), "\n") {
		if line = model.FlatText(line); line != "" && !strings.HasPrefix(line, "#") {
			lines = append(lines, line)
		}
	}

	return strings.Join(lines, stepJoin)
}

// uniqueNames gives a later step of the same name a number, so that the list holds no name twice.
func uniqueNames(steps []Step) []Step {
	seen := map[string]bool{}

	for i, step := range steps {
		name := step.Name

		for n := 2; seen[name]; n++ {
			name = step.Name + nameSeparator + strconv.Itoa(n)
		}

		seen[name] = true
		steps[i].Name = name
	}

	return steps
}

// publishOf reports what a step publishes, in words that name the rule. The second result is set when the step cannot be read.
func publishOf(step workflowStep) (string, string) {
	if hit := actionPublish(step.Uses, step.With); hit != "" {
		return hit, ""
	}

	return commandPublish(step.Run)
}

// commandPublish reads the commands of a run step and reports the first that publishes.
func commandPublish(run string) (string, string) {
	units, unreadable := shell.ParseUnits(expressionPart.ReplaceAllString(run, expressionText))
	if len(unreadable) > 0 {
		return "", fmt.Sprintf("a line that is not shell (%s). The gate cannot tell what it runs", unreadable[0])
	}

	return firstPublish(units), ""
}
