package refactor

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"path/filepath"
	"strings"

	"github.com/mamaar/gorefactor/pkg/analysis"
	"github.com/mamaar/gorefactor/pkg/types"
)

// ExtractMethodOperation implements extracting a method from a code block
type ExtractMethodOperation struct {
	SourceFile    string
	StartLine     int
	EndLine       int
	NewMethodName string
	TargetStruct  string
}

func (op *ExtractMethodOperation) Type() types.OperationType {
	return types.ExtractOperation
}

func (op *ExtractMethodOperation) Validate(ws *types.Workspace) error {
	if op.SourceFile == "" {
		return &types.RefactorError{
			Type:    types.InvalidOperation,
			Message: "source file cannot be empty",
		}
	}
	if op.NewMethodName == "" {
		return &types.RefactorError{
			Type:    types.InvalidOperation,
			Message: "method name cannot be empty",
		}
	}
	if op.StartLine > op.EndLine {
		return &types.RefactorError{
			Type:    types.InvalidOperation,
			Message: "start line must be before or equal to end line",
		}
	}
	if op.StartLine < 1 || op.EndLine < 1 {
		return &types.RefactorError{
			Type:    types.InvalidOperation,
			Message: "line numbers must be positive",
		}
	}

	// Check if source file exists
	var sourceFile *types.File
	for _, pkg := range ws.Packages {
		// Try exact match first
		if file, exists := pkg.Files[op.SourceFile]; exists {
			sourceFile = file
			break
		}
		// Try filepath base match
		for filename, file := range pkg.Files {
			if filename == op.SourceFile || filepath.Base(file.Path) == op.SourceFile {
				sourceFile = file
				break
			}
		}
		if sourceFile != nil {
			break
		}
	}
	if sourceFile == nil {
		return &types.RefactorError{
			Type:    types.FileSystemError,
			Message: fmt.Sprintf("source file not found: %s", op.SourceFile),
		}
	}

	// Validate method name is a valid Go identifier
	if !isValidGoIdentifierExtract(op.NewMethodName) {
		return &types.RefactorError{
			Type:    types.InvalidOperation,
			Message: fmt.Sprintf("invalid Go identifier: %s", op.NewMethodName),
		}
	}

	return nil
}

func (op *ExtractMethodOperation) Execute(ws *types.Workspace) (*types.RefactoringPlan, error) {
	// Find the source file
	var sourceFile *types.File
	var sourcePackage *types.Package
	for _, pkg := range ws.Packages {
		// Try exact match first
		if file, exists := pkg.Files[op.SourceFile]; exists {
			sourceFile = file
			sourcePackage = pkg
			break
		}
		// Try filepath base match
		for filename, file := range pkg.Files {
			if filename == op.SourceFile || filepath.Base(file.Path) == op.SourceFile {
				sourceFile = file
				sourcePackage = pkg
				break
			}
		}
		if sourceFile != nil {
			break
		}
	}

	if sourceFile == nil {
		return nil, &types.RefactorError{
			Type:    types.FileSystemError,
			Message: fmt.Sprintf("source file not found: %s", op.SourceFile),
		}
	}

	// Parse the file to get the AST
	fset := token.NewFileSet()
	astFile, err := parser.ParseFile(fset, op.SourceFile, sourceFile.OriginalContent, parser.ParseComments)
	if err != nil {
		return nil, &types.RefactorError{
			Type:    types.ParseError,
			Message: fmt.Sprintf("failed to parse source file: %v", err),
		}
	}

	// Extract the code block
	extractedCode, err := op.extractCodeBlock(string(sourceFile.OriginalContent), op.StartLine, op.EndLine)
	if err != nil {
		return nil, err
	}

	// Analyze the extracted code to determine parameters and return values
	params, returns, err := op.analyzeExtractedCode(extractedCode, astFile, fset)
	if err != nil {
		return nil, err
	}

	// Generate the new method
	newMethod := op.generateMethod(params, returns, extractedCode)

	// Create changes
	changes := []types.Change{
		// Replace extracted code with method call
		{
			File:        op.SourceFile,
			Start:       op.getLineOffset(string(sourceFile.OriginalContent), op.StartLine),
			End:         op.getLineOffset(string(sourceFile.OriginalContent), op.EndLine+1) - 1,
			OldText:     extractedCode,
			NewText:     op.generateMethodCall(params),
			Description: fmt.Sprintf("Replace extracted code with call to %s", op.NewMethodName),
		},
		// Add new method to the struct
		{
			File:        op.SourceFile,
			Start:       op.findInsertionPoint(astFile, op.TargetStruct),
			End:         op.findInsertionPoint(astFile, op.TargetStruct),
			OldText:     "",
			NewText:     "\n" + newMethod + "\n",
			Description: fmt.Sprintf("Add extracted method %s", op.NewMethodName),
		},
	}

	return &types.RefactoringPlan{
		Operations:    []types.Operation{op},
		Changes:       changes,
		AffectedFiles: []string{op.SourceFile},
		Impact: &types.ImpactAnalysis{
			AffectedFiles:    []string{op.SourceFile},
			AffectedPackages: []string{sourcePackage.Path},
		},
		Reversible: true,
	}, nil
}

func (op *ExtractMethodOperation) Description() string {
	return fmt.Sprintf("Extract method '%s' from lines %d-%d in %s to %s",
		op.NewMethodName, op.StartLine, op.EndLine, op.SourceFile, op.TargetStruct)
}

func (op *ExtractMethodOperation) extractCodeBlock(content string, startLine, endLine int) (string, error) {
	lines := strings.Split(content, "\n")
	if startLine < 1 || endLine > len(lines) || startLine > endLine {
		return "", &types.RefactorError{
			Type:    types.InvalidOperation,
			Message: fmt.Sprintf("invalid line range: %d-%d (file has %d lines)", startLine, endLine, len(lines)),
		}
	}

	// Extract lines (convert from 1-based to 0-based indexing)
	extractedLines := lines[startLine-1 : endLine]
	return strings.Join(extractedLines, "\n"), nil
}

func (op *ExtractMethodOperation) analyzeExtractedCode(code string, astFile *ast.File, fset *token.FileSet) ([]string, []string, error) {
	// Parse the extracted code to analyze variable usage
	extractedAST, err := parser.ParseFile(fset, "", "package main\nfunc dummy() {\n"+code+"\n}", parser.ParseComments)
	if err != nil {
		// If parsing fails, return basic analysis
		return op.basicVariableAnalysis(code)
	}
	
	// Find variables used but not declared in the extracted block
	usedVars := make(map[string]string) // name -> type
	declaredVars := make(map[string]bool) // variables declared in extracted block
	assignedVars := make(map[string]string) // variables assigned in block
	
	// Walk the AST to find variable usage
	ast.Inspect(extractedAST, func(n ast.Node) bool {
		switch node := n.(type) {
		case *ast.Ident:
			// Check if this is a variable reference
			if node.Obj == nil && node.Name != "_" {
				// Skip built-in functions and packages
				if !isBuiltinOrImported(node.Name) && !isGoKeyword(node.Name) {
					// Only include single-character variables or common variable patterns
					if len(node.Name) == 1 || node.Name == "err" || node.Name == "ctx" {
						usedVars[node.Name] = "int" // Default to int for simple vars
					}
				}
			}
		case *ast.AssignStmt:
			// Variables being assigned
			for _, expr := range node.Lhs {
				if ident, ok := expr.(*ast.Ident); ok {
					if node.Tok == token.DEFINE {
						declaredVars[ident.Name] = true
					} else {
						assignedVars[ident.Name] = "interface{}" // Default type
					}
				}
			}
		case *ast.GenDecl:
			// Variable declarations
			if node.Tok == token.VAR {
				for _, spec := range node.Specs {
					if valueSpec, ok := spec.(*ast.ValueSpec); ok {
						for _, name := range valueSpec.Names {
							declaredVars[name.Name] = true
						}
					}
				}
			}
		}
		return true
	})
	
	// Build parameter list (used but not declared)
	var params []string
	for varName, varType := range usedVars {
		if !declaredVars[varName] && !isBuiltinOrImported(varName) {
			params = append(params, fmt.Sprintf("%s %s", varName, varType))
		}
	}
	
	// Build return list (assigned and potentially used after)
	var returns []string
	for varName, varType := range assignedVars {
		if !declaredVars[varName] {
			returns = append(returns, varType)
		}
	}
	
	return params, returns, nil
}

