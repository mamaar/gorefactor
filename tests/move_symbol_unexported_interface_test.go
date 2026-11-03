package tests_test

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/mamaar/gorefactor/tests/mcptest"
)

const shaperSrc = "package src\n\ntype shaper interface{ area() int }\n\ntype Sq struct{}\n\nfunc (Sq) area() int { return 1 }\n\nfunc Total(s shaper) int { return s.area() }\n\nfunc Use() int { return Total(Sq{}) }\n"

func assertMoveRefused(t *testing.T, dir, src, symbol string, want ...string) {
	t.Helper()
	sess := mcptest.Dial(t.Context(), t, mcpTransport(), dir)
	defer sess.Close()

	res := moveSymbolCall(t, sess, symbol, "src", "dst")

	if !res.IsError {
		t.Fatalf("move %s succeeded, want a refusal", symbol)
	}
	for _, w := range want {
		if !strings.Contains(errText(res), w) {
			t.Errorf("error = %q, want it to mention %q", errText(res), w)
		}
	}
	assertFileUnchanged(t, dir, "src/src.go", src)
}

func TestMoveSymbol_RefusesTypeThatImplementsAnInterfaceThroughAnUnexportedMethod(t *testing.T) {
	dir := writeInlineWorkspace(t, map[string]string{"src/src.go": shaperSrc})
	assertMoveRefused(t, dir, shaperSrc, "Sq", "Sq", "shaper", "area")
}

func TestMoveSymbol_RefusesInterfaceWithUnexportedMethodWhoseImplementerStays(t *testing.T) {
	src := strings.ReplaceAll(shaperSrc, "shaper", "Shaper")
	src = strings.Replace(src, "return s.area()", "return 0", 1)
	dir := writeInlineWorkspace(t, map[string]string{"src/src.go": src})
	assertMoveRefused(t, dir, src, "Shaper", "Sq", "Shaper", "area")
}

func TestMoveSymbol_RefusesPointerReceiverImplementerOfUnexportedMethodInterface(t *testing.T) {
	src := strings.Replace(shaperSrc, "func (Sq) area", "func (*Sq) area", 1)
	src = strings.Replace(src, "Total(Sq{})", "Total(&Sq{})", 1)
	dir := writeInlineWorkspace(t, map[string]string{"src/src.go": src})
	assertMoveRefused(t, dir, src, "Sq", "Sq", "shaper", "area")
}

func TestMoveSymbol_MovesTypeImplementingOnlyExportedMethodInterface(t *testing.T) {
	src := "package src\n\ntype Shaper interface{ Area() int }\n\ntype Sq struct{}\n\nfunc (Sq) Area() int { return 1 }\n\nfunc Total(s Shaper) int { return s.Area() }\n"
	dir := writeInlineWorkspace(t, map[string]string{"src/src.go": src})
	sess := mcptest.Dial(context.Background(), t, mcpTransport(), dir)
	defer sess.Close()

	res := moveSymbolCall(t, sess, "Sq", "src", "dst")
	if res.IsError {
		t.Fatalf("move Sq: %s", errText(res))
	}

	assertFileContains(t, dir, "dst/dst.go", "type Sq struct")
	goVets(t, dir, "")
}

func TestMoveSymbol_MovesTypeThatDoesNotImplementTheUnexportedMethodInterface(t *testing.T) {
	src := "package src\n\ntype shaper interface{ area() int }\n\ntype Circle struct{}\n\nfunc (Circle) Radius() int { return 1 }\n\nfunc Total(s shaper) int { return s.area() }\n"
	dir := writeInlineWorkspace(t, map[string]string{"src/src.go": src})
	sess := mcptest.Dial(context.Background(), t, mcpTransport(), dir)
	defer sess.Close()

	res := moveSymbolCall(t, sess, "Circle", "src", "dst")
	if res.IsError {
		t.Fatalf("move Circle: %s", errText(res))
	}

	assertFileContains(t, dir, "dst/dst.go", "type Circle struct")
	goVets(t, dir, "")
}

func TestMoveByDependencies_SkipsCandidateThatImplementsAnUnexportedMethodInterface(t *testing.T) {
	src := shaperSrc + "\nfunc Clean() int { return 3 }\n"
	dir := writeInlineWorkspace(t, map[string]string{
		"src/src.go": src,
		"a/a.go":     "package a\n\nimport \"example.com/threadmcp/src\"\n\nfunc A() int { return src.Clean() + len([]src.Sq{}) }\n",
		"b/b.go":     "package b\n\nimport \"example.com/threadmcp/src\"\n\nfunc B() int { return src.Clean() + len([]src.Sq{}) }\n",
	})
	sess := mcptest.Dial(context.Background(), t, mcpTransport(), dir)
	defer sess.Close()

	out := callJSON(t, sess, "move_by_dependencies", map[string]any{"move_shared_to": "shared"})

	sites, _ := json.Marshal(out["skipped_sites"])
	for _, want := range []string{"Sq", "shaper", "area"} {
		if !strings.Contains(string(sites), want) {
			t.Errorf("skipped_sites = %s, want it to mention %q", sites, want)
		}
	}
	assertFileContains(t, dir, "shared/shared.go", "func Clean() int")
	assertFileContains(t, dir, "src/src.go", "type Sq struct")
	goVets(t, dir, "")
}
