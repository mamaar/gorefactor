package tests_test

import (
	"context"
	"strings"
	"testing"

	"github.com/mamaar/gorefactor/tests/mcptest"
)

const memberSrc = "package src\n\ntype T struct{ Name string }\n\nfunc (t T) M() string { return t.Name }\n"

func TestMoveSymbol_RefusesAMethodByBareOrDottedName(t *testing.T) {
	for _, name := range []string{"M", "T.M"} {
		t.Run(name, func(t *testing.T) {
			dir := writeInlineWorkspace(t, map[string]string{
				"src/src.go": memberSrc,
				"use/use.go": "package use\n\nimport \"example.com/threadmcp/src\"\n\nfunc U() string { return src.T{}.M() }\n",
			})

			sess := mcptest.Dial(context.Background(), t, mcpTransport(), dir)
			defer sess.Close()

			assertRefused(t, moveSymbolCall(t, sess, name, "src", "dst"), "cannot move M: it is a method of T and moves with its type; move T instead")
			assertFileUnchanged(t, dir, "src/src.go", memberSrc)
		})
	}
}

func TestMoveSymbol_RefusesAStructField(t *testing.T) {
	dir := writeInlineWorkspace(t, map[string]string{"src/src.go": memberSrc})

	sess := mcptest.Dial(context.Background(), t, mcpTransport(), dir)
	defer sess.Close()

	assertRefused(t, moveSymbolCall(t, sess, "T.Name", "src", "dst"), "cannot move Name: it is a field of T and moves with its type; move T instead")
	assertFileUnchanged(t, dir, "src/src.go", memberSrc)
}

func TestMoveSymbol_RefusesATypeTogetherWithItsMethodInValidate(t *testing.T) {
	dir := writeInlineWorkspace(t, map[string]string{"src/src.go": memberSrc})

	sess := mcptest.Dial(context.Background(), t, mcpTransport(), dir)
	defer sess.Close()

	res := moveSymbolsCall(t, sess, "src", "dst", "T", "M")
	assertRefused(t, res, "cannot move M: it is a method of T and moves with its type; move T instead")

	got := errText(res)
	if strings.Contains(got, "overlapping") {
		t.Errorf("refusal comes from the planner, not validation: %s", got)
	}

	assertFileUnchanged(t, dir, "src/src.go", memberSrc)
}

func TestMoveSymbol_MovesAFunctionSharingItsNameWithAMethod(t *testing.T) {
	dir := writeInlineWorkspace(t, map[string]string{
		"src/src.go": "package src\n\ntype T struct{}\n\nfunc (T) M() int { return 1 }\n\nfunc M() int { return 2 }\n",
		"use/use.go": "package use\n\nimport \"example.com/threadmcp/src\"\n\nfunc U() int { return src.M() + src.T{}.M() }\n",
	})

	sess := mcptest.Dial(context.Background(), t, mcpTransport(), dir)
	defer sess.Close()

	res := moveSymbolCall(t, sess, "M", "src", "dst")
	if res.IsError {
		t.Fatalf("move: %s", errText(res))
	}

	assertFileContains(t, dir, "dst/dst.go", "func M() int")
	assertFileContains(t, dir, "use/use.go", "dst.M()")
	goVets(t, dir, "")
}
