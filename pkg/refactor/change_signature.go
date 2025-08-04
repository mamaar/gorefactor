package refactor

import (
	"fmt"
	"go/ast"
	"go/token"
	"strings"

	"github.com/mamaar/gorefactor/pkg/analysis"
	pkgtypes "github.com/mamaar/gorefactor/pkg/types"
)

// Parameter represents a function parameter
type Parameter struct {
	Name string
	Type string
}

// ChangeSignatureOperation implements changing function/method signatures
type ChangeSignatureOperation struct {
	FunctionName string
	SourceFile   string
	NewParams    []Parameter
	NewReturns   []string
	Scope        pkgtypes.RenameScope // PackageScope or WorkspaceScope
}

func (op *ChangeSignatureOperation) Type() pkgtypes.OperationType {
	return pkgtypes.ExtractOperation // Reuse extract operation type for now
}

func (op *ChangeSignatureOperation) Validate(ws *pkgtypes.Workspace) error {
	if op.FunctionName == "" {
		return &pkgtypes.RefactorError{
			Type:    pkgtypes.InvalidOperation,
			Message: "function name cannot be empty",
		}
	}
	if op.SourceFile == "" {
		return &pkgtypes.RefactorError{
			Type:    pkgtypes.InvalidOperation,
			Message: "source file cannot be empty",
		}
	}

	// Check if source file exists
	var sourceFile *pkgtypes.File
	for _, pkg := range ws.Packages {
		if file, exists := pkg.Files[op.SourceFile]; exists {
			sourceFile = file
			break
		}
	}
	if sourceFile == nil {
		return &pkgtypes.RefactorError{
			Type:    pkgtypes.FileSystemError,
			Message: fmt.Sprintf("source file not found: %s", op.SourceFile),
		}
	}

	// Find the function
	functionNode := op.findFunction(sourceFile, op.FunctionName)
	if functionNode == nil {
		return &pkgtypes.RefactorError{
			Type:    pkgtypes.SymbolNotFound,
			Message: fmt.Sprintf("function %s not found in %s", op.FunctionName, op.SourceFile),
		}
	}

	// Validate parameter names are valid Go identifiers
	for _, param := range op.NewParams {
		if param.Name != "" && !isValidGoIdentifierExtract(param.Name) {
			return &pkgtypes.RefactorError{
				Type:    pkgtypes.InvalidOperation,
				Message: fmt.Sprintf("invalid parameter name: %s", param.Name),
			}
		}
	}

	return nil
}

func (op *ChangeSignatureOperation) Execute(ws *pkgtypes.Workspace) (*pkgtypes.RefactoringPlan, error) {
	// Find the source file and function
	var sourceFile *pkgtypes.File
	var sourcePackage *pkgtypes.Package
	for _, pkg := range ws.Packages {
		if file, exists := pkg.Files[op.SourceFile]; exists {
			sourceFile = file
			sourcePackage = pkg
			break
		}
	}

	functionNode := op.findFunction(sourceFile, op.FunctionName)
	if functionNode == nil {
		return nil, &pkgtypes.RefactorError{
			Type:    pkgtypes.SymbolNotFound,
			Message: fmt.Sprintf("function %s not found", op.FunctionName),
		}
	}

	plan := &pkgtypes.RefactoringPlan{
		Changes:       make([]pkgtypes.Change, 0),
		AffectedFiles: make([]string, 0),
		Reversible:    true,
	}

	// Change the function declaration
	newSignature := op.generateNewSignature(functionNode)
	oldSignature := op.extractCurrentSignature(functionNode)

	plan.Changes = append(plan.Changes, pkgtypes.Change{
		File:        sourceFile.Path,
		Start:       int(functionNode.Type.Pos()),
		End:         int(functionNode.Type.End()),
		OldText:     oldSignature,
		NewText:     newSignature,
		Description: fmt.Sprintf("Update signature of function %s", op.FunctionName),
	})
	plan.AffectedFiles = append(plan.AffectedFiles, sourceFile.Path)

	// Find and update all call sites
	resolver := analysis.NewSymbolResolver(ws)
	symbol, err := resolver.ResolveSymbol(sourcePackage, op.FunctionName)
	if err != nil {
		return nil, err
	}

	references, err := resolver.FindReferences(symbol)
	if err != nil {
		return nil, err
	}

	// Determine which packages to search based on scope
	packagesToSearch := make(map[string]*pkgtypes.Package)
	if op.Scope == pkgtypes.PackageScope {
		packagesToSearch[sourcePackage.Path] = sourcePackage
	} else {
		packagesToSearch = ws.Packages
	}

	// Update call sites
	for _, ref := range references {
		// Check if this reference is in scope
		inScope := false
		for _, pkg := range packagesToSearch {
			if _, exists := pkg.Files[ref.File]; exists {
				inScope = true
				break
			}
		}
		if !inScope {
			continue
		}

		// Find the call expression containing this reference
		callChanges := op.updateCallSite(ref, ws)
		plan.Changes = append(plan.Changes, callChanges...)
		
		if !contains(plan.AffectedFiles, ref.File) {
			plan.AffectedFiles = append(plan.AffectedFiles, ref.File)
		}
	}

	// Perform impact analysis
	plan.Impact = &pkgtypes.ImpactAnalysis{
		AffectedFiles:    plan.AffectedFiles,
		AffectedPackages: op.getAffectedPackages(ws, plan.AffectedFiles),
		PotentialIssues:  op.analyzeImpact(ws, functionNode, len(references)),
	}

	return plan, nil
}

