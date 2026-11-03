package tests_test

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/mamaar/gorefactor/tests/mcptest"
)

func moveSymbolCall(t *testing.T, sess *mcptest.Session, symbol, from, to string) *mcpsdk.CallToolResult {
	t.Helper()
	res, err := sess.CallTool(t.Context(), &mcpsdk.CallToolParams{
		Name:      "move_symbol",
		Arguments: map[string]any{"symbol": symbol, "from_package": from, "to_package": to},
	})
	if err != nil {
		t.Fatalf("CallTool(move_symbol): %v", err)
	}
	return res
}

func TestMoveSymbol_RemovesOnlyTheNamedDeclarationWhenAnotherNameExtendsIt(t *testing.T) {
	dir := writeInlineWorkspace(t, map[string]string{
		"src/src.go": "package src\n\ntype FooBar struct{ N int }\n\nfunc FooMaker() int { return 1 }\n\ntype Foo struct{ M int }\n\nfunc Fo() int { return 2 }\n\nfunc FooBarUse() FooBar { return FooBar{} }\n\nfunc Use() (Foo, int) { return Foo{}, FooMaker() }\n",
	})
	sess := mcptest.Dial(context.Background(), t, mcpTransport(), dir)
	defer sess.Close()

	for _, name := range []string{"Foo", "Fo"} {
		res := moveSymbolCall(t, sess, name, "src", "dst")
		if res.IsError {
			t.Fatalf("move %s: %s", name, errText(res))
		}
	}

	assertFileContains(t, dir, "src/src.go", "type FooBar struct")
	assertFileContains(t, dir, "src/src.go", "func FooMaker()")
	assertFileContains(t, dir, "dst/dst.go", "type Foo struct")
	assertFileContains(t, dir, "dst/dst.go", "func Fo()")
	got := fileText(t, dir, "src/src.go")
	if strings.Contains(got, "type Foo struct") || strings.Contains(got, "func Fo()") {
		t.Errorf("moved declarations remain in src:\n%s", got)
	}
	goVets(t, dir, "")
}

const nonReferencingIdentsSrc = `package src

type Package struct{ Name string }

type File struct {
	Package *Package
	Path    string
}

func (f File) Pkg() *Package { return f.Package }

func (f File) Item() int { return 1 }

func Make() File { return File{Package: &Package{Name: "x"}} }

func Shadow() int {
	Package := 3
	return Package + 1
}

func Labelled() int {
Package:
	for i := 0; i < 3; i++ {
		if i == 1 {
			break Package
		}
	}
	return 0
}
`

func TestMoveSymbol_LeavesFieldsKeysMethodsLabelsAndLocalsNamedLikeTheSymbol(t *testing.T) {
	dir := writeInlineWorkspace(t, map[string]string{
		"src/src.go": nonReferencingIdentsSrc,
		"a/a.go":     "package a\n\nimport \"example.com/threadmcp/src\"\n\nfunc A() string { f := src.Make(); return f.Package.Name + src.Package{}.Name }\n",
	})
	sess := mcptest.Dial(context.Background(), t, mcpTransport(), dir)
	defer sess.Close()

	res := moveSymbolCall(t, sess, "Package", "src", "dst")
	if res.IsError {
		t.Fatalf("move Package: %s", errText(res))
	}

	assertFileContains(t, dir, "dst/dst.go", "type Package struct")
	assertFileContains(t, dir, "src/src.go", "Package *dst.Package")
	assertFileContains(t, dir, "src/src.go", "File{Package: &dst.Package{")
	assertFileContains(t, dir, "src/src.go", "Package := 3")
	assertFileContains(t, dir, "a/a.go", "f.Package.Name + dst.Package{}.Name")
	goVets(t, dir, "")
}

