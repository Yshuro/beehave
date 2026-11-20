package workflow

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func skillsOf(t *testing.T, files map[string]string) []string {
	t.Helper()

	dir := t.TempDir()

	for name, text := range files {
		if name == "workflow.star" {
			text += "\nr, plan, skills = None, None, []\n"
		}

		require.NoError(t, os.WriteFile(filepath.Join(dir, name), []byte(text), 0o600))
	}

	return RequiredSkills(filepath.Join(dir, "workflow.star"))
}

func TestRequiredSkills_FindsEachSpellingOfTheCheck(t *testing.T) {
	t.Parallel()

	cases := map[string]struct {
		source string
		want   []string
	}{
		"in with the refusal in the else":      {"def main(item):\n    if \"x\" in r.skills:\n        pass\n    else:\n        fail('m')\n", []string{"x"}},
		"not in":                               {"def main(item):\n    if \"x\" not in r.skills:\n        fail('m')\n", []string{"x"}},
		"not around in":                        {"def main(item):\n    if not (\"x\" in r.skills):\n        fail('m')\n", []string{"x"}},
		"not not around in":                    {"def main(item):\n    if not not (\"x\" in r.skills):\n        pass\n    else:\n        fail('m')\n", []string{"x"}},
		"not around not in":                    {"def main(item):\n    if not (\"x\" not in r.skills):\n        pass\n    else:\n        fail('m')\n", []string{"x"}},
		"single quotes":                        {"def main(item):\n    if 'x' not in r.skills:\n        fail('m')\n", []string{"x"}},
		"parentheses":                          {"def main(item):\n    if ((\"x\") not in (r.skills)):\n        fail('m')\n", []string{"x"}},
		"a bound name":                         {"def main(item):\n    called = r.skills\n    if \"x\" not in called:\n        fail('m')\n", []string{"x"}},
		"a bound name in parens":               {"def main(item):\n    called = (r.skills)\n    if \"x\" not in called:\n        fail('m')\n", []string{"x"}},
		"a name bound twice from skills":       {"def main(item):\n    called = r.skills\n    called = plan.skills\n    if \"x\" not in called:\n        fail('m')\n", []string{"x"}},
		"a helper body":                        {"def need(r):\n    if \"x\" not in r.skills:\n        fail('m')\n\ndef main(item):\n    need(r)\n", []string{"x"}},
		"two checks in order":                  {"def main(item):\n    if \"b\" not in r.skills:\n        fail('m')\n    if \"a\" not in r.skills:\n        fail('m')\n", []string{"b", "a"}},
		"another step":                         {"def main(item):\n    if \"x\" not in plan.skills:\n        fail('m')\n", []string{"x"}},
		"a fail with other statements":         {"def main(item):\n    if \"x\" not in r.skills:\n        t = 1\n        fail('m')\n", []string{"x"}},
		"a check under if False":               {"def main(item):\n    if False:\n        if \"x\" not in r.skills:\n            fail('m')\n", []string{"x"}},
		"a nested function with its own check": {"def main(item):\n    def inner():\n        if \"x\" not in r.skills:\n            fail('m')\n    inner()\n", []string{"x"}},
		"a name bound in the same function":    {"def a(r):\n    c = r.skills\n    if \"x\" not in c:\n        fail('m')\n", []string{"x"}},
	}

	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			assert.Equal(t, c.want, skillsOf(t, map[string]string{"workflow.star": c.source}))
		})
	}
}

