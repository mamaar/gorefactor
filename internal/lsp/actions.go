package lsp

import (
	"fmt"
	"go/token"

	"github.com/mamaar/gorefactor/pkg/refactor"
	"github.com/mamaar/gorefactor/pkg/types"
)

// generateExtractActions creates code actions for extract refactorings
func (s *Server) generateExtractActions(filePath string, selectedRange Range) []CodeAction {
	var actions []CodeAction

	// Extract Method
	actions = append(actions, CodeAction{
		Title: "Extract Method",
		Kind:  "refactor.extract.method",
		Command: &Command{
			Title:   "Extract Method",
			Command: "gorefactor.extract.method",
			Arguments: []interface{}{
				filePath,
				selectedRange.Start.Line + 1,
				selectedRange.End.Line + 1,
			},
		},
	})

	// Extract Variable
	actions = append(actions, CodeAction{
		Title: "Extract Variable",
		Kind:  "refactor.extract.variable",
		Command: &Command{
			Title:   "Extract Variable",
			Command: "gorefactor.extract.variable",
			Arguments: []interface{}{
				filePath,
				selectedRange.Start.Line + 1,
				selectedRange.End.Line + 1,
			},
		},
	})

	// Extract Constant
	actions = append(actions, CodeAction{
		Title: "Extract Constant",
		Kind:  "refactor.extract.constant",
		Command: &Command{
			Title:   "Extract Constant",
			Command: "gorefactor.extract.constant",
			Arguments: []interface{}{
				filePath,
				selectedRange.Start.Line + 1,
				selectedRange.Start.Character + 1,
			},
		},
	})

	// Extract Function
	actions = append(actions, CodeAction{
		Title: "Extract Function",
		Kind:  "refactor.extract.function",
		Command: &Command{
			Title:   "Extract Function",
			Command: "gorefactor.extract.function",
			Arguments: []interface{}{
				filePath,
				selectedRange.Start.Line + 1,
				selectedRange.End.Line + 1,
			},
		},
	})

	// Extract Block (auto-detect boundaries)
	actions = append(actions, CodeAction{
		Title: "Extract Block as Function",
		Kind:  "refactor.extract.block",
		Command: &Command{
			Title:   "Extract Block",
			Command: "gorefactor.extract.block",
			Arguments: []interface{}{
				filePath,
				selectedRange.Start.Line + 1,
			},
		},
	})

	// Extract Interface (if we're on a struct)
	if s.isStructAtPosition(filePath, selectedRange.Start) {
		actions = append(actions, CodeAction{
			Title: "Extract Interface",
			Kind:  "refactor.extract.interface",
			Command: &Command{
				Title:   "Extract Interface",
				Command: "gorefactor.extract.interface",
				Arguments: []interface{}{
					filePath,
					selectedRange.Start.Line + 1,
				},
			},
		})
	}

	return actions
}

// generateInlineActions creates code actions for inline refactorings
func (s *Server) generateInlineActions(filePath string, selectedRange Range) []CodeAction {
	var actions []CodeAction

	// Check what symbol is at the position
	symbol, err := s.findSymbolAtPosition(filePath, selectedRange.Start)
	if err != nil || symbol == nil {
		return actions
	}

	switch symbol.Kind {
	case types.MethodSymbol:
		actions = append(actions, CodeAction{
			Title: "Inline Method",
			Kind:  "refactor.inline.method",
			Command: &Command{
				Title:   "Inline Method",
				Command: "gorefactor.inline.method",
				Arguments: []interface{}{
					symbol.Name,
					filePath,
				},
			},
		})

	case types.FunctionSymbol:
		actions = append(actions, CodeAction{
			Title: "Inline Function",
			Kind:  "refactor.inline.function",
			Command: &Command{
				Title:   "Inline Function",
				Command: "gorefactor.inline.function",
				Arguments: []interface{}{
					symbol.Name,
					filePath,
				},
			},
		})

	case types.VariableSymbol:
		actions = append(actions, CodeAction{
			Title: "Inline Variable",
			Kind:  "refactor.inline.variable",
			Command: &Command{
				Title:   "Inline Variable",
				Command: "gorefactor.inline.variable",
				Arguments: []interface{}{
					symbol.Name,
					filePath,
				},
			},
		})

	case types.ConstantSymbol:
		actions = append(actions, CodeAction{
			Title: "Inline Constant",
			Kind:  "refactor.inline.constant",
			Command: &Command{
				Title:   "Inline Constant",
				Command: "gorefactor.inline.constant",
				Arguments: []interface{}{
					symbol.Name,
					filePath,
				},
			},
		})
	}

	return actions
}

