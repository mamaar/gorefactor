package tests_test

import (
	"context"
	"strings"
	"testing"

	"github.com/mamaar/gorefactor/tests/mcptest"
)

const (
	rfcA = "package a\n\nimport \"example.com/threadmcp/b\"\n\ntype T struct{ Fld int }\n\nfunc F() int { return b.G() }\n"
	rfcB = "package b\n\nimport \"example.com/threadmcp/a\"\n\nfunc G() int {\n\tvar t a.T\n\treturn t.Fld\n}\n"
)

func TestRenameField_RefusesWhenProductionCycleHidesAUse(t *testing.T) {
	dir := writeInlineWorkspace(t, map[string]string{"a/a.go": rfcA, "b/b.go": rfcB})
	sess := mcptest.Dial(context.Background(), t, mcpTransport(), dir)
	defer sess.Close()

	res := callTool(t, sess, "rename_field", map[string]any{"type_name": "T", "field_name": "Fld", "new_field_name": "Gld", "package_path": "a"})

	if !res.IsError {
		t.Fatalf("rename_field succeeded: %s", errText(res))
	}
	got := errText(res)
	for _, want := range []string{"b/b.go:7", "import cycle not allowed"} {
		if !strings.Contains(got, want) {
			t.Errorf("error %q does not contain %q", got, want)
		}
	}
	assertFileUnchanged(t, dir, "a/a.go", rfcA)
	assertFileUnchanged(t, dir, "b/b.go", rfcB)
}

func TestRenameField_WithoutCycleRenamesAcrossPackages(t *testing.T) {
	dir := writeInlineWorkspace(t, map[string]string{
		"a/a.go": "package a\n\ntype T struct{ Fld int }\n",
		"b/b.go": rfcB,
	})
	sess := mcptest.Dial(context.Background(), t, mcpTransport(), dir)
	defer sess.Close()

	res := callTool(t, sess, "rename_field", map[string]any{"type_name": "T", "field_name": "Fld", "new_field_name": "Gld", "package_path": "a"})

	if res.IsError {
		t.Fatalf("rename_field failed: %s", errText(res))
	}
	if got := fileText(t, dir, "b/b.go"); !strings.Contains(got, "t.Gld") {
		t.Errorf("b.go not renamed:\n%s", got)
	}
	goVets(t, dir, "")
}
