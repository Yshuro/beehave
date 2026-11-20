package workflow

import "go.starlark.net/syntax"

// skillsField is the field of the result of an agent step that lists the skills the agent called.
const skillsField = "skills"

// varsField is the field of the facts of a project that holds its vars.
const varsField = "vars"

// failName is the builtin that stops a run with a message.
const failName = "fail"

// RequiredSkills returns the skills that the workflow file and the files it loads require of a step, in the order of the text.
// A skill is required where an if tests `"name" in X` or `"name" not in X`, through not and parentheses, and the branch taken when the skill is missing calls fail.
// X is `<expression>.skills`, or a name that every assignment in the same function binds from `<expression>.skills`.
// A test that fails when the skill is present is a forbid check and requires nothing. It runs no workflow code.
// A file that cannot be read or parsed gives no skills: the run reports that error.
func RequiredSkills(file string) []string {
	return needsOf(scan(file).skills, false)
}

// RequiredSkillVars returns the keys of the project vars that name a required skill, in the order of the text.
// A skill is named by a key where the text of the test is `<expression>.vars["key"]`, so the project, not the workflow, names the skill.
func RequiredSkillVars(file string) []string {
	return needsOf(scan(file).skills, true)
}

// skillNeed is one required skill: the literal text of its name, or the key of the project vars that holds the name.
type skillNeed struct {
	name     string
	fromVars bool
}

// needsOf returns the names of the needs that are read from the vars of the project, or the names that are literal text.
func needsOf(needs []skillNeed, fromVars bool) []string {
	var names []string

	for _, need := range needs {
		if need.fromVars == fromVars {
			names = append(names, need.name)
		}
	}

	return names
}

// skillChecks returns the skills that the functions of a parsed file require, in the order of the text. Each function is a scope of its own.
func skillChecks(parsed *syntax.File) []skillNeed {
	var found []skillNeed

	syntax.Walk(parsed, func(n syntax.Node) bool {
		if def, ok := n.(*syntax.DefStmt); ok {
			found = append(found, scopeChecks(def)...)
		}

		return true
	})

	return found
}

// scopeChecks returns the skills that one function requires. A nested function is a scope of its own and is not read here.
func scopeChecks(def *syntax.DefStmt) []skillNeed {
	aliases := skillAliases(def)

	var found []skillNeed

	inScope(def.Body, func(n syntax.Node) {
		if test, ok := n.(*syntax.IfStmt); ok {
			if need, required := requirement(test, aliases); required {
				found = append(found, need)
			}
		}
	})

	return found
}

// inScope calls visit for each node of the statements, and does not enter a nested function. A lambda holds no statement.
func inScope(body []syntax.Stmt, visit func(syntax.Node)) {
	for _, stmt := range body {
		syntax.Walk(stmt, func(n syntax.Node) bool {
			visit(n)

			_, nested := n.(*syntax.DefStmt)

			return !nested
		})
	}
}

// skillAliases lists the names that every binding in the function takes from a `.skills` field: `calls = r.skills`.
// A parameter, a loop variable, a nested function or any other assignment to the name makes it no alias.
func skillAliases(def *syntax.DefStmt) map[string]bool {
	from, other := skillBindings(def)
	aliases := map[string]bool{}

	for name := range from {
		aliases[name] = !other[name]
	}

	return aliases
}

// skillBindings returns the names that the function binds from a `.skills` field, and the names that it binds in any other way.
func skillBindings(def *syntax.DefStmt) (from, other map[string]bool) {
	from, other = map[string]bool{}, map[string]bool{}

	for _, param := range def.Params {
		markNames(param, other)
	}

	inScope(def.Body, func(n syntax.Node) {
		switch s := n.(type) {
		case *syntax.AssignStmt:
			bindAssign(s, from, other)
		case *syntax.ForStmt:
			markNames(s.Vars, other)
		case *syntax.DefStmt:
			other[s.Name.Name] = true
		}
	})

	return from, other
}

// bindAssign adds the names of an assignment to from when it is `name = <expression>.skills`, and to other for any other assignment.
func bindAssign(assign *syntax.AssignStmt, from, other map[string]bool) {
	if target, isName := assign.LHS.(*syntax.Ident); isName && assign.Op == syntax.EQ && isSkillField(assign.RHS) {
		from[target.Name] = true

		return
	}

	markNames(assign.LHS, other)
}

// markNames adds each name in the expression to the set.
func markNames(expr syntax.Expr, set map[string]bool) {
	syntax.Walk(expr, func(n syntax.Node) bool {
		if name, ok := n.(*syntax.Ident); ok {
			set[name.Name] = true
		}

		return true
	})
}

// requirement returns the skill that an if requires: its test is a membership of a text literal in a skills list, through not and parentheses,
// and a statement of the branch that runs when the skill is missing is a call of fail.
func requirement(test *syntax.IfStmt, aliases map[string]bool) (skillNeed, bool) {
	member, negations := membership(test.Cond)
	if member == nil || !isSkillList(member.Y, aliases) {
		return skillNeed{}, false
	}

	need, isName := skillName(member.X)
	if !isName {
		return skillNeed{}, false
	}

	missing := test.False
	if negations%2 == 1 {
		missing = test.True
	}

	return need, callsFail(missing)
}

// membership returns the in or not in test under any parentheses and not around the condition, and how many negations the condition holds.
// A not in counts as one negation. It returns nil when the condition is not such a test.
func membership(cond syntax.Expr) (*syntax.BinaryExpr, int) {
	switch c := cond.(type) {
	case *syntax.ParenExpr:
		return membership(c.X)
	case *syntax.UnaryExpr:
		if inner, negations := membership(c.X); c.Op == syntax.NOT && inner != nil {
			return inner, negations + 1
		}
	case *syntax.BinaryExpr:
		if c.Op == syntax.IN {
			return c, 0
		}

		if c.Op == syntax.NOT_IN {
			return c, 1
		}
	}

	return nil, 0
}

// skillName returns the need that the expression names, under any parentheses: a string literal, or `<expression>.vars["key"]` with a literal key.
// ok is false for any other expression.
func skillName(expr syntax.Expr) (skillNeed, bool) {
	switch e := expr.(type) {
	case *syntax.ParenExpr:
		return skillName(e.X)
	case *syntax.IndexExpr:
		holder, isDot := e.X.(*syntax.DotExpr)
		key, isText := literal(e.Y)

		return skillNeed{name: key, fromVars: true}, isDot && holder.Name.Name == varsField && isText
	}

	name, isText := literal(expr)

	return skillNeed{name: name}, isText
}

// callsFail reports whether a statement of the branch is a call of fail.
func callsFail(branch []syntax.Stmt) bool {
	for _, stmt := range branch {
		if run, ok := stmt.(*syntax.ExprStmt); ok {
			if call, isCall := run.X.(*syntax.CallExpr); isCall && callee(call) == failName {
				return true
			}
		}
	}

	return false
}

// isSkillList reports whether the expression is a `.skills` field or a name that the function binds from one.
func isSkillList(expr syntax.Expr, aliases map[string]bool) bool {
	switch e := expr.(type) {
	case *syntax.ParenExpr:
		return isSkillList(e.X, aliases)
	case *syntax.Ident:
		return aliases[e.Name]
	}

	return isSkillField(expr)
}

// isSkillField reports whether the expression reads the field skills of a value, under any parentheses.
func isSkillField(expr syntax.Expr) bool {
	switch e := expr.(type) {
	case *syntax.ParenExpr:
		return isSkillField(e.X)
	case *syntax.DotExpr:
		return e.Name.Name == skillsField
	}

	return false
}
