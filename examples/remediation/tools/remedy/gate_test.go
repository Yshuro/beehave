package remedy_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/tobocop2/beehave/examples/remediation/tools/remedy"
)

const (
	gateListText = "# Gate list: owner/name\n\n## Gate steps\n\n" +
		"| name | command |\n|---|---|\n| fmt | `cargo fmt --all -- --check` |\n| lint | `cargo clippy` |\n"

	goodJob = "#!/bin/bash\n" +
		"cargo fmt --all -- --check; echo \"STEP_RESULT name=fmt rc=$?\"\n" +
		"cargo clippy; echo \"STEP_RESULT name=lint rc=$?\"\n" +
		"echo \"DONE-x rc=$reds gate=full\"\n"

	laneHead = "abcdef1234567890abcdef1234567890abcdef12"
	laneRed  = "test empty_name_refused ... FAILED"

	goodLaneReport = "# Report\n\n## Claims\n| Claim | Where | Evidence | Verified at |\n|---|---|---|---|\n" +
		"| refuses an empty name | body | test empty_name_refused, red at base | " + laneHead + " |\n\n" +
		"## Hunks\n| Hunk | Arm | Red test line |\n|---|---|---|\n" +
		"| src/a.rs:10-14 | handler never calls validate() | " + laneRed + " |\n\n" +
		"## Inputs\n| Input class | Base | Head | Test |\n|---|---|---|---|\n" +
		"| empty string | accepted | refused 400 | empty_name_refused |\n\n" +
		"## Siblings\n| Site | Same behavior | Updated |\n|---|---|---|\n" +
		"| src/b.rs:40 create route | validates the same name | yes |\n\n" +
		"no background commands left\n"
)

// jobFiles writes the gate list and the job script in a new folder and returns the two paths.
func jobFiles(t *testing.T, script string) (string, string) {
	t.Helper()

	dir := t.TempDir()

	list := filepath.Join(dir, "gate.md")
	job := filepath.Join(dir, "job.sh")

	require.NoError(t, os.WriteFile(list, []byte(gateListText), 0o600))
	require.NoError(t, os.WriteFile(job, []byte(script), 0o600))

	return job, list
}

const (
	ciWorkflow = `name: CI
permissions:
  contents: read
jobs:
  check:
    steps:
      - uses: actions/checkout@v7
      - name: Format and vet
        run: make format-check vet
      - run: |
          go install gotest.tools/gotestsum@v1.13.0
          make test
  docs:
    steps:
      - name: Build the docs
        run: mkdocs build --strict
`

	jobsHead = "permissions: read-all\njobs:\n  ship:\n    steps:\n      - name: Test\n        run: make test\n      - "
)

func TestGatelist_TheStepsAreDerivedFromTheCIWorkflowFile(t *testing.T) {
	t.Parallel()

	code, out, errOut := run(t, remedy.Deps{}, "gatelist", textFile(t, "ci.yaml", ciWorkflow))

	assert.Equal(t, remedy.ExitPass, code)
	assert.Equal(t, "step check:format-and-vet: make format-check vet\n"+
		"step check:run-3: go install gotest.tools/gotestsum@v1.13.0; make test\n"+
		"step docs:build-the-docs: mkdocs build --strict\n"+
		"check-gatelist: OK (steps 3, refused 0)\n", out)
	assert.Empty(t, errOut)
}

func TestGatelist_TheStepsOfSeveralWorkflowFilesAreOneListWithNoNameTwice(t *testing.T) {
	t.Parallel()

	code, out, _ := run(t, remedy.Deps{}, "gatelist", textFile(t, "a.yaml", jobsHead+"run: make a\n"), textFile(t, "b.yaml", jobsHead+"run: make b\n"))

	assert.Equal(t, remedy.ExitPass, code)
	assert.Contains(t, out, "step ship:test: make test\n")
	assert.Contains(t, out, "step ship:run-2: make a\n")
	assert.Contains(t, out, "step ship:test-2: make test\n")
	assert.Contains(t, out, "step ship:run-2-2: make b\n")
	assert.Contains(t, out, "check-gatelist: OK (steps 4, refused 0)\n")
}

