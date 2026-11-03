package tests_test

import (
	"strings"
	"testing"

	"github.com/mamaar/gorefactor/tests/mcptest"
)

// moveAndVet moves symbol from src to dst in a workspace made of files and
// returns the resulting src and dst sources.
func moveAndVet(t *testing.T, symbol string, files map[string]string) (srcText, dstText string) {
	t.Helper()
	dir := writeInlineWorkspace(t, files)

	sess := mcptest.Dial(t.Context(), t, mcpTransport(), dir)
	defer sess.Close()

	res := moveSymbolCall(t, sess, symbol, "src", "dst")
	if res.IsError {
		t.Fatalf("move %s: %s", symbol, errText(res))
	}

	goVets(t, dir, "")

	return fileText(t, dir, "src/src.go"), fileText(t, dir, "dst/dst.go")
}

func TestMoveSymbol_TypeDocCommentTravelsWithTheType(t *testing.T) {
	src, dst := moveAndVet(t, "T", map[string]string{
		"src/src.go": "package src\n\n// T is documented.\ntype T struct{}\n\nfunc Keep() {}\n",
	})
	if !strings.Contains(dst, "// T is documented.\ntype T struct{}") {
		t.Errorf("dst lacks doc above type:\n%s", dst)
	}

	if strings.Contains(src, "T is documented") {
		t.Errorf("doc left in src:\n%s", src)
	}
}

func TestMoveSymbol_BlockDocCommentTravelsWithTheFunction(t *testing.T) {
	src, dst := moveAndVet(t, "Do", map[string]string{
		"src/src.go": "package src\n\n/* Do does. */\nfunc Do() {}\n\nfunc Keep() {}\n",
	})
	if !strings.Contains(dst, "/* Do does. */\nfunc Do()") {
		t.Errorf("dst lacks block doc:\n%s", dst)
	}

	if strings.Contains(src, "Do does") {
		t.Errorf("block doc left in src:\n%s", src)
	}
}

func TestMoveSymbol_DetachedSectionHeaderStaysInSource(t *testing.T) {
	src, dst := moveAndVet(t, "Do", map[string]string{
		"src/src.go": "package src\n\n// Section: helpers\n\nfunc Do() {}\n\nfunc Keep() {}\n",
	})
	if !strings.Contains(src, "// Section: helpers") {
		t.Errorf("header removed from src:\n%s", src)
	}

	if strings.Contains(dst, "Section: helpers") {
		t.Errorf("header copied to dst:\n%s", dst)
	}
}

func TestMoveSymbol_DetachedDirectiveStaysAndAttachedDirectiveTravels(t *testing.T) {
	src, dst := moveAndVet(t, "Do", map[string]string{
		"src/src.go": "package src\n\n//go:generate echo hi\n\n//go:noinline\nfunc Do() {}\n\nfunc Keep() {}\n",
	})
	if !strings.Contains(src, "//go:generate echo hi") {
		t.Errorf("detached directive removed from src:\n%s", src)
	}

	if strings.Contains(dst, "go:generate") {
		t.Errorf("detached directive copied to dst:\n%s", dst)
	}

	if !strings.Contains(dst, "//go:noinline\nfunc Do()") || strings.Contains(src, "go:noinline") {
		t.Errorf("attached directive did not travel:\nsrc:\n%s\ndst:\n%s", src, dst)
	}
}

func TestMoveSymbol_MethodBlockDocAppearsOnceInTargetAndNotInSource(t *testing.T) {
	src, dst := moveAndVet(t, "T", map[string]string{
		"src/src.go": "package src\n\ntype T struct{}\n\n/* block */\nfunc (T) M() {}\n\nfunc Keep() {}\n",
	})

	n := strings.Count(dst, "/* block */")
	if n != 1 {
		t.Errorf("block doc appears %d times in dst:\n%s", n, dst)
	}

	if strings.Contains(src, "block") {
		t.Errorf("block doc left in src:\n%s", src)
	}
}

func TestMoveSymbol_DetachedCommentAboveMethodStaysInSource(t *testing.T) {
	src, dst := moveAndVet(t, "T", map[string]string{
		"src/src.go": "package src\n\ntype T struct{}\n\n// note\n\nfunc (T) M() {}\n\nfunc Keep() {}\n",
	})
	if !strings.Contains(src, "// note") {
		t.Errorf("note removed from src:\n%s", src)
	}

	if strings.Contains(dst, "// note") {
		t.Errorf("note copied to dst:\n%s", dst)
	}
}

func TestMoveSymbol_GroupedTypeCarriesItsSpecDocAndLeavesSiblings(t *testing.T) {
	src, dst := moveAndVet(t, "A", map[string]string{
		"src/src.go": "package src\n\ntype (\n\t// A doc.\n\tA int\n\tB int\n)\n",
	})
	if !strings.Contains(dst, "\n// A doc.\ntype A int") {
		t.Errorf("dst lacks spec doc:\n%s", dst)
	}

	if strings.Contains(src, "A doc") || !strings.Contains(src, "B int") {
		t.Errorf("src wrong:\n%s", src)
	}
}

func TestMoveSymbol_TrailingCommentOnLastLineTravels(t *testing.T) {
	_, dst := moveAndVet(t, "T", map[string]string{
		"src/src.go": "package src\n\ntype T struct{} // trailing\n\nfunc Keep() {}\n",
	})
	if !strings.Contains(dst, "type T struct{} // trailing") {
		t.Errorf("dst lacks trailing comment:\n%s", dst)
	}
}
