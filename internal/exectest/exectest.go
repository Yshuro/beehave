// Package exectest writes the executable files that tests start and changes the mode of test folders.
package exectest

import (
	"fmt"
	"os"
	"syscall"
)

const mode = 0o700

// ChmodFolder changes the mode of a folder and returns an error when the path is not a folder.
func ChmodFolder(path string, folderMode os.FileMode) error {
	info, err := os.Stat(path)
	if err != nil {
		return err
	}

	if !info.IsDir() {
		return fmt.Errorf("%s is not a folder", path)
	}

	return os.Chmod(path, folderMode)
}

// Write writes a program that the test starts later and returns the error of the write.
//
// Linux refuses to start a file that any process holds open for writing ("text file busy"). Go opens a file with close-on-exec,
// but a child that another goroutine forks while the file is open keeps the descriptor until its own exec. Fork holds the write
// side of syscall.ForkLock, so the read side held from the open to the close keeps every fork out of that span.
func Write(path, text string) error {
	syscall.ForkLock.RLock()
	defer syscall.ForkLock.RUnlock()

	return os.WriteFile(path, []byte(text), mode)
}