func TestGatelist_AStepThatTheLabelRefusesIsListedWithItsReason(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name, step, reason string
	}{
		{"a_release_by_action", "uses: softprops/action-gh-release@v2", "uses softprops/action-gh-release@v2, an action that is neither a checkout nor a setup"},
		{"a_push_by_command", "run: cd out && git push origin main", "runs `git push`"},
		{"a_push_by_action", "uses: docker/build-push-action@v6\n        with:\n          push: true", "uses docker/build-push-action@v6"},
		{"a_package_by_action", "uses: pypa/gh-action-pypi-publish@release/v1", "uses pypa/gh-action-pypi-publish@release/v1"},
		{"a_deploy_by_action", "uses: actions/deploy-pages@v4", "uses actions/deploy-pages@v4"},
		{"a_shell_that_is_not_sh", "shell: pwsh\n        run: echo hi", "runs in the shell pwsh"},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			code, out, errOut := run(t, remedy.Deps{}, "gatelist", textFile(t, "ci.yaml", jobsHead+test.step+"\n"))

			assert.Equal(t, remedy.ExitPass, code)
			assert.Empty(t, errOut)
			assert.Contains(t, out, "step ship:test: make test\n")
			assert.Contains(t, out, "refused step ship:run-2: ")
			assert.Contains(t, out, test.reason)
			assert.Contains(t, out, "check-gatelist: OK (steps 1, refused 1)\n")
		})
	}
}

func TestGatelist_AStepThatOnlyMentionsAPublishingCommandIsNotRefused(t *testing.T) {
	t.Parallel()

	for _, step := range []string{
		`run: echo "git push is not run here"`,
		"run: grep -r 'npm publish' docs",
		"uses: actions/setup-go@v6\n        with:\n          go-version: '1.27'",
	} {
		code, out, errOut := run(t, remedy.Deps{}, "gatelist", textFile(t, "ci.yaml", jobsHead+step+"\n"))

		assert.Equal(t, remedy.ExitPass, code, step)
		assert.Contains(t, out, "refused 0)", step)
		assert.Empty(t, errOut, step)
	}
}

func TestGatelist_TheMapMustNameEveryDerivedCommandExactly(t *testing.T) {
	t.Parallel()

	file := textFile(t, "ci.yaml", jobsHead+"run: make lint\n")
	steps := []string{"gatelist", "--steps", "test", "--steps", "lint", "--map"}

	code, out, errOut := run(t, remedy.Deps{}, append(steps, `{"make test": ["test"], "make lint": ["lint"]}`, file)...)

	assert.Equal(t, remedy.ExitPass, code)
	assert.Contains(t, out, "check-gatelist: OK (steps 2, refused 0)")
	assert.Empty(t, errOut)

	for name, held := range map[string]string{
		"no key":           `{"make test": ["test"]}`,
		"a substring key":  `{"make test": ["test"], "make lin": ["lint"]}`,
		"a trailing space": `{"make test": ["test"], "make lint ": ["lint"]}`,
	} {
		code, out, errOut = run(t, remedy.Deps{}, append(steps, held, file)...)

		assert.Equal(t, remedy.ExitRefused, code, name)
		assert.Contains(t, out, "step ship:run-2: make lint\n", name)
		assert.Contains(t, out, "unmapped ship:run-2: make lint\n", name)
		assert.Contains(t, errOut, "the gate_map of the project does not match the CI commands", name)
		assert.NotContains(t, errOut, "Add them to gate_steps", name)
	}
}

func TestGatelist_AMapThatNamesAnUnknownGateStepOrAStaleCommandIsExitCodeTwo(t *testing.T) {
	t.Parallel()

	file := textFile(t, "ci.yaml", jobsHead+"run: make lint\n")

	code, out, _ := run(t, remedy.Deps{}, "gatelist", "--steps", "test", "--map", `{"make test": ["test"], "make lint": ["lint"]}`, file)

	assert.Equal(t, remedy.ExitRefused, code)
	assert.Contains(t, out, "unknown-step lint\n")
	assert.NotContains(t, out, "unmapped ")

	code, out, _ = run(t, remedy.Deps{}, "gatelist", "--steps", "test", "--map", `{"make test": ["test"], "make lint": [], "make old": []}`, file)

	assert.Equal(t, remedy.ExitRefused, code)
	assert.Contains(t, out, "stale make old\n")
	assert.NotContains(t, out, "unknown-step ")
}

