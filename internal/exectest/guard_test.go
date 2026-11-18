package exectest_test

import (
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const (
	execBits   = 0o111
	repoRoot   = "../.."
	modeArgOf  = 2
	openModeAt = 2
)

// bypasses returns "file:line" for each call in the Go files of root that writes a file with an executable mode and does not use exectest.Write.
func bypasses(t *testing.T, root string) []string {
	t.Helper()

	var found []string

	consts := map[string]map[string]int64{}

	require.NoError(t, filepath.WalkDir(root, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}

		if entry.IsDir() && (entry.Name() == ".git" || entry.Name() == "node_modules") {
			return filepath.SkipDir
		}

		if entry.IsDir() || !strings.HasSuffix(path, "_test.go") {
			return nil
		}

		dir := filepath.Dir(path)
		if consts[dir] == nil {
			consts[dir] = packageConsts(t, dir)
		}

		found = append(found, bypassesIn(t, path, consts[dir])...)

		return nil
	}))

	return found
}

func bypassesIn(t *testing.T, path string, consts map[string]int64) []string {
	t.Helper()

	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, path, nil, 0)
	require.NoError(t, err)

	var found []string

	ast.Inspect(file, func(node ast.Node) bool {
		call, ok := node.(*ast.CallExpr)
		if !ok {
			return true
		}

		if mode, ok := writeMode(call, consts); ok && mode&execBits != 0 {
			found = append(found, fset.Position(call.Pos()).String())
		}

		return true
	})

	return found
}

// writeMode returns the file mode that a WriteFile or OpenFile call creates a file with, when the mode is a literal or a constant of the package.
func writeMode(call *ast.CallExpr, consts map[string]int64) (int64, bool) {
	selector, ok := call.Fun.(*ast.SelectorExpr)
	if !ok {
		return 0, false
	}

	switch selector.Sel.Name {
	case "WriteFile":
		if len(call.Args) == modeArgOf+1 {
			return constant(call.Args[modeArgOf], consts)
		}
	case "OpenFile":
		if len(call.Args) == openModeAt+1 {
			return constant(call.Args[openModeAt], consts)
		}
	}

	return 0, false
}

func constant(expr ast.Expr, consts map[string]int64) (int64, bool) {
	switch value := expr.(type) {
	case *ast.BasicLit:
		number, err := strconv.ParseInt(value.Value, 0, 64)

		return number, err == nil
	case *ast.Ident:
		number, ok := consts[value.Name]

		return number, ok
	case *ast.ParenExpr:
		return constant(value.X, consts)
	}

	return 0, false
}

// packageConsts returns the integer constants that the test files of one folder declare at the top level.
func packageConsts(t *testing.T, dir string) map[string]int64 {
	t.Helper()

	files, err := filepath.Glob(filepath.Join(dir, "*.go"))
	require.NoError(t, err)

	consts := map[string]int64{}

	for _, path := range files {
		file, err := parser.ParseFile(token.NewFileSet(), path, nil, 0)
		require.NoError(t, err)

		for _, decl := range file.Decls {
			if gen, ok := decl.(*ast.GenDecl); ok && gen.Tok == token.CONST {
				addConsts(gen, consts)
			}
		}
	}

	return consts
}

func addConsts(gen *ast.GenDecl, consts map[string]int64) {
	for _, spec := range gen.Specs {
		values, ok := spec.(*ast.ValueSpec)
		if !ok {
			continue
		}

		for i, name := range values.Names {
			if i >= len(values.Values) {
				continue
			}

			if number, ok := constant(values.Values[i], consts); ok {
				consts[name.Name] = number
			}
		}
	}
}

func TestTests_NeverWriteAProgramWithoutTheForkLock(t *testing.T) {
	t.Parallel()

	assert.Empty(t, bypasses(t, repoRoot), "a test that writes a file it starts uses exectest.Write: a plain write fails with text file busy when another test forks")
}

func TestGuard_FlagsAPlainWriteOfAnExecutableFile(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	source := "package x\n\nconst toolMode = 0o700\n\nfunc f() {\n\tos.WriteFile(\"a\", nil, 0o755)\n\tos.WriteFile(\"b\", nil, toolMode)\n\troot.WriteFile(\"c\", nil, 0o500)\n\tos.OpenFile(\"d\", 0, 0o700)\n\tos.WriteFile(\"e\", nil, 0o600)\n\tos.OpenFile(\"f\", 0, 0o644)\n\tos.WriteFile(\"g\", nil, mode)\n\texectest.Write(\"h\", \"\")\n}\n"
	writeSource(t, filepath.Join(dir, "x_test.go"), source)

	found := bypasses(t, dir)

	require.Len(t, found, 4)

	for i, line := range []string{"6:", "7:", "8:", "9:"} {
		assert.Contains(t, found[i], "x_test.go:"+line)
	}
}

func writeSource(t *testing.T, path, text string) {
	t.Helper()

	require.NoError(t, os.WriteFile(path, []byte(text), 0o600))
}
