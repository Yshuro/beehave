package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"slices"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/rogpeppe/go-internal/testscript"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/tobocop2/beehave/internal/agent"
	"github.com/tobocop2/beehave/internal/agent/claude"
	"github.com/tobocop2/beehave/internal/cli"
	"github.com/tobocop2/beehave/internal/config"
	"github.com/tobocop2/beehave/internal/exectest"
	"github.com/tobocop2/beehave/internal/flow"
	"github.com/tobocop2/beehave/internal/gate"
	"github.com/tobocop2/beehave/internal/model"
	"github.com/tobocop2/beehave/internal/preflight"
	"github.com/tobocop2/beehave/internal/proc"
	"github.com/tobocop2/beehave/internal/review"
	"github.com/tobocop2/beehave/internal/sandbox/landlock"
	"github.com/tobocop2/beehave/internal/workflow/workflowtest"
)

// These tests run the beehave commands as separate processes, as testscript does for any command line tool.
// The collaborators of the workflow that later milestones add are files here; the agent is the real runner and starts a shell script named claude from PATH.

const (
	e2eVersion     = "e2e"
	e2eDirVariable = "E2E_DIR"
	e2eWait        = 120 * time.Second
	e2ePoll        = 100 * time.Millisecond
	e2eFileMode    = 0o600
	e2eFolderMode  = 0o700
)

func TestMain(m *testing.M) {
	// The test binary also acts as the Landlock helper, as beehave does in main, for the test that runs a gate under the real enforcer.
	if code, helper := landlock.RunHelper(os.Args, os.Stderr); helper {
		os.Exit(code)
	}

	testscript.Main(m, map[string]func(){"beehave": e2eBeehave, "beehavereal": e2eReal, "beehaveswarm": e2eSwarm})
}

// e2eBeehave is the beehave command of the scripts. It matches main, with the fake collaborators.
func e2eBeehave() {
	syscall.Umask(privateUmask)

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	env := config.ProcessEnv()

	e2eRecordRun(os.Args[1:])

	code := cli.Execute(ctx, os.Args[1:], cli.BuildInfo{Version: e2eVersion, Registry: newAdapters(nil, nil, nil).registry(), Flow: e2eFlow(env[e2eDirVariable], env["PATH"])}, env, os.Stdout, os.Stderr)
	stop()
	os.Exit(code)
}

// e2eFlow keeps the real client and gives the engine the fake collaborators.
func e2eFlow(dir, path string) cli.Flow {
	return cli.Flow{
		Client: func(ctx context.Context, _ config.Config, opts flow.Options) (cli.Steering, error) {
			files := e2eFiles{dir: dir}

			client, err := flow.OpenClient(ctx, opts, flow.ReleaseDeps{Lifecycle: files, Trees: files})
			if err != nil {
				return nil, err
			}

			return client, nil
		},
		Engine: func(ctx context.Context, cfg config.Config, opts flow.Options) (cli.Runner, error) {
			files := e2eFiles{dir: dir}

			engine, err := flow.NewEngine(ctx, withModules(opts), withBatch(flow.Deps{
				Agents: e2eAgents{dir: dir, path: path}, Gate: e2eGate{dir: dir}, Logins: e2eLogins{}, Commands: e2eCommands{dir: dir}, Slots: slotsOf(cfg.Projects), Facts: factsOf(cfg.Projects),
				Lifecycle: files, Forge: files, Watch: files, Trees: files, Beats: files, Items: files, Preflight: files, Reader: files, Hosts: files,
			}, dir, cfg))
			if err != nil {
				return nil, err
			}

			return engine, nil
		},
	}
}

// e2eFiles holds every collaborator that needs no process. It records each lifecycle call in events.log.
type e2eFiles struct{ dir string }

// Fields gives the item the fields that the prompts of the example read.
func (e2eFiles) Fields(_ context.Context, _, item string) (map[string]string, error) {
	return map[string]string{"id": item, "title": "title of " + item, "body": "body of " + item}, nil
}

// Preflight finds nothing changed: the scripts own no pull request before the first agent.
func (e2eFiles) Preflight(context.Context, string, string, preflight.Request, string) (preflight.Result, error) {
	return preflight.Result{}, nil
}

