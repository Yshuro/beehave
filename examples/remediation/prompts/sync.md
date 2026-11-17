## What the sync step must do

The round merges the trunk into the round branch `{{ .base }}` through the sync branch `{{ .branch }}`. The merge of `{{ .source }}` into the sync branch conflicts, so the forge wrote nothing. You resolve that one merge.

1. In your folder, run `git fetch origin {{ .branch }} {{ .source }}`.
2. Run `git checkout --detach origin/{{ .branch }}`.
3. Run `git merge --no-ff origin/{{ .source }}`.
4. Resolve each conflict so that both sides keep their intent. Keep every test of both sides. Commit the merge commit with `git commit --no-edit`.
5. Run `git push origin HEAD:refs/heads/{{ .branch }}`.

Make one merge commit with the two parents (the sync branch tip, then `origin/{{ .source }}`). Do not rebase, amend, squash or force-push. Do not change a line that the conflict does not need. Do not open a pull request: the round opens it after your push.

If you cannot resolve a conflict, push nothing, and say in your report which files conflict and why. The round then waits for the owner with the reason `sync-conflict`.

## Facts of the project

The run read these facts from the project file at its start. A fact that is empty holds no fact: ask the owner, and do not guess it.

- Repository identity and fork topology: repo `{{ .facts.repo }}`, source `{{ .facts.source }}`, forge `{{ .facts.forge }}`.
- Build host: `{{ .facts.build_host }}`.
- Base branch: `{{ .facts.base_branch }}`.
- The gate is `{{ .facts.gate }}`. The gate list comes from these workflow files:{{ range .facts.gate_files }} `{{ . }}`{{ end }}
- Tracker: `{{ .facts.tracker }}`. Queue to epic:{{ range $queue, $epic := .facts.epics }} `{{ $queue }}` is `{{ $epic }}`;{{ end }}
- People and the idle threshold for a third-party pull request:{{ range $role, $who := .facts.people }} `{{ $role }}` is `{{ $who }}`;{{ end }}
- Review against these convention files:{{ range .facts.conventions }} `{{ . }}`{{ end }}

Brief rules of the project:
{{ range .facts.brief_rules }}
- {{ . }}
{{ end }}
Rules for a pull request that stacks on another:
{{ range .facts.stacked_rules }}
- {{ . }}
{{ end }}
Token rules, cross-repository steps and release steps:
{{ range .facts.token_rules }}
- {{ . }}
{{ end }}
Hard limits, commands never run and real user data:
{{ range .facts.limits }}
- {{ . }}
{{ end }}
Measured traps and ownership rules:
{{ range .facts.traps }}
- {{ . }}
{{ end }}
