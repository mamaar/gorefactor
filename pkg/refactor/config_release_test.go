package refactor

import (
	"reflect"
	"sync"
	"testing"

	"github.com/mamaar/gorefactor/pkg/types"
)

func siblingWorkspace() map[string]string {
	return map[string]string{
		"p/leaf.go":         "package p\n\nfunc leaf() {}\n",
		"p/open_linux.go":   "package p\n\nfunc open() { leaf() }\n",
		"p/open_windows.go": "package p\n\nfunc open() { leaf() }\n",
		"p/use.go":          "package p\n\nfunc Use() { open() }\n",
		"q/q_tagx.go":       "//go:build tagx\n\npackage q\n\nimport \"example.com/threadtest/p\"\n\nfunc Q() { p.Use() }\n",
		"q/q.go":            "package q\n",
	}
}

func TestPlanAcrossConfigs_ReleasesSecondaryViewsTypeInfo(t *testing.T) {
	eng, _ := loadConfigsEngine(t, siblingWorkspace())
	req := types.ThreadContextRequest{FunctionName: "leaf", SourceFile: "p/leaf.go"}
	first, err := eng.ThreadContextAcrossConfigs(req)
	if err != nil {
		t.Fatal(err)
	}

	views, err := eng.WorkspaceSet().Views()
	if err != nil {
		t.Fatal(err)
	}
	if len(views) != 3 {
		t.Fatalf("views = %d, want 3", len(views))
	}
	for i, v := range views {
		for _, pkg := range v.Workspace.Packages {
			if held := pkg.TypesChecked || pkg.TypesInfo != nil || pkg.TypesPkg != nil; held != (i == 0) {
				t.Errorf("view %s: %s holds type info = %v, want %v", v.Config, pkg.ImportPath, held, i == 0)
			}
		}
	}

	second, err := eng.ThreadContextAcrossConfigs(req)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(sortedChanges(first), sortedChanges(second)) || !reflect.DeepEqual(first.Coverage, second.Coverage) {
		t.Errorf("a run after the release planned differently:\nfirst  %+v\nsecond %+v", sortedChanges(first), sortedChanges(second))
	}
}

func TestPlanAcrossConfigs_ConcurrentRunsPlanTheSame(t *testing.T) {
	eng, dir := loadConfigsEngine(t, siblingWorkspace())
	req := types.ThreadContextRequest{FunctionName: "leaf", SourceFile: "p/leaf.go"}
	want, err := eng.ThreadContextAcrossConfigs(req)
	if err != nil {
		t.Fatal(err)
	}

	plans := make([]*types.RefactoringPlan, 6)
	errs := make([]error, len(plans))
	var wg sync.WaitGroup
	for i := range plans {
		wg.Go(func() {
			if i%2 == 0 {
				plans[i], errs[i] = eng.ThreadContextAcrossConfigs(req)
				return
			}
			plans[i], errs[i] = eng.AddContextParameterAcrossConfigs(types.AddContextParameterRequest{FunctionName: "leaf", SourceFile: dir + "/p/leaf.go"})
		})
	}
	wg.Wait()
	for i, plan := range plans {
		if errs[i] != nil {
			t.Fatalf("run %d: %v", i, errs[i])
		}
		if i%2 == 0 && !reflect.DeepEqual(sortedChanges(plan), sortedChanges(want)) {
			t.Errorf("run %d planned differently:\n got %+v\nwant %+v", i, sortedChanges(plan), sortedChanges(want))
		}
	}
}
