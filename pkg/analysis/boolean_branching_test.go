package analysis

import (
	"go/parser"
	"go/token"
	"testing"

	"github.com/mamaar/gorefactor/pkg/types"
)

func createBoolBranchTestWorkspace(t *testing.T, src string) *types.Workspace {
	t.Helper()
	fileSet := token.NewFileSet()

	astFile, err := parser.ParseFile(fileSet, "testpkg.go", src, parser.ParseComments)
	if err != nil {
		t.Fatalf("Failed to parse test source: %v", err)
	}

	file := &types.File{
		Path:            "testpkg.go",
		AST:             astFile,
		OriginalContent: []byte(src),
	}

	pkg := &types.Package{
		Name:  "testpkg",
		Path:  "test/testpkg",
		Files: map[string]*types.File{"testpkg.go": file},
	}
	file.Package = pkg

	return &types.Workspace{
		Packages: map[string]*types.Package{"test/testpkg": pkg},
		FileSet:  fileSet,
	}
}

func TestBooleanBranching_BasicViolation(t *testing.T) {
	src := `package testpkg

func handle(accept string) {
	wantShapefile := accept == "x-shapefile"
	wantGeoJSON := accept == "geojson"

	if wantShapefile {
		doShapefile()
	} else if wantGeoJSON {
		doGeoJSON()
	}
}

func doShapefile() {}
func doGeoJSON() {}
`
	ws := createBoolBranchTestWorkspace(t, src)
	analyzer := NewBooleanBranchingAnalyzer(ws, 2)
	violations := analyzer.AnalyzeWorkspace()

	if len(violations) != 1 {
		t.Fatalf("Expected 1 violation, got %d", len(violations))
	}

	v := violations[0]
	if v.Function != "handle" {
		t.Errorf("Expected function 'handle', got %q", v.Function)
	}
	if v.SourceVariable != "accept" {
		t.Errorf("Expected source variable 'accept', got %q", v.SourceVariable)
	}
	if v.BranchCount != 2 {
		t.Errorf("Expected branch count 2, got %d", v.BranchCount)
	}
	if len(v.BooleanVariables) != 2 {
		t.Errorf("Expected 2 boolean variables, got %d", len(v.BooleanVariables))
	}
}

func TestBooleanBranching_SingleBoolean_NoViolation(t *testing.T) {
	src := `package testpkg

func handle(accept string) {
	wantShapefile := accept == "x-shapefile"

	if wantShapefile {
		doShapefile()
	}
}

func doShapefile() {}
`
	ws := createBoolBranchTestWorkspace(t, src)
	analyzer := NewBooleanBranchingAnalyzer(ws, 2)
	violations := analyzer.AnalyzeWorkspace()

	if len(violations) != 0 {
		t.Errorf("Expected 0 violations for single boolean, got %d", len(violations))
	}
}

func TestBooleanBranching_DifferentSources_NoViolation(t *testing.T) {
	src := `package testpkg

func handle(accept string, method string) {
	wantShapefile := accept == "x-shapefile"
	isPost := method == "POST"

	if wantShapefile {
		doShapefile()
	} else if isPost {
		doPost()
	}
}

func doShapefile() {}
func doPost() {}
`
	ws := createBoolBranchTestWorkspace(t, src)
	analyzer := NewBooleanBranchingAnalyzer(ws, 2)
	violations := analyzer.AnalyzeWorkspace()

	if len(violations) != 0 {
		t.Errorf("Expected 0 violations for different sources, got %d", len(violations))
	}
}

func TestBooleanBranching_AlreadySwitch_NoViolation(t *testing.T) {
	src := `package testpkg

func handle(accept string) {
	switch accept {
	case "x-shapefile":
		doShapefile()
	case "geojson":
		doGeoJSON()
	}
}

func doShapefile() {}
func doGeoJSON() {}
`
	ws := createBoolBranchTestWorkspace(t, src)
	analyzer := NewBooleanBranchingAnalyzer(ws, 2)
	violations := analyzer.AnalyzeWorkspace()

	if len(violations) != 0 {
		t.Errorf("Expected 0 violations for switch statement, got %d", len(violations))
	}
}

