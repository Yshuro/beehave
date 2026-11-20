package gate_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/tobocop2/beehave/examples/remediation/tools/gate"
)

// keptJob is a job that reads no secret, so that a file whose other jobs are left out still gives a step.
const keptJob = "  k:\n    steps:\n      - run: make test\n"

func commands(got gate.Derived) []string {
	out := make([]string, len(got.Steps))
	for i, step := range got.Steps {
		out[i] = step.Command
	}

	return out
}

func TestDerive_ASecretThroughAYAMLAliasOrMergeKeyIsRead(t *testing.T) {
	t.Parallel()

	anchorJob := "  a:\n    env:\n      T: &tok ${{ secrets.X }}\n    steps:\n      - run: go vet ./...\n"
	anchorEnvs := "  a:\n    env: &envs\n      T: ${{ secrets.X }}\n    steps:\n      - run: go vet ./...\n"

	for name, jobs := range map[string]string{
		"an alias of a value in another job": anchorJob + "  b:\n    env:\n      T: *tok\n    steps:\n      - run: curl -H \"Auth=$T\" https://api.example.com/deploy\n",
		"an alias of a value in a step":      anchorJob + "  b:\n    steps:\n      - env:\n          T: *tok\n        run: curl -d \"$T\" https://api.example.com/hook\n",
		"a merge key of a mapping":           anchorEnvs + "  b:\n    env:\n      <<: *envs\n    steps:\n      - run: curl -d \"$T\" https://api.example.com/hook\n",
		"an alias of the whole job":          "  a: &job\n    steps:\n      - env:\n          T: ${{ secrets.X }}\n        run: go vet ./...\n  b: *job\n",
	} {
		got := derivedAll(t, readOnlyFile+"jobs:\n"+jobs+keptJob)

		assert.Equal(t, []string{"job ci.yaml:a", "job ci.yaml:b"}, refusedNames(got), name)
		assert.Equal(t, []string{"k:run-1"}, stepNames(got), name)
	}
}

func TestDerive_APermissionsAliasIsReadThroughToItsValues(t *testing.T) {
	t.Parallel()

	got := derivedAll(t, "permissions: write-all\njobs:\n  a:\n    permissions: &ro\n      contents: read\n    steps:\n      - run: go vet ./...\n  b:\n    permissions: *ro\n    steps:\n      - run: make test\n")

	assert.Equal(t, []string{"a:run-1", "b:run-1"}, stepNames(got))
	assert.Empty(t, got.Refused)
}

func TestDerive_AnAliasThatHoldsItselfIsRefused(t *testing.T) {
	t.Parallel()

	assert.Contains(t, refusedDerive(t, readOnlyFile+"jobs:\n  a: &x\n    steps:\n      - run: make test\n        env: *x\n"), "ci.yaml: not a workflow file: a YAML alias refers to a node that holds it")
}

func TestDerive_AnExpressionThatReadsTheWholeGithubContextCountsAsTheToken(t *testing.T) {
	t.Parallel()

	jobWith := func(value string) string {
		return readOnlyFile + "jobs:\n  j:\n    steps:\n      - env:\n          ALL: " + value + "\n        run: go vet ./...\n" + keptJob
	}

	for name, value := range map[string]string{
		"join of the object filter":   "${{ join(github.*, ',') }}",
		"json of the object filter":   "${{ toJSON(github.*) }}",
		"a filter then a field":       "${{ github.*.token }}",
		"the context in a comparison": "${{ github == null }}",
		"over two lines":              "\"${{ format('{0}',\n            github) }}\"",
	} {
		got := derivedAll(t, jobWith(value))

		assert.Equal(t, []string{"job ci.yaml:j"}, refusedNames(got), name)
		assert.Equal(t, []string{"k:run-1"}, stepNames(got), name)
	}

	for name, value := range map[string]string{
		"another field of the context":   "${{ github.event.number }}",
		"an input named github":          "${{ inputs.github }}",
		"the word outside an expression": "github, then a comma",
	} {
		got := derivedAll(t, jobWith(value))

		assert.Empty(t, got.Refused, name)
		assert.Equal(t, []string{"j:run-1", "k:run-1"}, stepNames(got), name)
	}
}

