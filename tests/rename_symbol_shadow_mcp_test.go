package tests_test

import (
	"context"
	"strings"
	"testing"

	"github.com/mamaar/gorefactor/tests/mcptest"
)

func TestRenameSymbol_RefusesNewNameShadowedAtReference(t *testing.T) {
	cases := []struct {
		name  string
		files map[string]string
		path  string
		want  string
	}{
		{
			name: "local const",
			files: map[string]string{
				"p/p.go": "package p\n\nfunc helper() int { return 1 }\n\nfunc Use() int {\n\tconst local = 3\n\treturn local + helper()\n}\n",
			},
			path: "p/p.go",
			want: "cannot rename helper to local: at p/p.go:7 the name local refers to a local declaration (p/p.go:6)",
		},
		{
			name: "short variable declaration",
			files: map[string]string{
				"p/p.go": "package p\n\nfunc helper() int { return 1 }\n\nfunc Use() int {\n\tlocal := 3\n\treturn local + helper()\n}\n",
			},
			path: "p/p.go",
			want: "the name local refers to a local declaration (p/p.go:6)",
		},
		{
			name: "parameter",
			files: map[string]string{
				"p/p.go": "package p\n\nfunc helper() int { return 1 }\n\nfunc Use(local int) int {\n\treturn local + helper()\n}\n",
			},
			path: "p/p.go",
			want: "the name local refers to a local declaration (p/p.go:5)",
		},
		{
			name: "reference in test file",
			files: map[string]string{
				"p/p.go":      "package p\n\nfunc helper() int { return 1 }\n",
				"p/p_test.go": "package p\n\nimport \"testing\"\n\nfunc TestUse(t *testing.T) {\n\tlocal := 3\n\t_ = local + helper()\n}\n",
			},
			path: "p/p_test.go",
			want: "at p/p_test.go:7 the name local refers to a local declaration (p/p_test.go:6)",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dir := writeInlineWorkspace(t, tc.files)
			sess := mcptest.Dial(context.Background(), t, mcpTransport(), dir)
			defer sess.Close()

			res := renameSymbol(t, sess, map[string]any{"symbol": "helper", "new_name": "local", "package": "p"})
			if !res.IsError {
				t.Fatalf("rename succeeded: %s", errText(res))
			}
			if got := errText(res); !strings.Contains(got, tc.want) || !strings.Contains(got, "cannot rename helper to local") {
				t.Errorf("error = %q, want it to contain %q", got, tc.want)
			}
			for rel, content := range tc.files {
				assertFileUnchanged(t, dir, rel, content)
			}
		})
	}
}

func TestRenameSymbol_AllowsNewNameUsedLocallyWhereSymbolIsNotReferenced(t *testing.T) {
	dir := writeInlineWorkspace(t, map[string]string{
		"p/p.go": "package p\n\nfunc helper() int { return 1 }\n\nfunc Use() int { return helper() }\n\nfunc Other() int {\n\tlocal := 3\n\treturn local\n}\n",
	})
	sess := mcptest.Dial(context.Background(), t, mcpTransport(), dir)
	defer sess.Close()

	res := renameSymbol(t, sess, map[string]any{"symbol": "helper", "new_name": "local", "package": "p"})
	if res.IsError {
		t.Fatalf("rename failed: %s", errText(res))
	}
	if got := fileText(t, dir, "p/p.go"); !strings.Contains(got, "func local() int") || !strings.Contains(got, "return local()") {
		t.Errorf("not renamed:\n%s", got)
	}
	goVets(t, dir, "")
}

func TestRenameSymbol_RefusesNewNameShadowedInTaggedView(t *testing.T) {
	files := map[string]string{
		"p/p.go":      "package p\n\nfunc helper() int { return 1 }\n",
		"p/p_tagx.go": "//go:build tagx\n\npackage p\n\nfunc Use() int {\n\tlocal := 3\n\treturn local + helper()\n}\n",
	}
	dir := writeInlineWorkspace(t, files)
	sess := mcptest.Dial(context.Background(), t, mcpTransport(), dir)
	defer sess.Close()

	res := renameSymbol(t, sess, map[string]any{"symbol": "helper", "new_name": "local", "package": "p"})
	if !res.IsError || !strings.Contains(errText(res), "the name local refers to a local declaration (p/p_tagx.go:6)") {
		t.Fatalf("result = %v %s", res.IsError, errText(res))
	}
	for rel, content := range files {
		assertFileUnchanged(t, dir, rel, content)
	}
}