// Comments gives an item no comments: the pipeline of the example reads the parent of its item.
func (e2eFiles) Comments(context.Context, string, string) ([]string, error) { return nil, nil }

// Parent gives an item no parent.
func (e2eFiles) Parent(context.Context, string, string) (string, error) { return "", nil }

// Up starts no host: the project of the scripts has none. The finish function of the example stops the host that the round started.
func (e2eFiles) Up(context.Context, string, string) (bool, string, error) { return false, "", nil }

// Teardown stops no host.
func (e2eFiles) Teardown(context.Context, string, string) error { return nil }

// LostProof gives no proof: the project of the scripts has no host, so it has no host job.
func (e2eFiles) LostProof(context.Context, string, string, string) (model.LostProof, error) {
	return model.LostProof{}, nil
}

func (f e2eFiles) event(name, item string) error {
	log, err := os.OpenFile(filepath.Join(f.dir, "events.log"), os.O_APPEND|os.O_CREATE|os.O_WRONLY, e2eFileMode)
	if err != nil {
		return err
	}

	_, err = log.WriteString(name + " " + item + "\n")

	return errJoin(err, log.Close())
}

func (f e2eFiles) Claim(_ context.Context, _, item string) (string, error) {
	return "owner", f.event("claim", item)
}

func (f e2eFiles) Ready(_ context.Context, _, item string) error { return f.event("ready", item) }

func (e2eFiles) ClosingIssues(context.Context, string, string) ([]int, bool, error) {
	return nil, true, nil
}

func (e2eFiles) Comment(context.Context, string, string, string, string) (int, error) { return 0, nil }

func (e2eFiles) Open(context.Context, string, string, flow.OpenRequest) (flow.OpenResult, error) {
	return flow.OpenResult{}, nil
}

func (e2eFiles) PlaceCard(context.Context, string, string, flow.CardRequest) (int, error) {
	return 0, nil
}

func (e2eFiles) ReadCard(context.Context, string, string, flow.CardRequest) (string, error) {
	return "", nil
}

func (e2eFiles) Assign(context.Context, string, string, string, bool) (int, []int, error) {
	return 0, nil, nil
}

func (f e2eFiles) Release(_ context.Context, _, item, _ string) error {
	return f.event("release", item)
}

func (f e2eFiles) Pull(ctx context.Context, _, _ string) (review.Standing, error) {
	head, err := e2eHeadOf(ctx, f.dir)

	return review.Standing{Number: 1, Head: head}, err
}

func (f e2eFiles) Reviewed(ctx context.Context, project, item, _ string) (review.Standing, error) {
	return f.Pull(ctx, project, item)
}

func (e2eFiles) Watch(context.Context, string, int) (review.Outcome, error) {
	return review.Outcome{Kind: review.Merged}, nil
}

func (e2eFiles) SetDraft(context.Context, string, int, bool) error { return nil }

func (e2eFiles) WithTree(_ context.Context, _, _ string, fn func(string)) error {
	fn("")

	return nil
}

// Ensure gives the git folder that e2eSetup makes. The gate reads the commit of that folder.
func (f e2eFiles) Ensure(context.Context, string, string, string) (string, error) {
	return filepath.Join(f.dir, "tree"), nil
}

func (e2eFiles) Beat(context.Context, string, string) (string, error) { return "", nil }

func (e2eFiles) Renewed(context.Context, string, string, string) (time.Time, error) {
	return time.Time{}, nil
}

func errJoin(first, second error) error {
	if first != nil {
		return first
	}

	return second
}

// e2eHeadOf reads the commit of the git folder that e2eSetup makes. The gate and the pull request show it.
func e2eHeadOf(ctx context.Context, dir string) (string, error) {
	result := proc.NewExecRunner("git", filepath.Join(dir, "tree")).Run(ctx, []string{"rev-parse", "HEAD"})
	if result.ExitCode != 0 {
		return "", fmt.Errorf("git rev-parse: %s", result.Stderr)
	}

	return strings.TrimSpace(result.Stdout), nil
}

// e2eGate passes every item. A gate with a base, the gate of a candidate, also gives a clean check record: one new test, a clean secret scan,
// and a revert run in which the new test fails on the base code.
type e2eGate struct{ dir string }

