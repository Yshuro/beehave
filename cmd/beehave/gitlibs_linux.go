package main

import (
	"bytes"
	"context"
	"debug/elf"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// linkerCache is the list of library folders that the dynamic linker reads to find a library by its name.
const linkerCache = "/etc/ld.so.cache"

// gitLibraries returns the dynamic linker that the program at binary names and the libraries that it loads, with their links resolved, and the cache of
// the linker when the machine has one. The linker lists the libraries itself (--list, what ldd runs), by its path and with no environment, so no search
// path and no LD_LIBRARY_PATH plays a part. A static program loads none of them. A file that is no ELF program, a library that the linker cannot find,
// or a linker that cannot run, is an error.
func gitLibraries(ctx context.Context, binary string) ([]string, error) {
	interpreter, err := programInterpreter(binary)
	if err != nil || interpreter == "" {
		return nil, err
	}

	list := exec.CommandContext(ctx, interpreter, "--list", binary) //nolint:gosec // the linker that the git of the engine names
	list.Env = []string{}

	out, err := list.Output()
	if err != nil {
		return nil, fmt.Errorf("list the libraries with %s: %w", interpreter, err)
	}

	return lddLibraries(string(out), linkerCache)
}

// programInterpreter returns the dynamic linker that the ELF program at path names, or "" for a static program, which loads no library.
func programInterpreter(path string) (string, error) {
	file, err := elf.Open(path)
	if err != nil {
		return "", fmt.Errorf("read the program %s: %w", path, err)
	}
	defer file.Close()

	for _, header := range file.Progs {
		if header.Type == elf.PT_INTERP {
			name, err := io.ReadAll(header.Open())

			return string(bytes.TrimRight(name, "\x00")), err
		}
	}

	return "", nil
}

// lddLibraries returns the file of each library in a list of the dynamic linker (the lines that ldd prints), and cache when it exists. A line with no
// path, such as the vDSO, names no file.
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

	return withCache(found, cache)
}

// withCache returns found with cache added when it exists. A cache that cannot be read for another reason than that it does not exist is an error.
func withCache(found []string, cache string) ([]string, error) {
	_, err := os.Stat(cache)
	if err == nil {
		return append(found, cache), nil
	}

	if !errors.Is(err, fs.ErrNotExist) {
		return nil, fmt.Errorf("read the cache of the linker: %w", err)
	}

	return found, nil
}

// lddPath returns the file of one line of the list, "name => /path (address)" or "/path (address)", with its links resolved. A line of a library that
// the linker did not find, or whose file is gone, is an error.
func lddPath(line string) (string, error) {
	if _, after, ok := strings.Cut(line, "=>"); ok {
		line = strings.TrimSpace(after)
		if strings.HasPrefix(line, "not found") {
			return "", fmt.Errorf("the dynamic linker finds no file for a library of git: %s", line)
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
