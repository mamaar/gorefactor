package tests_test

import (
	"context"
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
