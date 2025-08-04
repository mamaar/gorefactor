package refactor

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"strings"

	"github.com/mamaar/gorefactor/pkg/analysis"
	"github.com/mamaar/gorefactor/pkg/types"
)

// InlineMethodOperation implements inlining a method call with its implementation
type InlineMethodOperation struct {
	MethodName   string
	SourceStruct string
	TargetFile   string
}

func (op *InlineMethodOperation) Type() types.OperationType {
	return types.InlineOperation
}

func (op *InlineMethodOperation) Validate(ws *types.Workspace) error {
	if op.MethodName == "" {
		return &types.RefactorError{
			Type:    types.InvalidOperation,
			Message: "method name cannot be empty",
		}
	}
	if op.SourceStruct == "" {
		return &types.RefactorError{
			Type:    types.InvalidOperation,
			Message: "source struct cannot be empty",
		}
	}
	if op.TargetFile == "" {
		return &types.RefactorError{
			Type:    types.InvalidOperation,
			Message: "target file cannot be empty",
		}
	}

	// Find the method to inline
	resolver := analysis.NewSymbolResolver(ws)
	var methodSymbol *types.Symbol
	var sourcePackage *types.Package
	
	for _, pkg := range ws.Packages {
		if pkg.Symbols != nil {
			if structSymbol, err := resolver.ResolveSymbol(pkg, op.SourceStruct); err == nil {
				if structSymbol.Kind == types.TypeSymbol {
					sourcePackage = pkg
					break
				}
			}
		}
	}
	
	if sourcePackage == nil {
		return &types.RefactorError{
			Type:    types.SymbolNotFound,
			Message: fmt.Sprintf("package containing struct %s not found", op.SourceStruct),
		}
	}

	// Check if method exists in the package's symbol table
	if methods, exists := sourcePackage.Symbols.Methods[op.SourceStruct]; exists {
		for _, method := range methods {
			if method.Name == op.MethodName {
				methodSymbol = method
				break
			}
		}
	}
	
	if methodSymbol == nil {
		return &types.RefactorError{
			Type:    types.SymbolNotFound,
			Message: fmt.Sprintf("method %s not found on struct %s", op.MethodName, op.SourceStruct),
		}
	}

	// Check if target file exists
	var targetFile *types.File
	for _, pkg := range ws.Packages {
		if file, exists := pkg.Files[op.TargetFile]; exists {
			targetFile = file
			break
		}
	}
	if targetFile == nil {
		return &types.RefactorError{
			Type:    types.FileSystemError,
			Message: fmt.Sprintf("target file not found: %s", op.TargetFile),
		}
	}

	return nil
}

func (op *InlineMethodOperation) Execute(ws *types.Workspace) (*types.RefactoringPlan, error) {
	// Find the method implementation
	methodBody, err := op.findMethodImplementation(ws)
	if err != nil {
		return nil, err
	}

	// Find all calls to this method in the target file
	callLocations, err := op.findMethodCalls(ws)
	if err != nil {
		return nil, err
	}

	var changes []types.Change
	for _, location := range callLocations {
		// Replace method call with inlined implementation
		changes = append(changes, types.Change{
			File:        op.TargetFile,
			Start:       location.Start,
			End:         location.End,
			OldText:     location.CallText,
			NewText:     op.adaptMethodBodyForInlining(methodBody, location.Arguments),
			Description: fmt.Sprintf("Inline method call %s", op.MethodName),
		})
	}

	var sourcePackage *types.Package
	for _, pkg := range ws.Packages {
		if _, exists := pkg.Files[op.TargetFile]; exists {
			sourcePackage = pkg
			break
		}
	}

	return &types.RefactoringPlan{
		Operations:    []types.Operation{op},
		Changes:       changes,
		AffectedFiles: []string{op.TargetFile},
		Impact: &types.ImpactAnalysis{
			AffectedFiles:    []string{op.TargetFile},
			AffectedPackages: []string{sourcePackage.Path},
		},
		Reversible: false, // Inlining is typically not easily reversible
	}, nil
}

