package refactor

import (
	"slices"
	"strings"
	"testing"

	"github.com/mamaar/gorefactor/pkg/types"
)

func TestRenameMethodAcrossConfigs_SingleConfigPlanEqualsSingleWorkspacePlan(t *testing.T) {
	eng, _ := loadConfigsEngine(t, map[string]string{
		"p/t.go":   "package p\n\ntype T struct{}\n\nfunc (T) M() {}\n",
		"p/use.go": "package p\n\nfunc Use(t T) { t.M() }\n",
	})
	req := types.RenameMethodRequest{TypeName: "T", MethodName: "M", NewMethodName: "N"}

	want, err := eng.RenameMethod(eng.WorkspaceSet().Primary().Workspace, req)
	if err != nil {
		t.Fatal(err)
	}

	got, err := eng.RenameMethodAcrossConfigs(req)
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

func TestRenameMethodPlanner_LinkedSupersetKeepsPlan(t *testing.T) {
	eng, _ := loadConfigsEngine(t, map[string]string{
		"p/t_default.go": "//go:build !tagx\n\npackage p\n\ntype T struct{}\n\nfunc (T) M() {}\n",
		"p/t_tagx.go":    "//go:build tagx\n\npackage p\n\ntype T struct{}\n\nfunc (T) M() {}\n",
		"p/use.go":       "package p\n\nfunc Use(t T) { t.M() }\n",
	})

	views, err := eng.WorkspaceSet().Views()
	if err != nil {
		t.Fatal(err)
	}

	planner := &renameMethodPlanner{req: types.RenameMethodRequest{TypeName: "T", MethodName: "M", NewMethodName: "N"}}

	first, err := planner.planIn(views[0], nil)
	if err != nil {
		t.Fatal(err)
	}

	linked := first.changed.Clone()
	linked[types.SymbolKey{PkgPath: "example.com/threadtest/p", Recv: "T", Name: "absent"}] = struct{}{}

	second, err := planner.planIn(views[0], linked)
	if err != nil {
		t.Fatal(err)
	}

	got, want := sortedChanges(second.plan), sortedChanges(first.plan)
	if !slices.Equal(got, want) {
		t.Errorf("replanned changes differ:\n got %+v\nwant %+v", got, want)
	}
}

func TestRenameMethodAcrossConfigs_NewNameTakenOnTypeOnlyUnderTagAborts(t *testing.T) {
	eng, _ := loadConfigsEngine(t, map[string]string{
		"p/t.go":      "package p\n\ntype T struct{}\n\nfunc (T) M() {}\n",
		"p/n_tagx.go": "//go:build tagx\n\npackage p\n\nfunc (T) N() {}\n",
	})

	_, err := eng.RenameMethodAcrossConfigs(types.RenameMethodRequest{TypeName: "T", MethodName: "M", NewMethodName: "N"})

	if err == nil || !strings.Contains(err.Error(), "tags=tagx") || !strings.Contains(err.Error(), "method N already exists on type T") {
		t.Errorf("error = %v, want a conflict naming tags=tagx", err)
	}
}
