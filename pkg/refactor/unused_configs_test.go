package refactor

import (
	"path/filepath"
	"reflect"
	"testing"

	"github.com/mamaar/gorefactor/pkg/analysis"
	"github.com/mamaar/gorefactor/pkg/types"
)

func unusedNames(findings []UnusedFinding) []string {
	names := []string{}
	for _, f := range findings {
		names = append(names, filepath.Base(f.Symbol.File)+":"+f.Symbol.Name)
	}

	return names
}

func TestUnusedAcrossConfigs_UseInAnyPortKeepsTheSymbol(t *testing.T) {
	files := portedCallersWorkspace()
	files["p/leaf.go"] = "package p\n\nfunc leaf() {}\n\nfunc onlyWindows() {}\n\nfunc dead() {}\n"
	files["p/p_windows.go"] = "package p\n\nfunc Win() { leaf(); onlyWindows() }\n"
	eng, _ := loadConfigsEngine(t, files)

	report, err := eng.UnusedAcrossConfigs(UnusedRequest{})
	if err != nil {
		t.Fatal(err)
	}

	want := []string{"leaf.go:dead", "p_darwin.go:mac", "p_js.go:web", "p_tagx.go:tagged"}

	got := unusedNames(report.Unused)
	if !reflect.DeepEqual(got, want) {
		t.Errorf("unused = %v, want %v", got, want)
	}

	if len(report.Configs) != 5 || len(report.PossiblyUsed) != 0 {
		t.Errorf("configs = %v, possibly used = %v", report.Configs, unusedNames(report.PossiblyUsed))
	}

	for _, f := range report.Unused {
		wantConfigs := map[bool]int{true: 0, false: 1}[f.Symbol.Name == "dead"]
		if len(f.Configs) != wantConfigs {
			t.Errorf("%s: Configs = %v, want %d configuration(s) named", f.Symbol.Name, f.Configs, wantConfigs)
		}
	}
}

func TestUnusedAcrossConfigs_BoundsAndReleasesSecondaryViews(t *testing.T) {
	eng, _ := loadConfigsEngine(t, portedCallersWorkspace())
	run := &unusedRun{engine: eng, used: make(types.SymbolKeySet), decls: make(map[declSite]*unusedDecl)}

	views, err := eng.beginRun(run)
	if err != nil {
		t.Fatal(err)
	}

	var held []int
	for _, v := range views {
		held = append(held, run.heldSecondaries())

		run.visit(v)
	}

	eng.endRun(run)

	if !reflect.DeepEqual(held, []int{2, 2, 2, 2, 1}) {
		t.Errorf("secondary views holding type info at each visit = %v, want [2 2 2 2 1]", held)
	}

	for _, v := range views[1:] {
		for _, pkg := range v.Workspace.Packages {
			if pkg.TypesChecked || pkg.TypesInfo != nil {
				t.Errorf("view %s still holds type info for %s", v.Config, pkg.ImportPath)
			}
		}
	}
}

func TestUnusedAcrossConfigs_PackagesFilterKeepsOnlyThose(t *testing.T) {
	eng, _ := loadConfigsEngine(t, map[string]string{
		"p/p.go": "package p\n\nfunc deadP() {}\n",
		"q/q.go": "package q\n\nfunc deadQ() {}\n",
	})

	report, err := eng.UnusedAcrossConfigs(UnusedRequest{Packages: []string{"example.com/threadtest/q"}})
	if err != nil {
		t.Fatal(err)
	}

	got := unusedNames(report.Unused)
	if !reflect.DeepEqual(got, []string{"q.go:deadQ"}) {
		t.Errorf("unused = %v, want only deadQ", got)
	}
}

func findingNamed(findings []UnusedFinding, file, name string) *UnusedFinding {
	for i := range findings {
		if filepath.Base(findings[i].Symbol.File) == file && findings[i].Symbol.Name == name {
			return &findings[i]
		}
	}

	return nil
}

