package flow

import (
	"bufio"
	"fmt"
	"maps"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.starlark.net/syntax"

	"github.com/tobocop2/beehave/internal/model"
	"github.com/tobocop2/beehave/internal/workflow"
)

// contractUnreached is the exception of the rule that a use counts only when an entry of a workflow reaches it. foreign_followup of the remediation example
// calls pr_state and files its item with the keyword shared, and no entry calls foreign_followup while the engine refuses every foreign pull request.
// The follow-up of a foreign review belongs to #438.
// The test fails when an entry reaches the pair, so the exception ends with #438.
var contractUnreached = map[contractPair]string{
	{"pr_state", noKeyword}:   "#438",
	{"pr_state", "name"}:      "#438",
	{"pr_state", "pr"}:        "#438",
	{"item_create", "shared"}: "#438",
}

// contractLibrary is the label of the embedded library in a load, and the folder that holds it.
const (
	contractLibraryLabel  = "@beehave//"
	contractLibraryFolder = "internal/workflow/lib"
)

// contractParsed is one parsed Starlark file of examples/ or of the library. rel is its path from the root of the repository.
type contractParsed struct {
	rel  string
	file *syntax.File
}

// contractFunc is a function of a file: the path of the file and the name.
type contractFunc struct{ rel, name string }

// contractLoadTarget is the path of the file that a load names, seen from the file that loads it.
func contractLoadTarget(from, label string) string {
	if rest, ok := strings.CutPrefix(label, contractLibraryLabel); ok {
		return path.Join(contractLibraryFolder, rest)
	}

	return path.Join(path.Dir(from), strings.TrimPrefix(label, "//"))
}

// contractGraph is the functions of the files, the names that the files load, and the functions that an entry reaches so far.
type contractGraph struct {
	defs    map[contractFunc]*syntax.DefStmt
	loads   map[contractFunc]contractFunc
	reached map[contractFunc]bool
	queue   []contractFunc
}

// newContractGraph indexes the functions and the loads of the files.
func newContractGraph(files []contractParsed) *contractGraph {
	g := &contractGraph{defs: map[contractFunc]*syntax.DefStmt{}, loads: map[contractFunc]contractFunc{}, reached: map[contractFunc]bool{}}

	for _, parsed := range files {
		for _, stmt := range parsed.file.Stmts {
			switch s := stmt.(type) {
			case *syntax.DefStmt:
				g.defs[contractFunc{parsed.rel, s.Name.Name}] = s
			case *syntax.LoadStmt:
				target := contractLoadTarget(parsed.rel, s.ModuleName())
				for i, to := range s.To {
					g.loads[contractFunc{parsed.rel, to.Name}] = contractFunc{target, s.From[i].Name}
				}
			}
		}
	}

	return g
}

// reach marks the function of a name in a file as reached. A name that the file loads leads to the function of the file that the load names.
func (g *contractGraph) reach(fn contractFunc) {
	if next, isLoad := g.loads[fn]; isLoad {
		fn = next
	}

	if _, isDef := g.defs[fn]; isDef && !g.reached[fn] {
		g.reached[fn] = true
		g.queue = append(g.queue, fn)
	}
}

// visit reaches every function that a name in the node of a file refers to.
func (g *contractGraph) visit(rel string, node syntax.Node) {
	syntax.Walk(node, func(n syntax.Node) bool {
		if ident, ok := n.(*syntax.Ident); ok {
			g.reach(contractFunc{rel, ident.Name})
		}

		return true
	})
}

// drain visits the body of each reached function until no new function is reached.
func (g *contractGraph) drain() {
	for len(g.queue) > 0 {
		fn := g.queue[0]
		g.queue = g.queue[1:]

		for _, stmt := range g.defs[fn].Body {
			g.visit(fn.rel, stmt)
		}
	}
}

// contractStartsChildren is true when a call in the file passes the keyword entry.
func contractStartsChildren(file *syntax.File) bool {
	found := false

	syntax.Walk(file, func(n syntax.Node) bool {
		if bin, ok := n.(*syntax.BinaryExpr); ok && bin.Op == syntax.EQ {
			if name, isIdent := bin.X.(*syntax.Ident); isIdent && name.Name == "entry" {
				found = true
			}
		}

		return !found
	})

	return found
}

// reachStrings reaches the function of each string of the file that is the name of a function of the file. The strings of a load are no names of functions.
func (g *contractGraph) reachStrings(parsed contractParsed) {
	syntax.Walk(parsed.file, func(n syntax.Node) bool {
		if _, isLoad := n.(*syntax.LoadStmt); isLoad {
			return false
		}

		if lit, ok := n.(*syntax.Literal); ok && lit.Token == syntax.STRING {
			g.reach(contractFunc{parsed.rel, lit.Value.(string)}) //nolint:forcetypeassert,errcheck // a string literal holds a string
		}

		return true
	})
}

