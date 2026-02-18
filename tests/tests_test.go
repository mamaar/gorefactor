package tests_test

import (
	"io"
	"log/slog"
	"path/filepath"
	"testing"

	"github.com/mamaar/gorefactor/pkg/analysis"
	"github.com/mamaar/gorefactor/pkg/refactor"
	"github.com/mamaar/gorefactor/pkg/types"
)

// --- Phase 1: Core renaming/moving ---

func TestRenameSymbol(t *testing.T) {
	tmpDir := copyFixture(t, "rename_symbol")
	eng := createEngine(t)
	ws := loadWorkspace(t, eng, tmpDir)

	plan, err := eng.RenameSymbol(ws, types.RenameSymbolRequest{
		SymbolName: "Add",
		NewName:    "Sum",
		Scope:      types.WorkspaceScope,
	})
	if err != nil {
		t.Fatalf("RenameSymbol: %v", err)
	}
	if err := eng.ExecutePlan(plan); err != nil {
		t.Fatalf("ExecutePlan: %v", err)
	}
	compareGoldenFiles(t, "rename_symbol", tmpDir)
}

func TestRenameMethod(t *testing.T) {
	tmpDir := copyFixture(t, "rename_method")
	eng := createEngine(t)
	ws := loadWorkspace(t, eng, tmpDir)

	plan, err := eng.RenameMethod(ws, types.RenameMethodRequest{
		TypeName:      "Calculator",
		MethodName:    "Add",
		NewMethodName: "Plus",
	})
	if err != nil {
		t.Fatalf("RenameMethod: %v", err)
	}
	if err := eng.ExecutePlan(plan); err != nil {
		t.Fatalf("ExecutePlan: %v", err)
	}
	compareGoldenFiles(t, "rename_method", tmpDir)
}

func TestRenamePackage(t *testing.T) {
	tmpDir := copyFixture(t, "rename_package")
	eng := createEngine(t)
	ws := loadWorkspace(t, eng, tmpDir)

	plan, err := eng.RenamePackage(ws, types.RenamePackageRequest{
		PackagePath:    filepath.Join(tmpDir, "pkg", "oldname"),
		OldPackageName: "oldname",
		NewPackageName: "newname",
		UpdateImports:  true,
	})
	if err != nil {
		t.Fatalf("RenamePackage: %v", err)
	}
	if err := eng.ExecutePlan(plan); err != nil {
		t.Fatalf("ExecutePlan: %v", err)
	}
	compareGoldenFiles(t, "rename_package", tmpDir)
}

func TestMoveSymbol(t *testing.T) {
	tmpDir := copyFixture(t, "move_symbol")
	eng := createEngine(t)
	ws := loadWorkspace(t, eng, tmpDir)

	plan, err := eng.MoveSymbol(ws, types.MoveSymbolRequest{
		SymbolName:  "Multiply",
		FromPackage: tmpDir,
		ToPackage:   filepath.Join(tmpDir, "pkg", "target"),
	})
	if err != nil {
		t.Fatalf("MoveSymbol: %v", err)
	}
	if err := eng.ExecutePlan(plan); err != nil {
		t.Fatalf("ExecutePlan: %v", err)
	}
	compareGoldenFiles(t, "move_symbol", tmpDir)
}

// --- Phase 2: Extract operations ---

func TestExtractFunction(t *testing.T) {
	tmpDir := copyFixture(t, "extract_function")
	eng := createEngine(t)
	ws := loadWorkspace(t, eng, tmpDir)

	plan, err := eng.ExtractFunction(ws, types.ExtractFunctionRequest{
		SourceFile:      filepath.Join(tmpDir, "main.go"),
		StartLine:       8,
		EndLine:         9,
		NewFunctionName: "computeSum",
	})
	if err != nil {
		t.Fatalf("ExtractFunction: %v", err)
	}
	if err := eng.ExecutePlan(plan); err != nil {
		t.Fatalf("ExecutePlan: %v", err)
	}
	compareGoldenFiles(t, "extract_function", tmpDir)
}

func TestExtractMethod(t *testing.T) {
	tmpDir := copyFixture(t, "extract_method")
	eng := createEngine(t)
	ws := loadWorkspace(t, eng, tmpDir)

	plan, err := eng.ExtractMethod(ws, types.ExtractMethodRequest{
		SourceFile:    filepath.Join(tmpDir, "main.go"),
		StartLine:     11,
		EndLine:       12,
		NewMethodName: "computeResult",
		TargetStruct:  "Calculator",
	})
	if err != nil {
		t.Fatalf("ExtractMethod: %v", err)
	}
	if err := eng.ExecutePlan(plan); err != nil {
		t.Fatalf("ExecutePlan: %v", err)
	}
	compareGoldenFiles(t, "extract_method", tmpDir)
}

