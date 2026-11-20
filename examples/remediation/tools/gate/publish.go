package gate

import (
	"path"
	"slices"
	"strings"

	"mvdan.cc/sh/v3/syntax"

	"github.com/tobocop2/beehave/internal/check/shell"
)

// publishKind names what a rule finds: a step that puts something where a user outside the repository gets it.
type publishKind string

const (
	kindRelease publishKind = "release"
	kindPush    publishKind = "push"
	kindPackage publishKind = "package publish"
	kindDeploy  publishKind = "deploy"

	dryRunFlag = "--dry-run"
	shellText  = "c"
)

// commandRule finds a command by its tool, its subcommand words and, for a command that publishes only with a flag, that flag.
type commandRule struct {
	kind  publishKind
	tool  string
	words []string
	flag  string
}

func (r commandRule) id() string {
	return strings.TrimSpace(strings.Join(append([]string{r.tool}, r.words...), " ") + " " + r.flag)
}

// matches reports whether the command words run the rule. A dry run publishes nothing.
func (r commandRule) matches(head []string) bool {
	if len(head) == 0 || path.Base(head[0]) != r.tool || slices.Contains(head, dryRunFlag) {
		return false
	}

	if r.flag != "" && !slices.Contains(head, r.flag) {
		return false
	}

	named := subcommands(r.tool, head[1:])

	return len(named) >= len(r.words) && slices.Equal(named[:len(r.words)], r.words)
}

// subcommands returns the words of a command that are not options. The value of the git options -C and -c is not one.
func subcommands(tool string, args []string) []string {
	var named []string

	for i := 0; i < len(args); i++ {
		switch {
		case tool == "git" && (args[i] == "-C" || args[i] == "-c"):
			i++
		case !strings.HasPrefix(args[i], "-"):
			named = append(named, args[i])
		}
	}

	return named
}

// commandOf reads a rule from its text: the tool, then the subcommand words, then the flag when the command publishes only with it.
func commandOf(kind publishKind, text string) commandRule {
	words := strings.Fields(text)
	rule := commandRule{kind: kind, tool: words[0]}

	for _, word := range words[1:] {
		if strings.HasPrefix(word, "-") {
			rule.flag = word
		} else {
			rule.words = append(rule.words, word)
		}
	}

	return rule
}

// commandRules are the commands that publish.
func commandRules() []commandRule {
	return slices.Concat(releaseCommands(), pushCommands(), packageCommands(), deployCommands())
}

func releaseCommands() []commandRule {
	return []commandRule{
		commandOf(kindRelease, "gh release create"),
		commandOf(kindRelease, "gh release upload"),
		commandOf(kindRelease, "goreleaser release"),
	}
}

func pushCommands() []commandRule {
	return []commandRule{
		commandOf(kindPush, "git push"),
		commandOf(kindPush, "docker push"),
		commandOf(kindPush, "docker build --push"),
		commandOf(kindPush, "docker buildx build --push"),
		commandOf(kindPush, "helm push"),
		commandOf(kindPush, "crane push"),
		commandOf(kindPush, "oras push"),
	}
}

func packageCommands() []commandRule {
	return []commandRule{
		commandOf(kindPackage, "npm publish"),
		commandOf(kindPackage, "pnpm publish"),
		commandOf(kindPackage, "yarn publish"),
		commandOf(kindPackage, "yarn npm publish"),
		commandOf(kindPackage, "cargo publish"),
		commandOf(kindPackage, "twine upload"),
		commandOf(kindPackage, "uv publish"),
		commandOf(kindPackage, "poetry publish"),
		commandOf(kindPackage, "gem push"),
		commandOf(kindPackage, "mvn deploy"),
		commandOf(kindPackage, "dotnet nuget push"),
	}
}

func deployCommands() []commandRule {
	return []commandRule{
		commandOf(kindDeploy, "kubectl apply"),
		commandOf(kindDeploy, "helm upgrade"),
		commandOf(kindDeploy, "terraform apply"),
		commandOf(kindDeploy, "pulumi up"),
		commandOf(kindDeploy, "flyctl deploy"),
		commandOf(kindDeploy, "fly deploy"),
		commandOf(kindDeploy, "firebase deploy"),
		commandOf(kindDeploy, "vercel deploy"),
		commandOf(kindDeploy, "netlify deploy"),
		commandOf(kindDeploy, "wrangler deploy"),
		commandOf(kindDeploy, "serverless deploy"),
		commandOf(kindDeploy, "gcloud run deploy"),
	}
}

