package shell

import (
	"path"
	"slices"
	"strings"

	"mvdan.cc/sh/v3/syntax"

	"github.com/tobocop2/beehave/internal/model"
)

// findingNetwork is the finding of NetworkWrites. It is no kind of Scan: no command of a workflow lists it in forbid_commands.
const findingNetwork model.FindingKind = "network-write"

// The words that name a subcommand of more than one tool.
const (
	verbPush    = "push"
	verbPublish = "publish"
	verbUpload  = "upload"
	verbApply   = "apply"
	verbDestroy = "destroy"
	verbRelease = "release"
	verbLogin   = "login"

	goProxy = "proxy.golang.org"
)

// NetworkWrites returns the commands of a shell text that write to a place outside the machine (a push, a release, a package publish, a deploy, a copy to a host,
// a request with a body), and the commands that no static read can judge (a command word that is an expansion, a line that is not shell, a script read from a pipe).
// Each text names one command. The text is read as Scan reads a command, so a shell with -c, a heredoc, eval and ssh are read too.
// The list of tools is a floor: a project keeps its publishing steps out of the CI files that name its gate.
func NetworkWrites(text string) []string {
	s := newScanner()
	s.network = true
	s.prepass(text)
	s.scan(text, false)

	out := expandedHeads(text)

	for _, finding := range dedupe(s.findings) {
		if slices.Contains([]model.FindingKind{findingNetwork, model.FindingOpaque, model.FindingUnparsed}, finding.Kind) {
			out = append(out, finding.Text)
		}
	}

	return out
}

// expandedHeads returns the command words of the text that a variable, a substitution or an arithmetic expansion makes. No static read can tell what such a command runs.
func expandedHeads(text string) []string {
	units, _ := ParseUnits(text) // NetworkWrites reports the lines that do not parse

	var out []string

	for _, u := range units {
		syntax.Walk(u.File, func(node syntax.Node) bool {
			if call, ok := node.(*syntax.CallExpr); ok && len(call.Args) > 0 && expands(call.Args[0]) {
				out = append(out, SrcOf(text, call.Args[0]))
			}

			return true
		})
	}

	return out
}

// expands reports whether a word holds an expansion, in quotes or not.
func expands(word *syntax.Word) bool {
	found := false

	syntax.Walk(word, func(node syntax.Node) bool {
		switch node.(type) {
		case *syntax.ParamExp, *syntax.CmdSubst, *syntax.ArithmExp, *syntax.ProcSubst:
			found = true
		}

		return !found
	})

	return found
}

// scanNetwork adds a finding for a command that writes outside the machine.
func (s *scanner) scanNetwork(cmd *command) {
	if slices.ContainsFunc(cmd.prefix, func(word string) bool { return strings.HasPrefix(word, "GOPROXY=") && strings.Contains(word, goProxy) }) {
		s.add(findingNetwork, cmd.prefix)
	}

	switch {
	case len(cmd.head) == 0: // a command of assignments only runs nothing
	case networkHit(cmd.head):
		s.add(findingNetwork, cmd.head)
	case cmd.prev != nil && isShell(path.Base(cmd.head[0])) && !slices.ContainsFunc(cmd.head[1:], shellCommandOption.MatchString):
		s.add(model.FindingOpaque, []string{"a script that a pipe feeds to", cmd.head[0]})
	}
}

// networkHit reports whether a command writes outside the machine.
func networkHit(head []string) bool {
	tool, args := path.Base(head[0]), head[1:]

	return namesReach(args) || slices.Contains(alwaysNetwork(), tool) || verbHit(tool, nonOptions(args)) || registryPush(tool, args) || requestWrites(tool, args)
}

// namesReach reports whether a word names the Go module proxy or the release tool: a fetch of a module version and a release publish.
func namesReach(args []string) bool {
	return slices.ContainsFunc(args, func(word string) bool {
		return strings.Contains(word, goProxy) || strings.Contains(word, "goreleaser/")
	})
}

// nonOptions returns the words of a command that are not options. The value of an option that takes one is a plain word here too, which is a safe over-read.
func nonOptions(args []string) []string {
	var out []string

	for _, arg := range args {
		if !strings.HasPrefix(arg, "-") {
			out = append(out, arg)
		}
	}

	return out
}

