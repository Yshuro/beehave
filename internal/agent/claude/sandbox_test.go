package claude_test

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/tobocop2/beehave/internal/agent"
	"github.com/tobocop2/beehave/internal/agent/claude"
)

func plan() agent.SandboxPlan {
	return agent.SandboxPlan{
		Work: "/work/tree", GitDirs: []string{"/repo/.git"}, Read: []string{"/opt/tools"},
		Deny: []string{"/home/o/.ssh", "/home/o/.aws"}, Network: []string{"api.example.org"},
	}
}

// settingsOf returns the arguments before --settings and the parsed JSON after it.
func settingsOf(t *testing.T, args []string) ([]string, map[string]any) {
	t.Helper()

	require.GreaterOrEqual(t, len(args), 2)
	require.Equal(t, "--settings", args[len(args)-2])

	var parsed map[string]any
	require.NoError(t, json.Unmarshal([]byte(args[len(args)-1]), &parsed))

	return args[:len(args)-2], parsed
}

func TestSandbox_IsolatesTheSettingSourcesAndTheMCPServersAndRemovesTheWebTools(t *testing.T) {
	t.Parallel()

	flags, _ := settingsOf(t, claude.New(nil).Sandbox(plan(), "", "/run").Args)

	assert.Equal(t, []string{"--setting-sources", "", "--strict-mcp-config", "--disallowedTools", "WebFetch", "WebSearch"}, flags)
}

func TestSandbox_AsksTheCLIToScrubCredentialsFromTheEnvironmentOfItsSubprocesses(t *testing.T) {
	t.Parallel()

	launch := claude.New(nil).Sandbox(plan(), "", "/run")

	assert.Equal(t, map[string]string{"CLAUDE_CODE_SUBPROCESS_ENV_SCRUB": "1", "CLAUDE_ENV_FILE": "/run/sandbox-env.sh"}, launch.Env)
}

func TestSandbox_TheShellReadsTheGitVariablesOfTheEngineFromAnEnvFile(t *testing.T) {
	t.Parallel()

	p := plan()
	p.Identity = agent.GitIdentity{Name: "Ann O'Brien $HOME", Email: "ann@example.com"}
	runDir := t.TempDir()

	launch := claude.New(nil).Sandbox(p, "", runDir)

	envFile := filepath.Join(runDir, "sandbox-env.sh")
	require.Equal(t, envFile, launch.Env["CLAUDE_ENV_FILE"], "the CLI sources the file in its shell")
	require.Contains(t, launch.Files, "sandbox-env.sh")
	require.NoError(t, os.WriteFile(envFile, []byte(launch.Files["sandbox-env.sh"]), 0o600))

	// A POSIX shell that sources the file holds each git variable of the engine with its value unchanged.
	shell := exec.CommandContext(t.Context(), "sh", "-c", `. "$1" && env`, "sh", envFile) //nolint:gosec // a test sources its own temporary file
	shell.Env = []string{"PATH=/usr/bin:/bin"}
	out, err := shell.Output()
	require.NoError(t, err)

	lines := strings.Split(string(out), "\n")
	for name, value := range map[string]string{
		"GIT_CONFIG_GLOBAL": "/dev/null", "GIT_CONFIG_NOSYSTEM": "1",
		"GIT_AUTHOR_NAME": "Ann O'Brien $HOME", "GIT_AUTHOR_EMAIL": "ann@example.com",
		"GIT_COMMITTER_NAME": "Ann O'Brien $HOME", "GIT_COMMITTER_EMAIL": "ann@example.com",
	} {
		assert.Contains(t, lines, name+"="+value)
	}
}

func TestSandbox_TheEnvFileIsTheOnlyRunFolderPathTheSandboxReads(t *testing.T) {
	t.Parallel()

	args := claude.New(nil).Sandbox(plan(), "/bin/beehave check command", "/run/folder").Args

	assert.Equal(t, 1, strings.Count(args[len(args)-1], "/run/folder"), "the settings name the run folder once")

	_, settings := settingsOf(t, args)
	box, ok := settings["sandbox"].(map[string]any)
	require.True(t, ok)

	files, ok := box["filesystem"].(map[string]any)
	require.True(t, ok)
	assert.Contains(t, files["allowRead"], "/run/folder/sandbox-env.sh")
	assert.NotContains(t, files["allowRead"], "/run/folder")
}

func TestSandbox_WritesTheDenyByDefaultBaseTheCredentialLayerAndTheNetworkList(t *testing.T) {
	t.Parallel()

	_, settings := settingsOf(t, claude.New(nil).Sandbox(plan(), "", "/run").Args)

	want := `{
	  "sandbox": {
	    "enabled": true,
	    "failIfUnavailable": true,
	    "allowUnsandboxedCommands": false,
	    "filesystem": {
	      "denyRead": ["~/", "/home/o/.ssh", "/home/o/.aws"],
	      "allowRead": ["/work/tree", "/repo/.git", "/opt/tools", "/run/sandbox-env.sh"],
	      "denyWrite": ["/home/o/.ssh", "/home/o/.aws"]
	    },
	    "network": {"allowedDomains": ["api.example.org"], "strictAllowlist": true}
	  },
	  "permissions": {
	    "blockReadsOutsideWorkingDirectories": true,
	    "deny": ["WebFetch", "WebSearch",
	             "Read(//home/o/.ssh/**)", "Edit(//home/o/.ssh/**)",
	             "Read(//home/o/.aws/**)", "Edit(//home/o/.aws/**)"]
	  }
	}`

	got, err := json.Marshal(settings)
	require.NoError(t, err)
	assert.JSONEq(t, want, string(got))
}

