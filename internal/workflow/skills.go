package workflow

import "go.starlark.net/syntax"

// skillsField is the field of the result of an agent step that lists the skills the agent called.
const skillsField = "skills"

// failName is the builtin that stops a run with a message.
const failName = "fail"

// RequiredSkills returns the skills that the workflow file and the files it loads require of a step, in the order of the text.
// A skill is required where an if tests `"name" in X` or `"name" not in X`, through not and parentheses, and the branch taken when the skill is missing calls fail.
// X is `<expression>.skills`, or a name that every assignment in the same function binds from `<expression>.skills`.
// A test that fails when the skill is present is a forbid check and requires nothing. It runs no workflow code.
// A file that cannot be read or parsed gives no skills: the run reports that error.
func RequiredSkills(file string) []string {
	return scan(file).skills
}

// skillChecks returns the skills that the functions of a parsed file require, in the order of the text. Each function is a scope of its own.
func skillChecks(parsed *syntax.File) []string {
	var found []string

	syntax.Walk(parsed, func(n syntax.Node) bool {
		if def, ok := n.(*syntax.DefStmt); ok {
			found = append(found, scopeChecks(def)...)
		}

		return true
	})

	return found
}

// scopeChecks returns the skills that one function requires. A nested function is a scope of its own and is not read here.
func scopeChecks(def *syntax.DefStmt) []string {
	aliases := skillAliases(def)

	var found []string

	inScope(def.Body, func(n syntax.Node) {
		if test, ok := n.(*syntax.IfStmt); ok {
			if name, required := requirement(test, aliases); required {
				found = append(found, name)
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
	from, other := map[string]bool{}, map[string]bool{}

	for _, param := range def.Params {
		markNames(param, other)
	}

	inScope(def.Body, func(n syntax.Node) {
		switch s := n.(type) {
		case *syntax.AssignStmt:
			if target, isName := s.LHS.(*syntax.Ident); isName && s.Op == syntax.EQ && isSkillField(s.RHS) {
				from[target.Name] = true
			} else {
				markNames(s.LHS, other)
			}
		case *syntax.ForStmt:
			markNames(s.Vars, other)
		case *syntax.DefStmt:
			other[s.Name.Name] = true
		}
	})

	aliases := map[string]bool{}

	for name := range from {
		aliases[name] = !other[name]
	}

	return aliases
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
func requirement(test *syntax.IfStmt, aliases map[string]bool) (string, bool) {
	cond, negations := test.Cond, 0

	for {
		cond = unparen(cond)

		not, isNot := cond.(*syntax.UnaryExpr)
		if !isNot || not.Op != syntax.NOT {
			break
		}

		cond, negations = not.X, negations+1
	}

	member, ok := cond.(*syntax.BinaryExpr)
	if !ok || (member.Op != syntax.IN && member.Op != syntax.NOT_IN) || !isSkillList(member.Y, aliases) {
		return "", false
	}

	name, isText := literal(unparen(member.X))
	if !isText {
		return "", false
	}

	if member.Op == syntax.NOT_IN {
		negations++
	}

	missing := test.False
	if negations%2 == 1 {
		missing = test.True
	}

	return name, callsFail(missing)
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
	expr = unparen(expr)
	if name, ok := expr.(*syntax.Ident); ok {
		return aliases[name.Name]
	}

	return isSkillField(expr)
}

// isSkillField reports whether the expression reads the field skills of a value.
func isSkillField(expr syntax.Expr) bool {
	field, ok := unparen(expr).(*syntax.DotExpr)

	return ok && field.Name.Name == skillsField
}

// unparen returns the expression inside any parentheses around it.
func unparen(expr syntax.Expr) syntax.Expr {
	for {
		paren, ok := expr.(*syntax.ParenExpr)
		if !ok {
			return expr
		}

		expr = paren.X
	}
}