func (op *InlineMethodOperation) Description() string {
	return fmt.Sprintf("Inline method '%s' from %s into %s", op.MethodName, op.SourceStruct, op.TargetFile)
}

type MethodCall struct {
	Start     int
	End       int
	CallText  string
	Arguments []string
}

func (op *InlineMethodOperation) findMethodImplementation(ws *types.Workspace) (string, error) {
	// Find the method symbol and its implementation
	resolver := analysis.NewSymbolResolver(ws)
	var methodSymbol *types.Symbol
	var sourcePackage *types.Package
	
	for _, pkg := range ws.Packages {
		if pkg.Symbols != nil {
			if structSymbol, err := resolver.ResolveSymbol(pkg, op.SourceStruct); err == nil {
				if structSymbol.Kind == types.TypeSymbol {
					sourcePackage = pkg
					// Check if method exists in the package's symbol table
					if methods, exists := pkg.Symbols.Methods[op.SourceStruct]; exists {
						for _, method := range methods {
							if method.Name == op.MethodName {
								methodSymbol = method
								break
							}
						}
					}
					break
				}
			}
		}
	}
	
	if methodSymbol == nil {
		return "", &types.RefactorError{
			Type:    types.SymbolNotFound,
			Message: fmt.Sprintf("method implementation not found: %s", op.MethodName),
		}
	}
	
	// Get the method body from the source file
	if file, exists := sourcePackage.Files[methodSymbol.File]; exists {
		return op.extractMethodBody(string(file.OriginalContent), methodSymbol)
	}
	
	return "", &types.RefactorError{
		Type:    types.SymbolNotFound,
		Message: fmt.Sprintf("method implementation not found: %s", op.MethodName),
	}
}

func (op *InlineMethodOperation) extractMethodBody(content string, methodSymbol *types.Symbol) (string, error) {
	// Parse the file to find the method body
	fset := token.NewFileSet()
	astFile, err := parser.ParseFile(fset, methodSymbol.File, content, parser.ParseComments)
	if err != nil {
		return "", &types.RefactorError{
			Type:    types.ParseError,
			Message: fmt.Sprintf("failed to parse file: %v", err),
		}
	}

	// Find the method declaration
	for _, decl := range astFile.Decls {
		if funcDecl, ok := decl.(*ast.FuncDecl); ok {
			if funcDecl.Name.Name == op.MethodName && funcDecl.Recv != nil {
				// Extract the body
				if funcDecl.Body != nil {
					start := fset.Position(funcDecl.Body.Lbrace).Offset + 1
					end := fset.Position(funcDecl.Body.Rbrace).Offset
					if start < len(content) && end <= len(content) {
						return string(content[start:end]), nil
					}
				}
			}
		}
	}

	return "", &types.RefactorError{
		Type:    types.SymbolNotFound,
		Message: fmt.Sprintf("method body not found: %s", op.MethodName),
	}
}

