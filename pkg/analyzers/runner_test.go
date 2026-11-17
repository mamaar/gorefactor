package analyzers_test

import (
	"go/parser"
	"go/token"
	gotypes "go/types"
	"slices"
	"testing"

	"golang.org/x/tools/go/analysis"

	"github.com/mamaar/gorefactor/pkg/analyzers"
	"github.com/mamaar/gorefactor/pkg/analyzers/errorwrap"
	"github.com/mamaar/gorefactor/pkg/types"
)

const bareReturn = `
func foo() error {
	err := bar()
	if err != nil {
		return err
	}
	return nil
}

func bar() error { return nil }
`

func TestRun_IncludesInPackageAndExternalTestFiles(t *testing.T) {
	fset := token.NewFileSet()
	pkg := &types.Package{
		Name:      "lib",
		Path:      "test/lib",
		Files:     map[string]*types.File{},
		TestFiles: map[string]*types.File{},
	}
	add := func(files map[string]*types.File, name, src string) {
		astFile, err := parser.ParseFile(fset, name, src, parser.ParseComments)
		if err != nil {
			t.Fatal(err)
		}

		files[name] = &types.File{Path: name, AST: astFile, OriginalContent: []byte(src), Package: pkg}
	}
	add(pkg.Files, "lib.go", "package lib\n"+bareReturn)
	add(pkg.TestFiles, "lib_test.go", "package lib\n"+bareReturn)
	add(pkg.TestFiles, "lib_ext_test.go", "package lib_test\n"+bareReturn)
	ws := &types.Workspace{Packages: map[string]*types.Package{"test/lib": pkg}, FileSet: fset}

	rr, err := analyzers.Run(ws, errorwrap.Analyzer, "")
	if err != nil {
		t.Fatal(err)
	}

	var files []string
	for _, r := range rr.Result.([]*errorwrap.Result) {
		files = append(files, r.File)
	}

	slices.Sort(files)

	want := []string{"lib.go", "lib_ext_test.go", "lib_test.go"}
	if !slices.Equal(files, want) {
		t.Fatalf("findings in %v, want %v", files, want)
	}
}

func TestRun_ExternalTestPackageWithoutTypeInfoIsListedAsSkipped(t *testing.T) {
	fset := token.NewFileSet()
	src := "package lib_test\n" + bareReturn

	astFile, err := parser.ParseFile(fset, "lib_ext_test.go", src, parser.ParseComments)
	if err != nil {
		t.Fatal(err)
	}

	pkg := &types.Package{
		Name:                 "lib",
		Path:                 "test/lib",
		ImportPath:           "test/lib",
		Files:                map[string]*types.File{},
		TestFiles:            map[string]*types.File{"lib_ext_test.go": {Path: "lib_ext_test.go", AST: astFile, OriginalContent: []byte(src)}},
		ExternalTestTypesPkg: gotypes.NewPackage("test/lib", "lib_test"),
	}
	ws := &types.Workspace{Packages: map[string]*types.Package{"test/lib": pkg}, FileSet: fset}

	rr, err := analyzers.Run(ws, errorwrap.Analyzer, "")
	if err != nil {
		t.Fatal(err)
	}

	if !slices.Equal(rr.Skipped, []string{"test/lib_test"}) {
		t.Fatalf("skipped = %v, want [test/lib_test]", rr.Skipped)
	}
}

func excludedTestFilePackage(t *testing.T) *types.Workspace {
	t.Helper()

	fset := token.NewFileSet()
	pkg := &types.Package{
		Name:              "lib",
		Path:              "test/lib",
		ImportPath:        "test/lib",
		Files:             map[string]*types.File{},
		TestFiles:         map[string]*types.File{},
		ExcludedTestFiles: map[string]error{"bad_test.go": gotypes.Error{Msg: "undefined: x"}},
	}

	for _, name := range []string{"good_test.go", "bad_test.go"} {
		astFile, err := parser.ParseFile(fset, name, "package lib\n", 0)
		if err != nil {
			t.Fatal(err)
		}

		pkg.TestFiles[name] = &types.File{Path: name, AST: astFile, Package: pkg}
	}

	return &types.Workspace{Packages: map[string]*types.Package{"test/lib": pkg}, FileSet: fset}
}

func fileCounter(despiteErrors bool) (*analysis.Analyzer, *int) {
	var seen int

	return &analysis.Analyzer{
		Name:             "filecount",
		Doc:              "counts files",
		RunDespiteErrors: despiteErrors,
		Run: func(pass *analysis.Pass) (any, error) {
			seen = len(pass.Files)
			return nil, nil
		},
	}, &seen
}

func TestRun_RunDespiteErrorsAnalyzerStillSeesExcludedTestFiles(t *testing.T) {
	a, seen := fileCounter(true)

	rr, err := analyzers.Run(excludedTestFilePackage(t), a, "")
	if err != nil {
		t.Fatal(err)
	}

	if *seen != 2 || len(rr.Skipped) != 0 {
		t.Fatalf("saw %d files, skipped %v; want both files and nothing skipped", *seen, rr.Skipped)
	}
}

func TestRun_ExcludedTestFileIsListedAsSkippedForStrictAnalyzers(t *testing.T) {
	a, seen := fileCounter(false)

	rr, err := analyzers.Run(excludedTestFilePackage(t), a, "")
	if err != nil {
		t.Fatal(err)
	}

	if *seen != 1 || !slices.Equal(rr.Skipped, []string{"test/lib (bad_test.go)"}) {
		t.Fatalf("saw %d files, skipped %v; want 1 file and [test/lib (bad_test.go)]", *seen, rr.Skipped)
	}
}
