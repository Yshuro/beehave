## What the queue audit must do

You audit the open work of the project before pass {{ .pass }} of the round on the base `{{ .base }}`. You change no file, no issue and no pull request.

Read the open issues and the open pull requests with the forge tool. Report each item that the queue would give out wrongly: an issue that a merged pull request already fixed, a pull request with no issue, and an item with a label that blocks it.

Say in your report what you found. Write "nothing found" when the open work is as the queue shows it.

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
