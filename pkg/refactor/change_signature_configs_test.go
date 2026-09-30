package refactor

import (
	"slices"
	"testing"

	"github.com/mamaar/gorefactor/pkg/types"
)

const (
	csDefault = "//go:build !tagx\n\npackage p\n\nfunc f(n int) int { return n }\n"
	csTagx    = "//go:build tagx\n\npackage p\n\nfunc f(n int) int { return n + 1 }\n"
)

func addMRequest(file string) ChangeSignatureRequest {
	return ChangeSignatureRequest{
		FunctionName:     "f",
		SourceFile:       file,
		Subcommand:       "add_param",
		NewParams:        []Parameter{{Name: "n", Type: "int"}, {Name: "m", Type: "int"}},
		DefaultValue:     "0",
		NewParamPosition: 1,
		Scope:            types.WorkspaceScope,
	}
}

func TestChangeSignatureAcrossConfigs_SingleConfigPlanEqualsSingleWorkspacePlan(t *testing.T) {
	files := map[string]string{
		"p/f.go":   "package p\n\nfunc f(n int) int { return n }\n",
		"p/use.go": "package p\n\nfunc Use() int { return f(1) }\n",
	}
	eng, dir := loadConfigsEngine(t, files)
	req := addMRequest(dir + "/p/f.go")
	ws := eng.WorkspaceSet().Primary().Workspace

	want, err := eng.ChangeSignature(ws, req)
	if err != nil {
		t.Fatal(err)
	}
	got, err := eng.ChangeSignatureAcrossConfigs(req)
	if err != nil {
		t.Fatal(err)
	}

	if !slices.Equal(sortedChanges(got), sortedChanges(want)) {
		t.Errorf("changes differ:\n got %+v\nwant %+v", sortedChanges(got), sortedChanges(want))
	}
	if !slices.Equal(got.AffectedFiles, want.AffectedFiles) {
		t.Errorf("affected files = %v, want %v", got.AffectedFiles, want.AffectedFiles)
	}
	if len(got.Coverage.Configs) > 1 {
		t.Errorf("configs = %v, want a single configuration", got.Coverage.Configs)
	}
}

func TestChangeSignaturePlanner_LinkedSupersetKeepsPlanAndDoesNotDoubleTheParameter(t *testing.T) {
	eng, dir := loadConfigsEngine(t, map[string]string{
		"p/f_default.go": csDefault,
		"p/f_tagx.go":    csTagx,
		"p/use.go":       "package p\n\nfunc Use() int { return f(1) }\n",
	})
	views, err := eng.WorkspaceSet().Views()
	if err != nil {
		t.Fatal(err)
	}
	planner := &changeSignaturePlanner{engine: eng, req: addMRequest(dir + "/p/f_default.go")}

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

	if got, want := sortedChanges(second.plan), sortedChanges(first.plan); !slices.Equal(got, want) {
		t.Errorf("replanned changes differ:\n got %+v\nwant %+v", got, want)
	}
	if err := eng.ExecutePlan(second.plan); err != nil {
		t.Fatal(err)
	}
	assertOnce(t, dir, "p/f_default.go", "func f(n int, m int) int")
	assertOnce(t, dir, "p/use.go", "f(1, 0)")
}

func TestChangeSignatureAcrossConfigs_AddReturnReachesSiblingAndTaggedCaller(t *testing.T) {
	eng, dir := loadConfigsEngine(t, map[string]string{
		"p/f_default.go":   csDefault,
		"p/f_tagx.go":      csTagx,
		"p/use_tagx.go":    "//go:build tagx\n\npackage p\n\nfunc Use() { _ = f(1) }\n",
		"p/use_default.go": "//go:build !tagx\n\npackage p\n\nfunc Use() { _ = f(1) }\n",
	})
	req := ChangeSignatureRequest{
		FunctionName: "f", SourceFile: dir + "/p/f_default.go", Subcommand: "add_return",
		NewReturns: []string{"int", "error"}, Scope: types.WorkspaceScope,
	}

	plan, err := eng.ChangeSignatureAcrossConfigs(req)
	if err != nil {
		t.Fatal(err)
	}
	if err := eng.ExecutePlan(plan); err != nil {
		t.Fatal(err)
	}

	assertOnce(t, dir, "p/f_default.go", "func f(n int) (int, error)")
	assertOnce(t, dir, "p/f_tagx.go", "func f(n int) (int, error)")
	vetBuilds(t, dir, linuxBuild, linuxTagx)
}