// contractReach is the functions that an entry reaches. The entries are the code at the top of every file, a function named main, a function named finish,
// and each function that a file names in a string in a file that calls something with `entry`. A function is reached when its name appears in code that is reached,
// in a call or as a value. A name that a file loads leads to the function of the file that the load names, and nothing else.
func contractReach(files []contractParsed) map[contractFunc]bool {
	g := newContractGraph(files)

	for _, parsed := range files {
		for _, stmt := range parsed.file.Stmts {
			switch stmt.(type) {
			case *syntax.DefStmt, *syntax.LoadStmt:
			default:
				g.visit(parsed.rel, stmt)
			}
		}

		// The engine calls main, and calls finish after main when a file defines it.
		g.reach(contractFunc{parsed.rel, "main"})
		g.reach(contractFunc{parsed.rel, "finish"})

		if contractStartsChildren(parsed.file) {
			g.reachStrings(parsed)
		}
	}

	g.drain()

	return g.reached
}

// contractParse parses the Starlark files of the repository, with the path of each from the root.
func contractParse(t *testing.T, repo string) []contractParsed {
	t.Helper()

	var files []contractParsed

	for _, found := range contractFiles(t, repo) {
		file, err := model.StarlarkFileOptions().Parse(found, nil, 0)
		require.NoError(t, err, "parse "+found)

		rel, err := filepath.Rel(repo, found)
		require.NoError(t, err)

		files = append(files, contractParsed{filepath.ToSlash(rel), file})
	}

	return files
}

// contractWalkReached records the calls of the builtins of the code in the files. The first result holds the calls that an entry reaches, the second all calls.
func contractWalkReached(t *testing.T, repo string, code map[contractPair]bool, order map[string][]string) (reached, every contractUses) {
	t.Helper()

	files := contractParse(t, repo)
	names := contractReach(files)
	reached, every = contractUses{}, contractUses{}

	for _, parsed := range files {
		splats := contractSplats(parsed.file)

		for _, stmt := range parsed.file.Stmts {
			into := []contractUses{every}
			if def, isDef := stmt.(*syntax.DefStmt); !isDef || names[contractFunc{parsed.rel, def.Name.Name}] {
				into = append(into, reached)
			}

			contractCallsIn(stmt, parsed.rel, code, order, splats, into)
		}
	}

	return reached, every
}

// contractCallsIn records each call in the statement in each of the uses.
func contractCallsIn(stmt syntax.Stmt, rel string, code map[contractPair]bool, order map[string][]string, splats map[string][]string, into []contractUses) {
	syntax.Walk(stmt, func(n syntax.Node) bool {
		if call, ok := n.(*syntax.CallExpr); ok {
			for _, uses := range into {
				contractCall(call, rel, code, order, splats, uses)
			}
		}

		return true
	})
}

// contractUseCell is the text of the Use column for the files of a pair: the count, then the first two files, then the count of the others.
func contractUseCell(files map[string]bool) string {
	names := slices.Sorted(maps.Keys(files))

	shown := make([]string, 0, 2)
	for _, name := range names[:min(2, len(names))] {
		shown = append(shown, "`"+name+"`")
	}

	cell := fmt.Sprintf("%d: %s", len(names), strings.Join(shown, ", "))
	if len(names) > 2 {
		cell += fmt.Sprintf(" and %d more", len(names)-2)
	}

	return cell
}

// contractCells reads the Change and Use cells of the rows of the target table.
func contractCells(t *testing.T, file string) map[contractPair][2]string {
	t.Helper()

	cells := map[contractPair][2]string{}

	rows := contractLines(t, file,
		func(line string) bool { return strings.Contains(line, "<!-- contract:target -->") },
		func(line string) bool { return strings.Contains(line, "<!-- /contract:target -->") })

	for _, line := range rows {
		parts := strings.Split(line, "|")
		require.GreaterOrEqual(t, len(parts), 6, line)
		cells[contractPair{strings.Trim(strings.TrimSpace(parts[1]), "`"), strings.Trim(strings.TrimSpace(parts[2]), "`")}] = [2]string{strings.TrimSpace(parts[3]), strings.TrimSpace(parts[4])}
	}

	return cells
}

