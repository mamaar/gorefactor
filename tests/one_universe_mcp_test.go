package tests_test

import (
	"context"
	"testing"

	"github.com/mamaar/gorefactor/tests/mcptest"
)

// common is handed to its importers as a production-only package: its
// in-package test imports x, whose in-package test imports common.
func oneUniverseWorkspace(t *testing.T) string {
	t.Helper()
	const mod = "example.com/threadmcp/"
	return writeInlineWorkspace(t, map[string]string{
		"common/common.go":      "package common\n\ntype GBNR struct{ N int }\n",
		"common/common_test.go": "package common\n\nimport \"" + mod + "x\"\n\nvar _ = x.X\n",
		"x/x.go":                "package x\n\nvar X = 1\n",
		"x/x_test.go":           "package x\n\nimport \"" + mod + "common\"\n\nvar _ = common.GBNR{}\n",
		"prop/prop.go":          "package prop\n\nimport \"" + mod + "common\"\n\nfunc Get(g common.GBNR) int { return g.N }\n",
		"planner/planner.go":    "package planner\n\ntype Planner struct{}\n\nfunc (Planner) First() int { return 1 }\n\nfunc (Planner) Second() int { return 2 }\n",
		"app/app.go":            "package app\n\nimport (\n\t\"" + mod + "common\"\n\t\"" + mod + "planner\"\n\t\"" + mod + "prop\"\n)\n\nfunc Run(g common.GBNR) int {\n\treturn prop.Get(g) + planner.Planner{}.First() + planner.Planner{}.Second()\n}\n",
	})
}

func TestRenameMethod_SecondAppliedRenameReportsNoTypeErrors(t *testing.T) {
	dir := oneUniverseWorkspace(t)
	sess := mcptest.Dial(context.Background(), t, mcpTransport(), dir)
	defer sess.Close()
	args := map[string]any{"type_name": "Planner", "method_name": "First", "new_method_name": "Primary", "package_path": "example.com/threadmcp/planner", "dry_run": false}
	first := callJSON(t, sess, "rename_method", args)

	args["method_name"], args["new_method_name"] = "Second", "Secondary"
	second := callJSON(t, sess, "rename_method", args)

	for name, out := range map[string]map[string]any{"first": first, "second": second} {
		if errs, ok := out["type_errors"]; ok {
			t.Errorf("%s rename reports type_errors of a workspace that compiles: %v", name, errs)
		}
	}
	assertFileContains(t, dir, "app/app.go", "planner.Planner{}.Primary() + planner.Planner{}.Secondary()")
	goVets(t, dir, "")
}