func TestBooleanBranching_MultipleViolations(t *testing.T) {
	src := `package testpkg

func handle(accept string, method string) {
	wantShapefile := accept == "x-shapefile"
	wantGeoJSON := accept == "geojson"

	isGet := method == "GET"
	isPost := method == "POST"

	if wantShapefile {
		doShapefile()
	} else if wantGeoJSON {
		doGeoJSON()
	}

	if isGet {
		doGet()
	} else if isPost {
		doPost()
	}
}

func doShapefile() {}
func doGeoJSON() {}
func doGet() {}
func doPost() {}
`
	ws := createBoolBranchTestWorkspace(t, src)
	analyzer := NewBooleanBranchingAnalyzer(ws, 2)
	violations := analyzer.AnalyzeWorkspace()

	if len(violations) != 2 {
		t.Fatalf("Expected 2 violations, got %d", len(violations))
	}

	// Verify both source variables are present
	sources := map[string]bool{}
	for _, v := range violations {
		sources[v.SourceVariable] = true
	}
	if !sources["accept"] {
		t.Error("Expected violation for source 'accept'")
	}
	if !sources["method"] {
		t.Error("Expected violation for source 'method'")
	}
}

func TestBooleanBranching_PackageFiltering(t *testing.T) {
	fileSet := token.NewFileSet()

	src1 := `package pkg1

func handle(accept string) {
	wantShapefile := accept == "x-shapefile"
	wantGeoJSON := accept == "geojson"

	if wantShapefile {
		doShapefile()
	} else if wantGeoJSON {
		doGeoJSON()
	}
}

func doShapefile() {}
func doGeoJSON() {}
`
	src2 := `package pkg2

func clean() {}
`
	ast1, _ := parser.ParseFile(fileSet, "pkg1.go", src1, parser.ParseComments)
	ast2, _ := parser.ParseFile(fileSet, "pkg2.go", src2, parser.ParseComments)

	file1 := &types.File{Path: "pkg1.go", AST: ast1, OriginalContent: []byte(src1)}
	file2 := &types.File{Path: "pkg2.go", AST: ast2, OriginalContent: []byte(src2)}

	pkg1 := &types.Package{Name: "pkg1", Path: "test/pkg1", Files: map[string]*types.File{"pkg1.go": file1}}
	pkg2 := &types.Package{Name: "pkg2", Path: "test/pkg2", Files: map[string]*types.File{"pkg2.go": file2}}
	file1.Package = pkg1
	file2.Package = pkg2

	ws := &types.Workspace{
		Packages: map[string]*types.Package{"test/pkg1": pkg1, "test/pkg2": pkg2},
		FileSet:  fileSet,
	}

	analyzer := NewBooleanBranchingAnalyzer(ws, 2)

	v1 := analyzer.AnalyzePackage(pkg1)
	if len(v1) != 1 {
		t.Errorf("Expected 1 violation in pkg1, got %d", len(v1))
	}

	v2 := analyzer.AnalyzePackage(pkg2)
	if len(v2) != 0 {
		t.Errorf("Expected 0 violations in pkg2, got %d", len(v2))
	}
}

func TestBooleanBranching_NotEqualOperator(t *testing.T) {
	src := `package testpkg

func handle(status string) {
	notOK := status != "ok"
	notError := status != "error"

	if notOK {
		handleNotOK()
	} else if notError {
		handleNotError()
	}
}

func handleNotOK() {}
func handleNotError() {}
`
	ws := createBoolBranchTestWorkspace(t, src)
	analyzer := NewBooleanBranchingAnalyzer(ws, 2)
	violations := analyzer.AnalyzeWorkspace()

	if len(violations) != 1 {
		t.Fatalf("Expected 1 violation for != operator, got %d", len(violations))
	}
	if violations[0].SourceVariable != "status" {
		t.Errorf("Expected source variable 'status', got %q", violations[0].SourceVariable)
	}
}

func TestBooleanBranching_BooleansNotUsedInBranching_NoViolation(t *testing.T) {
	src := `package testpkg

func handle(accept string) bool {
	wantShapefile := accept == "x-shapefile"
	wantGeoJSON := accept == "geojson"

	return wantShapefile || wantGeoJSON
}
`
	ws := createBoolBranchTestWorkspace(t, src)
	analyzer := NewBooleanBranchingAnalyzer(ws, 2)
	violations := analyzer.AnalyzeWorkspace()

	if len(violations) != 0 {
		t.Errorf("Expected 0 violations when bools not used in if branching, got %d", len(violations))
	}
}
