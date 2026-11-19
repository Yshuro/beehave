package flow

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// dryAgentFlow is a workflow of the functions in defs and one agent step whose result r the statements of check read.
func dryAgentFlow(t *testing.T, defs, check string) string {
	t.Helper()

	file := dryFile(t, defs+bodyFlow("r = agent(model = \"m\", prompt = \"a.md\", name = \"a\")\n"+check))
	require.NoError(t, os.WriteFile(filepath.Join(filepath.Dir(file), "a.md"), []byte("hello\n"), 0o600))

	return file
}

const skillRefusal = "fail(\"REFUSE: no skill x\")\n"

func TestDryRun_TheFakeAgentReportsTheSkillOfEachSpellingOfTheCheck(t *testing.T) {
	t.Parallel()

	const helper = "def need(r):\n    if \"x\" not in r.skills:\n        " + skillRefusal + "\n"

	cases := map[string]struct{ defs, check string }{
		"in":                {"", "if \"x\" in r.skills:\n    pass\nelse:\n    " + skillRefusal},
		"not in":            {"", "if \"x\" not in r.skills:\n    " + skillRefusal},
		"not around in":     {"", "if not (\"x\" in r.skills):\n    " + skillRefusal},
		"not not around in": {"", "if not not (\"x\" in r.skills):\n    pass\nelse:\n    " + skillRefusal},
		"single quotes":     {"", "if 'x' not in r.skills:\n    " + skillRefusal},
		"a bound name":      {"", "called = r.skills\nif \"x\" not in called:\n    " + skillRefusal},
		"in a helper":       {helper, "need(r)\n"},
		"parenthesized":     {"", "if (\"x\") not in (r.skills):\n    " + skillRefusal},
		"after a comment":   {"", "# \"y\" not in r.skills\nif \"x\" not in r.skills:\n    " + skillRefusal},
	}

	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			var out bytes.Buffer

			require.NoError(t, DryRun(t.Context(), dryAgentFlow(t, c.defs, c.check), "12", map[string]string{}, &out), out.String())
			assert.NotContains(t, out.String(), "REFUSE")
		})
	}
}

func TestDryRun_AForbidCheckOfASkillPassesTheDryRun(t *testing.T) {
	t.Parallel()

	forbids := map[string]string{
		"in":                   "if \"x\" in r.skills:\n    " + skillRefusal,
		"not in with the else": "if \"x\" not in r.skills:\n    pass\nelse:\n    " + skillRefusal,
		"not not around in":    "if not not (\"x\" in r.skills):\n    " + skillRefusal,
		"not around not in":    "if not (\"x\" not in r.skills):\n    " + skillRefusal,
		"a bound name with in": "called = r.skills\nif \"x\" in called:\n    " + skillRefusal,
	}

	for name, check := range forbids {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			var out bytes.Buffer

			require.NoError(t, DryRun(t.Context(), dryAgentFlow(t, "", check), "12", map[string]string{}, &out), out.String())
			assert.NotContains(t, out.String(), "REFUSE")
		})
	}
}

func TestDryRun_ACheckThroughAHelperThatTakesTheNameAsAnArgumentStopsTheRun(t *testing.T) {
	t.Parallel()

	var out bytes.Buffer

	file := dryAgentFlow(t, "def need(r, name):\n    if name not in r.skills:\n        "+skillRefusal+"\n", "need(r, \"x\")\n")

	require.Error(t, DryRun(t.Context(), file, "12", map[string]string{}, &out))
	assert.Contains(t, out.String(), "REFUSE: no skill x")
}
