package refactor

import (
	"fmt"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/mamaar/gorefactor/pkg/analysis"
	"github.com/mamaar/gorefactor/pkg/types"
)

const (
	seedSrc       = "package p\n\nfunc Seed() {}\n"
	seedTestSrc   = "package p\n\nimport \"testing\"\n\nfunc TestSeed(t *testing.T) {\n\tSeed()\n}\n"
	missingImport = "import _ \"example.com/missing/dep\"\n"
)

var (
	linuxName = analysis.NewBuildConfig("linux", "amd64", true).String()
	tagxName  = analysis.NewBuildConfig("linux", "amd64", true, "tagx").String()
)

func planSeed(t *testing.T, eng *DefaultEngine) *types.RefactoringPlan {
	t.Helper()

	plan, err := eng.ThreadContextAcrossConfigs(types.ThreadContextRequest{FunctionName: "Seed", SourceFile: "p/p.go"})
	if err != nil {
		t.Fatalf("ThreadContextAcrossConfigs: %v", err)
	}

	return plan
}

func changesIn(plan *types.RefactoringPlan, path string) []string {
	var out []string

	for _, c := range sortedChanges(plan) {
		if c.File == path {
			out = append(out, c.NewText)
		}
	}

	return out
}

// uncoveredPaths returns the uncovered files relative to dir.
func uncoveredPaths(t *testing.T, plan *types.RefactoringPlan, dir string) []string {
	t.Helper()
	var out []string

	for _, u := range plan.Coverage.Uncovered {
		rel, err := filepath.Rel(dir, u.Path)
		if err != nil {
			t.Fatal(err)
		}

		out = append(out, filepath.ToSlash(rel))
	}

	return out
}

// chainTestFiles adds test files to dir whose errors cannot be shed within the
// type-checker's pass limit, each starting with header.
func chainTestFiles(files map[string]string, dir, header string) {
	files[dir+"/t0_test.go"] = header + "package p\n\n" + missingImport + "\nfunc h0() {}\n"
	for i := 1; i <= 4; i++ {
		files[fmt.Sprintf("%s/t%d_test.go", dir, i)] = fmt.Sprintf("%spackage p\n\nfunc h%d() { h%d() }\n", header, i, i-1)
	}
}

func TestThreadContextAcrossConfigs_ExcludedTestFileMentioningSeedIsReportedAndSkipped(t *testing.T) {
	eng, dir := loadConfigsEngine(t, map[string]string{
		"p/p.go":        seedSrc,
		"p/p_test.go":   seedTestSrc,
		"p/bad_test.go": "package p\n\n" + missingImport + "\nfunc alsoCalls() {\n\tSeed()\n}\n",
	})
	plan := planSeed(t, eng)

	got := changesIn(plan, filepath.Join(dir, "p/p_test.go"))
	if !reflect.DeepEqual(got, []string{"t.Context()"}) {
		t.Errorf("changes in p_test.go = %q, want the ctx argument", got)
	}

	bad := filepath.Join(dir, "p/bad_test.go")

	if len(plan.Coverage.Uncovered) != 1 {
		t.Fatalf("Coverage.Uncovered = %+v, want bad_test.go", plan.Coverage.Uncovered)
	}

	u := plan.Coverage.Uncovered[0]
	if u.Path != bad || !reflect.DeepEqual(u.Configs, []string{linuxName}) || !strings.Contains(u.Reason, "could not import example.com/missing/dep") {
		t.Errorf("uncovered = %+v", u)
	}

	if len(plan.SkippedSites) != 1 {
		t.Fatalf("SkippedSites = %+v, want one in bad_test.go", plan.SkippedSites)
	}

	s := plan.SkippedSites[0]
	if s.File != bad || s.Line != 6 || s.Snippet != "Seed()" {
		t.Errorf("skipped site = %+v, want bad_test.go:6 Seed()", s)
	}

	for _, want := range []string{"Seed", linuxName, "could not import"} {
		if !strings.Contains(s.Reason, want) {
			t.Errorf("skipped site reason %q does not mention %q", s.Reason, want)
		}
	}

	if len(changesIn(plan, bad)) != 0 {
		t.Errorf("bad_test.go has changes: %q", changesIn(plan, bad))
	}
}

func TestThreadContextAcrossConfigs_ExcludedTestFileNotMentioningSeedIsOnlyListed(t *testing.T) {
	eng, dir := loadConfigsEngine(t, map[string]string{
		"p/p.go":        seedSrc,
		"p/p_test.go":   seedTestSrc,
		"p/bad_test.go": "package p\n\n" + missingImport + "\n// Seed is only named in this comment.\nfunc unrelated() {}\n",
	})
	plan := planSeed(t, eng)

	got := uncoveredPaths(t, plan, dir)
	if !reflect.DeepEqual(got, []string{"p/bad_test.go"}) {
		t.Errorf("uncovered = %v, want p/bad_test.go", got)
	}

	if len(plan.SkippedSites) != 0 {
		t.Errorf("SkippedSites = %+v, want none", plan.SkippedSites)
	}
}

