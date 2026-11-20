package main

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Each library line of ldd gives its file with links resolved, the vDSO line names no file, and the cache of the linker comes last when it exists.
func TestLddLibraries_GivesTheFileOfEachLibraryWithItsLinksResolvedAndTheCache(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	lib, linker, cache := filepath.Join(dir, "libz.so.1.3"), filepath.Join(dir, "ld.so"), filepath.Join(dir, "ld.so.cache")

	for _, path := range []string{lib, linker, cache} {
		require.NoError(t, os.WriteFile(path, nil, 0o600))
	}

	require.NoError(t, os.Symlink(lib, filepath.Join(dir, "libz.so.1")))

	out := "\tlinux-vdso.so.1 (0x00007ffd)\n\tlibz.so.1 => " + filepath.Join(dir, "libz.so.1") + " (0x00007f00)\n\t" + linker + " (0x00007f01)\n"

	found, err := lddLibraries(out, cache)
	require.NoError(t, err)
	assert.Equal(t, []string{evaluated(t, lib), evaluated(t, linker), cache}, found)

	found, err = lddLibraries(out, filepath.Join(dir, "absent"))
	require.NoError(t, err)
	assert.Equal(t, []string{evaluated(t, lib), evaluated(t, linker)}, found, "a machine with no cache reads none")
}

// A library that ldd does not find, or a file that is gone, gives no read set.
func TestLddLibraries_ALibraryThatCannotBeFoundIsAnError(t *testing.T) {
	t.Parallel()

	for out, want := range map[string]string{
		"\tlibpcre2-8.so.0 => not found\n":        "ldd finds no file",
		"\t/nonexistent/libz.so.1 (0x00007f00)\n": "find the library /nonexistent/libz.so.1",
	} {
		_, err := lddLibraries(out, linkerCache)
		assert.ErrorContains(t, err, want, out)
	}
}
