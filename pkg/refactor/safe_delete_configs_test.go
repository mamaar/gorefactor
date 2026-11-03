package refactor

import (
	"maps"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/mamaar/gorefactor/pkg/analysis"
	"github.com/mamaar/gorefactor/pkg/types"
)

func portedCallersWorkspace() map[string]string {
	return map[string]string{
		"p/leaf.go":      "package p\n\nfunc leaf() {}\n\nfunc Shared() { leaf() }\n",
		"p/p_tagx.go":    "//go:build tagx\n\npackage p\n\nfunc tagged() { leaf() }\n",
		"p/p_windows.go": "package p\n\nfunc win() { leaf() }\n",
		"p/p_darwin.go":  "package p\n\nfunc mac() { leaf() }\n",
		"p/p_js.go":      "package p\n\nfunc web() { leaf() }\n",
	}
}

func TestSafeDeleteBlockingReferences_AttributesEachReferenceToItsConfigs(t *testing.T) {
	eng, dir := loadConfigsEngine(t, portedCallersWorkspace())

	got, err := eng.SafeDeleteBlockingReferences(types.SafeDeleteRequest{Symbol: "leaf", SourceFile: filepath.Join(dir, "p/leaf.go")})

	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 5 {
		t.Fatalf("blockers = %+v, want one per call site", got)
	}
	owners := make(map[string]string)
	for _, b := range got {
		file := filepath.Base(b.File)
		if file == "leaf.go" {
			if len(b.Configs) != 0 {
				t.Errorf("leaf.go is in every configuration, got Configs %v", b.Configs)
			}
			continue
		}
		if len(b.Configs) != 1 {
			t.Fatalf("%s: Configs = %v, want the one configuration that includes it", file, b.Configs)
		}
		other, taken := owners[b.Configs[0]]
		if taken {
			t.Errorf("%s and %s are both attributed to %s", file, other, b.Configs[0])
		}
		owners[b.Configs[0]] = file
	}
	if !strings.Contains(strings.Join(slices.Collect(maps.Keys(owners)), "|"), "tags=tagx") {
		t.Errorf("no blocker attributed to the tagx configuration: %v", owners)
	}
}

type observingSafeDelete struct {
	*safeDeletePlanner
	held []int
}

func (p *observingSafeDelete) planIn(v *analysis.View, linked types.SymbolKeySet) (configResult, error) {
	p.held = append(p.held, p.heldSecondaries())
	return p.safeDeletePlanner.planIn(v, linked)
}

func TestSafeDeleteAcrossConfigs_BoundsAndReleasesSecondaryViews(t *testing.T) {
	eng, dir := loadConfigsEngine(t, portedCallersWorkspace())
	planner := &observingSafeDelete{safeDeletePlanner: &safeDeletePlanner{
		req: types.SafeDeleteRequest{Symbol: "leaf", SourceFile: filepath.Join(dir, "p/leaf.go")}}}

	_, err := eng.planAcrossConfigs(planner)

	if err == nil || !strings.Contains(err.Error(), "found 5 references") {
		t.Fatalf("err = %v, want the delete refused for 5 references", err)
	}
	if !reflect.DeepEqual(planner.held, []int{2, 2, 2, 2, 1}) {
		t.Errorf("secondary views holding type info at each plan = %v, want [2 2 2 2 1]", planner.held)
	}
	views, err := eng.WorkspaceSet().Views()
	if err != nil {
		t.Fatal(err)
	}
	for _, v := range views[1:] {
		for _, pkg := range v.Workspace.Packages {
			if pkg.TypesChecked || pkg.TypesInfo != nil {
				t.Errorf("view %s still holds type info for %s", v.Config, pkg.ImportPath)
			}
		}
	}
}

func TestSafeDeleteAcrossConfigs_MentionInUncheckedFileOfAnImporterBlocks(t *testing.T) {
	eng, dir := loadConfigsEngine(t, map[string]string{
		"p/p.go":         "package p\n\nfunc Exported() {}\n\nfunc unexported() {}\n",
		"q/q.go":         "package q\n",
		"q/gen.go":       "//go:build ignore\n\npackage main\n\nimport \"example.com/threadtest/p\"\n\nfunc main() { p.Exported(); unexported() }\n",
		"r/r.go":         "package r\n",
		"r/unrelated.go": "//go:build ignore\n\npackage main\n\nfunc main() { Exported() }\n",
	})

	for symbol, wantFiles := range map[string][]string{"Exported": {"gen.go"}, "unexported": nil} {
		got, err := eng.SafeDeleteBlockingReferences(types.SafeDeleteRequest{Symbol: symbol, SourceFile: filepath.Join(dir, "p/p.go")})
		if err != nil {
			t.Fatal(err)
		}
		var files []string
		for _, b := range got {
			if b.Unverified == "" {
				t.Errorf("%s: blocker %+v is not marked unverified", symbol, b)
			}
			files = append(files, filepath.Base(b.File))
		}
		if !reflect.DeepEqual(files, wantFiles) {
			t.Errorf("%s: blocked by %v, want %v", symbol, files, wantFiles)
		}
	}
}
