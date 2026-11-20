package main

import (
	"context"
	"os"
	"os/exec"
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

// A static git loads no library, so it reads no library and no cache; a file that is no ELF program gives no read set.
func TestGitLibraries_AStaticGitReadsNoLibraryAndAFileThatIsNoProgramIsAnError(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	source, static := filepath.Join(dir, "main.go"), filepath.Join(dir, "static")
	require.NoError(t, os.WriteFile(source, []byte("package main\n\nfunc main() {}\n"), 0o600))

	// A program that links no C code is static whatever CGO_ENABLED says.
	build := exec.CommandContext(t.Context(), "go", "build", "-o", static, source) //nolint:gosec // the paths are the temporary folder of the test
	out, err := build.CombinedOutput()
	require.NoError(t, err, string(out))

	found, err := gitLibraries(t.Context(), static)
	require.NoError(t, err)
	assert.Empty(t, found)

	_, err = gitLibraries(t.Context(), source)
	assert.ErrorContains(t, err, "read the program")
}

// A dynamic git reads the dynamic linker that its program names, and the cache of the linker.
func TestGitLibraries_ADynamicProgramReadsItsLinkerAndTheCache(t *testing.T) {
	t.Parallel()

	found, err := gitLibraries(t.Context(), "/bin/sh")
	require.NoError(t, err)
	assert.Contains(t, found, linkerCache)
	assert.NotEmpty(t, found[:len(found)-1], "the linker and the libraries")
}

// A cache of the linker that cannot be read, other than one that does not exist, gives no read set.
func TestLddLibraries_ACacheThatCannotBeStatedIsAnError(t *testing.T) {
	t.Parallel()

	file := filepath.Join(t.TempDir(), "file")
	require.NoError(t, os.WriteFile(file, nil, 0o600))

	_, err := lddLibraries("", filepath.Join(file, "ld.so.cache"))
	assert.ErrorContains(t, err, "read the cache of the linker")
}

// An ldd that cannot run gives no read set.
func TestGitLibraries_AnLddThatCannotRunIsAnError(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithCancel(t.Context())
	cancel()

	_, err := gitLibraries(ctx, "/bin/sh")
	assert.ErrorContains(t, err, "run ldd")
}
