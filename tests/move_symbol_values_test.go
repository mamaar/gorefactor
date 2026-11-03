package tests_test

import (
	"context"
	"strings"
	"testing"

	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/mamaar/gorefactor/tests/mcptest"
)

func moveSymbolsCall(t *testing.T, sess *mcptest.Session, from, to string, symbols ...string) *mcpsdk.CallToolResult {
	t.Helper()
	return callTool(t, sess, "move_symbol", map[string]any{"symbols": symbols, "from_package": from, "to_package": to})
}

func assertFileOmits(t *testing.T, dir, rel, unwanted string) {
	t.Helper()
	if got := fileTextOrEmpty(t, dir, rel); strings.Contains(got, unwanted) {
		t.Errorf("%s contains %q:\n%s", rel, unwanted, got)
	}
}

func assertRefused(t *testing.T, res *mcpsdk.CallToolResult, wants ...string) {
	t.Helper()
	if !res.IsError {
		t.Fatalf("move succeeded, want a refusal mentioning %q", wants)
	}
	for _, want := range wants {
		if !strings.Contains(errText(res), want) {
			t.Errorf("refusal = %s, want it to mention %q", errText(res), want)
		}
	}
}

func TestMoveSymbol_MovesAConstantAndAVariableWithTheirDocAndRewritesReferrers(t *testing.T) {
	const src = "package src\n\n// Limit caps the count.\nconst Limit = 3\n\n// Counter counts.\nvar Counter int\n"
	dir := writeInlineWorkspace(t, map[string]string{
		"src/src.go": src,
		"a/a.go":     "package a\n\nimport \"example.com/threadmcp/src\"\n\nfunc A() int { src.Counter++; return src.Limit + src.Counter }\n",
	})
	sess := mcptest.Dial(context.Background(), t, mcpTransport(), dir)
	defer sess.Close()

	for _, name := range []string{"Limit", "Counter"} {
		if res := moveSymbolCall(t, sess, name, "src", "dst"); res.IsError {
			t.Fatalf("move %s: %s", name, errText(res))
		}
	}

	assertFileContains(t, dir, "dst/dst.go", "// Limit caps the count.\nconst Limit = 3")
	assertFileContains(t, dir, "dst/dst.go", "// Counter counts.\nvar Counter int")
	assertFileContains(t, dir, "a/a.go", "dst.Counter++; return dst.Limit + dst.Counter")
	assertFileOmits(t, dir, "src/src.go", "Limit")
	assertFileOmits(t, dir, "src/src.go", "Counter")
	goVets(t, dir, "")
}

func TestMoveSymbol_LiftsOneConstantOutOfASelfContainedGroup(t *testing.T) {
	dir := writeInlineWorkspace(t, map[string]string{
		"src/src.go": "package src\n\nconst (\n\t// A is one.\n\tA = 1\n\tB = 2\n)\n",
		"a/a.go":     "package a\n\nimport \"example.com/threadmcp/src\"\n\nfunc F() int { return src.A + src.B }\n",
	})
	sess := mcptest.Dial(context.Background(), t, mcpTransport(), dir)
	defer sess.Close()

	if res := moveSymbolCall(t, sess, "A", "src", "dst"); res.IsError {
		t.Fatalf("move A: %s", errText(res))
	}

	assertFileContains(t, dir, "dst/dst.go", "// A is one.\nconst A = 1")
	assertFileContains(t, dir, "src/src.go", "const (\n\tB = 2\n)")
	assertFileOmits(t, dir, "src/src.go", "A = 1")
	assertFileContains(t, dir, "a/a.go", "dst.A + src.B")
	goVets(t, dir, "")
}

