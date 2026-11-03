package analysis

import (
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

func TestUncheckedFiles_ReturnsFilesNoViewTypeChecks(t *testing.T) {
	set, root := loadSet(t, map[string]string{
		"p/p.go":            "package p\n\nfunc F() {}\n",
		"p/tagged_test.go":  "//go:build tagx\n\npackage p\n\nimport _ \"example.com/missing/dep\"\n",
		"p/h_notag_test.go": "//go:build !tagx\n\npackage p\n\nfunc helper() {}\n",
		"p/shared_test.go":  "package p\n\nfunc useHelper() { helper() }\n",
		"p/gen.go":          "//go:build ignore\n\npackage main\n",
		"p/never.go":        "//go:build linux && !linux\n\npackage p\n",
		"p/broken.go":       "package p\n\nfunc (\n",
		"q/q.go":            "package q\n",
		"q/bad_test.go":     "package q\n\nimport _ \"example.com/missing/dep\"\n",
	})
	views := allViews(t, set)
	if len(views) != 2 {
		t.Fatalf("views = %d, want primary and tagx", len(views))
	}
	for _, v := range views {
		for _, pkg := range v.Workspace.Packages {
			v.Parser.EnsureTypeChecked(v.Workspace, pkg)
		}
	}
	p := filepath.Join(root, "p")

	got := UncheckedFiles(views, []string{p, p})

	var names []string
	byName := map[string]UncheckedFile{}
	for _, u := range got {
		names = append(names, u.Source.Name)
		byName[u.Source.Name] = u
	}
	want := []string{"broken.go", "gen.go", "never.go", "tagged_test.go"}
	if !slices.Equal(names, want) {
		t.Fatalf("unchecked = %v, want %v", names, want)
	}
	tagged := byName["tagged_test.go"]
	if len(tagged.Configs) != 1 || tagged.Configs[0].Key() != views[1].Config.Key() || !strings.Contains(tagged.Reason, "could not import") {
		t.Errorf("tagged_test.go = %+v, want the tagx view and its import error", tagged)
	}
	u := byName["gen.go"]
	if len(u.Configs) != 0 || u.Reason != "//go:build ignore" {
		t.Errorf("gen.go = %+v", u)
	}
	u = byName["never.go"]
	if len(u.Configs) != 0 || u.Reason != "unsatisfiable build constraint: linux && !linux" {
		t.Errorf("never.go = %+v", u)
	}
	u = byName["broken.go"]
	if !strings.HasPrefix(u.Reason, "parse error: ") {
		t.Errorf("broken.go = %+v", u)
	}
}
