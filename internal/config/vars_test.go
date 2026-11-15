package config_test

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestConfig_AProjectVarsMapTakesAnyKey(t *testing.T) {
	t.Parallel()

	project, problems := loadProjectText(t, call(localProject+`, vars = {"a": 1, "b": ["x"], "c": {"d": True}, "e": 1.5, "f": None, "g": ("y", 2)}`))

	require.Empty(t, problems)
	require.Equal(t, map[string]any{
		"a": json.Number("1"), "b": []any{"x"}, "c": map[string]any{"d": true}, "e": json.Number("1.5"), "f": nil, "g": []any{"y", json.Number("2")},
	}, project.Vars)
}

func TestConfig_AProjectWithNoVarsHasNone(t *testing.T) {
	t.Parallel()

	project, problems := loadProjectText(t, call(localProject))

	require.Empty(t, problems)
	require.Empty(t, project.Vars)
}

func TestConfig_AVarsValueThatTheRunCannotKeepIsRefusedWithItsKey(t *testing.T) {
	t.Parallel()

	for name, args := range map[string]string{
		"a list for the map":    `vars = ["a"]`,
		"a number as a key":     `vars = {1: "a"}`,
		"a function as a value": `vars = {"a": len}`,
		"a nested bad value":    `vars = {"a": {"b": [len]}}`,
		"a float that is inf":   `vars = {"a": float("inf")}`,
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			_, problems := loadProjectText(t, call(localProject+", "+args))

			require.Len(t, problems, 1)
			require.Contains(t, problems[0].Error(), "vars")
		})
	}
}