func TestMoveSymbol_RemovesASelfContainedGroupWhenAllItsConstantsMove(t *testing.T) {
	dir := writeInlineWorkspace(t, map[string]string{
		"src/src.go": "package src\n\nfunc Keep() int { return 1 }\n\nconst (\n\tA = 1\n\tB = 2\n)\n",
		"a/a.go":     "package a\n\nimport \"example.com/threadmcp/src\"\n\nfunc F() int { return src.A + src.B }\n",
	})
	sess := mcptest.Dial(context.Background(), t, mcpTransport(), dir)
	defer sess.Close()

	if res := moveSymbolsCall(t, sess, "src", "dst", "A", "B"); res.IsError {
		t.Fatalf("move A, B: %s", errText(res))
	}

	assertFileUnchanged(t, dir, "src/src.go", "package src\n\nfunc Keep() int { return 1 }\n")
	assertFileContains(t, dir, "dst/dst.go", "const A = 1")
	assertFileContains(t, dir, "dst/dst.go", "const B = 2")
	assertFileContains(t, dir, "a/a.go", "dst.A + dst.B")
	goVets(t, dir, "")
}

const iotaSrc = "package src\n\ntype Kind int\n\n// Kinds of thing.\nconst (\n\tA Kind = iota\n\tB\n\tC\n)\n"

func TestMoveSymbol_RefusesPartOfAnIotaGroupNamingTheRest(t *testing.T) {
	dir := writeInlineWorkspace(t, map[string]string{"src/src.go": iotaSrc})
	sess := mcptest.Dial(context.Background(), t, mcpTransport(), dir)
	defer sess.Close()

	res := moveSymbolCall(t, sess, "B", "src", "dst")

	assertRefused(t, res, "cannot move B: it is declared in an iota group with A, C; move them together")
	assertFileUnchanged(t, dir, "src/src.go", iotaSrc)
}

func TestMoveSymbol_MovesAWholeIotaGroupVerbatimAndTheTargetImportsTheTypeLeftBehind(t *testing.T) {
	dir := writeInlineWorkspace(t, map[string]string{
		"src/src.go": iotaSrc,
		"a/a.go":     "package a\n\nimport \"example.com/threadmcp/src\"\n\nfunc F() src.Kind { return src.B }\n",
	})
	sess := mcptest.Dial(context.Background(), t, mcpTransport(), dir)
	defer sess.Close()

	if res := moveSymbolsCall(t, sess, "src", "dst", "A", "B", "C"); res.IsError {
		t.Fatalf("move A, B, C: %s", errText(res))
	}

	assertFileContains(t, dir, "dst/dst.go", "// Kinds of thing.\nconst (\n\tA src.Kind = iota\n\tB\n\tC\n)")
	assertFileContains(t, dir, "dst/dst.go", "example.com/threadmcp/src")
	assertFileOmits(t, dir, "src/src.go", "iota")
	assertFileContains(t, dir, "a/a.go", "dst.B")
	goVets(t, dir, "")
}

func TestMoveSymbol_RefusesAnIotaGroupAsAnImportCycleWhenTheTypeLeftBehindUsesIt(t *testing.T) {
	const src = iotaSrc + "\nfunc (k Kind) IsA() bool { return k == A }\n"
	dir := writeInlineWorkspace(t, map[string]string{"src/src.go": src})
	sess := mcptest.Dial(context.Background(), t, mcpTransport(), dir)
	defer sess.Close()

	res := moveSymbolsCall(t, sess, "src", "dst", "A", "B", "C")

	assertRefused(t, res, "import cycle")
	assertFileUnchanged(t, dir, "src/src.go", src)
}

func TestMoveSymbol_MovesATypeWithItsMethodAndTheConstantsTheMethodUses(t *testing.T) {
	const src = iotaSrc + "\nfunc (k Kind) IsA() bool { return k == A }\n"
	dir := writeInlineWorkspace(t, map[string]string{
		"src/src.go": src,
		"a/a.go":     "package a\n\nimport \"example.com/threadmcp/src\"\n\nfunc F() bool { return src.Kind(1).IsA() || src.B == src.C }\n",
	})
	sess := mcptest.Dial(context.Background(), t, mcpTransport(), dir)
	defer sess.Close()

	if res := moveSymbolsCall(t, sess, "src", "dst", "Kind", "A", "B", "C"); res.IsError {
		t.Fatalf("move Kind, A, B, C: %s", errText(res))
	}

	assertFileContains(t, dir, "dst/dst.go", "A Kind = iota")
	assertFileContains(t, dir, "dst/dst.go", "func (k Kind) IsA() bool")
	assertFileContains(t, dir, "a/a.go", "dst.Kind(1).IsA() || dst.B == dst.C")
	goVets(t, dir, "")
}

