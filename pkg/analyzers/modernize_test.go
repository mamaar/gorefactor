package analyzers_test

import (
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"golang.org/x/tools/go/analysis"
	"golang.org/x/tools/go/analysis/passes/modernize"

	"github.com/mamaar/gorefactor/pkg/analyzers"
	"github.com/mamaar/gorefactor/pkg/analyzers/boolsimplify"
	"github.com/mamaar/gorefactor/pkg/refactor"
	wstypes "github.com/mamaar/gorefactor/pkg/types"
)

// runModernize writes files plus a go.mod with the given go directive, type-checks
// the workspace and runs a over it.
func runModernize(t *testing.T, goDirective string, files map[string]string, a *analysis.Analyzer) []analysis.Diagnostic {
	t.Helper()

	rr, _ := runModernizeIn(t, goDirective, files, a, "")

	return rr.Diagnostics
}

// runModernizeIn is runModernize with a package filter, returning the whole result and the workspace.
func runModernizeIn(t *testing.T, goDirective string, files map[string]string, a *analysis.Analyzer, filter string) (*analyzers.RunResult, *wstypes.Workspace) {
	t.Helper()

	dir := t.TempDir()
	files["go.mod"] = "module example.com/m\n\n" + goDirective + "\n"

	for rel, content := range files {
		full := filepath.Join(dir, rel)

		err := os.MkdirAll(filepath.Dir(full), 0o755)
		if err != nil {
			t.Fatal(err)
		}

		err = os.WriteFile(full, []byte(content), 0o644)
		if err != nil {
			t.Fatal(err)
		}
	}

	engine := refactor.CreateEngineWithConfig(&refactor.EngineConfig{SkipCompilation: true}, slog.New(slog.NewTextHandler(io.Discard, nil)))

	ws, err := engine.LoadWorkspace(dir)
	if err != nil {
		t.Fatal(err)
	}

	engine.(*refactor.DefaultEngine).EnsureTypeCheckedPackage(ws, filter)

	rr, err := analyzers.Run(ws, a, filter)
	if err != nil {
		t.Fatal(err)
	}

	return rr, ws
}

const threeClauseLoop = `package p

func f(n int) {
	for i := 0; i < n; i++ {
		_ = i
	}
}
`

func TestRun_RangeIntHonoursModuleGoVersion(t *testing.T) {
	got := runModernize(t, "go 1.22", map[string]string{"p.go": threeClauseLoop}, modernize.RangeIntAnalyzer)
	if len(got) != 1 {
		t.Fatalf("go 1.22: got %d diagnostics, want 1", len(got))
	}

	got = runModernize(t, "go 1.21", map[string]string{"p.go": threeClauseLoop}, modernize.RangeIntAnalyzer)
	if len(got) != 0 {
		t.Fatalf("go 1.21: got %d diagnostics, want 0", len(got))
	}
}

func TestRun_NewExprFactCrossesPackages(t *testing.T) {
	got := runModernize(t, "go 1.26", map[string]string{
		"zlib/a.go": "package zlib\n\nfunc NewInt(x int) *int {\n\treturn &x\n}\n",
		"app/b.go":  "package app\n\nimport \"example.com/m/zlib\"\n\nvar P = zlib.NewInt(3)\n",
	}, modernize.NewExprAnalyzer)

	var calls int

	for _, d := range got {
		if strings.Contains(d.Message, "simplified to new(x)") {
			calls++
		}
	}

	if calls != 1 {
		t.Fatalf("got %d call diagnostics, want 1: %v", calls, got)
	}
}

func TestRun_GoVersionComesFromNearestGoMod(t *testing.T) {
	got := runModernize(t, "go 1.22", map[string]string{
		"p/p.go":     threeClauseLoop,
		"sub/go.mod": "module example.com/sub\n\ngo 1.21\n",
		"sub/s/s.go": strings.Replace(threeClauseLoop, "package p", "package s", 1),
	}, modernize.RangeIntAnalyzer)
	if len(got) != 1 {
		t.Fatalf("got %d diagnostics, want 1 (the nested go 1.21 module is out of range)", len(got))
	}
}