func TestDerive_ASecretInOneStepLeavesOutTheWholeJob(t *testing.T) {
	t.Parallel()

	for name, steps := range map[string]string{
		"carried through GITHUB_ENV": "      - run: echo \"T=${{ secrets.X }}\" >> \"$GITHUB_ENV\"\n      - run: curl -H \"Auth=$T\" https://api.example.com/deploy\n",
		"a checkout given a PAT":     "      - uses: actions/checkout@v7\n        with:\n          token: ${{ secrets.PAT }}\n      - run: go run ./cmd/release\n",
	} {
		got := derivedAll(t, readOnlyFile+"jobs:\n  j:\n    steps:\n"+steps+keptJob)

		assert.Equal(t, []string{"job ci.yaml:j"}, refusedNames(got), name)
		assert.Equal(t, "reads a secret", got.Refused[0].Reason, name)
		assert.Equal(t, []string{"k:run-1"}, stepNames(got), name)
	}
}

func TestDerive_TheCommandOfAStepHoldsTheContextThatCIRunsItIn(t *testing.T) {
	t.Parallel()

	got := derivedAll(t, readOnlyFile+"env:\n  MODE: x\ndefaults:\n  run:\n    working-directory: ../..\njobs:\n"+
		"  j:\n    container: golang:1.22\n    env:\n      B: y\n    steps:\n"+
		"      - run: make test\n        shell: bash\n        working-directory: /\n        continue-on-error: true\n        env:\n          A: z\n          MODE: w\n"+
		"      - run: make vet\n"+
		"  k:\n    container:\n      image: golang\n      ports: [80]\n    env: ${{ fromJSON(vars.E) }}\n    steps:\n      - run: make lint\n")

	assert.Equal(t, []string{
		"make test [shell bash] [working-directory /] [env A B MODE] [container golang:1.22] [continue-on-error true]",
		"make vet [working-directory ../..] [env B MODE] [container golang:1.22]",
		"make lint [working-directory ../..] [env ${{ fromJSON(vars.E) }} MODE] [container {image: golang, ports: [80]}]",
	}, commands(got))

	merged := derivedAll(t, readOnlyFile+"jobs:\n  j:\n    env: &base\n      C: x\n    steps:\n      - run: make vet\n"+
		"  k:\n    env:\n      <<: [*base, {D: y}]\n      E: z\n    steps:\n      - run: make test\n        env:\n          <<: *base\n")
	assert.Equal(t, []string{"make vet [env C]", "make test [env C D E]"}, commands(merged), "a merge key gives the names it merges")

	plain := derivedAll(t, readOnlyFile+"jobs:\n  j:\n    env:\n    steps:\n      - run: make test\n")
	assert.Equal(t, []string{"make test"}, commands(plain), "a step with no context is its run text")
}

func TestDerive_TheRunTextKeepsTheSpacesInsideQuotes(t *testing.T) {
	t.Parallel()

	for run, want := range map[string]string{
		`grep -c "a  b" f`:         `grep -c "a  b" f`,
		`printf '%s   %s' x  y`:    `printf '%s   %s' x y`,
		`echo "it\"s  here"   now`: `echo "it\"s  here" now`,
		`echo a\  b   c`:           `echo a\  b c`,
		"echo 'x  y'\t\tz":         "echo 'x  y' z",
	} {
		got := derivedAll(t, readOnlyFile+"jobs:\n  j:\n    steps:\n      - run: |\n          "+run+"\n")

		assert.Equal(t, []string{want}, commands(got), run)
	}
}

func TestDerive_TheCommandsOfTheHeldListAreExactCIText(t *testing.T) {
	t.Parallel()

	got, err := gate.Derive([]gate.File{{Path: "ci.yaml", Text: readOnlyFile + "jobs:\n  j:\n    steps:\n      - run: grep -c \"a  b\" f\n"}})
	require.NoError(t, err)

	assert.Empty(t, gate.Hold(got.Steps, []string{"test"}, gate.Map{`grep -c "a  b" f`: {"test"}}), "a key copied from the CI file matches")
}
