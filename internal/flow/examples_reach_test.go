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

// contractScope is the code that a name is read in: a function (def is set) or a statement at the top of a file. bound holds the names that the code binds itself,
// its parameters, its assignments and its loop variables. A read of a bound name is a read of a local variable, so it reaches no function.
type contractScope struct {
	rel   string
	def   *syntax.DefStmt
	node  syntax.Node
	bound map[string]bool
}

// contractEntryParam is a function that starts a child run with a parameter as the entry: the child names are the strings that a call passes for the parameter.
type contractEntryParam struct {
	fn    contractFunc
	param string
}

// contractGraph is the functions of the files, the names that the files load, and the functions that an entry reaches so far.
type contractGraph struct {
	defs        map[contractFunc]*syntax.DefStmt
	loads       map[contractFunc]contractFunc
	consts      map[contractFunc]string
	loaded      map[string]bool
	calls       map[contractFunc][]*syntax.CallExpr
	entryParams []contractEntryParam
	reached     map[contractFunc]bool
	queue       []contractFunc
}

// newContractGraph indexes the functions, the string constants and the loads of the files.
func newContractGraph(files []contractParsed) *contractGraph {
	g := &contractGraph{
		defs: map[contractFunc]*syntax.DefStmt{}, loads: map[contractFunc]contractFunc{}, consts: map[contractFunc]string{}, loaded: map[string]bool{},
		calls: map[contractFunc][]*syntax.CallExpr{}, reached: map[contractFunc]bool{},
	}

	for _, parsed := range files {
		for _, stmt := range parsed.file.Stmts {
			g.index(parsed.rel, stmt)
		}
	}

	return g
}

// index records one top-level statement: a function, a load or a string constant.
func (g *contractGraph) index(rel string, stmt syntax.Stmt) {
	switch s := stmt.(type) {
	case *syntax.DefStmt:
		g.defs[contractFunc{rel, s.Name.Name}] = s
	case *syntax.LoadStmt:
		target := contractLoadTarget(rel, s.ModuleName())
		g.loaded[target] = true

		for i, to := range s.To {
			g.loads[contractFunc{rel, to.Name}] = contractFunc{target, s.From[i].Name}
		}
	case *syntax.AssignStmt:
		name, isName := s.LHS.(*syntax.Ident)
		text, isText := contractString(s.RHS)

		if s.Op == syntax.EQ && isName && isText {
			g.consts[contractFunc{rel, name.Name}] = text
		}
	}
}