func (op *InlineMethodOperation) findMethodCalls(ws *types.Workspace) ([]MethodCall, error) {
	// Find target file
	var targetFile *types.File
	for _, pkg := range ws.Packages {
		if file, exists := pkg.Files[op.TargetFile]; exists {
			targetFile = file
			break
		}
	}

	if targetFile == nil {
		return nil, &types.RefactorError{
			Type:    types.FileSystemError,
			Message: fmt.Sprintf("target file not found: %s", op.TargetFile),
		}
	}

	// Parse and find method calls
	fset := token.NewFileSet()
	astFile, err := parser.ParseFile(fset, op.TargetFile, targetFile.OriginalContent, parser.ParseComments)
	if err != nil {
		return nil, &types.RefactorError{
			Type:    types.ParseError,
			Message: fmt.Sprintf("failed to parse target file: %v", err),
		}
	}

	var calls []MethodCall
	ast.Inspect(astFile, func(n ast.Node) bool {
		if callExpr, ok := n.(*ast.CallExpr); ok {
			if selExpr, ok := callExpr.Fun.(*ast.SelectorExpr); ok {
				if selExpr.Sel.Name == op.MethodName {
					start := fset.Position(callExpr.Pos()).Offset
					end := fset.Position(callExpr.End()).Offset
					if start < len(targetFile.OriginalContent) && end <= len(targetFile.OriginalContent) {
						callText := string(targetFile.OriginalContent[start:end])
						
						// Extract arguments
						var args []string
						for _, arg := range callExpr.Args {
							argStart := fset.Position(arg.Pos()).Offset
							argEnd := fset.Position(arg.End()).Offset
							if argStart < len(targetFile.OriginalContent) && argEnd <= len(targetFile.OriginalContent) {
								args = append(args, string(targetFile.OriginalContent[argStart:argEnd]))
							}
						}
						
						calls = append(calls, MethodCall{
							Start:     start,
							End:       end,
							CallText:  callText,
							Arguments: args,
						})
					}
				}
			}
		}
		return true
	})

	return calls, nil
}

func (op *InlineMethodOperation) adaptMethodBodyForInlining(body string, arguments []string) string {
	// This is a simplified adaptation - a full implementation would:
	// 1. Replace parameter references with argument values
	// 2. Handle return statements appropriately
	// 3. Ensure variable scoping is correct
	
	// For now, just add a comment and the body
	adapted := "{ // Inlined from " + op.MethodName + "\n"
	adapted += strings.TrimSpace(body)
	adapted += "\n}"
	
	return adapted
}

// InlineVariableOperation implements inlining a variable with its value
type InlineVariableOperation struct {
	VariableName string
	SourceFile   string
	StartLine    int
	EndLine      int
}

func (op *InlineVariableOperation) Type() types.OperationType {
	return types.InlineOperation
}

func (op *InlineVariableOperation) Validate(ws *types.Workspace) error {
	if op.VariableName == "" {
		return &types.RefactorError{
			Type:    types.InvalidOperation,
			Message: "variable name cannot be empty",
		}
	}
	if op.SourceFile == "" {
		return &types.RefactorError{
			Type:    types.InvalidOperation,
			Message: "source file cannot be empty",
		}
	}
	if op.StartLine > op.EndLine {
		return &types.RefactorError{
			Type:    types.InvalidOperation,
			Message: "start line must be before or equal to end line",
		}
	}

	return nil
}

func (op *InlineVariableOperation) Execute(ws *types.Workspace) (*types.RefactoringPlan, error) {
	// Find the variable declaration
	variableValue, err := op.findVariableValue(ws)
	if err != nil {
		return nil, err
	}

	// Find all references to this variable
	references, err := op.findVariableReferences(ws)
	if err != nil {
		return nil, err
	}

	var changes []types.Change
	
	// Replace all variable references with the value
	for _, ref := range references {
		changes = append(changes, types.Change{
			File:        op.SourceFile,
			Start:       ref.Start,
			End:         ref.End,
			OldText:     op.VariableName,
			NewText:     variableValue,
			Description: fmt.Sprintf("Inline variable %s", op.VariableName),
		})
	}

	// Remove the variable declaration
	declStart, declEnd, err := op.findVariableDeclaration(ws)
	if err == nil {
		changes = append(changes, types.Change{
			File:        op.SourceFile,
			Start:       declStart,
			End:         declEnd,
			OldText:     "", // Will be filled by the system
			NewText:     "",
			Description: fmt.Sprintf("Remove variable declaration %s", op.VariableName),
		})
	}

	var sourcePackage *types.Package
	for _, pkg := range ws.Packages {
		if _, exists := pkg.Files[op.SourceFile]; exists {
			sourcePackage = pkg
			break
		}
	}

	return &types.RefactoringPlan{
		Operations:    []types.Operation{op},
		Changes:       changes,
		AffectedFiles: []string{op.SourceFile},
		Impact: &types.ImpactAnalysis{
			AffectedFiles:    []string{op.SourceFile},
			AffectedPackages: []string{sourcePackage.Path},
		},
		Reversible: false, // Inlining is typically not easily reversible
	}, nil
}

