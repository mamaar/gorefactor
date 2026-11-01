package analyzers_test

import (
	"go/parser"
	"go/token"
	"slices"
	"testing"

	"github.com/mamaar/gorefactor/pkg/analyzers"
	"github.com/mamaar/gorefactor/pkg/analyzers/errorwrap"
	"github.com/mamaar/gorefactor/pkg/types"
)

const bareReturn = `
func foo() error {
	err := bar()
	return err
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
