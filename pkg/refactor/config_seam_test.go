package refactor

import (
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"

	"github.com/mamaar/gorefactor/pkg/analysis"
	"github.com/mamaar/gorefactor/pkg/types"
)

// fakePlanner answers planIn from a function and records the linked set of every call.
type fakePlanner struct {
	plan  func(v *analysis.View, linked types.SymbolKeySet) (configResult, error)
	calls map[*analysis.View][]types.SymbolKeySet
}

func (p *fakePlanner) planIn(v *analysis.View, linked types.SymbolKeySet) (configResult, error) {
	if p.calls == nil {
		p.calls = make(map[*analysis.View][]types.SymbolKeySet)
	}

	p.calls[v] = append(p.calls[v], linked.Clone())

	return p.plan(v, linked)
}

func fakeViews() (primary, tagged *analysis.View) {
	return &analysis.View{Config: analysis.NewBuildConfig("linux", "amd64", true)},
		&analysis.View{Config: analysis.NewBuildConfig("linux", "amd64", true, "tagx")}
}

func fnKey(name string) types.SymbolKey { return types.SymbolKey{PkgPath: "m/p", Name: name} }

func TestPlanAcrossViews_RerunsViewsUntilNoNewKeysAppear(t *testing.T) {
	primary, tagged := fakeViews()
	planner := &fakePlanner{plan: func(v *analysis.View, linked types.SymbolKeySet) (configResult, error) {
		if v == tagged {
			return configResult{plan: planOf(ins("/ws/a.go", 1, "a", "a")), changed: linkedKeys(fnKey("a"))}, nil
		}

		if linked.Has(fnKey("a")) {
			return configResult{plan: planOf(ins("/ws/b.go", 2, "b", "b")), changed: linkedKeys(fnKey("b"))}, nil
		}

		return configResult{plan: planOf(ins("/ws/stale.go", 3, "s", "s"))}, nil
	}}

	plan, rounds, err := planAcrossViews([]*analysis.View{primary, tagged}, planner, 10)
	if err != nil {
		t.Fatal(err)
	}

	if rounds != 2 {
		t.Errorf("rounds = %d, want 2", rounds)
	}

	got := len(planner.calls[primary])
	if got != 2 {
		t.Errorf("primary planned %d times, want 2", got)
	}

	got = len(planner.calls[tagged])
	if got != 2 {
		t.Errorf("tagged view planned %d times, want 2 (it has not seen key b)", got)
	}

	want := []types.Change{ins("/ws/b.go", 2, "b", "b"), ins("/ws/a.go", 1, "a", "a")}
	if !reflect.DeepEqual(plan.Changes, want) {
		t.Errorf("merged changes = %+v, want only the final plans: %+v", plan.Changes, want)
	}

	wantConfigs := []string{primary.Config.String(), tagged.Config.String()}
	if !reflect.DeepEqual(plan.Coverage.Configs, wantConfigs) {
		t.Errorf("Coverage.Configs = %v, want %v", plan.Coverage.Configs, wantConfigs)
	}
}

func TestPlanAcrossViews_StopsAfterOneRoundWhenKeysAreStable(t *testing.T) {
	primary, tagged := fakeViews()
	planner := &fakePlanner{plan: func(*analysis.View, types.SymbolKeySet) (configResult, error) {
		return configResult{plan: planOf(ins("/ws/a.go", 1, "a", "a")), changed: linkedKeys(fnKey("a"))}, nil
	}}

	plan, rounds, err := planAcrossViews([]*analysis.View{primary, tagged}, planner, 10)
	if err != nil {
		t.Fatal(err)
	}

	if rounds != 1 || len(planner.calls[primary]) != 1 || len(planner.calls[tagged]) != 1 {
		t.Errorf("rounds = %d, calls = %d/%d; want one of each", rounds, len(planner.calls[primary]), len(planner.calls[tagged]))
	}

	if len(plan.Changes) != 1 {
		t.Errorf("identical changes not deduplicated: %+v", plan.Changes)
	}
}

