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
	execBits = 0o111
	repoRoot = "../.."
	modeArg  = 2
	argCount = 3

	chmodArgs = 2
)

// bypasses returns "file:line" for each call in the test files of root that can write a program without exectest.Write.
func bypasses(t *testing.T, root string) []string {
	t.Helper()

	var found []string

	consts := map[string]map[string]bool{}

	require.NoError(t, filepath.WalkDir(root, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}

		if entry.IsDir() && (entry.Name() == ".git" || entry.Name() == "node_modules") {
			return filepath.SkipDir
		}

		if entry.IsDir() || !strings.HasSuffix(path, "_test.go") || filepath.Base(filepath.Dir(path)) == "exectest" {
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

// packageConsts returns the plain mode constants that the Go files of one folder declare.
func packageConsts(t *testing.T, dir string) map[string]bool {
	t.Helper()

	files, err := filepath.Glob(filepath.Join(dir, "*.go"))
	require.NoError(t, err)

	plain := map[string]bool{}

	for _, path := range files {
		file, err := parser.ParseFile(token.NewFileSet(), path, nil, 0)
		require.NoError(t, err)
		plainConsts(file, plain)
	}

	return plain
}

func bypassesIn(t *testing.T, path string, consts map[string]bool) []string {
	t.Helper()

	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, path, nil, parser.ParseComments)
	require.NoError(t, err)

	var found []string

	ast.Inspect(file, func(node ast.Node) bool {
		call, ok := node.(*ast.CallExpr)
		if ok && bypasses1(call, consts) {
			found = append(found, fset.Position(call.Pos()).String())
		}

		return true
	})

	return found
}

// bypasses1 reports whether one call writes or changes a file in a way that can make a program.
func bypasses1(call *ast.CallExpr, consts map[string]bool) bool {
	selector, ok := call.Fun.(*ast.SelectorExpr)
	if !ok {
		return false
	}

	switch selector.Sel.Name {
	case "WriteFile", "OpenFile":
		return len(call.Args) == argCount && !plainMode(call.Args[modeArg], consts)
	case "Chmod":
		return len(call.Args) != chmodArgs || !plainMode(call.Args[1], consts)
	}

	return false
}

// plainMode reports whether a mode is a plain literal, or the name of a constant that the package declares as one.
func plainMode(expr ast.Expr, consts map[string]bool) bool {
	if ident, ok := expr.(*ast.Ident); ok {
		return consts[ident.Name]
	}

	return plainLiteral(expr)
}

// plainLiteral reports whether an expression is an integer literal with no execute bit, alone or in a FileMode conversion.
func plainLiteral(expr ast.Expr) bool {
	switch value := expr.(type) {
	case *ast.BasicLit:
		number, err := strconv.ParseInt(value.Value, 0, 64)

		return value.Kind == token.INT && err == nil && number&execBits == 0
	case *ast.CallExpr:
		selector, ok := value.Fun.(*ast.SelectorExpr)

		return ok && selector.Sel.Name == "FileMode" && len(value.Args) == 1 && plainLiteral(value.Args[0])
	}

	return false
}

// plainConsts returns the names of the constants that a file declares as an integer literal with no execute bit.
func plainConsts(file *ast.File, into map[string]bool) {
	for _, decl := range file.Decls {
		gen, ok := decl.(*ast.GenDecl)
		if !ok || gen.Tok != token.CONST {
			continue
		}

		for _, spec := range gen.Specs {
			plainSpec(spec, into)
		}
	}
}

func plainSpec(spec ast.Spec, into map[string]bool) {
	values, ok := spec.(*ast.ValueSpec)
	if !ok || len(values.Names) != len(values.Values) {
		return
	}

	for i, name := range values.Names {
		if plainLiteral(values.Values[i]) {
			into[name.Name] = true
		}
	}
}

func TestTests_NeverWriteAProgramWithoutTheForkLock(t *testing.T) {
	t.Parallel()

	assert.Empty(t, bypasses(t, repoRoot), "a test that writes a file it starts uses exectest.Write: a plain write fails with text file busy when another test forks")
}

func TestGuard_FlagsEveryWayToWriteAProgramWithoutTheHelper(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	source := `package x

const (
	plain = os.FileMode(0o600)
	exec  = os.FileMode(0o700)
)

func f() {
	os.WriteFile("a", nil, 0o755)
	os.WriteFile("b", nil, mode)
	root.WriteFile("c", nil, os.FileMode(0o700))
	os.OpenFile("d", 0, 0o600|0o100)
	os.Chmod("e", 0o700)
	root.Chmod("f", mode)
	os.Chmod("g", 0o700) // folder
	os.WriteFile("h", nil, exec)
	os.Chmod("i", os.FileMode(0o500))
	os.WriteFile("j", nil, 0o600)
	os.OpenFile("k", 0, 0o644)
	os.Chmod("l", 0o600)
	os.WriteFile("m", nil, os.FileMode(0o600))
	os.WriteFile("n", nil, plain)
	exectest.Write("o", "")
	exectest.ChmodFolder("p", 0o700)
}
`
	writeSource(t, filepath.Join(dir, "x_test.go"), source)

	found := bypasses(t, dir)

	require.Len(t, found, 9)

	for i, line := range []string{"9:", "10:", "11:", "12:", "13:", "14:", "15:", "16:", "17:"} {
		assert.Contains(t, found[i], "x_test.go:"+line)
	}
}

func writeSource(t *testing.T, path, text string) {
	t.Helper()

	require.NoError(t, os.WriteFile(path, []byte(text), 0o600))
}
