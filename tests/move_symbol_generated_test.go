package tests_test

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/mamaar/gorefactor/tests/mcptest"
)

const (
	msgKind       = "package src\n\ntype Kind int\n"
	msgKindString = genHeader + "package src\n\nfunc (k Kind) String() string { return \"kind\" }\n"
)

func requireRefusal(t *testing.T, res *mcpsdk.CallToolResult, wants ...string) {
	t.Helper()
	if !res.IsError {
		t.Fatalf("move succeeded, want a refusal naming %v", wants)
	}
	got := errText(res)
	for _, want := range wants {
		if !strings.Contains(got, want) {
			t.Errorf("error = %q, want it to mention %q", got, want)
		}
	}
}

func TestMoveSymbol_TypeWithMethodInGeneratedFileIsRefusedAtPlanTime(t *testing.T) {
	files := map[string]string{
		"src/kind.go":        msgKind,
		"src/kind_string.go": msgKindString,
	}
	dir := writeInlineWorkspace(t, files)
	sess := mcptest.Dial(context.Background(), t, mcpTransport(), dir)
	defer sess.Close()

	res := moveSymbolCall(t, sess, "Kind", "src", "dst")

	requireRefusal(t, res, "cannot move Kind", "String", "src/kind_string.go", "generated", "regenerate")
	assertFileUnchanged(t, dir, "src/kind.go", msgKind)
	assertFileUnchanged(t, dir, "src/kind_string.go", msgKindString)
}

func TestMoveSymbol_DryRunOfTypeWithGeneratedMethodReportsErrorNotPreview(t *testing.T) {
	dir := writeInlineWorkspace(t, map[string]string{
		"src/kind.go":        msgKind,
		"src/kind_string.go": msgKindString,
	})
	sess := mcptest.Dial(context.Background(), t, mcpTransport(), dir)
	defer sess.Close()

	res, err := sess.CallTool(context.Background(), &mcpsdk.CallToolParams{
		Name:      "move_symbol",
		Arguments: map[string]any{"symbol": "Kind", "from_package": "src", "to_package": "dst", "dry_run": true},
	})
	if err != nil {
		t.Fatal(err)
	}

	requireRefusal(t, res, "cannot move Kind", "kind_string.go", "generated")
}

func TestMoveSymbol_ReferenceInGeneratedFileIsRefusedNamingSymbolAndFile(t *testing.T) {
	genRef := genHeader + "package a\n\nimport \"example.com/threadmcp/src\"\n\nfunc Gen() int { return src.Shared() }\n"
	dir := mbdWorkspace(t, map[string]string{"a/zz_gen.go": genRef})
	sess := mcptest.Dial(context.Background(), t, mcpTransport(), dir)
	defer sess.Close()

	res := moveSymbolCall(t, sess, "Shared", "src", "dst")

	requireRefusal(t, res, "cannot move Shared", "a/zz_gen.go", "generated")
	assertFileUnchanged(t, dir, "src/src.go", mbdSrc)
	assertFileUnchanged(t, dir, "a/zz_gen.go", genRef)
}

func TestMoveSymbol_ReferenceInGeneratedTestFileIsRefused(t *testing.T) {
	genRef := genHeader + "package a\n\nimport \"example.com/threadmcp/src\"\n\nfunc gen() int { return src.Shared() }\n"
	dir := mbdWorkspace(t, map[string]string{"a/zz_gen_test.go": genRef})
	sess := mcptest.Dial(context.Background(), t, mcpTransport(), dir)
	defer sess.Close()

	res := moveSymbolCall(t, sess, "Shared", "src", "dst")

	requireRefusal(t, res, "cannot move Shared", "a/zz_gen_test.go", "generated")
	assertFileUnchanged(t, dir, "a/zz_gen_test.go", genRef)
}