func TestSandbox_DeniesThePatternsInTheSandboxAndInTheRulesOfTheFileTools(t *testing.T) {
	t.Parallel()

	p := plan()
	p.DenyPatterns = []string{"/work/tree/.env*", "/work/tree/**/.env*"}

	args := claude.New(nil).Sandbox(p, "", "/run").Args

	var parsed struct {
		Sandbox struct {
			Filesystem struct{ DenyRead, DenyWrite []string }
		}
		Permissions struct{ Deny []string }
	}

	require.NoError(t, json.Unmarshal([]byte(args[len(args)-1]), &parsed))

	for _, list := range [][]string{parsed.Sandbox.Filesystem.DenyRead, parsed.Sandbox.Filesystem.DenyWrite} {
		assert.Contains(t, list, "/work/tree/**/.env*")
		assert.Contains(t, list, "/work/tree/.env*")
	}

	for _, rule := range []string{"Read(//work/tree/**/.env*)", "Edit(//work/tree/**/.env*)", "Read(//work/tree/.env*)", "Edit(//work/tree/.env*)"} {
		assert.Contains(t, parsed.Permissions.Deny, rule)
	}
}

func TestSandbox_DeniesWritesToTheClosedPathsInTheSandboxAndTheEditRules(t *testing.T) {
	t.Parallel()

	p := plan()
	p.DenyWrite = []string{"/work/tree/.husky", "/work/tree/.git"}

	args := claude.New(nil).Sandbox(p, "", "/run").Args

	var parsed struct {
		Sandbox struct {
			Filesystem struct{ DenyRead, DenyWrite []string }
		}
		Permissions struct{ Deny []string }
	}

	require.NoError(t, json.Unmarshal([]byte(args[len(args)-1]), &parsed))

	assert.Contains(t, parsed.Sandbox.Filesystem.DenyWrite, "/work/tree/.husky")
	assert.Contains(t, parsed.Sandbox.Filesystem.DenyWrite, "/work/tree/.git")
	assert.NotContains(t, parsed.Sandbox.Filesystem.DenyRead, "/work/tree/.husky", "the closed paths stay readable")
	assert.Contains(t, parsed.Permissions.Deny, "Edit(//work/tree/.husky/**)")
	assert.Contains(t, parsed.Permissions.Deny, "Edit(//work/tree/.git/**)")
	assert.NotContains(t, parsed.Permissions.Deny, "Read(//work/tree/.husky/**)")
}

func TestSandbox_AnEmptyPlanWritesEmptyListsAndNeverNull(t *testing.T) {
	t.Parallel()

	args := claude.New(nil).Sandbox(agent.SandboxPlan{Work: "/w"}, "", "/run").Args
	text := args[len(args)-1]

	assert.Contains(t, text, `"allowedDomains":[]`)
	assert.Contains(t, text, `"denyWrite":[]`)
	assert.NotContains(t, text, "null")
}

func TestSandbox_NeverSetsTheKeysThatWidenTheSandbox(t *testing.T) {
	t.Parallel()

	args := claude.New(nil).Sandbox(plan(), "/bin/beehave check command", "/run").Args
	text := args[len(args)-1]

	for _, key := range []string{"excludedCommands", "allowLocalBinding", "allowAllUnixSockets", "allowAppleEvents"} {
		assert.NotContains(t, text, key)
	}

	// The shared git directory is readable, and no explicit entry makes it writable: the settings carry no allowWrite key.
	_, settings := settingsOf(t, args)
	box, ok := settings["sandbox"].(map[string]any)
	require.True(t, ok)

	files, ok := box["filesystem"].(map[string]any)
	require.True(t, ok)
	assert.NotContains(t, files, "allowWrite")
}

func TestSandbox_CarriesTheHookOfTheGuardInTheSameSettings(t *testing.T) {
	t.Parallel()

	args := claude.New(nil).Sandbox(plan(), "/bin/beehave check command", "/run").Args

	assert.Equal(t, 1, countOf(args, "--settings"), "one settings object holds the hook and the sandbox")

	_, settings := settingsOf(t, args)
	require.Contains(t, settings, "hooks")
	require.Contains(t, settings, "sandbox")

	hook, err := json.Marshal(settings["hooks"])
	require.NoError(t, err)
	assert.JSONEq(t, `{"PreToolUse":[{"matcher":"Bash","hooks":[{"type":"command","command":"/bin/beehave check command","onFailure":"block"}]}]}`, string(hook))
}

func TestSandbox_WithoutAGuardWritesNoHook(t *testing.T) {
	t.Parallel()

	_, settings := settingsOf(t, claude.New(nil).Sandbox(plan(), "", "/run").Args)

	assert.NotContains(t, settings, "hooks")
}

func TestProfile_ClaudeHasTheSandboxCapabilityAndAMinimumVersion(t *testing.T) {
	t.Parallel()

	profile := claude.New(nil).Profile()

	assert.True(t, profile.Sandbox)
	assert.Equal(t, "2.1.285", profile.MinVersion, "the oldest version that knows every key of the sandbox settings")
}

func countOf(list []string, item string) int {
	n := 0

	for _, entry := range list {
		if entry == item {
			n++
		}
	}

	return n
}