func TestRequiredSkills_FindsNothingThatIsNotARequirementOnTheSkillsOfAStep(t *testing.T) {
	t.Parallel()

	cases := map[string]string{
		"a comment":                           "# \"x\" not in r.skills\ndef main(item):\n    pass\n",
		"a string":                            "def main(item):\n    t = '\"x\" not in r.skills'\n",
		"a forbid check":                      "def main(item):\n    if \"x\" in r.skills:\n        fail('m')\n",
		"a forbid check with not in":          "def main(item):\n    if \"x\" not in r.skills:\n        pass\n    else:\n        fail('m')\n",
		"a forbid check under not not":        "def main(item):\n    if not not (\"x\" in r.skills):\n        fail('m')\n",
		"a forbid check under not around not": "def main(item):\n    if not (\"x\" not in r.skills):\n        fail('m')\n",
		"a test with no fail":                 "def main(item):\n    if \"x\" not in r.skills:\n        pass\n",
		"a fail inside a deeper test":         "def main(item):\n    if \"x\" not in r.skills:\n        if item:\n            fail('m')\n",
		"a fail in the other branch only":     "def main(item):\n    if \"x\" not in r.skills:\n        pass\n    elif item:\n        fail('m')\n",
		"a bool bound to the test":            "def main(item):\n    ok = \"x\" in r.skills\n    if not ok:\n        fail('m')\n",
		"a count":                             "def main(item):\n    if r.skills.count(\"x\") == 0:\n        fail('m')\n",
		"a comprehension":                     "def main(item):\n    if not [s for s in r.skills if s == \"x\"]:\n        fail('m')\n",
		"an index":                            "def main(item):\n    if \"x\" not in r[\"skills\"]:\n        fail('m')\n",
		"a constant name":                     "def main(item):\n    n = \"x\"\n    if n not in r.skills:\n        fail('m')\n",
		"a name as the helper argument":       "def need(r, name):\n    if name not in r.skills:\n        fail('m')\n\ndef main(item):\n    need(r, \"x\")\n",
		"two tests joined by or":              "def main(item):\n    if \"x\" not in r.skills or \"y\" not in r.skills:\n        fail('m')\n",
		"a name from another field":           "def main(item):\n    called = r.output\n    if \"x\" not in called:\n        fail('m')\n",
		"another field":                       "def main(item):\n    if \"x\" not in r.outputs:\n        fail('m')\n",
		"a name that is not a list":           "def main(item):\n    if \"x\" not in skills:\n        fail('m')\n",
		"the skills on the left":              "def main(item):\n    if r.skills not in \"x\":\n        fail('m')\n",
		"a call on the list":                  "def main(item):\n    if r.skills.index(\"x\"):\n        fail('m')\n",
		"a name that is bound again":          "def main(item):\n    called = r.skills\n    called = []\n    if \"x\" not in called:\n        fail('m')\n",
		"a name that is bound by a loop":      "def main(item):\n    called = r.skills\n    for called in item:\n        pass\n    if \"x\" not in called:\n        fail('m')\n",
		"a name bound in another function":    "def a(r):\n    called = r.skills\n    return called\n\ndef b(called):\n    if \"x\" not in called:\n        fail('m')\n",
		"a name that a nested function takes from the outer one": "def main(item):\n    called = r.skills\n    def inner():\n        if \"x\" not in called:\n            fail('m')\n    inner()\n",
		"a name that a nested function also binds":               "def main(item):\n    called = r.skills\n    def called():\n        pass\n    if \"x\" not in called:\n        fail('m')\n",
		"a parameter that has the name":                          "def main(item, called):\n    called = r.skills\n    if \"x\" not in called:\n        fail('m')\n",
		"a file that does not parse":                             "def main(item:\n    if \"x\" not in r.skills:\n",
	}

	for name, source := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			assert.Empty(t, skillsOf(t, map[string]string{"workflow.star": source}))
		})
	}
}

func TestRequiredSkills_ReadsTheFilesThatTheWorkflowLoads(t *testing.T) {
	t.Parallel()

	got := skillsOf(t, map[string]string{
		"workflow.star": "load(\"//gates.star\", \"need\")\n\ndef main(item):\n    need(r)\n",
		"gates.star":    "def need(r):\n    if \"x\" not in r.skills:\n        fail('m')\n",
	})

	assert.Equal(t, []string{"x"}, got)
}

func TestRequiredSkills_FindsNothingInAFileThatCannotBeRead(t *testing.T) {
	t.Parallel()

	assert.Empty(t, RequiredSkills(filepath.Join(t.TempDir(), "missing.star")))
}

func writeWorkflow(t *testing.T, source string) string {
	t.Helper()

	file := filepath.Join(t.TempDir(), "workflow.star")
	require.NoError(t, os.WriteFile(file, []byte(source+"\nr, project, key = None, None, ''\n"), 0o600))

	return file
}

func TestRequiredSkillVars_FindsTheKeyOfAVarsTestAndNoLiteralSkill(t *testing.T) {
	t.Parallel()

	file := writeWorkflow(t, "def main(item):\n    if project.vars[\"review\"] not in r.skills:\n        fail('m')\n    if (project.vars['again']) in r.skills:\n        pass\n    else:\n        fail('m')\n")

	assert.Equal(t, []string{"review", "again"}, RequiredSkillVars(file))
	assert.Empty(t, RequiredSkills(file), "a key is not a skill name")
}

func TestRequiredSkillVars_FindsNothingThatIsNotAVarsKeyTestWithAFail(t *testing.T) {
	t.Parallel()

	cases := map[string]string{
		"a dict that is not the vars": "def main(item):\n    if project.facts[\"x\"] not in r.skills:\n        fail('m')\n",
		"a name for the dict":         "def main(item):\n    if vars[\"x\"] not in r.skills:\n        fail('m')\n",
		"a key that is not a literal": "def main(item):\n    if project.vars[key] not in r.skills:\n        fail('m')\n",
		"a test with no fail":         "def main(item):\n    if project.vars[\"x\"] not in r.skills:\n        pass\n",
		"a forbid check on the key":   "def main(item):\n    if project.vars[\"x\"] in r.skills:\n        fail('m')\n",
	}

	for name, source := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			assert.Empty(t, RequiredSkillVars(writeWorkflow(t, source)))
		})
	}
}