func (e2eGate) Submit(_ context.Context, _, _, _, base string) (flow.GateJob, error) {
	return flow.GateJob{Plan: gate.Plan{Base: base}}, nil
}

func (g e2eGate) Run(ctx context.Context, _, _, _ string, job flow.GateJob) (flow.GateResult, error) {
	head, err := e2eHeadOf(ctx, g.dir)
	result := flow.GateResult{Passed: true, Head: head, Tree: "tree-of-" + head}

	if job.Plan.Base != "" {
		result.Checks = &flow.GateChecks{NewTests: []string{"shop TestE2E"}, New: 1, Secrets: new(int), Complete: true, Revert: model.RevertPass, RevertCases: 1}
	}

	return result, err
}

func (g e2eGate) Key(context.Context, string, string, string) (flow.GateKey, error) {
	return flow.GateKey{}, nil
}

// e2eCommands answers the command calls of the example: the mutation job on the host of the project.
// Each call adds its command line to mutation.log. The Nth call takes the Nth line of the file mutations: "lived" is a failed job that printed one finding, "silent" is a failed job that printed nothing, any other text or no line is a clean run.
type e2eCommands struct{ dir string }

func (c e2eCommands) Run(_ context.Context, request flow.CommandRequest) (flow.CommandOutput, error) {
	log, err := os.OpenFile(filepath.Join(c.dir, "mutation.log"), os.O_APPEND|os.O_CREATE|os.O_RDWR, e2eFileMode)
	if err != nil {
		return flow.CommandOutput{}, err
	}

	if _, err := log.WriteString(request.Host + " " + request.SlotClass + " " + strings.Join(request.Run, " ") + "\n"); err != nil {
		return flow.CommandOutput{}, errJoin(err, log.Close())
	}

	text, err := os.ReadFile(log.Name())
	if err := errJoin(err, log.Close()); err != nil {
		return flow.CommandOutput{}, err
	}

	answers, err := os.ReadFile(filepath.Join(c.dir, "mutations"))
	if err != nil && !os.IsNotExist(err) {
		return flow.CommandOutput{}, err
	}

	lines := strings.Split(string(answers), "\n")

	n := strings.Count(string(text), "\n")
	if n > len(lines) {
		return flow.CommandOutput{}, nil
	}

	switch lines[n-1] {
	case "lived":
		return flow.CommandOutput{Stdout: "      KILLED CONDITIONALS_NEGATION at internal/a.go:30:4\nfinding: internal/a.go:12: a mutant lived: CONDITIONALS_NEGATION\n", RC: 1}, nil
	case "silent":
		return flow.CommandOutput{RC: 1}, nil
	}

	return flow.CommandOutput{}, nil
}

// e2eAgents starts the claude program of the script PATH through the real agent runner.
type e2eAgents struct{ dir, path string }

// It describes the run as the real agents do: the session from the output of the program, the skill calls from the tool_use lines that it printed.
// A start after a lost run resumes its session or adds the note, as the real agents decide it.
func (a e2eAgents) Run(ctx context.Context, request flow.AgentRequest) (agent.Result, error) {
	adapter := newAdapters(nil, nil, nil).agent(claude.Name)
	runner := model.Runner{Name: request.Login, Agent: claude.Name, ConfigDir: a.dir}
	request, resumed := replayOf(request, runner, a.dir, adapter)
	prompt := "login=" + request.Login + "\n" + request.Prompt
	env := map[string]string{"PATH": a.path}
	tap := newSessionTap(adapter, agent.DefaultPrices(), request.Named)
	protect := protectionOf(request.Protect, a.dir)

	result := agent.Runner{}.Run(ctx, agent.Spec{
		Adapter: adapter, Runner: runner,
		Dir: a.dir, Prompt: prompt, Env: env, Log: tap, OnStart: request.Started, NoEdits: request.NoEdits, Resume: request.Resume,
		Guard: agents{guard: guardCommand(os.Executable)}.guardFor(request.Forbid, protect),
	})

	result, err := endOf(adapter, runner, a.dir, tap, result, request.Forbid, protect)
	result.Resumed = resumed

	return result, err
}

