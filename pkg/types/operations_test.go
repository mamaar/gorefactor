package types

import (
	"testing"
)

func TestOperationType(t *testing.T) {
	testCases := []struct {
		name     string
		opType   OperationType
		expected OperationType
	}{
		{"MoveOperation", MoveOperation, 0},
		{"RenameOperation", RenameOperation, 1},
		{"RenamePackageOperation", RenamePackageOperation, 2},
		{"RenameInterfaceMethodOperation", RenameInterfaceMethodOperation, 3},
		{"ExtractOperation", ExtractOperation, 4},
		{"InlineOperation", InlineOperation, 5},
		{"BatchOperation", BatchOperation, 6},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			if tc.opType != tc.expected {
				t.Errorf("Expected %s to be %d, got %d", tc.name, tc.expected, tc.opType)
			}
		})
	}
}

func TestMoveSymbolRequest(t *testing.T) {
	req := MoveSymbolRequest{
		SymbolName:   "TestSymbol",
		FromPackage:  "source/package",
		ToPackage:    "target/package",
		CreateTarget: true,
		UpdateTests:  true,
	}

	if req.SymbolName != "TestSymbol" {
		t.Errorf("Expected SymbolName to be 'TestSymbol', got '%s'", req.SymbolName)
	}

	if req.FromPackage != "source/package" {
		t.Errorf("Expected FromPackage to be 'source/package', got '%s'", req.FromPackage)
	}

	if req.ToPackage != "target/package" {
		t.Errorf("Expected ToPackage to be 'target/package', got '%s'", req.ToPackage)
	}

	if !req.CreateTarget {
		t.Error("Expected CreateTarget to be true")
	}

	if !req.UpdateTests {
		t.Error("Expected UpdateTests to be true")
	}
}

func TestRenameSymbolRequest(t *testing.T) {
	req := RenameSymbolRequest{
		SymbolName: "OldName",
		NewName:    "NewName",
		Package:    "test/package",
		Scope:      WorkspaceScope,
	}

	if req.SymbolName != "OldName" {
		t.Errorf("Expected SymbolName to be 'OldName', got '%s'", req.SymbolName)
	}

	if req.NewName != "NewName" {
		t.Errorf("Expected NewName to be 'NewName', got '%s'", req.NewName)
	}

	if req.Package != "test/package" {
		t.Errorf("Expected Package to be 'test/package', got '%s'", req.Package)
	}

	if req.Scope != WorkspaceScope {
		t.Errorf("Expected Scope to be WorkspaceScope, got %v", req.Scope)
	}
}

func TestRenamePackageRequest(t *testing.T) {
	req := RenamePackageRequest{
		OldPackageName: "oldpkg",
		NewPackageName: "newpkg",
		PackagePath:    "pkg/oldpkg",
		UpdateImports:  true,
	}

	if req.OldPackageName != "oldpkg" {
		t.Errorf("Expected OldPackageName to be 'oldpkg', got '%s'", req.OldPackageName)
	}

	if req.NewPackageName != "newpkg" {
		t.Errorf("Expected NewPackageName to be 'newpkg', got '%s'", req.NewPackageName)
	}

	if req.PackagePath != "pkg/oldpkg" {
		t.Errorf("Expected PackagePath to be 'pkg/oldpkg', got '%s'", req.PackagePath)
	}

	if !req.UpdateImports {
		t.Error("Expected UpdateImports to be true")
	}
}

func TestRenameInterfaceMethodRequest(t *testing.T) {
	req := RenameInterfaceMethodRequest{
		InterfaceName:         "CommandBus",
		MethodName:            "Execute",
		NewMethodName:         "Process",
		PackagePath:           "pkg/bus",
		UpdateImplementations: true,
	}

	if req.InterfaceName != "CommandBus" {
		t.Errorf("Expected InterfaceName to be 'CommandBus', got '%s'", req.InterfaceName)
	}

	if req.MethodName != "Execute" {
		t.Errorf("Expected MethodName to be 'Execute', got '%s'", req.MethodName)
	}

	if req.NewMethodName != "Process" {
		t.Errorf("Expected NewMethodName to be 'Process', got '%s'", req.NewMethodName)
	}

	if req.PackagePath != "pkg/bus" {
		t.Errorf("Expected PackagePath to be 'pkg/bus', got '%s'", req.PackagePath)
	}

	if !req.UpdateImplementations {
		t.Error("Expected UpdateImplementations to be true")
	}
}