// contractLines is the table rows of the file between the first line for which start is true and the next line for which stop is true.
func contractLines(t *testing.T, file string, start, stop func(string) bool) []string {
	t.Helper()

	f, err := os.Open(file) //nolint:gosec // the path is the contract of this repository
	require.NoError(t, err)

	defer func() { require.NoError(t, f.Close()) }()

	var lines []string

	inside := false
	scanner := bufio.NewScanner(f)

	for scanner.Scan() {
		line := scanner.Text()

		switch {
		case !inside && start(line):
			inside = true
		case inside && stop(line):
			inside = false
		case inside && strings.HasPrefix(line, "| `"):
			lines = append(lines, line)
		}
	}

	require.NoError(t, scanner.Err())

	return lines
}

var contractChange = regexp.MustCompile("^(unchanged|#[0-9]+: .+)$")

func TestExamples_TheUseAndChangeColumnsOfTheContractMatchTheCode(t *testing.T) {
	t.Parallel()

	code, order := contractCode()
	_, every := contractWalkReached(t, contractRepo, code, order)
	cells := contractCells(t, filepath.Join(contractRepo, contractDoc))

	for p := range code {
		want := contractUseCell(every[p])

		assert.Equal(t, want, cells[p][1], "%s: the Use column of %s does not match the calls of examples/ and the library", p, contractDoc)
		assert.Regexp(t, contractChange, cells[p][0], "%s: the Change column holds `unchanged` or `#<number>: <text>`", p)
	}
}

func TestExamples_TheFactsTableOfTheContractHoldsTheFieldsOfTheFactsStruct(t *testing.T) {
	t.Parallel()

	var table []string

	rows := contractLines(t, filepath.Join(contractRepo, contractDoc),
		func(line string) bool { return strings.HasPrefix(line, "## The fields of `facts()`") },
		func(line string) bool { return strings.HasPrefix(line, "## ") })

	for _, line := range rows {
		table = append(table, strings.Trim(strings.TrimSpace(strings.Split(line, "|")[1]), "`"))
	}

	checked, ok := workflow.ResultFields("facts")
	require.True(t, ok, "the check of workflow files knows the fields of facts")
	assert.Len(t, table, 10, "the contract lists the ten fields of facts")

	built := slices.Sorted(maps.Keys(factsOutputs(model.Project{})))

	assert.Equal(t, slices.Sorted(slices.Values(checked)), slices.Sorted(slices.Values(table)), "the facts table and the fields that the check of workflow files knows")
	assert.Equal(t, built, slices.Sorted(slices.Values(table)), "the facts table and the fields that facts returns")
}

func TestExamples_AUseCountsOnlyWhenAnEntryReachesIt(t *testing.T) {
	t.Parallel()

	code, order := contractCode()
	reached, every := contractWalkReached(t, contractRepo, code, order)

	for p := range code {
		if _, exempt := contractUnreached[p]; exempt {
			assert.Empty(t, reached[p], "%s: an entry reaches the pair, so it is no exception of %s any more. Remove it from contractUnreached", p, contractUnreached[p])
			assert.NotEmpty(t, every[p], "%s: the exception has no call at all", p)

			continue
		}

		assert.NotEmpty(t, reached[p], "%s: only code that no entry calls uses it. Call it from an entry, or remove it", p)
	}
}

func TestContractReach_OnlyACallFromAnEntryReachesAFunction(t *testing.T) {
	t.Parallel()

	parse := func(src string) *syntax.File {
		file, err := model.StarlarkFileOptions().Parse("f.star", src, 0)
		require.NoError(t, err)

		return file
	}

	main := parse("load(\"//lib.star\", \"helper\", other = \"shared\")\n\ndef main(item):\n    a()\n    helper()\n    spawn([], entry = \"child\")\n\ndef a():\n    pass\n\ndef child(item):\n    b()\n\n" +
		"def b():\n    pass\n\ndef lonely():\n    c()\n\ndef c():\n    pass\n\nTABLE = {\"x\": d}\n\ndef d():\n    pass\n\ndef shared():\n    pass\n")
	lib := parse("def helper():\n    pass\n\ndef shared():\n    pass\n\ndef main():\n    pass\n")

	reached := contractReach([]contractParsed{{"e/main.star", main}, {"e/lib.star", lib}})

	want := map[contractFunc]bool{
		{"e/main.star", "main"}: true, {"e/main.star", "a"}: true, {"e/main.star", "child"}: true, {"e/main.star", "b"}: true, {"e/main.star", "d"}: true,
		{"e/lib.star", "helper"}: true, {"e/lib.star", "main"}: true,
	}

	assert.Equal(t, want, reached, "a function that only another file names is not reached; a loaded name leads to the file that the load names")
}