// generateMoveActions creates code actions for move refactorings
func (s *Server) generateMoveActions(filePath string, selectedRange Range) []CodeAction {
	var actions []CodeAction

	// Check what symbol is at the position
	symbol, err := s.findSymbolAtPosition(filePath, selectedRange.Start)
	if err != nil || symbol == nil {
		return actions
	}

	// Only offer move for moveable symbols
	if s.isSymbolMoveable(symbol) {
		actions = append(actions, CodeAction{
			Title: fmt.Sprintf("Move %s", symbol.Name),
			Kind:  "refactor.move",
			Command: &Command{
				Title:   "Move Symbol",
				Command: "gorefactor.move.symbol",
				Arguments: []interface{}{
					symbol.Name,
					symbol.Package,
				},
			},
		})
	}

	return actions
}

// generateRenameActions creates code actions for rename refactorings
func (s *Server) generateRenameActions(filePath string, selectedRange Range) []CodeAction {
	var actions []CodeAction

	// Check what symbol is at the position
	symbol, err := s.findSymbolAtPosition(filePath, selectedRange.Start)
	if err != nil || symbol == nil {
		return actions
	}

	// Rename is available for most symbols
	actions = append(actions, CodeAction{
		Title: fmt.Sprintf("Rename %s", symbol.Name),
		Kind:  "refactor.rename",
		Command: &Command{
			Title:   "Rename Symbol",
			Command: "gorefactor.rename.symbol",
			Arguments: []interface{}{
				symbol.Name,
				filePath,
			},
		},
	})

	// Change signature for functions
	if symbol.Kind == types.FunctionSymbol || symbol.Kind == types.MethodSymbol {
		actions = append(actions, CodeAction{
			Title: fmt.Sprintf("Change Signature of %s", symbol.Name),
			Kind:  "refactor.rewrite.signature",
			Command: &Command{
				Title:   "Change Signature",
				Command: "gorefactor.change.signature",
				Arguments: []interface{}{
					symbol.Name,
					filePath,
				},
			},
		})
	}

	// Safe delete
	actions = append(actions, CodeAction{
		Title: fmt.Sprintf("Safe Delete %s", symbol.Name),
		Kind:  "refactor.safe.delete",
		Command: &Command{
			Title:   "Safe Delete",
			Command: "gorefactor.safe.delete",
			Arguments: []interface{}{
				symbol.Name,
				filePath,
			},
		},
	})

	// Batch operations
	actions = append(actions, CodeAction{
		Title: "Create Batch Operation",
		Kind:  "refactor.batch",
		Command: &Command{
			Title:   "Batch Operations",
			Command: "gorefactor.batch.create",
			Arguments: []interface{}{
				filePath,
				selectedRange.Start.Line + 1,
			},
		},
	})

	return actions
}

// Helper methods

func (s *Server) isStructAtPosition(filePath string, position Position) bool {
	symbol, err := s.findSymbolAtPosition(filePath, position)
	if err != nil || symbol == nil {
		return false
	}
	return symbol.Kind == types.TypeSymbol
}

func (s *Server) isSymbolMoveable(symbol *types.Symbol) bool {
	switch symbol.Kind {
	case types.FunctionSymbol, types.TypeSymbol, types.ConstantSymbol, types.VariableSymbol:
		return true
	case types.MethodSymbol:
		return false // Methods can't be moved independently
	default:
		return false
	}
}

// executeCodeAction executes a code action command
func (s *Server) executeCodeAction(command string, args []interface{}) (*types.RefactoringPlan, error) {
	switch command {
	case "gorefactor.extract.method":
		return s.executeExtractMethod(args)
	case "gorefactor.extract.function":
		return s.executeExtractFunction(args)
	case "gorefactor.extract.variable":
		return s.executeExtractVariable(args)
	case "gorefactor.extract.constant":
		return s.executeExtractConstant(args)
	case "gorefactor.extract.interface":
		return s.executeExtractInterface(args)
	case "gorefactor.extract.block":
		return s.executeExtractBlock(args)
	case "gorefactor.inline.method":
		return s.executeInlineMethod(args)
	case "gorefactor.inline.function":
		return s.executeInlineFunction(args)
	case "gorefactor.inline.variable":
		return s.executeInlineVariable(args)
	case "gorefactor.inline.constant":
		return s.executeInlineConstant(args)
	case "gorefactor.move.symbol":
		return s.executeMoveSymbol(args)
	case "gorefactor.rename.symbol":
		return s.executeRenameSymbol(args)
	case "gorefactor.change.signature":
		return s.executeChangeSignature(args)
	case "gorefactor.safe.delete":
		return s.executeSafeDelete(args)
	case "gorefactor.batch.create":
		return s.executeBatchOperation(args)
	default:
		return nil, fmt.Errorf("unknown command: %s", command)
	}
}