func TestThreadContextAcrossConfigs_FileExcludedInOneViewButCheckedInAnotherIsCovered(t *testing.T) {
	eng, dir := loadConfigsEngine(t, map[string]string{
		"p/p.go":             seedSrc,
		"p/tagged_test.go":   "//go:build tagx\n\npackage p\n\n" + missingImport + "\nfunc taggedCall() { Seed() }\n",
		"p/h_notag_test.go":  "//go:build !tagx\n\npackage p\n\nfunc helperOnlyUntagged() {}\n",
		"p/shared_test.go":   "package p\n\nimport \"testing\"\n\nfunc TestShared(t *testing.T) {\n\thelperOnlyUntagged()\n\tSeed()\n}\n",
		"p/other_tagx_ok.go": "//go:build tagx\n\npackage p\n\nfunc onlyTagx() {}\n",
	})
	plan := planSeed(t, eng)

	if !reflect.DeepEqual(plan.Coverage.Configs, []string{linuxName, tagxName}) {
		t.Fatalf("Coverage.Configs = %v", plan.Coverage.Configs)
	}

	got := changesIn(plan, filepath.Join(dir, "p/shared_test.go"))
	if !reflect.DeepEqual(got, []string{"t.Context()"}) {
		t.Errorf("changes in shared_test.go = %q, want the ctx argument from the primary view", got)
	}

	tagged := filepath.Join(dir, "p/tagged_test.go")

	if len(plan.Coverage.Uncovered) != 1 {
		t.Fatalf("Coverage.Uncovered = %+v, want only tagged_test.go", plan.Coverage.Uncovered)
	}

	u := plan.Coverage.Uncovered[0]
	if u.Path != tagged || !reflect.DeepEqual(u.Configs, []string{tagxName}) {
		t.Errorf("uncovered = %+v, want tagged_test.go under %s", u, tagxName)
	}

	if len(plan.SkippedSites) != 1 || plan.SkippedSites[0].File != tagged {
		t.Errorf("SkippedSites = %+v, want one in tagged_test.go", plan.SkippedSites)
	}
}

func TestThreadContextAcrossConfigs_UnattributedTestsInReachedPackageAbortNamingPackageAndConfig(t *testing.T) {
	files := map[string]string{"p/p.go": seedSrc, "p/p_test.go": seedTestSrc}
	chainTestFiles(files, "p", "//go:build tagx\n\n")
	eng, _ := loadConfigsEngine(t, files)

	_, err := eng.ThreadContextAcrossConfigs(types.ThreadContextRequest{FunctionName: "Seed", SourceFile: "p/p.go"})
	if err == nil {
		t.Fatal("expected an abort")
	}

	for _, want := range []string{"build configuration " + tagxName, "example.com/threadtest/p", "undefined: h2"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("err does not contain %q: %v", want, err)
		}
	}
}

func TestThreadContextAcrossConfigs_UnattributedTestsInUnreachedPackageDoNotAbort(t *testing.T) {
	files := map[string]string{"p/p.go": seedSrc, "p/p_test.go": seedTestSrc, "q/q.go": "package p\n\nfunc Other() {}\n"}
	chainTestFiles(files, "q", "")
	eng, dir := loadConfigsEngine(t, files)
	plan := planSeed(t, eng)

	got := changesIn(plan, filepath.Join(dir, "p/p_test.go"))
	if len(got) != 1 {
		t.Errorf("changes in p_test.go = %q", got)
	}

	if len(plan.Coverage.Uncovered) != 0 {
		t.Errorf("Coverage.Uncovered = %v, want none", uncoveredPaths(t, plan, dir))
	}
}

func TestThreadContextAcrossConfigs_UncoveredFilesAreReportedOnlyForReachedPackages(t *testing.T) {
	eng, dir := loadConfigsEngine(t, map[string]string{
		"p/p.go":           seedSrc,
		"p/bad_test.go":    "package p\n\n" + missingImport,
		"q/q.go":           "package q\n\nfunc Other() {}\n",
		"q/bad_test.go":    "package q\n\n" + missingImport,
		"q/ignored.go":     "//go:build ignore\n\npackage main\n",
		"caller/c.go":      "package caller\n\nimport (\n\t\"context\"\n\n\t\"example.com/threadtest/p\"\n)\n\nfunc Call(ctx context.Context) { p.Seed() }\n",
		"caller/b_test.go": "package caller\n\n" + missingImport,
	})
	plan := planSeed(t, eng)

	got, want := uncoveredPaths(t, plan, dir), []string{"caller/b_test.go", "p/bad_test.go"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("uncovered = %v, want %v", got, want)
	}
}

func TestThreadContextAcrossConfigs_IgnoredFileInReachedDirMentioningSeedIsReportedAndSkipped(t *testing.T) {
	eng, dir := loadConfigsEngine(t, map[string]string{
		"p/p.go":   seedSrc,
		"p/gen.go": "//go:build ignore\n\npackage main\n\nfunc main() { Seed() }\n",
	})
	plan := planSeed(t, eng)

	gen := filepath.Join(dir, "p/gen.go")

	if len(plan.Coverage.Uncovered) != 1 {
		t.Fatalf("Coverage.Uncovered = %+v, want gen.go", plan.Coverage.Uncovered)
	}

	u := plan.Coverage.Uncovered[0]
	if u.Path != gen || len(u.Configs) != 0 || u.Reason != "//go:build ignore" {
		t.Errorf("uncovered = %+v", u)
	}

	if len(plan.SkippedSites) != 1 || plan.SkippedSites[0].File != gen || plan.SkippedSites[0].Line != 5 {
		t.Errorf("SkippedSites = %+v, want gen.go:5", plan.SkippedSites)
	}
}

func TestThreadContext_UnattributedTestsInReachedPackageAbortInASingleView(t *testing.T) {
	files := map[string]string{"p/p.go": seedSrc, "p/p_test.go": seedTestSrc}
	chainTestFiles(files, "p", "")
	eng, _ := loadConfigsEngine(t, files)

	_, err := eng.ThreadContext(eng.WorkspaceSet().Primary().Workspace,
		types.ThreadContextRequest{FunctionName: "Seed", SourceFile: "p/p.go"})
	if err == nil || !strings.Contains(err.Error(), "example.com/threadtest/p") {
		t.Fatalf("err = %v, want an abort naming the package", err)
	}

	if strings.Contains(err.Error(), "build configuration") {
		t.Errorf("single-view abort names a configuration: %v", err)
	}
}
