package core

import (
	"go/ast"
	"go/parser"
	"go/token"
	"testing"
)

// This static lock-order check is deterministic and documents why migration
// may share sessionsMu with sessions.reset: TelegramSession takes its own lock
// before calling the migration helper, while reset only takes sessionsMu.
func TestLegacyTelegramMigrationUsesSessionsMutexInLockOrder(t *testing.T) {
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, "telegram_session.go", nil, parser.ParseComments)
	if err != nil {
		t.Fatal(err)
	}
	funcs := map[string]*ast.FuncDecl{}
	for _, decl := range file.Decls {
		if fn, ok := decl.(*ast.FuncDecl); ok && fn.Body != nil {
			funcs[fn.Name.Name] = fn
		}
	}

	migration := funcs["migrateLegacyTelegramSession"]
	if migration == nil || len(migration.Body.List) < 2 {
		t.Fatal("migration helper or its lock prologue not found")
	}
	if !isLockCall(migration.Body.List[0], "sessionsMu", "Lock") || !isUnlockDefer(migration.Body.List[1], "sessionsMu") {
		t.Fatal("migration must hold sessionsMu across every legacy read/check/write")
	}
	getPos, putPos := firstSelectorPosition(migration.Body, "Get"), firstSelectorPosition(migration.Body, "Put")
	unlockPos := migration.Body.List[1].End()
	if getPos == token.NoPos || putPos == token.NoPos || getPos <= unlockPos || putPos <= unlockPos {
		t.Fatal("legacy session access is not enclosed by sessionsMu")
	}

	telegramSession := funcs["TelegramSession"]
	if telegramSession == nil {
		t.Fatal("TelegramSession not found")
	}
	telegramLockPos := firstSelectorPosition(telegramSession.Body, "Lock")
	migrationCallPos := firstCallPosition(telegramSession.Body, "migrateLegacyTelegramSession")
	if telegramLockPos == token.NoPos || migrationCallPos == token.NoPos || telegramLockPos >= migrationCallPos {
		t.Fatal("expected lock order telegramSessionMu -> sessionsMu")
	}
}

func isLockCall(stmt ast.Stmt, mutex, method string) bool {
	expr, ok := stmt.(*ast.ExprStmt)
	if !ok {
		return false
	}
	call, ok := expr.X.(*ast.CallExpr)
	if !ok {
		return false
	}
	selector, ok := call.Fun.(*ast.SelectorExpr)
	if !ok || selector.Sel.Name != method {
		return false
	}
	owner, ok := selector.X.(*ast.SelectorExpr)
	return ok && owner.Sel.Name == mutex
}

func isUnlockDefer(stmt ast.Stmt, mutex string) bool {
	deferStmt, ok := stmt.(*ast.DeferStmt)
	if !ok {
		return false
	}
	call, ok := deferStmt.Call.Fun.(*ast.SelectorExpr)
	if !ok || call.Sel.Name != "Unlock" {
		return false
	}
	owner, ok := call.X.(*ast.SelectorExpr)
	return ok && owner.Sel.Name == mutex
}

func firstSelectorPosition(node ast.Node, selector string) token.Pos {
	pos := token.NoPos
	ast.Inspect(node, func(n ast.Node) bool {
		if expr, ok := n.(*ast.SelectorExpr); ok && expr.Sel.Name == selector && (pos == token.NoPos || expr.Pos() < pos) {
			pos = expr.Pos()
		}
		return true
	})
	return pos
}

func firstCallPosition(node ast.Node, name string) token.Pos {
	pos := token.NoPos
	ast.Inspect(node, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		matched := false
		if selector, ok := call.Fun.(*ast.Ident); ok {
			matched = selector.Name == name
		} else if selector, ok := call.Fun.(*ast.SelectorExpr); ok {
			matched = selector.Sel.Name == name
		}
		if matched && (pos == token.NoPos || call.Pos() < pos) {
			pos = call.Pos()
		}
		return true
	})
	return pos
}
