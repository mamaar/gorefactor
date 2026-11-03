package refactor

import (
	"io"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"testing"

	"github.com/mamaar/gorefactor/pkg/analysis"
	"github.com/mamaar/gorefactor/pkg/types"
)

const configsGoMod = "module example.com/threadtest\n\ngo 1.24\n"

// loadConfigsEngine loads files under an explicit linux/amd64 primary so the
// covering set does not depend on the host.
func loadConfigsEngine(t *testing.T, files map[string]string) (*DefaultEngine, string) {
	t.Helper()

	files["go.mod"] = configsGoMod
	dir := writeThreadContextWorkspace(t, files)
	cfg := analysis.NewBuildConfig("linux", "amd64", true)
	eng := CreateEngineWithConfig(&EngineConfig{SkipCompilation: true, AllowBreaking: true, BuildConfig: &cfg},
		slog.New(slog.NewTextHandler(io.Discard, nil))).(*DefaultEngine)

	_, err := eng.LoadWorkspace(dir)
	if err != nil {
		t.Fatal(err)
	}

	return eng, dir
}

// vetBuilds type-checks every package and its tests in dir, once per extra
// argument/environment set.
func vetBuilds(t *testing.T, dir string, builds ...[]string) {
	t.Helper()

	for _, build := range builds {
		args := []string{"vet"}

		env := append(os.Environ(), "CGO_ENABLED=0", "GOFLAGS=")

		for _, b := range build {
			if strings.Contains(b, "=") {
				env = append(env, b)
			} else {
				args = append(args, b)
			}
		}

		cmd := exec.Command("go", append(args, "./...")...)
		cmd.Dir = dir
		cmd.Env = env

		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Errorf("go vet %v failed: %v\n%s", build, err, out)
		}
	}
}

var (
	linuxBuild   = []string{"GOOS=linux", "GOARCH=amd64"}
	windowsBuild = []string{"GOOS=windows", "GOARCH=amd64"}
	linuxTagx    = []string{"GOOS=linux", "GOARCH=amd64", "-tags=tagx"}
)

func readFile(t *testing.T, dir, rel string) string {
	t.Helper()

	b, err := os.ReadFile(filepath.Join(dir, rel))
	if err != nil {
		t.Fatal(err)
	}

	return string(b)
}

func assertOnce(t *testing.T, dir, rel, want string) {
	t.Helper()

	got := readFile(t, dir, rel)
	if strings.Count(got, want) != 1 {
		t.Errorf("%s: want exactly one %q, got:\n%s", rel, want, got)
	}
}

func threadAcrossAndApply(t *testing.T, eng *DefaultEngine, file, fn string) *types.RefactoringPlan {
	t.Helper()

	plan, err := eng.ThreadContextAcrossConfigs(types.ThreadContextRequest{FunctionName: fn, SourceFile: file})
	if err != nil {
		t.Fatalf("ThreadContextAcrossConfigs: %v", err)
	}

	err = eng.ExecutePlan(plan)
	if err != nil {
		t.Fatalf("ExecutePlan: %v", err)
	}

	return plan
}

func sortedChanges(plan *types.RefactoringPlan) []types.Change {
	out := slices.Clone(plan.Changes)
	sort.Slice(out, func(i, j int) bool {
		a, b := out[i], out[j]
		if a.File != b.File {
			return a.File < b.File
		}

		if a.Start != b.Start {
			return a.Start < b.Start
		}

		return a.NewText < b.NewText
	})

	return out
}

