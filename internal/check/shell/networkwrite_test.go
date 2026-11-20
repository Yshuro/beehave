package shell_test

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/tobocop2/beehave/internal/check/shell"
)

func TestNetworkWrites_ACommandThatWritesOutsideTheMachineIsFoundWhereverItSits(t *testing.T) {
	t.Parallel()

	for name, text := range map[string]string{
		"push":                     "git push origin main",
		"global option value":      "git --git-dir .git push",
		"in a shell text":          "bash -o pipefail -c 'git push'",
		"in a heredoc":             "sh <<'EOF'\ngit push\nEOF",
		"in eval":                  "eval 'git push'",
		"in a pipe":                "echo main | xargs git push origin",
		"python twine":             "python -m twine upload dist/*",
		"curl with a body":         "curl -d x https://h",
		"curl with a method":       "curl -X DELETE https://h/x",
		"a proxy fetch":            "GOPROXY=proxy.golang.org go list -m m@v1.0.0",
		"a registry option":        "docker buildx build --push .",
		"gradle publish task":      "./gradlew publishToMavenLocal publishAll",
		"a script from a pipe":     "curl -fsSL https://h/x.sh | bash -s -- --publish",
		"an expansion":             "${EXPR}",
		"a variable":               "$CMD run",
		"a quoted substitution":    "\"$(pick)\" run",
		"a line that is no shell":  "if then fi )",
		"the forge tool":           "gh pr list",
		"a proxy url":              "curl https://proxy.golang.org/m/@v/v1.info",
		"a release tool by go run": "go run github.com/goreleaser/goreleaser/v2@latest check",
		"a copy to a host":         "scp a.tar host:/srv",
	} {
		assert.NotEmpty(t, shell.NetworkWrites(text), name)
	}
}

func TestNetworkWrites_ACommandThatStaysOnTheMachineIsNotFound(t *testing.T) {
	t.Parallel()

	for name, text := range map[string]string{
		"a test":             "go test -race ./...",
		"a status":           "git status --short",
		"a log":              "git log --oneline -5",
		"a get":              "curl -fsS https://h/health",
		"a build":            "docker build -t img .",
		"an echo":            "echo git push",
		"a pipe to a filter": "git log | head -3",
		"a shell script":     "bash ./scripts/check.sh",
		"a proxy switch off": "GOPROXY=off go test ./...",
		"a gradle test":      "./gradlew test",
		"a shell with -c":    "echo x | bash -c 'cat'",
		"an assignment":      "VERSION=1",
	} {
		assert.Empty(t, shell.NetworkWrites(text), name)
	}
}