func TestMoveSymbol_RefusesATypeAloneAsAnImportCycleWhenItsMethodUsesConstantsLeftBehind(t *testing.T) {
	const src = iotaSrc + "\nfunc (k Kind) IsA() bool { return k == A }\n"
	dir := writeInlineWorkspace(t, map[string]string{"src/src.go": src})
	sess := mcptest.Dial(context.Background(), t, mcpTransport(), dir)
	defer sess.Close()

	res := moveSymbolCall(t, sess, "Kind", "src", "dst")

	assertRefused(t, res, "import cycle")
	assertFileUnchanged(t, dir, "src/src.go", src)
}

func TestMoveSymbol_MovesVariablesDeclaredTogetherOnlyTogether(t *testing.T) {
	const src = "package src\n\nfunc Pair() (int, int) { return 1, 2 }\n\nvar a, b = Pair()\n"
	dir := writeInlineWorkspace(t, map[string]string{"src/src.go": src})
	sess := mcptest.Dial(context.Background(), t, mcpTransport(), dir)
	defer sess.Close()

	res := moveSymbolCall(t, sess, "a", "src", "dst")
	assertRefused(t, res, "cannot move a: it is declared together with b (var a, b = Pair()); move them together")
	assertFileUnchanged(t, dir, "src/src.go", src)

	if res := moveSymbolsCall(t, sess, "src", "dst", "a", "b"); res.IsError {
		t.Fatalf("move a, b: %s", errText(res))
	}
	assertFileContains(t, dir, "dst/dst.go", "var a, b = src.Pair()")
	assertFileUnchanged(t, dir, "src/src.go", "package src\n\nfunc Pair() (int, int) { return 1, 2 }\n")
	goVets(t, dir, "")
}

func TestMoveSymbol_RefusesAVariableWhoseInitializerCallsAnUnexportedFunctionLeftBehind(t *testing.T) {
	const src = "package src\n\nfunc build() int { return 1 }\n\nvar X = build()\n"
	dir := writeInlineWorkspace(t, map[string]string{"src/src.go": src})
	sess := mcptest.Dial(context.Background(), t, mcpTransport(), dir)
	defer sess.Close()

	res := moveSymbolCall(t, sess, "X", "src", "dst")

	assertRefused(t, res, "X cannot move", "build")
	assertFileUnchanged(t, dir, "src/src.go", src)
}

func TestMoveSymbol_CarriesBlankNamesAlongAndRefusesToMoveThem(t *testing.T) {
	const src = "package src\n\nfunc Pair() (int, int) { return 1, 2 }\n\nvar _, b = Pair()\n\nvar (\n\tx int\n\t_ = Pair\n)\n"
	dir := writeInlineWorkspace(t, map[string]string{"src/src.go": src})
	sess := mcptest.Dial(context.Background(), t, mcpTransport(), dir)
	defer sess.Close()

	assertRefused(t, moveSymbolCall(t, sess, "_", "src", "dst"), "_")
	assertRefused(t, moveSymbolsCall(t, sess, "src", "dst", "b", "_"), "_")
	assertFileUnchanged(t, dir, "src/src.go", src)

	for _, name := range []string{"b", "x"} {
		if res := moveSymbolCall(t, sess, name, "src", "dst"); res.IsError {
			t.Fatalf("move %s: %s", name, errText(res))
		}
	}

	assertFileContains(t, dir, "dst/dst.go", "var _, b = src.Pair()")
	assertFileContains(t, dir, "dst/dst.go", "var x int")
	assertFileContains(t, dir, "src/src.go", "_ = Pair")
	assertFileOmits(t, dir, "src/src.go", "x int")
	assertFileOmits(t, dir, "src/src.go", "_, b")
	goVets(t, dir, "")
}

