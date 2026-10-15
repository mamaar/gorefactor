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
func ViaOther() {}
func ViaShortest() {}
`,
	"side/side.go": `package side

import "example.com/w/dep"

var V = dep.ViaVar()

func init() { dep.ViaInit() }
`,
	"other/other.go": `package other

import "example.com/w/dep"

func init() { dep.ViaOther() }
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
	h := holder{fn: viaFieldFn}
	h.fn()
	short()
	long1()
}

func viaFieldFn() { dep.ViaField() }

func helper() { dep.ViaTest() }

func short() { target() }
func long1() { long2() }
func long2() { long3() }
func long3() { target() }
func target() { dep.ViaShortest() }

func unreachable() { dep.Dead() }
`,
	"cmd/main_test.go": `package main

func TestX() { helper() }
`,
}

// reachableSites returns the sites of every exported func of dep, keyed by
// target name.
func reachableSites(t *testing.T, includeTests bool) map[string][]ReachableSite {
	t.Helper()
	return reachableSitesOf(t, reachableFiles, []string{"example.com/w/dep"}, includeTests)
}

func reachableSitesOf(t *testing.T, files map[string]string, specs []string, includeTests bool) map[string][]ReachableSite {
	t.Helper()
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

	targets, err := ResolveTargets(v.Workspace, specs)
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
	for _, name := range []string{"dep.ViaIface", "dep.ViaField", "dep.ViaVar", "dep.ViaInit", "dep.ViaShortest"} {
		if len(got[name]) != 1 {
			t.Errorf("%s: got %d sites, want 1", name, len(got[name]))
		}
	}
	for _, name := range []string{"dep.Dead", "dep.ViaTest", "dep.ViaOther"} {
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
	want := []string{"main.main", "main.viaFieldFn"}
	if !slices.Equal(s.Path, want) || s.Enclosing != "viaFieldFn" || s.Kind != "call" {
		t.Errorf("path %v enclosing %q kind %q, want %v viaFieldFn call", s.Path, s.Enclosing, s.Kind, want)
	}

	s = reachableSites(t, false)["dep.ViaIface"][0]
	want = []string{"main.main", "main.impl.Do"}
	if !slices.Equal(s.Path, want) || s.Enclosing != "impl.Do" {
		t.Errorf("path %v enclosing %q, want %v impl.Do", s.Path, s.Enclosing, want)
	}
}

func TestReachableSites_PathIsShortest(t *testing.T) {
	s := reachableSites(t, false)["dep.ViaShortest"][0]
	want := []string{"main.main", "main.short", "main.target"}
	if !slices.Equal(s.Path, want) {
		t.Errorf("path %v, want %v", s.Path, want)
	}
}

func TestResolveTargets_UnknownSpecIsAnError(t *testing.T) {
	set, _ := loadSet(t, map[string]string{"dep/dep.go": "package dep\n\nfunc F() {}\n"})
	_, err := ResolveTargets(set.Primary().Workspace, []string{"example.com/w/dep.Nope"})
	if err == nil || !strings.Contains(err.Error(), "example.com/w/dep.Nope") {
		t.Errorf("err = %v, want one naming the spec", err)
	}
}

func TestReachableSites_TestVarSharingAProductionVarNameIsARoot(t *testing.T) {
	files := map[string]string{
		"dep/dep.go":  "package dep\n\nfunc T() {}\n",
		"p/p.go":      "package p\n\nvar x = 1\n",
		"p/p_test.go": "package p_test\n\nimport \"example.com/w/dep\"\n\nvar x = func() int { dep.T(); return 0 }()\n",
		"cmd/main.go": "package main\n\nimport _ \"example.com/w/p\"\n\nfunc main() {}\n",
	}

	got := reachableSitesOf(t, files, []string{"example.com/w/dep.T"}, true)

	if len(got["dep.T"]) != 1 {
		t.Errorf("dep.T: got %v, want 1 site", got["dep.T"])
	}
}

func TestReachableSites_PackageSpecIncludesMethodsOfUnexportedTypes(t *testing.T) {
	files := map[string]string{
		"p2/p2.go":    "package p2\n\ntype impl struct{}\n\nfunc (impl) Do() {}\n\nfunc New() impl { return impl{} }\n",
		"cmd/main.go": "package main\n\nimport \"example.com/w/p2\"\n\nfunc main() { p2.New().Do() }\n",
	}

	got := reachableSitesOf(t, files, []string{"example.com/w/p2"}, false)

	if len(got["p2.impl.Do"]) != 1 {
		t.Errorf("p2.impl.Do: got %v, want 1 site", got["p2.impl.Do"])
	}
}

func TestResolveTargets_OverlappingSpecsYieldEachObjectOnce(t *testing.T) {
	set, _ := loadSet(t, map[string]string{"dep/dep.go": "package dep\n\nfunc F() {}\n"})

	v := set.Primary()
	for _, pkg := range v.Workspace.Packages {
		v.Parser.EnsureTypeChecked(v.Workspace, pkg)
	}

	got, err := ResolveTargets(v.Workspace, []string{"example.com/w/dep", "example.com/w/dep.F"})

	if err != nil || len(got) != 1 {
		t.Errorf("got %v, %v, want one target", got, err)
	}
}

const guardsFiles = `package p

import "example.com/w/dep"

type T struct{}

func (T) Logs()   { dep.Target() }
func (T) helper() { dep.Helped() }

var guards = []func(T){T.helper}

