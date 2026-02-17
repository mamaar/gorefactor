package analysis

import (
	"go/parser"
	"go/token"
	"testing"

	"github.com/mamaar/gorefactor/pkg/types"
)

func createEnvBoolTestWorkspace(t *testing.T, src string) *types.Workspace {
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

func TestEnvBoolean_IsTestParam(t *testing.T) {
	src := `package testpkg

func NewService(name string, isTest bool) *Service {
	if isTest {
		return &Service{bucket: "test-bucket"}
	}
	return &Service{bucket: "prod-bucket"}
}

type Service struct{ bucket string }
`
	ws := createEnvBoolTestWorkspace(t, src)
	analyzer := NewEnvBooleanAnalyzer(ws, 0)
	violations := analyzer.AnalyzeWorkspace()

	if len(violations) != 1 {
		t.Fatalf("Expected 1 violation, got %d", len(violations))
	}

	v := violations[0]
	if v.Function != "NewService" {
		t.Errorf("Expected function 'NewService', got %q", v.Function)
	}
	if v.ParameterName != "isTest" {
		t.Errorf("Expected parameter 'isTest', got %q", v.ParameterName)
	}
	if v.ParameterType != "bool" {
		t.Errorf("Expected type 'bool', got %q", v.ParameterType)
	}
	if v.SuggestedPattern != "interface_implementation" {
		t.Errorf("Expected interface_implementation, got %q", v.SuggestedPattern)
	}
}

func TestEnvBoolean_IsProdParam(t *testing.T) {
	src := `package testpkg

func Export(isProd bool) error {
	if isProd {
		return exportProd()
	}
	return exportTest()
}

func exportProd() error { return nil }
func exportTest() error { return nil }
`
	ws := createEnvBoolTestWorkspace(t, src)
	analyzer := NewEnvBooleanAnalyzer(ws, 0)
	violations := analyzer.AnalyzeWorkspace()

	if len(violations) != 1 {
		t.Fatalf("Expected 1 violation, got %d", len(violations))
	}
	if violations[0].ParameterName != "isProd" {
		t.Errorf("Expected 'isProd', got %q", violations[0].ParameterName)
	}
}

func TestEnvBoolean_DebugParam(t *testing.T) {
	src := `package testpkg

func Process(debug bool) {
	if debug {
		log("debug info")
	}
}

func log(s string) {}
`
	ws := createEnvBoolTestWorkspace(t, src)
	analyzer := NewEnvBooleanAnalyzer(ws, 0)
	violations := analyzer.AnalyzeWorkspace()

	if len(violations) != 1 {
		t.Fatalf("Expected 1 violation, got %d", len(violations))
	}
	if violations[0].SuggestedPattern != "concrete_value" {
		t.Errorf("Expected concrete_value for debug, got %q", violations[0].SuggestedPattern)
	}
}

func TestEnvBoolean_NormalBool_NoViolation(t *testing.T) {
	src := `package testpkg

func Process(enabled bool, verbose bool, force bool) {
	if enabled {
		doWork()
	}
}

func doWork() {}
`
	ws := createEnvBoolTestWorkspace(t, src)
	analyzer := NewEnvBooleanAnalyzer(ws, 0)
	violations := analyzer.AnalyzeWorkspace()

	if len(violations) != 0 {
		t.Errorf("Expected 0 violations for normal bools, got %d", len(violations))
	}
}

func TestEnvBoolean_NonBoolParam_NoViolation(t *testing.T) {
	src := `package testpkg

func Process(isTest string) {
	_ = isTest
}
`
	ws := createEnvBoolTestWorkspace(t, src)
	analyzer := NewEnvBooleanAnalyzer(ws, 0)
	violations := analyzer.AnalyzeWorkspace()

	if len(violations) != 0 {
		t.Errorf("Expected 0 violations for non-bool param, got %d", len(violations))
	}
}

func TestEnvBoolean_Propagation(t *testing.T) {
	src := `package testpkg

func NewService(isTest bool) *Service {
	svc := &Service{}
	svc.init(isTest)
	return svc
}

type Service struct{}
func (s *Service) init(isTest bool) {}
`
	ws := createEnvBoolTestWorkspace(t, src)
	analyzer := NewEnvBooleanAnalyzer(ws, 1)
	violations := analyzer.AnalyzeWorkspace()

	// NewService passes isTest to svc.init — propagation depth >= 1
	found := false
	for _, v := range violations {
		if v.Function == "NewService" && v.PropagationDepth >= 1 {
			found = true
			if len(v.CallChain) < 2 {
				t.Errorf("Expected call chain length >= 2, got %d: %v", len(v.CallChain), v.CallChain)
			}
		}
	}
	if !found {
		t.Error("Expected violation for NewService with propagation depth >= 1")
	}
}

func TestEnvBoolean_NoPropagation_BelowThreshold(t *testing.T) {
	src := `package testpkg

func NewService(isTest bool) *Service {
	if isTest {
		return &Service{bucket: "test"}
	}
	return &Service{bucket: "prod"}
}

type Service struct{ bucket string }
`
	ws := createEnvBoolTestWorkspace(t, src)
	// maxDepth=2 means we only flag if propagation >= 2
	analyzer := NewEnvBooleanAnalyzer(ws, 2)
	violations := analyzer.AnalyzeWorkspace()

	if len(violations) != 0 {
		t.Errorf("Expected 0 violations with high threshold, got %d", len(violations))
	}
}

func TestEnvBoolean_MultipleViolations(t *testing.T) {
	src := `package testpkg

func NewService(isTest bool, isProd bool) *Service {
	return &Service{}
}

type Service struct{}
`
	ws := createEnvBoolTestWorkspace(t, src)
	analyzer := NewEnvBooleanAnalyzer(ws, 0)
	violations := analyzer.AnalyzeWorkspace()

	if len(violations) != 2 {
		t.Fatalf("Expected 2 violations, got %d", len(violations))
	}

	params := map[string]bool{}
	for _, v := range violations {
		params[v.ParameterName] = true
	}
	if !params["isTest"] || !params["isProd"] {
		t.Errorf("Expected isTest and isProd, got %v", params)
	}
}

func TestEnvBoolean_PackageFiltering(t *testing.T) {
	fileSet := token.NewFileSet()

	src1 := `package pkg1

func Bad(isTest bool) {}
`
	src2 := `package pkg2

func Clean(name string) {}
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

	analyzer := NewEnvBooleanAnalyzer(ws, 0)

	v1 := analyzer.AnalyzePackage(pkg1)
	if len(v1) != 1 {
		t.Errorf("Expected 1 violation in pkg1, got %d", len(v1))
	}

	v2 := analyzer.AnalyzePackage(pkg2)
	if len(v2) != 0 {
		t.Errorf("Expected 0 violations in pkg2, got %d", len(v2))
	}
}

func TestEnvBoolean_AllPatterns(t *testing.T) {
	names := []string{
		"isTest", "isProd", "isProduction", "isDev", "isDevelopment",
		"isLocal", "isStaging", "isDebug", "testMode", "devMode",
		"debugMode", "prodMode", "production", "debug", "testing",
	}

	for _, name := range names {
		if !isEnvBoolName(name) {
			t.Errorf("Expected %q to be recognized as env bool", name)
		}
	}

	nonEnv := []string{"enabled", "verbose", "force", "active", "valid", "ok"}
	for _, name := range nonEnv {
		if isEnvBoolName(name) {
			t.Errorf("Expected %q to NOT be recognized as env bool", name)
		}
	}
}
