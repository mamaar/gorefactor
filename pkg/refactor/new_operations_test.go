package refactor

import (
	"go/ast"
	"go/parser"
	"go/token"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/mamaar/gorefactor/pkg/types"
)

func TestChangeSignatureOperation_Type(t *testing.T) {
	op := &ChangeSignatureOperation{}
	if op.Type() != types.ExtractOperation {
		t.Errorf("Expected ExtractOperation, got %v", op.Type())
	}
}

func TestChangeSignatureOperation_Validate(t *testing.T) {
	tests := []struct {
		name    string
		op      *ChangeSignatureOperation
		wantErr bool
	}{
		{
			name: "valid change signature operation",
			op: &ChangeSignatureOperation{
				FunctionName: "TestFunction",
				SourceFile:   "test.go",
				NewParams: []Parameter{
					{Name: "param1", Type: "string"},
					{Name: "param2", Type: "int"},
				},
				NewReturns: []string{"error"},
				Scope:      types.PackageScope,
			},
			wantErr: false,
		},
		{
			name: "invalid - empty function name",
			op: &ChangeSignatureOperation{
				FunctionName: "",
				SourceFile:   "test.go",
				NewParams:    []Parameter{},
				Scope:        types.PackageScope,
			},
			wantErr: true,
		},
		{
			name: "invalid - empty source file",
			op: &ChangeSignatureOperation{
				FunctionName: "TestFunction",
				SourceFile:   "",
				NewParams:    []Parameter{},
				Scope:        types.PackageScope,
			},
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ws := createTestWorkspaceForNewOps()

			err := tt.op.Validate(ws)
			if (err != nil) != tt.wantErr {
				t.Errorf("ChangeSignatureOperation.Validate() error = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}

func TestChangeSignatureOperation_Description(t *testing.T) {
	op := &ChangeSignatureOperation{
		FunctionName: "TestFunction",
		Scope:        types.WorkspaceScope,
	}

	desc := op.Description()

	expected := "Change signature of function TestFunction (scope: workspace)"
	if desc != expected {
		t.Errorf("Expected description %q, got %q", expected, desc)
	}
}

func TestSafeDeleteOperation_Type(t *testing.T) {
	op := &SafeDeleteOperation{}
	if op.Type() != types.InlineOperation {
		t.Errorf("Expected InlineOperation, got %v", op.Type())
	}
}

func TestSafeDeleteOperation_Validate(t *testing.T) {
	tests := []struct {
		name    string
		op      *SafeDeleteOperation
		wantErr bool
	}{
		{
			name: "invalid - empty symbol name",
			op: &SafeDeleteOperation{
				SymbolName: "",
				SourceFile: "test.go",
				Scope:      types.PackageScope,
			},
			wantErr: true,
		},
		{
			name: "invalid - empty source file",
			op: &SafeDeleteOperation{
				SymbolName: "TestSymbol",
				SourceFile: "",
				Scope:      types.PackageScope,
			},
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ws := createTestWorkspaceForNewOps()

			err := tt.op.Validate(ws)
			if (err != nil) != tt.wantErr {
				t.Errorf("SafeDeleteOperation.Validate() error = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}

func TestSafeDeleteOperation_BlockingReferences(t *testing.T) {
	// Build a fixture where the symbol under test has real, resolvable
	// references: Greet is called twice from UseGreet.
	fset := token.NewFileSet()
	src := `package sample

func Greet() string {
	return "hi"
}

func UseGreet() string {
	return Greet() + Greet()
}
`

	astFile, err := parser.ParseFile(fset, "greeter.go", src, parser.ParseComments)
	if err != nil {
		t.Fatalf("failed to parse fixture source: %v", err)
	}

	file := &types.File{
		Path:            "greeter.go",
		AST:             astFile,
		OriginalContent: []byte(src),
	}

	// Locate the real declaration position of Greet so the symbol table
	// matches what FindReferences will see when it walks the AST.
	var greetDecl *ast.FuncDecl

	for _, decl := range astFile.Decls {
		fn, ok := decl.(*ast.FuncDecl)
		if ok && fn.Name.Name == "Greet" {
			greetDecl = fn
			break
		}
	}

	if greetDecl == nil {
		t.Fatal("fixture is missing a Greet function declaration")
	}

	greetSymbol := &types.Symbol{
		Name:     "Greet",
		Kind:     types.FunctionSymbol,
		Package:  "sample",
		File:     "greeter.go",
		Position: greetDecl.Name.Pos(),
		End:      greetDecl.Name.End(),
	}

	pkg := &types.Package{
		Path:  "sample",
		Name:  "sample",
		Files: map[string]*types.File{"greeter.go": file},
		Symbols: &types.SymbolTable{
			Functions: map[string]*types.Symbol{"Greet": greetSymbol},
		},
	}
	file.Package = pkg

	ws := &types.Workspace{
		Packages: map[string]*types.Package{"sample": pkg},
		FileSet:  fset,
	}

	op := &SafeDeleteOperation{
		SymbolName: "Greet",
		SourceFile: "greeter.go",
		Scope:      types.WorkspaceScope,
	}

	blocking, err := op.BlockingReferences(ws)
	if err != nil {
		t.Fatalf("BlockingReferences() returned error: %v", err)
	}

	if len(blocking) != 2 {
		t.Fatalf("expected 2 blocking references, got %d: %+v", len(blocking), blocking)
	}

	for _, ref := range blocking {
		if ref.File != "greeter.go" {
			t.Errorf("expected reference File %q, got %q", "greeter.go", ref.File)
		}

		if ref.Line <= 0 {
			t.Errorf("expected positive Line, got %d", ref.Line)
		}
	}
}

func TestSafeDeleteOperation_BlockingReferences_None(t *testing.T) {
	// A symbol with no callers should report zero blocking references,
	// returned as an empty (non-nil) slice.
	fset := token.NewFileSet()
	src := `package sample

func Unused() string {
	return "hi"
}
`

	astFile, err := parser.ParseFile(fset, "greeter.go", src, parser.ParseComments)
	if err != nil {
		t.Fatalf("failed to parse fixture source: %v", err)
	}

	file := &types.File{
		Path:            "greeter.go",
		AST:             astFile,
		OriginalContent: []byte(src),
	}

	var unusedDecl *ast.FuncDecl

	for _, decl := range astFile.Decls {
		fn, ok := decl.(*ast.FuncDecl)
		if ok && fn.Name.Name == "Unused" {
			unusedDecl = fn
			break
		}
	}

	if unusedDecl == nil {
		t.Fatal("fixture is missing an Unused function declaration")
	}

	unusedSymbol := &types.Symbol{
		Name:     "Unused",
		Kind:     types.FunctionSymbol,
		Package:  "sample",
		File:     "greeter.go",
		Position: unusedDecl.Name.Pos(),
		End:      unusedDecl.Name.End(),
	}

	pkg := &types.Package{
		Path:  "sample",
		Name:  "sample",
		Files: map[string]*types.File{"greeter.go": file},
		Symbols: &types.SymbolTable{
			Functions: map[string]*types.Symbol{"Unused": unusedSymbol},
		},
	}
	file.Package = pkg

	ws := &types.Workspace{
		Packages: map[string]*types.Package{"sample": pkg},
		FileSet:  fset,
	}

	op := &SafeDeleteOperation{
		SymbolName: "Unused",
		SourceFile: "greeter.go",
		Scope:      types.WorkspaceScope,
	}

	blocking, err := op.BlockingReferences(ws)
	if err != nil {
		t.Fatalf("BlockingReferences() returned error: %v", err)
	}

	if blocking == nil {
		t.Error("expected non-nil empty slice, got nil")
	}

	if len(blocking) != 0 {
		t.Errorf("expected 0 blocking references, got %d: %+v", len(blocking), blocking)
	}
}

func TestSafeDeleteOperation_Description(t *testing.T) {
	op := &SafeDeleteOperation{
		SymbolName: "TestSymbol",
		SourceFile: "test.go",
		Scope:      types.WorkspaceScope,
		Force:      true,
	}

	desc := op.Description()

	expected := "Safe delete TestSymbol from test.go (scope: workspace) (forced)"
	if desc != expected {
		t.Errorf("Expected description %q, got %q", expected, desc)
	}
}

// Helper function to create test workspace for new operations
func createTestWorkspaceForNewOps() *types.Workspace {
	// Create a test file with more comprehensive content
	testFile := &types.File{
		Path:    "test.go",
		Package: nil, // Will be set after package creation
		OriginalContent: []byte(`package main

import "fmt"

const ExistingConst = 42

func main() {
	fmt.Println("Hello World")
	x := 123
	y := "test string"
	z := true
}

func TestFunction(a int, b string) error {
	fmt.Printf("%d %s\n", a, b)
	return nil
}

type TestStruct struct {
	Name string
}

func (t *TestStruct) TestMethod() {
	fmt.Println(t.Name)
}
`),
	}

	fset := token.NewFileSet()
	parsedAST, _ := parser.ParseFile(fset, "test.go", testFile.OriginalContent, parser.ParseComments)
	testFile.AST = parsedAST

	// Create test symbols
	testFunctionSymbol := &types.Symbol{
		Name:     "TestFunction",
		Kind:     types.FunctionSymbol,
		Package:  "main",
		File:     "test.go",
		Position: 200,
		End:      300,
		Line:     15,
		Column:   6,
		Exported: true,
	}

	testStructSymbol := &types.Symbol{
		Name:     "TestStruct",
		Kind:     types.TypeSymbol,
		Package:  "main",
		File:     "test.go",
		Position: 350,
		End:      400,
		Line:     20,
		Column:   6,
		Exported: true,
	}

	testMethodSymbol := &types.Symbol{
		Name:     "TestMethod",
		Kind:     types.MethodSymbol,
		Package:  "main",
		File:     "test.go",
		Position: 450,
		End:      500,
		Line:     24,
		Column:   6,
		Exported: true,
	}

	// Create symbol table
	symbolTable := &types.SymbolTable{
		Functions: map[string]*types.Symbol{
			"TestFunction": testFunctionSymbol,
		},
		Types: map[string]*types.Symbol{
			"TestStruct": testStructSymbol,
		},
		Variables: make(map[string]*types.Symbol),
		Constants: map[string]*types.Symbol{
			"ExistingConst": {
				Name:     "ExistingConst",
				Kind:     types.ConstantSymbol,
				Package:  "main",
				File:     "test.go",
				Position: 50,
				Exported: true,
			},
		},
		Methods: map[string][]*types.Symbol{
			"TestStruct": {testMethodSymbol},
		},
	}

	// Create a test package
	pkg := &types.Package{
		Path: "main",
		Name: "main",
		Dir:  "/test",
		Files: map[string]*types.File{
			"test.go": testFile,
		},
		Symbols: symbolTable,
	}

	// Set the package reference in the file
	testFile.Package = pkg

	return &types.Workspace{
		RootPath: "/test",
		FileSet:  fset,
		Packages: map[string]*types.Package{
			"main": pkg,
		},
	}
}

// extractConstantFixture loads files as a module with compilation validation on.
func extractConstantFixture(t *testing.T, files map[string]string) (*DefaultEngine, *types.Workspace, string) {
	t.Helper()
	dir := t.TempDir()

	files["go.mod"] = "module example.com/ec\n\ngo 1.21\n"
	for rel, content := range files {
		err := os.WriteFile(filepath.Join(dir, rel), []byte(content), 0o644)
		if err != nil {
			t.Fatal(err)
		}
	}

	engine := CreateEngineWithConfig(DefaultConfig(), slog.New(slog.NewTextHandler(io.Discard, nil))).(*DefaultEngine)

	ws, err := engine.LoadWorkspace(dir)
	if err != nil {
		t.Fatalf("LoadWorkspace: %v", err)
	}

	return engine, ws, dir
}

// extractConstantAt extracts the first occurrence of needle in file.
func extractConstantAt(t *testing.T, e *DefaultEngine, ws *types.Workspace, dir, file, needle, name string) (*types.RefactoringPlan, error) {
	t.Helper()

	src, err := os.ReadFile(filepath.Join(dir, file))
	if err != nil {
		t.Fatal(err)
	}

	off := strings.Index(string(src), needle)
	if off < 0 {
		t.Fatalf("%q not in %s", needle, file)
	}

	line := 1 + strings.Count(string(src[:off]), "\n")
	col := off - strings.LastIndex(string(src[:off]), "\n")

	return e.ExtractConstant(ws, types.ExtractConstantRequest{
		SourceFile: filepath.Join(dir, file), Line: line, Column: col, ConstantName: name,
	})
}

func applyExtractConstant(t *testing.T, e *DefaultEngine, ws *types.Workspace, dir, file, needle, name string) (*types.RefactoringPlan, map[string]string) {
	t.Helper()

	plan, err := extractConstantAt(t, e, ws, dir, file, needle, name)
	if err != nil {
		t.Fatalf("ExtractConstant: %v", err)
	}

	if err := e.ExecutePlan(plan); err != nil {
		t.Fatalf("ExecutePlan: %v", err)
	}

	out := map[string]string{}

	entries, _ := os.ReadDir(dir)
	for _, ent := range entries {
		b, _ := os.ReadFile(filepath.Join(dir, ent.Name()))
		out[ent.Name()] = string(b)
	}

	return plan, out
}

func TestExtractConstant_ReplacesEqualLiteralsInFilesAndInPackageTests(t *testing.T) {
	e, ws, dir := extractConstantFixture(t, map[string]string{
		"a.go":        "package p\n\nfunc A() string { return \"red\" }\n",
		"b.go":        "package p\n\nfunc B() string { return `red` }\n",
		"a_test.go":   "package p\n\nimport \"testing\"\n\nfunc TestA(t *testing.T) {\n\tif A() != \"red\" {\n\t}\n}\n",
		"ext_test.go": "package p_test\n\nvar X = \"red\"\n",
		"keep.go":     "package p\n\nfunc K() string { return \"blue\" }\n",
	})
	_, out := applyExtractConstant(t, e, ws, dir, "a.go", `"red"`, "Red")

	for _, f := range []string{"a.go", "b.go", "a_test.go"} {
		if strings.Contains(out[f], "red") && f != "a.go" {
			t.Errorf("%s still has the literal:\n%s", f, out[f])
		}
	}

	if !strings.Contains(out["a.go"], "const Red = \"red\"") || !strings.Contains(out["a.go"], "return Red") {
		t.Errorf("a.go:\n%s", out["a.go"])
	}

	if !strings.Contains(out["a_test.go"], "A() != Red") {
		t.Errorf("a_test.go:\n%s", out["a_test.go"])
	}

	if !strings.Contains(out["ext_test.go"], `"red"`) {
		t.Errorf("external test file was edited:\n%s", out["ext_test.go"])
	}

	if !strings.Contains(out["keep.go"], `"blue"`) {
		t.Errorf("unequal literal was edited:\n%s", out["keep.go"])
	}
}

func TestExtractConstant_LeavesImportPathsAndStructTagsAlone(t *testing.T) {
	e, ws, dir := extractConstantFixture(t, map[string]string{
		"a.go": "package p\n\nimport \"strings\"\n\ntype T struct {\n\tF string `json:\"f\"`\n}\n\nvar _ = strings.ToUpper\n\nvar S = \"strings\"\nvar Tag = \"json:\\\"f\\\"\"\n",
	})

	plan, out := applyExtractConstant(t, e, ws, dir, "a.go", "\"strings\"\nvar Tag", "Pkg")
	if !strings.Contains(out["a.go"], "\t\"strings\"\n)") || !strings.Contains(out["a.go"], "var S = Pkg") {
		t.Errorf("import path rewritten or literal kept:\n%s", out["a.go"])
	}

	if len(plan.Changes) != 2 {
		t.Errorf("want insertion + 1 replacement, got %d changes", len(plan.Changes))
	}

	e, ws, dir = extractConstantFixture(t, map[string]string{
		"a.go": "package p\n\ntype T struct {\n\tF string `json:\"f\"`\n}\n\nvar Tag = \"json:\\\"f\\\"\"\n",
	})

	_, out = applyExtractConstant(t, e, ws, dir, "a.go", `"json:`, "TagText")
	if !strings.Contains(out["a.go"], "`json:\"f\"`") || !strings.Contains(out["a.go"], "var Tag = TagText") {
		t.Errorf("struct tag rewritten or literal kept:\n%s", out["a.go"])
	}

	e, ws, dir = extractConstantFixture(t, map[string]string{
		"a.go": "package p\n\ntype T struct {\n\tF string `json:\"f\"`\n}\n",
	})

	_, err := extractConstantAt(t, e, ws, dir, "a.go", "`json:", "Nope")
	if err == nil || !strings.Contains(err.Error(), "struct tag") {
		t.Errorf("selecting a struct tag: got %v", err)
	}
}

func TestExtractConstant_UntypedConstKeepsNamedTypesCompiling(t *testing.T) {
	e, ws, dir := extractConstantFixture(t, map[string]string{
		"a.go": "package p\n\ntype Color string\n\nvar c Color = \"red\"\nvar s string = \"red\"\n",
	})

	_, out := applyExtractConstant(t, e, ws, dir, "a.go", `"red"`, "Red")
	if !strings.Contains(out["a.go"], "const Red = \"red\"") {
		t.Errorf("want untyped const:\n%s", out["a.go"])
	}

	if !strings.Contains(out["a.go"], "var c Color = Red") || !strings.Contains(out["a.go"], "var s string = Red") {
		t.Errorf("uses not replaced:\n%s", out["a.go"])
	}
}

func TestExtractConstant_MatchesByKindAndValue(t *testing.T) {
	e, ws, dir := extractConstantFixture(t, map[string]string{
		"a.go": "package p\n\nvar A = 16\nvar B = 0x10\nvar C = 1\nvar D = 1.0\nvar E = \"a\"\nvar F = `a`\nvar G = 'a'\n",
	})

	_, out := applyExtractConstant(t, e, ws, dir, "a.go", "16", "Sixteen")
	if !strings.Contains(out["a.go"], "var B = Sixteen") {
		t.Errorf("0x10 not matched with 16:\n%s", out["a.go"])
	}

	e, ws, dir = extractConstantFixture(t, map[string]string{
		"a.go": "package p\n\nvar C = 1\nvar D = 1.0\nvar E = \"a\"\nvar F = `a`\nvar G = 'a'\n",
	})

	_, out = applyExtractConstant(t, e, ws, dir, "a.go", "1\n", "One")
	if !strings.Contains(out["a.go"], "var D = 1.0") {
		t.Errorf("1.0 matched with 1:\n%s", out["a.go"])
	}

	e, ws, dir = extractConstantFixture(t, map[string]string{
		"a.go": "package p\n\nvar E = \"a\"\nvar F = `a`\nvar G = 'a'\n",
	})

	_, out = applyExtractConstant(t, e, ws, dir, "a.go", `"a"`, "A1")
	if !strings.Contains(out["a.go"], "var F = A1") || !strings.Contains(out["a.go"], "var G = 'a'") {
		t.Errorf("string literals mismatch:\n%s", out["a.go"])
	}
}

func TestExtractConstant_ShadowedOccurrenceIsSkippedAndReported(t *testing.T) {
	e, ws, dir := extractConstantFixture(t, map[string]string{
		"a.go": "package p\n\nvar A = \"red\"\n\nfunc F() string {\n\tRed := 1\n\t_ = Red\n\treturn \"red\"\n}\n",
	})

	plan, out := applyExtractConstant(t, e, ws, dir, "a.go", `"red"`, "Red")
	if !strings.Contains(out["a.go"], "return \"red\"") || !strings.Contains(out["a.go"], "var A = Red") {
		t.Errorf("a.go:\n%s", out["a.go"])
	}

	if len(plan.SkippedSites) != 1 || plan.SkippedSites[0].Line != 8 {
		t.Errorf("skipped sites: %+v", plan.SkippedSites)
	}
}

func TestExtractConstant_GeneratedFiles(t *testing.T) {
	gen := "// Code generated by x. DO NOT EDIT.\n\npackage p\n\nvar G = \"red\"\n"
	e, ws, dir := extractConstantFixture(t, map[string]string{
		"a.go":   "package p\n\nvar A = \"red\"\n",
		"gen.go": gen,
	})

	plan, out := applyExtractConstant(t, e, ws, dir, "a.go", `"red"`, "Red")
	if out["gen.go"] != gen {
		t.Errorf("generated file edited:\n%s", out["gen.go"])
	}

	if len(plan.SkippedSites) != 1 || !strings.Contains(plan.SkippedSites[0].File, "gen.go") {
		t.Errorf("skipped sites: %+v", plan.SkippedSites)
	}

	e, ws, dir = extractConstantFixture(t, map[string]string{"gen.go": gen})

	_, err := extractConstantAt(t, e, ws, dir, "gen.go", `"red"`, "Red")
	if err == nil || !strings.Contains(err.Error(), "generated") {
		t.Errorf("generated source file: got %v", err)
	}
}

func TestExtractConstant_RefusesNameConflictsWritingNothing(t *testing.T) {
	cases := []struct {
		name  string
		files map[string]string
		pick  string
		cname string
		want  string
	}{
		{
			name: "declared in a file excluded by the build configuration",
			files: map[string]string{
				"a.go":         "package p\n\nvar A = \"red\"\n",
				"x_windows.go": "package p\n\nconst Red = 1\n",
			},
			pick: `"red"`, cname: "Red", want: "x_windows.go",
		},
		{
			name: "implicit import name in a file with an occurrence",
			files: map[string]string{
				"a.go": "package p\n\nimport \"strings\"\n\nvar A = \"red\"\nvar _ = strings.ToUpper\n",
			},
			pick: `"red"`, cname: "strings", want: "import",
		},
		{
			name: "aliased import name in a file with an occurrence",
			files: map[string]string{
				"a.go": "package p\n\nimport str \"strings\"\n\nvar A = \"red\"\nvar _ = str.ToUpper\n",
			},
			pick: `"red"`, cname: "str", want: "import",
		},
		{
			name:  "keyword",
			files: map[string]string{"a.go": "package p\n\nvar A = \"red\"\n"},
			pick:  `"red"`, cname: "func", want: "invalid",
		},
		{
			name:  "blank",
			files: map[string]string{"a.go": "package p\n\nvar A = \"red\"\n"},
			pick:  `"red"`, cname: "_", want: "invalid",
		},
		{
			name:  "not a literal",
			files: map[string]string{"a.go": "package p\n\nvar A = \"red\"\n"},
			pick:  "A =", cname: "Red", want: "*ast.Ident",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			e, ws, dir := extractConstantFixture(t, tc.files)

			_, err := extractConstantAt(t, e, ws, dir, "a.go", tc.pick, tc.cname)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("want error containing %q, got %v", tc.want, err)
			}
		})
	}
}

func TestExtractConstant_DoesNotMatchLiteralsThatConvertToTheSameValue(t *testing.T) {
	cases := map[string]string{
		"float32 rounding": "package p\n\nvar A float32 = 0.1\nvar B float64 = 0.100000001490116119384765625\n",
		"float32 integer":  "package p\n\nvar A float32 = 16777217\nvar B float64 = 16777216\n",
	}
	for name, src := range cases {
		t.Run(name, func(t *testing.T) {
			e, ws, dir := extractConstantFixture(t, map[string]string{"a.go": src})

			sel := "0.1"
			if strings.Contains(src, "16777217") {
				sel = "16777217"
			}

			plan, out := applyExtractConstant(t, e, ws, dir, "a.go", sel, "X")
			if len(plan.Changes) != 2 || !strings.Contains(out["a.go"], "var B float64 = 0.1") && !strings.Contains(out["a.go"], "var B float64 = 16777216") {
				t.Errorf("B was rewritten:\n%s", out["a.go"])
			}
		})
	}
}

func TestExtractConstant_RefusesPredeclaredNames(t *testing.T) {
	for _, name := range []string{"iota", "len", "string", "nil", "init", "true"} {
		t.Run(name, func(t *testing.T) {
			e, ws, dir := extractConstantFixture(t, map[string]string{"a.go": "package p\n\nvar A = \"red\"\n"})

			_, err := extractConstantAt(t, e, ws, dir, "a.go", `"red"`, name)
			if err == nil {
				t.Fatal("want a refusal")
			}
		})
	}
}

func TestExtractConstant_DeclaresInAnUnconstrainedProductionFile(t *testing.T) {
	cases := []struct {
		name, source string
		files        map[string]string
		wantIn       string
	}{
		{
			name: "test file source", source: "a_test.go", wantIn: "a.go",
			files: map[string]string{
				"a.go":      "package p\n\nvar A = \"red\"\n",
				"a_test.go": "package p\n\nvar T = \"red\"\n",
			},
		},
		{
			name: "constrained source", source: "a_cons.go", wantIn: "b.go",
			files: map[string]string{
				"a_cons.go": "//go:build !nosuchtag\n\npackage p\n\nvar A = \"red\"\n",
				"b.go":      "package p\n\nvar B = \"red\"\n",
			},
		},
		{
			name: "suffix-constrained source", source: "a_" + runtime.GOOS + ".go", wantIn: "b.go",
			files: map[string]string{
				"a_" + runtime.GOOS + ".go": "package p\n\nvar A = \"red\"\n",
				"b.go":                      "package p\n\nvar B = \"red\"\n",
			},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			e, ws, dir := extractConstantFixture(t, tc.files)

			_, out := applyExtractConstant(t, e, ws, dir, tc.source, `"red"`, "Red")
			for name, content := range out {
				has := strings.Contains(content, "const Red")
				if has != (name == tc.wantIn) {
					t.Errorf("const Red in %s = %v:\n%s", name, has, content)
				}
			}

			build := exec.Command("go", "build", "./...")
			build.Dir = dir

			b, err := build.CombinedOutput()
			if err != nil {
				t.Errorf("go build: %v\n%s", err, b)
			}
		})
	}

	t.Run("no eligible file", func(t *testing.T) {
		e, ws, dir := extractConstantFixture(t, map[string]string{
			"a_test.go": "package p\n\nvar T = \"red\"\n",
		})

		_, err := extractConstantAt(t, e, ws, dir, "a_test.go", `"red"`, "Red")
		if err == nil {
			t.Fatal("want a refusal")
		}
	})
}

func TestExtractConstant_RefusesNamesThatCollideWithImportsOfAnyFile(t *testing.T) {
	cases := map[string]map[string]string{
		"alias in a file excluded by the build configuration": {
			"a.go":         "package p\n\nvar A = \"red\"\n",
			"x_windows.go": "package p\n\nimport Red \"strings\"\n\nvar _ = Red.ToUpper\n",
		},
		"implicit name in a file without an occurrence": {
			"a.go": "package p\n\nvar A = \"red\"\n",
			"b.go": "package p\n\nimport \"strings\"\n\nvar _ = strings.ToUpper\n",
		},
		"dot import exporting the name": {
			"a.go": "package p\n\nvar A = \"red\"\n",
			"b.go": "package p\n\nimport . \"strings\"\n\nvar _ = ToUpper\n",
		},
	}
	for name, files := range cases {
		t.Run(name, func(t *testing.T) {
			cname := "strings"

			switch {
			case strings.HasPrefix(name, "alias"):
				cname = "Red"
			case strings.HasPrefix(name, "dot"):
				cname = "ToUpper"
			}

			e, ws, dir := extractConstantFixture(t, files)

			_, err := extractConstantAt(t, e, ws, dir, "a.go", `"red"`, cname)
			if err == nil {
				t.Fatal("want a refusal")
			}
		})
	}
}

func TestExtractConstant_KeepsTrailingCommentsOnTheInsertionLine(t *testing.T) {
	for name, src := range map[string]string{
		"import comment":  "package p\n\nimport \"strings\" // used below\n\nvar A = \"red\"\nvar _ = strings.ToUpper\n",
		"package comment": "package p // import \"example.com/ec\"\n\nvar A = \"red\"\n",
	} {
		t.Run(name, func(t *testing.T) {
			e, ws, dir := extractConstantFixture(t, map[string]string{"a.go": src})
			_, out := applyExtractConstant(t, e, ws, dir, "a.go", `"red"`, "Red")

			got := out["a.go"]
			if !strings.Contains(got, "used below\n") && !strings.Contains(got, `import "example.com/ec"`+"\n") {
				t.Errorf("comment detached:\n%s", got)
			}

			idx := strings.Index(got, "const Red")
			if strings.Contains(got[idx:], "// used below") || strings.Contains(got[idx:], "// import") {
				t.Errorf("comment moved after the const:\n%s", got)
			}
		})
	}
}

func TestExtractConstant_RefusesColumnPastEndOfLine(t *testing.T) {
	e, ws, dir := extractConstantFixture(t, map[string]string{"a.go": "package p\n\nvar A = 1\nvar B = \"red\"\n"})

	_, err := e.ExtractConstant(ws, types.ExtractConstantRequest{
		SourceFile: filepath.Join(dir, "a.go"), Line: 3, Column: 20, ConstantName: "Red",
	})
	if err == nil || !strings.Contains(err.Error(), "column") {
		t.Fatalf("want a column error, got %v", err)
	}
}
