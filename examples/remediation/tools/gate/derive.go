package gate

import (
	"errors"
	"fmt"
	"regexp"
	"slices"
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
	expressionText = "${EXPR}"
	stepsKey       = "steps"

	writePermission = "has a write permission"
	readsSecretText = "reads a secret"
)

var (
	expressionPart = regexp.MustCompile(`\$\{\{.*?\}\}`)
	nonNameRun     = regexp.MustCompile(`[^a-z0-9]+`)
	secretsPart    = regexp.MustCompile(`^secrets$|\bsecrets(\.|\[)`)
)

// File is a CI workflow file: its path and its text.
type File struct {
	Path, Text string
}

// Refused is a step or a job of a CI workflow file that the gate list leaves out, with the reason.
type Refused struct {
	Name, Reason string
}

// Derived is what a read of the CI workflow files gives: the steps of the gate list, and the steps and jobs that the gate list leaves out.
type Derived struct {
	Steps   []Step
	Refused []Refused
}

// workflowStep is one step of a job of a CI workflow file. Secret is set when any key or value of the step names a secret.
type workflowStep struct {
	Name   string `yaml:"name"`
	Uses   string `yaml:"uses"`
	Run    string `yaml:"run"`
	Shell  string `yaml:"shell"`
	Secret bool   `yaml:"-"`
}

func (s *workflowStep) UnmarshalYAML(node *yaml.Node) error {
	type plain workflowStep

	s.Secret = readsSecret(node)

	return node.Decode((*plain)(s))
}

// readsSecret reports whether a node holds a name or a value that reads a secret: the key secrets, or the expression secrets.NAME.
func readsSecret(node *yaml.Node) bool {
	if node.Kind == yaml.ScalarNode {
		return secretsPart.MatchString(node.Value)
	}

	return slices.ContainsFunc(node.Content, readsSecret)
}

// workflowJob is one job of a CI workflow file, with the id it has in the file. Problem is why the whole job is left out, or an empty text.
type workflowJob struct {
	ID      string
	Problem string
	Steps   []workflowStep
}

// workflowJobs holds the jobs of a workflow file in file order, which a map would lose.
type workflowJobs []workflowJob

func (j *workflowJobs) UnmarshalYAML(node *yaml.Node) error {
	if node.Kind != yaml.MappingNode {
		return errors.New("jobs must be a mapping of job ids to jobs")
	}

	for i := 0; i+1 < len(node.Content); i += 2 {
		var body struct {
			Uses        string         `yaml:"uses"`
			Permissions yaml.Node      `yaml:"permissions"`
			Steps       []workflowStep `yaml:"steps"`
		}

		if err := node.Content[i+1].Decode(&body); err != nil {
			return fmt.Errorf("job %s: %w", node.Content[i].Value, err)
		}

		*j = append(*j, workflowJob{ID: node.Content[i].Value, Problem: jobProblem(node.Content[i+1], body.Uses, &body.Permissions), Steps: body.Steps})
	}

	return nil
}

// jobProblem says why a job cannot give gate steps: it calls a reusable workflow, it can write, or it reads a secret.
func jobProblem(job *yaml.Node, uses string, permissions *yaml.Node) string {
	switch {
	case uses != "":
		return "calls the reusable workflow " + uses + ", which the gate does not read"
	case writes(permissions):
		return writePermission
	case readsSecret(withoutKey(job, stepsKey)):
		return readsSecretText
	}

	return ""
}

// writes reports whether a permissions node holds a write permission.
func writes(node *yaml.Node) bool {
	if node.Kind == yaml.ScalarNode {
		return slices.Contains([]string{"write", "write-all"}, node.Value)
	}

	return slices.ContainsFunc(node.Content, writes)
}

// withoutKey returns a mapping node without the pair of one key.
func withoutKey(node *yaml.Node, key string) *yaml.Node {
	rest := *node
	rest.Content = nil

	for i := 0; i+1 < len(node.Content); i += 2 {
		if node.Content[i].Value != key {
			rest.Content = append(rest.Content, node.Content[i], node.Content[i+1])
		}
	}

	return &rest
}

type workflowFile struct {
	Permissions yaml.Node    `yaml:"permissions"`
	Jobs        workflowJobs `yaml:"jobs"`
}

