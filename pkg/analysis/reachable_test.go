package analysis

import (
	"io"
	"log/slog"
	"slices"
	"strings"
	"testing"

	"github.com/mamaar/gorefactor/pkg/types"
)

var reachableFiles = map[string]string{
	"dep/dep.go": `package dep

func Dead() {}
func ViaIface() {}
func ViaField() {}
func ViaVar() int { return 1 }
func ViaInit() {}
func ViaTest() {}
`,
	"side/side.go": `package side

import "example.com/w/dep"

var V = dep.ViaVar()

func init() { dep.ViaInit() }
`,
	"cmd/main.go": `package main

import (
	"example.com/w/dep"
	_ "example.com/w/side"
)

type Doer interface{ Do() }

type impl struct{}

func (impl) Do() { dep.ViaIface() }

type holder struct{ fn func() }

func main() {
	var d Doer = impl{}
	d.Do()
	h := holder{fn: func() { dep.ViaField() }}
	h.fn()
}

func unreachable() { dep.Dead() }
`,
	"cmd/main_test.go": `package main

import "example.com/w/dep"

func TestX() { dep.ViaTest() }
`,
}

// reachableSites returns the sites of every exported func of dep, keyed by
// target name.
func reachableSites(t *testing.T, includeTests bool) map[string][]ReachableSite {
	t.Helper()
	files := map[string]string{}
	for k, v := range reachableFiles {
		files[k] = v
	}
	set, _ := loadSet(t, files)
	v := set.Primary()
	for _, pkg := range v.Workspace.Packages {
		v.Parser.EnsureTypeChecked(v.Workspace, pkg)
	}
	idx, err := v.ReferenceIndex()
	if err != nil {
		t.Fatal(err)
	}
	ua := NewUnusedAnalyzer(v.Workspace, slog.New(slog.NewTextHandler(io.Discard, nil)))
	ua.SetIndex(idx)

	targets, err := ResolveTargets(v.Workspace, []string{"example.com/w/dep"})
	if err != nil {
		t.Fatal(err)
	}
	var entry *types.Package
	for _, pkg := range v.Workspace.Packages {
		if pkg.Name == "main" {
			entry = pkg
		}
	}
	sites, err := ua.ReachableSites(entry, targets, includeTests)
	if err != nil {
		t.Fatal(err)
	}
	byTarget := map[string][]ReachableSite{}
	for _, s := range sites {
		byTarget[s.Target] = append(byTarget[s.Target], s)
	}
	return byTarget
}

func TestReachableSites(t *testing.T) {
	got := reachableSites(t, false)
	for _, name := range []string{"dep.ViaIface", "dep.ViaField", "dep.ViaVar", "dep.ViaInit"} {
		if len(got[name]) != 1 {
			t.Errorf("%s: got %d sites, want 1", name, len(got[name]))
		}
	}
	for _, name := range []string{"dep.Dead", "dep.ViaTest"} {
		if len(got[name]) != 0 {
			t.Errorf("%s: unexpected sites %v", name, got[name])
		}
	}
}

func TestReachableSites_IncludeTests(t *testing.T) {
	got := reachableSites(t, true)
	if len(got["dep.ViaTest"]) != 1 {
		t.Errorf("dep.ViaTest: got %v, want 1 site", got["dep.ViaTest"])
	}
	if len(got["dep.Dead"]) != 0 {
		t.Errorf("dep.Dead: unexpected sites %v", got["dep.Dead"])
	}
}

func TestReachableSites_PathRunsFromMainToEnclosing(t *testing.T) {
	sites := reachableSites(t, false)["dep.ViaField"]
	if len(sites) != 1 {
		t.Fatalf("got %d sites, want 1", len(sites))
	}
	s := sites[0]
	want := []string{"main.main"}
	if !slices.Equal(s.Path, want) || s.Enclosing != "main" || s.Kind != "call" {
		t.Errorf("path %v enclosing %q kind %q, want %v main call", s.Path, s.Enclosing, s.Kind, want)
	}

	s = reachableSites(t, false)["dep.ViaIface"][0]
	want = []string{"main.main", "main.impl", "main.impl.Do"}
	if !slices.Equal(s.Path, want) || s.Enclosing != "impl.Do" {
		t.Errorf("path %v enclosing %q, want %v impl.Do", s.Path, s.Enclosing, want)
	}
}

func TestResolveTargets_UnknownSpecIsAnError(t *testing.T) {
	set, _ := loadSet(t, map[string]string{"dep/dep.go": "package dep\n\nfunc F() {}\n"})
	_, err := ResolveTargets(set.Primary().Workspace, []string{"example.com/w/dep.Nope"})
	if err == nil || !strings.Contains(err.Error(), "example.com/w/dep.Nope") {
		t.Errorf("err = %v, want one naming the spec", err)
	}
}