const K = 1
`

func TestReachableSites_MentionedTypeDoesNotMakeItsMethodsLive(t *testing.T) {
	files := map[string]string{
		"dep/dep.go":  "package dep\n\nfunc Target() {}\nfunc Helped() {}\n",
		"p/p.go":      guardsFiles,
		"cmd/main.go": "package main\n\nimport \"example.com/w/p\"\n\nfunc main() { _ = p.K }\n",
	}

	got := reachableSitesOf(t, files, []string{"example.com/w/dep"}, false)

	if len(got["dep.Target"]) != 0 {
		t.Errorf("dep.Target: unexpected sites %v", got["dep.Target"])
	}
	if len(got["dep.Helped"]) != 1 {
		t.Errorf("dep.Helped: got %v, want 1 site (address-taken method)", got["dep.Helped"])
	}
}

func TestReachableSites_MethodNamedByExternalInterfaceStaysLive(t *testing.T) {
	files := map[string]string{
		"dep/dep.go":  "package dep\n\nfunc Target() {}\n",
		"cmd/main.go": "package main\n\nimport (\n\t\"fmt\"\n\t\"example.com/w/dep\"\n)\n\ntype s struct{}\n\nfunc (s) String() string { dep.Target(); return \"\" }\n\nfunc main() { fmt.Println(s{}) }\n",
	}

	got := reachableSitesOf(t, files, []string{"example.com/w/dep"}, false)

	if len(got["dep.Target"]) != 1 {
		t.Errorf("dep.Target: got %v, want 1 site", got["dep.Target"])
	}
}

func TestReachableSites_PackageWithTestImportCycleIsAnalysed(t *testing.T) {
	files := map[string]string{
		"dep/dep.go":  "package dep\n\nfunc T() {}\n",
		"p/p.go":      "package p\n\nimport \"example.com/w/dep\"\n\nfunc F() { dep.T() }\n",
		"p/p_test.go": "package p\n\nimport \"example.com/w/q\"\n\nvar _ = q.G\n",
		"q/q.go":      "package q\n\nimport \"example.com/w/p\"\n\nfunc G() { p.F() }\n",
		"cmd/main.go": "package main\n\nimport \"example.com/w/q\"\n\nfunc main() { q.G() }\n",
	}

	got := reachableSitesOf(t, files, []string{"example.com/w/dep.T"}, false)

	if len(got["dep.T"]) != 1 {
		t.Errorf("dep.T: got %v, want 1 site", got["dep.T"])
	}
}

func mainWith(body string) map[string]string {
	return map[string]string{
		"dep/dep.go":  "package dep\n\nfunc T() {}\n",
		"cmd/main.go": "package main\n\nimport (\n\t\"errors\"\n\t\"fmt\"\n\t\"example.com/w/dep\"\n)\n\nvar _ = errors.Is\nvar _ = fmt.Sprint\n\n" + body,
	}
}

func TestReachableSites_RootTakenAsValueStaysARoot(t *testing.T) {
	got := reachableSitesOf(t, mainWith("var keep func()\n\nfunc main() { keep = main; dep.T() }\n"), []string{"example.com/w/dep.T"}, false)

	if len(got["dep.T"]) != 1 {
		t.Errorf("dep.T: got %v, want 1 site", got["dep.T"])
	}
}

func TestReachableSites_TypeOnlyReachableThroughAFieldIsNotConverted(t *testing.T) {
	body := "type P struct{}\n\nfunc (*P) Close() { dep.T() }\n\ntype Holder struct{ P *P }\n\nfunc main() { fmt.Println(Holder{}) }\n"

	got := reachableSitesOf(t, mainWith(body), []string{"example.com/w/dep.T"}, false)

	if len(got["dep.T"]) != 0 {
		t.Errorf("dep.T: unexpected sites %v", got["dep.T"])
	}
}

func TestReachableSites_MethodOfUnnamedStdlibInterfaceStaysLive(t *testing.T) {
	body := "type E struct{}\n\nfunc (E) Error() string { return \"\" }\n\nfunc (E) Is(error) bool { dep.T(); return false }\n\nfunc main() { _ = errors.Is(E{}, nil) }\n"

	got := reachableSitesOf(t, mainWith(body), []string{"example.com/w/dep.T"}, false)

	if len(got["dep.T"]) != 1 {
		t.Errorf("dep.T: got %v, want 1 site", got["dep.T"])
	}
}

func TestReachableSites_TestFileInitDoesNotRootCodeWithoutIncludeTests(t *testing.T) {
	files := map[string]string{
		"dep/dep.go":  "package dep\n\nfunc T() {}\n",
		"p/p.go":      "package p\n\nimport \"example.com/w/dep\"\n\nfunc Helper() { dep.T() }\n",
		"p/p_test.go": "package p\n\nfunc init() { Helper() }\n",
		"cmd/main.go": "package main\n\nimport _ \"example.com/w/p\"\n\nfunc main() {}\n",
	}

	if got := reachableSitesOf(t, files, []string{"example.com/w/dep.T"}, false); len(got["dep.T"]) != 0 {
		t.Errorf("without tests: unexpected sites %v", got["dep.T"])
	}
	if got := reachableSitesOf(t, files, []string{"example.com/w/dep.T"}, true); len(got["dep.T"]) != 1 {
		t.Errorf("with tests: got %v, want 1 site", got["dep.T"])
	}
}

func TestReachableSites_GenericInstanceKeepsItsDeclarationInThePath(t *testing.T) {
	got := reachableSitesOf(t, mainWith("func G[T any](T) { dep.T() }\n\nfunc main() { G(1) }\n"), []string{"example.com/w/dep.T"}, false)

	if sites := got["dep.T"]; len(sites) != 1 || !slices.Equal(sites[0].Path, []string{"main.main", "main.G"}) {
		t.Errorf("dep.T: got %v, want one site on path main.main main.G", sites)
	}
}