// The agent runs in the work folder of the script and sees no other variable than PATH.
// The file hang names the step that blocks on its first call; the file started marks that the block began.
const e2eClaude = `#!/bin/sh
# The fake claude: one line in calls.log for each call. Fields: step round login ruling.
# A prompt that carries a seen value adds it as a line of seen.log.
# A prompt that carries a found value adds its first line to found.log, with the step in front.
# A fix stages everything when the next line of the file fixhits says hit.
# A review writes the next line of the file verdicts to the output file that its prompt names, with no open finding.
# When the file obey-hook exists, the fake obeys --settings as Claude Code does: it runs the hook command of the settings before each Bash call.
# A hook that exits with code 2 blocks the call: the fake adds a line to blocked.log and prints the tool_result of the refusal.
# The implement step also makes one Bash call for each line of the file cmds, when it exists.
# When the file edit-file exists, the implement step writes edited.txt with the Write tool. As with the real claude in print mode,
# the write is refused unless the start arguments hold --permission-mode acceptEdits: the fake adds a line to refused.log and prints the tool_result of the refusal.
# When the file hang-session exists, the call that blocks first names the session of that file in an init line and writes the transcript of the session:
# the prompt, under the transcript folder of the login. A call with --resume <id> adds its prompt to resumed.log.
# A resumed call whose prompt names no step (the continue prompt) reads the prompt of the session from its transcript, as the real claude continues a session that holds its brief.
# Each resumed call then adds "<step> --resume <id>" to resumes.log, with the flag and the id as it got them.
hook=
mode=
resume=
resumed=
while [ $# -gt 0 ]; do
  if [ "$1" = --settings ]; then hook=$(printf '%s' "$2" | sed 's/.*"command":"\([^"]*\)".*/\1/'); fi
  if [ "$1" = --permission-mode ]; then mode=$2; fi
  if [ "$1" = --resume ]; then resume=$2; resumed="$1 $2"; fi
  shift
done
write_call() {
  echo '{"type":"assistant","message":{"content":[{"type":"tool_use","id":"'"$1"'","name":"Write","input":{"file_path":"'"$2"'"}}]}}'
  if [ "$mode" = acceptEdits ]; then
    echo edited > "$2"
  else
    echo "refused $2" >> refused.log
    echo '{"type":"user","message":{"content":[{"type":"tool_result","tool_use_id":"'"$1"'","is_error":true,"content":"Claude requested permissions to write to '"$2"', but you haven'"'"'t granted it yet."}]}}'
  fi
}
bash_call() {
  echo '{"type":"assistant","message":{"content":[{"type":"tool_use","id":"'"$1"'","name":"Bash","input":{"command":"'"$2"'"}}]}}'
  if [ -e obey-hook ] && [ -n "$hook" ]; then
    out=$(printf '{"hook_event_name":"PreToolUse","tool_name":"Bash","tool_input":{"command":"%s"}}' "$2" | ( eval "$hook" ) 2>&1 >/dev/null)
    code=$?
    if [ "$code" = 2 ]; then
      echo "blocked $2" >> blocked.log
      echo '{"type":"user","message":{"content":[{"type":"tool_result","tool_use_id":"'"$1"'","is_error":true,"content":"PreToolUse:Bash hook error: '"$(printf '%s\n' "$out" | sed -n 1p)"'"}]}}'
    fi
  fi
}
prompt=$(cat)
field() { printf '%s\n' "$prompt" | sed -n "s/^$1=//p"; }
if [ -n "$resume" ]; then
  printf '%s\n' "$prompt" >> resumed.log
  if [ -z "$(field step)" ]; then prompt=$(cat "%TRANSCRIPTS%/$resume.jsonl"); fi
  echo "$(field step) $resumed" >> resumes.log
fi
step=$(field step)
echo "$step $(field round) $(field login) $(field ruling)" >> calls.log
seen=$(field seen)
if [ -n "$seen" ]; then echo "$seen" >> seen.log; fi
found=$(field found)
if [ -n "$found" ]; then echo "$step: $found" >> found.log; fi
# A prompt that carries a found value also adds all its lines, from that value to the end, to findings.log.
if [ -n "$found" ]; then printf '%s\n' "$prompt" | sed -n '/^found=/,$p' >> findings.log; fi
if [ -e hang ] && [ "$step" = "$(cat hang)" ] && [ ! -e started ]; then
  if [ -e hang-session ]; then
    session=$(cat hang-session)
    echo '{"type":"system","subtype":"init","session_id":"'"$session"'"}'
    mkdir -p "%TRANSCRIPTS%"
    printf '%s\n' "$prompt" > "%TRANSCRIPTS%/$session.jsonl"
  fi
  : > started
  exec sleep 300
fi
if [ "$step" = review ]; then
  n=$(grep -c '^review ' calls.log)
  printf '{"verdict":"%s","mechanism":"","regressions":"0","findings":[]}\n' "$(sed -n "${n}p" verdicts)" > "$(field write)"
fi
if [ "$step" = plan ]; then
  printf '{"regressions":"0"}\n' > "$(field write)"
fi
if [ "$step" = settle ]; then
  printf '{"parked":"[]"}\n' > "$(field write)"
fi
# The tool calls arrive as assistant lines of the stream, as with the real claude.
# The fake claude calls the skill that the example requires, so the session reports it.
echo '{"type":"assistant","message":{"content":[{"type":"tool_use","name":"Skill","input":{"skill":"%SKILL%"}}]}}'
# The implementer stages everything, the command that the example forbids.
if [ "$step" = implement ]; then
  if [ -e edit-file ]; then write_call toolu_edit edited.txt; fi
  bash_call toolu_impl "git add -A"
  if [ -e cmds ]; then
    i=0
    while IFS= read -r c; do
      i=$((i + 1))
      bash_call "toolu_cmd$i" "$c"
    done < cmds
  fi
fi
if [ "$step" = fix ]; then
  n=$(grep -c '^fix ' calls.log)
  if [ "$(sed -n "${n}p" fixhits 2>/dev/null)" = hit ]; then
    bash_call "toolu_fix$n" "git add -A"
  fi
fi
echo '{"type":"result","session_id":"'"${resume:-s1}"'","result":"done"}'
`

