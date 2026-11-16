package merge

import (
	"fmt"
	"regexp"
	"slices"
	"strings"
)

// Compile compiles the forbid_text patterns of a step. A pattern that is not RE2 is an error that names the pattern.
func Compile(patterns []string) ([]*regexp.Regexp, error) {
	rules := make([]*regexp.Regexp, len(patterns))

	for i, pattern := range patterns {
		rule, err := regexp.Compile(pattern)
		if err != nil {
			return nil, fmt.Errorf("the forbid_text pattern %q is not valid: %w", pattern, err)
		}

		rules[i] = rule
	}

	return rules, nil
}

// matchingRule returns the position of the first rule that the line matches, and false when none matches. It is the one line test of the merge guard and of the posting steps.
func matchingRule(rules []*regexp.Regexp, line string) (int, bool) {
	i := slices.IndexFunc(rules, func(rule *regexp.Regexp) bool { return rule.MatchString(line) })

	return i, i >= 0
}

// ForbiddenBy returns the first rule that a line of the text matches, and false when no line matches.
func ForbiddenBy(rules []*regexp.Regexp, text string) (*regexp.Regexp, bool) {
	for _, line := range strings.Split(text, "\n") {
		if i, found := matchingRule(rules, line); found {
			return rules[i], true
		}
	}

	return nil, false
}

// patternWord names a pattern by its position in forbid_text, counted from 1, and never by its source.
func patternWord(i int) string {
	return fmt.Sprintf("pattern %d of forbid_text", i+1)
}

// withheld returns the lines with each line that a rule matches replaced by a line that names the rule and not the text.
func withheld(rules []*regexp.Regexp, lines []string) []string {
	out := make([]string, len(lines))

	for n, line := range lines {
		out[n] = line
		if i, found := matchingRule(rules, line); found {
			out[n] = "(a line is left out: it matches " + patternWord(i) + ")"
		}
	}

	return out
}
