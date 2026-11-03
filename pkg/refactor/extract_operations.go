package refactor

import (
	"fmt"
	"go/ast"
	gotypes "go/types"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"

	"github.com/mamaar/gorefactor/pkg/analysis"
	"github.com/mamaar/gorefactor/pkg/refactor/extractsig"
	"github.com/mamaar/gorefactor/pkg/types"
)

// ExtractMethodOperation implements extracting a method from a code block
type ExtractMethodOperation struct {
	SourceFile    string
	StartLine     int
	EndLine       int
	NewMethodName string
	TargetStruct  string
	Logger        *slog.Logger
	Parser        *analysis.GoParser
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

	sourceFile, _, err := ws.FindFile(op.SourceFile)
	if err != nil {
		return err
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

func (op *ExtractMethodOperation) resolveSourceFile(ws *types.Workspace) (*types.File, *types.Package, error) {
	file, pkg, err := ws.FindFile(op.SourceFile)
	if err != nil {
		return nil, nil, err
	}
	if file != nil {
		return file, pkg, nil
	}
	return nil, nil, &types.RefactorError{
		Type:    types.FileSystemError,
		Message: fmt.Sprintf("source file not found: %s", op.SourceFile),
	}
}

// resolveReceiver decides what the extracted method's receiver is.
//
// Preferred: one of the captured variables already has the target struct's
// type, so it becomes the receiver instead of a parameter. Passing it both
// ways — which the old implementation did — produced a method whose parameter
// shadowed its own receiver.
//
// Otherwise the receiver has to be synthesized, and the only value of that
// type reachable from the call site is the enclosing method's own receiver.
// When there is no such value the extraction is refused rather than emitting
// a call on an identifier that does not exist.
func (op *ExtractMethodOperation) resolveReceiver(an *extractsig.Analysis) (*extractReceiver, error) {
	for i, p := range an.Params {
		if p.Type == op.TargetStruct || p.Type == "*"+op.TargetStruct {
			return &extractReceiver{decl: p.Name + " " + p.Type, name: p.Name, paramIdx: i}, nil
		}
	}

	fn := an.EnclosingFunc
	if fn == nil || fn.Recv == nil || len(fn.Recv.List) == 0 || len(fn.Recv.List[0].Names) == 0 || fn.Recv.List[0].Names[0].Name == "_" {
		return nil, fmt.Errorf("no value of type %s is available at the call site: the block captures none and the enclosing function has no named receiver", op.TargetStruct)
	}
	base := receiverBaseTypeName(fn.Recv.List[0].Type)
	if base != op.TargetStruct {
		return nil, fmt.Errorf("the enclosing function's receiver has type %s, not %s, so the new method could not be called there", base, op.TargetStruct)
	}
	recvName := fn.Recv.List[0].Names[0].Name
	return &extractReceiver{decl: recvName + " *" + op.TargetStruct, name: recvName, paramIdx: -1}, nil
}

// receiverBaseTypeName returns the bare type name of a receiver expression,
// unwrapping the pointer and any generic type parameter list.
func receiverBaseTypeName(expr ast.Expr) string {
	for {
		switch t := expr.(type) {
		case *ast.StarExpr:
			expr = t.X
		case *ast.IndexExpr:
			expr = t.X
		case *ast.IndexListExpr:
			expr = t.X
		case *ast.ParenExpr:
			expr = t.X
		case *ast.Ident:
			return t.Name
		default:
			return ""
		}
	}
}

func (op *ExtractMethodOperation) Execute(ws *types.Workspace) (*types.RefactoringPlan, error) {
	defer func() {
		r := recover()
		if r == nil {
			return
		}
		logFile, _ := os.Create("/tmp/gorefactor-panic.log")
		if logFile != nil {
			_, _ = fmt.Fprintf(logFile, "PANIC in ExtractMethodOperation.Execute: %v\n", r)
			_, _ = fmt.Fprintf(logFile, "SourceFile: %s\n", op.SourceFile)
			_, _ = fmt.Fprintf(logFile, "StartLine: %d, EndLine: %d\n", op.StartLine, op.EndLine)
			_, _ = fmt.Fprintf(logFile, "NewMethodName: %s\n", op.NewMethodName)
			_, _ = fmt.Fprintf(logFile, "TargetStruct: %s\n", op.TargetStruct)
			_ = logFile.Close()
		}
		panic(fmt.Sprintf("ExtractMethodOperation panic: %v", r))
	}()

	sourceFile, sourcePackage, err := op.resolveSourceFile(ws)
	if err != nil {
		return nil, err
	}

	// Type-checking is lazy in this project: without this call the package
	// carries no TypesInfo and extractsig refuses outright.
	if op.Parser != nil {
		op.Parser.EnsureTypeChecked(ws, sourcePackage)
	}

	an, err := extractsig.Analyze(ws, sourcePackage, sourceFile, op.StartLine, op.EndLine)
	if err != nil {
		return nil, refuseExtraction("method", op.NewMethodName, err)
	}
	if selectionHasDefer(an.Stmts) {
		return nil, refuseExtraction("method", op.NewMethodName, errDeferInSelection)
	}

	recv, err := op.resolveReceiver(an)
	if err != nil {
		return nil, refuseExtraction("method", op.NewMethodName, err)
	}

	decl, call, err := renderExtraction(an, op.NewMethodName, recv)
	if err != nil {
		return nil, refuseExtraction("method", op.NewMethodName, err)
	}

	if op.Logger != nil {
		op.Logger.Info("extract method",
			"name", op.NewMethodName, "params", len(an.Params),
			"results", len(an.Results), "tail_returns", an.TailReturns)
	}

	// Reuse op.Parser's importer for verification, if there is one: it is
	// the same importer (see EnsureTypeChecked above) that already
	// type-checked ws, so package identity for anything it memoized stays
	// consistent with what verification sees. See buildVerifiedExtractPlan's
	// doc comment.
	var importer gotypes.Importer
	if op.Parser != nil {
		importer = op.Parser.Importer()
	}

	return buildVerifiedExtractPlan(ws, op, sourceFile, sourcePackage, an,
		op.StartLine, op.EndLine, "method", op.NewMethodName, decl, call, importer)
}

func (op *ExtractMethodOperation) Description() string {
	return fmt.Sprintf("Extract method '%s' from lines %d-%d in %s to %s",
		op.NewMethodName, op.StartLine, op.EndLine, op.SourceFile, op.TargetStruct)
}

// ExtractFunctionOperation implements extracting a function from a code block.
//
// Parser and Logger mirror ExtractMethodOperation: the parser is needed to
// type-check the source package on demand (type-checking is lazy, and
// extractsig refuses without type information).
type ExtractFunctionOperation struct {
	SourceFile      string
	StartLine       int
	EndLine         int
	NewFunctionName string
	Logger          *slog.Logger
	Parser          *analysis.GoParser
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

	_, _, err := op.resolveSourceFile(ws)
	if err != nil {
		return err
	}

	if !isValidGoIdentifierExtract(op.NewFunctionName) {
		return &types.RefactorError{
			Type:    types.InvalidOperation,
			Message: fmt.Sprintf("invalid Go identifier: %s", op.NewFunctionName),
		}
	}

	return nil
}

func (op *ExtractFunctionOperation) resolveSourceFile(ws *types.Workspace) (*types.File, *types.Package, error) {
	file, pkg, err := ws.FindFile(op.SourceFile)
	if err != nil {
		return nil, nil, err
	}
	if file != nil {
		return file, pkg, nil
	}
	return nil, nil, &types.RefactorError{
		Type:    types.FileSystemError,
		Message: fmt.Sprintf("source file not found: %s", op.SourceFile),
	}
}

func (op *ExtractFunctionOperation) Execute(ws *types.Workspace) (*types.RefactoringPlan, error) {
	sourceFile, sourcePackage, err := op.resolveSourceFile(ws)
	if err != nil {
		return nil, err
	}

	// Type-checking is lazy in this project: without this call the package
	// carries no TypesInfo and extractsig refuses outright.
	if op.Parser != nil {
		op.Parser.EnsureTypeChecked(ws, sourcePackage)
	}

	an, err := extractsig.Analyze(ws, sourcePackage, sourceFile, op.StartLine, op.EndLine)
	if err != nil {
		return nil, refuseExtraction("function", op.NewFunctionName, err)
	}
	if selectionHasDefer(an.Stmts) {
		return nil, refuseExtraction("function", op.NewFunctionName, errDeferInSelection)
	}

	decl, call, err := renderExtraction(an, op.NewFunctionName, nil)
	if err != nil {
		return nil, refuseExtraction("function", op.NewFunctionName, err)
	}

	if op.Logger != nil {
		op.Logger.Info("extract function",
			"name", op.NewFunctionName, "params", len(an.Params),
			"results", len(an.Results), "tail_returns", an.TailReturns)
	}

	// Reuse op.Parser's importer for verification, if there is one: it is
	// the same importer (see EnsureTypeChecked above) that already
	// type-checked ws, so package identity for anything it memoized stays
	// consistent with what verification sees. See buildVerifiedExtractPlan's
	// doc comment.
	var importer gotypes.Importer
	if op.Parser != nil {
		importer = op.Parser.Importer()
	}

	return buildVerifiedExtractPlan(ws, op, sourceFile, sourcePackage, an,
		op.StartLine, op.EndLine, "function", op.NewFunctionName, decl, call, importer)
}

func (op *ExtractFunctionOperation) Description() string {
	return fmt.Sprintf("Extract function '%s' from lines %d-%d in %s",
		op.NewFunctionName, op.StartLine, op.EndLine, op.SourceFile)
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
	if err := op.checkRequestFields(); err != nil {
		return err
	}

	// Find the source struct
	resolver := analysis.NewSymbolResolver(ws, slog.New(slog.NewTextHandler(io.Discard, nil)))
	var structSymbol *types.Symbol

	for _, pkg := range ws.Packages {
		if pkg.Symbols == nil {
			continue
		}
		symbol, err := resolver.ResolveSymbol(pkg, op.SourceStruct)
		if err != nil {
			continue
		}
		if symbol.Kind == types.TypeSymbol {
			structSymbol = symbol
			break
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
	sourcePackage, err2 := op.findStructPackage(ws, resolver)
	if err2 != nil {
		return err2
	}

	// Check methods in the package's symbol table
	err := op.checkMethodsExist(sourcePackage)
	if err != nil {
		return err
	}

	return nil
}

func (op *ExtractInterfaceOperation) checkRequestFields() error {
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
	return nil
}

func (op *ExtractInterfaceOperation) findStructPackage(ws *types.Workspace, resolver *analysis.SymbolResolver) (*types.Package, error) {
	var sourcePackage *types.Package
	for _, pkg := range ws.Packages {
		if pkg.Symbols == nil {
			continue
		}
		symbol, err := resolver.ResolveSymbol(pkg, op.SourceStruct)
		if err != nil {
			continue
		}
		if symbol.Kind == types.TypeSymbol {
			sourcePackage = pkg
			break
		}
	}

	if sourcePackage == nil {
		return nil, &types.RefactorError{
			Type:    types.SymbolNotFound,
			Message: fmt.Sprintf("package containing struct %s not found", op.SourceStruct),
		}
	}
	return sourcePackage, nil
}

func (op *ExtractInterfaceOperation) checkMethodsExist(sourcePackage *types.Package) error {
	for _, methodName := range op.Methods {
		found := false
		methods, exists := sourcePackage.Symbols.Methods[op.SourceStruct]
		if exists {
			for _, method := range methods {
				if method.Name == methodName {
					found = true
					break
				}
			}
		}
		if found {
			continue
		}
		return &types.RefactorError{
			Type:    types.SymbolNotFound,
			Message: fmt.Sprintf("method %s not found on struct %s", methodName, op.SourceStruct),
		}
	}
	return nil
}

func (op *ExtractInterfaceOperation) Execute(ws *types.Workspace) (*types.RefactoringPlan, error) {
	// Find the struct and its methods
	resolver := analysis.NewSymbolResolver(ws, slog.New(slog.NewTextHandler(io.Discard, nil)))
	var structSymbol *types.Symbol
	var sourcePackage *types.Package

	for _, pkg := range ws.Packages {
		if pkg.Symbols == nil {
			continue
		}
		symbol, err := resolver.ResolveSymbol(pkg, op.SourceStruct)
		if err != nil {
			continue
		}
		if symbol.Kind != types.TypeSymbol {
			continue
		}
		structSymbol = symbol
		sourcePackage = pkg
		break
	}

	if structSymbol == nil {
		return nil, &types.RefactorError{
			Type:    types.SymbolNotFound,
			Message: fmt.Sprintf("struct not found: %s", op.SourceStruct),
		}
	}

	// Generate interface definition
	interfaceCode := op.generateInterface(sourcePackage)

	// Determine target file (absolute path so the serializer can write it)
	targetFile := filepath.Join(sourcePackage.Dir, op.getTargetFileName())

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

func (op *ExtractInterfaceOperation) generateInterface(sourcePackage *types.Package) string {
	var interfaceCode strings.Builder
	fmt.Fprintf(&interfaceCode, "type %s interface {\n", op.InterfaceName)

	for _, methodName := range op.Methods {
		methods, exists := sourcePackage.Symbols.Methods[op.SourceStruct]
		if !exists {
			continue
		}
		for _, method := range methods {
			if method.Name != methodName {
				continue
			}
			// Extract method signature without receiver
			signature := op.extractMethodSignature(method.Signature)
			fmt.Fprintf(&interfaceCode, "\t%s%s\n", methodName, signature)
			break
		}
	}

	interfaceCode.WriteString("}")
	return interfaceCode.String()
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
		switch fullSignature[receiverEnd] {
		case '(':
			parenCount++
		case ')':
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
	sourceFile, sourcePackage, err := ws.FindFile(op.SourceFile)
	if err != nil {
		return nil, err
	}
	if sourceFile == nil {
		return nil, &types.RefactorError{
			Type:    types.FileSystemError,
			Message: fmt.Sprintf("source file not found: %s", op.SourceFile),
		}
	}

	// Resolve absolute path for the serializer
	absPath := sourceFile.Path

	// Find insertion point for variable declaration (before the expression usage)
	insertionPoint := op.findVariableInsertionPoint(string(sourceFile.OriginalContent), op.StartLine)

	// Generate variable declaration
	variableDecl := fmt.Sprintf("%s := %s", op.VariableName, op.Expression)

	changes := []types.Change{
		// Insert variable declaration
		{
			File:        absPath,
			Start:       insertionPoint,
			End:         insertionPoint,
			OldText:     "",
			NewText:     variableDecl + "\n",
			Description: fmt.Sprintf("Declare extracted variable %s", op.VariableName),
		},
		// Replace expression with variable reference
		{
			File:        absPath,
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
		AffectedFiles: []string{absPath},
		Impact: &types.ImpactAnalysis{
			AffectedFiles:    []string{absPath},
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
	if (first < 'a' || first > 'z') && (first < 'A' || first > 'Z') && first != '_' {
		return false
	}

	// Check remaining characters
	for _, r := range name[1:] {
		if (r < 'a' || r > 'z') && (r < 'A' || r > 'Z') && (r < '0' || r > '9') && r != '_' {
			return false
		}
	}

	return true
}
