package refactor

import (
	"slices"
	"testing"

	"github.com/mamaar/gorefactor/pkg/types"
)

const (
	acpDefault = "//go:build !tagx\n\npackage p\n\nfunc f(n int) int { return n }\n"
	acpTagx    = "//go:build tagx\n\npackage p\n\nfunc f(n int) int { return n + 1 }\n"
)

func TestAddContextParameterPlanner_LinkedSupersetKeepsPlanAndDoesNotDoubleTheParameter(t *testing.T) {
	eng, dir := loadConfigsEngine(t, map[string]string{
		"p/f_default.go": acpDefault,
		"p/f_tagx.go":    acpTagx,
		"p/use.go":       "package p\n\nfunc Use() int { return f(1) }\n",
	})

	views, err := eng.WorkspaceSet().Views()
	if err != nil {
		t.Fatal(err)
	}

	planner := &addContextParameterPlanner{engine: eng, req: types.AddContextParameterRequest{FunctionName: "f", SourceFile: dir + "/p/f_default.go"}}

	first, err := planner.planIn(views[0], nil)
	if err != nil {
		t.Fatal(err)
	}

	linked := first.changed.Clone()
	linked[types.SymbolKey{PkgPath: "example.com/threadtest/p", Name: "f"}] = struct{}{}
	linked[types.SymbolKey{PkgPath: "example.com/threadtest/p", Name: "absent"}] = struct{}{}

	second, err := planner.planIn(views[0], linked)
	if err != nil {
		t.Fatal(err)
	}

	got, want := sortedChanges(second.plan), sortedChanges(first.plan)
	if !slices.Equal(got, want) {
		t.Errorf("replanned changes differ:\n got %+v\nwant %+v", got, want)
	}

	if len(planner.indexes) != 1 {
		t.Errorf("reference indexes built = %d, want 1 reused across both plans", len(planner.indexes))
	}

	if err := eng.ExecutePlan(second.plan); err != nil {
		t.Fatal(err)
	}

	assertOnce(t, dir, "p/f_default.go", "func f(ctx context.Context, n int) int")
	assertOnce(t, dir, "p/use.go", "f(context.TODO(), 1)")
}

func TestAddContextParameterAcrossConfigs_SiblingThatAlreadyTakesCtxIsSkipped(t *testing.T) {
	eng, dir := loadConfigsEngine(t, map[string]string{
		"p/f_default.go":   acpDefault,
		"p/f_tagx.go":      "//go:build tagx\n\npackage p\n\nimport \"context\"\n\nfunc f(ctx context.Context, n int) int { return n + 1 }\n",
		"p/use_default.go": "//go:build !tagx\n\npackage p\n\nfunc Use() int { return f(1) }\n",
	})
	tagxBefore := readFile(t, dir, "p/f_tagx.go")

	plan, err := eng.AddContextParameterAcrossConfigs(types.AddContextParameterRequest{FunctionName: "f", SourceFile: dir + "/p/f_default.go"})
	if err != nil {
		t.Fatalf("AddContextParameterAcrossConfigs: %v", err)
	}

	if err := eng.ExecutePlan(plan); err != nil {
		t.Fatal(err)
	}

	assertOnce(t, dir, "p/f_default.go", "func f(ctx context.Context, n int) int")
	assertOnce(t, dir, "p/use_default.go", "f(context.TODO(), 1)")

	got := readFile(t, dir, "p/f_tagx.go")
	if got != tagxBefore {
		t.Errorf("f_tagx.go changed:\n%s", got)
	}

	vetBuilds(t, dir, linuxBuild, linuxTagx)
}
