package refactor

import (
	"errors"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"io"
	"log/slog"
	"slices"
	"strings"

	"github.com/mamaar/gorefactor/pkg/analysis"
	"github.com/mamaar/gorefactor/pkg/types"
)

// InlineMethodOperation implements inlining a method call with its implementation
type InlineMethodOperation struct {
	MethodName   string
	SourceStruct string
	TargetFile   string

	// CachedIndex, when set, limits the inlined calls to those it resolves to
	// the method.
	CachedIndex *analysis.ReferenceIndex
}

// dropAnalysis forgets the reference index the operation planned from.
func (op *InlineMethodOperation) dropAnalysis() { op.CachedIndex = nil }

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
	resolver := analysis.NewSymbolResolver(ws, slog.New(slog.NewTextHandler(io.Discard, nil)))
	var methodSymbol *types.Symbol
	var sourcePackage *types.Package

	for _, pkg := range ws.Packages {
		if pkg.Symbols == nil {
			continue
		}

		structSymbol, err := resolver.ResolveSymbol(pkg, op.SourceStruct)
		if err != nil {
			continue
		}

		if structSymbol.Kind == types.TypeSymbol {
			sourcePackage = pkg
			break
		}
	}

	if sourcePackage == nil {
		return &types.RefactorError{
			Type:    types.SymbolNotFound,
			Message: fmt.Sprintf("package containing struct %s not found", op.SourceStruct),
		}
	}

	// Check if method exists in the package's symbol table
	methods, exists := sourcePackage.Symbols.Methods[op.SourceStruct]
	if exists {
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

	_, _, err := ws.FindFile(op.TargetFile)
	if errors.Is(err, types.ErrFileNotFound) {
		return &types.RefactorError{
			Type:    types.FileSystemError,
			Message: fmt.Sprintf("target file not found: %s", op.TargetFile),
		}
	}

	if err != nil {
		return err
	}

	return nil
}