func TestRun_MissingGoDirectiveMeansGo116(t *testing.T) {
	got := runModernize(t, "", map[string]string{"p.go": threeClauseLoop}, modernize.RangeIntAnalyzer)
	if len(got) != 0 {
		t.Fatalf("got %d diagnostics, want 0", len(got))
	}
}

func TestRun_GoVersionNewerThanToolchainIsCapped(t *testing.T) {
	rr, _ := runModernizeIn(t, "go 1.99", map[string]string{"p.go": threeClauseLoop}, modernize.RangeIntAnalyzer, "")
	if len(rr.Diagnostics) != 1 || len(rr.Skipped) != 0 {
		t.Fatalf("got %d diagnostics and skipped %v, want 1 and none", len(rr.Diagnostics), rr.Skipped)
	}
}

func TestDiagnosticsToChanges_IgnoresLineDirectives(t *testing.T) {
	src := "package p\n\n//line foo.y:10\n" + strings.TrimPrefix(threeClauseLoop, "package p\n\n")

	rr, ws := runModernizeIn(t, "go 1.22", map[string]string{"p.go": src}, modernize.RangeIntAnalyzer, "")

	changes := analyzers.FillOldText(ws, analyzers.DiagnosticsToChanges(ws.FileSet, rr.Diagnostics))
	if len(changes) == 0 {
		t.Fatal("no changes")
	}

	for _, c := range changes {
		if filepath.Base(c.File) != "p.go" {
			t.Errorf("change addressed to %s, want p.go", c.File)
		}
	}

	if changes[0].OldText != "0; i < " {
		t.Errorf("OldText = %q, want the loop clause", changes[0].OldText)
	}
}

func TestRun_NewExprFactsWithPackageFilter(t *testing.T) {
	files := map[string]string{
		"zlib/a.go": "package zlib\n\nfunc NewInt(x int) *int {\n\treturn &x\n}\n",
		"app/b.go":  "package app\n\nimport \"example.com/m/zlib\"\n\nvar P = zlib.NewInt(3)\n",
	}

	rr, _ := runModernizeIn(t, "go 1.26", files, modernize.NewExprAnalyzer, "app")

	var calls int

	for _, d := range rr.Diagnostics {
		if strings.Contains(d.Message, "simplified to new(x)") {
			calls++
		}
	}

	if calls != 1 {
		t.Fatalf("got %d call diagnostics, want 1: %v", calls, rr.Diagnostics)
	}
}

func TestRun_NewExprFactsAcrossTestImportCycle(t *testing.T) {
	got := runModernize(t, "go 1.26", map[string]string{
		"zlib/a.go":      "package zlib\n\nfunc NewInt(x int) *int {\n\treturn &x\n}\n",
		"zlib/a_test.go": "package zlib\n\nimport _ \"example.com/m/app\"\n",
		"app/b.go":       "package app\n\nimport \"example.com/m/zlib\"\n\nvar P = zlib.NewInt(3)\n",
	}, modernize.NewExprAnalyzer)

	var calls int

	for _, d := range got {
		if strings.Contains(d.Message, "simplified to new(x)") {
			calls++
		}
	}

	if calls != 1 {
		t.Fatalf("got %d call diagnostics, want 1: %v", calls, got)
	}
}

func TestRun_PanicIsReturnedNamingAnalyzerAndPackage(t *testing.T) {
	boom := &analysis.Analyzer{Name: "boom", Doc: "panics", Run: func(*analysis.Pass) (any, error) { panic("kaboom") }}

	dir := t.TempDir()
	writeFiles(t, dir, map[string]string{"go.mod": "module example.com/m\n\ngo 1.22\n", "p/p.go": "package p\n"})

	engine := refactor.CreateEngineWithConfig(&refactor.EngineConfig{SkipCompilation: true}, slog.New(slog.NewTextHandler(io.Discard, nil)))

	ws, err := engine.LoadWorkspace(dir)
	if err != nil {
		t.Fatal(err)
	}

	_, err = analyzers.Run(ws, boom, "")
	if err == nil || !strings.Contains(err.Error(), "boom") || !strings.Contains(err.Error(), "example.com/m/p") || !strings.Contains(err.Error(), "kaboom") {
		t.Fatalf("err = %v, want it to name analyzer boom, package example.com/m/p and the panic value", err)
	}
}