func TestUnusedAcrossConfigs_DefaultModeReportsFindingsThatAreNotSafeToDelete(t *testing.T) {
	eng, _ := loadConfigsEngine(t, map[string]string{
		"p/p.go": "package p\n\nfunc helper() {}\n\nfunc deadCaller() { helper() }\n",
	})

	report, err := eng.UnusedAcrossConfigs(UnusedRequest{})
	if err != nil {
		t.Fatal(err)
	}

	helper := findingNamed(report.Unused, "p.go", "helper")
	if helper == nil {
		t.Fatalf("unused = %v, want helper reported", unusedNames(report.Unused))
	}

	if helper.Cause != analysis.CauseOnlyUnusedCode || helper.SafeToDelete {
		t.Errorf("helper: cause %v, safe %v, want only unused code and not safe", helper.Cause, helper.SafeToDelete)
	}

	caller := findingNamed(report.Unused, "p.go", "deadCaller")
	if caller == nil || !caller.SafeToDelete {
		t.Errorf("deadCaller = %+v, want reported and safe to delete", caller)
	}
}

func TestUnusedAcrossConfigs_TestReferenceInOneConfigMakesTheFindingNotSafeToDelete(t *testing.T) {
	eng, _ := loadConfigsEngine(t, map[string]string{
		"p/p.go":           "package p\n\nfunc helper() {}\n",
		"p/p_tagx_test.go": "//go:build tagx\n\npackage p\n\nimport \"testing\"\n\nfunc TestHelper(t *testing.T) { helper() }\n",
	})

	report, err := eng.UnusedAcrossConfigs(UnusedRequest{})
	if err != nil {
		t.Fatal(err)
	}

	helper := findingNamed(report.Unused, "p.go", "helper")
	if helper == nil {
		t.Fatalf("unused = %v, want helper reported", unusedNames(report.Unused))
	}

	if helper.Cause != analysis.CauseOnlyTests || helper.SafeToDelete || helper.Reason != "Only referenced from tests" {
		t.Errorf("helper: cause %v, reason %q, safe %v, want only tests and not safe", helper.Cause, helper.Reason, helper.SafeToDelete)
	}

	if len(helper.Referrers) != 1 || helper.Referrers[0].Name != "TestHelper" {
		t.Errorf("referrers = %v, want TestHelper", helper.Referrers)
	}
}

func TestUnusedAcrossConfigs_CalleeOfDeadSiblingWhoseKeyIsUsedElsewhereIsNotReported(t *testing.T) {
	eng, _ := loadConfigsEngine(t, map[string]string{
		"p/f_default.go": "//go:build !tagx\n\npackage p\n\nfunc f() { g() }\n",
		"p/g_default.go": "//go:build !tagx\n\npackage p\n\nfunc g() {}\n",
		"p/f_tagx.go":    "//go:build tagx\n\npackage p\n\nfunc f() {}\n",
		"p/use_tagx.go":  "//go:build tagx\n\npackage p\n\nfunc Use() { f() }\n",
	})

	report, err := eng.UnusedAcrossConfigs(UnusedRequest{})
	if err != nil {
		t.Fatal(err)
	}

	got := unusedNames(report.Unused)
	if len(got) != 0 {
		t.Errorf("unused = %v, want none: f is used under tagx and g only by f", got)
	}
}

func TestUnusedAcrossConfigs_ProductionUseUnderOneTagKeepsCalleeUnreportedDespiteDeadCallerElsewhere(t *testing.T) {
	eng, _ := loadConfigsEngine(t, map[string]string{
		"p/g.go":         "package p\n\nfunc g() {}\n",
		"p/c_default.go": "//go:build !tagx\n\npackage p\n\nfunc caller() { g() }\n",
		"p/c_tagx.go":    "//go:build tagx\n\npackage p\n\nfunc caller() { g() }\n",
		"p/use_tagx.go":  "//go:build tagx\n\npackage p\n\nfunc Use() { caller() }\n",
	})

	report, err := eng.UnusedAcrossConfigs(UnusedRequest{})
	if err != nil {
		t.Fatal(err)
	}

	got := unusedNames(report.Unused)
	if len(got) != 0 {
		t.Errorf("unused = %v, want none", got)
	}
}