// e2eLogins picks the runner "a" of the config, so no script reads the usage of a login.
type e2eLogins struct{}

func (e2eLogins) Pick(context.Context, string, string, []string, bool) (string, error) {
	return "a", nil
}

func (e2eLogins) Reset(context.Context, string, string) (time.Time, bool, error) {
	return time.Time{}, false, nil
}

// e2ePrompts replace the prompts of the example in the work folder. The workflow file is the example itself.
var e2ePrompts = map[string]string{
	"plan":      "step=plan\nwrite={{ .output_file }}\n",
	"implement": "step=implement\n",
	"review":    "step=review\nwrite={{ .output_file }}\nseen={{ .violations }}\n",
	"fix":       "step=fix\nround={{ .round }}\nfound={{ .findings }}\n",
	"settle":    "step=settle\nwrite={{ .output_file }}\nruling={{ .ruling }}\n",
	"audit":     "step=audit\nseen={{ .seen }}\n",
}

const e2eRunners = `agents(max_concurrent = 2)

runner(name = "a", agent = "claude", config_dir = "%DIR%")

runner(name = "b", agent = "claude", config_dir = "%DIR%")
`

const e2eProject = `project(
    repo = "example/shop",
    tracker = "github",
    forge = "github",
    claims = "beads",
    beads = beads(prefix = "shop"),
    base_branch = "main",
    gate = "true",
    test_report = "r.xml",
    build_host = "build",
    host = host(provider = "ssh", disk_floor_gb = 50, slots = {"mutation": 1}, ssh = ssh(address = "10.0.0.5")),
    vars = {
        "round": {"base": "integration", "owner": "owner", "lane_cap": 1, "fix_rounds": 5, "source_checkout": "/own/checkout", "label": "round"},
        "mutation_command": ["sh", "mutation.sh"],
    },
)
`

// exampleMain is the one entry of the example that the scripts replace.
const exampleMain = "def main(item):"