func TestExtractInterface(t *testing.T) {
	tmpDir := copyFixture(t, "extract_interface")
	eng := createEngine(t)
	ws := loadWorkspace(t, eng, tmpDir)

	plan, err := eng.ExtractInterface(ws, types.ExtractInterfaceRequest{
		SourceStruct:  "Store",
		InterfaceName: "Storage",
		Methods:       []string{"Get", "Set"},
	})
	if err != nil {
		t.Fatalf("ExtractInterface: %v", err)
	}
	if err := eng.ExecutePlan(plan); err != nil {
		t.Fatalf("ExecutePlan: %v", err)
	}
	compareGoldenFiles(t, "extract_interface", tmpDir)
}

func TestExtractVariable(t *testing.T) {
	tmpDir := copyFixture(t, "extract_variable")
	eng := createEngine(t)
	ws := loadWorkspace(t, eng, tmpDir)

	plan, err := eng.ExtractVariable(ws, types.ExtractVariableRequest{
		SourceFile:   "main.go",
		StartLine:    6,
		EndLine:      6,
		VariableName: "result",
		Expression:   "2*3 + 4*5",
	})
	if err != nil {
		t.Fatalf("ExtractVariable: %v", err)
	}
	if err := eng.ExecutePlan(plan); err != nil {
		t.Fatalf("ExecutePlan: %v", err)
	}
	compareGoldenFiles(t, "extract_variable", tmpDir)
}

// --- Phase 2 continued: Inline operations ---

func TestInlineFunction(t *testing.T) {
	tmpDir := copyFixture(t, "inline_function")
	eng := createEngine(t)
	ws := loadWorkspace(t, eng, tmpDir)

	plan, err := eng.InlineFunction(ws, types.InlineFunctionRequest{
		FunctionName: "double",
		SourceFile:   filepath.Join(tmpDir, "helper.go"),
		TargetFiles:  []string{filepath.Join(tmpDir, "main.go")},
	})
	if err != nil {
		t.Fatalf("InlineFunction: %v", err)
	}
	if err := eng.ExecutePlan(plan); err != nil {
		t.Fatalf("ExecutePlan: %v", err)
	}
	compareGoldenFiles(t, "inline_function", tmpDir)
}

func TestInlineMethod(t *testing.T) {
	tmpDir := copyFixture(t, "inline_method")
	eng := createEngine(t)
	ws := loadWorkspace(t, eng, tmpDir)

	plan, err := eng.InlineMethod(ws, types.InlineMethodRequest{
		MethodName:   "Square",
		SourceStruct: "Math",
		TargetFile:   filepath.Join(tmpDir, "main.go"),
	})
	if err != nil {
		t.Fatalf("InlineMethod: %v", err)
	}
	if err := eng.ExecutePlan(plan); err != nil {
		t.Fatalf("ExecutePlan: %v", err)
	}
	compareGoldenFiles(t, "inline_method", tmpDir)
}

func TestInlineVariable(t *testing.T) {
	tmpDir := copyFixture(t, "inline_variable")
	eng := createEngine(t)
	ws := loadWorkspace(t, eng, tmpDir)

	plan, err := eng.InlineVariable(ws, types.InlineVariableRequest{
		VariableName: "msg",
		SourceFile:   filepath.Join(tmpDir, "main.go"),
	})
	if err != nil {
		t.Fatalf("InlineVariable: %v", err)
	}
	if err := eng.ExecutePlan(plan); err != nil {
		t.Fatalf("ExecutePlan: %v", err)
	}
	compareGoldenFiles(t, "inline_variable", tmpDir)
}

// --- Phase 3: Signature & delete tools ---

func TestChangeSignature(t *testing.T) {
	tmpDir := copyFixture(t, "change_signature")
	eng := createEngine(t)
	ws := loadWorkspace(t, eng, tmpDir)

	idx := buildReferenceIndex(t, ws)
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))

	op := &refactor.ChangeSignatureOperation{
		FunctionName: "Greet",
		SourceFile:   filepath.Join(tmpDir, "main.go"),
		NewParams: []refactor.Parameter{
			{Name: "greeting", Type: "string"},
			{Name: "name", Type: "string"},
		},
		Scope:              types.WorkspaceScope,
		DefaultValue:       `"Hello"`,
		NewParamPosition:   0,
		NewReturnPosition:  -1,
		RemovedReturnIndex: -1,
		CachedIndex:        idx,
		Logger:             logger,
	}

	if err := op.Validate(ws); err != nil {
		t.Fatalf("Validate: %v", err)
	}
	plan, err := op.Execute(ws)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if err := eng.ExecutePlan(plan); err != nil {
		t.Fatalf("ExecutePlan: %v", err)
	}
	compareGoldenFiles(t, "change_signature", tmpDir)
}