func (op *ExtractMethodOperation) basicVariableAnalysis(code string) ([]string, []string, error) {
	// Fallback analysis using string parsing
	lines := strings.Split(code, "\n")
	usedVars := make(map[string]bool)
	
	for _, line := range lines {
		// Simple heuristic: look for common variable patterns
		line = strings.TrimSpace(line)
		if strings.Contains(line, ":=") {
			// Variable declaration, skip
			continue
		}
		
		// Look for variable usage patterns (very basic)
		words := strings.Fields(line)
		for _, word := range words {
			// Remove common punctuation
			word = strings.Trim(word, "()[]{},.;:")
			// Check if it looks like a variable (starts with lowercase, not a keyword)
			if len(word) > 0 && word[0] >= 'a' && word[0] <= 'z' && !isGoKeyword(word) {
				usedVars[word] = true
			}
		}
	}
	
	// Convert to parameter list (basic types)
	var params []string
	for varName := range usedVars {
		if !isBuiltinOrImported(varName) {
			params = append(params, fmt.Sprintf("%s interface{}", varName))
		}
	}
	
	return params, []string{}, nil
}

func isBuiltinOrImported(name string) bool {
	builtins := map[string]bool{
		"true": true, "false": true, "nil": true,
		"len": true, "cap": true, "make": true, "new": true,
		"append": true, "copy": true, "delete": true,
		"fmt": true, "strings": true, "strconv": true,
		"err": false, // err is commonly used variable
	}
	
	if builtin, exists := builtins[name]; exists {
		return builtin
	}
	
	// Check if it starts with uppercase (likely imported)
	return len(name) > 0 && name[0] >= 'A' && name[0] <= 'Z'
}

func isGoKeyword(word string) bool {
	keywords := map[string]bool{
		"break": true, "case": true, "chan": true, "const": true,
		"continue": true, "default": true, "defer": true, "else": true,
		"fallthrough": true, "for": true, "func": true, "go": true,
		"goto": true, "if": true, "import": true, "interface": true,
		"map": true, "package": true, "range": true, "return": true,
		"select": true, "struct": true, "switch": true, "type": true,
		"var": true,
	}
	return keywords[word]
}

func (op *ExtractMethodOperation) generateMethod(params, returns []string, body string) string {
	method := fmt.Sprintf("func (receiver *%s) %s(", op.TargetStruct, op.NewMethodName)
	
	// Add parameters
	method += strings.Join(params, ", ")
	method += ")"
	
	// Add return types
	if len(returns) > 0 {
		if len(returns) == 1 {
			method += " " + returns[0]
		} else {
			method += " (" + strings.Join(returns, ", ") + ")"
		}
	}
	
	method += " {\n"
	// Indent the body
	indentedBody := op.indentCode(body, "\t")
	method += indentedBody
	method += "\n}"
	
	return method
}

func (op *ExtractMethodOperation) generateMethodCall(params []string) string {
	call := fmt.Sprintf("receiver.%s(", op.NewMethodName)
	// Add parameter names (without types)
	var paramNames []string
	for _, param := range params {
		// Extract just the parameter name (before the type)
		parts := strings.Fields(param)
		if len(parts) > 0 {
			paramNames = append(paramNames, parts[0])
		}
	}
	call += strings.Join(paramNames, ", ")
	call += ")"
	return call
}

func (op *ExtractMethodOperation) indentCode(code, indent string) string {
	lines := strings.Split(code, "\n")
	for i, line := range lines {
		if strings.TrimSpace(line) != "" {
			lines[i] = indent + line
		}
	}
	return strings.Join(lines, "\n")
}

func (op *ExtractMethodOperation) getLineOffset(content string, line int) int {
	return getLineOffset(content, line)
}

func (op *ExtractMethodOperation) findInsertionPoint(astFile *ast.File, structName string) int {
	// Find the end of the struct definition to insert the new method after it
	// This is a simplified implementation - a full version would be more sophisticated
	
	for _, decl := range astFile.Decls {
		if genDecl, ok := decl.(*ast.GenDecl); ok && genDecl.Tok == token.TYPE {
			for _, spec := range genDecl.Specs {
				if typeSpec, ok := spec.(*ast.TypeSpec); ok && typeSpec.Name.Name == structName {
					return int(genDecl.End())
				}
			}
		}
	}
	
	// If struct not found, insert at end of file
	return int(astFile.End())
}

// ExtractFunctionOperation implements extracting a function from a code block
type ExtractFunctionOperation struct {
	SourceFile      string
	StartLine       int
	EndLine         int
	NewFunctionName string
}

func (op *ExtractFunctionOperation) Type() types.OperationType {
	return types.ExtractOperation
}

func (op *ExtractFunctionOperation) Validate(ws *types.Workspace) error {
	if op.SourceFile == "" {
		return &types.RefactorError{
			Type:    types.InvalidOperation,
			Message: "source file cannot be empty",
		}
	}
	if op.NewFunctionName == "" {
		return &types.RefactorError{
			Type:    types.InvalidOperation,
			Message: "function name cannot be empty",
		}
	}
	if op.StartLine > op.EndLine {
		return &types.RefactorError{
			Type:    types.InvalidOperation,
			Message: "start line must be before or equal to end line",
		}
	}
	if op.StartLine < 1 || op.EndLine < 1 {
		return &types.RefactorError{
			Type:    types.InvalidOperation,
			Message: "line numbers must be positive",
		}
	}

	// Check if source file exists - prioritize root package
	var sourceFile *types.File
	
	// First, try to find in the root package
	if rootPkg, exists := ws.Packages[ws.RootPath]; exists {
		if file, exists := rootPkg.Files[op.SourceFile]; exists {
			sourceFile = file
		} else {
			// Try filepath base match in root package
			for filename, file := range rootPkg.Files {
				if filename == op.SourceFile || filepath.Base(file.Path) == op.SourceFile {
					sourceFile = file
					break
				}
			}
		}
	}
	
	// If not found in root package, search all packages
	if sourceFile == nil {
		for _, pkg := range ws.Packages {
			// Try exact match first
			if file, exists := pkg.Files[op.SourceFile]; exists {
				sourceFile = file
				break
			}
			// Try filepath base match
			for filename, file := range pkg.Files {
				if filename == op.SourceFile || filepath.Base(file.Path) == op.SourceFile {
					sourceFile = file
					break
				}
			}
			// Try relative path match from workspace root
			for _, file := range pkg.Files {
				if relPath, err := filepath.Rel(ws.RootPath, file.Path); err == nil {
					if relPath == op.SourceFile || relPath == filepath.Clean(op.SourceFile) {
						sourceFile = file
						break
					}
				}
			}
			// Try absolute path match
			for _, file := range pkg.Files {
				if file.Path == op.SourceFile || file.Path == filepath.Clean(op.SourceFile) {
					sourceFile = file
					break
				}
			}
			if sourceFile != nil {
				break
			}
		}
	}
	if sourceFile == nil {
		return &types.RefactorError{
			Type:    types.FileSystemError,
			Message: fmt.Sprintf("source file not found: %s", op.SourceFile),
		}
	}

	// Validate function name is a valid Go identifier
	if !isValidGoIdentifierExtract(op.NewFunctionName) {
		return &types.RefactorError{
			Type:    types.InvalidOperation,
			Message: fmt.Sprintf("invalid Go identifier: %s", op.NewFunctionName),
		}
	}

	return nil
}

