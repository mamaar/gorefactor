package tests_test

import (
	"context"
	"strings"
	"testing"

	"github.com/mamaar/gorefactor/tests/mcptest"
)

const sdsA = "package p\n\nfunc helper() int { return 1 }\n\nfunc Live() int { return helper() }\n"
const sdsB = "package p\n\ntype V struct{}\n\nfunc (V) helper() int { return 2 }\n"

func TestSafeDelete_BareNameResolvesToTheDeclarationInSourceFile(t *testing.T) {
	dir := writeInlineWorkspace(t, map[string]string{"p/a.go": sdsA, "p/b.go": sdsB})

	sess := mcptest.Dial(context.Background(), t, mcpTransport(), dir)
	defer sess.Close()

	callJSON(t, sess, "safe_delete", map[string]any{"symbol": "helper", "source_file": "p/b.go"})

	assertFileUnchanged(t, dir, "p/a.go", sdsA)
	assertFileOmits(t, dir, "p/b.go", "helper")
}

func TestSafeDelete_BareNameInAFileWithFunctionAndMethodDeletesTheFunction(t *testing.T) {
	const both = "package p\n\ntype V struct{}\n\nfunc (V) helper() int { return 2 }\n\nfunc helper() int { return 1 }\n"
	dir := writeInlineWorkspace(t, map[string]string{"p/a.go": both})

	sess := mcptest.Dial(context.Background(), t, mcpTransport(), dir)
	defer sess.Close()

	callJSON(t, sess, "safe_delete", map[string]any{"symbol": "helper", "source_file": "p/a.go"})

	assertFileUnchanged(t, dir, "p/a.go", "package p\n\ntype V struct{}\n\nfunc (V) helper() int { return 2 }\n")
}

func TestSafeDelete_RefusesABareNameOfMethodsOnSeveralTypesInSourceFile(t *testing.T) {
	const both = "package p\n\ntype V struct{}\n\ntype W struct{}\n\nfunc (V) helper() int { return 2 }\n\nfunc (W) helper() int { return 1 }\n"
	dir := writeInlineWorkspace(t, map[string]string{"p/a.go": both})

	sess := mcptest.Dial(context.Background(), t, mcpTransport(), dir)
	defer sess.Close()

	res := callTool(t, sess, "safe_delete", map[string]any{"symbol": "helper", "source_file": "p/a.go", "dry_run": true})

	if !res.IsError || !strings.Contains(errText(res), "ambiguous") || !strings.Contains(errText(res), "V.helper") {
		t.Fatalf("want an ambiguity refusal naming V.helper, got error=%v: %s", res.IsError, errText(res))
	}
}

func TestSafeDelete_RemovesTheTrailingCommentOfAConstSpec(t *testing.T) {
	const src = "package p\n\nconst (\n\t// First is one.\n\tFirst = 1 // one\n\tSecond = 2 // two\n)\n"
	dir := writeInlineWorkspace(t, map[string]string{"p/c.go": src})

	sess := mcptest.Dial(context.Background(), t, mcpTransport(), dir)
	defer sess.Close()

	callJSON(t, sess, "safe_delete", map[string]any{"symbol": "First", "source_file": "p/c.go"})

	assertFileUnchanged(t, dir, "p/c.go", "package p\n\nconst (\n\tSecond = 2 // two\n)\n")
}
