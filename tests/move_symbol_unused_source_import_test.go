package tests_test

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"github.com/mamaar/gorefactor/tests/mcptest"
)

const unusedImportSrc = `package src

func One() int { return 1 }

func Two() int { return 2 }

func Keep() int { return 3 }
`

func moveUnusedImportFixture(t *testing.T, extra map[string]string) string {
	t.Helper()
	files := map[string]string{
		"src/src.go": unusedImportSrc,
		"a/a.go":     "package a\n\nimport \"example.com/threadmcp/src\"\n\nfunc A() int { return src.One() + src.Two() }\n",
		"b/b.go":     "package b\n\nimport \"example.com/threadmcp/src\"\n\nfunc B() int { return src.One() + src.Two() }\n",
	}
	for k, v := range extra {
		files[k] = v
	}
	return writeInlineWorkspace(t, files)
}

func moveToShared(t *testing.T, sess *mcptest.Session, symbols ...string) {
	t.Helper()
	for _, s := range symbols {
		callJSON(t, sess, "move_symbol", map[string]any{"symbol": s, "from_package": "src", "to_package": "shared"})
	}
}

func assertNoSourceImport(t *testing.T, dir string, files ...string) {
	t.Helper()
	for _, f := range files {
		if got := mustRead(t, filepath.Join(dir, f)); strings.Contains(got, "threadmcp/src") {
			t.Errorf("%s still imports src:\n%s", f, got)
		}
	}
}

func TestMoveSymbol_SecondMoveDropsSourceImportOnceLastReferenceIsGone(t *testing.T) {
	dir := moveUnusedImportFixture(t, nil)
	sess := mcptest.Dial(context.Background(), t, mcpTransport(), dir)
	defer sess.Close()

	moveToShared(t, sess, "One", "Two")

	assertNoSourceImport(t, dir, "a/a.go", "b/b.go")
	goVets(t, dir, "")
}

func TestMoveSymbol_FileUsingAnotherSourceSymbolKeepsSourceImport(t *testing.T) {
	dir := moveUnusedImportFixture(t, map[string]string{
		"c/c.go": "package c\n\nimport \"example.com/threadmcp/src\"\n\nfunc C() int { return src.One() + src.Two() + src.Keep() }\n",
	})
	sess := mcptest.Dial(context.Background(), t, mcpTransport(), dir)
	defer sess.Close()

	moveToShared(t, sess, "One", "Two")

	if got := mustRead(t, filepath.Join(dir, "c/c.go")); !strings.Contains(got, "src.Keep()") || !strings.Contains(got, "threadmcp/src\"") {
		t.Errorf("c/c.go lost the source import:\n%s", got)
	}
	assertNoSourceImport(t, dir, "a/a.go")
	goVets(t, dir, "")
}

func TestMoveSymbol_TestFileReferrerDropsSourceImport(t *testing.T) {
	dir := moveUnusedImportFixture(t, map[string]string{
		"a/a_test.go": "package a\n\nimport (\n\t\"testing\"\n\n\t\"example.com/threadmcp/src\"\n)\n\nfunc TestA(t *testing.T) {\n\tif src.One()+src.Two() != 3 {\n\t\tt.Fatal(\"bad\")\n\t}\n}\n",
	})
	sess := mcptest.Dial(context.Background(), t, mcpTransport(), dir)
	defer sess.Close()

	moveToShared(t, sess, "One", "Two")

	assertNoSourceImport(t, dir, "a/a_test.go")
	goVets(t, dir, "")
}

func TestMoveSymbol_AliasedSourceImportDroppedWhenLastReferenceIsGone(t *testing.T) {
	dir := moveUnusedImportFixture(t, map[string]string{
		"d/d.go": "package d\n\nimport s \"example.com/threadmcp/src\"\n\nfunc D() int { return s.One() + s.Two() }\n",
		"e/e.go": "package e\n\nimport s \"example.com/threadmcp/src\"\n\nfunc E() int { return s.One() + s.Two() + s.Keep() }\n",
	})
	sess := mcptest.Dial(context.Background(), t, mcpTransport(), dir)
	defer sess.Close()

	moveToShared(t, sess, "One", "Two")

	assertNoSourceImport(t, dir, "d/d.go")
	if got := mustRead(t, filepath.Join(dir, "e/e.go")); !strings.Contains(got, "s \"example.com/threadmcp/src\"") {
		t.Errorf("e/e.go lost the aliased source import:\n%s", got)
	}
	goVets(t, dir, "")
}

func TestMoveSymbol_BlankSourceImportIsLeftAlone(t *testing.T) {
	dir := moveUnusedImportFixture(t, map[string]string{
		"f/f.go": "package f\n\nimport (\n\t_ \"example.com/threadmcp/src\"\n)\n\nfunc F() {}\n",
		"g/g.go": "package g\n\nimport (\n\t_ \"example.com/threadmcp/src\"\n\t\"example.com/threadmcp/src\"\n)\n\nfunc G() int { return src.One() + src.Two() }\n",
	})
	sess := mcptest.Dial(context.Background(), t, mcpTransport(), dir)
	defer sess.Close()

	moveToShared(t, sess, "One", "Two")

	if got := mustRead(t, filepath.Join(dir, "g/g.go")); !strings.Contains(got, "_ \"example.com/threadmcp/src\"") {
		t.Errorf("g/g.go lost the blank import:\n%s", got)
	}
	goVets(t, dir, "")
}
