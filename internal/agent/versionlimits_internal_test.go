package agent

import (
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestGuardVersionProblem_RefusesAGuardedStepFromTheLimitOnAndNothingElse(t *testing.T) {
	t.Parallel()

	limited := Profile{Binary: "cli", GuardBefore: "2.0.0"}
	refusal := "cli is at version %s, and the guard of forbid_commands does not work from version 2.0.0 on, so a step with forbid_commands is not started. " +
		"Remove forbid_commands from the step, or install a version below 2.0.0."

	cases := []struct {
		name    string
		guard   string
		profile Profile
		out     string
		want    string
	}{
		{"a guard below the limit", "g", limited, "cli 1.99.99", ""},
		{"a guard at the limit", "g", limited, "cli 2.0.0", fmt.Sprintf(refusal, "2.0.0")},
		{"a guard above the limit", "g", limited, "cli v2.2.5", fmt.Sprintf(refusal, "2.2.5")},
		{"no guard at the limit", "", limited, "cli 2.0.0", ""},
		{"no guard above the limit", "", limited, "cli 2.2.5", ""},
		{"a guard and a profile with no limit", "g", Profile{Binary: "cli"}, "cli 9.9.9", ""},
		{"a guard and a version text with no number", "g", limited, "next", ""},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			assert.Equal(t, tc.want, guardVersionProblem(Spec{Guard: tc.guard}, tc.profile, tc.out))
		})
	}
}

func TestVersionArgs_AddsTheEntriesThatTheVersionReaches(t *testing.T) {
	t.Parallel()

	profile := Profile{VersionArgs: []VersionArgs{
		{From: "2.0.0", Args: []string{"--two"}},
		{From: "3.1.0", Args: []string{"--three", "--more"}},
	}}

	cases := []struct {
		name, out string
		want      []string
	}{
		{"below every entry", "cli 1.99.99", nil},
		{"at the first entry", "cli 2.0.0", []string{"--two"}},
		{"between the entries", "cli 3.0.9", []string{"--two"}},
		{"at the second entry", "cli 3.1.0", []string{"--two", "--three", "--more"}},
		{"above both", "cli v4.0.0", []string{"--two", "--three", "--more"}},
		{"a text with no number", "next", nil},
		{"an empty text", "", nil},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			assert.Equal(t, tc.want, versionArgs(profile, tc.out))
		})
	}

	assert.Nil(t, versionArgs(Profile{}, "cli 2.0.0"), "a profile with no entries adds nothing")
}
