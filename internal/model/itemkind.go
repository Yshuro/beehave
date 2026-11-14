package model

import "strings"

// The kinds of work item an id can name.
const (
	KindForge = "gh"
	KindBeads = "bd"
	// KindRound is the kind of the item that a round starts as: round/<base>. The base is a branch name and holds "/".
	KindRound = "round"
)

// RoundPrefix starts the id of the item of a round.
const RoundPrefix = KindRound + "/"

const (
	idSeparator = '-'
	underscore  = '_'
)

// ItemKind returns KindForge for a forge number, KindBeads for a beads id, KindRound for a round id and "" for none of them.
func ItemKind(item string) string {
	if IsDigits(item) {
		return KindForge
	}

	if base, found := strings.CutPrefix(item, RoundPrefix); found {
		if ValidBase(base) {
			return KindRound
		}

		return ""
	}

	prefix, rest, found := strings.Cut(item, string(idSeparator))
	if !found || !validPrefix(prefix) || rest == "" || strings.IndexFunc(rest, notIDRune) >= 0 {
		return ""
	}

	return KindBeads
}

func validPrefix(prefix string) bool {
	if prefix == "" || strings.IndexFunc(prefix[:1], NotAlnum) >= 0 {
		return false
	}

	return strings.IndexFunc(prefix, func(r rune) bool { return NotAlnum(r) && r != underscore }) < 0
}

// NotAlnum reports whether r is not an ASCII letter or digit.
func NotAlnum(r rune) bool {
	return (r < 'a' || r > 'z') && (r < 'A' || r > 'Z') && (r < '0' || r > '9')
}

func notIDRune(r rune) bool {
	return NotAlnum(r) && r != underscore && r != '.' && r != idSeparator
}

// ValidBase reports whether text is the name of a round branch: segments of letters, digits, "_", "." and "-" joined by "/".
// Each segment starts with a letter or a digit, so the name holds no "..", no leading or trailing "/" and no empty segment.
func ValidBase(text string) bool {
	if strings.Contains(text, "..") {
		return false
	}

	for segment := range strings.SplitSeq(text, "/") {
		if segment == "" || strings.IndexFunc(segment[:1], NotAlnum) >= 0 || strings.IndexFunc(segment, notIDRune) >= 0 {
			return false
		}
	}

	return true
}
