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
	res, err := sess.CallTool(context.Background(), &mcpsdk.CallToolParams{
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
		if res := moveSymbolCall(t, sess, name, "src", "dst"); res.IsError {
			t.Fatalf("move %s: %s", name, errText(res))
		}
	}

	assertFileContains(t, dir, "src/src.go", "type FooBar struct")
	assertFileContains(t, dir, "src/src.go", "func FooMaker()")
	assertFileContains(t, dir, "dst/dst.go", "type Foo struct")
	assertFileContains(t, dir, "dst/dst.go", "func Fo()")
	if got := fileText(t, dir, "src/src.go"); strings.Contains(got, "type Foo struct") || strings.Contains(got, "func Fo()") {
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

	if res := moveSymbolCall(t, sess, "Package", "src", "dst"); res.IsError {
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
				if got := errText(res); !strings.Contains(got, want) {
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