// oneItemCopy returns the text of the example for the scripts that run one item: its main is renamed round_main and a new main runs pipeline.
// The scripts prove the engine in a real process over the pipeline. A full round needs the queue, remedy, a base branch and a host.
func oneItemCopy(example string) (string, error) {
	if found := strings.Count(example, exampleMain); found != 1 {
		return "", fmt.Errorf("the example holds %d lines %q, want 1", found, exampleMain)
	}

	return strings.Replace(example, exampleMain, "def round_main(item):", 1) + "\n" + exampleMain + "\n    return pipeline(item)\n", nil
}

// exampleSkill returns the one skill that an agent of examples/remediation/workflow.star requires.
func exampleSkill() (string, error) {
	text, err := os.ReadFile(filepath.Join("..", "..", "examples", "remediation", "workflow.star"))
	if err != nil {
		return "", err
	}

	calls, err := workflowtest.Calls("workflow.star", text)
	if err != nil {
		return "", err
	}

	var skills []string

	for _, call := range calls {
		if call.Builtin == "agent" {
			skills = append(skills, workflowtest.Literals(call.Args["require_skills"])...)
		}
	}

	if len(skills) != 1 {
		return "", fmt.Errorf("the example requires %d skills, want 1", len(skills))
	}

	return skills[0], nil
}

// e2eFile is a file of the work folder of a script; a program is a file that the script starts.
type e2eFile struct {
	text    string
	program bool
}

// writeE2EFiles writes each file below work, with the folders it needs.
func writeE2EFiles(work string, files map[string]e2eFile) error {
	for name, file := range files {
		path := filepath.Join(work, name)
		if err := os.MkdirAll(filepath.Dir(path), e2eFolderMode); err != nil {
			return err
		}

		if err := writeE2EFile(path, file); err != nil {
			return err
		}
	}

	return nil
}

func writeE2EFile(path string, file e2eFile) error {
	if file.program {
		return exectest.Write(path, file.text)
	}

	return os.WriteFile(path, []byte(file.text), e2eFileMode)
}

// e2eSetup writes the config folder and the fake claude into the work folder of a script.
func e2eSetup(env *testscript.Env) error {
	work := env.WorkDir
	skill, err := exampleSkill()
	if err != nil {
		return err
	}

	// The fake claude keeps a transcript where the adapter looks for it: the agent of the scripts runs in the work folder with the work folder as its login folder.
	transcripts := filepath.Dir(newAdapters(nil, nil, nil).agent(claude.Name).TranscriptPath(work, work, "session"))
	fake := strings.NewReplacer("%SKILL%", skill, "%TRANSCRIPTS%", transcripts).Replace(e2eClaude)

	files := map[string]e2eFile{
		"bin/claude":                {fake, true},
		"config/runners.star":       {strings.ReplaceAll(e2eRunners, "%DIR%", work), false},
		"config/projects/shop.star": {e2eProject, false},
	}

	example, err := os.ReadFile(filepath.Join("..", "..", "examples", "remediation", "workflow.star"))
	if err != nil {
		return err
	}

	copied, err := oneItemCopy(string(example))
	if err != nil {
		return err
	}

	files["flow/workflow.star"] = e2eFile{copied, false}

	for name, text := range e2ePrompts {
		files["flow/prompts/"+name+".md"] = e2eFile{text, false}
	}

	if err := writeE2EFiles(work, files); err != nil {
		return err
	}

	tree := filepath.Join(work, "tree")
	if err := os.MkdirAll(tree, e2eFolderMode); err != nil {
		return err
	}

	// The remote branch of the round base points at the first commit, as in a clone, so the checks before review find a head that descends from the base.
	for _, args := range [][]string{
		{"init", "-q"},
		{"-c", "user.name=e2e", "-c", "user.email=e2e@example.com", "commit", "-q", "--allow-empty", "-m", "start"},
		{"update-ref", "refs/remotes/origin/integration", "HEAD"},
	} {
		if result := proc.NewExecRunner("git", tree).Run(context.Background(), args); result.ExitCode != 0 {
			return fmt.Errorf("git %s: %s", args[0], result.Stderr)
		}
	}

	env.Setenv("PATH", filepath.Join(work, "bin")+string(os.PathListSeparator)+env.Getenv("PATH"))
	env.Setenv("BEEHAVE_CONFIG", filepath.Join(work, "config"))
	env.Setenv("BEEHAVE_STATE_DIR", filepath.Join(work, "state"))
	env.Setenv(e2eDirVariable, work)

	return nil
}

