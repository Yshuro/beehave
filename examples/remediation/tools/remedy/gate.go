package remedy

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"path/filepath"
	"slices"
	"strings"

	"github.com/tobocop2/beehave/examples/remediation/tools/brief"
	"github.com/tobocop2/beehave/examples/remediation/tools/check"
	"github.com/tobocop2/beehave/examples/remediation/tools/gate"
	"github.com/tobocop2/beehave/examples/remediation/tools/job"
)

// errNoGateSteps is the refusal of a map with no gate step names: the project sets gate, not gate_steps.
var errNoGateSteps = errors.New("--map holds the CI commands to gate_steps, and the project has no gate_steps. Set gate_steps in the project file, one named step for each command of the gate")

const (
	gatelistSyntax = "gatelist [--steps <name>]... [--map <json>] <workflow-file>..."
	jobSyntax      = "job <script> --list <file> [--job-dir <folder>]"
	reportSyntax   = "report <file> --head <sha>"
)

// gateCommands are the subcommands that judge the gate list, a host job and the report of a lane.
func gateCommands() []command {
	return []command{
		{
			name:    "gatelist",
			usage:   gatelistSyntax,
			summary: "derive the gate steps from the run steps of the CI workflow files, list the steps and jobs that the list leaves out (a secret, a token that can write, an action that is not a setup, a shell other than bash or sh, a git write or a text no read can judge), and with --steps and --map refuse a gate_map that does not map each derived command exactly to gate_steps names",
			run:     gatelistCommand,
		},
		{
			name:    "job",
			usage:   jobSyntax,
			summary: "check a host job script against the gate list: every step runs or declares a skip, tests have a timeout, no bulk staging, and the final marker prints a real exit status; --job-dir is the folder that a sourced file is read from, the folder of the script when not given",
			run:     jobCommand,
		},
		{
			name:    "report",
			usage:   reportSyntax,
			summary: "check the four tables of a lane report and that the report names the head commit",
			run:     reportCommand,
		},
	}
}

// printable keeps the bytes that are not UTF-8 out of a printed line.
func printable(lines []string) []string {
	out := make([]string, len(lines))
	for i, line := range lines {
		out[i] = strings.ToValidUTF8(line, "�")
	}

	return out
}

// gatelistCommand derives the gate list from the CI workflow files and prints one line for each step, then one line for each step or job that the list leaves out.
// A file that cannot be read, files with no step to give, and (with --steps or --map) a map that does not match the derived commands are exit code 2.
func gatelistCommand(_ context.Context, _ Deps, s streams, args []string) int {
	var given gatelistFlags

	paths, ok := gatelistArgs(s, args, &given)
	if !ok {
		return ExitRefused
	}

	lines, err := gatelistLines(paths, given)
	if err != nil {
		return refused(s, "gatelist", err)
	}

	return showLines(s, printable(lines), false)
}

// gatelistFlags are the gate step names of the round and the gate_map of the project. mapped is set when --map is given.
type gatelistFlags struct {
	steps  []string
	held   gate.Map
	mapped bool
}

func (f *gatelistFlags) declare(flags *flag.FlagSet) {
	flags.Func("steps", "a name of the gate_steps of the round; give it once for each step", func(name string) error {
		f.steps = append(f.steps, name)

		return nil
	})
	flags.Func("map", "the gate_map of the project as JSON: each derived command to the gate_steps names that run it, or [] when it is not a check", func(text string) error {
		f.mapped = true

		return json.Unmarshal([]byte(text), &f.held)
	})
}

// gatelistArgs reads the flags and the workflow files. It shows the usage and gives false for a bad flag or for no file.
func gatelistArgs(s streams, args []string, given *gatelistFlags) ([]string, bool) {
	flags := flag.NewFlagSet("remedy gatelist", flag.ContinueOnError)
	flags.SetOutput(s.err)
	given.declare(flags)

	if flags.Parse(args) != nil {
		return nil, false
	}

	if flags.NArg() == 0 {
		wrongUsage(s.err, gatelistSyntax)

		return nil, false
	}

	return flags.Args(), true
}

// gatelistLines reads the workflow files and gives the lines of the list. With --steps or --map, a defect of the map is a refusal that carries the lines and one line for each defect.
func gatelistLines(paths []string, given gatelistFlags) ([]string, error) {
	files, err := workflowFiles(paths)
	if err != nil {
		return nil, err
	}

	derived, err := gate.Derive(files)
	if err != nil {
		return nil, err
	}

	return holdLines(stepLines(derived), derived.Steps, given)
}

// holdLines gives the lines of the list. With --steps or --map, a map with no gate step names, or with a defect against the derived steps, is a refusal.
func holdLines(lines []string, steps []gate.Step, given gatelistFlags) ([]string, error) {
	switch defects := gate.Hold(steps, given.steps, given.held); {
	case !given.mapped && len(given.steps) == 0:
		return lines, nil
	case len(given.steps) == 0:
		return nil, errNoGateSteps
	case len(defects) > 0:
		return nil, &check.Refusal{Message: "the gate_map of the project does not match the CI commands. Map each derived command, exactly as the step line prints it, to the gate_steps that run it or to [] when it is not a check, and remove each stale key", Lines: printable(append(lines, defects...))}
	}

	return lines, nil
}