func TestGatelist_AMapWithNoGateStepsIsExitCodeTwo(t *testing.T) {
	t.Parallel()

	code, out, errOut := run(t, remedy.Deps{}, "gatelist", "--map", `{"make test": ["test"]}`, textFile(t, "ci.yaml", jobsHead+"run: make lint\n"))

	assert.Equal(t, remedy.ExitRefused, code)
	assert.Empty(t, out)
	assert.Contains(t, errOut, "the project has no gate_steps")
}

func TestGatelist_AStepsFlagWithNoMapHoldsEachCommandToAnEmptyMap(t *testing.T) {
	t.Parallel()

	code, out, _ := run(t, remedy.Deps{}, "gatelist", "--steps", "test", textFile(t, "ci.yaml", jobsHead+"run: make lint\n"))

	assert.Equal(t, remedy.ExitRefused, code)
	assert.Contains(t, out, "unmapped ship:test: make test\n")
	assert.Contains(t, out, "unmapped ship:run-2: make lint\n")
}

func TestGatelist_AMapThatIsNotAnObjectOfListsOfTextIsExitCodeTwo(t *testing.T) {
	t.Parallel()

	for _, held := range []string{`["make test"]`, `{"make test": "test"}`, `{"make test": [1]}`, `{`} {
		code, out, errOut := run(t, remedy.Deps{}, "gatelist", "--steps", "test", "--map", held, textFile(t, "ci.yaml", ciWorkflow))

		assert.Equal(t, remedy.ExitRefused, code, held)
		assert.Empty(t, out, held)
		assert.Contains(t, errOut, "for flag -map: ", held)
	}
}

func TestGatelist_AMapThatNamesACommandTwiceHoldsANullListOrIsGivenTwiceIsExitCodeTwo(t *testing.T) {
	t.Parallel()

	file := textFile(t, "ci.yaml", jobsHead+"run: make lint\n")
	good := `{"make test": ["test"], "make lint": []}`

	for name, args := range map[string][]string{
		"a key twice": {"--map", `{"make test": ["nope"], "make test": ["test"], "make lint": []}`},
		"a null list": {"--map", `{"make test": null, "make lint": []}`},
		"two maps":    {"--map", `{"make test": ["test"]}`, "--map", `{"make lint": []}`},
	} {
		code, out, errOut := run(t, remedy.Deps{}, append(append([]string{"gatelist", "--steps", "test"}, args...), file)...)

		assert.Equal(t, remedy.ExitRefused, code, name)
		assert.Empty(t, out, name)
		assert.Contains(t, errOut, "for flag -map: ", name)
	}

	code, _, errOut := run(t, remedy.Deps{}, "gatelist", "--steps", "test", "--map", good, file)
	assert.Equal(t, remedy.ExitPass, code, "the control: the same map once and whole")
	assert.Empty(t, errOut)
}

func TestGatelist_AFlagThatDoesNotExistIsExitCodeTwo(t *testing.T) {
	t.Parallel()

	code, out, errOut := run(t, remedy.Deps{}, "gatelist", "--nope", textFile(t, "ci.yaml", ciWorkflow))

	assert.Equal(t, remedy.ExitRefused, code)
	assert.Empty(t, out)
	assert.Contains(t, errOut, "flag provided but not defined: -nope")
}

func TestGatelist_AFileThatGivesNoUsableStepIsExitCodeTwo(t *testing.T) {
	t.Parallel()

	tests := map[string]string{
		"not_yaml":                "jobs: [unclosed\n",
		"no_run_step":             "permissions: read-all\njobs:\n  a:\n    steps:\n      - uses: actions/checkout@v7\n",
		"a_run_that_is_not_shell": "permissions: read-all\njobs:\n  a:\n    steps:\n      - run: if then fi )\n",
		"only_a_push":             "permissions: read-all\njobs:\n  a:\n    steps:\n      - run: git push origin main\n",
		"only_a_job_that_writes":  "jobs:\n  a:\n    steps:\n      - run: make test\n",
		"only_a_job_with_a_step_that_reads_a_secret": "permissions: read-all\njobs:\n  a:\n    steps:\n      - run: make test\n      - env:\n          T: ${{ secrets.T }}\n        run: make docs\n",
		"no_jobs": "name: CI\n",
	}

	for name, text := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			code, out, errOut := run(t, remedy.Deps{}, "gatelist", textFile(t, "ci.yaml", text))

			assert.Equal(t, remedy.ExitRefused, code)
			assert.Empty(t, out)
			assert.Contains(t, errOut, "remedy gatelist: FAIL: ")
		})
	}
}

