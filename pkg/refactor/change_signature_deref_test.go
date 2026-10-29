package refactor

import (
	"go/ast"
	"go/parser"
	"go/token"
	"testing"
)

func TestGenerateNewSignature_KeptGroupHasNoSpaceBeforeComma(t *testing.T) {
	f, err := parser.ParseFile(token.NewFileSet(), "f.go", "package p\n\nfunc g(userID, pass string, n *int) {}\n", 0)
	if err != nil {
		t.Fatal(err)
	}
	op := &ChangeSignatureOperation{NewParams: []Parameter{{Name: "userID", Type: "string"}, {Name: "pass", Type: "string"}, {Name: "n", Type: "int"}}}
	got := op.generateNewSignature(f.Decls[0].(*ast.FuncDecl))
	if want := "func g(userID, pass string, n int)"; got != want {
		t.Errorf("signature = %q, want %q", got, want)
	}
}