func TestThreadContextAcrossConfigs_PlatformSiblingsGainTheParameterTogether(t *testing.T) {
	eng, dir := loadConfigsEngine(t, map[string]string{
		"p/open_linux.go":   "//go:build linux\n\npackage p\n\nfunc open(name string) error { return nil }\n",
		"p/open_windows.go": "//go:build windows\n\npackage p\n\nfunc open(name string) error { return leaf(name) }\n",
		"p/leaf.go":         "package p\n\nfunc leaf(name string) error { return nil }\n",
		"p/use.go":          "package p\n\nfunc Use(name string) error { return open(name) }\n",
	})

	plan := threadAcrossAndApply(t, eng, "p/leaf.go", "leaf")

	if len(plan.Coverage.Configs) != 2 {
		t.Errorf("Coverage.Configs = %v, want linux and windows", plan.Coverage.Configs)
	}

	assertOnce(t, dir, "p/open_linux.go", "func open(ctx context.Context, name string) error { return nil }")
	assertOnce(t, dir, "p/open_windows.go", "func open(ctx context.Context, name string) error { return leaf(ctx, name) }")
	assertOnce(t, dir, "p/use.go", "func Use(ctx context.Context, name string) error { return open(ctx, name) }")
	assertOnce(t, dir, "p/leaf.go", "func leaf(ctx context.Context, name string) error { return nil }")

	for _, f := range []string{"p/open_linux.go", "p/open_windows.go", "p/use.go", "p/leaf.go"} {
		assertOnce(t, dir, f, "ctx context.Context")
	}

	vetBuilds(t, dir, linuxBuild, windowsBuild)
}

func TestThreadContextAcrossConfigs_SeedReachedOnlyThroughTaggedVariant(t *testing.T) {
	eng, dir := loadConfigsEngine(t, map[string]string{
		"p/open_default.go": "//go:build !tagx\n\npackage p\n\nfunc open(name string) error { return nil }\n",
		"p/open_tagx.go":    "//go:build tagx\n\npackage p\n\nfunc open(name string) error { return leaf(name) }\n",
		"p/leaf.go":         "package p\n\nfunc leaf(name string) error { return nil }\n",
		"p/use.go":          "package p\n\nfunc Use(name string) error { return open(name) }\n",
	})

	threadAcrossAndApply(t, eng, "p/leaf.go", "leaf")

	assertOnce(t, dir, "p/open_default.go", "func open(ctx context.Context, name string) error { return nil }")
	assertOnce(t, dir, "p/open_tagx.go", "func open(ctx context.Context, name string) error { return leaf(ctx, name) }")
	assertOnce(t, dir, "p/use.go", "func Use(ctx context.Context, name string) error { return open(ctx, name) }")
	vetBuilds(t, dir, linuxBuild, linuxTagx)
}

func TestThreadContextAcrossConfigs_AlternatingSiblingChainNeedsSeveralRounds(t *testing.T) {
	eng, dir := loadConfigsEngine(t, map[string]string{
		"p/leaf.go":      "package p\n\nfunc leaf() {}\n",
		"p/f_tagx.go":    "//go:build tagx\n\npackage p\n\nfunc f() { leaf() }\n",
		"p/f_default.go": "//go:build !tagx\n\npackage p\n\nfunc f() {}\n",
		"p/g_default.go": "//go:build !tagx\n\npackage p\n\nfunc g() { f() }\n",
		"p/g_tagx.go":    "//go:build tagx\n\npackage p\n\nfunc g() {}\n",
		"p/h_tagx.go":    "//go:build tagx\n\npackage p\n\nfunc h() { g() }\n",
		"p/h_default.go": "//go:build !tagx\n\npackage p\n\nfunc h() {}\n",
		"p/top.go":       "package p\n\nfunc Top() { h() }\n",
	})

	views, err := eng.WorkspaceSet().Views()
	if err != nil {
		t.Fatal(err)
	}

	planner := &threadContextPlanner{engine: eng, req: types.ThreadContextRequest{FunctionName: "leaf", SourceFile: "p/leaf.go"}}

	plan, rounds, err := planAcrossViews(views, planner, maxConfigRounds)
	if err != nil {
		t.Fatal(err)
	}

	if rounds < 3 {
		t.Errorf("rounds = %d, want at least 3", rounds)
	}

	op := plan.Operations[0].(*ThreadContextOperation)
	if len(planner.indexes) != len(views) || op.CachedIndex == nil || op.CachedIndex != planner.indexes[views[0]] {
		t.Errorf("want one reference index per view, reused across rounds; have %d for %d views", len(planner.indexes), len(views))
	}

	err = eng.ExecutePlan(plan)
	if err != nil {
		t.Fatal(err)
	}

	for _, f := range []string{"f_tagx", "f_default", "g_default", "g_tagx", "h_tagx", "h_default", "top", "leaf"} {
		assertOnce(t, dir, "p/"+f+".go", "ctx context.Context")
	}

	assertOnce(t, dir, "p/top.go", "func Top(ctx context.Context) { h(ctx) }")
	vetBuilds(t, dir, linuxBuild, linuxTagx)
}

