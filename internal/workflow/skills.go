package workflow

import "go.starlark.net/syntax"

// skillsField is the field of the result of an agent step that lists the skills the agent called.
const skillsField = "skills"

// RequiredSkills returns the skills that the workflow file and the files it loads test for in the skills of a step, in the order of the text.
// A test is `"name" in X`, `"name" not in X`, or either one under `not`, where X is `<expression>.skills` or a name that the file assigns from `<expression>.skills`.
// A name that is not a text literal in the test itself, such as a helper argument, is not found. It runs no workflow code.
// A file that cannot be read or parsed gives no skills: the run reports that error.
func RequiredSkills(file string) []string {
	return scan(file).skills
}

// skillChecks returns the skills that a parsed file tests for in a `.skills` list, in the order of the text.
func skillChecks(parsed *syntax.File) []string {
	aliases := skillAliases(parsed)

	var found []string

	syntax.Walk(parsed, func(n syntax.Node) bool {
		test, ok := n.(*syntax.BinaryExpr)
		if !ok || (test.Op != syntax.IN && test.Op != syntax.NOT_IN) || !isSkillList(test.Y, aliases) {
			return true
		}

		if name, isText := literal(unparen(test.X)); isText {
			found = append(found, name)
		}

		return true
	})

	return found
}

// skillAliases lists the names that the file assigns from a `.skills` field, at any depth: `calls = r.skills`.
func skillAliases(parsed *syntax.File) map[string]bool {
	aliases := map[string]bool{}

	syntax.Walk(parsed, func(n syntax.Node) bool {
		assign, ok := n.(*syntax.AssignStmt)
		if !ok || assign.Op != syntax.EQ {
			return true
		}

		if target, isName := assign.LHS.(*syntax.Ident); isName && isSkillField(assign.RHS) {
			aliases[target.Name] = true
		}

		return true
	})

	return aliases
}

// isSkillList reports whether the expression is a `.skills` field or a name that the file assigns from one.
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
