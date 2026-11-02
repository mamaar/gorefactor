package refactor

import (
	"fmt"
	"go/ast"
	"go/token"
	gotypes "go/types"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/mamaar/gorefactor/pkg/analysis"
	pkgtypes "github.com/mamaar/gorefactor/pkg/types"
)

// Parameter represents a function parameter
type Parameter struct {
	Name string
	Type string
}

func (p Parameter) isNamed() bool { return p.Name != "" }

// ChangeSignatureRequest contains the inputs for a change-signature engine call.
type ChangeSignatureRequest struct {
	FunctionName         string
	SourceFile           string
	NewParams            []Parameter
	NewReturns           []string
	Scope                pkgtypes.RenameScope
	PropagateToInterface bool
	DefaultValue         string
	NewParamPosition     int
	CachedIndex          *analysis.ReferenceIndex

	// Subcommand distinguishes the four operations: add_param, remove_param,
	// add_return, remove_return. The engine uses it to:
	//   - preserve existing params when subcommand is add_return or remove_return
	//   - set NewReturnPosition / RemovedReturnIndex / DefaultReturnValue automatically
	Subcommand string
}

// operation returns the operation the request describes. The subcommand sets
// the return-change sentinels: -1 means "not a return change", since 0 would
// read as "insert at" or "remove" index 0.
func (req ChangeSignatureRequest) operation(logger *slog.Logger) *ChangeSignatureOperation {
	newParamPos := -1
	if req.DefaultValue != "" && req.NewParamPosition >= 0 {
		newParamPos = req.NewParamPosition
	}

	newReturnPosition := -1
	removedReturnIndex := -1
	defaultReturnValue := ""
	deref := false
	removeParam := false

	switch req.Subcommand {
	case "add_return":
		// NewReturns is the full new list, the added return last.
		if len(req.NewReturns) > 0 {
			newReturnPosition = len(req.NewReturns) - 1
			defaultReturnValue = zeroValueForType(req.NewReturns[newReturnPosition])
		}
	case "deref_param":
		deref = true
	case "remove_param":
		removeParam = true
	case "remove_return":
		// NewReturns is the list after removal, so the removed return was the one past its end.
		removedReturnIndex = len(req.NewReturns)
	}

	return &ChangeSignatureOperation{
		FunctionName:         req.FunctionName,
		SourceFile:           req.SourceFile,
		NewParams:            req.NewParams,
		NewReturns:           req.NewReturns,
		Scope:                req.Scope,
		PropagateToInterface: req.PropagateToInterface,
		DefaultValue:         req.DefaultValue,
		NewParamPosition:     newParamPos,
		NewReturnPosition:    newReturnPosition,
		RemovedReturnIndex:   removedReturnIndex,
		DefaultReturnValue:   defaultReturnValue,
		DerefParam:           deref,
		RemoveParam:          removeParam,
		CachedIndex:          req.CachedIndex,
		Logger:               logger,
	}
}

// ChangeSignatureOperation implements changing function/method signatures
type ChangeSignatureOperation struct {
	FunctionName string
	SourceFile   string
	NewParams    []Parameter
	NewReturns   []string
	// NamedReturns holds named return parameters (e.g. {Name:"secret", Type:"[]byte"}).
	// When non-nil it takes precedence over NewReturns in generateNewSignature so that
	// the original named-result names are preserved verbatim.  Populated automatically
	// by preserveExistingReturnsIfNeeded when the existing function uses named returns.
	NamedReturns         []Parameter
	Scope                pkgtypes.RenameScope     // PackageScope or WorkspaceScope
	PropagateToInterface bool                     // When targeting a concrete method, also update interface + siblings
	DefaultValue         string                   // Default value to use for new parameters at call sites
	NewParamPosition     int                      // Position where the new parameter was inserted (-1 if not an add-param operation)
	NewReturnPosition    int                      // Position where a new return type was inserted (-1 if N/A)
	RemovedReturnIndex   int                      // Which return was removed (-1 if N/A)
	DefaultReturnValue   string                   // Default value for new return statements (e.g., "", nil, 0)
	CachedIndex          *analysis.ReferenceIndex // Optional pre-built reference index for performance
	Logger               *slog.Logger             // Logger for progress reporting

	// DerefParam turns the one parameter whose type differs between the
	// declaration and NewParams from *T into T, rewriting the body and calls.
	DerefParam bool

	// RemoveParam drops the one parameter of the declaration that NewParams
	// lacks, and the argument for it at every call.
	RemoveParam bool

	altered       []AlteredDecl
	derefIdx      int
	derefParamObj gotypes.Object // the parameter at derefIdx in the function body
	derefSkips    []pkgtypes.SkippedSite
	derefNil      derefNilMode // how the body compares the dereferenced parameter with nil
	derefErr      error        // first call-site finding that aborts the whole change

	removedIdx      int
	removedCount    int      // parameters before the removal
	removedVariadic bool     // the last of them is variadic
	removeRefusals  []string // every site that stops a remove_param, reported together
}

// AlteredDecl is a declaration whose signature a ChangeSignatureOperation rewrites.
type AlteredDecl struct {
	Key  pkgtypes.SymbolKey
	File string
}

// AlteredDecls returns the declarations the executed operation rewrote: the
// target and, when propagating, the interface methods and implementations.
func (op *ChangeSignatureOperation) AlteredDecls() []AlteredDecl {
	return op.altered
}