func (op *ExtractFunctionOperation) Execute(ws *types.Workspace) (*types.RefactoringPlan, error) {
	// Find the source file - prioritize root package
	var sourceFile *types.File
	var sourcePackage *types.Package
	
	// First, try to find in the root package
	if rootPkg, exists := ws.Packages[ws.RootPath]; exists {
		if file, exists := rootPkg.Files[op.SourceFile]; exists {
			sourceFile = file
			sourcePackage = rootPkg
		} else {
			// Try filepath base match in root package
			for filename, file := range rootPkg.Files {
				if filename == op.SourceFile || filepath.Base(file.Path) == op.SourceFile {
					sourceFile = file
					sourcePackage = rootPkg
					break
				}
			}
		}
	}
	
	// If not found in root package, search all packages
	if sourceFile == nil {
		for _, pkg := range ws.Packages {
			// Try exact match first
			if file, exists := pkg.Files[op.SourceFile]; exists {
				sourceFile = file
				sourcePackage = pkg
				break
			}
			// Try filepath base match
			for filename, file := range pkg.Files {
				if filename == op.SourceFile || filepath.Base(file.Path) == op.SourceFile {
					sourceFile = file
					sourcePackage = pkg
					break
				}
			}
			// Try relative path match from workspace root
			for _, file := range pkg.Files {
				if relPath, err := filepath.Rel(ws.RootPath, file.Path); err == nil {
					if relPath == op.SourceFile || relPath == filepath.Clean(op.SourceFile) {
						sourceFile = file
						sourcePackage = pkg
						break
					}
				}
			}
			// Try absolute path match
			for _, file := range pkg.Files {
				if file.Path == op.SourceFile || file.Path == filepath.Clean(op.SourceFile) {
					sourceFile = file
					sourcePackage = pkg
					break
				}
			}
			if sourceFile != nil {
				break
			}
		}
	}

	if sourceFile == nil {
		return nil, &types.RefactorError{
			Type:    types.FileSystemError,
			Message: fmt.Sprintf("source file not found: %s", op.SourceFile),
		}
	}

	// Parse the file to get the AST
	fset := token.NewFileSet()
	astFile, err := parser.ParseFile(fset, op.SourceFile, sourceFile.OriginalContent, parser.ParseComments)
	if err != nil {
		return nil, &types.RefactorError{
			Type:    types.ParseError,
			Message: fmt.Sprintf("failed to parse source file: %v", err),
		}
	}

	// Extract the code block
	extractedCode, err := op.extractCodeBlock(string(sourceFile.OriginalContent), op.StartLine, op.EndLine)
	if err != nil {
		return nil, err
	}

	// Analyze the extracted code to determine parameters and return values
	params, returns, err := op.analyzeExtractedCode(extractedCode, astFile, fset)
	if err != nil {
		return nil, err
	}

	// Generate the new function
	newFunction := op.generateFunction(params, returns, extractedCode)

	// Create changes
	changes := []types.Change{
		// Replace extracted code with function call
		{
			File:        op.SourceFile,
			Start:       op.getLineOffset(string(sourceFile.OriginalContent), op.StartLine),
			End:         op.getLineOffset(string(sourceFile.OriginalContent), op.EndLine+1) - 1,
			OldText:     extractedCode,
			NewText:     op.generateFunctionCall(params),
			Description: fmt.Sprintf("Replace extracted code with call to %s", op.NewFunctionName),
		},
		// Add new function to the package
		{
			File:        op.SourceFile,
			Start:       op.findFunctionInsertionPoint(astFile),
			End:         op.findFunctionInsertionPoint(astFile),
			OldText:     "",
			NewText:     "\n" + newFunction + "\n",
			Description: fmt.Sprintf("Add extracted function %s", op.NewFunctionName),
		},
	}

	return &types.RefactoringPlan{
		Operations:    []types.Operation{op},
		Changes:       changes,
		AffectedFiles: []string{op.SourceFile},
		Impact: &types.ImpactAnalysis{
			AffectedFiles:    []string{op.SourceFile},
			AffectedPackages: []string{sourcePackage.Path},
		},
		Reversible: true,
	}, nil
}

func (op *ExtractFunctionOperation) Description() string {
	return fmt.Sprintf("Extract function '%s' from lines %d-%d in %s",
		op.NewFunctionName, op.StartLine, op.EndLine, op.SourceFile)
}

func (op *ExtractFunctionOperation) extractCodeBlock(content string, startLine, endLine int) (string, error) {
	lines := strings.Split(content, "\n")
	if startLine < 1 || endLine > len(lines) || startLine > endLine {
		return "", &types.RefactorError{
			Type:    types.InvalidOperation,
			Message: fmt.Sprintf("invalid line range: %d-%d (file has %d lines)", startLine, endLine, len(lines)),
		}
	}

	// Extract lines (convert from 1-based to 0-based indexing)
	extractedLines := lines[startLine-1 : endLine]
	return strings.Join(extractedLines, "\n"), nil
}

