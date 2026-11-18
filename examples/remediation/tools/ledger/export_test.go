package ledger

import "github.com/gofrs/flock"

// WaitAtMost sets how many milliseconds the ledger waits for a lock, so a test of a locked file does not wait 30 seconds.
func WaitAtMost(l *Ledger, ms int) { l.busyMs = ms }

// FreeWith makes the ledger free its lock file through free after a write. The test frees the real lock first, so no lock stays held.
func FreeWith(l *Ledger, free func(turn *flock.Flock) error) { l.free = free }

// Touch makes the ledger file if it is missing, as the first step of a write does.
func Touch(l *Ledger) error { return l.touch() }

// Fail returns the refusal that the ledger makes for an error of a verb.
func Fail(l *Ledger, verb string, err error) error { return l.fail(verb, err) }