func TestMoveSymbol_RefusesWhenMovedCodeUsesUnexportedMembersLeftBehind(t *testing.T) {
	const src = `package src

type Box struct{ n int }

func (b *Box) peek() int { return b.n }

const limit = 3

var counter int

type hidden struct{}

func helper() int { return 1 }

func UsesFunc() int { return helper() }

func UsesField(b *Box) int { return b.n }

func UsesMethod(b *Box) int { return b.peek() }

func UsesConst() int { return limit }

func UsesVar() int { return counter }

func UsesType() hidden { return hidden{} }
`
	for symbol, unexported := range map[string]string{
		"UsesFunc": "helper", "UsesField": "n", "UsesMethod": "peek",
		"UsesConst": "limit", "UsesVar": "counter", "UsesType": "hidden",
	} {
		t.Run(symbol, func(t *testing.T) {
			dir := writeInlineWorkspace(t, map[string]string{"src/src.go": src})
			sess := mcptest.Dial(context.Background(), t, mcpTransport(), dir)
			defer sess.Close()

			res := moveSymbolCall(t, sess, symbol, "src", "dst")

			if !res.IsError {
				t.Fatalf("move %s succeeded, want a refusal", symbol)
			}
			for _, want := range []string{symbol, unexported, "unexported"} {
				got := errText(res)
				if !strings.Contains(got, want) {
					t.Errorf("error %q does not mention %q", got, want)
				}
			}
			assertFileUnchanged(t, dir, "src/src.go", src)
		})
	}
}

func TestMoveSymbol_RefusesWhenCodeLeftBehindUsesAnUnexportedFieldOfTheMovedType(t *testing.T) {
	const src = "package src\n\ntype Box struct{ n int }\n\nfunc NewBox() *Box { return &Box{n: 1} }\n\nfunc Dump(b Box) int { return b.n }\n"
	dir := writeInlineWorkspace(t, map[string]string{"src/src.go": src})
	sess := mcptest.Dial(context.Background(), t, mcpTransport(), dir)
	defer sess.Close()

	res := moveSymbolCall(t, sess, "Box", "src", "dst")

	if !res.IsError || !strings.Contains(errText(res), "Dump") && !strings.Contains(errText(res), "stays in package") {
		t.Fatalf("move Box = error %v: %s, want a refusal naming the code left behind", res.IsError, errText(res))
	}
	assertFileUnchanged(t, dir, "src/src.go", src)
}

func TestMoveByDependencies_SkipsCandidateThatUsesAnUnexportedHelperAndMovesTheRest(t *testing.T) {
	dir := writeInlineWorkspace(t, map[string]string{
		"src/src.go": "package src\n\nfunc helper() int { return 1 }\n\nfunc NeedsHelper() int { return helper() }\n\nfunc Free() int { return 2 }\n",
		"a/a.go":     "package a\n\nimport \"example.com/threadmcp/src\"\n\nfunc A() int { return src.NeedsHelper() + src.Free() }\n",
		"b/b.go":     "package b\n\nimport \"example.com/threadmcp/src\"\n\nfunc B() int { return src.NeedsHelper() + src.Free() }\n",
	})
	sess := mcptest.Dial(context.Background(), t, mcpTransport(), dir)
	defer sess.Close()

	out := callJSON(t, sess, "move_by_dependencies", map[string]any{"move_shared_to": "shared"})

	sites, _ := json.Marshal(out["skipped_sites"])
	if !strings.Contains(string(sites), "NeedsHelper not moved") || !strings.Contains(string(sites), "helper") {
		t.Errorf("skipped_sites = %s, want NeedsHelper skipped for its unexported helper", sites)
	}
	assertFileContains(t, dir, "shared/shared.go", "func Free() int")
	assertFileContains(t, dir, "src/src.go", "func NeedsHelper()")
	goVets(t, dir, "")
}

const cycleSrc = "package src\n\nfunc Base() int { return 1 }\n\nfunc Shared() int { return Base() }\n\nfunc Use() int { return Shared() }\n"

func TestMoveSymbol_RefusesWhenTargetWouldImportASourceThatStillUsesTheSymbol(t *testing.T) {
	dir := writeInlineWorkspace(t, map[string]string{"src/src.go": cycleSrc})
	sess := mcptest.Dial(context.Background(), t, mcpTransport(), dir)
	defer sess.Close()

	res := moveSymbolCall(t, sess, "Shared", "src", "dst")

	if !res.IsError {
		t.Fatalf("move Shared succeeded, want an import cycle refusal")
	}
	for _, want := range []string{"import cycle", "dst", "src"} {
		got := errText(res)
		if !strings.Contains(got, want) {
			t.Errorf("error %q does not mention %q", got, want)
		}
	}
	assertFileUnchanged(t, dir, "src/src.go", cycleSrc)
}

