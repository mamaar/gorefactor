package refactor

import (
	"slices"
	"testing"

	"github.com/mamaar/gorefactor/pkg/types"
)

func TestRenameSymbolAcrossConfigs_SingleConfigPlanEqualsSingleWorkspacePlan(t *testing.T) {
	eng, _ := loadConfigsEngine(t, map[string]string{
		"p/f.go":   "package p\n\nfunc f(n int) int { return n }\n",
		"p/use.go": "package p\n\nfunc Use() int { return f(1) }\n",
	})
	req := types.RenameSymbolRequest{SymbolName: "f", NewName: "g", Scope: types.WorkspaceScope}

	want, err := eng.RenameSymbol(eng.WorkspaceSet().Primary().Workspace, req)
	if err != nil {
		t.Fatal(err)
	}

	got, err := eng.RenameSymbolAcrossConfigs(req)
	if err != nil {
		t.Fatal(err)
	}

	if !slices.Equal(sortedChanges(got), sortedChanges(want)) {
		t.Errorf("changes differ:\n got %+v\nwant %+v", sortedChanges(got), sortedChanges(want))
	}

	if len(got.Coverage.Configs) > 1 {
		t.Errorf("configs = %v, want a single configuration", got.Coverage.Configs)
	}
}

func TestRenameSymbolPlanner_LinkedSupersetKeepsPlan(t *testing.T) {
	eng, _ := loadConfigsEngine(t, map[string]string{
		"p/f_default.go": "//go:build !tagx\n\npackage p\n\nfunc f(n int) int { return n }\n",
		"p/f_tagx.go":    "//go:build tagx\n\npackage p\n\nfunc f(n int) int { return n + 1 }\n",
		"p/use.go":       "package p\n\nfunc Use() int { return f(1) }\n",
	})

	views, err := eng.WorkspaceSet().Views()
	if err != nil {
		t.Fatal(err)
	}

	planner := &renameSymbolPlanner{req: types.RenameSymbolRequest{SymbolName: "f", NewName: "g", Scope: types.WorkspaceScope}}

	first, err := planner.planIn(views[0], nil)
	if err != nil {
		t.Fatal(err)
	}

	linked := first.changed.Clone()
	linked[types.SymbolKey{PkgPath: "example.com/threadtest/p", Name: "absent"}] = struct{}{}

	second, err := planner.planIn(views[0], linked)
	if err != nil {
		t.Fatal(err)
	}

	got, want := sortedChanges(second.plan), sortedChanges(first.plan)
	if !slices.Equal(got, want) {
		t.Errorf("replanned changes differ:\n got %+v\nwant %+v", got, want)
	}
}

func TestRenameSymbolAcrossConfigs_DottedMethodNameLeavesOtherReceiverAlone(t *testing.T) {
	eng, dir := loadConfigsEngine(t, map[string]string{
		"p/a.go":      "package p\n\ntype A struct{}\n\nfunc (A) Run() {}\n",
		"p/b_tagx.go": "//go:build tagx\n\npackage p\n\ntype B struct{}\n\nfunc (B) Run() {}\n",
	})

	plan, err := eng.RenameSymbolAcrossConfigs(types.RenameSymbolRequest{SymbolName: "A.Run", NewName: "Go", Scope: types.WorkspaceScope})
	if err != nil {
		t.Fatal(err)
	}

	err = eng.ExecutePlan(plan)
	if err != nil {
		t.Fatal(err)
	}

	assertOnce(t, dir, "p/a.go", "func (A) Go()")
	assertOnce(t, dir, "p/b_tagx.go", "func (B) Run()")
	vetBuilds(t, dir, linuxBuild, linuxTagx)
}
