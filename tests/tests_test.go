package tests_test

import (
	"io"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/mamaar/gorefactor/pkg/analyzers"
	"github.com/mamaar/gorefactor/pkg/analyzers/booleanbranch"
	"github.com/mamaar/gorefactor/pkg/analyzers/deepifelse"
	"github.com/mamaar/gorefactor/pkg/analyzers/errorwrap"
	"github.com/mamaar/gorefactor/pkg/analyzers/ifinit"
	"github.com/mamaar/gorefactor/pkg/analyzers/switchchain"
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

func TestRenameField(t *testing.T) {
	tmpDir := copyFixture(t, "rename_field")
	eng := createEngine(t)
	ws := loadWorkspace(t, eng, tmpDir)

	plan, err := eng.RenameField(ws, types.RenameFieldRequest{
		TypeName:     "User",
		FieldName:    "Name",
		NewFieldName: "FullName",
	})
	if err != nil {
		t.Fatalf("RenameField: %v", err)
	}
	if err := eng.ExecutePlan(plan); err != nil {
		t.Fatalf("ExecutePlan: %v", err)
	}
	compareGoldenFiles(t, "rename_field", tmpDir)
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

// TestAddReturn verifies the three bugs that were present in the add_return subcommand:
//  1. Parameter list was stripped when only returns were provided.
//  2. Return statements inside the function body were not updated.
//  3. Call sites with expression statements (discarded returns) were not rewritten.
func TestAddReturn(t *testing.T) {
	tmpDir := copyFixture(t, "add_return")
	eng := createEngine(t)
	ws := loadWorkspace(t, eng, tmpDir)

	plan, err := eng.ChangeSignature(ws, refactor.ChangeSignatureRequest{
		FunctionName: "GetConfig",
		SourceFile:   filepath.Join(tmpDir, "main.go"),
		// Bug 1 regression: NewParams is nil — the engine must preserve existing params.
		NewParams:  nil,
		NewReturns: []string{"string", "error"},
		Scope:      types.WorkspaceScope,
		Subcommand: "add_return",
	})
	if err != nil {
		t.Fatalf("ChangeSignature: %v", err)
	}
	if err := eng.ExecutePlan(plan); err != nil {
		t.Fatalf("ExecutePlan: %v", err)
	}
	compareGoldenFiles(t, "add_return", tmpDir)
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

// --- Thread context (transitive add_context_parameter) ---

func TestThreadContext_Chain(t *testing.T) {
	tmpDir := copyFixture(t, "thread_context_chain")
	eng := createEngine(t)
	ws := loadWorkspace(t, eng, tmpDir)

	plan, err := eng.ThreadContext(ws, types.ThreadContextRequest{
		FunctionName: "c",
		SourceFile:   filepath.Join(tmpDir, "c.go"),
	})
	if err != nil {
		t.Fatalf("ThreadContext: %v", err)
	}
	if err := eng.ExecutePlan(plan); err != nil {
		t.Fatalf("ExecutePlan: %v", err)
	}
	compareGoldenFiles(t, "thread_context_chain", tmpDir)
}

func TestThreadContext_Stops(t *testing.T) {
	tmpDir := copyFixture(t, "thread_context_stops")
	eng := createEngine(t)
	ws := loadWorkspace(t, eng, tmpDir)

	plan, err := eng.ThreadContext(ws, types.ThreadContextRequest{
		FunctionName: "Do",
		SourceFile:   filepath.Join(tmpDir, "target", "target.go"),
	})
	if err != nil {
		t.Fatalf("ThreadContext: %v", err)
	}
	if err := eng.ExecutePlan(plan); err != nil {
		t.Fatalf("ExecutePlan: %v", err)
	}
	compareGoldenFiles(t, "thread_context_stops", tmpDir)
}

func TestThreadContext_Interface(t *testing.T) {
	tmpDir := copyFixture(t, "thread_context_interface")
	eng := createEngine(t)
	ws := loadWorkspace(t, eng, tmpDir)

	plan, err := eng.ThreadContext(ws, types.ThreadContextRequest{
		FunctionName: "MemStore.Save",
		SourceFile:   filepath.Join(tmpDir, "main.go"),
	})
	if err != nil {
		t.Fatalf("ThreadContext: %v", err)
	}
	if err := eng.ExecutePlan(plan); err != nil {
		t.Fatalf("ExecutePlan: %v", err)
	}
	compareGoldenFiles(t, "thread_context_interface", tmpDir)
}

func TestThreadContext_TestRoots(t *testing.T) {
	tmpDir := copyFixture(t, "thread_context_test_roots")
	eng := createEngine(t)
	ws := loadWorkspace(t, eng, tmpDir)

	plan, err := eng.ThreadContext(ws, types.ThreadContextRequest{
		FunctionName: "Seed",
		SourceFile:   filepath.Join(tmpDir, "lib.go"),
	})
	if err != nil {
		t.Fatalf("ThreadContext: %v", err)
	}
	if err := eng.ExecutePlan(plan); err != nil {
		t.Fatalf("ExecutePlan: %v", err)
	}
	compareGoldenFiles(t, "thread_context_test_roots", tmpDir)
}

func TestThreadContext_Roots(t *testing.T) {
	tmpDir := copyFixture(t, "thread_context_roots")
	eng := createEngine(t)
	ws := loadWorkspace(t, eng, tmpDir)

	plan, err := eng.ThreadContext(ws, types.ThreadContextRequest{
		FunctionName: "seed",
		SourceFile:   filepath.Join(tmpDir, "main.go"),
	})
	if err != nil {
		t.Fatalf("ThreadContext: %v", err)
	}
	if err := eng.ExecutePlan(plan); err != nil {
		t.Fatalf("ExecutePlan: %v", err)
	}
	compareGoldenFiles(t, "thread_context_roots", tmpDir)
}

func TestThreadContext_FakeRoot(t *testing.T) {
	tmpDir := copyFixture(t, "thread_context_fake_root")
	eng := createEngine(t)
	ws := loadWorkspace(t, eng, tmpDir)

	plan, err := eng.ThreadContext(ws, types.ThreadContextRequest{
		FunctionName: "target",
		SourceFile:   filepath.Join(tmpDir, "main.go"),
	})
	if err != nil {
		t.Fatalf("ThreadContext: %v", err)
	}
	if err := eng.ExecutePlan(plan); err != nil {
		t.Fatalf("ExecutePlan: %v", err)
	}
	compareGoldenFiles(t, "thread_context_fake_root", tmpDir)
}

func TestThreadContext_Nested(t *testing.T) {
	tmpDir := copyFixture(t, "thread_context_nested")
	eng := createEngine(t)
	ws := loadWorkspace(t, eng, tmpDir)

	plan, err := eng.ThreadContext(ws, types.ThreadContextRequest{
		FunctionName: "Helper",
		SourceFile:   filepath.Join(tmpDir, "main.go"),
	})
	if err != nil {
		t.Fatalf("ThreadContext: %v", err)
	}
	if err := eng.ExecutePlan(plan); err != nil {
		t.Fatalf("ExecutePlan: %v", err)
	}
	compareGoldenFiles(t, "thread_context_nested", tmpDir)
}

func TestThreadContext_Generics(t *testing.T) {
	tmpDir := copyFixture(t, "thread_context_generics")
	eng := createEngine(t)
	ws := loadWorkspace(t, eng, tmpDir)

	plan, err := eng.ThreadContext(ws, types.ThreadContextRequest{
		FunctionName: "Seed",
		SourceFile:   filepath.Join(tmpDir, "main.go"),
	})
	if err != nil {
		t.Fatalf("ThreadContext: %v", err)
	}
	if err := eng.ExecutePlan(plan); err != nil {
		t.Fatalf("ExecutePlan: %v", err)
	}
	compareGoldenFiles(t, "thread_context_generics", tmpDir)
}

// TestThreadContext_AbortsOnValueUse asserts that threading a function that
// is referenced as a value (not called) makes NO changes to disk at all: a
// half-threaded workspace would not compile, so the abort must happen before
// any file is touched.
func TestThreadContext_AbortsOnValueUse(t *testing.T) {
	tmpDir := copyFixture(t, "thread_context_value_use")
	eng := createEngine(t)
	ws := loadWorkspace(t, eng, tmpDir)

	before, err := os.ReadFile(filepath.Join(tmpDir, "main.go"))
	if err != nil {
		t.Fatalf("reading fixture before ThreadContext: %v", err)
	}

	_, err = eng.ThreadContext(ws, types.ThreadContextRequest{
		FunctionName: "Seed",
		SourceFile:   filepath.Join(tmpDir, "main.go"),
	})
	if err == nil {
		t.Fatalf("expected ThreadContext to abort, got nil error")
	}
	if !strings.Contains(err.Error(), "Seed") || !strings.Contains(err.Error(), "used as a value") {
		t.Fatalf("error must name the offending value use, got: %v", err)
	}

	after, err := os.ReadFile(filepath.Join(tmpDir, "main.go"))
	if err != nil {
		t.Fatalf("reading fixture after ThreadContext: %v", err)
	}
	if string(before) != string(after) {
		t.Fatalf("expected zero files changed after an abort, but main.go differs:\n%s", unifiedDiff(string(before), string(after)))
	}
}

// TestThreadContext_AbortsOnExternalInterface asserts that threading a
// method satisfying an interface declared OUTSIDE the workspace (here
// net/http.Handler) makes NO changes to disk: adding a ctx parameter would
// silently break that satisfaction, and it cannot be repaired by rewriting
// anything this workspace owns.
func TestThreadContext_AbortsOnExternalInterface(t *testing.T) {
	tmpDir := copyFixture(t, "thread_context_external_iface")
	eng := createEngine(t)
	ws := loadWorkspace(t, eng, tmpDir)

	before, err := os.ReadFile(filepath.Join(tmpDir, "main.go"))
	if err != nil {
		t.Fatalf("reading fixture before ThreadContext: %v", err)
	}

	_, err = eng.ThreadContext(ws, types.ThreadContextRequest{
		FunctionName: "H.ServeHTTP",
		SourceFile:   filepath.Join(tmpDir, "main.go"),
	})
	if err == nil {
		t.Fatalf("expected ThreadContext to abort, got nil error")
	}
	if !strings.Contains(err.Error(), "net/http.Handler") {
		t.Fatalf("error must name the external interface by package path, got: %v", err)
	}

	after, err := os.ReadFile(filepath.Join(tmpDir, "main.go"))
	if err != nil {
		t.Fatalf("reading fixture after ThreadContext: %v", err)
	}
	if string(before) != string(after) {
		t.Fatalf("expected zero files changed after an abort, but main.go differs:\n%s", unifiedDiff(string(before), string(after)))
	}
}

// TestThreadContext_Compiles is the highest-value test in this file:
// createEngine sets SkipCompilation: true, so every golden test above
// compares text but never proves the result actually compiles. This test
// applies the refactor for a representative sample of fixtures and then
// runs `go build ./...` and `go vet ./...` in the temp module directory —
// two independent, non-engine checks that the rendered output is real,
// compiling Go.
func TestThreadContext_Compiles(t *testing.T) {
	cases := []struct {
		fixture      string
		functionName string
		sourceRel    string
	}{
		{"thread_context_chain", "c", "c.go"},
		{"thread_context_interface", "MemStore.Save", "main.go"},
		{"thread_context_test_roots", "Seed", "lib.go"},
		{"thread_context_generics", "Seed", "main.go"},
		{"thread_context_nested", "Helper", "main.go"},
	}

	for _, tc := range cases {
		t.Run(tc.fixture, func(t *testing.T) {
			tmpDir := copyFixture(t, tc.fixture)
			eng := createEngine(t)
			ws := loadWorkspace(t, eng, tmpDir)

			plan, err := eng.ThreadContext(ws, types.ThreadContextRequest{
				FunctionName: tc.functionName,
				SourceFile:   filepath.Join(tmpDir, tc.sourceRel),
			})
			if err != nil {
				t.Fatalf("ThreadContext: %v", err)
			}
			if err := eng.ExecutePlan(plan); err != nil {
				t.Fatalf("ExecutePlan: %v", err)
			}

			buildCmd := exec.Command("go", "build", "./...")
			buildCmd.Dir = tmpDir
			if out, err := buildCmd.CombinedOutput(); err != nil {
				t.Fatalf("go build ./... failed after ThreadContext:\n%s", out)
			}

			// go vet also type-checks _test.go files, which go build does
			// not compile at all. That makes vet the only check here that
			// catches a _test.go left referencing an import that was never
			// added — the exact failure findParsedFile exists to prevent.
			vetCmd := exec.Command("go", "vet", "./...")
			vetCmd.Dir = tmpDir
			out, err := vetCmd.CombinedOutput()
			if err != nil {
				t.Fatalf("go vet ./... failed after ThreadContext:\n%s", out)
			}
		})
	}
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

	rr, err := analyzers.Run(ws, ifinit.Analyzer, "")
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	changes := analyzers.DiagnosticsToChanges(ws.FileSet, rr.Diagnostics)
	plan := analyzers.ChangesToPlan(changes)
	if err := eng.ExecutePlan(plan); err != nil {
		t.Fatalf("ExecutePlan: %v", err)
	}
	compareGoldenFiles(t, "fix_if_init", tmpDir)
}

// runFixIfInit copies fixtureDir, type-checks it (the ifinit analyzer's
// collision-downgrade and type-incompatible-skip decisions read
// pass.TypesInfo, and loadWorkspace alone does not type-check), runs the
// ifinit analyzer, and applies every resulting fix. It returns the temp
// dir so callers can compare golden files and/or verify compilation.
func runFixIfInit(t *testing.T, fixtureDir string) string {
	t.Helper()
	tmpDir := copyFixture(t, fixtureDir)
	eng := createEngine(t)
	ws := loadWorkspace(t, eng, tmpDir)
	eng.EnsureTypeChecked(ws)

	rr, err := analyzers.Run(ws, ifinit.Analyzer, "")
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	changes := analyzers.DiagnosticsToChanges(ws.FileSet, rr.Diagnostics)
	plan := analyzers.ChangesToPlan(changes)
	if err := eng.ExecutePlan(plan); err != nil {
		t.Fatalf("ExecutePlan: %v", err)
	}
	return tmpDir
}

// TestFixIfInitElseIf covers de-sugaring of `else if init; cond { ... }`
// into a nested block: a single else-if with no trailing else, and a
// chain of two else-ifs (each with its own init) ending in a plain else.
func TestFixIfInitElseIf(t *testing.T) {
	tmpDir := runFixIfInit(t, "fix_if_init_elseif")
	compareGoldenFiles(t, "fix_if_init_elseif", tmpDir)
}

// TestFixIfInitCollision covers the ':=' -> '=' downgrade when every
// non-blank LHS name in the hoisted init already exists in the landing
// block (siblings, chained siblings, a partial collision that must stay
// ':=', and a collision against a function parameter).
func TestFixIfInitCollision(t *testing.T) {
	tmpDir := runFixIfInit(t, "fix_if_init_collision")
	compareGoldenFiles(t, "fix_if_init_collision", tmpDir)
}

// TestFixIfInitSwitch covers switch-init hoisting: a tagged switch, a
// nil-tag switch, and a type switch (where only Init is hoisted, never
// Assign).
func TestFixIfInitSwitch(t *testing.T) {
	tmpDir := runFixIfInit(t, "fix_if_init_switch")
	compareGoldenFiles(t, "fix_if_init_switch", tmpDir)
}

// TestFixIfInitSkip covers sites that must never be hoisted: a for-loop
// init, an if that is the direct Stmt of a LabeledStmt, and a collision
// where the RHS type is not assignable to the pre-existing name's type.
// The golden file is byte-identical to the input.
func TestFixIfInitSkip(t *testing.T) {
	tmpDir := runFixIfInit(t, "fix_if_init_skip")
	compareGoldenFiles(t, "fix_if_init_skip", tmpDir)
}

// TestFixIfInit_Compiles is the ifinit analogue of
// TestThreadContext_Compiles: createEngine sets SkipCompilation: true, so
// every golden test above compares text but never proves the result
// actually compiles. This test applies the ifinit fix for every ifinit
// fixture and then runs `go build ./...` in the temp module directory —
// an independent, non-engine check that the rendered output is real,
// compiling Go.
func TestFixIfInit_Compiles(t *testing.T) {
	if _, err := exec.LookPath("go"); err != nil {
		t.Skip("go binary not found on PATH")
	}

	fixtures := []string{
		"fix_if_init",
		"fix_if_init_elseif",
		"fix_if_init_collision",
		"fix_if_init_switch",
		"fix_if_init_skip",
	}

	for _, fixture := range fixtures {
		t.Run(fixture, func(t *testing.T) {
			tmpDir := runFixIfInit(t, fixture)

			buildCmd := exec.Command("go", "build", "./...")
			buildCmd.Dir = tmpDir
			if out, err := buildCmd.CombinedOutput(); err != nil {
				t.Fatalf("go build ./... failed after ifinit fix:\n%s", out)
			}

			vetCmd := exec.Command("go", "vet", "./...")
			vetCmd.Dir = tmpDir
			if out, err := vetCmd.CombinedOutput(); err != nil {
				t.Fatalf("go vet ./... failed after ifinit fix:\n%s", out)
			}
		})
	}
}

func TestFixBooleanBranching(t *testing.T) {
	tmpDir := copyFixture(t, "fix_boolean_branching")
	eng := createEngine(t)
	ws := loadWorkspace(t, eng, tmpDir)

	rr, err := analyzers.Run(ws, booleanbranch.Analyzer, "")
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	changes := analyzers.DiagnosticsToChanges(ws.FileSet, rr.Diagnostics)
	plan := analyzers.ChangesToPlan(changes)
	if err := eng.ExecutePlan(plan); err != nil {
		t.Fatalf("ExecutePlan: %v", err)
	}
	compareGoldenFiles(t, "fix_boolean_branching", tmpDir)
}

func TestFixDeepIfElse(t *testing.T) {
	tmpDir := copyFixture(t, "fix_deep_if_else")
	eng := createEngine(t)
	ws := loadWorkspace(t, eng, tmpDir)

	a := deepifelse.NewAnalyzer(deepifelse.WithMaxNesting(2), deepifelse.WithMinElseLines(3))
	rr, err := analyzers.Run(ws, a, "")
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	changes := analyzers.DiagnosticsToChanges(ws.FileSet, rr.Diagnostics)
	plan := analyzers.ChangesToPlan(changes)
	if err := eng.ExecutePlan(plan); err != nil {
		t.Fatalf("ExecutePlan: %v", err)
	}
	compareGoldenFiles(t, "fix_deep_if_else", tmpDir)
}

// TestFixDeepIfElseChain reproduces the real-world shape that motivated
// flattening else-if chains at all: five levels deep, using `else if`, with
// two `if x, ok := ...; ok` headers along the way. deepifelse alone cannot
// hoist those inits - that is fix_if_init_assignments' job - so this fixture
// pins the partial result: flattened to depth 2, both `; ok` headers left
// intact.
func TestFixDeepIfElseChain(t *testing.T) {
	tmpDir := copyFixture(t, "fix_deep_if_else_chain")
	eng := createEngine(t)
	ws := loadWorkspace(t, eng, tmpDir)

	rr, err := analyzers.Run(ws, deepifelse.Analyzer, "")
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	changes := analyzers.DiagnosticsToChanges(ws.FileSet, rr.Diagnostics)
	plan := analyzers.ChangesToPlan(changes)
	if err := eng.ExecutePlan(plan); err != nil {
		t.Fatalf("ExecutePlan: %v", err)
	}
	compareGoldenFiles(t, "fix_deep_if_else_chain", tmpDir)
}

// TestFixSwitchableIfChains covers the golden path for fix_switchable_if_chains:
// the tagged, tagless and type-switch chains all convert, the chain
// fix_deep_if_else_chains already owns is left untouched, and the chain with a
// later-link init produces no rewrite at all (its SuggestedFix is never
// attached, so it yields no Change and no diff to apply).
func TestFixSwitchableIfChains(t *testing.T) {
	tmpDir := copyFixture(t, "switch_chains")
	eng := createEngine(t)
	ws := loadWorkspace(t, eng, tmpDir)

	rr, err := analyzers.Run(ws, switchchain.Analyzer, "")
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	changes := analyzers.DiagnosticsToChanges(ws.FileSet, rr.Diagnostics)
	plan := analyzers.ChangesToPlan(changes)
	if err := eng.ExecutePlan(plan); err != nil {
		t.Fatalf("ExecutePlan: %v", err)
	}
	compareGoldenFiles(t, "switch_chains", tmpDir)
}

func TestFixErrorWrapping(t *testing.T) {
	tmpDir := copyFixture(t, "fix_error_wrapping")
	eng := createEngine(t)
	ws := loadWorkspace(t, eng, tmpDir)

	a := errorwrap.NewAnalyzer(errorwrap.WithSeverity(errorwrap.SeverityCritical))
	rr, err := analyzers.Run(ws, a, "")
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	changes := analyzers.DiagnosticsToChanges(ws.FileSet, rr.Diagnostics)
	plan := analyzers.ChangesToPlan(changes)
	if err := eng.ExecutePlan(plan); err != nil {
		t.Fatalf("ExecutePlan: %v", err)
	}
	compareGoldenFiles(t, "fix_error_wrapping", tmpDir)
}
