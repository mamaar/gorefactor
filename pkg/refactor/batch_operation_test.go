package refactor

import (
	"testing"

	"github.com/mamaar/gorefactor/pkg/types"
)

// Test cases for BatchOperation when it gets implemented

func TestBatchOperation_Type(t *testing.T) {
	moveOp := &MoveSymbolOperation{
		Request: types.MoveSymbolRequest{
			SymbolName:  "TestSymbol",
			FromPackage: "source",
			ToPackage:   "target",
		},
	}
	renameOp := &RenameSymbolOperation{
		Request: types.RenameSymbolRequest{
			SymbolName: "OldName",
			NewName:    "NewName",
		},
	}

	batchOp := &BatchOperation{
		Operations: []types.Operation{moveOp, renameOp},
		Name:       "Combined Move and Rename",
	}

	if batchOp.Type() != types.BatchOperation {
		t.Errorf("Expected BatchOperation, got %v", batchOp.Type())
	}
}

func TestBatchOperation_Validate(t *testing.T) {
	tests := []struct {
		name    string
		op      *BatchOperation
		wantErr bool
	}{
		{
			name: "valid batch operation with multiple operations",
			op: &BatchOperation{
				Operations: []types.Operation{
					&MockBatchOperationItem{opType: types.MoveOperation},
					&MockBatchOperationItem{opType: types.RenameOperation},
				},
				Name: "Move and Rename Batch",
			},
			wantErr: false,
		},
		{
			name: "invalid - empty operations list",
			op: &BatchOperation{
				Operations: []types.Operation{},
				Name:       "Empty Batch",
			},
			wantErr: true,
		},
		{
			name: "invalid - nil operations list",
			op: &BatchOperation{
				Operations: nil,
				Name:       "Nil Batch",
			},
			wantErr: true,
		},
		{
			name: "invalid - empty name",
			op: &BatchOperation{
				Operations: []types.Operation{
					&MockBatchOperationItem{opType: types.MoveOperation},
				},
				Name: "",
			},
			wantErr: true,
		},
		{
			name: "invalid - operation validation fails",
			op: &BatchOperation{
				Operations: []types.Operation{
					&MockBatchOperationItem{opType: types.MoveOperation, shouldFailValidation: true},
				},
				Name: "Batch with Invalid Operation",
			},
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ws := createTestWorkspaceForBatch()
			err := tt.op.Validate(ws)
			if (err != nil) != tt.wantErr {
				t.Errorf("BatchOperation.Validate() error = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}

func TestBatchOperation_Description(t *testing.T) {
	batchOp := &BatchOperation{
		Operations: []types.Operation{
			&MockBatchOperationItem{opType: types.MoveOperation},
			&MockBatchOperationItem{opType: types.RenameOperation},
		},
		Name: "Combined Refactoring",
	}

	desc := batchOp.Description()
	expectedDesc := "Batch operation 'Combined Refactoring' with 2 operations: [MoveOperation RenameOperation]"
	if desc != expectedDesc {
		t.Errorf("Expected description '%s', got '%s'", expectedDesc, desc)
	}
}

func TestBatchOperation_Execute(t *testing.T) {
	tests := []struct {
		name    string
		op      *BatchOperation
		wantErr bool
	}{
		{
			name: "successful batch execution",
			op: &BatchOperation{
				Operations: []types.Operation{
					&MockBatchOperationItem{opType: types.MoveOperation},
					&MockBatchOperationItem{opType: types.RenameOperation},
				},
				Name: "Successful Batch",
			},
			wantErr: false,
		},
		{
			name: "batch execution with one failing operation",
			op: &BatchOperation{
				Operations: []types.Operation{
					&MockBatchOperationItem{opType: types.MoveOperation},
					&MockBatchOperationItem{opType: types.RenameOperation, shouldFailExecution: true},
				},
				Name: "Failing Batch",
			},
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ws := createTestWorkspaceForBatch()
			plan, err := tt.op.Execute(ws)
			if (err != nil) != tt.wantErr {
				t.Errorf("BatchOperation.Execute() error = %v, wantErr %v", err, tt.wantErr)
			}
			if !tt.wantErr && plan == nil {
				t.Error("Expected Execute to return a plan when successful")
			}
		})
	}
}

func TestBatchOperation_ConflictDetection(t *testing.T) {
	// Test batch operation with conflicting changes
	batchOp := &BatchOperation{
		Operations: []types.Operation{
			&MockBatchOperationItem{
				opType: types.MoveOperation,
				changes: []types.Change{
					{File: "test.go", Start: 10, End: 20},
				},
			},
			&MockBatchOperationItem{
				opType: types.RenameOperation,
				changes: []types.Change{
					{File: "test.go", Start: 15, End: 25}, // Overlapping with first operation
				},
			},
		},
		Name: "Conflicting Batch",
	}

	ws := createTestWorkspaceForBatch()
	_, err := batchOp.Execute(ws)
	if err == nil {
		t.Error("Expected error due to conflicting changes")
	}
}

func TestBatchOperation_TransactionalBehavior(t *testing.T) {
	// Test that batch operations are atomic - if any operation fails, none are applied
	batchOp := &BatchOperation{
		Operations: []types.Operation{
			&MockBatchOperationItem{opType: types.MoveOperation},
			&MockBatchOperationItem{opType: types.RenameOperation, shouldFailExecution: true},
			&MockBatchOperationItem{opType: types.ExtractOperation},
		},
		Name: "Atomic Batch Test",
		Atomic: true,
	}

	ws := createTestWorkspaceForBatch()
	_, err := batchOp.Execute(ws)
	if err == nil {
		t.Error("Expected error due to failed operation in atomic batch")
	}

	// Verify error type indicates rollback
	if refErr, ok := err.(*types.RefactorError); ok {
		if refErr.Type != types.InvalidOperation {
			t.Errorf("Expected InvalidOperation error type, got %v", refErr.Type)
		}
	} else {
		t.Error("Expected RefactorError")
	}
}

func TestBatchOperation_ImpactAnalysis(t *testing.T) {
	batchOp := &BatchOperation{
		Operations: []types.Operation{
			&MockBatchOperationItem{
				opType: types.MoveOperation,
				affectedFiles: []string{"file1.go", "file2.go"},
			},
			&MockBatchOperationItem{
				opType: types.RenameOperation,
				affectedFiles: []string{"file2.go", "file3.go"},
			},
		},
		Name: "Impact Analysis Test",
	}

	ws := createTestWorkspaceForBatch()
	plan, err := batchOp.Execute(ws)
	if err != nil {
		t.Fatalf("Unexpected error: %v", err)
	}

	// Check that impact analysis combines all affected files
	if plan.Impact == nil {
		t.Fatal("Expected impact analysis to be present")
	}

	expectedFiles := []string{"file1.go", "file2.go", "file3.go"}
	if len(plan.Impact.AffectedFiles) != len(expectedFiles) {
		t.Errorf("Expected %d affected files, got %d", len(expectedFiles), len(plan.Impact.AffectedFiles))
	}

	// Verify all expected files are present (order might vary)
	fileMap := make(map[string]bool)
	for _, file := range plan.Impact.AffectedFiles {
		fileMap[file] = true
	}
	for _, expected := range expectedFiles {
		if !fileMap[expected] {
			t.Errorf("Expected file %s to be in affected files", expected)
		}
	}
}

// Note: BatchOperation implementation is now in batch_operations.go

// Mock operation for testing batch operations
type MockBatchOperationItem struct {
	opType                 types.OperationType
	shouldFailValidation   bool
	shouldFailExecution    bool
	changes                []types.Change
	affectedFiles          []string
}

func (m *MockBatchOperationItem) Type() types.OperationType {
	return m.opType
}

func (m *MockBatchOperationItem) Validate(ws *types.Workspace) error {
	if m.shouldFailValidation {
		return &types.RefactorError{
			Type:    types.InvalidOperation,
			Message: "mock validation failure",
		}
	}
	return nil
}

func (m *MockBatchOperationItem) Execute(ws *types.Workspace) (*types.RefactoringPlan, error) {
	if m.shouldFailExecution {
		return nil, &types.RefactorError{
			Type:    types.InvalidOperation,
			Message: "mock execution failure",
		}
	}

	changes := m.changes
	if changes == nil {
		// Create non-overlapping changes based on operation type
		var startPos, endPos int
		var fileName string
		switch m.opType {
		case types.MoveOperation:
			startPos, endPos = 0, 10
			fileName = "move_mock.go"
		case types.RenameOperation:
			startPos, endPos = 20, 30
			fileName = "rename_mock.go"
		case types.ExtractOperation:
			startPos, endPos = 40, 50
			fileName = "extract_mock.go"
		default:
			startPos, endPos = 0, 10
			fileName = "mock.go"
		}
		
		changes = []types.Change{
			{
				File:        fileName,
				Start:       startPos,
				End:         endPos,
				OldText:     "old",
				NewText:     "new",
				Description: "mock change",
			},
		}
	}

	affectedFiles := m.affectedFiles
	if affectedFiles == nil {
		// Use the same file as the change
		if len(changes) > 0 {
			affectedFiles = []string{changes[0].File}
		} else {
			affectedFiles = []string{"mock.go"}
		}
	}

	return &types.RefactoringPlan{
		Operations:    []types.Operation{m},
		Changes:       changes,
		AffectedFiles: affectedFiles,
		Impact: &types.ImpactAnalysis{
			AffectedFiles: affectedFiles,
		},
		Reversible: true,
	}, nil
}

func (m *MockBatchOperationItem) Description() string {
	return "Mock " + m.operationTypeToString() + " operation"
}

func (m *MockBatchOperationItem) operationTypeToString() string {
	switch m.opType {
	case types.MoveOperation:
		return "Move"
	case types.RenameOperation:
		return "Rename"
	case types.ExtractOperation:
		return "Extract"
	case types.InlineOperation:
		return "Inline"
	default:
		return "Unknown"
	}
}

// Helper functions for testing
func createTestWorkspaceForBatch() *types.Workspace {
	return &types.Workspace{
		RootPath: "/test",
		Packages: make(map[string]*types.Package),
	}
}

// removeDuplicateStrings is now defined in batch_operations.go