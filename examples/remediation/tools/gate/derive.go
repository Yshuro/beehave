package gate

import (
	"cmp"
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
	readAll        = "read-all"

	writePermission = "can write: the token of the run is not read only. Set permissions to read-all, {} or read and none values, at the job or the file"
	readsSecretText = "reads a secret"
)

var (
	expressionPart = regexp.MustCompile(`\$\{\{.*?\}\}`)
	nonNameRun     = regexp.MustCompile(`[^a-z0-9]+`)
	// secretsPart names the secrets context, or the token of the run: github.token, an index of github, or the github context passed whole.
	secretsPart = regexp.MustCompile(`(?i)\bsecrets\b|\bgithub\s*(?:\.\s*token\b|[\[,)}])`)
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

// runDefaults is the defaults key of a file or a job: the shell of its run steps. Secret is set when a key or value of it names a secret.
type runDefaults struct {
	Run struct {
		Shell string `yaml:"shell"`
	} `yaml:"run"`
	Secret bool `yaml:"-"`
}

func (d *runDefaults) UnmarshalYAML(node *yaml.Node) error {
	type plain runDefaults

	d.Secret = readsSecret(node)

	return node.Decode((*plain)(d))
}

// readsSecret reports whether a node holds a name or a value that names a secret or the token of the run.
func readsSecret(node *yaml.Node) bool {
	if node.Kind == yaml.ScalarNode {
		return secretsPart.MatchString(node.Value)
	}

	return slices.ContainsFunc(node.Content, readsSecret)
}

// workflowJob is one job of a CI workflow file, with the id it has in the file. Secret is set when a key or value outside its steps names a secret.
type workflowJob struct {
	ID          string
	Uses        string
	Permissions yaml.Node
	Shell       string
	Secret      bool
	Steps       []workflowStep
}

// workflowJobs holds the jobs of a workflow file in file order, which a map would lose.
type workflowJobs []workflowJob

func (j *workflowJobs) UnmarshalYAML(node *yaml.Node) error {
	if node.Kind != yaml.MappingNode {
		return errors.New("jobs must be a mapping of job ids to jobs")
	}

	for i := 0; i+1 < len(node.Content); i += 2 {
		job, err := decodeJob(node.Content[i].Value, node.Content[i+1])
		if err != nil {
			return err
		}

		*j = append(*j, job)
	}

	return nil
}

// decodeJob reads one job of a workflow file by its id.
func decodeJob(id string, node *yaml.Node) (workflowJob, error) {
	var body struct {
		Uses        string         `yaml:"uses"`
		Permissions yaml.Node      `yaml:"permissions"`
		Defaults    runDefaults    `yaml:"defaults"`
		Steps       []workflowStep `yaml:"steps"`
	}

	if err := node.Decode(&body); err != nil {
		return workflowJob{}, fmt.Errorf("job %s: %w", id, err)
	}

	return workflowJob{ID: id, Uses: body.Uses, Permissions: body.Permissions, Shell: body.Defaults.Run.Shell, Secret: readsSecret(withoutKey(node, stepsKey)), Steps: body.Steps}, nil
}

// readOnly reports whether a permissions node gives the token of the run no write: read-all, or a mapping of read and none values only. A missing node takes the default of the repository, which can write.
func readOnly(node *yaml.Node) bool {
	if node.Kind == yaml.ScalarNode {
		return node.Value == readAll
	}

	if node.Kind != yaml.MappingNode {
		return false
	}

	for i := 1; i < len(node.Content); i += 2 {
		if value := node.Content[i]; value.Kind != yaml.ScalarNode || !slices.Contains([]string{"read", "none"}, value.Value) {
			return false
		}
	}

	return true
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
	Env         yaml.Node    `yaml:"env"`
	Defaults    runDefaults  `yaml:"defaults"`
	Jobs        workflowJobs `yaml:"jobs"`
}

// Derive reads the CI workflow files and returns the gate steps: one for each run step of each job, in file order. A step or a job that the gate list cannot hold is in Refused, with the reason.
// A job stays out when it calls a reusable workflow, when its token can write, or when it or the file env or defaults names a secret. A step stays out when it names a secret, uses an action other than a setup or checkout action,
// runs in a shell other than bash or sh, or has run text that the shell reader finds a git write, an opaque command or an unparsed line in. Derive refuses a file that is not a workflow, and files with no run step to give.
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
		if problem := jobProblem(&workflow, job); problem != "" {
			out.Refused = append(out.Refused, Refused{"job " + file.Path + nameJoin + job.ID, problem})

			continue
		}

		got := deriveJob(job, cmp.Or(job.Shell, workflow.Defaults.Run.Shell))
		out.Steps = append(out.Steps, got.Steps...)
		out.Refused = append(out.Refused, got.Refused...)
	}

	return out, nil
}

// jobProblem says why a whole job cannot give gate steps, or gives an empty text. The permissions key of the job wins over the key of the file.
func jobProblem(file *workflowFile, job workflowJob) string {
	permissions := &file.Permissions
	if !job.Permissions.IsZero() {
		permissions = &job.Permissions
	}

	switch {
	case job.Uses != "":
		return "calls the reusable workflow " + job.Uses + ", which the gate does not read"
	case !readOnly(permissions):
		return writePermission
	case job.Secret || readsSecret(&file.Env) || file.Defaults.Secret:
		return readsSecretText
	}

	return ""
}

// deriveJob gives the steps of a job. shell is the default shell of its run steps, from the job or the file.
func deriveJob(job workflowJob, shell string) Derived {
	var out Derived

	for i, step := range job.Steps {
		stepName := stepNameOf(job.ID, step, i+1)

		if problem := stepProblem(step, cmp.Or(step.Shell, shell)); problem != "" {
			out.Refused = append(out.Refused, Refused{"step " + stepName, problem})
		} else if command := runCommand(step.Run); command != "" {
			out.Steps = append(out.Steps, Step{stepName, command})
		}
	}

	return out
}

// stepProblem says why a step cannot be a gate step, or gives an empty text. stepShell is the shell the step runs in, or an empty text for the default.
func stepProblem(step workflowStep, stepShell string) string {
	switch {
	case step.Secret:
		return readsSecretText
	case step.Uses != "" && !setupAction(step.Uses):
		return "uses " + step.Uses + ", an action that is neither a checkout nor a setup. Add its command to the gate list by hand"
	case stepShell != "" && !slices.Contains([]string{"bash", "sh"}, stepShell):
		return "runs in the shell " + stepShell + ", which the gate does not read"
	}

	findings := shell.ScanFor(expressionPart.ReplaceAllLiteralString(step.Run, expressionText), []model.FindingKind{model.FindingGitWrite, model.FindingOpaque, model.FindingUnparsed})
	if len(findings) > 0 {
		return "runs `" + findings[0].Text + "`, which the gate does not judge (" + string(findings[0].Kind) + ")"
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
