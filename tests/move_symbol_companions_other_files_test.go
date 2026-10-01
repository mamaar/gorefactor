package tests_test

import (
	"context"
	"strings"
	"testing"

	"github.com/mamaar/gorefactor/tests/mcptest"
)

func moveTypeAcrossFiles(t *testing.T, files map[string]string, symbol string) (string, bool, string) {
	t.Helper()
	dir := writeInlineWorkspace(t, files)
	sess := mcptest.Dial(context.Background(), t, mcpTransport(), dir)
	defer sess.Close()
	res := moveSymbolCall(t, sess, symbol, "src", "dst")
	return dir, res.IsError, errText(res)
}

func TestMoveSymbolMovesConstructorDeclaredInAnotherFile(t *testing.T) {
	dir, isErr, msg := moveTypeAcrossFiles(t, map[string]string{
		"src/t.go":   "package src\n\ntype T struct{ n int }\n",
		"src/new.go": "package src\n\nfunc NewT() *T { return &T{n: 1} }\n",
		"use/use.go": "package use\n\nimport \"example.com/threadmcp/src\"\n\nfunc Make() *src.T { return src.NewT() }\n",
	}, "T")
	if isErr {
		t.Fatalf("move: %s", msg)
	}
	assertFileContains(t, dir, "dst/dst.go", "type T struct")
	assertFileContains(t, dir, "dst/dst.go", "func NewT()")
	if got := fileText(t, dir, "src/new.go"); strings.Contains(got, "NewT") {
		t.Errorf("NewT remains in src/new.go:\n%s", got)
	}
	assertFileContains(t, dir, "use/use.go", "dst.NewT()")
	goVets(t, dir, "")
}

func TestMoveSymbolBringsImportOfConstructorInAnotherFile(t *testing.T) {
	dir, isErr, msg := moveTypeAcrossFiles(t, map[string]string{
		"src/t.go":   "package src\n\ntype T struct{ name string }\n",
		"src/new.go": "package src\n\nimport \"strings\"\n\nfunc NewT(s string) *T { return &T{name: strings.ToUpper(s)} }\n",
	}, "T")
	if isErr {
		t.Fatalf("move: %s", msg)
	}
	assertFileContains(t, dir, "dst/dst.go", "\"strings\"")
	assertFileContains(t, dir, "dst/dst.go", "func NewT(")
	if got := fileText(t, dir, "src/new.go"); strings.Contains(got, "strings") {
		t.Errorf("unused import remains in src/new.go:\n%s", got)
	}
	goVets(t, dir, "")
}

func TestMoveSymbolOrdersConstructorsOwnFileFirstThenOtherFilesByName(t *testing.T) {
	files := func() map[string]string {
		return map[string]string{
			"src/t.go": "package src\n\ntype T struct{ n int }\n\nfunc NewT() *T { return &T{n: 1} }\n",
			"src/b.go": "package src\n\nfunc NewTB() *T { return &T{n: 3} }\n",
			"src/a.go": "package src\n\nfunc NewTA() *T { return &T{n: 2} }\n",
		}
	}
	var outputs []string
	for range 2 {
		dir, isErr, msg := moveTypeAcrossFiles(t, files(), "T")
		if isErr {
			t.Fatalf("move: %s", msg)
		}
		goVets(t, dir, "")
		outputs = append(outputs, fileText(t, dir, "dst/dst.go"))
	}
	if outputs[0] != outputs[1] {
		t.Errorf("output differs between runs:\n%s\n---\n%s", outputs[0], outputs[1])
	}
	got := outputs[0]
	iT, iNew, iA, iB := strings.Index(got, "type T struct"), strings.Index(got, "func NewT()"), strings.Index(got, "func NewTA()"), strings.Index(got, "func NewTB()")
	if iT < 0 || iNew < iT || iA < iNew || iB < iA {
		t.Errorf("unexpected order (T=%d NewT=%d NewTA=%d NewTB=%d):\n%s", iT, iNew, iA, iB, got)
	}
}

func TestMoveSymbolRefusesWhenUnexportedLiteralInAnotherFileCannotMove(t *testing.T) {
	dir, isErr, msg := moveTypeAcrossFiles(t, map[string]string{
		"src/t.go":     "package src\n\ntype T struct{ n int }\n",
		"src/other.go": "package src\n\nfunc Peek() int { return T{n: 1}.n }\n\nfunc Keep() int { return 2 }\n",
		"src/use.go":   "package src\n\nfunc Use() int { return Keep() + Peek() }\n",
	}, "T")
	if !isErr || !strings.Contains(msg, "other.go") {
		t.Fatalf("want refusal naming other.go, got isErr=%v msg=%s", isErr, msg)
	}
	assertFileContains(t, dir, "src/other.go", "func Peek()")
}

func TestMoveSymbolRefusesConstructorInTestFile(t *testing.T) {
	dir, isErr, msg := moveTypeAcrossFiles(t, map[string]string{
		"src/t.go":      "package src\n\ntype T struct{ n int }\n",
		"src/t_test.go": "package src\n\nimport \"testing\"\n\nfunc newT() *T { return &T{n: 1} }\n\nfunc TestT(t *testing.T) { _ = newT() }\n",
	}, "T")
	if !isErr || !strings.Contains(msg, "t_test.go") {
		t.Fatalf("want refusal naming t_test.go, got isErr=%v msg=%s", isErr, msg)
	}
	assertFileContains(t, dir, "src/t.go", "type T struct")
}
