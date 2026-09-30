package tests_test

import (
	"context"
	"testing"

	"github.com/mamaar/gorefactor/tests/mcptest"
)

func embeddedTypeWorkspace() map[string]string {
	return map[string]string{
		"a/a.go": `package a

type Base struct{ N int }

type D struct{ Base }

type P struct{ *Base }

func Make() D { return D{Base: Base{N: 1}} }

func (d D) Get() int { return d.Base.N }
`,
		"b/b.go": `package b

import "example.com/threadmcp/a"

func Get(p *a.P, d a.D) int {
	return p.Base.N + d.Base.N + p.N + a.Make().Base.N + len([]a.D{{Base: a.Base{}}})
}
`,
	}
}

func TestRenameSymbol_RenamesTheFieldNameOfAnEmbeddedType(t *testing.T) {
	dir := writeInlineWorkspace(t, embeddedTypeWorkspace())
	sess := mcptest.Dial(context.Background(), t, mcpTransport(), dir)
	defer sess.Close()

	callJSON(t, sess, "rename_symbol", map[string]any{"symbol": "Base", "new_name": "Core", "package": "a"})

	assertFileContains(t, dir, "a/a.go", "D{Core: Core{N: 1}}")
	assertFileContains(t, dir, "a/a.go", "d.Core.N")
	assertFileContains(t, dir, "b/b.go", "p.Core.N + d.Core.N + p.N + a.Make().Core.N")
	assertFileContains(t, dir, "b/b.go", "{Core: a.Core{}}")
	goVets(t, dir, "")
}

func TestMoveSymbol_KeepsTheFieldNameOfAnEmbeddedType(t *testing.T) {
	dir := writeInlineWorkspace(t, embeddedTypeWorkspace())
	sess := mcptest.Dial(context.Background(), t, mcpTransport(), dir)
	defer sess.Close()

	if res := moveSymbolCall(t, sess, "Base", "a", "dst"); res.IsError {
		t.Fatalf("move_symbol: %s", errText(res))
	}

	assertFileContains(t, dir, "a/a.go", "D{Base: dst.Base{N: 1}}")
	assertFileContains(t, dir, "b/b.go", "p.Base.N + d.Base.N")
	goVets(t, dir, "")
}