func (op *InlineMethodOperation) Execute(ws *types.Workspace) (*types.RefactoringPlan, error) {
	// Find the method implementation
	impl, err := op.findMethodImplementation(ws)
	if err != nil {
		return nil, err
	}

	// Find all calls to this method in the target file
	callLocations, err := op.findMethodCalls(ws)
	if err != nil {
		return nil, err
	}

	methodSymbol, _ := op.methodSymbol(ws)
	callLocations = referenceSites(ws, op.CachedIndex, methodSymbol).keep(op.TargetFile, callLocations)

	var changes []types.Change
	for _, location := range callLocations {
		// Replace method call with inlined implementation
		changes = append(changes, types.Change{
			File:        op.TargetFile,
			Start:       location.Start,
			End:         location.End,
			OldText:     location.CallText,
			NewText:     op.adaptMethodBodyForInlining(impl, location.Receiver, location.Arguments),
			Description: fmt.Sprintf("Inline method call %s", op.MethodName),
		})
	}

	_, sourcePackage, _ := ws.FindFile(op.TargetFile)

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

// MethodImpl holds the extracted method body and its parameter names.
type MethodImpl struct {
	Body       string
	ParamNames []string
	// Receiver is the method's receiver identifier, empty when it has none or is blank.
	// ReceiverType and ResultType are source text, ResultType empty for no result.
	Receiver     string
	ReceiverType string
	ResultType   string
}

type MethodCall struct {
	Start     int
	End       int
	CallText  string
	Arguments []string
	// Receiver is the source text of the expression the method is called on.
	Receiver string
	// NameOffset is the byte offset of the called identifier.
	NameOffset int
}

// callSites holds, per file, the offsets of the identifiers that resolve to
// the symbol being inlined. A nil callSites accepts every call.
type callSites map[string]map[int]bool

// referenceSites returns the sites idx resolves to sym, or nil without an index.
func referenceSites(ws *types.Workspace, idx *analysis.ReferenceIndex, sym *types.Symbol) callSites {
	if idx == nil || sym == nil {
		return nil
	}

	refs, err := quietResolver(ws).FindReferencesIndexed(sym, idx)
	if err != nil {
		return nil
	}

	sites := make(callSites)
	for _, ref := range refs {
		if sites[ref.File] == nil {
			sites[ref.File] = make(map[int]bool)
		}

		sites[ref.File][ref.Offset] = true
	}

	return sites
}

// count returns the number of sites.
func (c callSites) count() int {
	n := 0
	for _, offsets := range c {
		n += len(offsets)
	}

	return n
}

// keep returns the calls in file whose called identifier is one of the sites.
func (c callSites) keep(file string, calls []MethodCall) []MethodCall {
	if c == nil {
		return calls
	}
	var kept []MethodCall

	for _, call := range calls {
		if c[file][call.NameOffset] {
			kept = append(kept, call)
		}
	}

	return kept
}

func (op *InlineMethodOperation) findMethodImplementation(ws *types.Workspace) (MethodImpl, error) {
	methodSymbol, sourcePackage := op.methodSymbol(ws)
	if methodSymbol == nil {
		return MethodImpl{}, &types.RefactorError{
			Type:    types.SymbolNotFound,
			Message: fmt.Sprintf("method implementation not found: %s", op.MethodName),
		}
	}

	// Get the method body from the source file (pkg.Files is keyed by basename)
	file := findFileContainingSymbol(sourcePackage, methodSymbol)
	if file != nil {
		return op.extractMethodBody(string(file.OriginalContent), methodSymbol)
	}

	return MethodImpl{}, &types.RefactorError{
		Type:    types.SymbolNotFound,
		Message: fmt.Sprintf("method implementation not found: %s", op.MethodName),
	}
}

// methodSymbol returns the method to inline and the package of its struct,
// or nil when ws has no such method.
func (op *InlineMethodOperation) methodSymbol(ws *types.Workspace) (*types.Symbol, *types.Package) {
	resolver := quietResolver(ws)
	for _, pkg := range ws.Packages {
		if pkg.Symbols == nil {
			continue
		}

		structSymbol, err := resolver.ResolveSymbol(pkg, op.SourceStruct)
		if err != nil || structSymbol.Kind != types.TypeSymbol {
			continue
		}

		for _, method := range pkg.Symbols.Methods[op.SourceStruct] {
			if method.Name == op.MethodName {
				return method, pkg
			}
		}

		return nil, pkg
	}

	return nil, nil
}

func (op *InlineMethodOperation) extractMethodBody(content string, methodSymbol *types.Symbol) (MethodImpl, error) {
	// Parse the file to find the method body
	fset := token.NewFileSet()

	astFile, err := parser.ParseFile(fset, methodSymbol.File, content, parser.ParseComments)
	if err != nil {
		return MethodImpl{}, &types.RefactorError{
			Type:    types.ParseError,
			Message: fmt.Sprintf("failed to parse file: %v", err),
		}
	}

	// Find the method declaration
	for _, decl := range astFile.Decls {
		funcDecl, ok := decl.(*ast.FuncDecl)
		if !ok {
			continue
		}

		if funcDecl.Name.Name != op.MethodName || funcDecl.Recv == nil {
			continue
		}
		// Extract parameter names
		var paramNames []string

		if funcDecl.Type.Params != nil {
			for _, field := range funcDecl.Type.Params.List {
				for _, name := range field.Names {
					paramNames = append(paramNames, name.Name)
				}
			}
		}

		// Extract the body
		if funcDecl.Body == nil {
			continue
		}

		start := fset.Position(funcDecl.Body.Lbrace).Offset + 1

		end := fset.Position(funcDecl.Body.Rbrace).Offset
		if start >= len(content) || end > len(content) {
			continue
		}

		impl := MethodImpl{
			Body:         content[start:end],
			ParamNames:   paramNames,
			ReceiverType: sourceText(fset, content, funcDecl.Recv.List[0].Type),
		}

		names := funcDecl.Recv.List[0].Names
		if len(names) == 1 && names[0].Name != "_" {
			impl.Receiver = names[0].Name
		}

		if funcDecl.Type.Results != nil {
			impl.ResultType = sourceText(fset, content, funcDecl.Type.Results)
		}

		return impl, nil
	}

	return MethodImpl{}, &types.RefactorError{
		Type:    types.SymbolNotFound,
		Message: fmt.Sprintf("method body not found: %s", op.MethodName),
	}
}

func (op *InlineMethodOperation) findMethodCalls(ws *types.Workspace) ([]MethodCall, error) {
	targetFile, _, err := ws.FindFile(op.TargetFile)
	if errors.Is(err, types.ErrFileNotFound) {
		return nil, &types.RefactorError{
			Type:    types.FileSystemError,
			Message: fmt.Sprintf("target file not found: %s", op.TargetFile),
		}
	}

	if err != nil {
		return nil, fmt.Errorf("find method calls: %w", err)
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
		callExpr, ok := n.(*ast.CallExpr)
		if ok {
			selExpr, ok := callExpr.Fun.(*ast.SelectorExpr)
			if ok {
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
							Start:      start,
							End:        end,
							CallText:   callText,
							Arguments:  args,
							Receiver:   sourceText(fset, string(targetFile.OriginalContent), selExpr.X),
							NameOffset: fset.Position(selExpr.Sel.Pos()).Offset,
						})
					}
				}
			}
		}

		return true
	})

	return calls, nil
}

