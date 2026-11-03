package refactor

import (
	"slices"
	"strings"
	"testing"

	"github.com/mamaar/gorefactor/pkg/types"
)

func TestRenameFieldAcrossConfigs_SingleConfigPlanEqualsSingleWorkspacePlan(t *testing.T) {
	eng, _ := loadConfigsEngine(t, map[string]string{
		"p/t.go":   "package p\n\ntype T struct{ A int }\n",
		"p/use.go": "package p\n\nfunc Use(t T) int { return t.A }\n",
	})
	req := types.RenameFieldRequest{TypeName: "T", FieldName: "A", NewFieldName: "B"}

	want, err := eng.RenameField(eng.WorkspaceSet().Primary().Workspace, req)
	if err != nil {
		t.Fatal(err)
	}
	got, err := eng.RenameFieldAcrossConfigs(req)
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

func TestRenameFieldPlanner_LinkedSupersetKeepsPlan(t *testing.T) {
	eng, _ := loadConfigsEngine(t, map[string]string{
		"p/t_default.go": "//go:build !tagx\n\npackage p\n\ntype T struct{ A int }\n",
		"p/t_tagx.go":    "//go:build tagx\n\npackage p\n\ntype T struct{ A int }\n",
		"p/use.go":       "package p\n\nfunc Use(t T) int { return t.A }\n",
	})
	views, err := eng.WorkspaceSet().Views()
	if err != nil {
		t.Fatal(err)
	}
	planner := &renameFieldPlanner{engine: eng, req: types.RenameFieldRequest{TypeName: "T", FieldName: "A", NewFieldName: "B"}}

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
	if len(first.changed) != 1 {
		t.Errorf("changed = %v, want the one field key", first.changed)
	}
}

func TestRenameFieldAcrossConfigs_NewNameTakenOnStructOnlyUnderTagAborts(t *testing.T) {
	eng, _ := loadConfigsEngine(t, map[string]string{
		"p/t_default.go": "//go:build !tagx\n\npackage p\n\ntype T struct{ A int }\n",
		"p/t_tagx.go":    "//go:build tagx\n\npackage p\n\ntype T struct{ A, B int }\n",
	})

	_, err := eng.RenameFieldAcrossConfigs(types.RenameFieldRequest{TypeName: "T", FieldName: "A", NewFieldName: "B"})

	if err == nil || !strings.Contains(err.Error(), "tags=tagx") || !strings.Contains(err.Error(), "field B already exists on type T") {
		t.Errorf("error = %v, want a conflict naming tags=tagx", err)
	}
}