func (op *ChangeSignatureOperation) recordAltered(pkg *pkgtypes.Package, recv, name, file string) {
	decl := AlteredDecl{Key: pkgtypes.SymbolKey{PkgPath: pkg.ImportPath, Recv: recv, Name: name}, File: file}
	if !slices.Contains(op.altered, decl) {
		op.altered = append(op.altered, decl)
	}
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
		if err := op.resolveSourceFile(ws); err != nil {
			return err
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
		// Also try to match by comparing file paths
		for filePath, file := range pkg.Files {
			if filePath == op.SourceFile || file.Path == op.SourceFile {
				sourceFile = file
				break
			}
		}
		if sourceFile != nil {
			break
		}
	}
	if sourceFile == nil {
		return &pkgtypes.RefactorError{
			Type:    pkgtypes.SymbolNotFound,
			Message: fmt.Sprintf("source file not found: %s", op.SourceFile),
		}
	}

	// Find the function
	functionNode := op.findFunction(sourceFile, op.FunctionName)
	if functionNode == nil {
		// If FunctionName is dotted, it may be an interface method (no FuncDecl)
		if strings.Contains(op.FunctionName, ".") {
			parts := strings.SplitN(op.FunctionName, ".", 2)
			if isInterfaceMethod(sourceFile, parts[0], parts[1]) {
				// Valid interface method — skip the nil-FuncDecl error
				goto validateParams
			}
		}
		return &pkgtypes.RefactorError{
			Type:    pkgtypes.SymbolNotFound,
			Message: fmt.Sprintf("function %s not found in %s", op.FunctionName, op.SourceFile),
		}
	}

validateParams:
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
	// Find the source file and package
	var sourceFile *pkgtypes.File
	var sourcePackage *pkgtypes.Package
	for _, pkg := range ws.Packages {
		if file, exists := pkg.Files[op.SourceFile]; exists {
			sourceFile = file
			sourcePackage = pkg
			break
		}
		// Also try to match by comparing file paths
		for filePath, file := range pkg.Files {
			if filePath == op.SourceFile || file.Path == op.SourceFile {
				sourceFile = file
				sourcePackage = pkg
				break
			}
		}
		if sourceFile != nil {
			break
		}
	}

	// Preserve existing return types if not explicitly provided (fixes add_param/remove_param dropping returns)
	if err := op.preserveExistingReturnsIfNeeded(sourceFile); err != nil {
		return nil, err
	}

	// Preserve existing params if not explicitly provided (fixes add_return/remove_return dropping params)
	if err := op.preserveExistingParamsIfNeeded(sourceFile); err != nil {
		return nil, err
	}

	plan := &pkgtypes.RefactoringPlan{
		Changes:       make([]pkgtypes.Change, 0),
		AffectedFiles: make([]string, 0),
		Reversible:    true,
	}
	op.altered = nil

	resolver, idx := op.initResolver(ws)

	// Parse FunctionName into typeName + methodName if dotted
	var typeName, methodName string
	isDotted := strings.Contains(op.FunctionName, ".")
	if isDotted {
		parts := strings.SplitN(op.FunctionName, ".", 2)
		typeName, methodName = parts[0], parts[1]
	}

	// Determine if target is an interface method
	isIface := isDotted && isInterfaceMethod(sourceFile, typeName, methodName)

	if op.Logger != nil {
		op.Logger.Info("processing signature change", "function", op.FunctionName, "is_interface", isIface)
	}

	if decl := op.findFunction(sourceFile, op.FunctionName); !isIface && decl != nil && hasTypeParams(decl) {
		return nil, fmt.Errorf("change_signature: %s declares type parameters or has a generic receiver, which change_signature does not support", op.FunctionName)
	}

	op.derefSkips = nil
	op.removeRefusals = nil
	if op.RemoveParam {
		if err := op.planRemoveParam(ws, sourcePackage, sourceFile, typeName, methodName, isIface); err != nil {
			return nil, err
		}
	}
	if op.DerefParam {
		if isIface || op.PropagateToInterface {
			return nil, fmt.Errorf("deref_param: interface methods and propagate are not supported")
		}
		decl := op.findFunction(sourceFile, op.FunctionName)
		if decl == nil {
			return nil, fmt.Errorf("deref_param: function %s not found", op.FunctionName)
		}
		idx, err := op.derefTarget(decl)
		if err != nil {
			return nil, err
		}
		op.derefIdx = idx
		if err := op.planDerefBody(ws, sourcePackage, sourceFile, decl, plan); err != nil {
			return nil, err
		}
	}

	// Collect all symbols whose references need call-site updates
	var allRefSymbols []*pkgtypes.Symbol
	var primaryFuncDecl *ast.FuncDecl // for impact analysis

	var members []*gotypes.Func
	if isDotted {
		var err error
		members, err = op.signatureMembers(ws, sourcePackage, typeName, methodName, isIface)
		if err != nil {
			return nil, err
		}
	}

	if isIface {
		var err error
		allRefSymbols, err = op.collectInterfaceChanges(ws, sourceFile, sourcePackage, typeName, methodName, members, resolver, plan)
		if err != nil {
			return nil, err
		}
	} else {
		var err error
		allRefSymbols, primaryFuncDecl, err = op.collectConcreteChanges(ws, sourceFile, sourcePackage, members, resolver, plan)
		if err != nil {
			return nil, err
		}
	}

	callSiteCount := op.updateCallSites(ws, sourcePackage, allRefSymbols, resolver, idx, plan)
	op.updateReturnStatements(ws, sourcePackage, allRefSymbols, resolver, idx, plan)
	op.updateAssignmentLHS(ws, sourcePackage, allRefSymbols, resolver, idx, plan)
	if op.derefErr != nil {
		return nil, op.derefErr
	}
	if len(op.removeRefusals) > 0 {
		return nil, fmt.Errorf("remove_param: %s cannot be changed:\n%s", op.FunctionName, strings.Join(op.removeRefusals, "\n"))
	}
	plan.SkippedSites = append(plan.SkippedSites, op.derefSkips...)
	op.addDefaultValueImports(ws, plan)
	op.buildImpactAnalysis(ws, plan, primaryFuncDecl, callSiteCount)

	return plan, nil
}