func (op *ChangeSignatureOperation) Description() string {
	scopeStr := "package"
	if op.Scope == pkgtypes.WorkspaceScope {
		scopeStr = "workspace"
	}
	return fmt.Sprintf("Change signature of function %s (scope: %s)", op.FunctionName, scopeStr)
}

// Helper methods

func (op *ChangeSignatureOperation) findFunction(file *pkgtypes.File, funcName string) *ast.FuncDecl {
	if file.AST == nil {
		return nil
	}

	var found *ast.FuncDecl
	ast.Inspect(file.AST, func(n ast.Node) bool {
		if funcDecl, ok := n.(*ast.FuncDecl); ok {
			if funcDecl.Name != nil && funcDecl.Name.Name == funcName {
				found = funcDecl
				return false
			}
		}
		return true
	})

	return found
}

func (op *ChangeSignatureOperation) generateNewSignature(funcDecl *ast.FuncDecl) string {
	var signature strings.Builder
	
	// Add receiver if this is a method
	if funcDecl.Recv != nil {
		signature.WriteString("(")
		// Keep existing receiver unchanged
		for i, field := range funcDecl.Recv.List {
			if i > 0 {
				signature.WriteString(", ")
			}
			if len(field.Names) > 0 {
				signature.WriteString(field.Names[0].Name)
				signature.WriteString(" ")
			}
			signature.WriteString(op.extractTypeString(field.Type))
		}
		signature.WriteString(") ")
	}

	// Add function name
	signature.WriteString(funcDecl.Name.Name)

	// Add parameters
	signature.WriteString("(")
	for i, param := range op.NewParams {
		if i > 0 {
			signature.WriteString(", ")
		}
		if param.Name != "" {
			signature.WriteString(param.Name)
			signature.WriteString(" ")
		}
		signature.WriteString(param.Type)
	}
	signature.WriteString(")")

	// Add return types
	if len(op.NewReturns) > 0 {
		signature.WriteString(" ")
		if len(op.NewReturns) == 1 {
			signature.WriteString(op.NewReturns[0])
		} else {
			signature.WriteString("(")
			for i, ret := range op.NewReturns {
				if i > 0 {
					signature.WriteString(", ")
				}
				signature.WriteString(ret)
			}
			signature.WriteString(")")
		}
	}

	return signature.String()
}

func (op *ChangeSignatureOperation) extractCurrentSignature(funcDecl *ast.FuncDecl) string {
	// This is a simplified extraction - in a full implementation, 
	// we would use go/format to properly format the signature
	var signature strings.Builder
	
	// Add receiver if this is a method
	if funcDecl.Recv != nil {
		signature.WriteString("(")
		for i, field := range funcDecl.Recv.List {
			if i > 0 {
				signature.WriteString(", ")
			}
			if len(field.Names) > 0 {
				signature.WriteString(field.Names[0].Name)
				signature.WriteString(" ")
			}
			signature.WriteString(op.extractTypeString(field.Type))
		}
		signature.WriteString(") ")
	}

	// Add function name
	signature.WriteString(funcDecl.Name.Name)

	// Add current parameters
	signature.WriteString("(")
	if funcDecl.Type.Params != nil {
		for i, field := range funcDecl.Type.Params.List {
			if i > 0 {
				signature.WriteString(", ")
			}
			if len(field.Names) > 0 {
				for j, name := range field.Names {
					if j > 0 {
						signature.WriteString(", ")
					}
					signature.WriteString(name.Name)
				}
				signature.WriteString(" ")
			}
			signature.WriteString(op.extractTypeString(field.Type))
		}
	}
	signature.WriteString(")")

	// Add current return types
	if funcDecl.Type.Results != nil && len(funcDecl.Type.Results.List) > 0 {
		signature.WriteString(" ")
		results := funcDecl.Type.Results.List
		if len(results) == 1 && len(results[0].Names) == 0 {
			signature.WriteString(op.extractTypeString(results[0].Type))
		} else {
			signature.WriteString("(")
			for i, field := range results {
				if i > 0 {
					signature.WriteString(", ")
				}
				if len(field.Names) > 0 {
					for j, name := range field.Names {
						if j > 0 {
							signature.WriteString(", ")
						}
						signature.WriteString(name.Name)
					}
					signature.WriteString(" ")
				}
				signature.WriteString(op.extractTypeString(field.Type))
			}
			signature.WriteString(")")
		}
	}

	return signature.String()
}

