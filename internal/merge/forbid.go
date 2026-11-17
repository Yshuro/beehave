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

// matchingRule returns the position of the first rule that a line of the text matches, and -1 when no line matches.
// It splits the text on newlines and tests each line. It is the one test of the merge guard, of the merge result and, through ForbiddenBy, of the posting steps.
func matchingRule(rules []*regexp.Regexp, text string) int {
	for line := range strings.SplitSeq(text, "\n") {
		if i := slices.IndexFunc(rules, func(rule *regexp.Regexp) bool { return rule.MatchString(line) }); i >= 0 {
			return i
		}
	}

	return -1
}

// ForbiddenBy returns the first rule that a line of the text matches, and false when no line matches.
func ForbiddenBy(rules []*regexp.Regexp, text string) (*regexp.Regexp, bool) {
	if i := matchingRule(rules, text); i >= 0 {
		return rules[i], true
	}

	return nil, false
}

// patternWord names a pattern by its position in forbid_text, counted from 1, and never by its source.
func patternWord(i int) string {
	return fmt.Sprintf("pattern %d of forbid_text", i+1)
}

// withheld returns the lines with each line that a rule matches replaced by a line that names the rule and not the text.
// A line that holds a newline is replaced whole when a rule matches one of its parts, as the posting steps test it.
func withheld(rules []*regexp.Regexp, lines []string) []string {
	out := make([]string, len(lines))

	for n, line := range lines {
		out[n] = line
		if i := matchingRule(rules, line); i >= 0 {
			out[n] = "(a line is left out: it matches " + patternWord(i) + ")"
		}
	}

	return out
}
