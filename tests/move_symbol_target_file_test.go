package tests_test

import (
	"context"
	"strings"
	"testing"

	"github.com/mamaar/gorefactor/tests/mcptest"
)

func TestMoveSymbol_PutsTheSymbolInATargetFileEveryBuildConfigurationIncludes(t *testing.T) {
	dir := writeInlineWorkspace(t, map[string]string{
		"src/src.go": "package src\n\nfunc Foo() int { return 1 }\n",
		"dst/a.go":   "//go:build !tagx\n\npackage dst\n\nfunc A() int { return 0 }\n",
		"dst/b.go":   "package dst\n\nfunc B() int { return 0 }\n",
		"use/use.go": "package use\n\nimport \"example.com/threadmcp/src\"\n\nfunc Use() int { return src.Foo() }\n",
	})

	sess := mcptest.Dial(context.Background(), t, mcpTransport(), dir)
	defer sess.Close()

	res := moveSymbolCall(t, sess, "Foo", "src", "dst")
	if res.IsError {
		t.Fatalf("move_symbol: %s", errText(res))
	}

	assertFileContains(t, dir, "dst/b.go", "func Foo()")
	goVets(t, dir, "", "tagx")
}

func TestMoveSymbol_CreatesATargetFileWhenEveryExistingOneIsConstrained(t *testing.T) {
	dir := writeInlineWorkspace(t, map[string]string{
		"src/src.go":       "package src\n\nfunc Foo() int { return 1 }\n",
		"dst/a.go":         "//go:build !tagx\n\npackage dst\n\nfunc A() int { return 0 }\n",
		"dst/b_linux.go":   "package dst\n\nfunc B() int { return 0 }\n",
		"dst/b_windows.go": "package dst\n\nfunc B() int { return 0 }\n",
	})

	sess := mcptest.Dial(context.Background(), t, mcpTransport(), dir)
	defer sess.Close()

	res := moveSymbolCall(t, sess, "Foo", "src", "dst")
	if res.IsError {
		t.Fatalf("move_symbol: %s", errText(res))
	}

	assertFileContains(t, dir, "dst/dst.go", "func Foo()")

	for _, rel := range []string{"dst/a.go", "dst/b_linux.go", "dst/b_windows.go"} {
		got := fileText(t, dir, rel)
		if strings.Contains(got, "Foo") {
			t.Errorf("%s received Foo", rel)
		}
	}

	goVets(t, dir, "", "tagx")
}