func (op *ChangeSignatureOperation) extractTypeString(expr ast.Expr) string {
	// Simplified type extraction - in a full implementation,
	// we would use go/format for proper formatting
	switch t := expr.(type) {
	case *ast.Ident:
		return t.Name
	case *ast.StarExpr:
		return "*" + op.extractTypeString(t.X)
	case *ast.ArrayType:
		if t.Len == nil {
			return "[]" + op.extractTypeString(t.Elt)
		}
		return "[...]" + op.extractTypeString(t.Elt) // Simplified
	case *ast.SelectorExpr:
		return op.extractTypeString(t.X) + "." + t.Sel.Name
	default:
		return "interface{}" // Fallback
	}
}

func (op *ChangeSignatureOperation) updateCallSite(ref *pkgtypes.Reference, ws *pkgtypes.Workspace) []pkgtypes.Change {
	// Find the file containing the reference
	var refFile *pkgtypes.File
	for _, pkg := range ws.Packages {
		if file, exists := pkg.Files[ref.File]; exists {
			refFile = file
			break
		}
	}
	if refFile == nil || refFile.AST == nil {
		return nil
	}

	var changes []pkgtypes.Change

	// Find the call expression containing this reference
	ast.Inspect(refFile.AST, func(n ast.Node) bool {
		if callExpr, ok := n.(*ast.CallExpr); ok {
			// Check if this call expression contains our reference
			if op.containsReference(callExpr, ref.Position) {
				// Generate new call arguments based on new signature
				newCall := op.generateNewCall(callExpr, ref)
				if newCall != "" {
					changes = append(changes, pkgtypes.Change{
						File:        ref.File,
						Start:       int(callExpr.Pos()),
						End:         int(callExpr.End()),
						OldText:     op.extractCallString(callExpr),
						NewText:     newCall,
						Description: fmt.Sprintf("Update call to %s with new signature", op.FunctionName),
					})
				}
				return false
			}
		}
		return true
	})

	return changes
}

func (op *ChangeSignatureOperation) containsReference(callExpr *ast.CallExpr, pos token.Pos) bool {
	return callExpr.Pos() <= pos && pos <= callExpr.End()
}

func (op *ChangeSignatureOperation) generateNewCall(callExpr *ast.CallExpr, ref *pkgtypes.Reference) string {
	// This is a simplified implementation
	// In practice, we'd need to map old arguments to new parameters
	// and generate appropriate default values for new parameters
	
	funcName := op.FunctionName
	if selectorExpr, ok := callExpr.Fun.(*ast.SelectorExpr); ok {
		// Method call - preserve receiver
		return fmt.Sprintf("%s.%s(/* TODO: update arguments */)", 
			op.extractExprString(selectorExpr.X), funcName)
	}
	
	// Function call
	return fmt.Sprintf("%s(/* TODO: update arguments */)", funcName)
}

func (op *ChangeSignatureOperation) extractCallString(callExpr *ast.CallExpr) string {
	// Simplified call extraction
	return fmt.Sprintf("%s(...)", op.extractExprString(callExpr.Fun))
}

func (op *ChangeSignatureOperation) extractExprString(expr ast.Expr) string {
	switch e := expr.(type) {
	case *ast.Ident:
		return e.Name
	case *ast.SelectorExpr:
		return op.extractExprString(e.X) + "." + e.Sel.Name
	default:
		return "expr"
	}
}

func (op *ChangeSignatureOperation) getAffectedPackages(ws *pkgtypes.Workspace, affectedFiles []string) []string {
	packageMap := make(map[string]bool)
	for _, filePath := range affectedFiles {
		for _, pkg := range ws.Packages {
			if _, exists := pkg.Files[filePath]; exists {
				packageMap[pkg.Path] = true
				break
			}
		}
	}

	packages := make([]string, 0, len(packageMap))
	for pkg := range packageMap {
		packages = append(packages, pkg)
	}
	return packages
}

func (op *ChangeSignatureOperation) analyzeImpact(ws *pkgtypes.Workspace, funcDecl *ast.FuncDecl, numReferences int) []pkgtypes.Issue {
	var issues []pkgtypes.Issue

	// Warn about breaking changes
	issues = append(issues, pkgtypes.Issue{
		Type:        pkgtypes.IssueCompilationError,
		Severity:    pkgtypes.Warning,
		Description: "Changing function signature is a breaking change that may cause compilation errors",
	})

	// Warn about number of call sites
	if numReferences > 5 {
		issues = append(issues, pkgtypes.Issue{
			Type:        pkgtypes.IssueCompilationError,
			Severity:    pkgtypes.Info,
			Description: fmt.Sprintf("Function has %d call sites that will be updated", numReferences),
		})
	}

	// Check if function is exported
	if funcDecl.Name.IsExported() {
		issues = append(issues, pkgtypes.Issue{
			Type:        pkgtypes.IssueVisibilityError,
			Severity:    pkgtypes.Warning,
			Description: "Changing signature of exported function may break external packages",
		})
	}

	return issues
}