// Code action execution methods

func (s *Server) executeExtractMethod(args []interface{}) (*types.RefactoringPlan, error) {
	if len(args) < 3 {
		return nil, fmt.Errorf("extract method requires 3 arguments")
	}

	filePath := args[0].(string)
	startLine := int(args[1].(float64))
	endLine := int(args[2].(float64))

	operation := &refactor.ExtractMethodOperation{
		SourceFile:    filePath,
		StartLine:     startLine,
		EndLine:       endLine,
		NewMethodName: "extractedMethod", // Could be made configurable
		TargetStruct:  "self",            // Simplified
	}

	return operation.Execute(s.workspace)
}

func (s *Server) executeExtractVariable(args []interface{}) (*types.RefactoringPlan, error) {
	if len(args) < 3 {
		return nil, fmt.Errorf("extract variable requires 3 arguments")
	}

	filePath := args[0].(string)
	startLine := int(args[1].(float64))
	endLine := int(args[2].(float64))

	operation := &refactor.ExtractVariableOperation{
		SourceFile:   filePath,
		StartLine:    startLine,
		EndLine:      endLine,
		VariableName: "extractedVar",       // Could be made configurable
		Expression:   "selectedExpression", // Would need to be extracted from range
	}

	return operation.Execute(s.workspace)
}

func (s *Server) executeExtractConstant(args []interface{}) (*types.RefactoringPlan, error) {
	if len(args) < 3 {
		return nil, fmt.Errorf("extract constant requires 3 arguments")
	}

	filePath := args[0].(string)
	line := int(args[1].(float64))
	character := int(args[2].(float64))

	operation := &refactor.ExtractConstantOperation{
		SourceFile:   filePath,
		Position:     token.Pos(line*100 + character), // Simplified position calculation
		ConstantName: "ExtractedConstant",             // Could be made configurable
		Scope:        types.PackageScope,
	}

	return operation.Execute(s.workspace)
}

func (s *Server) executeExtractInterface(args []interface{}) (*types.RefactoringPlan, error) {
	if len(args) < 2 {
		return nil, fmt.Errorf("extract interface requires 2 arguments")
	}

	filePath := args[0].(string)
	line := int(args[1].(float64))

	// Find struct at position
	position := Position{Line: line - 1, Character: 0}
	symbol, err := s.findSymbolAtPosition(filePath, position)
	if err != nil || symbol == nil {
		return nil, fmt.Errorf("no struct found at position")
	}

	// Get methods for the struct (simplified - would need proper method discovery)
	methods := []string{"Method1", "Method2"} // Placeholder

	operation := &refactor.ExtractInterfaceOperation{
		SourceStruct:  symbol.Name,
		InterfaceName: symbol.Name + "Interface",
		Methods:       methods,
		TargetPackage: "interfaces",
	}

	return operation.Execute(s.workspace)
}

func (s *Server) executeInlineMethod(args []interface{}) (*types.RefactoringPlan, error) {
	if len(args) < 2 {
		return nil, fmt.Errorf("inline method requires 2 arguments")
	}

	methodName := args[0].(string)
	filePath := args[1].(string)

	operation := &refactor.InlineMethodOperation{
		MethodName:   methodName,
		SourceStruct: "targetStruct", // Would need to be determined
		TargetFile:   filePath,
	}

	return operation.Execute(s.workspace)
}

func (s *Server) executeInlineFunction(args []interface{}) (*types.RefactoringPlan, error) {
	if len(args) < 2 {
		return nil, fmt.Errorf("inline function requires 2 arguments")
	}

	functionName := args[0].(string)
	filePath := args[1].(string)

	operation := &refactor.InlineFunctionOperation{
		FunctionName: functionName,
		SourceFile:   filePath,
		TargetFiles:  []string{filePath}, // Simplified
	}

	return operation.Execute(s.workspace)
}

func (s *Server) executeInlineVariable(args []interface{}) (*types.RefactoringPlan, error) {
	if len(args) < 2 {
		return nil, fmt.Errorf("inline variable requires 2 arguments")
	}

	variableName := args[0].(string)
	filePath := args[1].(string)

	operation := &refactor.InlineVariableOperation{
		VariableName: variableName,
		SourceFile:   filePath,
		StartLine:    1, // Would need to be determined
		EndLine:      1, // Would need to be determined
	}

	return operation.Execute(s.workspace)
}