func TestRenameScope(t *testing.T) {
	testCases := []struct {
		name     string
		scope    RenameScope
		expected RenameScope
	}{
		{"PackageScope", PackageScope, 0},
		{"WorkspaceScope", WorkspaceScope, 1},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			if tc.scope != tc.expected {
				t.Errorf("Expected %s to be %d, got %d", tc.name, tc.expected, tc.scope)
			}
		})
	}
}

func TestRefactoringPlan(t *testing.T) {
	plan := &RefactoringPlan{
		Operations:    make([]Operation, 0),
		Changes:       make([]Change, 0),
		AffectedFiles: []string{"/test/file1.go", "/test/file2.go"},
		Impact:        &ImpactAnalysis{},
		Reversible:    true,
	}

	if len(plan.Operations) != 0 {
		t.Errorf("Expected 0 operations, got %d", len(plan.Operations))
	}

	if len(plan.Changes) != 0 {
		t.Errorf("Expected 0 changes, got %d", len(plan.Changes))
	}

	if len(plan.AffectedFiles) != 2 {
		t.Errorf("Expected 2 affected files, got %d", len(plan.AffectedFiles))
	}

	if !plan.Reversible {
		t.Error("Expected plan to be reversible")
	}

	if plan.Impact == nil {
		t.Error("Expected Impact to be initialized")
	}
}

func TestChange(t *testing.T) {
	change := Change{
		File:        "/test/file.go",
		Start:       100,
		End:         150,
		OldText:     "old content",
		NewText:     "new content",
		Description: "Replace old content with new content",
	}

	if change.File != "/test/file.go" {
		t.Errorf("Expected File to be '/test/file.go', got '%s'", change.File)
	}

	if change.Start != 100 {
		t.Errorf("Expected Start to be 100, got %d", change.Start)
	}

	if change.End != 150 {
		t.Errorf("Expected End to be 150, got %d", change.End)
	}

	if change.OldText != "old content" {
		t.Errorf("Expected OldText to be 'old content', got '%s'", change.OldText)
	}

	if change.NewText != "new content" {
		t.Errorf("Expected NewText to be 'new content', got '%s'", change.NewText)
	}

	if change.Description != "Replace old content with new content" {
		t.Errorf("Expected Description to be 'Replace old content with new content', got '%s'", change.Description)
	}
}

func TestImpactAnalysis(t *testing.T) {
	symbol := &Symbol{Name: "TestSymbol", Kind: FunctionSymbol}
	impact := &ImpactAnalysis{
		AffectedPackages: []string{"pkg1", "pkg2"},
		AffectedFiles:    []string{"/test/file1.go", "/test/file2.go"},
		AffectedSymbols:  []*Symbol{symbol},
		PotentialIssues:  make([]Issue, 0),
		ImportChanges:    make([]ImportChange, 0),
	}

	if len(impact.AffectedPackages) != 2 {
		t.Errorf("Expected 2 affected packages, got %d", len(impact.AffectedPackages))
	}

	if len(impact.AffectedFiles) != 2 {
		t.Errorf("Expected 2 affected files, got %d", len(impact.AffectedFiles))
	}

	if len(impact.AffectedSymbols) != 1 {
		t.Errorf("Expected 1 affected symbol, got %d", len(impact.AffectedSymbols))
	}

	if impact.AffectedSymbols[0].Name != "TestSymbol" {
		t.Errorf("Expected affected symbol name to be 'TestSymbol', got '%s'", impact.AffectedSymbols[0].Name)
	}
}

