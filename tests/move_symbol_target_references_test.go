package tests_test

import (
	"context"
	"strings"
	"testing"

	"github.com/mamaar/gorefactor/tests/mcptest"
)

func TestMoveSymbol_UnqualifiesReferencesTheEarlierMoveWroteInTheTarget(t *testing.T) {
	dir := writeInlineWorkspace(t, map[string]string{
		"src/src.go": "package src\n\nfunc Helper() int { return 1 }\n\nfunc Moved() int { return Helper() }\n",
		"dst/dst.go": "package dst\n\nfunc D() int { return 0 }\n",
		"a/a.go":     "package a\n\nimport \"example.com/threadmcp/src\"\n\nfunc A() int { return src.Moved() + src.Helper() }\n",
	})
	sess := mcptest.Dial(context.Background(), t, mcpTransport(), dir)
	defer sess.Close()

	for _, name := range []string{"Moved", "Helper"} {
		if res := moveSymbolCall(t, sess, name, "src", "dst"); res.IsError {
			t.Fatalf("move %s: %s", name, errText(res))
		}
	}

	assertFileContains(t, dir, "dst/dst.go", "return Helper()")
	if got := fileText(t, dir, "dst/dst.go"); strings.Contains(got, "src") {
		t.Errorf("dst still mentions src:\n%s", got)
	}
	goVets(t, dir, "")
}

func TestMoveSymbol_UnqualifiesAliasedSourceReferencesInTheTargetAndKeepsTheImportMovedCodeNeeds(t *testing.T) {
	dir := writeInlineWorkspace(t, map[string]string{
		"src/src.go": "package src\n\nfunc Helper() int { return 1 }\n\nfunc Moved() int { return Helper() }\n",
		"dst/dst.go": "package dst\n\nimport s \"example.com/threadmcp/src\"\n\nfunc D() int { return s.Moved() }\n",
	})
	sess := mcptest.Dial(context.Background(), t, mcpTransport(), dir)
	defer sess.Close()

	if res := moveSymbolCall(t, sess, "Moved", "src", "dst"); res.IsError {
		t.Fatalf("move Moved: %s", errText(res))
	}
	assertFileContains(t, dir, "dst/dst.go", "return Moved()")
	assertFileContains(t, dir, "dst/dst.go", "return s.Helper()")
	goVets(t, dir, "")

	if res := moveSymbolCall(t, sess, "Helper", "src", "dst"); res.IsError {
		t.Fatalf("move Helper: %s", errText(res))
	}
	if got := fileText(t, dir, "dst/dst.go"); strings.Contains(got, "src") || strings.Contains(got, "s.") {
		t.Errorf("dst still refers to src:\n%s", got)
	}
	goVets(t, dir, "")
}

func TestMoveByDependencies_SecondRunUnqualifiesTheSourceReferencesTheFirstWrote(t *testing.T) {
	dir := writeInlineWorkspace(t, map[string]string{
		"src/src.go": "package src\n\nfunc Helper() int { return 1 }\n\nfunc Shared() int { return Helper() }\n",
		"a/a.go":     "package a\n\nimport \"example.com/threadmcp/src\"\n\nfunc A() int { return src.Shared() + src.Helper() }\n",
		"b/b.go":     "package b\n\nimport \"example.com/threadmcp/src\"\n\nfunc B() int { return src.Shared() }\n",
	})
	sess := mcptest.Dial(context.Background(), t, mcpTransport(), dir)
	defer sess.Close()

	callJSON(t, sess, "move_by_dependencies", map[string]any{"move_shared_to": "shared"})
	assertFileContains(t, dir, "shared/shared.go", "src.Helper()")
	goVets(t, dir, "")
	callJSON(t, sess, "move_by_dependencies", map[string]any{"move_shared_to": "shared"})

	assertFileContains(t, dir, "shared/shared.go", "func Helper()")
	if got := fileText(t, dir, "shared/shared.go"); strings.Contains(got, "src") {
		t.Errorf("shared still mentions src:\n%s", got)
	}
	goVets(t, dir, "")
}

func TestMoveSymbol_KeepsTheTargetImportASourceFileStillNeedsForItsRewrittenReferences(t *testing.T) {
	dir := writeInlineWorkspace(t, map[string]string{
		"dst/dst.go": "package dst\n\nfunc Base() int { return 1 }\n",
		"src/src.go": "package src\n\nimport \"example.com/threadmcp/dst\"\n\nfunc Wrapper() int { return dst.Base() }\n\nfunc Other() int { return Wrapper() }\n",
	})
	sess := mcptest.Dial(context.Background(), t, mcpTransport(), dir)
	defer sess.Close()

	if res := moveSymbolCall(t, sess, "Wrapper", "src", "dst"); res.IsError {
		t.Fatalf("move Wrapper: %s", errText(res))
	}

	assertFileContains(t, dir, "src/src.go", "dst.Wrapper()")
	goVets(t, dir, "")
}