func TestMoveSymbol_RewritesAConstantUsedInInPackageAndExternalTestFiles(t *testing.T) {
	dir := writeInlineWorkspace(t, map[string]string{
		"src/src.go":      "package src\n\nconst Limit = 3\n",
		"src/src_test.go": "package src\n\nimport \"testing\"\n\nfunc TestIn(t *testing.T) {\n\tif Limit != 3 {\n\t\tt.Fatal()\n\t}\n}\n",
		"src/ext_test.go": "package src_test\n\nimport (\n\t\"testing\"\n\n\t\"example.com/threadmcp/src\"\n)\n\nfunc TestOut(t *testing.T) {\n\tif src.Limit != 3 {\n\t\tt.Fatal()\n\t}\n}\n",
	})
	sess := mcptest.Dial(context.Background(), t, mcpTransport(), dir)
	defer sess.Close()

	if res := moveSymbolCall(t, sess, "Limit", "src", "dst"); res.IsError {
		t.Fatalf("move Limit: %s", errText(res))
	}

	assertFileContains(t, dir, "src/src_test.go", "dst.Limit != 3")
	assertFileContains(t, dir, "src/ext_test.go", "dst.Limit != 3")
	goVets(t, dir, "")
}

func TestMoveSymbol_RefusesAConstantDeclaredInAGeneratedFile(t *testing.T) {
	const src = "// Code generated by gen. DO NOT EDIT.\n\npackage src\n\nconst Limit = 3\n"
	dir := writeInlineWorkspace(t, map[string]string{"src/gen.go": src})
	sess := mcptest.Dial(context.Background(), t, mcpTransport(), dir)
	defer sess.Close()

	res := moveSymbolCall(t, sess, "Limit", "src", "dst")

	assertRefused(t, res, "generated file")
	assertFileUnchanged(t, dir, "src/gen.go", src)
}

func TestMoveSymbol_RequiresExactlyOneOfSymbolAndSymbols(t *testing.T) {
	dir := writeInlineWorkspace(t, map[string]string{"src/src.go": "package src\n\nconst Limit = 3\n"})
	sess := mcptest.Dial(context.Background(), t, mcpTransport(), dir)
	defer sess.Close()

	both := callTool(t, sess, "move_symbol", map[string]any{"symbol": "Limit", "symbols": []string{"Limit"}, "from_package": "src", "to_package": "dst"})
	neither := callTool(t, sess, "move_symbol", map[string]any{"from_package": "src", "to_package": "dst"})

	assertRefused(t, both, "symbol", "symbols")
	assertRefused(t, neither, "symbol", "symbols")
	assertFileUnchanged(t, dir, "src/src.go", "package src\n\nconst Limit = 3\n")
}

func TestMoveByDependencies_MovesASharedConstantAndSkipsOneFromAnIotaGroup(t *testing.T) {
	const src = "package src\n\nconst Limit = 3\n\ntype Kind int\n\nconst (\n\tA Kind = iota\n\tB\n)\n"
	dir := writeInlineWorkspace(t, map[string]string{
		"src/src.go": src,
		"a/a.go":     "package a\n\nimport \"example.com/threadmcp/src\"\n\nfunc F() (int, int) { return src.Limit, int(src.A) }\n",
		"b/b.go":     "package b\n\nimport \"example.com/threadmcp/src\"\n\nfunc G() (int, int) { return src.Limit, int(src.A) }\n",
	})
	sess := mcptest.Dial(context.Background(), t, mcpTransport(), dir)
	defer sess.Close()

	out := callJSON(t, sess, "move_by_dependencies", map[string]any{"move_shared_to": "shared"})

	sites := strings.Join(skippedReasons(out), "\n")
	if !strings.Contains(sites, "A not moved: ") || !strings.Contains(sites, "cannot move A: it is declared in an iota group with B; move them together") {
		t.Errorf("skipped_sites = %s, want A skipped as part of an iota group", sites)
	}
	assertFileContains(t, dir, "shared/shared.go", "const Limit = 3")
	assertFileContains(t, dir, "a/a.go", "shared.Limit, int(src.A)")
	assertFileContains(t, dir, "src/src.go", "A Kind = iota")
	goVets(t, dir, "")
}

func skippedReasons(out map[string]any) []string {
	var reasons []string
	sites, _ := out["skipped_sites"].([]any)
	for _, s := range sites {
		if m, ok := s.(map[string]any); ok {
			if r, ok := m["reason"].(string); ok {
				reasons = append(reasons, r)
			}
		}
	}
	return reasons
}
