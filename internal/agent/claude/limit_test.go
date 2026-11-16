package claude_test

import (
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/tobocop2/beehave/internal/agent"
	"github.com/tobocop2/beehave/internal/agent/claude"
)

// The rate-limited-*.jsonl fixtures are built from the code of the Claude CLI 2.1.325 that writes these lines, not from a captured run: a rate_limit_event
// carries the rate_limit_info that the CLI builds from the response headers, and the assistant line of a refused request carries error "rate_limit".

// fixtureLines returns the lines of a fixture file.
func fixtureLines(t *testing.T, name string) []string {
	t.Helper()

	return strings.Split(strings.TrimSpace(fixture(t, name)), "\n")
}

func TestCalls_ARejectedRateLimitEventReportsALimit(t *testing.T) {
	t.Parallel()

	for _, name := range []string{"rate-limited-five-hour.jsonl", "rate-limited-seven-day.jsonl", "rate-limited-overage.jsonl"} {
		seen := claude.Adapter{}.Calls(fixtureLines(t, name)[0])

		assert.NotEqual(t, agent.Seen{}, seen, "the adapter reads nothing from the rejected rate_limit_event of %s", name)
		assert.NotNil(t, seen.Limit, "the rejected rate_limit_event of %s reports no limit", name)
	}
}

func TestCalls_ARejectedRateLimitEventCarriesItsKindWindowAndResetTime(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name, fixture string
		want          agent.Limit
	}{
		{"five_hour", "rate-limited-five-hour.jsonl", agent.Limit{Kind: agent.LimitRate, Window: "five_hour", ResetsAt: time.Unix(1795006800, 0).UTC()}},
		{"seven_day", "rate-limited-seven-day.jsonl", agent.Limit{Kind: agent.LimitRate, Window: "seven_day", ResetsAt: time.Unix(1795392000, 0).UTC()}},
		{"overage_takes_the_earlier_spend_reset", "rate-limited-overage.jsonl", agent.Limit{Kind: agent.LimitSpend, Window: "seven_day", ResetsAt: time.Unix(1795132800, 0).UTC()}},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			seen := claude.Adapter{}.Calls(fixtureLines(t, tc.fixture)[0])

			require.NotNil(t, seen.Limit)
			assert.Equal(t, tc.want, *seen.Limit)
		})
	}
}

func TestCalls_ASpendLimitResetsAtTheEarlierOfThePlanResetAndTheSpendReset(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name, info string
		want       time.Time
	}{
		{"plan_resets_first", `"resetsAt":1795000000,"overageResetsAt":1795100000`, time.Unix(1795000000, 0).UTC()},
		{"spend_resets_first", `"resetsAt":1795100000,"overageResetsAt":1795000000`, time.Unix(1795000000, 0).UTC()},
		{"only_the_plan_reset", `"resetsAt":1795000000`, time.Unix(1795000000, 0).UTC()},
		{"only_the_spend_reset", `"overageResetsAt":1795000000`, time.Unix(1795000000, 0).UTC()},
		{"no_reset", `"rateLimitType":"overage"`, time.Time{}},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			line := `{"type":"rate_limit_event","rate_limit_info":{"status":"rejected","overageStatus":"rejected","isUsingOverage":true,` + tc.info + `}}`
			seen := claude.Adapter{}.Calls(line)

			require.NotNil(t, seen.Limit)
			assert.Equal(t, agent.LimitSpend, seen.Limit.Kind)
			assert.Equal(t, tc.want, seen.Limit.ResetsAt)
		})
	}
}

func TestCalls_ARejectedRateLimitEventWithNoUsableResetTimeHasTheZeroTime(t *testing.T) {
	t.Parallel()

	for name, reset := range map[string]string{"missing": `"rateLimitType":"five_hour"`, "zero": `"resetsAt":0`, "negative": `"resetsAt":-5`, "text": `"resetsAt":"soon"`} {
		seen := claude.Adapter{}.Calls(`{"type":"rate_limit_event","rate_limit_info":{"status":"rejected",` + reset + `}}`)

		require.NotNil(t, seen.Limit, name)
		assert.Equal(t, agent.Limit{Kind: agent.LimitRate, Window: map[string]string{"missing": "five_hour"}[name]}, *seen.Limit, name)
	}
}

func TestCalls_AnAllowedOrWarningRateLimitEventIsNoLimit(t *testing.T) {
	t.Parallel()

	for _, line := range fixtureLines(t, "rate-limited-allowed.jsonl") {
		assert.Equal(t, agent.Seen{}, claude.Adapter{}.Calls(line), "a rate_limit_event that allows the request is no limit: %s", line)
	}

	assert.Nil(t, claude.Adapter{}.Calls(`{"type":"rate_limit_event"}`).Limit, "an event with no rate_limit_info is no limit")
}

func TestCalls_AnAssistantRateLimitErrorIsALimitWithNoTime(t *testing.T) {
	t.Parallel()

	seen := claude.Adapter{}.Calls(fixtureLines(t, "rate-limited-assistant-error.jsonl")[0])

	assert.NotEqual(t, agent.Seen{}, seen, "the adapter reads nothing from an assistant rate_limit error")
	require.NotNil(t, seen.Limit)
	assert.Equal(t, agent.Limit{Kind: agent.LimitRate}, *seen.Limit)
}

func TestCalls_AnAssistantLineWithAnotherErrorOrOnARejectedLineOfAnotherTypeIsNoLimit(t *testing.T) {
	t.Parallel()

	for _, line := range []string{
		`{"type":"assistant","message":{"content":[]},"error":"server_error"}`,
		`{"type":"assistant","message":{"content":[]}}`,
		`{"type":"result","subtype":"success","is_error":true,"error":"rate_limit","rate_limit_info":{"status":"rejected"}}`,
		`["rate_limit_event"]`,
	} {
		assert.Nil(t, claude.Adapter{}.Calls(line).Limit, line)
	}
}