// sourceText is the text of node in content, which fset positions describe.
func sourceText(fset *token.FileSet, content string, node ast.Node) string {
	return content[fset.Position(node.Pos()).Offset:fset.Position(node.End()).Offset]
}

// receiverOperand is receiver spelled so a selector can follow it.
func receiverOperand(receiver string) string {
	expr, err := parser.ParseExpr(receiver)
	if err != nil {
		return "(" + receiver + ")"
	}

	switch expr.(type) {
	case *ast.Ident, *ast.SelectorExpr, *ast.CallExpr, *ast.IndexExpr, *ast.ParenExpr:
		return receiver
	}

	return "(" + receiver + ")"
}

// evaluatesCode reports whether evaluating receiver runs a call or a receive,
// so that evaluating it twice would not be the same as once.
func evaluatesCode(receiver string) bool {
	expr, err := parser.ParseExpr(receiver)
	if err != nil {
		return true
	}

	found := false

	ast.Inspect(expr, func(n ast.Node) bool {
		switch n := n.(type) {
		case *ast.CallExpr:
			found = true
		case *ast.UnaryExpr:
			found = found || n.Op == token.ARROW
		}

		return !found
	})

	return found
}

func (op *InlineMethodOperation) adaptMethodBodyForInlining(impl MethodImpl, receiver string, arguments []string) string {
	body := strings.TrimSpace(impl.Body)

	// Build param→arg substitution map
	paramToArg := make(map[string]string)

	for i, name := range impl.ParamNames {
		if i < len(arguments) {
			paramToArg[name] = arguments[i]
		}
	}

	if impl.Receiver != "" {
		paramToArg[impl.Receiver] = receiverOperand(receiver)
	}

	// Try to parse the body as a single return expression.
	// Wrap in a dummy function so it's valid Go.
	wrapped := "package p\nfunc _() {\n" + body + "\n}"
	fset := token.NewFileSet()

	f, err := parser.ParseFile(fset, "", wrapped, 0)
	if err == nil {
		funcDecl, ok := f.Decls[0].(*ast.FuncDecl)
		if ok && funcDecl.Body != nil {
			stmts := funcDecl.Body.List
			// Single return statement with one result → extract expression
			if len(stmts) == 1 {
				retStmt, ok := stmts[0].(*ast.ReturnStmt)
				if ok && len(retStmt.Results) == 1 {
					expr := retStmt.Results[0]
					if impl.Receiver != "" && countIdents(expr, impl.Receiver) > 1 && evaluatesCode(receiver) {
						delete(paramToArg, impl.Receiver)
						inner := replaceIdentsInExpr(expr, paramToArg, fset, wrapped)

						return fmt.Sprintf("func(%s %s) %s { return %s }(%s)", impl.Receiver, impl.ReceiverType, impl.ResultType, inner, receiver)
					}

					return replaceIdentsInExpr(expr, paramToArg, fset, wrapped)
				}
			}
		}
	}

	if err != nil {
		return body
	}

	return inlineStatements(impl, receiver, arguments, fset, wrapped, f.Decls[0].(*ast.FuncDecl).Body)
}

