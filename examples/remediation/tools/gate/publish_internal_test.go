package gate

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/tobocop2/beehave/internal/check/shell"
)

// commandCases holds one command that each command rule finds, by the id of the rule.
func commandCases() map[string]string {
	return map[string]string{
		"gh release create":          "gh release create v1 dist/a.tgz",
		"gh release upload":          "gh release upload v1 dist/a.tgz",
		"goreleaser release":         "goreleaser release --clean",
		"git push":                   "git -C out push origin HEAD",
		"docker push":                "docker push ghcr.io/o/r:1",
		"docker build --push":        "docker build --push -t ghcr.io/o/r:1 .",
		"docker buildx build --push": "docker buildx build --platform linux/amd64 --push .",
		"helm push":                  "helm push chart.tgz oci://ghcr.io/o",
		"crane push":                 "crane push image.tar ghcr.io/o/r:1",
		"oras push":                  "oras push ghcr.io/o/r:1 file.txt",
		"npm publish":                "npm publish",
		"pnpm publish":               "pnpm -r publish",
		"yarn publish":               "yarn publish",
		"yarn npm publish":           "yarn npm publish",
		"cargo publish":              "cargo publish",
		"twine upload":               "twine upload dist/*",
		"uv publish":                 "uv publish",
		"poetry publish":             "poetry publish --build",
		"gem push":                   "gem push a-1.gem",
		"mvn deploy":                 "mvn -B deploy",
		"dotnet nuget push":          "dotnet nuget push a.nupkg",
		"kubectl apply":              "kubectl apply -f k8s/",
		"helm upgrade":               "helm upgrade --install r chart",
		"terraform apply":            "terraform apply -auto-approve",
		"pulumi up":                  "pulumi up --yes",
		"flyctl deploy":              "flyctl deploy --remote-only",
		"fly deploy":                 "fly deploy",
		"firebase deploy":            "firebase deploy --only hosting",
		"vercel deploy":              "vercel deploy --prod",
		"netlify deploy":             "netlify deploy --prod",
		"wrangler deploy":            "wrangler deploy",
		"serverless deploy":          "serverless deploy",
		"gcloud run deploy":          "gcloud run deploy api --image x",
	}
}

// actionCases holds the inputs with which each action rule finds an action, by the name of the action.
func actionCases() map[string]map[string]string {
	return map[string]map[string]string{
		"softprops/action-gh-release":                   nil,
		"ncipollo/release-action":                       nil,
		"actions/create-release":                        nil,
		"actions/upload-release-asset":                  nil,
		"goreleaser/goreleaser-action":                  {"args": "release --clean"},
		"docker/build-push-action":                      {"push": "true"},
		"ad-m/github-push-action":                       nil,
		"stefanzweifel/git-auto-commit-action":          nil,
		"peter-evans/create-pull-request":               nil,
		"pypa/gh-action-pypi-publish":                   nil,
		"js-devtools/npm-publish":                       nil,
		"rubygems/release-gem":                          nil,
		"actions/deploy-pages":                          nil,
		"peaceiris/actions-gh-pages":                    nil,
		"jamesives/github-pages-deploy-action":          nil,
		"azure/webapps-deploy":                          nil,
		"google-github-actions/deploy-cloudrun":         nil,
		"google-github-actions/deploy-appengine":        nil,
		"aws-actions/amazon-ecs-deploy-task-definition": nil,
	}
}

func TestPublishRules_EveryCommandRuleFindsItsCommandAndTheCaseTableHoldsNoOtherRule(t *testing.T) {
	t.Parallel()

	cases := commandCases()
	rules := commandRules()

	require.Len(t, rules, len(cases), "one case for each rule, and no case for a rule that does not exist")

	for _, rule := range rules {
		text, ok := cases[rule.id()]
		require.True(t, ok, "no case for the rule %s", rule.id())

		units, bad := shell.ParseUnits(text)
		require.Empty(t, bad)

		assert.Equal(t, string(rule.kind)+": "+rule.id(), firstPublish(units), rule.id())
	}
}

func TestPublishRules_EveryActionRuleFindsItsActionAndNoOtherRule(t *testing.T) {
	t.Parallel()

	cases := actionCases()
	rules := actionRules()

	require.Len(t, rules, len(cases))

	for _, rule := range rules {
		with, ok := cases[rule.action]
		require.True(t, ok, "no case for the rule %s", rule.action)

		want := string(rule.kind) + ": uses " + rule.action

		assert.Equal(t, want, actionPublish(rule.action+"@v1", with), rule.action)
		assert.Equal(t, want, actionPublish(strings.ToUpper(rule.action)+"/sub@v1", with), "a name in capitals and a path inside the action")
	}
}

func TestPublishRules_AnActionThatPublishesOnlyWithItsInputsIsFoundOnlyWithThem(t *testing.T) {
	t.Parallel()

	for name, test := range map[string]struct {
		action string
		with   map[string]string
	}{
		"a_build_with_no_push_input": {"docker/build-push-action@v6", nil},
		"a_build_that_does_not_push": {"docker/build-push-action@v6", map[string]string{"push": "false"}},
		"a_snapshot_release":         {"goreleaser/goreleaser-action@v6", map[string]string{"args": "release --snapshot"}},
		"a_goreleaser_check":         {"goreleaser/goreleaser-action@v6", map[string]string{"args": "check"}},
		"a_goreleaser_build":         {"goreleaser/goreleaser-action@v6", map[string]string{"args": "build --single-target"}},
		"an_action_of_another_owner": {"someone/actions-gh-pages-fork@v1", nil},
		"a_local_action":             {"./.github/actions/deploy-pages", nil},
	} {
		assert.Empty(t, actionPublish(test.action, test.with), name)
	}
}

func TestPublishRules_ACommandThatDoesNotPublishIsNotFound(t *testing.T) {
	t.Parallel()

	for name, text := range map[string]string{
		"a_word_in_a_string":         `echo "git push"`,
		"a_word_of_another_command":  "git commit -m push",
		"a_dry_run":                  "cargo publish --dry-run",
		"a_build_with_no_push_flag":  "docker buildx build .",
		"a_subcommand_in_the_middle": "npm run publish",
		"a_value_of_a_git_option":    "git -c push.default=simple status",
		"a_subcommand_inside_a_word": "git pushall origin",
		"a_tool_name_inside_a_word":  "mygit push origin",
		"a_tool_with_no_subcommand":  "terraform",
	} {
		units, bad := shell.ParseUnits(text)
		require.Empty(t, bad, name)

		assert.Empty(t, firstPublish(units), name)
	}
}

func TestPublishRules_AShellThatRunsATextWithDashCIsReadLikeAScript(t *testing.T) {
	t.Parallel()

	for name, test := range map[string]struct{ text, want string }{
		"a_publish_inside_a_text": {`bash -c "make build && npm publish"`, "package publish: npm publish"},
		"a_login_shell":           {`sh -lc 'git push origin main'`, "push: git push"},
		"a_text_with_no_publish":  {`bash -c "make test"`, ""},
		"a_shell_with_a_script":   {`bash scripts/test.sh`, ""},
	} {
		units, bad := shell.ParseUnits(test.text)
		require.Empty(t, bad, name)

		assert.Equal(t, test.want, firstPublish(units), name)
	}
}