const brokenSuite = `package p

import (
	"sort"
	"strings"
	"sync"
)

func f(xs []string, m map[string]int, wg *sync.WaitGroup, s string) {
	for i := 0; i < undefinedN; i++ {
		_ = i
	}

	for k, v := range m {
		other[k] = v
	}

	sort.Slice(xs, func(i, j int) bool { return xs[i] < missing })

	i := strings.Index(s, ":")
	if i >= 0 {
		_ = s[:i]
	}

	wg.Add(1)

	go func() { defer wg.Done(); zz() }()
}
`

func TestRun_SuiteSkipsPackagesWithTypeErrors(t *testing.T) {
	for _, a := range modernize.Suite {
		rr, _ := runModernizeIn(t, "go 1.26", map[string]string{"p/p.go": brokenSuite}, a, "")
		if len(rr.Diagnostics) != 0 || len(rr.Skipped) != 1 || rr.Skipped[0] != "example.com/m/p" {
			t.Errorf("%s: got %d diagnostics and skipped %v, want none and [example.com/m/p]", a.Name, len(rr.Diagnostics), rr.Skipped)
		}
	}
}

// passRecorder records what each pass sees.
type passRecorder struct {
	passes []recordedPass
}

type recordedPass struct {
	pkg       string
	files     []string
	scopes    int
	versions  int
	instances bool
}

func (r *passRecorder) analyzer() *analysis.Analyzer {
	return &analysis.Analyzer{Name: "recorder", Doc: "records passes", Run: func(pass *analysis.Pass) (any, error) {
		rec := recordedPass{pkg: pass.Pkg.Name(), scopes: len(pass.TypesInfo.Scopes), versions: len(pass.TypesInfo.FileVersions), instances: pass.TypesInfo.Instances != nil}
		for _, f := range pass.Files {
			rec.files = append(rec.files, filepath.Base(pass.Fset.Position(f.Pos()).Filename))
		}

		r.passes = append(r.passes, rec)

		return nil, nil
	}}
}

func TestRun_ExternalTestPackageGetsItsOwnPass(t *testing.T) {
	rec := &passRecorder{}

	runModernize(t, "go 1.22", map[string]string{
		"p/p.go":        "package p\n",
		"p/in_test.go":  "package p\n\nfunc a() {}\n",
		"p/ext_test.go": "package p_test\n\nfunc b() {}\n",
	}, rec.analyzer())

	if len(rec.passes) != 2 {
		t.Fatalf("got %d passes, want 2: %+v", len(rec.passes), rec.passes)
	}

	own, ext := rec.passes[0], rec.passes[1]
	if own.pkg != "p" || !slices.Equal(own.files, []string{"in_test.go", "p.go"}) && !slices.Equal(own.files, []string{"p.go", "in_test.go"}) {
		t.Errorf("own pass = %+v, want package p with p.go and in_test.go", own)
	}

	if ext.pkg != "p_test" || !slices.Equal(ext.files, []string{"ext_test.go"}) {
		t.Errorf("external pass = %+v, want package p_test with ext_test.go only", ext)
	}

	if ext.scopes == 0 || ext.versions == 0 || !ext.instances {
		t.Errorf("external pass lacks full type info: %+v", ext)
	}
}