func TestMoveByDependencies_CandidateReferencedFromGeneratedFileIsSkippedAndTheRestMove(t *testing.T) {
	dir := writeInlineWorkspace(t, map[string]string{
		"src/src.go":  "package src\n\nfunc Shared() int { return 1 }\n\nfunc Other() int { return 2 }\n",
		"a/a.go":      "package a\n\nimport \"example.com/threadmcp/src\"\n\nfunc A() int { return src.Shared() + src.Other() }\n",
		"b/b.go":      "package b\n\nimport \"example.com/threadmcp/src\"\n\nfunc B() int { return src.Shared() + src.Other() }\n",
		"a/zz_gen.go": genHeader + "package a\n\nimport \"example.com/threadmcp/src\"\n\nfunc Gen() int { return src.Other() }\n",
	})
	sess := mcptest.Dial(context.Background(), t, mcpTransport(), dir)
	defer sess.Close()

	out := callJSON(t, sess, "move_by_dependencies", map[string]any{"move_shared_to": "shared"})

	sites, _ := json.Marshal(out["skipped_sites"])
	for _, want := range []string{"Other", "a/zz_gen.go", "generated"} {
		if !strings.Contains(string(sites), want) {
			t.Errorf("skipped_sites = %s, want it to mention %q", sites, want)
		}
	}
	assertFileContains(t, dir, "shared/shared.go", "func Shared() int")
	if got := fileText(t, dir, "shared/shared.go"); strings.Contains(got, "Other") {
		t.Errorf("Other moved:\n%s", got)
	}
	assertFileContains(t, dir, "src/src.go", "func Other() int")
	goVets(t, dir, "")
}

func TestMoveSymbol_TargetFileSkipsGeneratedFile(t *testing.T) {
	dir := writeInlineWorkspace(t, map[string]string{
		"src/src.go":   "package src\n\nfunc F() int { return 1 }\n",
		"dst/a_gen.go": genHeader + "package dst\n\nfunc G() int { return 2 }\n",
		"dst/b.go":     "package dst\n\nfunc H() int { return 3 }\n",
	})
	sess := mcptest.Dial(context.Background(), t, mcpTransport(), dir)
	defer sess.Close()

	if res := moveSymbolCall(t, sess, "F", "src", "dst"); res.IsError {
		t.Fatalf("move F: %s", errText(res))
	}

	assertFileContains(t, dir, "dst/b.go", "func F() int")
	assertFileUnchanged(t, dir, "dst/a_gen.go", genHeader+"package dst\n\nfunc G() int { return 2 }\n")
	goVets(t, dir, "")
}

func TestMoveSymbol_TargetPackageWithOnlyGeneratedFileGetsNewFile(t *testing.T) {
	gen := genHeader + "package dst\n\nfunc G() int { return 2 }\n"
	dir := writeInlineWorkspace(t, map[string]string{
		"src/src.go":   "package src\n\nfunc F() int { return 1 }\n",
		"dst/a_gen.go": gen,
	})
	sess := mcptest.Dial(context.Background(), t, mcpTransport(), dir)
	defer sess.Close()

	if res := moveSymbolCall(t, sess, "F", "src", "dst"); res.IsError {
		t.Fatalf("move F: %s", errText(res))
	}

	assertFileContains(t, dir, "dst/dst.go", "func F() int")
	assertFileUnchanged(t, dir, "dst/a_gen.go", gen)
	goVets(t, dir, "")
}

func TestMoveSymbol_ReferenceInTaggedGeneratedFileIsRefused(t *testing.T) {
	genRef := "//go:build tagx\n\n" + genHeader + "package a\n\nimport \"example.com/threadmcp/src\"\n\nfunc Gen() int { return src.Shared() }\n"
	dir := mbdWorkspace(t, map[string]string{"a/zz_gen.go": genRef})
	sess := mcptest.Dial(context.Background(), t, mcpTransport(), dir)
	defer sess.Close()

	res := moveSymbolCall(t, sess, "Shared", "src", "dst")

	requireRefusal(t, res, "Shared", "a/zz_gen.go", "generated")
	assertFileUnchanged(t, dir, "src/src.go", mbdSrc)
	assertFileUnchanged(t, dir, "a/zz_gen.go", genRef)
}