// contractString is the text of a string literal.
func contractString(expr syntax.Expr) (string, bool) {
	lit, ok := expr.(*syntax.Literal)
	if !ok || lit.Token != syntax.STRING {
		return "", false
	}

	text, isText := lit.Value.(string)

	return text, isText
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

// contractTargets calls into for each name that a target of an assignment or a loop binds.
func contractTargets(target syntax.Expr, into func(*syntax.Ident)) {
	switch t := target.(type) {
	case *syntax.Ident:
		into(t)
	case *syntax.TupleExpr:
		for _, each := range t.List {
			contractTargets(each, into)
		}
	case *syntax.ListExpr:
		for _, each := range t.List {
			contractTargets(each, into)
		}
	case *syntax.ParenExpr:
		contractTargets(t.X, into)
	}
}

// contractParams calls into for each name that the parameters of a function or a lambda bind.
func contractParams(params []syntax.Expr, into func(*syntax.Ident)) {
	for _, param := range params {
		switch p := param.(type) {
		case *syntax.Ident:
			into(p)
		case *syntax.BinaryExpr:
			contractTargets(p.X, into)
		case *syntax.UnaryExpr:
			contractTargets(p.X, into)
		}
	}
}

// contractBound is the names that the code binds itself: the parameters of the function and everything that its body assigns, loops over or defines.
func contractBound(def *syntax.DefStmt) map[string]bool {
	bound := map[string]bool{}
	bind := func(id *syntax.Ident) { bound[id.Name] = true }

	if def == nil {
		return bound
	}

	contractParams(def.Params, bind)

	for _, stmt := range def.Body {
		syntax.Walk(stmt, func(n syntax.Node) bool {
			switch x := n.(type) {
			case *syntax.AssignStmt:
				contractTargets(x.LHS, bind)
			case *syntax.ForStmt:
				contractTargets(x.Vars, bind)
			case *syntax.ForClause:
				contractTargets(x.Vars, bind)
			case *syntax.DefStmt:
				bind(x.Name)
				contractParams(x.Params, bind)
			case *syntax.LambdaExpr:
				contractParams(x.Params, bind)
			}

			return true
		})
	}

	return bound
}

// visit reaches every function that code of a scope reads by name.
func (g *contractGraph) visit(scope contractScope) {
	g.scan(scope, scope.node)
}

// scan walks a node of the scope. A name counts when the code reads it as a value or calls it. A keyword of a call, an attribute, and the name that an assignment,
// a loop or a parameter binds are no reads, and a read of a name that the scope binds is the read of a local variable.
func (g *contractGraph) scan(scope contractScope, node syntax.Node) {
	syntax.Walk(node, func(n syntax.Node) bool {
		switch x := n.(type) {
		case *syntax.Ident:
			if !scope.bound[x.Name] {
				g.reach(contractFunc{scope.rel, x.Name})
			}
		case *syntax.DotExpr:
			g.scan(scope, x.X)

			return false
		case *syntax.BinaryExpr:
			if keyword, isKeyword := contractKeyword(x); isKeyword {
				g.entryOf(scope, keyword, x.Y)
				g.scan(scope, x.Y)

				return false
			}
		case *syntax.CallExpr:
			g.noteCall(scope, x)
		case *syntax.AssignStmt, *syntax.ForStmt, *syntax.ForClause:
			g.scanBinding(scope, x)

			return false
		}

		return true
	})
}

// scanBinding walks an assignment, a loop or a comprehension clause: the target binds names, and the other parts are reads.
func (g *contractGraph) scanBinding(scope contractScope, node syntax.Node) {
	switch x := node.(type) {
	case *syntax.AssignStmt:
		g.scanTarget(scope, x.LHS)
		g.scan(scope, x.RHS)
	case *syntax.ForStmt:
		g.scanTarget(scope, x.Vars)
		g.scan(scope, x.X)

		for _, stmt := range x.Body {
			g.scan(scope, stmt)
		}
	case *syntax.ForClause:
		g.scanTarget(scope, x.Vars)
		g.scan(scope, x.X)
	}
}

// contractKeyword is the name of a keyword argument, `name = value`.
func contractKeyword(bin *syntax.BinaryExpr) (string, bool) {
	name, isName := bin.X.(*syntax.Ident)
	if bin.Op != syntax.EQ || !isName {
		return "", false
	}

	return name.Name, true
}

// scanTarget reads the parts of a target that are reads: an index or an attribute holds names that the code reads. A plain name is bound, and no read.
func (g *contractGraph) scanTarget(scope contractScope, target syntax.Expr) {
	switch t := target.(type) {
	case *syntax.Ident:
	case *syntax.TupleExpr:
		for _, each := range t.List {
			g.scanTarget(scope, each)
		}
	case *syntax.ListExpr:
		for _, each := range t.List {
			g.scanTarget(scope, each)
		}
	case *syntax.ParenExpr:
		g.scanTarget(scope, t.X)
	default:
		g.scan(scope, target)
	}
}

// noteCall keeps a call of a function of the file, to read the strings that it passes for the entry of a child run.
func (g *contractGraph) noteCall(scope contractScope, call *syntax.CallExpr) {
	if name, isName := call.Fn.(*syntax.Ident); isName && !scope.bound[name.Name] {
		fn := contractFunc{scope.rel, name.Name}
		if next, isLoad := g.loads[fn]; isLoad {
			fn = next
		}

		g.calls[fn] = append(g.calls[fn], call)
	}
}

// entryOf reaches the child functions that the keyword entry of a call lists. A string names a function of the file. A parameter of the function that holds the call
// names the strings that a call of that function passes for it. Another name stands for the string constants of the file that the code reads.
func (g *contractGraph) entryOf(scope contractScope, keyword string, value syntax.Expr) {
	if keyword != "entry" {
		return
	}

	if text, isText := contractString(value); isText {
		g.reach(contractFunc{scope.rel, text})

		return
	}

	name, isName := value.(*syntax.Ident)
	if !isName {
		return
	}

	if scope.def != nil && contractIsParam(scope.def, name.Name) {
		g.entryParams = append(g.entryParams, contractEntryParam{contractFunc{scope.rel, scope.def.Name.Name}, name.Name})

		return
	}

	syntax.Walk(scope.node, func(n syntax.Node) bool {
		if id, isID := n.(*syntax.Ident); isID {
			if text, isConst := g.consts[contractFunc{scope.rel, id.Name}]; isConst {
				g.reach(contractFunc{scope.rel, text})
			}
		}

		return true
	})
}

// contractIsParam is true when the function has a parameter of the name.
func contractIsParam(def *syntax.DefStmt, name string) bool {
	found := false

	contractParams(def.Params, func(id *syntax.Ident) { found = found || id.Name == name })

	return found
}

// contractArgument is the argument that a call passes for a parameter: by keyword, or by position.
func contractArgument(call *syntax.CallExpr, def *syntax.DefStmt, param string) syntax.Expr {
	index := -1

	for i, each := range def.Params {
		if id, isID := each.(*syntax.Ident); isID && id.Name == param {
			index = i
		}
	}

	for i, arg := range call.Args {
		if bin, isBin := arg.(*syntax.BinaryExpr); isBin {
			if keyword, isKeyword := contractKeyword(bin); isKeyword && keyword == param {
				return bin.Y
			}

			continue
		}

		if i == index {
			return arg
		}
	}

	return nil
}

// resolveParams reaches the functions that the calls of a function name for its entry parameter. It reports whether a new function is reached.
func (g *contractGraph) resolveParams() bool {
	before := len(g.reached)

	for _, entry := range g.entryParams {
		for _, call := range g.calls[entry.fn] {
			if arg := contractArgument(call, g.defs[entry.fn], entry.param); arg != nil {
				if text, isText := contractString(arg); isText {
					g.reach(contractFunc{entry.fn.rel, text})
				}
			}
		}
	}

	return len(g.reached) > before
}

// drain visits the body of each reached function until no new function is reached.
func (g *contractGraph) drain() {
	for {
		for len(g.queue) > 0 {
			fn := g.queue[0]
			g.queue = g.queue[1:]

			def := g.defs[fn]
			scope := contractScope{rel: fn.rel, def: def, bound: contractBound(def)}

			for _, stmt := range def.Body {
				scope.node = stmt
				g.visit(scope)
			}
		}

		if !g.resolveParams() {
			return
		}
	}
}

// contractReach is the functions that an entry reaches. The entries are the code at the top of every file; main of a file that runs, and finish of it, which the engine
// calls after main; and the functions that a call lists with the keyword entry. A file runs when no other file loads it. A function is reached when code that is reached
// reads its name: in a call or as a value. A keyword of a call, an attribute, a local variable and a string are no reads of a function. A name that a file loads leads
// to the function of the file that the load names, and nothing else.
func contractReach(files []contractParsed) map[contractFunc]bool {
	g := newContractGraph(files)

	for _, parsed := range files {
		for _, stmt := range parsed.file.Stmts {
			switch stmt.(type) {
			case *syntax.DefStmt, *syntax.LoadStmt:
			default:
				g.visit(contractScope{rel: parsed.rel, node: stmt, bound: map[string]bool{}})
			}
		}

		if !g.loaded[parsed.rel] {
			g.reach(contractFunc{parsed.rel, "main"})
			g.reach(contractFunc{parsed.rel, "finish"})
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

// contractReachOf parses the files, each with the path e/<name>, and returns the names of the functions that an entry reaches, as "<name>:<function>".
func contractReachOf(t *testing.T, sources map[string]string) []string {
	t.Helper()

	var files []contractParsed

	for _, name := range slices.Sorted(maps.Keys(sources)) {
		file, err := model.StarlarkFileOptions().Parse(name, sources[name], 0)
		require.NoError(t, err)

		files = append(files, contractParsed{"e/" + name, file})
	}

	var got []string

	for fn := range contractReach(files) {
		got = append(got, strings.TrimPrefix(fn.rel, "e/")+":"+fn.name)
	}

	slices.Sort(got)

	return got
}

func TestContractReach_OnlyACallFromAnEntryReachesAFunction(t *testing.T) {
	t.Parallel()

	main := "load(\"//lib.star\", \"helper\", other = \"shared\")\n\ndef main(item):\n    a()\n    helper()\n    spawn([], entry = \"child\")\n\ndef a():\n    pass\n\ndef child(item):\n    b()\n\n" +
		"def b():\n    pass\n\ndef lonely():\n    c()\n\ndef c():\n    pass\n\nTABLE = {\"x\": d}\n\ndef d():\n    pass\n\ndef shared():\n    pass\n"
	lib := "def helper():\n    pass\n\ndef shared():\n    pass\n\ndef main():\n    pass\n"

	got := contractReachOf(t, map[string]string{"main.star": main, "lib.star": lib})

	assert.Equal(t, []string{"lib.star:helper", "main.star:a", "main.star:b", "main.star:child", "main.star:d", "main.star:main"}, got,
		"a function that only another file names is not reached; a loaded name leads to the file that the load names")
}

func TestContractReach_AKeywordOfACallIsNoCallOfAFunctionOfTheSameName(t *testing.T) {
	t.Parallel()

	got := contractReachOf(t, map[string]string{"main.star": "def main():\n    command(name = \"x\", plan = 1)\n\ndef plan():\n    pass\n"})

	assert.Equal(t, []string{"main.star:main"}, got)
}

func TestContractReach_AnAttributeIsNoCallOfAFunctionOfTheSameName(t *testing.T) {
	t.Parallel()

	got := contractReachOf(t, map[string]string{"main.star": "def main(item):\n    return item.plan\n\ndef plan():\n    pass\n"})

	assert.Equal(t, []string{"main.star:main"}, got)
}

func TestContractReach_ALocalVariableIsNoCallOfAFunctionOfTheSameName(t *testing.T) {
	t.Parallel()

	got := contractReachOf(t, map[string]string{
		"main.star": "def main(plan):\n    work = plan\n    for lonely in [1]:\n        print(lonely)\n    return [c for c in [1]] + [work]\n\ndef plan():\n    pass\n\ndef lonely():\n    pass\n\ndef c():\n    pass\n",
	})

	assert.Equal(t, []string{"main.star:main"}, got, "a parameter, an assignment, a loop variable and a comprehension variable shadow the function")
}

func TestContractReach_AStringIsNoCallOfAFunctionOfTheSameName(t *testing.T) {
	t.Parallel()

	got := contractReachOf(t, map[string]string{"main.star": "def main():\n    spawn([], entry = \"child\")\n    return \"lonely\"\n\ndef child():\n    pass\n\ndef lonely():\n    pass\n"})

	assert.Equal(t, []string{"main.star:child", "main.star:main"}, got, "only the string of the keyword entry names a function, though the file starts child runs")
}

func TestContractReach_MainAndFinishCountOnlyInTheFileThatRuns(t *testing.T) {
	t.Parallel()

	got := contractReachOf(t, map[string]string{
		"workflow.star": "load(\"//lib.star\", \"helper\")\n\ndef main():\n    helper()\n\ndef finish():\n    pass\n",
		"lib.star":      "def helper():\n    pass\n\ndef main():\n    pass\n\ndef finish():\n    pass\n",
	})

	assert.Equal(t, []string{"lib.star:helper", "workflow.star:finish", "workflow.star:main"}, got, "the engine calls main and finish of the file that it runs, and of no file that a load names")
}

func TestContractReach_AnEntryThroughAParameterNamesTheStringsThatACallPasses(t *testing.T) {
	t.Parallel()

	src := "def main():\n    run_alone(1, \"shadow\")\n    run_alone(2, entry = \"calibrate\")\n\ndef run_alone(case_id, entry):\n    return spawn([case_id], entry = entry)\n\n" +
		"def shadow():\n    pass\n\ndef calibrate():\n    pass\n\ndef other():\n    pass\n"

	got := contractReachOf(t, map[string]string{"main.star": src})

	assert.Equal(t, []string{"main.star:calibrate", "main.star:main", "main.star:run_alone", "main.star:shadow"}, got)
}

func TestContractReach_AnEntryThroughAConstantNamesTheConstantsThatTheCodeReads(t *testing.T) {
	t.Parallel()

	src := "TASK = \"task\"\nRESOLVE = \"resolve\"\nUNUSED = \"unused\"\n\ndef main():\n    for kind in [TASK, RESOLVE]:\n        spawn([], entry = kind)\n\n" +
		"def task():\n    pass\n\ndef resolve():\n    pass\n\ndef unused():\n    pass\n"

	got := contractReachOf(t, map[string]string{"main.star": src})

	assert.Equal(t, []string{"main.star:main", "main.star:resolve", "main.star:task"}, got)
}
