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
		"in":                     {"def main(item):\n    if \"x\" in r.skills:\n        pass\n", []string{"x"}},
		"not in":                 {"def main(item):\n    if \"x\" not in r.skills:\n        fail('m')\n", []string{"x"}},
		"not around in":          {"def main(item):\n    if not (\"x\" in r.skills):\n        fail('m')\n", []string{"x"}},
		"single quotes":          {"def main(item):\n    if 'x' in r.skills:\n        pass\n", []string{"x"}},
		"parentheses":            {"def main(item):\n    if (\"x\") in (r.skills):\n        pass\n", []string{"x"}},
		"a bound name":           {"def main(item):\n    called = r.skills\n    if \"x\" not in called:\n        fail('m')\n", []string{"x"}},
		"a bound name in parens": {"def main(item):\n    called = (r.skills)\n    if \"x\" not in called:\n        fail('m')\n", []string{"x"}},
		"a helper body":          {"def need(r):\n    if \"x\" not in r.skills:\n        fail('m')\n\ndef main(item):\n    need(r)\n", []string{"x"}},
		"two checks in order":    {"def main(item):\n    if \"b\" not in r.skills or \"a\" not in r.skills:\n        fail('m')\n", []string{"b", "a"}},
		"another step":           {"def main(item):\n    if \"x\" not in plan.skills:\n        fail('m')\n", []string{"x"}},
	}

	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			assert.Equal(t, c.want, skillsOf(t, map[string]string{"workflow.star": c.source}))
		})
	}
}

func TestRequiredSkills_FindsNothingThatIsNotATestOfTheSkillsOfAStep(t *testing.T) {
	t.Parallel()

	cases := map[string]string{
		"a comment":                     "# \"x\" not in r.skills\ndef main(item):\n    pass\n",
		"a string":                      "def main(item):\n    t = '\"x\" not in r.skills'\n",
		"a name from another field":     "def main(item):\n    called = r.output\n    if \"x\" in called:\n        pass\n",
		"another field":                 "def main(item):\n    if \"x\" in r.outputs:\n        pass\n",
		"a name as the helper argument": "def need(r, name):\n    if name not in r.skills:\n        fail('m')\n\ndef main(item):\n    need(r, \"x\")\n",
		"a name that is not a list":     "def main(item):\n    if \"x\" in skills:\n        pass\n",
		"the skills on the left":        "def main(item):\n    if r.skills in \"x\":\n        pass\n",
		"a call on the list":            "def main(item):\n    if r.skills.index(\"x\"):\n        pass\n",
		"an assignment of the check":    "def main(item):\n    ok = \"x\" == r.skills\n",
		"a file that does not parse":    "def main(item:\n    if \"x\" in r.skills:\n",
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