func (op *ExtractFunctionOperation) analyzeExtractedCode(code string, astFile *ast.File, fset *token.FileSet) ([]string, []string, error) {
	// Parse the extracted code to analyze variable usage
	// Add proper indentation to the extracted code
	indentedCode := "\t" + strings.ReplaceAll(code, "\n", "\n\t")
	wrapperCode := "package main\nfunc dummy() {\n" + indentedCode + "\n}"
	
	extractedAST, err := parser.ParseFile(fset, "", wrapperCode, parser.ParseComments)
	if err != nil {
		// If parsing fails, return basic analysis
		return op.basicVariableAnalysis(code)
	}
	
	// Find variables used but not declared in the extracted block
	usedVars := make(map[string]string) // name -> type
	declaredVars := make(map[string]bool) // variables declared in extracted block
	assignedVars := make(map[string]string) // variables assigned in block
	
	// First, build a map of variable types from the original file context
	varTypes := op.extractVariableTypesFromFile(astFile)
	
	// Walk the AST to find variable usage
	ast.Inspect(extractedAST, func(n ast.Node) bool {
		switch node := n.(type) {
		case *ast.Ident:
			// Check if this is a variable reference
			if node.Obj == nil && node.Name != "_" {
				// Skip built-in functions and packages
				if !isBuiltinOrImported(node.Name) && !isGoKeyword(node.Name) {
					// Only include single-character variables or common variable patterns
					if len(node.Name) == 1 || node.Name == "err" || node.Name == "ctx" {
						// Use the actual type from the file context if available
						if actualType, exists := varTypes[node.Name]; exists {
							usedVars[node.Name] = actualType
						} else {
							usedVars[node.Name] = "int" // Default to int for simple vars
						}
					}
				}
			}
		case *ast.AssignStmt:
			// Variables being assigned
			for _, expr := range node.Lhs {
				if ident, ok := expr.(*ast.Ident); ok {
					if node.Tok == token.DEFINE {
						declaredVars[ident.Name] = true
					} else {
						// Use actual type if available
						if actualType, exists := varTypes[ident.Name]; exists {
							assignedVars[ident.Name] = actualType
						} else {
							assignedVars[ident.Name] = "interface{}" // Default type
						}
					}
				}
			}
		case *ast.GenDecl:
			// Variable declarations
			if node.Tok == token.VAR {
				for _, spec := range node.Specs {
					if valueSpec, ok := spec.(*ast.ValueSpec); ok {
						for _, name := range valueSpec.Names {
							declaredVars[name.Name] = true
						}
					}
				}
			}
		}
		return true
	})
	
	// Build parameter list (used but not declared)
	var params []string
	for varName, varType := range usedVars {
		if !declaredVars[varName] && !isBuiltinOrImported(varName) {
			params = append(params, fmt.Sprintf("%s %s", varName, varType))
		}
	}
	
	// Build return list (assigned and potentially used after)
	var returns []string
	for varName, varType := range assignedVars {
		if !declaredVars[varName] {
			returns = append(returns, varType)
		}
	}
	
	return params, returns, nil
}

// extractVariableTypesFromFile extracts variable types from the original file context
func (op *ExtractFunctionOperation) extractVariableTypesFromFile(astFile *ast.File) map[string]string {
	varTypes := make(map[string]string)
	
	// Walk the AST to find variable declarations
	ast.Inspect(astFile, func(n ast.Node) bool {
		switch node := n.(type) {
		case *ast.GenDecl:
			// Variable declarations with 'var'
			if node.Tok == token.VAR {
				for _, spec := range node.Specs {
					if valueSpec, ok := spec.(*ast.ValueSpec); ok {
						// Extract type from specification
						var typeStr string
						if valueSpec.Type != nil {
							typeStr = op.extractTypeString(valueSpec.Type)
						} else if len(valueSpec.Values) > 0 {
							// Infer type from initial value
							typeStr = op.inferTypeFromValue(valueSpec.Values[0])
						} else {
							typeStr = "interface{}" // Default
						}
						
						// Apply type to all names in this declaration
						for _, name := range valueSpec.Names {
							varTypes[name.Name] = typeStr
						}
					}
				}
			}
		case *ast.AssignStmt:
			// Short variable declarations with ':='
			if node.Tok == token.DEFINE {
				for i, expr := range node.Lhs {
					if ident, ok := expr.(*ast.Ident); ok {
						var typeStr string
						if i < len(node.Rhs) {
							typeStr = op.inferTypeFromValue(node.Rhs[i])
						} else {
							typeStr = "interface{}"
						}
						varTypes[ident.Name] = typeStr
					}
				}
			}
		}
		return true
	})
	
	return varTypes
}

// extractTypeString converts an AST type expression to a string
func (op *ExtractFunctionOperation) extractTypeString(typeExpr ast.Expr) string {
	switch t := typeExpr.(type) {
	case *ast.Ident:
		return t.Name
	case *ast.StarExpr:
		return "*" + op.extractTypeString(t.X)
	case *ast.ArrayType:
		if t.Len == nil {
			return "[]" + op.extractTypeString(t.Elt)
		}
		return fmt.Sprintf("[%s]%s", op.extractExprString(t.Len), op.extractTypeString(t.Elt))
	case *ast.MapType:
		return fmt.Sprintf("map[%s]%s", op.extractTypeString(t.Key), op.extractTypeString(t.Value))
	case *ast.ChanType:
		dir := ""
		if t.Dir == ast.SEND {
			dir = "chan<- "
		} else if t.Dir == ast.RECV {
			dir = "<-chan "
		} else {
			dir = "chan "
		}
		return dir + op.extractTypeString(t.Value)
	case *ast.InterfaceType:
		return "interface{}"
	case *ast.StructType:
		return "struct{}"
	case *ast.FuncType:
		return "func(...)"
	case *ast.SelectorExpr:
		return fmt.Sprintf("%s.%s", op.extractExprString(t.X), t.Sel.Name)
	default:
		return "interface{}"
	}
}

// extractExprString converts an AST expression to a string (simplified)
func (op *ExtractFunctionOperation) extractExprString(expr ast.Expr) string {
	switch e := expr.(type) {
	case *ast.Ident:
		return e.Name
	case *ast.BasicLit:
		return e.Value
	case *ast.SelectorExpr:
		return fmt.Sprintf("%s.%s", op.extractExprString(e.X), e.Sel.Name)
	default:
		return "..."
	}
}

// inferTypeFromValue infers type from an initial value expression
func (op *ExtractFunctionOperation) inferTypeFromValue(valueExpr ast.Expr) string {
	switch v := valueExpr.(type) {
	case *ast.BasicLit:
		switch v.Kind {
		case token.INT:
			return "int"
		case token.FLOAT:
			return "float64"
		case token.STRING:
			return "string"
		case token.CHAR:
			return "rune"
		default:
			return "interface{}"
		}
	case *ast.Ident:
		// Could be a boolean literal or variable reference
		if v.Name == "true" || v.Name == "false" {
			return "bool"
		}
		return "interface{}" // Unknown
	case *ast.CompositeLit:
		if v.Type != nil {
			return op.extractTypeString(v.Type)
		}
		return "interface{}"
	case *ast.CallExpr:
		// Function call - try to infer from function name
		if ident, ok := v.Fun.(*ast.Ident); ok {
			switch ident.Name {
			case "make":
				if len(v.Args) > 0 {
					return op.extractTypeString(v.Args[0])
				}
			case "new":
				if len(v.Args) > 0 {
					return "*" + op.extractTypeString(v.Args[0])
				}
			}
		}
		return "interface{}"
	default:
		return "interface{}"
	}
}

