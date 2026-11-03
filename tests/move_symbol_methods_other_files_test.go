package tests_test

import (
	"context"
	"strings"
	"testing"

	"github.com/mamaar/gorefactor/tests/mcptest"
)

const mofRef = "package use\n\nimport \"example.com/threadmcp/src\"\n\nfunc Use() string { return src.T{}.Upper() }\n"

func TestMoveSymbol_MethodInAnotherFileMovesWithItsType(t *testing.T) {
	dir := writeInlineWorkspace(t, map[string]string{
		"src/t.go":   "package src\n\ntype T struct{ Name string }\n",
		"src/m.go":   "package src\n\nimport \"strings\"\n\nfunc (t T) Upper() string { return strings.ToUpper(t.Name) }\n\nfunc Keep() {}\n",
		"use/use.go": mofRef,
	})
	sess := mcptest.Dial(context.Background(), t, mcpTransport(), dir)
	defer sess.Close()
	res := moveSymbolCall(t, sess, "T", "src", "dst")
	if res.IsError {
		t.Fatalf("move T: %s", errText(res))
	}
	assertFileContains(t, dir, "dst/dst.go", "func (t T) Upper() string")
	assertFileContains(t, dir, "dst/dst.go", `"strings"`)
	m := fileText(t, dir, "src/m.go")
	if strings.Contains(m, "Upper") || strings.Contains(m, `"strings"`) {
		t.Errorf("method or its import left in src/m.go:\n%s", m)
	}
	goVets(t, dir, "")
}

func TestMoveSymbol_MethodsSpreadOverFilesMoveInDeterministicOrder(t *testing.T) {
	files := func() map[string]string {
		return map[string]string{
			"src/t.go": "package src\n\ntype T struct{ N int }\n\nfunc (t T) A() int { return t.N }\n",
			"src/z.go": "package src\n\nfunc (t *T) Z() { t.N++ }\n",
			"src/b.go": "package src\n\nfunc (t T) B() int { return t.N }\n\nfunc (t *T) B2() { t.N-- }\n",
		}
	}
	var dsts []string
	for range 2 {
		dir := writeInlineWorkspace(t, files())
		sess := mcptest.Dial(context.Background(), t, mcpTransport(), dir)
		res := moveSymbolCall(t, sess, "T", "src", "dst")
		if res.IsError {
			t.Fatalf("move T: %s", errText(res))
		}
		sess.Close()
		goVets(t, dir, "")
		dsts = append(dsts, fileText(t, dir, "dst/dst.go"))
	}
	if dsts[0] != dsts[1] {
		t.Errorf("output differs between runs:\n%s\n---\n%s", dsts[0], dsts[1])
	}
	last := -1
	for _, m := range []string{"type T struct", ") A()", ") B()", ") B2()", ") Z()"} {
		i := strings.Index(dsts[0], m)
		if i <= last {
			t.Fatalf("%q out of order in dst:\n%s", m, dsts[0])
		}
		last = i
	}
}

func TestMoveSymbol_GenericTypeMethodInAnotherFileMoves(t *testing.T) {
	dir := writeInlineWorkspace(t, map[string]string{
		"src/g.go": "package src\n\ntype G[K comparable] struct{ M map[K]int }\n",
		"src/m.go": "package src\n\nfunc (g *G[K]) Len() int { return len(g.M) }\n",
	})
	sess := mcptest.Dial(context.Background(), t, mcpTransport(), dir)
	defer sess.Close()
	res := moveSymbolCall(t, sess, "G", "src", "dst")
	if res.IsError {
		t.Fatalf("move G: %s", errText(res))
	}
	assertFileContains(t, dir, "dst/dst.go", "func (g *G[K]) Len() int")
	goVets(t, dir, "")
}

func TestMoveSymbol_DocCommentOfMethodInAnotherFileTravelsWithIt(t *testing.T) {
	dir := writeInlineWorkspace(t, map[string]string{
		"src/t.go": "package src\n\ntype T struct{}\n",
		"src/m.go": "package src\n\n// Do does it.\nfunc (t T) Do() {}\n\nfunc Keep() {}\n",
	})
	sess := mcptest.Dial(context.Background(), t, mcpTransport(), dir)
	defer sess.Close()
	res := moveSymbolCall(t, sess, "T", "src", "dst")
	if res.IsError {
		t.Fatalf("move T: %s", errText(res))
	}
	assertFileContains(t, dir, "dst/dst.go", "// Do does it.\nfunc (t T) Do() {}")
	m := fileText(t, dir, "src/m.go")
	if strings.Contains(m, "Do does it") {
		t.Errorf("doc left in src/m.go:\n%s", m)
	}
	goVets(t, dir, "")
}

