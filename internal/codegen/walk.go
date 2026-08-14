package codegen

import "jabascript/internal/ast"

// inspect calls f on n and every node beneath it, in source order.
func inspect(n ast.Node, f func(ast.Node)) {
	if n == nil {
		return
	}
	f(n)
	switch n := n.(type) {
	case *ast.File:
		for _, d := range n.Decls {
			inspect(d, f)
		}
	case *ast.FuncDecl:
		if n.Body != nil {
			inspect(n.Body, f)
		}
	case *ast.VarDecl:
		if n.Init != nil {
			inspect(n.Init, f)
		}
	case *ast.Block:
		for _, s := range n.Stmts {
			inspect(s, f)
		}
	case *ast.AssignStmt:
		inspect(n.LHS, f)
		inspect(n.RHS, f)
	case *ast.ExprStmt:
		inspect(n.X, f)
	case *ast.IfStmt:
		inspect(n.Cond, f)
		inspect(n.Then, f)
		if n.Else != nil {
			inspect(n.Else, f)
		}
	case *ast.WhileStmt:
		inspect(n.Cond, f)
		inspect(n.Body, f)
	case *ast.ReturnStmt:
		if n.X != nil {
			inspect(n.X, f)
		}
	case *ast.ParenExpr:
		inspect(n.X, f)
	case *ast.UnaryExpr:
		inspect(n.X, f)
	case *ast.BinaryExpr:
		inspect(n.X, f)
		inspect(n.Y, f)
	case *ast.CastExpr:
		inspect(n.X, f)
	case *ast.CallExpr:
		for _, a := range n.Args {
			inspect(a, f)
		}
	case *ast.IndexExpr:
		inspect(n.X, f)
		inspect(n.Index, f)
	case *ast.FieldExpr:
		inspect(n.X, f)
	case *ast.StructLit:
		for _, init := range n.Inits {
			inspect(init.Value, f)
		}
	}
}
