package shell

import (
	"fmt"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
)

// doublingScript sets a chain of variables that each double the one before, so the last one is near the size cap, and uses the last one n times.
func doublingScript(n int) string {
	var script strings.Builder

	script.WriteString("C='ssh h $C'\nV0=\"$C $C\"\n")

	for i := 1; i < 8; i++ {
		fmt.Fprintf(&script, "V%d=\"$V%d $V%d\"\n", i, i-1, i-1)
	}

	script.WriteString(strings.Repeat("$V7 a b\n", n))

	return script.String()
}

// readsOf returns how many shell strings a scan of the script reads behind a wrapper, and how many findings it returns.
func readsOf(script string) (int, int) {
	s := newScanner()
	s.prepass(script)
	s.scanZsh(script)
	s.scan(script, false)

	return s.reads, len(dedupe(s.findings))
}

// The scan reads the remote text of the last variable once, however many times the script uses it.
// The test counts the reads, so the result does not depend on the speed of the host.
// Before the fix, each use read the same text again and the count grew with the uses.
func TestScanUsesOfADoublingChainCostOneRead(t *testing.T) {
	t.Parallel()

	one, _ := readsOf(doublingScript(1))
	many, findings := readsOf(doublingScript(3000))

	assert.Zero(t, findings)
	assert.Positive(t, one, "the scan reads the remote text")
	assert.Equal(t, one, many, "3000 uses read the same texts as one use")
}
