package tests_test

import (
	"context"
	"testing"

	"github.com/mamaar/gorefactor/tests/mcptest"
)

func TestMoveSymbol_RefusesWhenACompanionConstructorExistsInTheTarget(t *testing.T) {
	const dstSrc = "package dst\n\nfunc NewFoo() int { return 1 }\n"
	cases := map[string]map[string]string{
		"same file": {
			"src/src.go": "package src\n\ntype Foo struct{ n int }\n\nfunc NewFoo() *Foo { return &Foo{n: 1} }\n",
		},
		"other file": {
			"src/src.go": "package src\n\ntype Foo struct{ n int }\n",
			"src/new.go": "package src\n\nfunc NewFoo() *Foo { return &Foo{n: 1} }\n",
		},
	}
	for name, files := range cases {
		t.Run(name, func(t *testing.T) {
			files["dst/dst.go"] = dstSrc
			dir := writeInlineWorkspace(t, files)
			sess := mcptest.Dial(context.Background(), t, mcpTransport(), dir)
			defer sess.Close()

			assertRefused(t, moveSymbolCall(t, sess, "Foo", "src", "dst"), "cannot move Foo: its constructor NewFoo already exists in package dst")
			for rel, want := range files {
				assertFileUnchanged(t, dir, rel, want)
			}
		})
	}
}