// inlineStatements substitutes the receiver and parameters in a statement body in one pass.
// Operands that run code, and a value receiver whose fields the body writes, are bound once
// to fresh names in a block.
func inlineStatements(impl MethodImpl, receiver string, arguments []string, fset *token.FileSet, src string, block *ast.BlockStmt) string {
	subst := make(map[string]string)
	var binds []string

	bind := func(name, expr string, force bool) {
		if !force && !evaluatesCode(expr) {
			subst[name] = receiverOperand(expr)
			return
		}

		temp := name + "_"
		for strings.Contains(src, temp) || strings.Contains(receiver, temp) || strings.Contains(strings.Join(arguments, "\x00"), temp) {
			temp += "_"
		}

		binds = append(binds, temp+" := "+expr)
		subst[name] = temp
	}
	if impl.Receiver != "" {
		bind(impl.Receiver, receiver, !strings.HasPrefix(impl.ReceiverType, "*") && writesFieldsOf(block, impl.Receiver))
	}

	for i, name := range impl.ParamNames {
		if i < len(arguments) {
			bind(name, arguments[i], false)
		}
	}

	text := replaceIdentsInExpr(block, subst, fset, src)

	text = strings.TrimSpace(text[1 : len(text)-1])
	if len(binds) == 0 {
		return text
	}

	return "{\n" + strings.Join(binds, "\n") + "\n" + text + "\n}"
}

// writesFieldsOf reports whether block assigns to or increments a field reached through name.
func writesFieldsOf(block *ast.BlockStmt, name string) bool {
	rooted := func(e ast.Expr) bool {
		for {
			switch x := e.(type) {
			case *ast.SelectorExpr:
				e = x.X
			case *ast.IndexExpr:
				e = x.X
			case *ast.ParenExpr:
				e = x.X
			case *ast.Ident:
				return x.Name == name && x.Obj == nil
			default:
				return false
			}
		}
	}
	found := false

	ast.Inspect(block, func(n ast.Node) bool {
		switch n := n.(type) {
		case *ast.AssignStmt:
			for _, lhs := range n.Lhs {
				_, plain := lhs.(*ast.Ident)
				found = found || !plain && rooted(lhs)
			}
		case *ast.IncDecStmt:
			found = found || rooted(n.X)
		}

		return !found
	})

	return found
}

// inspectIdents calls visit for each identifier in expr other than the
// selected name of a selector, which is not a use of any variable.
func inspectIdents(expr ast.Node, visit func(*ast.Ident)) {
	ast.Inspect(expr, func(n ast.Node) bool {
		switch n := n.(type) {
		case *ast.SelectorExpr:
			inspectIdents(n.X, visit)
			return false
		case *ast.Ident:
			visit(n)
		}

		return true
	})
}

func countIdents(expr ast.Expr, name string) int {
	count := 0

	inspectIdents(expr, func(id *ast.Ident) {
		if id.Name == name {
			count++
		}
	})

	return count
}

// replaceIdentsInExpr rewrites identifier tokens in an AST expression using the
// param→arg map and returns the resulting source text.
func replaceIdentsInExpr(expr ast.Node, paramToArg map[string]string, fset *token.FileSet, src string) string {
	// Collect identifiers that need replacing, in reverse order so offsets stay valid.
	type replacement struct {
		start, end int
		text       string
	}
	var replacements []replacement

	inspectIdents(expr, func(ident *ast.Ident) {
		arg, exists := paramToArg[ident.Name]
		if !exists || ident.Obj != nil {
			return
		}

		start := fset.Position(ident.Pos()).Offset
		end := fset.Position(ident.End()).Offset
		replacements = append(replacements, replacement{start, end, arg})
	})

	// Extract the expression text from src first
	exprStart := fset.Position(expr.Pos()).Offset
	exprEnd := fset.Position(expr.End()).Offset
	exprText := src[exprStart:exprEnd]

	// Apply replacements in reverse order (adjusted to be relative to exprText)
	for _, r := range slices.Backward(replacements) {
		relStart := r.start - exprStart
		relEnd := r.end - exprStart
		exprText = exprText[:relStart] + r.text + exprText[relEnd:]
	}

	return exprText
}