func TestAddContextParameter(t *testing.T) {
	tmpDir := copyFixture(t, "add_context_parameter")
	eng := createEngine(t)
	ws := loadWorkspace(t, eng, tmpDir)

	idx := buildReferenceIndex(t, ws)
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))

	op := &refactor.ChangeSignatureOperation{
		FunctionName: "Process",
		SourceFile:   filepath.Join(tmpDir, "main.go"),
		NewParams: []refactor.Parameter{
			{Name: "ctx", Type: "context.Context"},
			{Name: "name", Type: "string"},
		},
		Scope:              types.WorkspaceScope,
		DefaultValue:       "context.TODO()",
		NewParamPosition:   0,
		NewReturnPosition:  -1,
		RemovedReturnIndex: -1,
		CachedIndex:        idx,
		Logger:             logger,
	}

	if err := op.Validate(ws); err != nil {
		t.Fatalf("Validate: %v", err)
	}
	plan, err := op.Execute(ws)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if err := eng.ExecutePlan(plan); err != nil {
		t.Fatalf("ExecutePlan: %v", err)
	}
	compareGoldenFiles(t, "add_context_parameter", tmpDir)
}

func TestSafeDelete(t *testing.T) {
	tmpDir := copyFixture(t, "safe_delete")
	eng := createEngine(t)
	ws := loadWorkspace(t, eng, tmpDir)

	op := &refactor.SafeDeleteOperation{
		SymbolName: "deprecatedHelper",
		SourceFile: filepath.Join(tmpDir, "main.go"),
		Scope:      types.WorkspaceScope,
		Force:      false,
	}

	if err := op.Validate(ws); err != nil {
		t.Fatalf("Validate: %v", err)
	}
	plan, err := op.Execute(ws)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if err := eng.ExecutePlan(plan); err != nil {
		t.Fatalf("ExecutePlan: %v", err)
	}
	compareGoldenFiles(t, "safe_delete", tmpDir)
}

// --- Phase 4: Code smell fixers ---

func TestFixIfInit(t *testing.T) {
	tmpDir := copyFixture(t, "fix_if_init")
	eng := createEngine(t)
	ws := loadWorkspace(t, eng, tmpDir)

	fixer := analysis.NewIfInitFixer(ws)
	plan, err := fixer.Fix("")
	if err != nil {
		t.Fatalf("Fix: %v", err)
	}
	if err := eng.ExecutePlan(plan); err != nil {
		t.Fatalf("ExecutePlan: %v", err)
	}
	compareGoldenFiles(t, "fix_if_init", tmpDir)
}

func TestFixBooleanBranching(t *testing.T) {
	tmpDir := copyFixture(t, "fix_boolean_branching")
	eng := createEngine(t)
	ws := loadWorkspace(t, eng, tmpDir)

	fixer := analysis.NewBooleanBranchingFixer(ws, 2)
	plan, _, err := fixer.Fix("")
	if err != nil {
		t.Fatalf("Fix: %v", err)
	}
	if err := eng.ExecutePlan(plan); err != nil {
		t.Fatalf("ExecutePlan: %v", err)
	}
	compareGoldenFiles(t, "fix_boolean_branching", tmpDir)
}

func TestFixDeepIfElse(t *testing.T) {
	tmpDir := copyFixture(t, "fix_deep_if_else")
	eng := createEngine(t)
	ws := loadWorkspace(t, eng, tmpDir)

	fixer := analysis.NewDeepIfElseFixer(ws, 2, 3)
	plan, _, err := fixer.Fix("")
	if err != nil {
		t.Fatalf("Fix: %v", err)
	}
	if err := eng.ExecutePlan(plan); err != nil {
		t.Fatalf("ExecutePlan: %v", err)
	}
	compareGoldenFiles(t, "fix_deep_if_else", tmpDir)
}

func TestFixErrorWrapping(t *testing.T) {
	tmpDir := copyFixture(t, "fix_error_wrapping")
	eng := createEngine(t)
	ws := loadWorkspace(t, eng, tmpDir)

	fixer := analysis.NewErrorWrappingFixer(ws, analysis.SeverityCritical)
	plan, _, err := fixer.Fix("")
	if err != nil {
		t.Fatalf("Fix: %v", err)
	}
	if err := eng.ExecutePlan(plan); err != nil {
		t.Fatalf("ExecutePlan: %v", err)
	}
	compareGoldenFiles(t, "fix_error_wrapping", tmpDir)
}