func (s *Server) executeInlineConstant(args []interface{}) (*types.RefactoringPlan, error) {
	if len(args) < 2 {
		return nil, fmt.Errorf("inline constant requires 2 arguments")
	}

	constantName := args[0].(string)
	filePath := args[1].(string)

	operation := &refactor.InlineConstantOperation{
		ConstantName: constantName,
		SourceFile:   filePath,
	}

	return operation.Execute(s.workspace)
}

func (s *Server) executeMoveSymbol(args []interface{}) (*types.RefactoringPlan, error) {
	if len(args) < 2 {
		return nil, fmt.Errorf("move symbol requires 2 arguments")
	}

	symbolName := args[0].(string)
	fromPackage := args[1].(string)

	operation := &refactor.MoveSymbolOperation{
		Request: types.MoveSymbolRequest{
			SymbolName:   symbolName,
			FromPackage:  fromPackage,
			ToPackage:    "target", // Would need to be provided by user
			CreateTarget: true,
		},
	}

	return operation.Execute(s.workspace)
}

func (s *Server) executeRenameSymbol(args []interface{}) (*types.RefactoringPlan, error) {
	if len(args) < 2 {
		return nil, fmt.Errorf("rename symbol requires 2 arguments")
	}

	symbolName := args[0].(string)
	_ = args[1].(string) // filePath - unused in current implementation

	operation := &refactor.RenameSymbolOperation{
		Request: types.RenameSymbolRequest{
			SymbolName: symbolName,
			NewName:    "newName", // Would need to be provided by user
			Scope:      types.WorkspaceScope,
		},
	}

	return operation.Execute(s.workspace)
}

func (s *Server) executeChangeSignature(args []interface{}) (*types.RefactoringPlan, error) {
	if len(args) < 2 {
		return nil, fmt.Errorf("change signature requires 2 arguments")
	}

	functionName := args[0].(string)
	filePath := args[1].(string)

	operation := &refactor.ChangeSignatureOperation{
		FunctionName: functionName,
		SourceFile:   filePath,
		NewParams:    []refactor.Parameter{}, // Would need to be provided by user
		NewReturns:   []string{},             // Would need to be provided by user
		Scope:        types.WorkspaceScope,
	}

	return operation.Execute(s.workspace)
}

func (s *Server) executeSafeDelete(args []interface{}) (*types.RefactoringPlan, error) {
	if len(args) < 2 {
		return nil, fmt.Errorf("safe delete requires 2 arguments")
	}

	symbolName := args[0].(string)
	filePath := args[1].(string)

	operation := &refactor.SafeDeleteOperation{
		SymbolName: symbolName,
		SourceFile: filePath,
		Scope:      types.WorkspaceScope,
		Force:      false,
	}

	return operation.Execute(s.workspace)
}

func (s *Server) executeExtractFunction(args []interface{}) (*types.RefactoringPlan, error) {
	if len(args) < 3 {
		return nil, fmt.Errorf("extract function requires 3 arguments")
	}

	filePath := args[0].(string)
	startLine := int(args[1].(float64))
	endLine := int(args[2].(float64))

	operation := &refactor.ExtractFunctionOperation{
		SourceFile:      filePath,
		StartLine:       startLine,
		EndLine:         endLine,
		NewFunctionName: "extractedFunction", // Could be made configurable
	}

	return operation.Execute(s.workspace)
}

func (s *Server) executeExtractBlock(args []interface{}) (*types.RefactoringPlan, error) {
	if len(args) < 2 {
		return nil, fmt.Errorf("extract block requires 2 arguments")
	}

	filePath := args[0].(string)
	position := int(args[1].(float64))

	operation := &refactor.ExtractBlockOperation{
		SourceFile:      filePath,
		Position:        position,
		NewFunctionName: "extractedBlock", // Could be made configurable
	}

	return operation.Execute(s.workspace)
}

func (s *Server) executeBatchOperation(args []interface{}) (*types.RefactoringPlan, error) {
	if len(args) < 2 {
		return nil, fmt.Errorf("batch operation requires 2 arguments")
	}

	filePath := args[0].(string)
	_ = args[1] // position - could be used to determine context

	// Create a simple batch operation - in practice, this would be more sophisticated
	// For now, create a batch that extracts a method and renames it
	operation := refactor.NewBatchOperation("LSP Batch Operation").
		AddExtractMethodOperation(filePath, 1, 5, "extractedMethod", "self").
		SetAtomic(true).
		Build()

	return operation.Execute(s.workspace)
}