func TestRun_TestFilesDroppedFromTypeCheckingAreNotAnalyzed(t *testing.T) {
	rec := &passRecorder{}

	rr, _ := runModernizeIn(t, "go 1.22", map[string]string{
		"p/p.go":        "package p\n",
		"p/bad_test.go": "package p\n\nvar _ = undefined\n",
	}, rec.analyzer(), "")

	if len(rr.Skipped) != 0 || len(rec.passes) != 1 || !slices.Equal(rec.passes[0].files, []string{"p.go"}) {
		t.Fatalf("passes = %+v, skipped = %v, want one pass over p.go", rec.passes, rr.Skipped)
	}
}

const rangeIntTest = "\nfunc %s(n int) {\n\tfor i := 0; i < n; i++ {\n\t\t_ = i\n\t}\n}\n"

func TestRun_ExternalTestFilesAreModernized(t *testing.T) {
	got := runModernize(t, "go 1.22", map[string]string{
		"p/p.go":        "package p\n",
		"p/in_test.go":  "package p\n" + fmt.Sprintf(rangeIntTest, "a"),
		"p/ext_test.go": "package p_test\n" + fmt.Sprintf(rangeIntTest, "b"),
	}, modernize.RangeIntAnalyzer)
	if len(got) != 2 {
		t.Fatalf("got %d diagnostics, want 2 (in-package and external test)", len(got))
	}
}

const minPattern = `
func pick(a, b int) int {
	var x int
	if a < b {
		x = a
	} else {
		x = b
	}

	return x
}
`

func TestRun_MinMaxRespectsExternalTestPackageShadowingMin(t *testing.T) {
	got := runModernize(t, "go 1.22", map[string]string{
		"p/p.go":        "package p\n" + minPattern,
		"p/ext_test.go": "package p_test\n\nfunc min(a, b int) int { return a }\n" + minPattern,
	}, modernize.MinMaxAnalyzer)
	if len(got) != 1 {
		t.Fatalf("got %d diagnostics, want 1 (only p.go; p_test declares its own min): %v", len(got), got)
	}
}

func writeFiles(t *testing.T, dir string, files map[string]string) {
	t.Helper()

	for rel, content := range files {
		full := filepath.Join(dir, rel)

		err := os.MkdirAll(filepath.Dir(full), 0o755)
		if err != nil {
			t.Fatal(err)
		}

		err = os.WriteFile(full, []byte(content), 0o644)
		if err != nil {
			t.Fatal(err)
		}
	}
}

func TestRun_DetectorsRunOnPackagesWithTypeErrors(t *testing.T) {
	src := "package p\n\nfunc F(a, b int) int {\n\tif !(a == b) {\n\t\treturn 1\n\t}\n\treturn 0\n}\n\nvar _ = undefinedThing\n"
	rr, _ := runModernizeIn(t, "go 1.22", map[string]string{"p/p.go": src}, boolsimplify.Analyzer, "")

	if len(rr.Diagnostics) != 1 || len(rr.Skipped) != 0 {
		t.Fatalf("got %d diagnostics and skipped %v, want 1 and none", len(rr.Diagnostics), rr.Skipped)
	}
}

func TestRun_GoVersionOfWorkspaceLoadedBelowItsModuleRoot(t *testing.T) {
	dir := t.TempDir()
	writeFiles(t, dir, map[string]string{
		"go.mod":     "module example.com/m\n\ngo 1.21\n",
		"sub/s/s.go": strings.Replace(threeClauseLoop, "package p", "package s", 1),
	})

	engine := refactor.CreateEngineWithConfig(&refactor.EngineConfig{SkipCompilation: true}, slog.New(slog.NewTextHandler(io.Discard, nil)))

	ws, err := engine.LoadWorkspace(filepath.Join(dir, "sub"))
	if err != nil {
		t.Fatal(err)
	}

	engine.(*refactor.DefaultEngine).EnsureTypeCheckedPackage(ws, "")

	rr, err := analyzers.Run(ws, modernize.RangeIntAnalyzer, "")
	if err != nil {
		t.Fatal(err)
	}

	if len(rr.Diagnostics) != 0 {
		t.Fatalf("got %d diagnostics, want 0 under the go 1.21 of the parent go.mod", len(rr.Diagnostics))
	}
}
