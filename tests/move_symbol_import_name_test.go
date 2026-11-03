package tests_test

import (
	"context"
	"testing"

	"github.com/mamaar/gorefactor/tests/mcptest"
)

// Imports whose package name differs from the last path segment.
var importNameFixtures = map[string]struct{ dir, pkg, path string }{
	"hyphenated directory": {"x-util", "util", "example.com/threadmcp/x-util"},
	"major version suffix": {"lib/v2", "lib", "example.com/threadmcp/lib/v2"},
}

func importNameFiles(f struct{ dir, pkg, path string }, srcBody string) map[string]string {
	return map[string]string{
		f.dir + "/p.go": "package " + f.pkg + "\n\nfunc N() int { return 1 }\n",
		"src/src.go":    "package src\n\nimport \"" + f.path + "\"\n\n" + srcBody,
	}
}

func TestMoveSymbol_KeepsAnImportTheStayingCodeStillUsesWhenItsNameIsNotThePathSuffix(t *testing.T) {
	for name, f := range importNameFixtures {
		t.Run(name, func(t *testing.T) {
			dir := writeInlineWorkspace(t, importNameFiles(f,
				"func F() int { return "+f.pkg+".N() }\n\nfunc K() int { return "+f.pkg+".N() }\n"))

			sess := mcptest.Dial(context.Background(), t, mcpTransport(), dir)
			defer sess.Close()

			res := moveSymbolCall(t, sess, "F", "src", "dst")
			if res.IsError {
				t.Fatalf("move: %s", errText(res))
			}

			assertFileContains(t, dir, "src/src.go", f.path)
			assertFileContains(t, dir, "dst/dst.go", f.path)
			goVets(t, dir, "")
		})
	}
}

func TestMoveSymbol_DropsAnImportWhoseOnlyUserMovesWhenItsNameIsNotThePathSuffix(t *testing.T) {
	for name, f := range importNameFixtures {
		t.Run(name, func(t *testing.T) {
			dir := writeInlineWorkspace(t, importNameFiles(f,
				"func F() int { return "+f.pkg+".N() }\n\nfunc K() int { return 2 }\n"))

			sess := mcptest.Dial(context.Background(), t, mcpTransport(), dir)
			defer sess.Close()

			res := moveSymbolCall(t, sess, "F", "src", "dst")
			if res.IsError {
				t.Fatalf("move: %s", errText(res))
			}

			assertFileOmits(t, dir, "src/src.go", f.path)
			goVets(t, dir, "")
		})
	}
}

func TestMoveSymbol_DropsTheSourceImportOfAReferrerWhenTheSourceNameIsNotThePathSuffix(t *testing.T) {
	for name, f := range importNameFixtures {
		t.Run(name, func(t *testing.T) {
			dir := writeInlineWorkspace(t, map[string]string{
				f.dir + "/p.go": "package " + f.pkg + "\n\nfunc N() int { return 1 }\n",
				"use/use.go":    "package use\n\nimport \"" + f.path + "\"\n\nfunc U() int { return " + f.pkg + ".N() }\n",
			})

			sess := mcptest.Dial(context.Background(), t, mcpTransport(), dir)
			defer sess.Close()

			res := moveSymbolCall(t, sess, "N", f.dir, "dst")
			if res.IsError {
				t.Fatalf("move: %s", errText(res))
			}

			assertFileOmits(t, dir, "use/use.go", f.path)
			goVets(t, dir, "")
		})
	}
}