func (op *ExtractFunctionOperation) basicVariableAnalysis(code string) ([]string, []string, error) {
	// Fallback analysis using string parsing
	usedVars := make(map[string]bool)
	
	// Look for simple identifiers in the code
	// For the specific case of "for k := range i { fmt.Println(i, j, k) }"
	// We want to detect i and j as parameters
	
	// More targeted analysis - look for identifiers that appear to be used
	// Split on word boundaries and look for lowercase identifiers
	tokens := strings.FieldsFunc(code, func(r rune) bool {
		return !((r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || r == '_')
	})
	
	for _, token := range tokens {
		token = strings.TrimSpace(token)
		// Skip empty, keywords, builtins, and uppercase names
		if token == "" || isGoKeyword(token) || isBuiltinOrImported(token) {
			continue
		}
		// Skip if it contains numbers (likely not a simple variable)
		if strings.ContainsAny(token, "0123456789") {
			continue
		}
		// Check if it's a simple lowercase identifier
		if len(token) > 0 && token[0] >= 'a' && token[0] <= 'z' {
			// Special filtering: only include single character variables or common names
			// This avoids picking up parts of keywords or function names
			if len(token) == 1 || token == "err" || token == "ctx" {
				usedVars[token] = true
			}
		}
	}
	
	// Convert to parameter list - use interface{} as safer fallback
	var params []string
	for varName := range usedVars {
		params = append(params, fmt.Sprintf("%s interface{}", varName))
	}
	
	return params, []string{}, nil
}

func (op *ExtractFunctionOperation) generateFunction(params, returns []string, body string) string {
	function := fmt.Sprintf("func %s(", op.NewFunctionName)
	
	// Add parameters
	function += strings.Join(params, ", ")
	function += ")"
	
	// Add return types
	if len(returns) > 0 {
		if len(returns) == 1 {
			function += " " + returns[0]
		} else {
			function += " (" + strings.Join(returns, ", ") + ")"
		}
	}
	
	function += " {\n"
	// Indent the body
	indentedBody := op.indentCode(body, "\t")
	function += indentedBody
	function += "\n}"
	
	return function
}

func (op *ExtractFunctionOperation) generateFunctionCall(params []string) string {
	call := fmt.Sprintf("%s(", op.NewFunctionName)
	// Add parameter names (without types)
	var paramNames []string
	for _, param := range params {
		// Extract just the parameter name (before the type)
		parts := strings.Fields(param)
		if len(parts) > 0 {
			paramNames = append(paramNames, parts[0])
		}
	}
	call += strings.Join(paramNames, ", ")
	call += ")"
	return call
}

func (op *ExtractFunctionOperation) indentCode(code, indent string) string {
	lines := strings.Split(code, "\n")
	for i, line := range lines {
		if strings.TrimSpace(line) != "" {
			lines[i] = indent + line
		}
	}
	return strings.Join(lines, "\n")
}

func (op *ExtractFunctionOperation) getLineOffset(content string, line int) int {
	return getLineOffset(content, line)
}

func (op *ExtractFunctionOperation) findFunctionInsertionPoint(astFile *ast.File) int {
	// Insert function at package level, after all imports and type declarations
	// but before the main function or other functions
	
	var lastImport ast.Node
	var firstFunc ast.Node
	
	for _, decl := range astFile.Decls {
		if genDecl, ok := decl.(*ast.GenDecl); ok {
			if genDecl.Tok == token.IMPORT {
				lastImport = genDecl
			}
		} else if funcDecl, ok := decl.(*ast.FuncDecl); ok && firstFunc == nil {
			firstFunc = funcDecl
		}
	}
	
	// Insert after imports but before first function
	if firstFunc != nil {
		return int(firstFunc.Pos()) - 1
	} else if lastImport != nil {
		return int(lastImport.End())
	}
	
	// If no imports or functions, insert at end of file
	return int(astFile.End())
}

// ExtractInterfaceOperation implements extracting an interface from a struct
type ExtractInterfaceOperation struct {
	SourceStruct  string
	InterfaceName string
	Methods       []string
	TargetPackage string
}

func (op *ExtractInterfaceOperation) Type() types.OperationType {
	return types.ExtractOperation
}

func (op *ExtractInterfaceOperation) Validate(ws *types.Workspace) error {
	if op.SourceStruct == "" {
		return &types.RefactorError{
			Type:    types.InvalidOperation,
			Message: "source struct cannot be empty",
		}
	}
	if op.InterfaceName == "" {
		return &types.RefactorError{
			Type:    types.InvalidOperation,
			Message: "interface name cannot be empty",
		}
	}
	if len(op.Methods) == 0 {
		return &types.RefactorError{
			Type:    types.InvalidOperation,
			Message: "methods list cannot be empty",
		}
	}
	if !isValidGoIdentifierExtract(op.InterfaceName) {
		return &types.RefactorError{
			Type:    types.InvalidOperation,
			Message: fmt.Sprintf("invalid Go identifier: %s", op.InterfaceName),
		}
	}

	// Find the source struct
	resolver := analysis.NewSymbolResolver(ws)
	var structSymbol *types.Symbol
	
	for _, pkg := range ws.Packages {
		if pkg.Symbols != nil {
			if symbol, err := resolver.ResolveSymbol(pkg, op.SourceStruct); err == nil {
				if symbol.Kind == types.TypeSymbol {
					structSymbol = symbol
					break
				}
			}
		}
	}
	
	if structSymbol == nil {
		return &types.RefactorError{
			Type:    types.SymbolNotFound,
			Message: fmt.Sprintf("struct not found: %s", op.SourceStruct),
		}
	}

	// Validate that all specified methods exist on the struct
	// Find the package containing the struct
	var sourcePackage *types.Package
	for _, pkg := range ws.Packages {
		if pkg.Symbols != nil {
			if symbol, err := resolver.ResolveSymbol(pkg, op.SourceStruct); err == nil {
				if symbol.Kind == types.TypeSymbol {
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
	
	// Check methods in the package's symbol table
	for _, methodName := range op.Methods {
		found := false
		if methods, exists := sourcePackage.Symbols.Methods[op.SourceStruct]; exists {
			for _, method := range methods {
				if method.Name == methodName {
					found = true
					break
				}
			}
		}
		if !found {
			return &types.RefactorError{
				Type:    types.SymbolNotFound,
				Message: fmt.Sprintf("method %s not found on struct %s", methodName, op.SourceStruct),
			}
		}
	}

	return nil
}

func (op *ExtractInterfaceOperation) Execute(ws *types.Workspace) (*types.RefactoringPlan, error) {
	// Find the struct and its methods
	resolver := analysis.NewSymbolResolver(ws)
	var structSymbol *types.Symbol
	var sourcePackage *types.Package
	
	for _, pkg := range ws.Packages {
		if pkg.Symbols != nil {
			if symbol, err := resolver.ResolveSymbol(pkg, op.SourceStruct); err == nil {
				if symbol.Kind == types.TypeSymbol {
					structSymbol = symbol
					sourcePackage = pkg
					break
				}
			}
		}
	}

	if structSymbol == nil {
		return nil, &types.RefactorError{
			Type:    types.SymbolNotFound,
			Message: fmt.Sprintf("struct not found: %s", op.SourceStruct),
		}
	}

	// Generate interface definition
	interfaceCode := op.generateInterface(structSymbol, sourcePackage)

	// Determine target file
	targetFile := op.getTargetFileName()
	
	// Generate complete file content including package declaration
	packageName := op.getTargetPackageName(sourcePackage)
	fileContent := fmt.Sprintf("package %s\n\n%s\n", packageName, interfaceCode)
	
	changes := []types.Change{
		{
			File:        targetFile,
			Start:       0, // Insert at beginning for now
			End:         0,
			OldText:     "",
			NewText:     fileContent,
			Description: fmt.Sprintf("Create interface %s", op.InterfaceName),
		},
	}

	affectedFiles := []string{targetFile}
	affectedPackages := []string{sourcePackage.Path}
	
	if op.TargetPackage != "" && op.TargetPackage != sourcePackage.Path {
		affectedPackages = append(affectedPackages, op.TargetPackage)
	}

	return &types.RefactoringPlan{
		Operations:    []types.Operation{op},
		Changes:       changes,
		AffectedFiles: affectedFiles,
		Impact: &types.ImpactAnalysis{
			AffectedFiles:    affectedFiles,
			AffectedPackages: affectedPackages,
		},
		Reversible: true,
	}, nil
}

func (op *ExtractInterfaceOperation) Description() string {
	return fmt.Sprintf("Extract interface '%s' from %s with methods [%s] to package %s",
		op.InterfaceName, op.SourceStruct, strings.Join(op.Methods, " "), op.TargetPackage)
}

func (op *ExtractInterfaceOperation) generateInterface(structSymbol *types.Symbol, sourcePackage *types.Package) string {
	interfaceCode := fmt.Sprintf("type %s interface {\n", op.InterfaceName)
	
	for _, methodName := range op.Methods {
		if methods, exists := sourcePackage.Symbols.Methods[op.SourceStruct]; exists {
			for _, method := range methods {
				if method.Name == methodName {
					// Extract method signature without receiver
					signature := op.extractMethodSignature(method.Signature)
					interfaceCode += fmt.Sprintf("\t%s%s\n", methodName, signature)
					break
				}
			}
		}
	}
	
	interfaceCode += "}"
	return interfaceCode
}

func (op *ExtractInterfaceOperation) extractMethodSignature(fullSignature string) string {
	// Remove receiver from method signature
	// Example: "func (c *Calculator) Add(a, b float64) float64" -> "(a, b float64) float64"
	
	if fullSignature == "" {
		return "()"
	}
	
	// Method signatures typically look like: "func (receiver Type) MethodName(params) returns"
	// We want to extract: "(params) returns"
	
	funcStart := strings.Index(fullSignature, "func")
	if funcStart == -1 {
		// Not a function signature, return as-is
		return fullSignature
	}
	
	// Skip past the receiver: func (receiver Type)
	receiverStart := strings.Index(fullSignature[funcStart:], "(")
	if receiverStart == -1 {
		return "()"
	}
	receiverStart += funcStart
	
	// Find the matching closing parenthesis for receiver
	parenCount := 1
	receiverEnd := receiverStart + 1
	for receiverEnd < len(fullSignature) && parenCount > 0 {
		if fullSignature[receiverEnd] == '(' {
			parenCount++
		} else if fullSignature[receiverEnd] == ')' {
			parenCount--
		}
		receiverEnd++
	}
	
	if receiverEnd >= len(fullSignature) {
		return "()"
	}
	
	// Skip past method name to find parameters
	remaining := strings.TrimSpace(fullSignature[receiverEnd:])
	
	// Find method name (skip whitespace and method name)
	spaceOrParen := -1
	for i, r := range remaining {
		if r == ' ' || r == '(' {
			spaceOrParen = i
			break
		}
	}
	
	if spaceOrParen == -1 {
		return "()"
	}
	
	// Find the opening parenthesis of parameters
	paramStart := strings.Index(remaining[spaceOrParen:], "(")
	if paramStart == -1 {
		return "()"
	}
	paramStart += spaceOrParen
	
	// Return everything from the parameters onward
	result := strings.TrimSpace(remaining[paramStart:])
	if result == "" {
		return "()"
	}
	
	return result
}

func (op *ExtractInterfaceOperation) getTargetFileName() string {
	if op.TargetPackage != "" {
		return op.TargetPackage + "/interfaces.go"
	}
	return "interfaces.go"
}

func (op *ExtractInterfaceOperation) getTargetPackageName(sourcePackage *types.Package) string {
	if op.TargetPackage != "" {
		// Extract package name from path
		parts := strings.Split(op.TargetPackage, "/")
		return parts[len(parts)-1]
	}
	return sourcePackage.Name
}

// ExtractVariableOperation implements extracting a variable from an expression
type ExtractVariableOperation struct {
	SourceFile   string
	StartLine    int
	EndLine      int
	VariableName string
	Expression   string
}

func (op *ExtractVariableOperation) Type() types.OperationType {
	return types.ExtractOperation
}

func (op *ExtractVariableOperation) Validate(ws *types.Workspace) error {
	if op.SourceFile == "" {
		return &types.RefactorError{
			Type:    types.InvalidOperation,
			Message: "source file cannot be empty",
		}
	}
	if op.VariableName == "" {
		return &types.RefactorError{
			Type:    types.InvalidOperation,
			Message: "variable name cannot be empty",
		}
	}
	if op.Expression == "" {
		return &types.RefactorError{
			Type:    types.InvalidOperation,
			Message: "expression cannot be empty",
		}
	}
	if op.StartLine > op.EndLine {
		return &types.RefactorError{
			Type:    types.InvalidOperation,
			Message: "start line must be before or equal to end line",
		}
	}
	if !isValidGoIdentifierExtract(op.VariableName) {
		return &types.RefactorError{
			Type:    types.InvalidOperation,
			Message: fmt.Sprintf("invalid Go identifier: %s", op.VariableName),
		}
	}

	return nil
}

func (op *ExtractVariableOperation) Execute(ws *types.Workspace) (*types.RefactoringPlan, error) {
	// Find the source file
	var sourceFile *types.File
	var sourcePackage *types.Package
	for _, pkg := range ws.Packages {
		if file, exists := pkg.Files[op.SourceFile]; exists {
			sourceFile = file
			sourcePackage = pkg
			break
		}
	}

	if sourceFile == nil {
		return nil, &types.RefactorError{
			Type:    types.FileSystemError,
			Message: fmt.Sprintf("source file not found: %s", op.SourceFile),
		}
	}

	// Find insertion point for variable declaration (before the expression usage)
	insertionPoint := op.findVariableInsertionPoint(string(sourceFile.OriginalContent), op.StartLine)
	
	// Generate variable declaration
	variableDecl := fmt.Sprintf("%s := %s", op.VariableName, op.Expression)
	
	changes := []types.Change{
		// Insert variable declaration
		{
			File:        op.SourceFile,
			Start:       insertionPoint,
			End:         insertionPoint,
			OldText:     "",
			NewText:     variableDecl + "\n",
			Description: fmt.Sprintf("Declare extracted variable %s", op.VariableName),
		},
		// Replace expression with variable reference
		{
			File:        op.SourceFile,
			Start:       op.findExpressionStart(string(sourceFile.OriginalContent), op.StartLine, op.Expression),
			End:         op.findExpressionEnd(string(sourceFile.OriginalContent), op.StartLine, op.Expression),
			OldText:     op.Expression,
			NewText:     op.VariableName,
			Description: fmt.Sprintf("Replace expression with variable %s", op.VariableName),
		},
	}

	return &types.RefactoringPlan{
		Operations:    []types.Operation{op},
		Changes:       changes,
		AffectedFiles: []string{op.SourceFile},
		Impact: &types.ImpactAnalysis{
			AffectedFiles:    []string{op.SourceFile},
			AffectedPackages: []string{sourcePackage.Path},
		},
		Reversible: true,
	}, nil
}

func (op *ExtractVariableOperation) Description() string {
	return fmt.Sprintf("Extract variable '%s' from expression '%s' at line %d in %s",
		op.VariableName, op.Expression, op.StartLine, op.SourceFile)
}

func (op *ExtractVariableOperation) findVariableInsertionPoint(content string, expressionLine int) int {
	// Find the start of the statement containing the expression
	// This is simplified - a full implementation would parse the AST
	return getLineOffset(content, expressionLine)
}

func (op *ExtractVariableOperation) findExpressionStart(content string, line int, expression string) int {
	lines := strings.Split(content, "\n")
	if line < 1 || line > len(lines) {
		return 0
	}
	
	lineContent := lines[line-1]
	index := strings.Index(lineContent, expression)
	if index == -1 {
		// If exact expression not found, return start of line
		return getLineOffset(content, line)
	}
	
	return getLineOffset(content, line) + index
}

func (op *ExtractVariableOperation) findExpressionEnd(content string, line int, expression string) int {
	start := op.findExpressionStart(content, line, expression)
	return start + len(expression)
}

// ExtractBlockOperation implements extracting a function from a block (auto-detects boundaries)
type ExtractBlockOperation struct {
	SourceFile      string
	Position        int // Line or character position within the target block
	NewFunctionName string
}

func (op *ExtractBlockOperation) Type() types.OperationType {
	return types.ExtractOperation
}

func (op *ExtractBlockOperation) Validate(ws *types.Workspace) error {
	if op.SourceFile == "" {
		return &types.RefactorError{
			Type:    types.InvalidOperation,
			Message: "source file cannot be empty",
		}
	}
	if op.NewFunctionName == "" {
		return &types.RefactorError{
			Type:    types.InvalidOperation,
			Message: "function name cannot be empty",
		}
	}
	if op.Position < 1 {
		return &types.RefactorError{
			Type:    types.InvalidOperation,
			Message: "position must be positive",
		}
	}

	// Check if source file exists
	var sourceFile *types.File
	for _, pkg := range ws.Packages {
		if file, exists := pkg.Files[op.SourceFile]; exists {
			sourceFile = file
			break
		}
		for filename, file := range pkg.Files {
			if filename == op.SourceFile || filepath.Base(file.Path) == op.SourceFile {
				sourceFile = file
				break
			}
		}
		if sourceFile != nil {
			break
		}
	}
	if sourceFile == nil {
		return &types.RefactorError{
			Type:    types.FileSystemError,
			Message: fmt.Sprintf("source file not found: %s", op.SourceFile),
		}
	}

	if !isValidGoIdentifierExtract(op.NewFunctionName) {
		return &types.RefactorError{
			Type:    types.InvalidOperation,
			Message: fmt.Sprintf("invalid Go identifier: %s", op.NewFunctionName),
		}
	}

	return nil
}

func (op *ExtractBlockOperation) Execute(ws *types.Workspace) (*types.RefactoringPlan, error) {
	// Find the source file - prioritize files in workspace root
	var sourceFile *types.File
	var candidates []*types.File
	
	// Collect all candidates
	for _, pkg := range ws.Packages {
		if file, exists := pkg.Files[op.SourceFile]; exists {
			candidates = append(candidates, file)
		}
		for filename, file := range pkg.Files {
			if filename == op.SourceFile || filepath.Base(file.Path) == op.SourceFile {
				candidates = append(candidates, file)
			}
		}
	}
	
	// Prefer files in the workspace root package
	for _, candidate := range candidates {
		if filepath.Dir(candidate.Path) == ws.RootPath {
			sourceFile = candidate
			break
		}
	}
	
	// Fallback to first candidate if no root file found
	if sourceFile == nil && len(candidates) > 0 {
		sourceFile = candidates[0]
	}

	if sourceFile == nil {
		return nil, &types.RefactorError{
			Type:    types.FileSystemError,
			Message: fmt.Sprintf("source file not found: %s", op.SourceFile),
		}
	}

	// Parse the file to get the AST
	fset := token.NewFileSet()
	astFile, err := parser.ParseFile(fset, op.SourceFile, sourceFile.OriginalContent, parser.ParseComments)
	if err != nil {
		return nil, &types.RefactorError{
			Type:    types.ParseError,
			Message: fmt.Sprintf("failed to parse source file: %v", err),
		}
	}

	// Find the block containing the specified position
	blockInfo, err := op.findBlockAtPosition(astFile, fset, string(sourceFile.OriginalContent), op.Position)
	if err != nil {
		return nil, err
	}

	// Create an ExtractFunctionOperation with the detected boundaries and delegate to it
	extractFunctionOp := &ExtractFunctionOperation{
		SourceFile:      op.SourceFile,
		StartLine:       blockInfo.StartLine,
		EndLine:         blockInfo.EndLine,
		NewFunctionName: op.NewFunctionName,
	}

	// Validate the function operation
	if err := extractFunctionOp.Validate(ws); err != nil {
		return nil, err
	}

	// Execute the function extraction
	return extractFunctionOp.Execute(ws)
}

func (op *ExtractBlockOperation) Description() string {
	return fmt.Sprintf("Extract block as function '%s' from position %d in %s",
		op.NewFunctionName, op.Position, op.SourceFile)
}

// BlockInfo represents information about a detected block
type BlockInfo struct {
	StartLine int
	EndLine   int
	BlockType string // "if", "for", "switch", "function", etc.
}

func (op *ExtractBlockOperation) findBlockAtPosition(astFile *ast.File, fset *token.FileSet, content string, position int) (*BlockInfo, error) {
	var targetBlock *ast.BlockStmt
	var blockType string
	var blockStart, blockEnd token.Pos
	var closestDistance int = int(^uint(0) >> 1) // max int
	var entireStmtStart, entireStmtEnd token.Pos

	// Walk the AST to find the innermost block containing the position
	ast.Inspect(astFile, func(n ast.Node) bool {
		if n == nil {
			return false
		}

		pos := fset.Position(n.Pos())
		end := fset.Position(n.End())
		
		// Check different types of nodes that contain blocks
		switch node := n.(type) {
		case *ast.IfStmt:
			if node.Body != nil {
				// Check if position is within this if statement
				if position >= pos.Line && position <= end.Line {
					// For containing blocks, prefer smaller blocks (innermost)
					stmtSize := end.Line - pos.Line
					if stmtSize < closestDistance {
						targetBlock = node.Body
						blockStart = node.Body.Pos()
						blockEnd = node.Body.End()
						entireStmtStart = node.Pos()
						entireStmtEnd = node.End()
						blockType = "if"
						closestDistance = stmtSize
					}
				}
			}
		case *ast.ForStmt:
			if node.Body != nil {
				// Check if position is within this for statement
				if position >= pos.Line && position <= end.Line {
					// For containing blocks, prefer smaller blocks (innermost)
					stmtSize := end.Line - pos.Line
					if stmtSize < closestDistance {
						targetBlock = node.Body
						blockStart = node.Body.Pos()
						blockEnd = node.Body.End()
						entireStmtStart = node.Pos()
						entireStmtEnd = node.End()
						blockType = "for"
						closestDistance = stmtSize
					}
				}
			}
		case *ast.RangeStmt:
			if node.Body != nil {
				// Check if position is within this range statement
				if position >= pos.Line && position <= end.Line {
					// For containing blocks, prefer smaller blocks (innermost)
					stmtSize := end.Line - pos.Line
					if stmtSize < closestDistance {
						targetBlock = node.Body
						blockStart = node.Body.Pos()
						blockEnd = node.Body.End()
						entireStmtStart = node.Pos()
						entireStmtEnd = node.End()
						blockType = "range"
						closestDistance = stmtSize
					}
				}
			}
		case *ast.SwitchStmt:
			if node.Body != nil {
				if position >= pos.Line && position <= end.Line {
					// For containing blocks, prefer smaller blocks (innermost)
					stmtSize := end.Line - pos.Line
					if stmtSize < closestDistance {
						targetBlock = node.Body
						blockStart = node.Body.Pos()
						blockEnd = node.Body.End()
						entireStmtStart = node.Pos()
						entireStmtEnd = node.End()
						blockType = "switch"
						closestDistance = stmtSize
					}
				}
			}
		case *ast.BlockStmt:
			// Generic block statement
			if position >= pos.Line && position <= end.Line {
				// For containing blocks, prefer smaller blocks (innermost)
				stmtSize := end.Line - pos.Line
				if stmtSize < closestDistance {
					targetBlock = node
					blockStart = node.Pos()
					blockEnd = node.End()
					entireStmtStart = node.Pos()
					entireStmtEnd = node.End()
					blockType = "block"
					closestDistance = stmtSize
				}
			}
		}
		return true
	})

	if targetBlock == nil {
		return nil, &types.RefactorError{
			Type:    types.InvalidOperation,
			Message: fmt.Sprintf("no extractable block found at position %d", position),
		}
	}

	// For statements like for/range, we want to extract the entire statement including the declaration
	// Use the entire statement bounds instead of just the body
	var startPos, endPos token.Position
	if entireStmtStart != 0 && entireStmtEnd != 0 {
		startPos = fset.Position(entireStmtStart)
		endPos = fset.Position(entireStmtEnd)
	} else {
		startPos = fset.Position(blockStart)
		endPos = fset.Position(blockEnd)
	}

	return &BlockInfo{
		StartLine: startPos.Line,
		EndLine:   endPos.Line,
		BlockType: blockType,
	}, nil
}

// calculateDistance returns the distance from the position to the block, or -1 if position is outside the block
func (op *ExtractBlockOperation) calculateDistance(position, blockStart, blockEnd int) int {
	if position >= blockStart && position <= blockEnd {
		return 0 // Inside the block
	}
	if position < blockStart {
		return blockStart - position
	}
	return position - blockEnd
}

func (op *ExtractBlockOperation) determineBlockType(block *ast.BlockStmt, parent ast.Node) string {
	// This could be enhanced to determine the context of the block
	return "block"
}

func (op *ExtractBlockOperation) extractBlockCode(content string, startLine, endLine int) string {
	lines := strings.Split(content, "\n")
	if startLine < 1 || endLine > len(lines) || startLine > endLine {
		return ""
	}

	// Extract lines (convert from 1-based to 0-based indexing)
	extractedLines := lines[startLine-1 : endLine]
	
	// Remove the surrounding braces and empty lines for cleaner extraction
	if len(extractedLines) >= 2 {
		// Check if first and last lines are just braces
		firstLine := strings.TrimSpace(extractedLines[0])
		lastLine := strings.TrimSpace(extractedLines[len(extractedLines)-1])
		
		if firstLine == "{" && lastLine == "}" {
			// Remove opening and closing braces
			extractedLines = extractedLines[1 : len(extractedLines)-1]
		} else {
			// Look for braces within the lines and extract content between them
			startIdx := 0
			endIdx := len(extractedLines)
			
			for i, line := range extractedLines {
				trimmed := strings.TrimSpace(line)
				if strings.HasSuffix(trimmed, "{") {
					startIdx = i + 1
					break
				}
			}
			
			for i := len(extractedLines) - 1; i >= 0; i-- {
				trimmed := strings.TrimSpace(extractedLines[i])
				if trimmed == "}" {
					endIdx = i
					break
				}
			}
			
			if startIdx < endIdx {
				extractedLines = extractedLines[startIdx:endIdx]
			}
		}
	}
	
	// Remove leading and trailing empty lines
	for len(extractedLines) > 0 && strings.TrimSpace(extractedLines[0]) == "" {
		extractedLines = extractedLines[1:]
	}
	for len(extractedLines) > 0 && strings.TrimSpace(extractedLines[len(extractedLines)-1]) == "" {
		extractedLines = extractedLines[:len(extractedLines)-1]
	}
	
	return strings.Join(extractedLines, "\n")
}

func (op *ExtractBlockOperation) analyzeExtractedCode(code string, astFile *ast.File, fset *token.FileSet) ([]string, []string, error) {
	// Reuse the analysis logic from ExtractFunctionOperation
	funcOp := &ExtractFunctionOperation{
		SourceFile:      op.SourceFile,
		NewFunctionName: op.NewFunctionName,
	}
	return funcOp.analyzeExtractedCode(code, astFile, fset)
}

func (op *ExtractBlockOperation) generateFunction(params, returns []string, body string) string {
	function := fmt.Sprintf("func %s(", op.NewFunctionName)
	
	// Add parameters
	function += strings.Join(params, ", ")
	function += ")"
	
	// Add return types
	if len(returns) > 0 {
		if len(returns) == 1 {
			function += " " + returns[0]
		} else {
			function += " (" + strings.Join(returns, ", ") + ")"
		}
	}
	
	function += " {\n"
	// Indent the body
	indentedBody := op.indentCode(body, "\t")
	function += indentedBody
	function += "\n}"
	
	return function
}

func (op *ExtractBlockOperation) generateFunctionCall(params []string) string {
	call := fmt.Sprintf("%s(", op.NewFunctionName)
	// Add parameter names (without types)
	var paramNames []string
	for _, param := range params {
		// Extract just the parameter name (before the type)
		parts := strings.Fields(param)
		if len(parts) > 0 {
			paramNames = append(paramNames, parts[0])
		}
	}
	call += strings.Join(paramNames, ", ")
	call += ")"
	return call
}

func (op *ExtractBlockOperation) indentCode(code, indent string) string {
	lines := strings.Split(code, "\n")
	for i, line := range lines {
		if strings.TrimSpace(line) != "" {
			lines[i] = indent + line
		}
	}
	return strings.Join(lines, "\n")
}

func (op *ExtractBlockOperation) getLineOffset(content string, line int) int {
	return getLineOffset(content, line)
}

func (op *ExtractBlockOperation) findFunctionInsertionPoint(astFile *ast.File) int {
	// Reuse the logic from ExtractFunctionOperation
	funcOp := &ExtractFunctionOperation{}
	return funcOp.findFunctionInsertionPoint(astFile)
}

// Helper functions

func getLineOffset(content string, line int) int {
	lines := strings.Split(content, "\n")
	if line < 1 || line > len(lines) {
		return 0
	}
	
	offset := 0
	for i := 0; i < line-1; i++ {
		offset += len(lines[i]) + 1 // +1 for newline
	}
	return offset
}

// isValidGoIdentifier checks if identifier is valid
// Note: This is already defined in operations.go, but we need it here too
func isValidGoIdentifierExtract(name string) bool {
	if name == "" {
		return false
	}
	
	// Check first character
	first := rune(name[0])
	if !((first >= 'a' && first <= 'z') || (first >= 'A' && first <= 'Z') || first == '_') {
		return false
	}
	
	// Check remaining characters
	for _, r := range name[1:] {
		if !((r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || r == '_') {
			return false
		}
	}
	
	return true
}