func (op *ChangeSignatureOperation) logger() *slog.Logger {
	if op.Logger != nil {
		return op.Logger
	}
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

func (op *ChangeSignatureOperation) initResolver(ws *pkgtypes.Workspace) (*analysis.SymbolResolver, *analysis.ReferenceIndex) {
	resolver := analysis.NewSymbolResolver(ws, op.logger())

	var idx *analysis.ReferenceIndex
	if op.CachedIndex != nil {
		idx = op.CachedIndex
	} else {
		idx = resolver.BuildReferenceIndex()
	}
	return resolver, idx
}

func (op *ChangeSignatureOperation) collectInterfaceChanges(
	ws *pkgtypes.Workspace, sourceFile *pkgtypes.File, sourcePackage *pkgtypes.Package,
	typeName string, methodName string, members []*gotypes.Func,
	resolver *analysis.SymbolResolver, plan *pkgtypes.RefactoringPlan,
) ([]*pkgtypes.Symbol, error) {
	logger := op.logger()
	logger.Info("updating interface method signature")
	ifaceChange, err := op.generateInterfaceMethodSignatureChange(ws, sourceFile, typeName, methodName)
	if err != nil {
		return nil, err
	}
	plan.Changes = append(plan.Changes, ifaceChange)
	op.recordAltered(sourcePackage, typeName, methodName, sourceFile.Path)
	if !contains(plan.AffectedFiles, sourceFile.Path) {
		plan.AffectedFiles = append(plan.AffectedFiles, sourceFile.Path)
	}

	var allRefSymbols []*pkgtypes.Symbol
	if ifaceMethodSym, err := resolver.ResolveSymbol(sourcePackage, op.FunctionName); err == nil {
		allRefSymbols = append(allRefSymbols, ifaceMethodSym)
	}
	memberSymbols, err := op.applyMembers(ws, members, plan)
	if err != nil {
		return nil, err
	}
	logger.Info("updated implementations", "count", len(members))
	return append(allRefSymbols, memberSymbols...), nil
}

func (op *ChangeSignatureOperation) collectConcreteChanges(
	ws *pkgtypes.Workspace, sourceFile *pkgtypes.File, sourcePackage *pkgtypes.Package,
	members []*gotypes.Func,
	resolver *analysis.SymbolResolver, plan *pkgtypes.RefactoringPlan,
) ([]*pkgtypes.Symbol, *ast.FuncDecl, error) {
	functionNode := op.findFunction(sourceFile, op.FunctionName)
	if functionNode == nil {
		return nil, nil, &pkgtypes.RefactorError{
			Type:    pkgtypes.SymbolNotFound,
			Message: fmt.Sprintf("function %s not found", op.FunctionName),
		}
	}

	newSignature := op.generateNewSignature(functionNode)
	oldSignature := op.extractCurrentSignature(functionNode)

	plan.Changes = append(plan.Changes, pkgtypes.Change{
		File:        sourceFile.Path,
		Start:       op.tokenPosToOffset(ws, functionNode.Type.Pos()),
		End:         op.tokenPosToOffset(ws, functionNode.Type.End()),
		OldText:     oldSignature,
		NewText:     newSignature,
		Description: fmt.Sprintf("Update signature of function %s", op.FunctionName),
	})
	plan.AffectedFiles = append(plan.AffectedFiles, sourceFile.Path)
	op.recordAltered(sourcePackage, receiverTypeName(functionNode), functionNode.Name.Name, sourceFile.Path)

	symbol, err := resolver.ResolveSymbol(sourcePackage, op.FunctionName)
	if err != nil {
		return nil, nil, err
	}
	memberSymbols, err := op.applyMembers(ws, members, plan)
	if err != nil {
		return nil, nil, err
	}
	return append([]*pkgtypes.Symbol{symbol}, memberSymbols...), functionNode, nil
}

func (op *ChangeSignatureOperation) updateCallSites(
	ws *pkgtypes.Workspace, sourcePackage *pkgtypes.Package,
	allRefSymbols []*pkgtypes.Symbol, resolver *analysis.SymbolResolver,
	idx *analysis.ReferenceIndex, plan *pkgtypes.RefactoringPlan,
) int {
	packagesToSearch := make(map[string]*pkgtypes.Package)
	if op.Scope == pkgtypes.PackageScope {
		packagesToSearch[sourcePackage.Path] = sourcePackage
	} else {
		packagesToSearch = ws.Packages
	}

	logger := op.logger()
	logger.Info("searching for references", "scope", op.Scope, "package_count", len(packagesToSearch))
	logger.Info("searching for call sites", "symbol_count", len(allRefSymbols))
	logger.Debug("symbol details")
	for i, sym := range allRefSymbols {
		logger.Debug("symbol", "index", i+1, "name", sym.Name, "package", sym.Package, "kind", sym.Kind)
	}

	seen := make(map[string]bool)
	totalRefs := 0
	for i, sym := range allRefSymbols {
		startTime := time.Now()
		logger.Info("finding references", "symbol", fmt.Sprintf("%s.%s", sym.Package, sym.Name), "progress", fmt.Sprintf("%d/%d", i+1, len(allRefSymbols)))

		references, err := resolver.FindReferencesIndexedFiltered(sym, idx, packagesToSearch)
		if err != nil {
			logger.Error("error finding references", "symbol", sym.Name, "err", err)
			continue
		}

		elapsed := time.Since(startTime)
		logger.Info("found references", "count", len(references), "elapsed", elapsed)
		totalRefs += len(references)

		updateStart := time.Now()
		updatedCount := 0
		for _, ref := range references {
			callChanges := op.updateCallSite(ref, ws)
			for _, c := range callChanges {
				key := fmt.Sprintf("%s:%d", c.File, c.Start)
				if seen[key] {
					continue
				}
				seen[key] = true
				plan.Changes = append(plan.Changes, c)
				updatedCount++
			}

			if !contains(plan.AffectedFiles, ref.File) {
				plan.AffectedFiles = append(plan.AffectedFiles, ref.File)
			}
		}
		updateElapsed := time.Since(updateStart)
		logger.Info("updated call sites", "count", updatedCount, "elapsed", updateElapsed)
	}
	logger.Info("signature change complete", "total_references", totalRefs, "changes", len(plan.Changes))

	return len(seen)
}

func (op *ChangeSignatureOperation) addDefaultValueImports(ws *pkgtypes.Workspace, plan *pkgtypes.RefactoringPlan) {
	if op.DefaultValue == "" {
		return
	}
	requiredImport := extractRequiredImport(op.DefaultValue)
	if requiredImport == "" {
		return
	}
	filesNeedingImport := make(map[string]bool)
	for _, c := range plan.Changes {
		if strings.Contains(c.Description, "call to") {
			filesNeedingImport[c.File] = true
		}
	}
	for filePath := range filesNeedingImport {
		if !hasImport(ws, filePath, requiredImport) {
			importChange := generateAddImportChange(ws, filePath, "", requiredImport)
			if importChange != nil {
				plan.Changes = append(plan.Changes, *importChange)
			}
		}
	}
}

func (op *ChangeSignatureOperation) buildImpactAnalysis(
	ws *pkgtypes.Workspace, plan *pkgtypes.RefactoringPlan,
	primaryFuncDecl *ast.FuncDecl, callSiteCount int,
) {
	if primaryFuncDecl != nil {
		plan.Impact = &pkgtypes.ImpactAnalysis{
			AffectedFiles:    plan.AffectedFiles,
			AffectedPackages: op.getAffectedPackages(ws, plan.AffectedFiles),
			PotentialIssues:  op.analyzeImpact(ws, primaryFuncDecl, callSiteCount),
		}
	} else {
		plan.Impact = &pkgtypes.ImpactAnalysis{
			AffectedFiles:    plan.AffectedFiles,
			AffectedPackages: op.getAffectedPackages(ws, plan.AffectedFiles),
			PotentialIssues: []pkgtypes.Issue{
				{
					Type:        pkgtypes.IssueCompilationError,
					Severity:    pkgtypes.Warning,
					Description: "Changing interface method signature is a breaking change that may cause compilation errors",
				},
			},
		}
	}
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

	// Support Type.Method syntax
	if strings.Contains(funcName, ".") {
		parts := strings.SplitN(funcName, ".", 2)
		typeName, methodName := parts[0], parts[1]
		return findFuncDeclForMethod(file, typeName, methodName)
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

func (op *ChangeSignatureOperation) tokenPosToOffset(ws *pkgtypes.Workspace, pos token.Pos) int {
	return ws.FileSet.Position(pos).Offset
}

func (op *ChangeSignatureOperation) generateNewSignature(funcDecl *ast.FuncDecl) string {
	var signature strings.Builder
	signature.WriteString("func ")

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
	named := slices.ContainsFunc(op.NewParams, Parameter.isNamed)
	for i, param := range op.NewParams {
		if i > 0 {
			signature.WriteString(", ")
		}
		if sharesTypeWithNext(funcDecl, op.NewParams, i) {
			signature.WriteString(param.Name)
			continue
		}
		switch {
		case param.Name != "":
			signature.WriteString(param.Name + " ")
		case named:
			signature.WriteString("_ ") // Go rejects a list that mixes named and unnamed
		}
		signature.WriteString(param.Type)
	}
	signature.WriteString(")")

	// Add return types.
	// NamedReturns ([]Parameter) takes precedence over NewReturns ([]string) so that
	// named result variables from the original signature are preserved verbatim.
	if len(op.NamedReturns) > 0 {
		signature.WriteString(" ")
		needsParens := len(op.NamedReturns) > 1 || op.NamedReturns[0].Name != ""
		if needsParens {
			signature.WriteString("(")
		}
		for i, ret := range op.NamedReturns {
			if i > 0 {
				signature.WriteString(", ")
			}
			if ret.Name != "" {
				signature.WriteString(ret.Name)
				signature.WriteString(" ")
			}
			signature.WriteString(ret.Type)
		}
		if needsParens {
			signature.WriteString(")")
		}
	} else if len(op.NewReturns) > 0 {
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

// keptGroup returns the declaration group `a, b T` of decl that p is an
// unchanged member of, and its position in the group.
func keptGroup(decl *ast.FuncDecl, p Parameter) (*ast.Field, int) {
	for _, f := range listOf(decl.Type.Params) {
		for k, n := range f.Names {
			if len(f.Names) > 1 && paramEqual(p, Parameter{Name: n.Name, Type: gotypes.ExprString(f.Type)}) {
				return f, k
			}
		}
	}
	return nil, 0
}

// sharesTypeWithNext reports whether params[i] and params[i+1] are adjacent,
// unchanged members of one declaration group, so the type is written once.
func sharesTypeWithNext(decl *ast.FuncDecl, params []Parameter, i int) bool {
	if i+1 >= len(params) {
		return false
	}
	f, k := keptGroup(decl, params[i])
	g, l := keptGroup(decl, params[i+1])
	return f != nil && f == g && l == k+1
}

func (op *ChangeSignatureOperation) extractCurrentSignature(funcDecl *ast.FuncDecl) string {
	// This is a simplified extraction - in a full implementation,
	// we would use go/format to properly format the signature
	var signature strings.Builder
	signature.WriteString("func ")

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
	case *ast.Ellipsis:
		return "..." + op.extractTypeString(t.Elt)
	case *ast.MapType:
		return "map[" + op.extractTypeString(t.Key) + "]" + op.extractTypeString(t.Value)
	case *ast.InterfaceType:
		return "interface{}"
	case *ast.FuncType:
		return "func(...)"
	case *ast.ChanType:
		return "chan " + op.extractTypeString(t.Value)
	default:
		return "interface{}" // Fallback
	}
}

func (op *ChangeSignatureOperation) updateCallSite(ref *pkgtypes.Reference, ws *pkgtypes.Workspace) []pkgtypes.Change {
	if op.Logger != nil {
		op.Logger.Debug("updateCallSite called", "ref.File", ref.File, "ref.Position", ref.Position)
	}

	// Find the file containing the reference
	// pkg.Files and pkg.TestFiles use just the filename as key, not the full path
	refFileName := filepath.Base(ref.File)
	var refFile *pkgtypes.File
	for _, pkg := range ws.Packages {
		// Check regular files first
		if file, exists := pkg.Files[refFileName]; exists {
			// Verify this is the right file by checking the full path
			if file.Path == ref.File {
				refFile = file
				if op.Logger != nil {
					op.Logger.Debug("Found reference file in package Files", "pkg", pkg.Path, "file", refFileName)
				}
				break
			}
		}
		// Also check test files
		if file, exists := pkg.TestFiles[refFileName]; exists {
			// Verify this is the right file by checking the full path
			if file.Path == ref.File {
				refFile = file
				if op.Logger != nil {
					op.Logger.Debug("Found reference file in package TestFiles", "pkg", pkg.Path, "file", refFileName)
				}
				break
			}
		}
	}
	if refFile == nil {
		// Debug: show what files are available
		if op.Logger != nil {
			op.Logger.Debug("Reference file not found, checking package files")
			for pkgPath, pkg := range ws.Packages {
				var fileKeys []string
				for fpath := range pkg.Files {
					fileKeys = append(fileKeys, fpath)
				}
				if len(fileKeys) > 0 && len(fileKeys) < 10 {
					op.Logger.Debug("Package files", "pkg", pkgPath, "files", fileKeys)
				} else if len(fileKeys) > 0 {
					op.Logger.Debug("Package has files", "pkg", pkgPath, "file_count", len(fileKeys))
				}
			}
		}
	}
	if refFile == nil || refFile.AST == nil {
		if op.Logger != nil {
			op.Logger.Debug("Reference file not found or has no AST",
				"refFile_nil", refFile == nil,
				"AST_nil", refFile == nil || refFile.AST == nil)
		}
		return nil
	}

	var changes []pkgtypes.Change

	// Find the call expression containing this reference
	foundCallExpr := false
	ast.Inspect(refFile.AST, func(n ast.Node) bool {
		if callExpr, ok := n.(*ast.CallExpr); ok {
			// Check if this call expression contains our reference
			if op.containsReference(callExpr, ref.Position) {
				foundCallExpr = true
				if op.Logger != nil {
					op.Logger.Debug("Found call expression containing reference",
						"callExpr.Pos", callExpr.Pos(),
						"callExpr.End", callExpr.End())
				}
				// Generate new call arguments based on new signature
				var info *gotypes.Info
				if op.DerefParam || op.RemoveParam {
					info = refFile.Package.TypesInfo
				}
				newCall := op.generateNewCall(callExpr, ref, ws, refFile.OriginalContent, info)
				if newCall != "" {
					start := op.tokenPosToOffset(ws, callExpr.Pos())
					end := op.tokenPosToOffset(ws, callExpr.End())
					oldText := string(refFile.OriginalContent[start:end])
					// Skip no-op changes: when only returns changed (add_return / remove_return),
					// the arguments are unchanged and the generated call equals the original text.
					// Emitting a no-op change creates overlapping ranges with the assignment-LHS
					// and expression-statement rewrites produced by updateAssignmentLHS.
					if oldText == newCall {
						if op.Logger != nil {
							op.Logger.Debug("Skipping no-op call site change", "call", oldText)
						}
					} else {
						if op.Logger != nil {
							op.Logger.Debug("Generated new call", "newCall", newCall)
						}
						changes = append(changes, pkgtypes.Change{
							File:        ref.File,
							Start:       start,
							End:         end,
							OldText:     oldText,
							NewText:     newCall,
							Description: fmt.Sprintf("Update call to %s with new signature", op.FunctionName),
						})
					}
				} else if op.Logger != nil {
					op.Logger.Debug("generateNewCall returned empty string")
				}
				return false
			}
		}
		return true
	})

	if !foundCallExpr {
		if op.RemoveParam {
			op.refuseRemove(ws, ref.Position, "reference is not a call, so its argument cannot be removed")
		}
		if op.DerefParam {
			op.skipSite(ref, "reference is not a call, so its argument cannot be rewritten", ref.Context)
		}
		if op.Logger != nil {
			op.Logger.Debug("No call expression found containing reference")
		}
	}

	return changes
}

func (op *ChangeSignatureOperation) containsReference(callExpr *ast.CallExpr, pos token.Pos) bool {
	// Check the specific identifier that names the called function,
	// not the entire Fun expression. This prevents matching outer
	// goroutine closures (FuncLit) that contain the target call in their body.
	switch fun := callExpr.Fun.(type) {
	case *ast.SelectorExpr:
		// e.g., s.Process(...) — check the "Process" Sel identifier
		return fun.Sel.Pos() <= pos && pos <= fun.Sel.End()
	case *ast.Ident:
		// e.g., Process(...) — check the identifier directly
		return fun.Pos() <= pos && pos <= fun.End()
	default:
		// FuncLit, IndexExpr, etc. — never a direct reference to the target function
		return false
	}
}

func (op *ChangeSignatureOperation) generateNewCall(callExpr *ast.CallExpr, ref *pkgtypes.Reference, ws *pkgtypes.Workspace, fileContent []byte, info *gotypes.Info) string {
	// Extract existing arguments from source text to preserve original formatting
	existingArgs := make([]string, len(callExpr.Args))
	for i, arg := range callExpr.Args {
		start := op.tokenPosToOffset(ws, arg.Pos())
		end := op.tokenPosToOffset(ws, arg.End())
		existingArgs[i] = string(fileContent[start:end])
	}

	if op.DerefParam {
		if reason := op.derefArgs(existingArgs, callExpr, ws, fileContent, info); reason != "" {
			op.skipSite(ref, reason, string(fileContent[op.tokenPosToOffset(ws, callExpr.Pos()):op.tokenPosToOffset(ws, callExpr.End())]))
			return ""
		}
	}

	if op.RemoveParam {
		args, reason := op.removeArg(callExpr, existingArgs, info)
		if reason != "" {
			op.refuseRemove(ws, callExpr.Pos(), reason)
			return ""
		}
		return op.callText(callExpr, ws, fileContent, strings.Join(args, ", "))
	}

	// Build new argument list; a trailing variadic parameter takes whatever
	// arguments the call passes, so only the fixed parameters are mapped one by one.
	newArgCount := len(op.NewParams)
	if op.isVariadic() {
		newArgCount--
	}
	newArgs := make([]string, newArgCount)

	// If this is an add-param operation (DefaultValue is set and NewParamPosition >= 0),
	// insert the default value at the specified position
	if op.DefaultValue != "" && op.NewParamPosition >= 0 {
		// Insert default value at the new parameter position
		for i := range newArgCount {
			switch {
			case i < op.NewParamPosition:
				// Arguments before the new parameter
				if i < len(existingArgs) {
					newArgs[i] = existingArgs[i]
				} else {
					newArgs[i] = zeroValueForType(op.NewParams[i].Type)
				}
			case i == op.NewParamPosition:
				// The new parameter - use default value
				newArgs[i] = op.DefaultValue
			default:
				// Arguments after the new parameter (shifted by 1)
				oldArgIdx := i - 1
				if oldArgIdx < len(existingArgs) {
					newArgs[i] = existingArgs[oldArgIdx]
				} else {
					newArgs[i] = zeroValueForType(op.NewParams[i].Type)
				}
			}
		}
	} else {
		// Original behavior: map existing args positionally
		for i := range newArgCount {
			if i < len(existingArgs) {
				newArgs[i] = existingArgs[i]
			} else {
				newArgs[i] = zeroValueForType(op.NewParams[i].Type)
			}
		}
	}

	if op.isVariadic() {
		start := newArgCount
		if op.DefaultValue != "" && op.NewParamPosition >= 0 && op.NewParamPosition < newArgCount {
			start-- // the added parameter shifted the variadic one
		}
		if start < len(existingArgs) {
			tail := slices.Clone(existingArgs[start:])
			if callExpr.Ellipsis.IsValid() {
				tail[len(tail)-1] += "..."
			}
			newArgs = append(newArgs, tail...)
		}
	}

	return op.callText(callExpr, ws, fileContent, strings.Join(newArgs, ", "))
}

// callText returns the call with its arguments replaced by argStr.
func (op *ChangeSignatureOperation) callText(callExpr *ast.CallExpr, ws *pkgtypes.Workspace, fileContent []byte, argStr string) string {
	// When FunctionName is "Repo.Save", use just the method name for calls
	funcName := op.FunctionName
	if strings.Contains(funcName, ".") {
		parts := strings.SplitN(funcName, ".", 2)
		funcName = parts[1]
	}

	if selectorExpr, ok := callExpr.Fun.(*ast.SelectorExpr); ok {
		// Method call - preserve receiver from source text
		recvStart := op.tokenPosToOffset(ws, selectorExpr.X.Pos())
		recvEnd := op.tokenPosToOffset(ws, selectorExpr.X.End())
		recvText := string(fileContent[recvStart:recvEnd])
		return fmt.Sprintf("%s.%s(%s)", recvText, funcName, argStr)
	}

	// Function call
	return fmt.Sprintf("%s(%s)", funcName, argStr)
}

// zeroValueForType returns a sensible zero-value literal for common Go types.
func zeroValueForType(typeName string) string {
	switch typeName {
	case "int", "int8", "int16", "int32", "int64",
		"uint", "uint8", "uint16", "uint32", "uint64",
		"byte", "rune", "float32", "float64",
		"complex64", "complex128", "uintptr":
		return "0"
	case "string":
		return `""`
	case "bool":
		return "false"
	case "error":
		return "nil"
	default:
		if strings.HasPrefix(typeName, "*") || strings.HasPrefix(typeName, "[]") ||
			strings.HasPrefix(typeName, "map[") || strings.HasPrefix(typeName, "func") ||
			strings.HasPrefix(typeName, "chan") {
			return "nil"
		}
		// For struct/interface/unknown types, use the type's zero value
		return typeName + "{}"
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

// --- Return type preservation helpers ---

// extractReturnTypes extracts return type strings from a FuncType.
func (op *ChangeSignatureOperation) extractReturnTypes(funcType *ast.FuncType) []string {
	if funcType.Results == nil || len(funcType.Results.List) == 0 {
		return nil
	}

	var returns []string
	for _, field := range funcType.Results.List {
		typeStr := op.extractTypeString(field.Type)
		if len(field.Names) > 0 {
			for range field.Names {
				returns = append(returns, typeStr)
			}
		} else {
			returns = append(returns, typeStr)
		}
	}
	return returns
}

// preserveExistingReturnsIfNeeded populates NewReturns (and NamedReturns when the
// existing function uses named result variables) from the existing signature when
// the caller did not explicitly provide return types.  This prevents add_param and
// remove_param from accidentally dropping return types or stripping named results.
func (op *ChangeSignatureOperation) preserveExistingReturnsIfNeeded(sourceFile *pkgtypes.File) error {
	if len(op.NewReturns) > 0 {
		return nil // User explicitly provided returns
	}

	// Check if this is an interface method
	if strings.Contains(op.FunctionName, ".") {
		parts := strings.SplitN(op.FunctionName, ".", 2)
		typeName, methodName := parts[0], parts[1]

		if isInterfaceMethod(sourceFile, typeName, methodName) {
			op.NewReturns = op.extractInterfaceMethodReturns(sourceFile, typeName, methodName)
			return nil
		}
	}

	// Extract from concrete function/method
	funcDecl := op.findFunction(sourceFile, op.FunctionName)
	if funcDecl != nil && funcDecl.Type != nil {
		op.NewReturns = op.extractReturnTypes(funcDecl.Type)
		// Also populate NamedReturns when the original signature uses named results.
		// NamedReturns preserves both the name and the type so that generateNewSignature
		// can emit e.g. "(secret []byte, err error)" instead of "([]byte, error)".
		// A bare "return" inside the body would be invalid if names are stripped.
		if hasNamedResults(funcDecl.Type) {
			op.NamedReturns = op.extractNamedReturnParams(funcDecl.Type)
		}
	}

	return nil
}

// hasNamedResults reports whether any result field in funcType has an explicit name.
func hasNamedResults(funcType *ast.FuncType) bool {
	if funcType.Results == nil {
		return false
	}
	for _, field := range funcType.Results.List {
		if len(field.Names) > 0 {
			return true
		}
	}
	return false
}

// extractNamedReturnParams extracts named return parameters as []Parameter,
// preserving both variable names and types (e.g. {Name:"err", Type:"error"}).
func (op *ChangeSignatureOperation) extractNamedReturnParams(funcType *ast.FuncType) []Parameter {
	if funcType.Results == nil {
		return nil
	}
	var result []Parameter
	for _, field := range funcType.Results.List {
		typeStr := op.extractTypeString(field.Type)
		if len(field.Names) == 0 {
			result = append(result, Parameter{Type: typeStr})
		} else {
			for _, name := range field.Names {
				result = append(result, Parameter{Name: name.Name, Type: typeStr})
			}
		}
	}
	return result
}

// preserveExistingParamsIfNeeded populates NewParams from the existing signature
// when the caller did not explicitly provide parameters. This prevents add_return
// and remove_return from accidentally dropping the parameter list.
func (op *ChangeSignatureOperation) preserveExistingParamsIfNeeded(sourceFile *pkgtypes.File) error {
	if len(op.NewParams) > 0 {
		return nil // Caller explicitly provided params
	}

	// For interface methods, extract params from the interface declaration.
	if strings.Contains(op.FunctionName, ".") {
		parts := strings.SplitN(op.FunctionName, ".", 2)
		typeName, methodName := parts[0], parts[1]
		if isInterfaceMethod(sourceFile, typeName, methodName) {
			op.NewParams = op.extractInterfaceMethodParams(sourceFile, typeName, methodName)
			return nil
		}
	}

	// Extract from concrete function/method.
	funcDecl := op.findFunction(sourceFile, op.FunctionName)
	if funcDecl != nil && funcDecl.Type != nil && funcDecl.Type.Params != nil {
		op.NewParams = op.extractParamList(funcDecl.Type.Params)
	}

	return nil
}

// extractParamList extracts Parameter values from an ast.FieldList.
func (op *ChangeSignatureOperation) extractParamList(params *ast.FieldList) []Parameter {
	if params == nil {
		return nil
	}
	var result []Parameter
	for _, field := range params.List {
		typeStr := op.extractTypeString(field.Type)
		if len(field.Names) == 0 {
			// Unnamed parameter
			result = append(result, Parameter{Type: typeStr})
		} else {
			for _, name := range field.Names {
				result = append(result, Parameter{Name: name.Name, Type: typeStr})
			}
		}
	}
	return result
}

// extractInterfaceMethodParams gets parameter list from an interface method definition.
func (op *ChangeSignatureOperation) extractInterfaceMethodParams(file *pkgtypes.File, typeName, methodName string) []Parameter {
	var result []Parameter
	ast.Inspect(file.AST, func(n ast.Node) bool {
		typeSpec, ok := n.(*ast.TypeSpec)
		if !ok || typeSpec.Name.Name != typeName {
			return true
		}
		ifaceType, ok := typeSpec.Type.(*ast.InterfaceType)
		if !ok || ifaceType.Methods == nil {
			return false
		}
		for _, field := range ifaceType.Methods.List {
			if len(field.Names) > 0 && field.Names[0].Name == methodName {
				if funcType, ok := field.Type.(*ast.FuncType); ok {
					result = op.extractParamList(funcType.Params)
				}
				return false
			}
		}
		return false
	})
	return result
}

// extractInterfaceMethodReturns gets return types from an interface method definition.
func (op *ChangeSignatureOperation) extractInterfaceMethodReturns(file *pkgtypes.File, typeName, methodName string) []string {
	var returns []string
	ast.Inspect(file.AST, func(n ast.Node) bool {
		typeSpec, ok := n.(*ast.TypeSpec)
		if !ok || typeSpec.Name.Name != typeName {
			return true
		}
		ifaceType, ok := typeSpec.Type.(*ast.InterfaceType)
		if !ok || ifaceType.Methods == nil {
			return false
		}
		for _, field := range ifaceType.Methods.List {
			if len(field.Names) > 0 && field.Names[0].Name == methodName {
				if funcType, ok := field.Type.(*ast.FuncType); ok {
					returns = op.extractReturnTypes(funcType)
				}
				return false
			}
		}
		return false
	})
	return returns
}

// --- Interface-aware helpers ---

// receiverTypeName extracts the receiver type name from a FuncDecl (stripping pointer).
func receiverTypeName(funcDecl *ast.FuncDecl) string {
	if funcDecl.Recv == nil || len(funcDecl.Recv.List) == 0 {
		return ""
	}
	return baseTypeName(funcDecl.Recv.List[0].Type)
}

// hasTypeParams reports whether decl is generic or a method on a generic type.
func hasTypeParams(decl *ast.FuncDecl) bool {
	if decl.Type.TypeParams.NumFields() > 0 {
		return true
	}
	if decl.Recv == nil || len(decl.Recv.List) == 0 {
		return false
	}
	recv := decl.Recv.List[0].Type
	if star, ok := recv.(*ast.StarExpr); ok {
		recv = star.X
	}
	switch recv.(type) {
	case *ast.IndexExpr, *ast.IndexListExpr:
		return true
	}
	return false
}

// baseTypeName strips the pointer star from a type expression and returns the ident name.
func baseTypeName(expr ast.Expr) string {
	switch t := expr.(type) {
	case *ast.Ident:
		return t.Name
	case *ast.StarExpr:
		return baseTypeName(t.X)
	case *ast.IndexExpr:
		return baseTypeName(t.X)
	case *ast.IndexListExpr:
		return baseTypeName(t.X)
	}
	return ""
}

// isInterfaceMethod checks whether typeName is an interface type in file that contains methodName.
func isInterfaceMethod(file *pkgtypes.File, typeName, methodName string) bool {
	if file == nil || file.AST == nil {
		return false
	}
	found := false
	ast.Inspect(file.AST, func(n ast.Node) bool {
		typeSpec, ok := n.(*ast.TypeSpec)
		if !ok || typeSpec.Name.Name != typeName {
			return true
		}
		ifaceType, ok := typeSpec.Type.(*ast.InterfaceType)
		if !ok || ifaceType.Methods == nil {
			return false
		}
		for _, field := range ifaceType.Methods.List {
			if len(field.Names) > 0 && field.Names[0].Name == methodName {
				found = true
				return false
			}
		}
		return false
	})
	return found
}

// findFuncDeclForMethod finds a FuncDecl with the given receiver type and method name.
func findFuncDeclForMethod(file *pkgtypes.File, typeName, methodName string) *ast.FuncDecl {
	if file == nil || file.AST == nil {
		return nil
	}
	var found *ast.FuncDecl
	ast.Inspect(file.AST, func(n ast.Node) bool {
		funcDecl, ok := n.(*ast.FuncDecl)
		if !ok || funcDecl.Name == nil || funcDecl.Name.Name != methodName {
			return true
		}
		if receiverTypeName(funcDecl) == typeName {
			found = funcDecl
			return false
		}
		return true
	})
	return found
}

// generateInterfaceMethodSignatureChange creates a Change that rewrites an interface method field.
func (op *ChangeSignatureOperation) generateInterfaceMethodSignatureChange(ws *pkgtypes.Workspace, file *pkgtypes.File, typeName, methodName string) (pkgtypes.Change, error) {
	var change pkgtypes.Change
	var found bool

	ast.Inspect(file.AST, func(n ast.Node) bool {
		typeSpec, ok := n.(*ast.TypeSpec)
		if !ok || typeSpec.Name.Name != typeName {
			return true
		}
		ifaceType, ok := typeSpec.Type.(*ast.InterfaceType)
		if !ok || ifaceType.Methods == nil {
			return false
		}
		for _, field := range ifaceType.Methods.List {
			if len(field.Names) == 0 || field.Names[0].Name != methodName {
				continue
			}
			oldText := extractInterfaceMethodSignature(op, field)
			newText := generateInterfaceMethodSignature(op, methodName)

			if op.Logger != nil {
				op.Logger.Debug("generateInterfaceMethodSignatureChange",
					"oldText", oldText,
					"newText", newText,
					"file", file.Path)
			}

			change = pkgtypes.Change{
				File:        file.Path,
				Start:       op.tokenPosToOffset(ws, field.Names[0].Pos()),
				End:         op.tokenPosToOffset(ws, field.End()),
				OldText:     oldText,
				NewText:     newText,
				Description: fmt.Sprintf("Update interface method signature %s.%s", typeName, methodName),
			}
			found = true
			return false
		}
		return false
	})

	if !found {
		return change, &pkgtypes.RefactorError{
			Type:    pkgtypes.SymbolNotFound,
			Message: fmt.Sprintf("interface method %s.%s not found", typeName, methodName),
		}
	}
	return change, nil
}

// extractInterfaceMethodSignature renders the current text of an interface method field,
// e.g. "Save(ctx context.Context, id string) error".
func extractInterfaceMethodSignature(op *ChangeSignatureOperation, field *ast.Field) string {
	var sig strings.Builder
	sig.WriteString(field.Names[0].Name)

	funcType, ok := field.Type.(*ast.FuncType)
	if !ok {
		return sig.String()
	}

	sig.WriteString("(")
	if funcType.Params != nil {
		for i, param := range funcType.Params.List {
			if i > 0 {
				sig.WriteString(", ")
			}
			if len(param.Names) > 0 {
				for j, name := range param.Names {
					if j > 0 {
						sig.WriteString(", ")
					}
					sig.WriteString(name.Name)
				}
				sig.WriteString(" ")
			}
			sig.WriteString(op.extractTypeString(param.Type))
		}
	}
	sig.WriteString(")")

	if funcType.Results != nil && len(funcType.Results.List) > 0 {
		sig.WriteString(" ")
		results := funcType.Results.List
		if len(results) == 1 && len(results[0].Names) == 0 {
			sig.WriteString(op.extractTypeString(results[0].Type))
		} else {
			sig.WriteString("(")
			for i, r := range results {
				if i > 0 {
					sig.WriteString(", ")
				}
				if len(r.Names) > 0 {
					for j, name := range r.Names {
						if j > 0 {
							sig.WriteString(", ")
						}
						sig.WriteString(name.Name)
					}
					sig.WriteString(" ")
				}
				sig.WriteString(op.extractTypeString(r.Type))
			}
			sig.WriteString(")")
		}
	}

	return sig.String()
}

// generateInterfaceMethodSignature renders the new method signature for an interface field.
func generateInterfaceMethodSignature(op *ChangeSignatureOperation, methodName string) string {
	var sig strings.Builder
	sig.WriteString(methodName)
	sig.WriteString("(")
	for i, param := range op.NewParams {
		if i > 0 {
			sig.WriteString(", ")
		}
		if param.Name != "" {
			sig.WriteString(param.Name)
			sig.WriteString(" ")
		}
		sig.WriteString(param.Type)
	}
	sig.WriteString(")")

	if len(op.NewReturns) > 0 {
		sig.WriteString(" ")
		if len(op.NewReturns) == 1 {
			sig.WriteString(op.NewReturns[0])
		} else {
			sig.WriteString("(")
			for i, ret := range op.NewReturns {
				if i > 0 {
					sig.WriteString(", ")
				}
				sig.WriteString(ret)
			}
			sig.WriteString(")")
		}
	}

	return sig.String()
}

// extractRequiredImport determines if a default value requires an import and returns the import path
func extractRequiredImport(defaultValue string) string {
	// Handle common patterns like "context.TODO()", "context.Background()", etc.
	if strings.HasPrefix(defaultValue, "context.") {
		return "context"
	}
	// Add more patterns as needed
	return ""
}

// hasImport checks if a file already has a specific import. It searches test
// files too (via findParsedFile) — reporting "no import" for a _test.go that
// actually has one would add a duplicate.
func hasImport(ws *pkgtypes.Workspace, filePath, importPath string) bool {
	file := findParsedFile(ws, filePath)
	if file == nil {
		return false
	}
	for _, imp := range file.AST.Imports {
		// Strip quotes from import path
		path := strings.Trim(imp.Path.Value, "\"")
		if path == importPath {
			return true
		}
	}
	return false
}

// --- Return-type change helpers ---

// resolveSourceFile auto-detects the source file when SourceFile is empty
// and FunctionName contains a dot (e.g., "MyInterface.MyMethod").
func (op *ChangeSignatureOperation) resolveSourceFile(ws *pkgtypes.Workspace) error {
	if !strings.Contains(op.FunctionName, ".") {
		return &pkgtypes.RefactorError{
			Type:    pkgtypes.InvalidOperation,
			Message: "source file is required for non-method function names",
		}
	}

	parts := strings.SplitN(op.FunctionName, ".", 2)
	typeName, methodName := parts[0], parts[1]

	// Search all packages for the type definition (interface or struct with that method)
	for _, pkg := range ws.Packages {
		for _, file := range pkg.Files {
			if file.AST == nil {
				continue
			}
			// Skip test files — prefer production files
			if strings.HasSuffix(file.Path, "_test.go") {
				continue
			}
			// Check if this file has the interface with that method
			if isInterfaceMethod(file, typeName, methodName) {
				op.SourceFile = file.Path
				return nil
			}
			// Check if this file has a concrete method receiver
			if findFuncDeclForMethod(file, typeName, methodName) != nil {
				op.SourceFile = file.Path
				return nil
			}
		}
	}

	// Fallback: check test files
	for _, pkg := range ws.Packages {
		for _, file := range pkg.TestFiles {
			if file == nil || file.AST == nil {
				continue
			}
			if isInterfaceMethod(file, typeName, methodName) {
				op.SourceFile = file.Path
				return nil
			}
			if findFuncDeclForMethod(file, typeName, methodName) != nil {
				op.SourceFile = file.Path
				return nil
			}
		}
	}

	return &pkgtypes.RefactorError{
		Type:    pkgtypes.SymbolNotFound,
		Message: fmt.Sprintf("could not find type %s with method %s in workspace", typeName, methodName),
	}
}

// updateReturnStatements updates return statements inside implementation method bodies
// when return types are added or removed.
func (op *ChangeSignatureOperation) updateReturnStatements(
	ws *pkgtypes.Workspace, sourcePackage *pkgtypes.Package,
	allRefSymbols []*pkgtypes.Symbol, resolver *analysis.SymbolResolver,
	idx *analysis.ReferenceIndex, plan *pkgtypes.RefactoringPlan,
) {
	if op.NewReturnPosition < 0 && op.RemovedReturnIndex < 0 {
		return // Not a return-type change operation
	}

	logger := op.logger()

	// Parse FunctionName to get method name
	var methodName string
	if strings.Contains(op.FunctionName, ".") {
		parts := strings.SplitN(op.FunctionName, ".", 2)
		methodName = parts[1]
	} else {
		methodName = op.FunctionName
	}

	// Find all implementation methods and update their return statements
	allFiles := collectAllWorkspaceFiles(ws)
	for _, file := range allFiles {
		if file == nil || file.AST == nil {
			continue
		}

		content, err := os.ReadFile(file.Path)
		if err != nil {
			continue
		}

		ast.Inspect(file.AST, func(n ast.Node) bool {
			funcDecl, ok := n.(*ast.FuncDecl)
			if !ok || funcDecl.Name == nil || funcDecl.Name.Name != methodName {
				return true
			}
			if funcDecl.Body == nil {
				return true
			}

			// Determine existing return count from the original signature
			existingReturnCount := countFieldListEntries(funcDecl.Type.Results)

			// For add-return: existing returns should match (NewReturns.length - 1)
			// For remove-return: existing returns should match (NewReturns.length + 1)
			var expectedExisting int
			if op.NewReturnPosition >= 0 {
				expectedExisting = len(op.NewReturns) - 1
			} else {
				expectedExisting = len(op.NewReturns) + 1
			}
			if existingReturnCount != expectedExisting {
				return true
			}

			// Walk the body for return statements, skipping nested FuncLit
			walkBodyForReturnStmts(funcDecl.Body, func(retStmt *ast.ReturnStmt) {
				if len(retStmt.Results) != existingReturnCount {
					return // bare return or mismatched count
				}

				retStart := ws.FileSet.Position(retStmt.Pos()).Offset
				retEnd := ws.FileSet.Position(retStmt.End()).Offset
				oldText := string(content[retStart:retEnd])

				retValues := splitReturnValueTexts(retStmt, content, ws)

				var newParts []string
				if op.NewReturnPosition >= 0 {
					// Add return value
					pos := op.NewReturnPosition
					if pos < 0 || pos >= len(retValues) {
						newParts = append(newParts, retValues...)
						newParts = append(newParts, op.DefaultReturnValue)
					} else {
						newParts = append(newParts, retValues[:pos]...)
						newParts = append(newParts, op.DefaultReturnValue)
						newParts = append(newParts, retValues[pos:]...)
					}
				} else {
					// Remove return value
					for i, v := range retValues {
						if i == op.RemovedReturnIndex {
							continue
						}
						newParts = append(newParts, v)
					}
				}

				var newText string
				if len(newParts) == 0 {
					newText = "return"
				} else {
					newText = "return " + strings.Join(newParts, ", ")
				}

				plan.Changes = append(plan.Changes, pkgtypes.Change{
					File:        file.Path,
					Start:       retStart,
					End:         retEnd,
					OldText:     oldText,
					NewText:     newText,
					Description: fmt.Sprintf("Update return statement in %s", methodName),
				})
				if !contains(plan.AffectedFiles, file.Path) {
					plan.AffectedFiles = append(plan.AffectedFiles, file.Path)
				}
			})

			logger.Debug("updated return statements", "method", funcDecl.Name.Name, "file", file.Path)
			return true
		})
	}
}

// callExprMatchesTarget returns true when the CallExpr's function identifier matches
// the target method/function name.  Handles both selector (pkg.Func / recv.Method)
// and plain identifier (Func) forms.
func callExprMatchesTarget(callExpr *ast.CallExpr, methodName string) bool {
	switch fun := callExpr.Fun.(type) {
	case *ast.SelectorExpr:
		return fun.Sel.Name == methodName
	case *ast.Ident:
		return fun.Name == methodName
	}
	return false
}

// updateAssignmentLHS updates the LHS of assignment statements at call sites
// when return types are added or removed (e.g., `a, b := obj.Method()`).
//
// For add_return it also handles two additional patterns:
//   - Expression statement: `f(x)` — the result was discarded.  Rewritten to
//     `_ = f(x)` (or `_, _ = f(x)` if already multi-return) so the new return is
//     explicitly ignored.
//   - Inline call nested inside another expression: these cannot be safely rewritten
//     (Go does not allow multi-return inside another expression) so they are recorded
//     in plan.SkippedSites for manual attention.
func (op *ChangeSignatureOperation) updateAssignmentLHS(
	ws *pkgtypes.Workspace, sourcePackage *pkgtypes.Package,
	allRefSymbols []*pkgtypes.Symbol, resolver *analysis.SymbolResolver,
	idx *analysis.ReferenceIndex, plan *pkgtypes.RefactoringPlan,
) {
	if op.NewReturnPosition < 0 && op.RemovedReturnIndex < 0 {
		return // Not a return-type change operation
	}

	// Parse FunctionName to get method name
	var methodName string
	if strings.Contains(op.FunctionName, ".") {
		parts := strings.SplitN(op.FunctionName, ".", 2)
		methodName = parts[1]
	} else {
		methodName = op.FunctionName
	}

	// Number of returns before this operation.
	var existingReturnCount int
	if op.NewReturnPosition >= 0 {
		existingReturnCount = len(op.NewReturns) - 1
	} else {
		existingReturnCount = len(op.NewReturns) + 1
	}

	allFiles := collectAllWorkspaceFiles(ws)
	for _, file := range allFiles {
		if file == nil || file.AST == nil {
			continue
		}

		content, err := os.ReadFile(file.Path)
		if err != nil {
			continue
		}

		// Build a set of all call expression positions that appear inside other
		// call expression argument lists.  Such sites cannot be automatically
		// rewritten and must be reported as SkippedSites.
		inlinedCallPositions := collectInlinedCallPositions(file.AST, methodName)

		ast.Inspect(file.AST, func(n ast.Node) bool {
			switch stmt := n.(type) {

			// --- assignment / short-variable-declaration -----------------------
			case *ast.AssignStmt:
				if len(stmt.Rhs) != 1 {
					return true
				}
				callExpr, ok := stmt.Rhs[0].(*ast.CallExpr)
				if !ok || !callExprMatchesTarget(callExpr, methodName) {
					return true
				}
				if len(stmt.Lhs) != existingReturnCount {
					return true
				}
				// Inline? Should not happen here (call is the whole RHS), but guard anyway.
				if inlinedCallPositions[callExpr.Pos()] {
					op.recordSkippedSite(plan, ws, file, callExpr, content, "inlined call in assignment RHS")
					return true
				}

				lhsExprs := make([]string, len(stmt.Lhs))
				for i, expr := range stmt.Lhs {
					start := ws.FileSet.Position(expr.Pos()).Offset
					end := ws.FileSet.Position(expr.End()).Offset
					lhsExprs[i] = strings.TrimSpace(string(content[start:end]))
				}

				newLHSParts := op.rebuildLHS(lhsExprs)

				lhsStart := ws.FileSet.Position(stmt.Lhs[0].Pos()).Offset
				lhsEnd := ws.FileSet.Position(stmt.Lhs[len(stmt.Lhs)-1].End()).Offset
				oldLHS := string(content[lhsStart:lhsEnd])

				if len(newLHSParts) == 0 {
					// Remove entire assignment (the call itself is already an expression statement)
					stmtStart := ws.FileSet.Position(stmt.Pos()).Offset
					stmtEnd := ws.FileSet.Position(stmt.End()).Offset
					callStart := ws.FileSet.Position(callExpr.Pos()).Offset
					callEnd := ws.FileSet.Position(callExpr.End()).Offset
					plan.Changes = append(plan.Changes, pkgtypes.Change{
						File:        file.Path,
						Start:       stmtStart,
						End:         stmtEnd,
						OldText:     string(content[stmtStart:stmtEnd]),
						NewText:     string(content[callStart:callEnd]),
						Description: fmt.Sprintf("Remove assignment LHS for call to %s", methodName),
					})
				} else {
					plan.Changes = append(plan.Changes, pkgtypes.Change{
						File:        file.Path,
						Start:       lhsStart,
						End:         lhsEnd,
						OldText:     oldLHS,
						NewText:     strings.Join(newLHSParts, ", "),
						Description: fmt.Sprintf("Update assignment LHS for call to %s", methodName),
					})
				}

				if !contains(plan.AffectedFiles, file.Path) {
					plan.AffectedFiles = append(plan.AffectedFiles, file.Path)
				}

			// --- expression statement (result discarded) -----------------------
			case *ast.ExprStmt:
				callExpr, ok := stmt.X.(*ast.CallExpr)
				if !ok || !callExprMatchesTarget(callExpr, methodName) {
					return true
				}
				// Only handle the add-return case: previously the result was unused;
				// now we must explicitly discard it.
				if op.NewReturnPosition < 0 {
					// remove-return: expression statement is fine — still no LHS needed
					return true
				}
				if inlinedCallPositions[callExpr.Pos()] {
					op.recordSkippedSite(plan, ws, file, callExpr, content, "inlined call in expression statement")
					return true
				}

				// Rewrite `f(x)` → `_ = f(x)` (or `_, _ = f(x)` for multiple returns).
				// Build new LHS blanks for all new returns.
				newReturnCount := len(op.NewReturns)
				blanks := make([]string, newReturnCount)
				for i := range blanks {
					blanks[i] = "_"
				}

				stmtStart := ws.FileSet.Position(stmt.Pos()).Offset
				stmtEnd := ws.FileSet.Position(stmt.End()).Offset
				oldStmt := string(content[stmtStart:stmtEnd])
				callStart := ws.FileSet.Position(callExpr.Pos()).Offset
				callEnd := ws.FileSet.Position(callExpr.End()).Offset
				callText := string(content[callStart:callEnd])
				newStmt := strings.Join(blanks, ", ") + " = " + callText

				plan.Changes = append(plan.Changes, pkgtypes.Change{
					File:        file.Path,
					Start:       stmtStart,
					End:         stmtEnd,
					OldText:     oldStmt,
					NewText:     newStmt,
					Description: fmt.Sprintf("Capture discarded return values of %s", methodName),
				})

				if !contains(plan.AffectedFiles, file.Path) {
					plan.AffectedFiles = append(plan.AffectedFiles, file.Path)
				}
			}

			return true
		})
	}
}

// rebuildLHS returns the new set of LHS variable names after adding or removing
// a return position, following op.NewReturnPosition / op.RemovedReturnIndex.
func (op *ChangeSignatureOperation) rebuildLHS(lhsExprs []string) []string {
	var newParts []string
	if op.NewReturnPosition >= 0 {
		pos := op.NewReturnPosition
		if pos < 0 || pos >= len(lhsExprs) {
			newParts = append(newParts, lhsExprs...)
			newParts = append(newParts, "_")
		} else {
			newParts = append(newParts, lhsExprs[:pos]...)
			newParts = append(newParts, "_")
			newParts = append(newParts, lhsExprs[pos:]...)
		}
	} else {
		for i, v := range lhsExprs {
			if i == op.RemovedReturnIndex {
				continue
			}
			newParts = append(newParts, v)
		}
	}
	return newParts
}

// collectInlinedCallPositions returns a set of token.Pos values for CallExpr
// nodes whose function identifier matches methodName and that appear as an
// argument inside another CallExpr.  Such sites cannot be expanded to a
// multi-return form (Go does not allow multi-return inside an expression) and
// must be reported as SkippedSites.
func collectInlinedCallPositions(astFile *ast.File, methodName string) map[token.Pos]bool {
	result := make(map[token.Pos]bool)
	ast.Inspect(astFile, func(n ast.Node) bool {
		outerCall, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		for _, arg := range outerCall.Args {
			innerCall, ok := arg.(*ast.CallExpr)
			if !ok {
				continue
			}
			if callExprMatchesTarget(innerCall, methodName) {
				result[innerCall.Pos()] = true
			}
		}
		return true
	})
	return result
}

// recordSkippedSite appends a SkippedSite entry to plan.SkippedSites.
func (op *ChangeSignatureOperation) recordSkippedSite(
	plan *pkgtypes.RefactoringPlan, ws *pkgtypes.Workspace,
	file *pkgtypes.File, callExpr *ast.CallExpr, content []byte, reason string,
) {
	pos := ws.FileSet.Position(callExpr.Pos())
	start := pos.Offset
	end := ws.FileSet.Position(callExpr.End()).Offset
	snippet := ""
	if start >= 0 && end <= len(content) && end > start {
		snippet = string(content[start:end])
	}
	plan.SkippedSites = append(plan.SkippedSites, pkgtypes.SkippedSite{
		File:    file.Path,
		Line:    pos.Line,
		Reason:  reason,
		Snippet: snippet,
	})
}

// walkBodyForReturnStmts walks a function body for ReturnStmt nodes, skipping FuncLit bodies.
func walkBodyForReturnStmts(body *ast.BlockStmt, fn func(*ast.ReturnStmt)) {
	ast.Inspect(body, func(n ast.Node) bool {
		if _, ok := n.(*ast.FuncLit); ok {
			return false // skip closures
		}
		if retStmt, ok := n.(*ast.ReturnStmt); ok {
			fn(retStmt)
		}
		return true
	})
}

// splitReturnValueTexts extracts the text of each return value from a ReturnStmt.
func splitReturnValueTexts(retStmt *ast.ReturnStmt, content []byte, ws *pkgtypes.Workspace) []string {
	values := make([]string, len(retStmt.Results))
	for i, result := range retStmt.Results {
		start := ws.FileSet.Position(result.Pos()).Offset
		end := ws.FileSet.Position(result.End()).Offset
		values[i] = strings.TrimSpace(string(content[start:end]))
	}
	return values
}

// countFieldListEntries counts the total number of entries in a FieldList.
func countFieldListEntries(fl *ast.FieldList) int {
	if fl == nil {
		return 0
	}
	count := 0
	for _, field := range fl.List {
		if len(field.Names) == 0 {
			count++
		} else {
			count += len(field.Names)
		}
	}
	return count
}

// collectAllWorkspaceFiles returns all files in the workspace (both regular and test files).
func collectAllWorkspaceFiles(ws *pkgtypes.Workspace) []*pkgtypes.File {
	var files []*pkgtypes.File
	for _, pkg := range ws.Packages {
		for _, f := range pkg.Files {
			files = append(files, f)
		}
		for _, f := range pkg.TestFiles {
			files = append(files, f)
		}
	}
	return files
}