// cmdWaitFile is "waitfile <path>": it waits until the file exists.
func cmdWaitFile(ts *testscript.TestScript, neg bool, args []string) {
	if len(args) != 1 {
		ts.Fatalf("usage: waitfile <path>")
	}

	path := ts.MkAbs(args[0])
	e2eRunWait(ts, neg, e2eWaitingFor(ts, path, func() bool {
		_, err := os.Stat(path)

		return err == nil
	}))
}

// cmdWaitSession is "waitsession <id>": it waits until the log of the engine, flow.log in the state folder, says that the engine saved the session id.
// The engine saves the id on a goroutine of its own after the agent names it, so a kill that only waits for the agent could come before the save.
func cmdWaitSession(ts *testscript.TestScript, neg bool, args []string) {
	if len(args) != 1 {
		ts.Fatalf("usage: waitsession <id>")
	}

	e2eRunWait(ts, neg, e2eWaitingFor(ts, "the engine to save the session "+args[0], func() bool {
		text, err := os.ReadFile(filepath.Join(ts.Getenv("BEEHAVE_STATE_DIR"), "flow.log"))

		return err == nil && slices.ContainsFunc(strings.Split(string(text), "\n"), func(line string) bool {
			return strings.Contains(line, `msg="saved the session of the agent"`) && strings.HasSuffix(line, " session="+args[0])
		})
	}))
}

// cmdWaitStatus is "waitstatus <text>": it runs beehave status until the output holds the text.
func cmdWaitStatus(ts *testscript.TestScript, neg bool, args []string) {
	if len(args) != 1 {
		ts.Fatalf("usage: waitstatus <text>")
	}

	e2eRunWait(ts, neg, e2eWaitingFor(ts, "beehave status to show "+args[0], func() bool {
		out, err := e2eStatus(ts)

		return err == nil && strings.Contains(out, args[0])
	}))
}

func TestE2E_TheOneItemCopyHasOneMain(t *testing.T) {
	t.Parallel()

	example, err := os.ReadFile(filepath.Join("..", "..", "examples", "remediation", "workflow.star"))
	require.NoError(t, err)

	copied, err := oneItemCopy(string(example))
	require.NoError(t, err)

	assert.Equal(t, 1, strings.Count(copied, exampleMain))
	assert.Contains(t, copied, "def round_main(item):")
	assert.True(t, strings.HasSuffix(copied, exampleMain+"\n    return pipeline(item)\n"))
}

func TestE2E_TheOneItemCopyRefusesAnExampleWithTwoMains(t *testing.T) {
	t.Parallel()

	_, err := oneItemCopy("def main(item):\n    pass\ndef main(item):\n    pass\n")

	require.ErrorContains(t, err, "holds 2 lines")
}

func TestScripts(t *testing.T) {
	t.Parallel()

	tools := writeTools(t)

	testscript.Run(t, testscript.Params{
		Dir:   "testdata/e2e",
		Setup: e2eSetup,
		Cmds: map[string]func(*testscript.TestScript, bool, []string){
			"waitfile": cmdWaitFile, "waitstatus": cmdWaitStatus, "waitsession": cmdWaitSession,
			"example": cmdExample, "script": cmdScript, "subst": cmdSubst, "isolate": isolateFunc(tools), "exitcode": cmdExitCode, "noclaims": cmdNoClaims, "mode": cmdMode,
			"swarmfakes": cmdSwarmFakes, "batchsetup": cmdBatchSetup, "replace": cmdReplace, "serve": cmdServe, "fakeclaude": cmdFakeClaude, "deskfakes": cmdDeskFakes, "deskinit": cmdDeskInit,
			"incidentfakes": cmdIncidentFakes, "incidentinit": cmdIncidentInit, "maintfakes": cmdMaintFakes, "maintinit": cmdMaintInit,
			"rdinit": cmdResearchDeskInit, "rdfakes": cmdResearchDeskFakes,
			"pipeinit": cmdPipelineInit, "pipefakes": cmdPipelineFakes,
			"approvalsfakes": cmdApprovalsFakes, "approvalsinit": cmdApprovalsInit, "approvalskeys": cmdApprovalsKeys,
		},
	})
}