func TestMoveSymbol_RefusesWhenAnExistingImportOfTheTargetReachesAReferrer(t *testing.T) {
	dir := writeInlineWorkspace(t, map[string]string{
		"src/src.go": "package src\n\nfunc Shared() int { return 1 }\n",
		"lib/lib.go": "package lib\n\nimport \"example.com/threadmcp/src\"\n\nfunc Lib() int { return src.Shared() }\n",
		"dst/dst.go": "package dst\n\nimport \"example.com/threadmcp/mid\"\n\nfunc Dst() int { return mid.Mid() }\n",
		"mid/mid.go": "package mid\n\nimport \"example.com/threadmcp/lib\"\n\nfunc Mid() int { return lib.Lib() }\n",
	})
	sess := mcptest.Dial(context.Background(), t, mcpTransport(), dir)
	defer sess.Close()

	res := moveSymbolCall(t, sess, "Shared", "src", "dst")

	if !res.IsError || !strings.Contains(errText(res), "import cycle") {
		t.Fatalf("move Shared = error %v: %s, want an import cycle refusal", res.IsError, errText(res))
	}
	for _, pkg := range []string{"dst", "mid", "lib"} {
		if !strings.Contains(errText(res), pkg) {
			t.Errorf("error %q does not name %s on the cycle", errText(res), pkg)
		}
	}
}

func TestMoveSymbol_QualifiesLeftBehindExportedSymbolsAndImportsTheSource(t *testing.T) {
	dir := writeInlineWorkspace(t, map[string]string{
		"src/src.go": "package src\n\nfunc Base() int { return 1 }\n\nconst Limit = 3\n\nfunc Shared() int { return Base() + Limit }\n",
		"a/a.go":     "package a\n\nimport \"example.com/threadmcp/src\"\n\nfunc A() int { return src.Shared() }\n",
	})
	sess := mcptest.Dial(context.Background(), t, mcpTransport(), dir)
	defer sess.Close()

	res := moveSymbolCall(t, sess, "Shared", "src", "dst")
	if res.IsError {
		t.Fatalf("move Shared: %s", errText(res))
	}

	assertFileContains(t, dir, "dst/dst.go", "src.Base() + src.Limit")
	assertFileContains(t, dir, "dst/dst.go", "example.com/threadmcp/src")
	goVets(t, dir, "")
}

func TestMoveByDependencies_SkipsCandidateWhoseSourceStillUsesItAndMovesTheRest(t *testing.T) {
	dir := writeInlineWorkspace(t, map[string]string{
		"src/src.go": cycleSrc + "\nfunc Free() int { return 2 }\n",
		"a/a.go":     "package a\n\nimport \"example.com/threadmcp/src\"\n\nfunc A() int { return src.Shared() + src.Free() }\n",
		"b/b.go":     "package b\n\nimport \"example.com/threadmcp/src\"\n\nfunc B() int { return src.Shared() + src.Free() }\n",
	})
	sess := mcptest.Dial(context.Background(), t, mcpTransport(), dir)
	defer sess.Close()

	out := callJSON(t, sess, "move_by_dependencies", map[string]any{"move_shared_to": "shared"})

	sites, _ := json.Marshal(out["skipped_sites"])
	if !strings.Contains(string(sites), "Shared not moved") || !strings.Contains(string(sites), "import cycle") {
		t.Errorf("skipped_sites = %s, want Shared skipped for an import cycle", sites)
	}
	assertFileContains(t, dir, "shared/shared.go", "func Free() int")
	assertFileContains(t, dir, "src/src.go", "func Shared()")
	goVets(t, dir, "")
}

func TestMoveByDependencies_SkipsTheSymbolWhoseCombinationWithAnotherClosesACycle(t *testing.T) {
	dir := writeInlineWorkspace(t, map[string]string{
		"types/types.go": "package types\n\ntype Thing struct{ N int }\n",
		"lib/lib.go":     "package lib\n\nimport \"example.com/threadmcp/types\"\n\nfunc Lib() types.Thing { return types.Thing{N: 1} }\n",
		"app/app.go":     "package app\n\nimport \"example.com/threadmcp/lib\"\n\nfunc New() int { return lib.Lib().N }\n",
		"x/x.go":         "package x\n\nimport \"example.com/threadmcp/types\"\n\nfunc X() types.Thing { return types.Thing{} }\n",
		"main/main.go":   "package main\n\nimport \"example.com/threadmcp/app\"\n\nfunc main() { _ = app.New() }\n",
		"y/y.go":         "package y\n\nimport \"example.com/threadmcp/app\"\n\nfunc Y() int { return app.New() }\n",
	})
	sess := mcptest.Dial(context.Background(), t, mcpTransport(), dir)
	defer sess.Close()

	out := callJSON(t, sess, "move_by_dependencies", map[string]any{"move_shared_to": "shared"})

	sites, _ := json.Marshal(out["skipped_sites"])
	if !strings.Contains(string(sites), "New not moved") || !strings.Contains(string(sites), "import cycle") {
		t.Errorf("skipped_sites = %s, want New skipped for an import cycle", sites)
	}
	assertFileContains(t, dir, "shared/shared.go", "type Thing struct")
	assertFileContains(t, dir, "app/app.go", "func New()")
	goVets(t, dir, "")
}

