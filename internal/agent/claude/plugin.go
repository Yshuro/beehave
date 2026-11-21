package claude

import (
	"maps"
	"path/filepath"
	"slices"
	"strings"

	"github.com/tobocop2/beehave/internal/agent"
)

// pluginName is the name of the plugin that holds the skills of a sandboxed run. The CLI names each of its skills <pluginName>:<skill>.
const pluginName = "beehave-skills"

// pluginFolder is the plugin folder in the run folder, and pluginManifest its manifest, relative to it.
const (
	pluginFolder   = "skills-plugin"
	pluginManifest = ".claude-plugin/plugin.json"
)

// plugin is what the skills of a plan add to a sandboxed launch: the manifest, the copy of each skill folder, the flag that loads the plugin, and its folder.
type plugin struct {
	files  map[string]string
	copies map[string]string
	args   []string
	dir    string
}

// skillsPlugin returns the plugin that holds a copy of each skill folder, or an empty plugin for a plan with no skills.
func skillsPlugin(skills []string, runDir string) plugin {
	if len(skills) == 0 {
		return plugin{}
	}

	copies := make(map[string]string, len(skills))
	for _, source := range skills {
		copies[filepath.Join(pluginFolder, "skills", filepath.Base(source))] = source
	}

	dir := filepath.Join(runDir, pluginFolder)

	return plugin{
		files:  map[string]string{filepath.Join(pluginFolder, pluginManifest): `{"name":"` + pluginName + `","version":"0.0.1","description":"Skills that the workflow names"}` + "\n"},
		copies: copies,
		args:   []string{"--plugin-dir", dir},
		dir:    dir,
	}
}

// confine returns the plan with the plugin folder readable by the shell and closed to writes, so the agent reads the skills and cannot change them.
func (p plugin) confine(plan agent.SandboxPlan) agent.SandboxPlan {
	if p.dir == "" {
		return plan
	}

	plan.Read = slices.Concat(plan.Read, []string{p.dir})
	plan.DenyWrite = slices.Concat(plan.DenyWrite, []string{p.dir})

	return plan
}

// withFiles returns the launch files with the manifest of the plugin added.
func (p plugin) withFiles(files map[string]string) map[string]string {
	maps.Copy(files, p.files)

	return files
}

// plainSkill returns the name of a skill without the prefix of the plugin of the engine. A skill of any other plugin keeps its prefix.
func plainSkill(name string) string {
	return strings.TrimPrefix(name, pluginName+":")
}