func TestPlanAcrossViews_ExceedingTheRoundBoundIsAnError(t *testing.T) {
	primary, tagged := fakeViews()
	n := 0
	planner := &fakePlanner{plan: func(*analysis.View, types.SymbolKeySet) (configResult, error) {
		n++
		return configResult{plan: planOf(), changed: linkedKeys(fnKey(fmt.Sprint("f", n)))}, nil
	}}

	_, _, err := planAcrossViews([]*analysis.View{primary, tagged}, planner, 5)
	if err == nil || !strings.Contains(err.Error(), "5 rounds") {
		t.Fatalf("err = %v, want the round bound reported", err)
	}
}

func TestPlanAcrossViews_PlannerErrorNamesItsConfig(t *testing.T) {
	primary, tagged := fakeViews()
	boom := errors.New("boom")
	planner := &fakePlanner{plan: func(v *analysis.View, _ types.SymbolKeySet) (configResult, error) {
		if v == tagged {
			return configResult{}, boom
		}

		return configResult{plan: planOf()}, nil
	}}

	_, _, err := planAcrossViews([]*analysis.View{primary, tagged}, planner, 10)
	if !errors.Is(err, boom) {
		t.Fatalf("err = %v, want it to wrap the planner error", err)
	}

	want := "build configuration " + tagged.Config.String() + ": boom"
	if err.Error() != want {
		t.Errorf("err = %q, want %q", err, want)
	}
}

func TestPlanAcrossViews_SingleViewErrorIsReturnedUnchanged(t *testing.T) {
	primary, _ := fakeViews()
	boom := errors.New("boom")
	planner := &fakePlanner{plan: func(*analysis.View, types.SymbolKeySet) (configResult, error) {
		return configResult{}, boom
	}}

	_, _, err := planAcrossViews([]*analysis.View{primary}, planner, 10)
	if err != boom { //nolint:errorlint // pins the error as returned unchanged
		t.Errorf("err = %v, want the planner error itself", err)
	}
}

func TestPlanAcrossViews_TargetAbsentEverywhereReturnsThePrimaryError(t *testing.T) {
	primary, tagged := fakeViews()
	primaryErr := errors.New("not in primary")
	planner := &fakePlanner{plan: func(v *analysis.View, _ types.SymbolKeySet) (configResult, error) {
		if v == primary {
			return configResult{absent: primaryErr}, nil
		}

		return configResult{absent: errors.New("not in tagged")}, nil
	}}

	_, _, err := planAcrossViews([]*analysis.View{primary, tagged}, planner, 10)
	if err != primaryErr { //nolint:errorlint // pins the error as returned unchanged
		t.Errorf("err = %v, want the primary view's error unchanged", err)
	}
}

func TestPlanAcrossViews_AbsentViewContributesNothing(t *testing.T) {
	primary, tagged := fakeViews()
	planner := &fakePlanner{plan: func(v *analysis.View, _ types.SymbolKeySet) (configResult, error) {
		if v == primary {
			return configResult{absent: errors.New("not in primary")}, nil
		}

		return configResult{plan: planOf(ins("/ws/a.go", 1, "a", "a"))}, nil
	}}

	plan, _, err := planAcrossViews([]*analysis.View{primary, tagged}, planner, 10)
	if err != nil {
		t.Fatal(err)
	}

	if len(plan.Changes) != 1 || len(plan.Coverage.Configs) != 2 {
		t.Errorf("plan = %+v", plan)
	}
}