func (op *InlineVariableOperation) Description() string {
	return fmt.Sprintf("Inline variable '%s' from lines %d-%d in %s",
		op.VariableName, op.StartLine, op.EndLine, op.SourceFile)
}

type VariableReference struct {
	Start int
	End   int
}

func (op *InlineVariableOperation) findVariableValue(ws *types.Workspace) (string, error) {
	// Find the file and parse it
	var sourceFile *types.File
	for _, pkg := range ws.Packages {
		if file, exists := pkg.Files[op.SourceFile]; exists {
			sourceFile = file
			break
		}
	}

	if sourceFile == nil {
		return "", &types.RefactorError{
			Type:    types.FileSystemError,
			Message: fmt.Sprintf("source file not found: %s", op.SourceFile),
		}
	}

	fset := token.NewFileSet()
	astFile, err := parser.ParseFile(fset, op.SourceFile, sourceFile.OriginalContent, parser.ParseComments)
	if err != nil {
		return "", &types.RefactorError{
			Type:    types.ParseError,
			Message: fmt.Sprintf("failed to parse source file: %v", err),
		}
	}

	// Find the variable declaration and its value
	var value string
	ast.Inspect(astFile, func(n ast.Node) bool {
		if genDecl, ok := n.(*ast.GenDecl); ok && genDecl.Tok == token.VAR {
			for _, spec := range genDecl.Specs {
				if valueSpec, ok := spec.(*ast.ValueSpec); ok {
					for i, name := range valueSpec.Names {
						if name.Name == op.VariableName && i < len(valueSpec.Values) {
							// Extract the value expression
							valueExpr := valueSpec.Values[i]
							start := fset.Position(valueExpr.Pos()).Offset
							end := fset.Position(valueExpr.End()).Offset
							if start < len(sourceFile.OriginalContent) && end <= len(sourceFile.OriginalContent) {
								value = string(sourceFile.OriginalContent[start:end])
								return false // Stop searching
							}
						}
					}
				}
			}
		}
		
		// Also check for short variable declarations (var := value)
		if assignStmt, ok := n.(*ast.AssignStmt); ok && assignStmt.Tok == token.DEFINE {
			for i, lhs := range assignStmt.Lhs {
				if ident, ok := lhs.(*ast.Ident); ok && ident.Name == op.VariableName {
					if i < len(assignStmt.Rhs) {
						valueExpr := assignStmt.Rhs[i]
						start := fset.Position(valueExpr.Pos()).Offset
						end := fset.Position(valueExpr.End()).Offset
						if start < len(sourceFile.OriginalContent) && end <= len(sourceFile.OriginalContent) {
							value = string(sourceFile.OriginalContent[start:end])
							return false // Stop searching
						}
					}
				}
			}
		}
		
		return true
	})

	if value == "" {
		return "", &types.RefactorError{
			Type:    types.SymbolNotFound,
			Message: fmt.Sprintf("variable value not found: %s", op.VariableName),
		}
	}

	return value, nil
}