func TestMoveSymbol_MethodInAnotherFileUsingUnexportedHelperIsRefused(t *testing.T) {
	files := map[string]string{
		"src/t.go": "package src\n\ntype T struct{}\n",
		"src/m.go": "package src\n\nfunc helper() int { return 1 }\n\nfunc (t T) Do() int { return helper() }\n",
	}
	dir := writeInlineWorkspace(t, files)
	sess := mcptest.Dial(context.Background(), t, mcpTransport(), dir)
	defer sess.Close()
	res := moveSymbolCall(t, sess, "T", "src", "dst")
	if !res.IsError {
		t.Fatal("move succeeded, want refusal")
	}
	msg := errText(res)
	if !strings.Contains(msg, "T cannot move") || !strings.Contains(msg, "helper") {
		t.Errorf("error = %s, want it to name T and helper", msg)
	}
	assertFileUnchanged(t, dir, "src/t.go", files["src/t.go"])
	assertFileUnchanged(t, dir, "src/m.go", files["src/m.go"])
}

func TestMoveSymbol_MethodInAnotherFileUsingExportedLeftBehindNameIsQualified(t *testing.T) {
	dir := writeInlineWorkspace(t, map[string]string{
		"src/t.go": "package src\n\ntype T struct{}\n",
		"src/m.go": "package src\n\nfunc Helper() int { return 1 }\n\nfunc (t T) Do() int { return Helper() }\n",
	})
	sess := mcptest.Dial(context.Background(), t, mcpTransport(), dir)
	defer sess.Close()
	res := moveSymbolCall(t, sess, "T", "src", "dst")
	if res.IsError {
		t.Fatalf("move T: %s", errText(res))
	}
	assertFileContains(t, dir, "dst/dst.go", "src.Helper()")
	goVets(t, dir, "")
}

func TestMoveSymbol_MethodInInPackageTestFileIsRefusedNamingIt(t *testing.T) {
	files := map[string]string{
		"src/t.go":           "package src\n\ntype T struct{ N int }\n",
		"src/export_test.go": "package src\n\nimport \"testing\"\n\nfunc (t T) Peek() int { return t.N }\n\nvar _ = testing.Short\n",
	}
	dir := writeInlineWorkspace(t, files)
	sess := mcptest.Dial(context.Background(), t, mcpTransport(), dir)
	defer sess.Close()
	res := moveSymbolCall(t, sess, "T", "src", "dst")
	if !res.IsError {
		t.Fatal("move succeeded, want refusal")
	}
	msg := errText(res)
	if !strings.Contains(msg, "Peek") || !strings.Contains(msg, "src/export_test.go:5") {
		t.Errorf("error = %s, want it to name Peek and src/export_test.go:5", msg)
	}
	assertFileUnchanged(t, dir, "src/t.go", files["src/t.go"])
	assertFileUnchanged(t, dir, "src/export_test.go", files["src/export_test.go"])
}

func TestMoveByDependencies_SharedTypeWithMethodInAnotherFileMovesWithIt(t *testing.T) {
	dir := writeInlineWorkspace(t, map[string]string{
		"src/box.go":  "package src\n\ntype Box struct{ N int }\n",
		"src/boxm.go": "package src\n\nfunc (b Box) Get() int { return b.N }\n",
		"a/a.go":      "package a\n\nimport \"example.com/threadmcp/src\"\n\nfunc A() int { return src.Box{N: 1}.Get() }\n",
		"b/b.go":      "package b\n\nimport \"example.com/threadmcp/src\"\n\nfunc B() int { return src.Box{N: 2}.Get() }\n",
	})
	sess := mcptest.Dial(context.Background(), t, mcpTransport(), dir)
	defer sess.Close()
	callJSON(t, sess, "move_by_dependencies", map[string]any{"move_shared_to": "shared"})
	assertFileContains(t, dir, "shared/shared.go", "type Box struct")
	assertFileContains(t, dir, "shared/shared.go", "func (b Box) Get() int")
	goVets(t, dir, "")
}
