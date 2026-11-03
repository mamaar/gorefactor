package tests_test

import (
	"context"
	"testing"

	"github.com/mamaar/gorefactor/tests/mcptest"
)

const embedSrc = "package src\n\nimport _ \"embed\"\n\n//go:embed x.txt\nvar Data string\n\nconst Other = 1\n"

func TestMoveSymbol_RefusesAVariableWithAGoEmbedDirective(t *testing.T) {
	dir := writeInlineWorkspace(t, map[string]string{"src/src.go": embedSrc, "src/x.txt": "hi"})

	sess := mcptest.Dial(context.Background(), t, mcpTransport(), dir)
	defer sess.Close()

	assertRefused(t, moveSymbolCall(t, sess, "Data", "src", "dst"), "cannot move Data: it carries a //go:embed directive, whose files are relative to package src; move it by hand")
	assertFileUnchanged(t, dir, "src/src.go", embedSrc)
}

func TestMoveSymbol_RefusesAGoEmbedVariableRequestedAmongOthers(t *testing.T) {
	dir := writeInlineWorkspace(t, map[string]string{"src/src.go": embedSrc, "src/x.txt": "hi"})

	sess := mcptest.Dial(context.Background(), t, mcpTransport(), dir)
	defer sess.Close()

	assertRefused(t, moveSymbolsCall(t, sess, "src", "dst", "Other", "Data"), "cannot move Data: it carries a //go:embed directive")
	assertFileUnchanged(t, dir, "src/src.go", embedSrc)
}