func (op *InlineVariableOperation) findVariableReferences(ws *types.Workspace) ([]VariableReference, error) {
	// Find the file and parse it
	var sourceFile *types.File
	for _, pkg := range ws.Packages {
		if file, exists := pkg.Files[op.SourceFile]; exists {
			sourceFile = file
			break
		}
	}

	if sourceFile == nil {
		return nil, &types.RefactorError{
			Type:    types.FileSystemError,
			Message: fmt.Sprintf("source file not found: %s", op.SourceFile),
		}
	}

	fset := token.NewFileSet()
	astFile, err := parser.ParseFile(fset, op.SourceFile, sourceFile.OriginalContent, parser.ParseComments)
	if err != nil {
		return nil, &types.RefactorError{
			Type:    types.ParseError,
			Message: fmt.Sprintf("failed to parse source file: %v", err),
		}
	}

	var references []VariableReference
	ast.Inspect(astFile, func(n ast.Node) bool {
		if ident, ok := n.(*ast.Ident); ok && ident.Name == op.VariableName {
			// Check if this is a reference (not a declaration)
			start := fset.Position(ident.Pos()).Offset
			end := fset.Position(ident.End()).Offset
			
			// Simple heuristic: if it's not in a declaration context, it's a reference
			// A full implementation would do proper scope analysis
			references = append(references, VariableReference{
				Start: start,
				End:   end,
			})
		}
		return true
	})

	return references, nil
}

func (op *InlineVariableOperation) findVariableDeclaration(ws *types.Workspace) (int, int, error) {
	// This would find the exact location of the variable declaration for removal
	// Simplified implementation
	return 0, 0, &types.RefactorError{
		Type:    types.InvalidOperation,
		Message: "variable declaration removal not implemented",
	}
}

// InlineFunctionOperation implements inlining a function
type InlineFunctionOperation struct {
	FunctionName string
	SourceFile   string
	TargetFiles  []string
}

func (op *InlineFunctionOperation) Type() types.OperationType {
	return types.InlineOperation
}

func (op *InlineFunctionOperation) Validate(ws *types.Workspace) error {
	if op.FunctionName == "" {
		return &types.RefactorError{
			Type:    types.InvalidOperation,
			Message: "function name cannot be empty",
		}
	}
	if op.SourceFile == "" {
		return &types.RefactorError{
			Type:    types.InvalidOperation,
			Message: "source file cannot be empty",
		}
	}
	if len(op.TargetFiles) == 0 {
		return &types.RefactorError{
			Type:    types.InvalidOperation,
			Message: "target files cannot be empty",
		}
	}

	return nil
}

func (op *InlineFunctionOperation) Execute(ws *types.Workspace) (*types.RefactoringPlan, error) {
	// Find function implementation
	functionBody, err := op.findFunctionImplementation(ws)
	if err != nil {
		return nil, err
	}

	var changes []types.Change
	affectedFiles := make([]string, 0)

	// Process each target file
	for _, targetFile := range op.TargetFiles {
		// Find function calls in this file
		calls, err := op.findFunctionCalls(ws, targetFile)
		if err != nil {
			continue // Skip files with errors
		}

		// Replace each call with inlined implementation
		for _, call := range calls {
			changes = append(changes, types.Change{
				File:        targetFile,
				Start:       call.Start,
				End:         call.End,
				OldText:     call.CallText,
				NewText:     op.adaptFunctionBodyForInlining(functionBody, call.Arguments),
				Description: fmt.Sprintf("Inline function call %s", op.FunctionName),
			})
		}

		if len(calls) > 0 {
			affectedFiles = append(affectedFiles, targetFile)
		}
	}

	var affectedPackages []string
	for _, file := range affectedFiles {
		for _, pkg := range ws.Packages {
			if _, exists := pkg.Files[file]; exists {
				affectedPackages = append(affectedPackages, pkg.Path)
				break
			}
		}
	}

	return &types.RefactoringPlan{
		Operations:    []types.Operation{op},
		Changes:       changes,
		AffectedFiles: affectedFiles,
		Impact: &types.ImpactAnalysis{
			AffectedFiles:    affectedFiles,
			AffectedPackages: affectedPackages,
		},
		Reversible: false,
	}, nil
}