func TestViewIndexes_PrepareTypeChecksAndIndexesTheViews(t *testing.T) {
	eng, _ := loadConfigsEngine(t, map[string]string{
		"p/p.go":      "package p\n\nfunc P() {}\n",
		"p/p_tagx.go": "//go:build tagx\n\npackage p\n\nfunc Tagged() { P() }\n",
		"p/p_win.go":  "//go:build windows\n\npackage p\n\nfunc Win() { P() }\n",
	})

	views, err := eng.WorkspaceSet().Views()
	if err != nil {
		t.Fatal(err)
	}

	if len(views) != 3 {
		t.Fatalf("views = %d, want 3", len(views))
	}

	var c viewIndexes
	c.prepare(views)

	seen := make(map[*analysis.ReferenceIndex]bool)

	for _, v := range views {
		idx := c.indexOf(v)
		if idx == nil || !idx.HasTypeInfo() {
			t.Fatalf("view %s: no typed index", v.Config)
		}

		if seen[idx] {
			t.Errorf("view %s shares its index with another view", v.Config)
		}

		seen[idx] = true
		if c.indexOf(v) != idx {
			t.Errorf("view %s: indexOf rebuilt the prepared index", v.Config)
		}

		for _, pkg := range v.Workspace.Packages {
			if !pkg.TypesChecked {
				t.Errorf("view %s: %s was not type-checked", v.Config, pkg.ImportPath)
			}
		}
	}

	c.finish()

	for i, v := range views {
		for _, pkg := range v.Workspace.Packages {
			if pkg.TypesChecked != (i == 0) {
				t.Errorf("after finish, view %s: %s type-checked = %v", v.Config, pkg.ImportPath, pkg.TypesChecked)
			}
		}
	}
}

func TestPlanAcrossViews_ReplansAViewOnlyForKeysItDeclares(t *testing.T) {
	for _, tc := range []struct {
		name     string
		declared types.SymbolKeySet
		want     int
	}{
		{"new key not declared", linkedKeys(fnKey("a")), 1},
		{"new key declared", linkedKeys(fnKey("a"), fnKey("b")), 2},
		{"declarations unknown", nil, 2},
	} {
		primary, tagged := fakeViews()
		planner := &fakePlanner{plan: func(v *analysis.View, linked types.SymbolKeySet) (configResult, error) {
			if v == tagged {
				return configResult{plan: planOf(ins("/ws/b.go", 2, "b", "b")), changed: linkedKeys(fnKey("a"), fnKey("b"))}, nil
			}

			return configResult{plan: planOf(ins("/ws/a.go", 1, "a", "a")), changed: linkedKeys(fnKey("a")), declared: tc.declared}, nil
		}}

		plan, _, err := planAcrossViews([]*analysis.View{primary, tagged}, planner, 10)
		if err != nil {
			t.Fatalf("%s: %v", tc.name, err)
		}

		got := len(planner.calls[primary])
		if got != tc.want {
			t.Errorf("%s: primary planned %d times, want %d", tc.name, got, tc.want)
		}

		if len(plan.Changes) != 2 {
			t.Errorf("%s: merged %d changes, want 2", tc.name, len(plan.Changes))
		}
	}
}

func TestThreadContextPlanner_ReportsTheKeysTheViewDeclares(t *testing.T) {
	eng, _ := loadConfigsEngine(t, map[string]string{
		"p/p.go":      "package p\n\ntype S struct{}\n\nfunc (S) M() {}\n\nfunc leaf() {}\n",
		"p/p_tagx.go": "//go:build tagx\n\npackage p\n\nfunc tagged() { leaf() }\n",
	})

	views, err := eng.WorkspaceSet().Views()
	if err != nil {
		t.Fatal(err)
	}

	planner := &threadContextPlanner{engine: eng, req: types.ThreadContextRequest{FunctionName: "leaf", SourceFile: "p/p.go"}}
	pkgPath := "example.com/threadtest/p"

	for i, v := range views {
		res, err := planner.planIn(v, nil)
		if err != nil {
			t.Fatal(err)
		}

		for key, want := range map[types.SymbolKey]bool{
			{PkgPath: pkgPath, Name: "leaf"}:         true,
			{PkgPath: pkgPath, Recv: "S", Name: "M"}: true,
			{PkgPath: pkgPath, Name: "tagged"}:       i == 1,
		} {
			got := res.declared.Has(key)
			if got != want {
				t.Errorf("view %s: declares %s = %v, want %v", v.Config, key, got, want)
			}
		}
	}
}