func TestThreadContextAcrossConfigs_SeedOnlyInSecondaryView(t *testing.T) {
	eng, dir := loadConfigsEngine(t, map[string]string{
		"p/p.go":               "package p\n\nfunc Plain() {}\n",
		"p/extra_tagx.go":      "//go:build tagx\n\npackage p\n\nfunc extra() {}\n",
		"p/extra_tagx_test.go": "//go:build tagx\n\npackage p\n\nimport \"testing\"\n\nfunc TestExtra(t *testing.T) { extra() }\n",
	})

	plan := threadAcrossAndApply(t, eng, "p/extra_tagx.go", "extra")

	got := changedFiles(plan)
	if !slices.Equal(got, []string{"extra_tagx.go", "extra_tagx_test.go"}) {
		t.Errorf("changed files = %v", got)
	}

	assertOnce(t, dir, "p/extra_tagx_test.go", "extra(t.Context())")
	vetBuilds(t, dir, linuxBuild, linuxTagx)
}

func TestThreadContextAcrossConfigs_SeedInNoViewReturnsThePrimaryError(t *testing.T) {
	eng, _ := loadConfigsEngine(t, map[string]string{
		"p/p.go":      "package p\n\nfunc Plain() {}\n",
		"p/p_tagx.go": "//go:build tagx\n\npackage p\n\nfunc tagged() {}\n",
	})
	for _, req := range []types.ThreadContextRequest{
		{FunctionName: "missing", SourceFile: "p/p.go"},
		{FunctionName: "missing", SourceFile: "p/nofile.go"},
	} {
		_, want := eng.ThreadContext(eng.WorkspaceSet().Primary().Workspace, req)
		if want == nil {
			t.Fatal("expected the primary view to reject the seed")
		}

		_, err := eng.ThreadContextAcrossConfigs(req)
		if err == nil || err.Error() != want.Error() {
			t.Errorf("err = %v, want %v", err, want)
		}
	}
}

func TestThreadContextAcrossConfigs_AbortInSecondaryViewNamesItsConfig(t *testing.T) {
	eng, dir := loadConfigsEngine(t, map[string]string{
		"p/leaf.go":     "package p\n\nfunc leaf() {}\n",
		"p/use.go":      "package p\n\nfunc Use() { leaf() }\n",
		"p/val_tagx.go": "//go:build tagx\n\npackage p\n\nvar h = leaf\n",
	})

	views, err := eng.WorkspaceSet().Views()
	if err != nil || len(views) != 2 {
		t.Fatalf("views = %d, err = %v", len(views), err)
	}

	_, err = eng.ThreadContextAcrossConfigs(types.ThreadContextRequest{FunctionName: "leaf", SourceFile: "p/leaf.go"})
	if err == nil {
		t.Fatal("expected the value use under tagx to abort")
	}

	if !strings.HasPrefix(err.Error(), "build configuration "+views[1].Config.String()+": ") {
		t.Errorf("err does not name the tagx configuration: %v", err)
	}

	if !strings.Contains(err.Error(), "used as a value") {
		t.Errorf("err = %v", err)
	}

	got := readFile(t, dir, "p/leaf.go")
	if strings.Contains(got, "context") {
		t.Errorf("leaf.go was modified:\n%s", got)
	}
}

