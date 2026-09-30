package refactor

import (
	"path/filepath"
	"reflect"
	"testing"

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
	if got := unusedNames(report.Unused); !reflect.DeepEqual(got, want) {
		t.Errorf("unused = %v, want %v", got, want)
	}
	if len(report.Configs) != 5 || len(report.PossiblyUsed) != 0 {
		t.Errorf("configs = %v, possibly used = %v", report.Configs, unusedNames(report.PossiblyUsed))
	}
	for _, f := range report.Unused {
		if wantConfigs := map[bool]int{true: 0, false: 1}[f.Symbol.Name == "dead"]; len(f.Configs) != wantConfigs {
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
		if err := run.visit(v); err != nil {
			t.Fatal(err)
		}
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
	if got := unusedNames(report.Unused); !reflect.DeepEqual(got, []string{"q.go:deadQ"}) {
		t.Errorf("unused = %v, want only deadQ", got)
	}
}
