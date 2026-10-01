package watch

import (
	"path/filepath"
	"testing"

	"github.com/fsnotify/fsnotify"
	"github.com/mamaar/gorefactor/pkg/analysis"
)

// common is rescued: its in-package test imports x, whose
// in-package test imports common.
func universeWorkspace() map[string]string {
	const mod = "example.com/test/"
	return map[string]string{
		"common/common.go":      "package common\n\ntype GBNR struct{ N int }\n",
		"common/common_test.go": "package common\n\nimport \"" + mod + "x\"\n\nvar _ = x.X\n",
		"x/x.go":                "package x\n\nvar X = 1\n",
		"x/x_test.go":           "package x\n\nimport \"" + mod + "common\"\n\nvar _ = common.GBNR{}\n",
		"prop/prop.go":          "package prop\n\nimport \"" + mod + "common\"\n\nfunc Get(g common.GBNR) int { return g.N }\n",
		"planner/planner.go":    "package planner\n\ntype Planner struct{}\n\nfunc (Planner) M() int { return 1 }\n",
		"app/app.go":            "package app\n\nimport (\n\t\"" + mod + "common\"\n\t\"" + mod + "planner\"\n\t\"" + mod + "prop\"\n)\n\nfunc Run(g common.GBNR) int { return prop.Get(g) + planner.Planner{}.M() }\n",
	}
}

func checkAll(t *testing.T, v *analysis.View) {
	t.Helper()
	for _, pkg := range v.Workspace.Packages {
		v.Parser.EnsureTypeChecked(v.Workspace, pkg)
	}
	for _, pkg := range v.Workspace.Packages {
		if pkg.TypesErr != nil {
			t.Errorf("%s: TypesErr = %v", pkg.ImportPath, pkg.TypesErr)
		}
	}
	for _, msg := range v.Parser.UniverseErrors(v.Workspace) {
		t.Error(msg)
	}
}

func TestUpdater_ModifyKeepsOnePackageObjectPerImportPath(t *testing.T) {
	u, set, root := setupViews(t, universeWorkspace())
	v := builtViews(t, set, 1)[0]
	checkAll(t, v)
	if t.Failed() {
		t.Fatal("workspace is inconsistent before the change")
	}

	path := filepath.Join(root, "planner", "planner.go")
	writeFile(t, path, "package planner\n\ntype Planner struct{}\n\nfunc (Planner) M() int { return 2 }\n")
	u.HandleChanges([]ChangeEvent{{Path: path, Op: fsnotify.Write}})

	checkAll(t, v)
}

func TestUpdater_ModifyOfRescuedPackageKeepsOnePackageObjectPerImportPath(t *testing.T) {
	u, set, root := setupViews(t, universeWorkspace())
	v := builtViews(t, set, 1)[0]
	checkAll(t, v)

	path := filepath.Join(root, "common", "common.go")
	writeFile(t, path, "package common\n\ntype GBNR struct{ N, M int }\n")
	u.HandleChanges([]ChangeEvent{{Path: path, Op: fsnotify.Write}})

	checkAll(t, v)
}