func TestGatelist_AMissingFileAndABadCallAreExitCodeTwo(t *testing.T) {
	t.Parallel()

	code, out, errOut := run(t, remedy.Deps{}, "gatelist", filepath.Join(t.TempDir(), "none.yaml"))

	assert.Equal(t, remedy.ExitRefused, code)
	assert.Empty(t, out)
	assert.NotEmpty(t, errOut)
}

func TestGatelist_ACallWithNoWorkflowFileShowsTheUsageAndIsExitCodeTwo(t *testing.T) {
	t.Parallel()

	for _, args := range [][]string{{"gatelist"}, {"gatelist", "--steps", "x"}} {
		code, out, errOut := run(t, remedy.Deps{}, args...)

		assert.Equal(t, remedy.ExitRefused, code, args)
		assert.Empty(t, out, args)
		assert.Equal(t, "usage: remedy gatelist [--steps <name>]... [--map <json>] <workflow-file>...\n", errOut, args)
	}
}

func TestJob_AJobThatRunsEveryStepPassesWithTheSummary(t *testing.T) {
	t.Parallel()

	script, list := jobFiles(t, goodJob)

	code, out, errOut := run(t, remedy.Deps{}, "job", script, "--list", list)

	assert.Equal(t, remedy.ExitPass, code)
	assert.Equal(t, "check-job: OK (steps 2, run 2, declared skips 0, unstated 0)\n", out)
	assert.Empty(t, errOut)
}

func TestJob_AStepThatTheJobDoesNotRunIsExitCodeOne(t *testing.T) {
	t.Parallel()

	script, list := jobFiles(t, strings.Replace(goodJob, "cargo clippy; echo \"STEP_RESULT name=lint rc=$?\"\n", "", 1))

	code, out, _ := run(t, remedy.Deps{}, "job", script, "--list", list)

	assert.Equal(t, remedy.ExitDefect, code)
	assert.Contains(t, out, "FAIL: the job does not run the step lint (cargo clippy)")
	assert.NotContains(t, out, "check-job: OK")
}

func TestJob_ARuleOfTheJobScriptIsExitCodeOneEvenWhenEveryStepRuns(t *testing.T) {
	t.Parallel()

	script, list := jobFiles(t, goodJob+"git add -A\n")

	code, out, _ := run(t, remedy.Deps{}, "job", script, "--list", list)

	assert.Equal(t, remedy.ExitDefect, code)
	assert.Contains(t, out, "FAIL: the job stages in bulk (")
	assert.NotContains(t, out, "check-job: OK")
}

func TestJob_ATestCommandWithNoTimeoutIsExitCodeOne(t *testing.T) {
	t.Parallel()

	script, list := jobFiles(t, goodJob+"pytest tests\n")

	code, out, _ := run(t, remedy.Deps{}, "job", script, "--list", list)

	assert.Equal(t, remedy.ExitDefect, code)
	assert.Contains(t, out, "FAIL: the job runs a test command without timeout in front of it: ")

	script, list = jobFiles(t, goodJob+"timeout 60 pytest tests\n")

	code, _, _ = run(t, remedy.Deps{}, "job", script, "--list", list)

	assert.Equal(t, remedy.ExitPass, code)
}

func TestJob_AFileThatTheJobSourcesIsReadFromTheJobFolder(t *testing.T) {
	t.Parallel()

	sourced := strings.Replace(goodJob, "echo \"DONE-x", "source lib.sh\necho \"DONE-x", 1)
	script, list := jobFiles(t, sourced)

	// With no file to read, the sourced file can set any variable, so the exit status is not fixed.
	code, _, _ := run(t, remedy.Deps{}, "job", script, "--list", list)
	assert.Equal(t, remedy.ExitPass, code)

	// The file sets the one variable of the marker to a literal.
	require.NoError(t, os.WriteFile(filepath.Join(filepath.Dir(script), "lib.sh"), []byte("reds=0\n"), 0o600))

	code, out, _ := run(t, remedy.Deps{}, "job", script, "--list", list)
	assert.Equal(t, remedy.ExitDefect, code)
	assert.Contains(t, out, "the job prints a fixed exit status")

	// A --job-dir that holds no such file reads nothing.
	code, _, _ = run(t, remedy.Deps{}, "job", script, "--list", list, "--job-dir", t.TempDir())
	assert.Equal(t, remedy.ExitPass, code)
}

