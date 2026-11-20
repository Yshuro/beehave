package gate_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/tobocop2/beehave/examples/remediation/tools/gate"
)

// formStep puts a body of steps in a job, before a step that checks and never publishes.
func formStep(body string) string {
	return "on: push\njobs:\n  j:\n    runs-on: ubuntu-latest\n    steps:\n" + body + "      - run: make test\n"
}

func formRun(command string) string { return formStep("      - run: " + command + "\n") }

func formUses(action, with string) string {
	return formStep("      - uses: " + action + "\n" + with)
}

func formJobs(job string) string {
	return "on: push\njobs:\n" + job + "  t:\n    runs-on: x\n    steps:\n      - run: make test\n"
}

// everyPublishingForm is each form of the review of the pull request (the table of 71 forms that the deny list missed, with the forms it caught).
func everyPublishingForm() map[string]string {
	return map[string]string{
		"gh release":                   formRun("gh release create v1"),
		"gh -R value release":          formRun("gh -R o/r release create v1"),
		"gh release edit":              formRun("gh release edit v1 --draft=false"),
		"gh api post":                  formRun("gh api -X POST repos/o/r/releases -f tag_name=v1"),
		"gh workflow run":              formRun("gh workflow run release.yaml"),
		"goreleaser bare":              formRun("goreleaser --clean"),
		"goreleaser go run":            formRun("go run github.com/goreleaser/goreleaser/v2@latest release --clean"),
		"goreleaser docker image":      formUses("docker://goreleaser/goreleaser:latest", "        with:\n          args: release --clean\n"),
		"goreleaser action":            formUses("goreleaser/goreleaser-action@v6", "        with:\n          args: ${{ env.ARGS }}\n"),
		"git push":                     formRun("git push origin main"),
		"git git-dir push":             formRun("git --git-dir .git push origin main"),
		"git work-tree push":           formRun("git --work-tree . push"),
		"git push eval":                formRun("eval 'git push origin main'"),
		"bash o pipefail c":            formRun("bash -o pipefail -c 'git push origin main'"),
		"bash e c":                     formRun("bash -e -c 'git push origin main'"),
		"bash ce":                      formRun("bash -ce 'git push origin main'"),
		"git push over ssh":            formRun("ssh deploy@host 'git push origin main'"),
		"git push sudo":                formRun("sudo -u ci git push origin main"),
		"git push env":                 formRun("env A=1 git push origin main"),
		"git push xargs":               formRun("echo main | xargs git push origin"),
		"git push in function":         formStep("      - run: |\n          f() { git push origin main; }\n          f\n"),
		"git push in substitution":     formRun("echo $(git push origin main)"),
		"git push in if":               formStep("      - run: |\n          if [ \"$X\" = 1 ]; then git push; fi\n"),
		"git push heredoc":             formStep("      - run: |\n          sh <<'EOF'\n          git push origin main\n          EOF\n"),
		"commit action":                formUses("EndBug/add-and-commit@v9", ""),
		"github-script":                formUses("actions/github-script@v7", "        with:\n          script: github.rest.repos.createRelease({owner:'o',repo:'r',tag_name:'v1'})\n"),
		"expression command":           formRun("${{ matrix.cmd }}"),
		"variable command":             formRun("$CMD origin main"),
		"env value is the command":     formStep("      - env:\n          CMD: git push origin main\n        run: $CMD\n"),
		"docker push":                  formRun("docker push ghcr.io/o/r:1"),
		"docker image push":            formRun("docker image push ghcr.io/o/r:1"),
		"docker context push":          formRun("docker --context ci push ghcr.io/o/r:1"),
		"buildx bake push":             formRun("docker buildx bake --push"),
		"manifest push":                formRun("docker manifest push ghcr.io/o/r:1"),
		"compose push":                 formRun("docker compose push"),
		"podman push":                  formRun("podman push ghcr.io/o/r:1"),
		"skopeo copy":                  formRun("skopeo copy docker-archive:a.tar docker://ghcr.io/o/r:1"),
		"ko build":                     formRun("ko build ./cmd/x"),
		"build push action":            formUses("docker/build-push-action@v6", "        with:\n          push: ${{ github.event_name != 'pull_request' }}\n"),
		"build push action true":       formUses("docker/build-push-action@v6", "        with:\n          push: True\n"),
		"buildx push true":             formRun("docker buildx build --push=true ."),
		"buildx output registry":       formRun("docker buildx build --output type=registry ."),
		"npm publish":                  formRun("npm publish"),
		"npm workspace publish":        formRun("npm --workspace pkg publish"),
		"npx semantic-release":         formRun("npx semantic-release"),
		"npx changeset publish":        formRun("npx changeset publish"),
		"npm publish dry-run value":    formRun("npm publish --tag --dry-run"),
		"changesets action":            formUses("changesets/action@v1", "        with:\n          publish: npm run release\n"),
		"twine upload":                 formRun("twine upload dist/*"),
		"python m twine":               formRun("python -m twine upload dist/*"),
		"hatch publish":                formRun("hatch publish"),
		"flit publish":                 formRun("flit publish"),
		"pdm publish":                  formRun("pdm publish"),
		"pypi action":                  formUses("pypa/gh-action-pypi-publish@release/v1", ""),
		"cargo publish":                formRun("cargo publish"),
		"cargo toolchain publish":      formRun("cargo +stable publish"),
		"cargo z publish":              formRun("cargo -Z unstable-options publish"),
		"cargo release":                formRun("cargo release --execute"),
		"cargo publish dry-run":        formRun("cargo publish --dry-run"),
		"go proxy fetch":               formRun("GOPROXY=proxy.golang.org go list -m example.com/m@v1.0.0"),
		"curl go proxy":                formRun("curl https://proxy.golang.org/example.com/m/@v/v1.0.0.info"),
		"kubectl apply":                formRun("kubectl -n prod apply -f k8s/"),
		"kubectl apply dry run":        formRun("kubectl apply --dry-run=client -f k8s/"),
		"aws s3 sync":                  formRun("aws s3 sync site/ s3://bucket"),
		"gsutil cp":                    formRun("gsutil cp -r site gs://bucket"),
		"az webapp deploy":             formRun("az webapp deploy --src-path a.zip"),
		"rsync to host":                formRun("rsync -a site/ deploy@host:/var/www"),
		"heroku release":               formRun("heroku container:release web"),
		"gradlew publish":              formRun("./gradlew publish"),
		"mvnw deploy":                  formRun("./mvnw deploy"),
		"mvn settings deploy":          formRun("mvn -s settings.xml deploy"),
		"deploy pages":                 formUses("actions/deploy-pages@v4", ""),
		"ecs deploy":                   formUses("aws-actions/amazon-ecs-deploy-task-definition@v2", ""),
		"wrangler action":              formUses("cloudflare/wrangler-action@v3", ""),
		"k8s deploy action":            formUses("azure/k8s-deploy@v5", ""),
		"ssh action":                   formUses("appleboy/ssh-action@v1", "        with:\n          script: ./deploy.sh\n"),
		"curl pipe sh":                 formRun("curl -fsSL https://x/release.sh | sh"),
		"curl pipe bash s":             formRun("curl -fsSL https://x/release.sh | bash -s -- --publish"),
		"local composite action":       formUses("./.github/actions/release", ""),
		"reusable workflow":            formJobs("  rel:\n    uses: o/r/.github/workflows/release.yaml@main\n    secrets: inherit\n"),
		"local reusable workflow":      formJobs("  rel:\n    uses: ./.github/workflows/release.yaml\n"),
		"if gated push":                formStep("      - if: github.ref == 'refs/heads/main'\n        run: git push origin main\n"),
		"env gated publish":            formStep("      - env:\n          PUBLISH: ${{ inputs.publish }}\n        run: '[ \"$PUBLISH\" = true ] && npm publish'\n"),
		"shell python":                 formStep("      - shell: python\n        run: |\n          import subprocess\n          subprocess.run(['git','push'])\n"),
		"shell pwsh":                   formStep("      - shell: pwsh\n        run: git push origin main\n"),
		"dry-run in the other command": formRun("npm publish --dry-run && npm publish"),
		"dry-run as push option":       formRun("git push -o --dry-run origin main"),
		"dry-run as notes value":       formRun("gh release create v1 --notes --dry-run"),
		"dry-run in a shell text":      formRun("sh -c 'npm publish --dry-run; npm publish'"),
		"a secret in a step":           formStep("      - env:\n          K: ${{ secrets.KEY }}\n        run: make publish-docs\n"),
		"a secret in a with":           formStep("      - uses: actions/checkout@v7\n        with:\n          token: ${{ secrets.PAT }}\n"),
	}
}