// replaceIdentToken does a token-aware replacement of identifier tokens in Go source text.
func replaceIdentToken(src, oldIdent, newText string) string {
	var result strings.Builder

	remainder := src
	for len(remainder) > 0 {
		idx := strings.Index(remainder, oldIdent)
		if idx < 0 {
			result.WriteString(remainder)
			break
		}
		// Check that the character before and after are not identifier characters
		before := idx == 0 || !isIdentChar(remainder[idx-1]) && remainder[idx-1] != '.'
		afterIdx := idx + len(oldIdent)

		after := afterIdx >= len(remainder) || !isIdentChar(remainder[afterIdx])
		if before && after {
			result.WriteString(remainder[:idx])
			result.WriteString(newText)

			remainder = remainder[afterIdx:]
		} else {
			result.WriteString(remainder[:afterIdx])
			remainder = remainder[afterIdx:]
		}
	}

	return result.String()
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

	_, sourcePackage, _ := ws.FindFile(op.SourceFile)

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
	sourceFile, _, err := ws.FindFile(op.SourceFile)
	if errors.Is(err, types.ErrFileNotFound) {
		return "", &types.RefactorError{
			Type:    types.FileSystemError,
			Message: fmt.Sprintf("source file not found: %s", op.SourceFile),
		}
	}

	if err != nil {
		return "", fmt.Errorf("find variable value: %w", err)
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
		genDecl, ok := n.(*ast.GenDecl)
		if ok && genDecl.Tok == token.VAR {
			for _, spec := range genDecl.Specs {
				valueSpec, ok := spec.(*ast.ValueSpec)
				if !ok {
					continue
				}

				for i, name := range valueSpec.Names {
					if name.Name != op.VariableName || i >= len(valueSpec.Values) {
						continue
					}
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

		// Also check for short variable declarations (var := value)
		assignStmt, ok := n.(*ast.AssignStmt)
		if ok && assignStmt.Tok == token.DEFINE {
			for i, lhs := range assignStmt.Lhs {
				ident, ok := lhs.(*ast.Ident)
				if !ok || ident.Name != op.VariableName {
					continue
				}

				if i >= len(assignStmt.Rhs) {
					continue
				}

				valueExpr := assignStmt.Rhs[i]
				start := fset.Position(valueExpr.Pos()).Offset

				end := fset.Position(valueExpr.End()).Offset
				if start < len(sourceFile.OriginalContent) && end <= len(sourceFile.OriginalContent) {
					value = string(sourceFile.OriginalContent[start:end])
					return false // Stop searching
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
	sourceFile, _, err := ws.FindFile(op.SourceFile)
	if errors.Is(err, types.ErrFileNotFound) {
		return nil, &types.RefactorError{
			Type:    types.FileSystemError,
			Message: fmt.Sprintf("source file not found: %s", op.SourceFile),
		}
	}

	if err != nil {
		return nil, fmt.Errorf("find variable references: %w", err)
	}

	fset := token.NewFileSet()

	astFile, err := parser.ParseFile(fset, op.SourceFile, sourceFile.OriginalContent, parser.ParseComments)
	if err != nil {
		return nil, &types.RefactorError{
			Type:    types.ParseError,
			Message: fmt.Sprintf("failed to parse source file: %v", err),
		}
	}

	// First pass: find the declaring ident's position so we can exclude it.
	var declPos token.Pos

	ast.Inspect(astFile, func(n ast.Node) bool {
		if declPos.IsValid() {
			return false
		}

		switch node := n.(type) {
		case *ast.GenDecl:
			if node.Tok != token.VAR {
				return true
			}

			for _, spec := range node.Specs {
				vs, ok := spec.(*ast.ValueSpec)
				if !ok {
					continue
				}

				for _, name := range vs.Names {
					if name.Name == op.VariableName {
						declPos = name.Pos()
						return false
					}
				}
			}
		case *ast.AssignStmt:
			if node.Tok != token.DEFINE {
				return true
			}

			for _, lhs := range node.Lhs {
				ident, ok := lhs.(*ast.Ident)
				if ok && ident.Name == op.VariableName {
					declPos = ident.Pos()
					return false
				}
			}
		}

		return true
	})

	// Second pass: collect all references, skipping the declaration ident.
	var references []VariableReference

	ast.Inspect(astFile, func(n ast.Node) bool {
		ident, ok := n.(*ast.Ident)
		if ok && ident.Name == op.VariableName {
			if declPos.IsValid() && ident.Pos() == declPos {
				return true // skip declaration
			}

			start := fset.Position(ident.Pos()).Offset
			end := fset.Position(ident.End()).Offset
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
	sourceFile, _, err := ws.FindFile(op.SourceFile)
	if errors.Is(err, types.ErrFileNotFound) {
		return 0, 0, &types.RefactorError{
			Type:    types.FileSystemError,
			Message: fmt.Sprintf("source file not found: %s", op.SourceFile),
		}
	}

	if err != nil {
		return 0, 0, fmt.Errorf("find variable declaration: %w", err)
	}

	fset := token.NewFileSet()

	astFile, err := parser.ParseFile(fset, op.SourceFile, sourceFile.OriginalContent, parser.ParseComments)
	if err != nil {
		return 0, 0, &types.RefactorError{
			Type:    types.ParseError,
			Message: fmt.Sprintf("failed to parse source file: %v", err),
		}
	}

	content := sourceFile.OriginalContent
	var declStart, declEnd int
	found := false

	ast.Inspect(astFile, func(n ast.Node) bool {
		if found {
			return false
		}

		switch node := n.(type) {
		case *ast.GenDecl:
			if node.Tok != token.VAR {
				return true
			}

			for _, spec := range node.Specs {
				valueSpec, ok := spec.(*ast.ValueSpec)
				if !ok {
					continue
				}

				for _, name := range valueSpec.Names {
					if name.Name != op.VariableName {
						continue
					}
					// Single spec in the declaration: remove the entire GenDecl
					if len(node.Specs) == 1 {
						declStart = fset.Position(node.Pos()).Offset
						declEnd = fset.Position(node.End()).Offset
					} else {
						// Multiple specs: remove just this ValueSpec
						declStart = fset.Position(valueSpec.Pos()).Offset
						declEnd = fset.Position(valueSpec.End()).Offset
					}
					// Extend to include trailing newline
					if declEnd < len(content) && content[declEnd] == '\n' {
						declEnd++
					}

					found = true

					return false
				}
			}
		case *ast.AssignStmt:
			if node.Tok != token.DEFINE {
				return true
			}

			for _, lhs := range node.Lhs {
				ident, ok := lhs.(*ast.Ident)
				if !ok || ident.Name != op.VariableName {
					continue
				}

				declStart = fset.Position(node.Pos()).Offset

				declEnd = fset.Position(node.End()).Offset
				if declEnd < len(content) && content[declEnd] == '\n' {
					declEnd++
				}

				found = true

				return false
			}
		}

		return true
	})

	if !found {
		return 0, 0, &types.RefactorError{
			Type:    types.SymbolNotFound,
			Message: fmt.Sprintf("variable declaration not found: %s", op.VariableName),
		}
	}

	return declStart, declEnd, nil
}

// InlineFunctionOperation implements inlining a function
type InlineFunctionOperation struct {
	FunctionName string
	SourceFile   string
	TargetFiles  []string

	// CachedIndex, when set, limits the inlined calls to those it resolves to
	// the function.
	CachedIndex *analysis.ReferenceIndex
}

// dropAnalysis forgets the reference index the operation planned from.
func (op *InlineFunctionOperation) dropAnalysis() { op.CachedIndex = nil }

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
	impl, err := op.findFunctionImplementation(ws)
	if err != nil {
		return nil, err
	}

	var changes []types.Change
	affectedFiles := make([]string, 0)

	// A bare call can only name the function from inside its own package.
	funcSymbol, sourcePackage, _ := op.functionSymbol(ws)

	sites := referenceSites(ws, op.CachedIndex, funcSymbol)
	for _, targetFile := range op.TargetFiles {
		_, targetPackage, _ := ws.FindFile(targetFile)
		if targetPackage != sourcePackage {
			continue
		}

		calls, err := op.findFunctionCalls(ws, targetFile)
		if err != nil {
			continue // Skip files with errors
		}

		calls = sites.keep(targetFile, calls)

		// Replace each call with inlined implementation
		for _, call := range calls {
			changes = append(changes, types.Change{
				File:        targetFile,
				Start:       call.Start,
				End:         call.End,
				OldText:     call.CallText,
				NewText:     op.adaptFunctionBodyForInlining(impl, call.Arguments),
				Description: fmt.Sprintf("Inline function call %s", op.FunctionName),
			})
		}

		if len(calls) > 0 {
			affectedFiles = append(affectedFiles, targetFile)
		}
	}

	if len(changes) == 0 && sites.count() > 0 {
		return nil, &types.RefactorError{
			Type: types.InvalidOperation,
			Message: fmt.Sprintf("cannot inline %s: it has %d references, but none is a direct call from the same package in the target files",
				op.FunctionName, sites.count()),
		}
	}

	var affectedPackages []string

	for _, file := range affectedFiles {
		pkg := findPackageForFile(ws, file)
		if pkg != nil {
			affectedPackages = append(affectedPackages, pkg.Path)
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

// functionSymbol returns the function to inline and the package holding
// op.SourceFile, or nil when that package declares no such function.
func (op *InlineFunctionOperation) functionSymbol(ws *types.Workspace) (*types.Symbol, *types.Package, error) {
	_, pkg, err := ws.FindFile(op.SourceFile)
	if errors.Is(err, types.ErrFileNotFound) {
		return nil, nil, nil
	}

	if err != nil || pkg.Symbols == nil {
		return nil, pkg, err
	}

	return pkg.Symbols.Functions[op.FunctionName], pkg, nil
}

func (op *InlineFunctionOperation) findFunctionImplementation(ws *types.Workspace) (MethodImpl, error) {
	funcSymbol, sourcePackage, err := op.functionSymbol(ws)
	if err != nil {
		return MethodImpl{}, fmt.Errorf("find function implementation: %w", err)
	}

	if funcSymbol == nil {
		return MethodImpl{}, &types.RefactorError{
			Type:    types.SymbolNotFound,
			Message: fmt.Sprintf("function implementation not found: %s", op.FunctionName),
		}
	}

	// Get the source file content
	file := findFileContainingSymbol(sourcePackage, funcSymbol)
	if file == nil {
		return MethodImpl{}, &types.RefactorError{
			Type:    types.SymbolNotFound,
			Message: fmt.Sprintf("source file not found for function: %s", op.FunctionName),
		}
	}

	content := string(file.OriginalContent)
	fset := token.NewFileSet()

	astFile, err := parser.ParseFile(fset, funcSymbol.File, content, parser.ParseComments)
	if err != nil {
		return MethodImpl{}, &types.RefactorError{
			Type:    types.ParseError,
			Message: fmt.Sprintf("failed to parse file: %v", err),
		}
	}

	// Find the function declaration (Recv == nil means it's a function, not a method)
	for _, decl := range astFile.Decls {
		funcDecl, ok := decl.(*ast.FuncDecl)
		if !ok || funcDecl.Name.Name != op.FunctionName || funcDecl.Recv != nil {
			continue
		}

		// Extract parameter names
		var paramNames []string

		if funcDecl.Type.Params != nil {
			for _, field := range funcDecl.Type.Params.List {
				for _, name := range field.Names {
					paramNames = append(paramNames, name.Name)
				}
			}
		}

		// Extract the body
		if funcDecl.Body == nil {
			continue
		}

		start := fset.Position(funcDecl.Body.Lbrace).Offset + 1

		end := fset.Position(funcDecl.Body.Rbrace).Offset
		if start >= len(content) || end > len(content) {
			continue
		}

		return MethodImpl{
			Body:       content[start:end],
			ParamNames: paramNames,
		}, nil
	}

	return MethodImpl{}, &types.RefactorError{
		Type:    types.SymbolNotFound,
		Message: fmt.Sprintf("function body not found: %s", op.FunctionName),
	}
}

func (op *InlineFunctionOperation) findFunctionCalls(ws *types.Workspace, targetFile string) ([]MethodCall, error) {
	file, _, err := ws.FindFile(targetFile)
	if errors.Is(err, types.ErrFileNotFound) {
		return nil, &types.RefactorError{
			Type:    types.FileSystemError,
			Message: fmt.Sprintf("target file not found: %s", targetFile),
		}
	}

	if err != nil {
		return nil, fmt.Errorf("find function calls: %w", err)
	}

	fset := token.NewFileSet()

	astFile, err := parser.ParseFile(fset, targetFile, file.OriginalContent, parser.ParseComments)
	if err != nil {
		return nil, &types.RefactorError{
			Type:    types.ParseError,
			Message: fmt.Sprintf("failed to parse target file: %v", err),
		}
	}

	var calls []MethodCall

	ast.Inspect(astFile, func(n ast.Node) bool {
		callExpr, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		// Look for bare function calls (Ident), not method calls (SelectorExpr)
		ident, ok := callExpr.Fun.(*ast.Ident)
		if !ok || ident.Name != op.FunctionName {
			return true
		}

		start := fset.Position(callExpr.Pos()).Offset

		end := fset.Position(callExpr.End()).Offset
		if start >= len(file.OriginalContent) || end > len(file.OriginalContent) {
			return true
		}

		callText := string(file.OriginalContent[start:end])

		var args []string

		for _, arg := range callExpr.Args {
			argStart := fset.Position(arg.Pos()).Offset

			argEnd := fset.Position(arg.End()).Offset
			if argStart < len(file.OriginalContent) && argEnd <= len(file.OriginalContent) {
				args = append(args, string(file.OriginalContent[argStart:argEnd]))
			}
		}

		calls = append(calls, MethodCall{
			Start:      start,
			End:        end,
			CallText:   callText,
			Arguments:  args,
			NameOffset: fset.Position(ident.Pos()).Offset,
		})

		return true
	})

	return calls, nil
}

func (op *InlineFunctionOperation) adaptFunctionBodyForInlining(impl MethodImpl, arguments []string) string {
	body := strings.TrimSpace(impl.Body)

	// Build param→arg substitution map
	paramToArg := make(map[string]string)

	for i, name := range impl.ParamNames {
		if i < len(arguments) {
			paramToArg[name] = arguments[i]
		}
	}

	// Try to parse the body as a single return expression.
	wrapped := "package p\nfunc _() {\n" + body + "\n}"
	fset := token.NewFileSet()

	f, err := parser.ParseFile(fset, "", wrapped, 0)
	if err == nil {
		funcDecl, ok := f.Decls[0].(*ast.FuncDecl)
		if ok && funcDecl.Body != nil {
			stmts := funcDecl.Body.List
			if len(stmts) == 1 {
				retStmt, ok := stmts[0].(*ast.ReturnStmt)
				if ok && len(retStmt.Results) == 1 {
					expr := retStmt.Results[0]
					result := replaceIdentsInExpr(expr, paramToArg, fset, wrapped)

					return result
				}
			}
		}
	}

	// Fallback: do textual param substitution on the whole body
	result := body
	for param, arg := range paramToArg {
		result = replaceIdentToken(result, param, arg)
	}

	return result
}