func TestJob_AGateListThatCannotBeReadIsExitCodeTwoAndKeepsTheFailuresFound(t *testing.T) {
	t.Parallel()

	script, _ := jobFiles(t, goodJob+"git add -A\n")

	code, out, errOut := run(t, remedy.Deps{}, "job", script, "--list", textFile(t, "bad.md", "no table here\n"))

	assert.Equal(t, remedy.ExitRefused, code)
	assert.Contains(t, out, "FAIL: the job stages in bulk (")
	assert.Contains(t, errOut, "remedy job: FAIL: ")
}

func TestJob_AMissingListAScriptThatCannotBeReadAndABadCallAreExitCodeTwo(t *testing.T) {
	t.Parallel()

	script, list := jobFiles(t, goodJob)
	none := filepath.Join(t.TempDir(), "none")

	for _, args := range [][]string{
		{"job", script},
		{"job"},
		{"job", script, "--list", list, "extra"},
		{"job", script, "--nope"},
		{"job", none, "--list", list},
		{"job", script, "--list", none},
	} {
		code, out, errOut := run(t, remedy.Deps{}, args...)

		assert.Equal(t, remedy.ExitRefused, code, args)
		assert.Empty(t, out, args)
		assert.NotEmpty(t, errOut, args)
	}
}

func TestReport_AWholeReportPassesWithTheSummary(t *testing.T) {
	t.Parallel()

	code, out, errOut := run(t, remedy.Deps{}, "report", textFile(t, "report.md", goodLaneReport), "--head", laneHead)

	assert.Equal(t, remedy.ExitPass, code)
	assert.Equal(t, "check-report: OK at abcdef123456 (claims 1, hunks 1, inputs 1, siblings 1)\n", out)
	assert.Empty(t, errOut)
}

func TestReport_AReportThatNamesAnotherHeadIsExitCodeOne(t *testing.T) {
	t.Parallel()

	code, out, _ := run(t, remedy.Deps{}, "report", textFile(t, "report.md", goodLaneReport), "--head", "1234567890abcdef1234567890abcdef12345678")

	assert.Equal(t, remedy.ExitDefect, code)
	assert.NotContains(t, out, "check-report: OK")
	assert.NotEmpty(t, out)
}

func TestReport_AReportWithNoBackgroundLineIsExitCodeOne(t *testing.T) {
	t.Parallel()

	text := strings.Replace(goodLaneReport, "no background commands left\n", "", 1)

	code, out, _ := run(t, remedy.Deps{}, "report", textFile(t, "report.md", text), "--head", laneHead)

	assert.Equal(t, remedy.ExitDefect, code)
	assert.Contains(t, out, "no background commands left")
}

func TestReport_AMissingHeadAFileThatCannotBeReadAndABadCallAreExitCodeTwo(t *testing.T) {
	t.Parallel()

	path := textFile(t, "report.md", goodLaneReport)

	for _, args := range [][]string{
		{"report", path},
		{"report"},
		{"report", path, "--head", laneHead, "extra"},
		{"report", path, "--nope"},
		{"report", filepath.Join(t.TempDir(), "none.md"), "--head", laneHead},
	} {
		code, out, errOut := run(t, remedy.Deps{}, args...)

		assert.Equal(t, remedy.ExitRefused, code, args)
		assert.Empty(t, out, args)
		assert.NotEmpty(t, errOut, args)
	}
}

func TestPrintable_ALineWithABadByteIsPrintedAsText(t *testing.T) {
	t.Parallel()

	script, list := jobFiles(t, goodJob+"echo \"DONE-x rc=0\"\x80\n")

	code, out, _ := run(t, remedy.Deps{}, "job", script, "--list", list)

	assert.Equal(t, remedy.ExitDefect, code)
	assert.Equal(t, strings.ToValidUTF8(out, ""), out, "output is valid UTF-8")
}
