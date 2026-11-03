package tests_test

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/mamaar/gorefactor/tests/mcptest"
)

// crossTestImportWorkspace has two packages whose in-package tests import each
// other, so one of them is type-checked while its own check is on the stack.
func crossTestImportWorkspace() map[string]string {
	return map[string]string{
		"a/a.go":      "package a\n\nfunc Foo() int { return 1 }\n",
		"a/a_test.go": "package a\n\nimport (\n\t\"testing\"\n\n\t\"example.com/threadmcp/b\"\n)\n\nfunc TestA(t *testing.T) { _ = b.B() }\n",
		"b/b.go":      "package b\n\nfunc B() int { return 2 }\n",
		"b/b_test.go": "package b\n\nimport (\n\t\"testing\"\n\n\t\"example.com/threadmcp/a\"\n)\n\nfunc TestB(t *testing.T) { _ = a.Foo() }\n",
		"c/c.go":      "package c\n\nimport \"example.com/threadmcp/a\"\n\nfunc C() int { return a.Foo() }\n\nfunc init() { _ = C() }\n",
	}
}

func TestRenameSymbol_RenamesReferencesInPackagesReachedDuringAnotherPackagesCheck(t *testing.T) {
	dir := writeInlineWorkspace(t, crossTestImportWorkspace())

	sess := mcptest.Dial(context.Background(), t, mcpTransport(), dir)
	defer sess.Close()

	callJSON(t, sess, "rename_symbol", map[string]any{"symbol": "Foo", "new_name": "Bar", "package": "a"})

	assertFileContains(t, dir, "c/c.go", "a.Bar()")
	assertFileContains(t, dir, "b/b_test.go", "a.Bar()")
	goVets(t, dir, "")
}

func TestMoveSymbol_UpdatesReferencesInPackagesReachedDuringAnotherPackagesCheck(t *testing.T) {
	dir := writeInlineWorkspace(t, crossTestImportWorkspace())

	sess := mcptest.Dial(context.Background(), t, mcpTransport(), dir)
	defer sess.Close()

	res := moveSymbolCall(t, sess, "Foo", "a", "dst")
	if res.IsError {
		t.Fatalf("move_symbol: %s", errText(res))
	}

	assertFileContains(t, dir, "c/c.go", "dst.Foo()")
	assertFileContains(t, dir, "b/b_test.go", "dst.Foo()")
	goVets(t, dir, "")
}

func TestSafeDelete_BlocksOnReferencesInPackagesReachedDuringAnotherPackagesCheck(t *testing.T) {
	files := crossTestImportWorkspace()
	dir := writeInlineWorkspace(t, files)

	sess := mcptest.Dial(context.Background(), t, mcpTransport(), dir)
	defer sess.Close()

	res, text := callSafeDelete(context.Background(), t, sess, "Foo", "a/a.go", false, false)

	if !res.IsError || !strings.Contains(text, "found 2 references") {
		t.Errorf("safe_delete = %q (IsError %v), want it refused with the 2 references in c and b_test", text, res.IsError)
	}

	assertFileUnchanged(t, dir, "a/a.go", files["a/a.go"])
}

func TestUnused_DoesNotListSymbolUsedFromPackagesReachedDuringAnotherPackagesCheck(t *testing.T) {
	dir := writeInlineWorkspace(t, crossTestImportWorkspace())

	sess := mcptest.Dial(context.Background(), t, mcpTransport(), dir)
	defer sess.Close()

	out := callJSON(t, sess, "unused", map[string]any{"include_exported": true})

	text, _ := json.Marshal(out["unused_symbols"])
	if strings.Contains(string(text), `"Foo"`) {
		t.Errorf("unused lists Foo, which the live C in c uses: %s", text)
	}
}