func TestDerive_EveryFormThatTheReviewerSawMissedIsLeftOutOfTheList(t *testing.T) {
	t.Parallel()

	for name, text := range everyPublishingForm() {
		got, err := gate.Derive([]gate.File{{Path: "w.yaml", Text: text}})

		require.NoError(t, err, name)
		commands := make([]string, len(got.Steps))
		for i, step := range got.Steps {
			commands[i] = step.Command
		}

		assert.Equal(t, []string{"make test"}, commands, name)
		assert.NotEmpty(t, got.Refused, name)
	}
}

func TestDerive_ATestOfTheRepositoryThatOnlyNamesAPublishingToolIsKept(t *testing.T) {
	t.Parallel()

	for name, command := range map[string]string{
		"echo of a command":  "echo git push is not run here",
		"grep for a command": "grep -r 'npm publish' docs",
		"git status":         "git status --short",
		"docker build":       "docker build -t img .",
		"npm test":           "npm test",
		"cargo test":         "cargo test --all",
		"curl get":           "curl -fsS https://example.com/health",
		"a make target named for the release tool": "make goreleaser-install",
		"go proxy off":        "GOPROXY=off go test ./...",
		"make with a target":  "make check",
		"a gate with options": "go test -race -count=1 ./...",
	} {
		got := derivedAll(t, formRun(command))

		assert.Len(t, got.Steps, 2, name)
		assert.Empty(t, got.Refused, name)
	}
}

func TestDerive_ASetupOrCheckoutActionGivesNoStepAndIsNotRefused(t *testing.T) {
	t.Parallel()

	got := derivedAll(t, formUses("actions/setup-go@v6", "        with:\n          go-version: '1.27'\n"))

	assert.Len(t, got.Steps, 1)
	assert.Empty(t, got.Refused)
}