func TestThreadContextAcrossConfigs_SingleConfigMatchesThreadContext(t *testing.T) {
	eng, _ := loadConfigsEngine(t, map[string]string{
		"p/leaf.go": "package p\n\nfunc leaf() {}\n\nfunc mid() { leaf() }\n",
		"p/use.go":  "package p\n\nfunc Use() { mid() }\n\nfunc main() { Use() }\n",
	})
	req := types.ThreadContextRequest{FunctionName: "leaf", SourceFile: "p/leaf.go"}

	want, err := eng.ThreadContext(eng.WorkspaceSet().Primary().Workspace, req)
	if err != nil {
		t.Fatal(err)
	}

	got, err := eng.ThreadContextAcrossConfigs(req)
	if err != nil {
		t.Fatal(err)
	}

	if !slices.Equal(sortedChanges(got), sortedChanges(want)) {
		t.Errorf("changes differ:\n got %+v\nwant %+v", sortedChanges(got), sortedChanges(want))
	}

	slices.Sort(got.AffectedFiles)
	slices.Sort(want.AffectedFiles)

	if !slices.Equal(got.AffectedFiles, want.AffectedFiles) {
		t.Errorf("affected files = %v, want %v", got.AffectedFiles, want.AffectedFiles)
	}

	if len(got.Coverage.Configs) != 1 {
		t.Errorf("Coverage.Configs = %v, want one entry", got.Coverage.Configs)
	}
}

func TestThreadContextAcrossConfigs_SingleConfigAbortMessageIsUnchanged(t *testing.T) {
	eng, _ := loadConfigsEngine(t, map[string]string{
		"p/leaf.go": "package p\n\nfunc leaf() {}\n\nvar h = leaf\n",
	})
	req := types.ThreadContextRequest{FunctionName: "leaf", SourceFile: "p/leaf.go"}

	_, want := eng.ThreadContext(eng.WorkspaceSet().Primary().Workspace, req)
	if want == nil {
		t.Fatal("expected a value-use abort")
	}

	_, err := eng.ThreadContextAcrossConfigs(req)
	if err == nil || err.Error() != want.Error() {
		t.Errorf("err = %q, want %q", err, want)
	}
}

func TestThreadContextAcrossConfigs_InterfaceReachesTagOnlyImplementation(t *testing.T) {
	eng, dir := loadConfigsEngine(t, map[string]string{
		"p/store.go":       "package p\n\ntype Store interface {\n\tSave(x string) error\n}\n\ntype MemStore struct{}\n\nfunc Use(s Store) error { return s.Save(\"a\") }\n\nfunc leaf(x string) error { return nil }\n",
		"p/mem_default.go": "//go:build !tagx\n\npackage p\n\nfunc (MemStore) Save(x string) error { return leaf(x) }\n",
		"p/mem_tagx.go":    "//go:build tagx\n\npackage p\n\nfunc (MemStore) Save(x string) error { return nil }\n",
		"p/tag_tagx.go":    "//go:build tagx\n\npackage p\n\ntype TagStore struct{}\n\nfunc (*TagStore) Save(x string) error { return nil }\n\nvar _ Store = (*TagStore)(nil)\n",
	})

	threadAcrossAndApply(t, eng, "p/store.go", "leaf")

	assertOnce(t, dir, "p/tag_tagx.go", "func (*TagStore) Save(ctx context.Context, x string) error")
	assertOnce(t, dir, "p/mem_tagx.go", "func (MemStore) Save(ctx context.Context, x string) error")
	assertOnce(t, dir, "p/mem_default.go", "func (MemStore) Save(ctx context.Context, x string) error { return leaf(ctx, x) }")
	assertOnce(t, dir, "p/store.go", "Save(ctx context.Context, x string) error")
	assertOnce(t, dir, "p/store.go", "func Use(ctx context.Context, s Store) error { return s.Save(ctx, \"a\") }")
	vetBuilds(t, dir, linuxBuild, linuxTagx)
}

func TestThreadContextAcrossConfigs_RewritesCallSiteInTagOnlyTestFile(t *testing.T) {
	eng, dir := loadConfigsEngine(t, map[string]string{
		"p/seed.go":        "package p\n\nfunc Seed() {}\n",
		"p/x_tagx_test.go": "//go:build tagx\n\npackage p\n\nimport \"testing\"\n\nfunc TestSeed(t *testing.T) { Seed() }\n",
	})

	threadAcrossAndApply(t, eng, "p/seed.go", "Seed")

	assertOnce(t, dir, "p/seed.go", "func Seed(ctx context.Context) {}")
	assertOnce(t, dir, "p/x_tagx_test.go", "Seed(t.Context())")
	vetBuilds(t, dir, linuxBuild, linuxTagx)
}