func TestMoveByDependencies_SkipsCandidateUsedInAFileNoConfigurationTypeChecks(t *testing.T) {
	dir := mbdWorkspace(t, map[string]string{
		"src/src.go": "package src\n\nfunc Shared() int { return 1 }\n\nfunc Free() int { return 2 }\n",
		"a/a.go":     "package a\n\nimport \"example.com/threadmcp/src\"\n\nfunc A() int { return src.Shared() + src.Free() }\n",
		"b/b.go":     "package b\n\nimport \"example.com/threadmcp/src\"\n\nfunc B() int { return src.Shared() + src.Free() }\n",
		"t/only.go":  "//go:build ignore\n\npackage main\n\nimport \"example.com/threadmcp/src\"\n\nfunc main() { _ = src.Shared() }\n",
	})
	sess := mcptest.Dial(context.Background(), t, mcpTransport(), dir)
	defer sess.Close()

	out := callJSON(t, sess, "move_by_dependencies", map[string]any{"move_shared_to": "shared"})

	sites, _ := json.Marshal(out["skipped_sites"])
	for _, want := range []string{"Shared not moved", "t/only.go"} {
		if !strings.Contains(string(sites), want) {
			t.Errorf("skipped_sites = %s, want it to mention %q", sites, want)
		}
	}
	assertFileContains(t, dir, "src/src.go", "func Shared()")
	assertFileContains(t, dir, "shared/shared.go", "func Free()")
	goVets(t, dir, "")
}

func TestMoveByDependencies_MovesCandidateUsedFromATestOnlyPackage(t *testing.T) {
	dir := mbdWorkspace(t, map[string]string{
		"src/src.go":     "package src\n\nfunc Shared() int { return 1 }\n",
		"a/a.go":         "package a\n\nimport \"example.com/threadmcp/src\"\n\nfunc A() int { return src.Shared() }\n",
		"b/b.go":         "package b\n\nimport \"example.com/threadmcp/src\"\n\nfunc B() int { return src.Shared() }\n",
		"t/only_test.go": "package t_test\n\nimport (\n\t\"testing\"\n\n\t\"example.com/threadmcp/src\"\n)\n\nfunc TestShared(t *testing.T) { _ = src.Shared() }\n",
	})
	sess := mcptest.Dial(context.Background(), t, mcpTransport(), dir)
	defer sess.Close()

	callJSON(t, sess, "move_by_dependencies", map[string]any{"move_shared_to": "shared"})

	assertFileContains(t, dir, "shared/shared.go", "func Shared()")
	assertFileContains(t, dir, "t/only_test.go", "shared.Shared()")
	goVets(t, dir, "")
}

func TestMoveSymbol_RefusesWhenAnImportOfTheMovedCodeReachesTheTargetAgain(t *testing.T) {
	srcFile := "package src\n\nimport \"example.com/threadmcp/x\"\n\nfunc Moved() int { return x.X() }\n"
	dir := writeInlineWorkspace(t, map[string]string{
		"src/src.go":   srcFile,
		"x/x.go":       "package x\n\nimport \"example.com/threadmcp/y\"\n\nfunc X() int { return y.Y() }\n",
		"y/y.go":       "package y\n\nimport \"example.com/threadmcp/base\"\n\nfunc Y() int { return base.B() }\n",
		"base/base.go": "package base\n\nfunc B() int { return 1 }\n",
	})
	sess := mcptest.Dial(context.Background(), t, mcpTransport(), dir)
	defer sess.Close()

	res := moveSymbolCall(t, sess, "Moved", "src", "base")

	if !res.IsError || !strings.Contains(errText(res), "import cycle") {
		t.Fatalf("move Moved = error %v: %s, want an import cycle refusal", res.IsError, errText(res))
	}
	want := "threadmcp/base -> example.com/threadmcp/x -> example.com/threadmcp/y -> example.com/threadmcp/base"
	if !strings.Contains(errText(res), want) {
		t.Errorf("error %q does not name the path %q", errText(res), want)
	}
	assertFileUnchanged(t, dir, "src/src.go", srcFile)
}