func TestIssue(t *testing.T) {
	issue := Issue{
		Type:        IssueCompilationError,
		Description: "Compilation will fail",
		File:        "/test/file.go",
		Line:        25,
		Severity:    Error,
	}

	if issue.Type != IssueCompilationError {
		t.Errorf("Expected Type to be IssueCompilationError, got %v", issue.Type)
	}

	if issue.Description != "Compilation will fail" {
		t.Errorf("Expected Description to be 'Compilation will fail', got '%s'", issue.Description)
	}

	if issue.File != "/test/file.go" {
		t.Errorf("Expected File to be '/test/file.go', got '%s'", issue.File)
	}

	if issue.Line != 25 {
		t.Errorf("Expected Line to be 25, got %d", issue.Line)
	}

	if issue.Severity != Error {
		t.Errorf("Expected Severity to be Error, got %v", issue.Severity)
	}
}

func TestIssueType(t *testing.T) {
	testCases := []struct {
		name     string
		issueType IssueType
		expected IssueType
	}{
		{"IssueCompilationError", IssueCompilationError, 0},
		{"IssueImportCycle", IssueImportCycle, 1},
		{"IssueVisibilityError", IssueVisibilityError, 2},
		{"IssueNameConflict", IssueNameConflict, 3},
		{"IssueTypeMismatch", IssueTypeMismatch, 4},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			if tc.issueType != tc.expected {
				t.Errorf("Expected %s to be %d, got %d", tc.name, tc.expected, tc.issueType)
			}
		})
	}
}

func TestIssueSeverity(t *testing.T) {
	testCases := []struct {
		name     string
		severity IssueSeverity
		expected IssueSeverity
	}{
		{"Error", Error, 0},
		{"Warning", Warning, 1},
		{"Info", Info, 2},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			if tc.severity != tc.expected {
				t.Errorf("Expected %s to be %d, got %d", tc.name, tc.expected, tc.severity)
			}
		})
	}
}

func TestImportChange(t *testing.T) {
	importChange := ImportChange{
		File:      "/test/file.go",
		OldImport: "old/package",
		NewImport: "new/package",
		Action:    UpdateImport,
	}

	if importChange.File != "/test/file.go" {
		t.Errorf("Expected File to be '/test/file.go', got '%s'", importChange.File)
	}

	if importChange.OldImport != "old/package" {
		t.Errorf("Expected OldImport to be 'old/package', got '%s'", importChange.OldImport)
	}

	if importChange.NewImport != "new/package" {
		t.Errorf("Expected NewImport to be 'new/package', got '%s'", importChange.NewImport)
	}

	if importChange.Action != UpdateImport {
		t.Errorf("Expected Action to be UpdateImport, got %v", importChange.Action)
	}
}

func TestImportAction(t *testing.T) {
	testCases := []struct {
		name     string
		action   ImportAction
		expected ImportAction
	}{
		{"AddImport", AddImport, 0},
		{"RemoveImport", RemoveImport, 1},
		{"UpdateImport", UpdateImport, 2},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			if tc.action != tc.expected {
				t.Errorf("Expected %s to be %d, got %d", tc.name, tc.expected, tc.action)
			}
		})
	}
}

// Mock implementation of Operation interface for testing
type MockOperation struct {
	opType      OperationType
	description string
}

func (m *MockOperation) Type() OperationType {
	return m.opType
}

func (m *MockOperation) Validate(ws *Workspace) error {
	return nil
}

func (m *MockOperation) Execute(ws *Workspace) (*RefactoringPlan, error) {
	return &RefactoringPlan{}, nil
}

func (m *MockOperation) Description() string {
	return m.description
}

func TestMockOperation(t *testing.T) {
	op := &MockOperation{
		opType:      MoveOperation,
		description: "Test move operation",
	}

	if op.Type() != MoveOperation {
		t.Errorf("Expected operation type to be MoveOperation, got %v", op.Type())
	}

	if op.Description() != "Test move operation" {
		t.Errorf("Expected description to be 'Test move operation', got '%s'", op.Description())
	}

	// Test interface methods
	if err := op.Validate(nil); err != nil {
		t.Errorf("Expected Validate to return nil, got %v", err)
	}

	plan, err := op.Execute(nil)
	if err != nil {
		t.Errorf("Expected Execute to return nil error, got %v", err)
	}

	if plan == nil {
		t.Error("Expected Execute to return a plan")
	}
}