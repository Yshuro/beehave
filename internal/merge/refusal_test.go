package merge_test

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/tobocop2/beehave/internal/merge"
	"github.com/tobocop2/beehave/internal/model"
)

// attributionLine is a line that the patterns of the example forbid.
const attributionLine = "Co-Authored-By: Claude <noreply@anthropic.com>"

func TestRun_AForbiddenLineIsNamedByItsNumberAndPatternAndNotQuoted(t *testing.T) {
	t.Parallel()

	req := request()
	result := newWorld(t).set(t, map[string]any{"body": "a\nb\nGenerated with [Claude Code]", "commits": []any{commitJSON("dev@x", dev, "fix", "")}}).run(req)

	assert.Equal(t, merge.Refused, result.Stop)
	assert.Equal(t, []string{"REFUSE: forbidden text on #5. Offending lines:", "  line 3 matches pattern 4 of forbid_text"}, result.Refusal)

	_, found := merge.ForbiddenBy(req.ForbidText, all(result))
	assert.False(t, found, "no line of the result repeats the forbidden text")
}

func TestRun_NoLineOfTheResultMatchesTheForbidTextOfTheRequest(t *testing.T) {
	t.Parallel()

	path, line := "a.go", 3
	cases := map[string]struct {
		over    map[string]any
		threads []model.ReviewThread
		says    string
	}{
		"a line of the body":       {over: map[string]any{"body": "first\n" + attributionLine}, says: "  line 2 matches pattern 1 of forbid_text"},
		"a line of a commit body":  {over: map[string]any{"commits": []any{commitJSON("dev@x", dev, "fix", attributionLine)}}, says: "  line 2 matches pattern 1 of forbid_text"},
		"a commit headline":        {over: map[string]any{"commits": []any{commitJSON("dev@x", dev, attributionLine, "")}}, says: "  line 3 matches pattern 1 of forbid_text"},
		"the first thread comment": {threads: []model.ReviewThread{{Path: &path, Line: &line, First: &model.ThreadComment{Author: model.Account{Login: "rev"}, Body: new(attributionLine)}}}, says: "a.go:3 rev: (the comment is left out: it matches pattern 1 of forbid_text)"},
	}
	for name, one := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			w := newWorld(t)
			if one.over != nil {
				w.set(t, one.over)
			}

			w.threads = one.threads
			req := request()
			result := w.run(req)

			assert.Equal(t, merge.Refused, result.Stop)
			assert.Contains(t, result.Refusal, one.says)

			for _, said := range result.Lines {
				_, found := merge.ForbiddenBy(req.ForbidText, said)
				assert.False(t, found, "the result line %q matches a pattern of the request", said)
			}
		})
	}
}

func TestRun_ALineOfForgeTextThatAPatternMatchesIsLeftOutOfTheResult(t *testing.T) {
	t.Parallel()

	w := newWorld(t)
	w.fails["UnresolvedThreads"] = true
	req := request()
	req.ForbidText = compiled([]string{"zzz", "down"})
	result := w.run(req)

	assert.Equal(t, merge.Refused, result.Stop)
	require.NotEmpty(t, result.Refusal)
	assert.Equal(t, "(a line is left out: it matches pattern 2 of forbid_text)", result.Refusal[0])
	assert.Equal(t, result.Refusal, result.Lines[len(result.Lines)-len(result.Refusal):])
}

func TestRun_ARefusalKeepsItsLinesApartFromTheLinesBefore(t *testing.T) {
	t.Parallel()

	w := newWorld(t)
	w.checks = rows(model.CheckPass, model.CheckBucket("fail"))
	result := w.run(request())

	assert.Equal(t, merge.Refused, result.Stop)
	assert.Equal(t, []string{"REFUSE: checks are not green: c1=fail", "       (above refers to #5)"}, result.Refusal)
	require.Greater(t, len(result.Lines), len(result.Refusal), "the guards that passed said lines before the refusal")
	assert.Equal(t, result.Refusal, result.Lines[len(result.Lines)-len(result.Refusal):])
	assert.NotContains(t, strings.Join(result.Refusal, "\n"), "OK")
}

func TestRun_AMergeThatLandsHasNoRefusal(t *testing.T) {
	t.Parallel()

	result := newWorld(t).run(request())

	assert.Equal(t, merge.Done, result.Stop)
	assert.Empty(t, result.Refusal)
}