// Derive reads the CI workflow files and returns the gate steps: one for each run step of each job, in file order. A step or a job that the gate list cannot hold is in Refused, with the reason, and never runs.
// A step stays out when its job or the file can write or reads a secret, when it uses an action other than a setup or checkout action, when its run text has a command that writes outside the machine or that no read can judge.
// Derive refuses a file that is not a workflow, and files with no run step to give.
func Derive(files []File) (Derived, error) {
	out, problems := Derived{}, []string(nil)

	for _, file := range files {
		got, bad := deriveFile(file)
		out.Steps = append(out.Steps, got.Steps...)
		out.Refused = append(out.Refused, got.Refused...)
		problems = append(problems, bad...)
	}

	if len(problems) == 0 && len(out.Steps) == 0 {
		problems = append([]string{"the workflow files give no run step for the gate list. Name the CI workflow files that hold the gate commands"}, refusedLines(out.Refused)...)
	}

	if len(problems) > 0 {
		return Derived{}, listRefusal(problems, nil)
	}

	out.Steps = uniqueNames(out.Steps)

	return out, nil
}

// Missing returns the names of the derived steps whose command the text of the gate of the round does not hold.
func Missing(steps []Step, gateText string) []string {
	var names []string

	for _, step := range steps {
		if !strings.Contains(gateText, step.Command) {
			names = append(names, step.Name)
		}
	}

	return names
}

func refusedLines(refused []Refused) []string {
	lines := make([]string, len(refused))
	for i, r := range refused {
		lines[i] = r.Name + " " + r.Reason
	}

	return lines
}

func deriveFile(file File) (Derived, []string) {
	var workflow workflowFile
	if err := yaml.Unmarshal([]byte(file.Text), &workflow); err != nil {
		return Derived{}, []string{fmt.Sprintf("%s: not a workflow file: %v", file.Path, err)}
	}

	var out Derived

	for _, job := range workflow.Jobs {
		got := deriveJob(job)

		if problem := job.Problem; problem != "" || writes(&workflow.Permissions) {
			got = Derived{Refused: []Refused{{"job " + file.Path + nameJoin + job.ID, firstProblem(problem)}}}
		}

		out.Steps = append(out.Steps, got.Steps...)
		out.Refused = append(out.Refused, got.Refused...)
	}

	return out, nil
}

// firstProblem returns the problem of a job, or the write permission of its file when the job has none.
func firstProblem(problem string) string {
	if problem == "" {
		return writePermission
	}

	return problem
}

func deriveJob(job workflowJob) Derived {
	var out Derived

	for i, step := range job.Steps {
		stepName := stepNameOf(job.ID, step, i+1)

		if problem := stepProblem(step); problem != "" {
			out.Refused = append(out.Refused, Refused{"step " + stepName, problem})
		} else if command := runCommand(step.Run); command != "" {
			out.Steps = append(out.Steps, Step{stepName, command})
		}
	}

	return out
}

// stepProblem says why a step cannot be a gate step, or gives an empty text.
func stepProblem(step workflowStep) string {
	switch {
	case step.Secret:
		return readsSecretText
	case step.Uses != "" && !setupAction(step.Uses):
		return "uses " + step.Uses + ", an action that is neither a checkout nor a setup. Add its command to the gate list by hand"
	case step.Shell != "" && !slices.Contains([]string{"bash", "sh"}, step.Shell):
		return "runs in the shell " + step.Shell + ", which the gate does not read"
	}

	if hits := shell.NetworkWrites(expressionPart.ReplaceAllLiteralString(step.Run, expressionText)); len(hits) > 0 {
		return "runs `" + hits[0] + "`, a command that writes outside the machine or that the gate cannot read"
	}

	return ""
}

// setupAction reports whether a uses value names a checkout action or a setup action, which the gate list needs no step for.
func setupAction(uses string) bool {
	name, _, _ := strings.Cut(strings.ToLower(uses), "@")

	return slices.Contains([]string{
		"actions/checkout", "actions/setup-go", "actions/setup-node", "actions/setup-python", "actions/setup-java", "actions/setup-dotnet",
	}, name)
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