// workflowFiles reads each workflow file.
func workflowFiles(paths []string) ([]gate.File, error) {
	texts, err := readTexts(paths...)
	if err != nil {
		return nil, err
	}

	files := make([]gate.File, len(paths))
	for i, path := range paths {
		files[i] = gate.File{Path: path, Text: texts[i]}
	}

	return files, nil
}

// stepLines gives one line for each derived step, one for each refused step or job, then the summary.
func stepLines(derived gate.Derived) []string {
	lines := make([]string, 0, len(derived.Steps)+len(derived.Refused)+1)
	for _, step := range derived.Steps {
		lines = append(lines, "step "+step.Name+": "+step.Command)
	}

	for _, r := range derived.Refused {
		lines = append(lines, "refused "+r.Name+": "+r.Reason)
	}

	return append(lines, fmt.Sprintf("check-gatelist: OK (steps %d, refused %d)", len(derived.Steps), len(derived.Refused)))
}

// jobFlags are the values of the flags of job.
type jobFlags struct {
	list, dir string
}

func (f *jobFlags) declare(flags *flag.FlagSet) {
	flags.StringVar(&f.list, "list", "", "the gate list that the job must follow")
	flags.StringVar(&f.dir, "job-dir", "", "the folder that a file the job sources is read from; the folder of the script when not given")
}

// flagGiven reports whether a required flag has a value. With none, it shows the usage.
func flagGiven(errOut io.Writer, syntax, value string) bool {
	if value == "" {
		say(errOut, "usage: remedy %s\n", syntax)
	}

	return value != ""
}

// readTexts reads each file, and gives the refusal of the first file that cannot be read.
func readTexts(paths ...string) ([]string, error) {
	texts := make([]string, len(paths))

	for i, path := range paths {
		text, err := brief.ReadText(path)
		if err != nil {
			return nil, err
		}

		texts[i] = text
	}

	return texts, nil
}

// readSource reads a file that the job sources, and gives false when it cannot be read.
func readSource(path string) (string, bool) {
	text, err := brief.ReadText(path)

	return text, err == nil
}

// jobCommand checks a job script against the gate list. A defect in the job is exit code 1. A gate list that cannot be read, a missing --list and a file that cannot be read are exit code 2.
func jobCommand(_ context.Context, _ Deps, s streams, args []string) int {
	var given jobFlags

	script, ok := parseSubject("job", jobSyntax, args, s.err, given.declare)
	if !ok || !flagGiven(s.err, jobSyntax, given.list) {
		return ExitRefused
	}

	texts, err := readTexts(script, given.list)
	if err != nil {
		return refused(s, "job", err)
	}

	return checkJob(s, texts[0], texts[1], given.list, sourceDir(given.dir, script))
}

// checkJob judges the text of a job script against the text of the gate list. A gate list that cannot be read keeps the lines of the job script first.
func checkJob(s streams, script, list, listPath, dir string) int {
	own := job.Script(script)

	result, err := gate.Job(script, list, listPath, dir, readSource)
	if err != nil {
		show(s.out, printable(own))

		return refused(s, "job", err)
	}

	lines, defect := jobLines(own, result)

	return showLines(s, printable(lines), defect)
}

// jobLines joins the lines of the job script and of the gate comparison. A job with no defect ends with the summary.
func jobLines(own []string, result gate.Result) ([]string, bool) {
	lines := slices.Concat(own, result.Lines)
	defect := len(own) > 0 || !result.OK()

	if !defect {
		lines = append(lines, result.Summary())
	}

	return lines, defect
}

// sourceDir is the folder that a sourced file is read from: the one that is given, else the folder of the script.
func sourceDir(dir, script string) string {
	if dir != "" {
		return dir
	}

	return filepath.Dir(script)
}

// reportFlags are the values of the flags of report.
type reportFlags struct {
	head string
}

func (f *reportFlags) declare(flags *flag.FlagSet) {
	flags.StringVar(&f.head, "head", "", "the commit that the report must name")
}

// reportCommand checks the report of a lane in a file. A defect in the report is exit code 1. A missing --head and a file that cannot be read are exit code 2.
func reportCommand(_ context.Context, _ Deps, s streams, args []string) int {
	var given reportFlags

	path, ok := parseSubject("report", reportSyntax, args, s.err, given.declare)
	if !ok || !flagGiven(s.err, reportSyntax, given.head) {
		return ExitRefused
	}

	texts, err := readTexts(path)
	if err != nil {
		return refused(s, "report", err)
	}

	result := gate.Report(texts[0], given.head)
	if result.OK() {
		return showLines(s, []string{result.Summary(given.head)}, false)
	}

	return showLines(s, printable(result.Failures), true)
}