// actionRule finds an action by its name. A rule with a condition finds the action only when its inputs make it publish.
type actionRule struct {
	kind      publishKind
	action    string
	publishes func(with map[string]string) bool
}

func (r actionRule) matches(uses string, with map[string]string) bool {
	name, _, _ := strings.Cut(strings.ToLower(uses), "@")
	if name != r.action && !strings.HasPrefix(name, r.action+"/") {
		return false
	}

	return r.publishes == nil || r.publishes(with)
}

func pushInput(with map[string]string) bool {
	return with["push"] == "true"
}

// releasesForReal is false for a snapshot run and for the commands of goreleaser that publish nothing.
func releasesForReal(with map[string]string) bool {
	args := with["args"]

	return !strings.Contains(args, "--snapshot") && !strings.HasPrefix(args, "check") && !strings.HasPrefix(args, "build")
}

// actionRules are the actions that publish.
func actionRules() []actionRule {
	return slices.Concat(releaseActions(), pushActions(), packageActions(), deployActions())
}

func releaseActions() []actionRule {
	return []actionRule{
		{kind: kindRelease, action: "softprops/action-gh-release"},
		{kind: kindRelease, action: "ncipollo/release-action"},
		{kind: kindRelease, action: "actions/create-release"},
		{kind: kindRelease, action: "actions/upload-release-asset"},
		{kind: kindRelease, action: "goreleaser/goreleaser-action", publishes: releasesForReal},
	}
}

func pushActions() []actionRule {
	return []actionRule{
		{kind: kindPush, action: "docker/build-push-action", publishes: pushInput},
		{kind: kindPush, action: "ad-m/github-push-action"},
		{kind: kindPush, action: "stefanzweifel/git-auto-commit-action"},
		{kind: kindPush, action: "peter-evans/create-pull-request"},
	}
}

func packageActions() []actionRule {
	return []actionRule{
		{kind: kindPackage, action: "pypa/gh-action-pypi-publish"},
		{kind: kindPackage, action: "js-devtools/npm-publish"},
		{kind: kindPackage, action: "rubygems/release-gem"},
	}
}

func deployActions() []actionRule {
	return []actionRule{
		{kind: kindDeploy, action: "actions/deploy-pages"},
		{kind: kindDeploy, action: "peaceiris/actions-gh-pages"},
		{kind: kindDeploy, action: "jamesives/github-pages-deploy-action"},
		{kind: kindDeploy, action: "azure/webapps-deploy"},
		{kind: kindDeploy, action: "google-github-actions/deploy-cloudrun"},
		{kind: kindDeploy, action: "google-github-actions/deploy-appengine"},
		{kind: kindDeploy, action: "aws-actions/amazon-ecs-deploy-task-definition"},
	}
}

// actionPublish reports the rule that an action step meets, or an empty text.
func actionPublish(uses string, with map[string]string) string {
	for _, rule := range actionRules() {
		if rule.matches(uses, with) {
			return string(rule.kind) + ": uses " + rule.action
		}
	}

	return ""
}

// firstPublish reports the first command of the units that publishes, or an empty text.
func firstPublish(units []shell.Unit) string {
	reader := shell.NewReader()

	for _, u := range units {
		var found string

		syntax.Walk(u.File, func(node syntax.Node) bool {
			if call, ok := node.(*syntax.CallExpr); ok && found == "" {
				found = wordsPublish(reader.Words(u, call.Args))
			}

			return found == ""
		})

		if found != "" {
			return found
		}
	}

	return ""
}

// runsText reports whether a shell option, or a cluster of short options such as -lc, makes the shell run the next word as text.
func runsText(option string) bool {
	return strings.HasPrefix(option, "-") && !strings.HasPrefix(option, "--") && strings.HasSuffix(option, shellText)
}

// wordsPublish reports the rule that a command meets. A shell that runs its own text with -c is read the same way.
func wordsPublish(words []string) string {
	head, _ := shell.HeadOf(words)

	for _, rule := range commandRules() {
		if rule.matches(head) {
			return string(rule.kind) + ": " + rule.id()
		}
	}

	for i := 0; i+2 < len(words); i++ {
		if shell.IsShell(path.Base(words[i])) && runsText(words[i+1]) {
			units, _ := shell.ParseUnits(words[i+2])

			if found := firstPublish(units); found != "" {
				return found
			}
		}
	}

	return ""
}