// alwaysNetwork lists the tools that exist to reach another machine or a forge, a registry or a cloud.
func alwaysNetwork() []string {
	return []string{
		cmdGh, "goreleaser", "twine", "aws", "gcloud", "gsutil", "az", "s3cmd", "rclone", "rsync", "scp", "sftp", "ssh", "heroku", "flyctl", "fly", "vercel", "netlify", "wrangler",
		"firebase", "serverless", "sls", "ansible", "ansible-playbook", "doctl", "semantic-release", "release-it", "ko", "crane", "oras", "regctl", "skopeo",
	}
}

// verbHit reports whether a tool that also does local work is given a subcommand that writes outside the machine.
func verbHit(tool string, words []string) bool {
	if verbs, ok := networkVerbs()[tool]; ok && slices.ContainsFunc(words, func(word string) bool { return slices.Contains(verbs, word) }) {
		return true
	}

	return tool == "python" && slices.Contains(words, "twine") || strings.HasPrefix(tool, "gradle") && slices.ContainsFunc(words, func(word string) bool { return strings.HasPrefix(word, verbPublish) })
}

// networkVerbs lists, for each tool that also does local work, the subcommands that write outside the machine.
func networkVerbs() map[string][]string {
	out := map[string][]string{}

	for _, group := range append(packageGroups(), clusterGroups()...) {
		for _, tool := range group.tools {
			out[tool] = group.verbs
		}
	}

	return out
}

// verbGroup is a list of tools that share the subcommands that write outside the machine.
type verbGroup struct{ verbs, tools []string }

// packageGroups lists the tools of a package registry, a repository and a container registry.
func packageGroups() []verbGroup {
	return []verbGroup{
		{[]string{verbPublish, verbPush, verbUpload}, []string{"npm", "yarn", "pnpm", "bun", "lerna", "changeset", "hatch", "flit", "pdm", "poetry", "uv", "gem", "nuget", "dotnet"}},
		{[]string{verbPublish, "semantic-release", "changeset", "release-it", "lerna"}, []string{"npx", "pnpx", "bunx"}},
		{[]string{verbPublish, verbRelease}, []string{"cargo"}},
		{[]string{verbPush, subSendPack}, []string{cmdGit}},
		{[]string{verbPush, verbLogin}, []string{"docker", "podman", "buildah", "nerdctl"}},
		{[]string{"deploy", verbRelease}, []string{"mvn", "mvnw"}},
		{[]string{verbRelease}, []string{"gradle", "gradlew"}},
	}
}

// clusterGroups lists the tools that change a cluster or a cloud account.
func clusterGroups() []verbGroup {
	return []verbGroup{
		{[]string{verbPush, "install", "upgrade", "uninstall"}, []string{"helm"}},
		{[]string{verbApply, verbDestroy}, []string{"terraform", "tofu"}},
		{[]string{"up", verbDestroy}, []string{"pulumi"}},
		{[]string{verbApply, "create", "delete", "patch", "replace", "scale", "rollout", "set", "annotate", "label", "edit", "exec", "cp"}, []string{"kubectl"}},
	}
}

// registryPush reports whether a container tool is told to push by an option.
func registryPush(tool string, args []string) bool {
	if !slices.Contains([]string{"docker", "podman", "buildah", "nerdctl", "buildctl"}, tool) {
		return false
	}

	return slices.ContainsFunc(args, func(arg string) bool {
		return arg == "--push" || strings.HasPrefix(arg, "--push=") || strings.Contains(arg, "type=registry") || strings.Contains(arg, "push=true")
	})
}

// requestWrites reports whether curl or wget sends a body or a method that writes.
func requestWrites(tool string, args []string) bool {
	if !slices.Contains([]string{"curl", "wget", "http", "https"}, tool) {
		return false
	}

	return slices.ContainsFunc(args, func(arg string) bool {
		return slices.Contains([]string{"-d", "-F", "-T", "-X", "--json", "--upload-file", "--form", "--post-data", "--post-file", "--method"}, arg) ||
			strings.HasPrefix(arg, "--data") || strings.HasPrefix(arg, "--form") || strings.HasPrefix(arg, "--request") || strings.HasPrefix(arg, "-X") || strings.HasPrefix(arg, "--post")
	})
}
