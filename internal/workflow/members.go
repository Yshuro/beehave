package workflow

import (
	"go.starlark.net/resolve"
	"go.starlark.net/starlark"
	"go.starlark.net/starlarkstruct"
	"go.starlark.net/syntax"
)

// structName is the builtin that makes a struct.
const structName = "struct"

// memberList is the value of the prs expression as the rule of the keyword reads it. ok is false when the whole value is computed.
func memberList(expr syntax.Expr) (starlark.Value, bool) { //nolint:ireturn // the value is any of the Starlark types
	v := readable(expr)
	_, unknown := v.(computed)

	return v, !unknown
}

// readable is the value of an expression as far as the check can know it. A literal is its value. A list, a tuple, a dict and a struct call
// have their type whatever their members hold, so each is built with the readable part of its members. Anything else is the computed
// stand-in, which the parser of the run skips. Every member keeps its place, so an error names the right index.
func readable(expr syntax.Expr) starlark.Value { //nolint:ireturn // the value is any of the Starlark types
	if v, ok := literalValue(expr); ok {
		return v
	}

	switch e := expr.(type) {
	case *syntax.ParenExpr:
		return readable(e.X)
	case *syntax.ListExpr:
		return starlark.NewList(readableAll(e.List))
	case *syntax.TupleExpr:
		return starlark.Tuple(readableAll(e.List))
	case *syntax.DictExpr:
		return starlark.NewDict(0) // the type is all that a reader of a member can see
	case *syntax.CallExpr:
		if v, ok := structCall(e); ok {
			return v
		}
	}

	return computed{}
}

// readableAll reads each expression of a list.
func readableAll(exprs []syntax.Expr) []starlark.Value {
	values := make([]starlark.Value, 0, len(exprs))

	for _, expr := range exprs {
		values = append(values, readable(expr))
	}

	return values
}

// structCall is the struct that a call of the builtin struct with keywords makes. ok is false for any other call, for a name that the file binds
// itself, and for a call with a positional argument, *args or **kwargs.
func structCall(call *syntax.CallExpr) (*starlarkstruct.Struct, bool) {
	if fn, isName := call.Fn.(*syntax.Ident); !isName || fn.Name != structName || !isPredeclared(fn) {
		return nil, false
	}

	fields := make([]starlark.Tuple, 0, len(call.Args))

	for _, arg := range call.Args {
		pair, isPair := arg.(*syntax.BinaryExpr)
		if !isPair || pair.Op != syntax.EQ {
			return nil, false
		}

		name := pair.X.(*syntax.Ident) //nolint:errcheck,forcetypeassert // the parser refuses a keyword argument whose left side is not a name

		fields = append(fields, starlark.Tuple{starlark.String(name.Name), readable(pair.Y)})
	}

	return starlarkstruct.FromKeywords(starlarkstruct.Default, fields), true
}

// isPredeclared reports whether the resolver bound the name to the builtin and not to a name of the file.
func isPredeclared(name *syntax.Ident) bool {
	binding, isBinding := name.Binding.(*resolve.Binding)

	return isBinding && binding.Scope == resolve.Predeclared
}