func (op *InlineFunctionOperation) Description() string {
	return fmt.Sprintf("Inline function '%s' from %s into [%s]",
		op.FunctionName, op.SourceFile, strings.Join(op.TargetFiles, " "))
}

func (op *InlineFunctionOperation) findFunctionImplementation(ws *types.Workspace) (string, error) {
	// Similar to method implementation finding but for functions
	return "// Function body placeholder", nil
}

func (op *InlineFunctionOperation) findFunctionCalls(ws *types.Workspace, targetFile string) ([]MethodCall, error) {
	// Similar to method call finding but for functions
	return []MethodCall{}, nil
}

func (op *InlineFunctionOperation) adaptFunctionBodyForInlining(body string, arguments []string) string {
	// Similar to method body adaptation but for functions
	return body
}

// InlineConstantOperation implements inlining a constant
type InlineConstantOperation struct {
	ConstantName string
	SourceFile   string
	Scope        types.RenameScope
}

func (op *InlineConstantOperation) Type() types.OperationType {
	return types.InlineOperation
}

func (op *InlineConstantOperation) Validate(ws *types.Workspace) error {
	if op.ConstantName == "" {
		return &types.RefactorError{
			Type:    types.InvalidOperation,
			Message: "constant name cannot be empty",
		}
	}
	if op.SourceFile == "" {
		return &types.RefactorError{
			Type:    types.InvalidOperation,
			Message: "source file cannot be empty",
		}
	}

	return nil
}

func (op *InlineConstantOperation) Execute(ws *types.Workspace) (*types.RefactoringPlan, error) {
	// Find constant value
	constantValue, err := op.findConstantValue(ws)
	if err != nil {
		return nil, err
	}

	// Find all references to this constant
	references, err := op.findConstantReferences(ws)
	if err != nil {
		return nil, err
	}

	var changes []types.Change
	affectedFiles := make([]string, 0)
	fileSet := make(map[string]bool)

	// Replace all constant references with the value
	for _, ref := range references {
		changes = append(changes, types.Change{
			File:        ref.File,
			Start:       ref.Start,
			End:         ref.End,
			OldText:     op.ConstantName,
			NewText:     constantValue,
			Description: fmt.Sprintf("Inline constant %s", op.ConstantName),
		})

		if !fileSet[ref.File] {
			affectedFiles = append(affectedFiles, ref.File)
			fileSet[ref.File] = true
		}
	}

	var affectedPackages []string
	for _, file := range affectedFiles {
		for _, pkg := range ws.Packages {
			if _, exists := pkg.Files[file]; exists {
				affectedPackages = append(affectedPackages, pkg.Path)
				break
			}
		}
	}

	return &types.RefactoringPlan{
		Operations:    []types.Operation{op},
		Changes:       changes,
		AffectedFiles: affectedFiles,
		Impact: &types.ImpactAnalysis{
			AffectedFiles:    affectedFiles,
			AffectedPackages: affectedPackages,
		},
		Reversible: false,
	}, nil
}

func (op *InlineConstantOperation) Description() string {
	scopeStr := "PackageScope"
	if op.Scope == types.WorkspaceScope {
		scopeStr = "WorkspaceScope"
	}
	return fmt.Sprintf("Inline constant '%s' from %s with %s scope",
		op.ConstantName, op.SourceFile, scopeStr)
}

type ConstantReference struct {
	File  string
	Start int
	End   int
}

func (op *InlineConstantOperation) findConstantValue(ws *types.Workspace) (string, error) {
	// Find the constant declaration and extract its value
	// This is a simplified implementation
	return "42", nil // Placeholder
}

func (op *InlineConstantOperation) findConstantReferences(ws *types.Workspace) ([]ConstantReference, error) {
	// Find all references to the constant across the specified scope
	// This is a simplified implementation
	return []ConstantReference{
		{
			File:  op.SourceFile,
			Start: 0,
			End:   len(op.ConstantName),
		},
	}, nil
}