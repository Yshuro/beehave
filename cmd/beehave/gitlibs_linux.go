package main

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// linkerCache is the list of library folders that the dynamic linker reads to find a library by its name.
const linkerCache = "/etc/ld.so.cache"

// gitLibraries returns the dynamic linker and the libraries that ldd reports for the program at binary, with their links resolved, and the cache of the
// linker when the machine has one. A library that ldd cannot find, or no ldd, is an error.
func gitLibraries(ctx context.Context, binary string) ([]string, error) {
	out, err := exec.CommandContext(ctx, "ldd", binary).Output() //nolint:gosec // the git that the search path of the engine names
	if err != nil {
		return nil, fmt.Errorf("run ldd: %w", err)
	}

	return lddLibraries(string(out), linkerCache)
}

// lddLibraries returns the file of each library in the output of ldd, and cache when it exists. A line with no path, such as the vDSO, names no
// file.
func lddLibraries(out, cache string) ([]string, error) {
	var found []string

	for line := range strings.SplitSeq(out, "\n") {
		path, err := lddPath(strings.TrimSpace(line))
		if err != nil {
			return nil, err
		}

		if path != "" {
			found = append(found, path)
		}
	}

	if _, err := os.Stat(cache); err == nil {
		found = append(found, cache)
	}

	return found, nil
}

// lddPath returns the file of one line of ldd, "name => /path (address)" or "/path (address)", with its links resolved. A line of a library that ldd did
// not find, or whose file is gone, is an error.
func lddPath(line string) (string, error) {
	if _, after, ok := strings.Cut(line, "=>"); ok {
		line = strings.TrimSpace(after)
		if strings.HasPrefix(line, "not found") {
			return "", fmt.Errorf("ldd finds no file for a library of git: %s", line)
		}
	}

	path, _, _ := strings.Cut(line, " ")
	if !filepath.IsAbs(path) {
		return "", nil
	}

	resolved, err := filepath.EvalSymlinks(path)
	if err != nil {
		return "", fmt.Errorf("find the library %s: %w", path, err)
	}

	return resolved, nil
}
