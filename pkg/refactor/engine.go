package refactor

import (
	"cmp"
	"errors"
	"fmt"
	gotypes "go/types"
	"io"
	"log/slog"
	"maps"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"

	"github.com/mamaar/gorefactor/pkg/analysis"
	"github.com/mamaar/gorefactor/pkg/types"
)

// Engine is the main interface for refactoring operations
type RefactorEngine interface {
	// Workspace management
	LoadWorkspace(path string) (*types.Workspace, error)
	SaveWorkspace(ws *types.Workspace) error

	// Refactoring operations
	MoveSymbol(ws *types.Workspace, req types.MoveSymbolRequest) (*types.RefactoringPlan, error)
	RenameSymbol(ws *types.Workspace, req types.RenameSymbolRequest) (*types.RefactoringPlan, error)
	RenamePackage(ws *types.Workspace, req types.RenamePackageRequest) (*types.RefactoringPlan, error)
	RenameMethod(ws *types.Workspace, req types.RenameMethodRequest) (*types.RefactoringPlan, error)
	RenameField(ws *types.Workspace, req types.RenameFieldRequest) (*types.RefactoringPlan, error)
	ThreadContext(ws *types.Workspace, req types.ThreadContextRequest) (*types.RefactoringPlan, error)
	ExtractMethod(ws *types.Workspace, req types.ExtractMethodRequest) (*types.RefactoringPlan, error)
	ExtractFunction(ws *types.Workspace, req types.ExtractFunctionRequest) (*types.RefactoringPlan, error)
	ExtractInterface(ws *types.Workspace, req types.ExtractInterfaceRequest) (*types.RefactoringPlan, error)
	ExtractVariable(ws *types.Workspace, req types.ExtractVariableRequest) (*types.RefactoringPlan, error)
	ExtractConstant(ws *types.Workspace, req types.ExtractConstantRequest) (*types.RefactoringPlan, error)
	InlineMethod(ws *types.Workspace, req types.InlineMethodRequest) (*types.RefactoringPlan, error)
	InlineVariable(ws *types.Workspace, req types.InlineVariableRequest) (*types.RefactoringPlan, error)
	InlineFunction(ws *types.Workspace, req types.InlineFunctionRequest) (*types.RefactoringPlan, error)
	SafeDelete(ws *types.Workspace, req types.SafeDeleteRequest) (*types.RefactoringPlan, error)
	ChangeSignature(ws *types.Workspace, req ChangeSignatureRequest) (*types.RefactoringPlan, error)

	// Bulk operations
	MovePackage(ws *types.Workspace, req types.MovePackageRequest) (*types.RefactoringPlan, error)
	MoveDir(ws *types.Workspace, req types.MoveDirRequest) (*types.RefactoringPlan, error)
	MovePackages(ws *types.Workspace, req types.MovePackagesRequest) (*types.RefactoringPlan, error)

	// Facade operations
	CreateFacade(ws *types.Workspace, req types.CreateFacadeRequest) (*types.RefactoringPlan, error)
	GenerateFacades(ws *types.Workspace, req types.GenerateFacadesRequest) (*types.RefactoringPlan, error)
	UpdateFacades(ws *types.Workspace, req types.UpdateFacadesRequest) (*types.RefactoringPlan, error)

	// Import alias operations
	CleanAliases(ws *types.Workspace, req types.CleanAliasesRequest) (*types.RefactoringPlan, error)
	StandardizeImports(ws *types.Workspace, req types.StandardizeImportsRequest) (*types.RefactoringPlan, error)
	ResolveAliasConflicts(ws *types.Workspace, req types.ResolveAliasConflictsRequest) (*types.RefactoringPlan, error)
	ConvertAliases(ws *types.Workspace, req types.ConvertAliasesRequest) (*types.RefactoringPlan, error)

	// Dependency graph operations
	MoveByDependencies(ws *types.Workspace, req types.MoveByDependenciesRequest) (*types.RefactoringPlan, error)
	OrganizeByLayers(ws *types.Workspace, req types.OrganizeByLayersRequest) (*types.RefactoringPlan, error)
	AnalyzeDependencies(ws *types.Workspace, req types.AnalyzeDependenciesRequest) (*types.RefactoringPlan, error)

	// Batch operations
	BatchOperations(ws *types.Workspace, req types.BatchOperationRequest) (*types.RefactoringPlan, error)

	// Analysis
	ValidateRefactoring(plan *types.RefactoringPlan) error
	EnsureTypeChecked(ws *types.Workspace)
	EnsureTypeCheckedPackage(ws *types.Workspace, pkgPath string)

	// Execution
	ExecutePlan(plan *types.RefactoringPlan) error
	PreviewPlan(plan *types.RefactoringPlan) (string, error)
}

// DefaultEngine implements the Engine interface
type DefaultEngine struct {
	set        *analysis.WorkspaceSet
	validator  *Validator
	serializer *Serializer
	config     *EngineConfig
	logger     *slog.Logger

	// configRuns serializes the runs across build configurations.
	configRuns sync.Mutex
}

// EngineConfig contains configuration options for the refactoring engine
type EngineConfig struct {
	SkipCompilation bool
	AllowBreaking   bool
	// BuildConfig overrides the parser's host build configuration when set.
	BuildConfig *analysis.BuildConfig
}

// DefaultConfig returns the default engine configuration
func DefaultConfig() *EngineConfig {
	return &EngineConfig{
		SkipCompilation: false,
		AllowBreaking:   false,
	}
}

func CreateEngineWithConfig(config *EngineConfig, logger *slog.Logger) RefactorEngine {
	return &DefaultEngine{
		validator:  NewValidator(logger),
		serializer: NewSerializer(),
		config:     config,
		logger:     logger,
	}
}

// primaryConfig returns the build configuration workspaces are loaded under.
func (e *DefaultEngine) primaryConfig() analysis.BuildConfig {
	if e.config != nil && e.config.BuildConfig != nil {
		return *e.config.BuildConfig
	}
	return analysis.DefaultBuildConfig()
}

// viewOf returns the view that owns ws, the primary view for a workspace the
// set does not know, or nil before LoadWorkspace.
func (e *DefaultEngine) viewOf(ws *types.Workspace) *analysis.View {
	if e.set == nil {
		return nil
	}
	v := e.set.ViewOf(ws)
	if v != nil {
		return v
	}
	return e.set.Primary()
}

func (e *DefaultEngine) parserFor(ws *types.Workspace) *analysis.GoParser {
	v := e.viewOf(ws)
	if v != nil {
		return v.Parser
	}
	return nil
}

// WorkspaceSet returns the per-build-configuration views of the loaded
// workspace, or nil before LoadWorkspace.
func (e *DefaultEngine) WorkspaceSet() *analysis.WorkspaceSet { return e.set }

// LoadWorkspace loads a workspace and returns its primary view's workspace.
func (e *DefaultEngine) LoadWorkspace(path string) (*types.Workspace, error) {
	e.logger.Info("loading workspace", "path", path)

	set, err := analysis.LoadWorkspaceSet(path, e.primaryConfig(), e.logger)
	if err != nil {
		e.logger.Error("workspace loading failed", "path", path, "err", err)
		return nil, fmt.Errorf("failed to parse workspace: %w", err)
	}
	e.set = set
	workspace := set.Primary().Workspace

	// Configure import ordering with module info
	if workspace.Module != nil {
		workspaceModules, _ := DiscoverWorkspaceModules(workspace.RootPath)
		// Filter out the current module from workspace modules
		var filtered []string
		for _, wm := range workspaceModules {
			if wm != workspace.Module.Path {
				filtered = append(filtered, wm)
			}
		}
		e.serializer.SetModuleInfo(workspace.Module.Path, filtered)
	}

	return workspace, nil
}

// SaveWorkspace saves all changes in the workspace to disk
func (e *DefaultEngine) SaveWorkspace(ws *types.Workspace) error {
	var allChanges []types.Change

	// Collect all pending changes from all files
	for _, pkg := range ws.Packages {
		for _, file := range pkg.Files {
			if len(file.Modifications) > 0 {
				changes := e.modificationsToChanges(file.Modifications, file.Path)
				allChanges = append(allChanges, changes...)
			}
		}
	}

	if len(allChanges) == 0 {
		return nil // No changes to save
	}

	// Apply all changes
	return e.serializer.ApplyChanges(allChanges)
}

// MoveSymbol implements symbol moving between packages
func (e *DefaultEngine) MoveSymbol(ws *types.Workspace, req types.MoveSymbolRequest) (*types.RefactoringPlan, error) {
	// Apply sensible defaults
	req.CreateTarget = true
	req.UpdateTests = true

	operation := &MoveSymbolOperation{Request: req}
	e.EnsureTypeChecked(ws)

	// Validate the operation
	err := operation.Validate(ws)
	if err != nil {
		return nil, fmt.Errorf("move operation validation failed: %w", err)
	}

	// Execute the operation to generate the plan
	plan, err := operation.Execute(ws)
	if err != nil {
		return nil, fmt.Errorf("failed to generate move plan: %w", err)
	}

	plan.Operations = []types.Operation{operation}

	return plan, nil
}

// MoveSymbolAcrossConfigs moves a symbol like MoveSymbol and checks the plan
// under every build configuration covering the workspace. It refuses when a
// configuration declares the symbol elsewhere or not at all, references it
// where the plan rewrites nothing, has the name taken in the target package,
// or would get an import cycle.
func (e *DefaultEngine) MoveSymbolAcrossConfigs(req types.MoveSymbolRequest) (*types.RefactoringPlan, error) {
	req.CreateTarget = true
	req.UpdateTests = true
	return e.planAcrossConfigs(&moveSymbolPlanner{req: req})
}

// RenameSymbol implements symbol renaming
func (e *DefaultEngine) RenameSymbol(ws *types.Workspace, req types.RenameSymbolRequest) (*types.RefactoringPlan, error) {
	operation := &RenameSymbolOperation{Request: req}

	// Validate the operation
	err := operation.Validate(ws)
	if err != nil {
		return nil, fmt.Errorf("rename operation validation failed: %w", err)
	}

	// Execute the operation to generate the plan
	plan, err := operation.Execute(ws)
	if err != nil {
		return nil, fmt.Errorf("failed to generate rename plan: %w", err)
	}

	plan.Operations = []types.Operation{operation}

	return plan, nil
}

// RenameSymbolAcrossConfigs renames a symbol like RenameSymbol, under every
// build configuration covering the workspace, and returns one merged plan. A
// declaration with the same key as a renamed one is renamed under every
// configuration that declares it, and the new name must be free under each.
func (e *DefaultEngine) RenameSymbolAcrossConfigs(req types.RenameSymbolRequest) (*types.RefactoringPlan, error) {
	return e.planAcrossConfigs(&renameSymbolPlanner{req: req})
}

// RenamePackage implements package renaming
func (e *DefaultEngine) RenamePackage(ws *types.Workspace, req types.RenamePackageRequest) (*types.RefactoringPlan, error) {
	run := &typesRun{}
	views, err := e.beginRun(run)
	if err != nil {
		return nil, fmt.Errorf("rename package: %w", err)
	}
	defer e.endRun(run)
	operation := &RenamePackageOperation{Request: req, Sources: e.set.Sources(), Views: views}

	// Validate the operation
	if err := operation.Validate(ws); err != nil {
		return nil, fmt.Errorf("rename package operation validation failed: %w", err)
	}

	// Execute the operation to generate the plan
	plan, err := operation.Execute(ws)
	if err != nil {
		return nil, fmt.Errorf("failed to generate rename package plan: %w", err)
	}
	plan.Coverage.Configs = configNames(e.set.Covering().Configs)

	plan.Operations = []types.Operation{operation}

	return plan, nil
}

// RenameMethod implements renaming methods on specific types (structs or interfaces)
func (e *DefaultEngine) RenameMethod(ws *types.Workspace, req types.RenameMethodRequest) (*types.RefactoringPlan, error) {
	// Method references resolve by go/types object identity, which needs a
	// type-checked workspace.
	e.EnsureTypeChecked(ws)

	operation := &RenameMethodOperation{Request: req}

	// Validate the operation
	err := operation.Validate(ws)
	if err != nil {
		return nil, fmt.Errorf("rename method operation validation failed: %w", err)
	}

	// Execute the operation to generate the plan
	plan, err := operation.Execute(ws)
	if err != nil {
		return nil, fmt.Errorf("failed to generate rename method plan: %w", err)
	}

	plan.Operations = []types.Operation{operation}

	return plan, nil
}

// RenameMethodAcrossConfigs renames a method like RenameMethod, under every
// build configuration covering the workspace, and returns one merged plan. A
// method with the same key as a renamed one is renamed under every
// configuration that declares it, and the new name must be free under each.
func (e *DefaultEngine) RenameMethodAcrossConfigs(req types.RenameMethodRequest) (*types.RefactoringPlan, error) {
	return e.planAcrossConfigs(&renameMethodPlanner{req: req})
}

// RenameField implements renaming a struct field on a named type
func (e *DefaultEngine) RenameField(ws *types.Workspace, req types.RenameFieldRequest) (*types.RefactoringPlan, error) {
	operation := &RenameFieldOperation{Request: req}
	// The operation cannot reach EngineConfig through the Operation interface,
	// so the engine hands it the AllowBreaking flag for the export-status check.
	if e.config != nil {
		operation.AllowBreaking = e.config.AllowBreaking
	}

	// Struct-field renames resolve references by go/types object identity, which
	// requires a type-checked workspace. Type-checking is lazy (LoadWorkspace does
	// not perform it), so ensure every package is checked before the operation
	// builds its reference index — otherwise FindMemberReferences fails closed.
	e.EnsureTypeChecked(ws)

	// Validate the operation
	err := operation.Validate(ws)
	if err != nil {
		return nil, fmt.Errorf("rename field operation validation failed: %w", err)
	}

	// Execute the operation to generate the plan
	plan, err := operation.Execute(ws)
	if err != nil {
		return nil, fmt.Errorf("failed to generate rename field plan: %w", err)
	}

	plan.Operations = []types.Operation{operation}

	return plan, nil
}

// RenameFieldAcrossConfigs renames a field like RenameField, under every build
// configuration covering the workspace, and returns one merged plan. A field
// with the same key as a renamed one is renamed under every configuration that
// declares it, and the new name must be free under each.
func (e *DefaultEngine) RenameFieldAcrossConfigs(req types.RenameFieldRequest) (*types.RefactoringPlan, error) {
	return e.planAcrossConfigs(&renameFieldPlanner{engine: e, req: req})
}

// ThreadContext implements transitive context.Context threading: it adds
// `ctx context.Context` to a seed function and every caller up its call
// chains, stopping only at a caller that already has a context in scope or at
// a root that cannot take one.
func (e *DefaultEngine) ThreadContext(ws *types.Workspace, req types.ThreadContextRequest) (*types.RefactoringPlan, error) {
	stop, err := resolveStopAt([]*types.Workspace{ws}, req.StopAt)
	if err != nil {
		return nil, err
	}
	if req.EntryPackage != "" || len(req.CallSitesOf) > 0 {
		return nil, fmt.Errorf("entry_package and call_sites_of need every build configuration; use ThreadContextAcrossConfigs")
	}
	plan, _, err := e.threadContext(ws, e.parserFor(ws), nil, req, nil, stop, nil)
	return plan, err
}

// ThreadContextAcrossConfigs threads context like ThreadContext, under every
// build configuration covering the workspace, and returns one merged plan. A
// function that gains ctx under one configuration gains it under all of them.
func (e *DefaultEngine) ThreadContextAcrossConfigs(req types.ThreadContextRequest) (*types.RefactoringPlan, error) {
	views, err := e.set.Views()
	if err != nil {
		return nil, err
	}
	wss := make([]*types.Workspace, len(views))
	for i, v := range views {
		wss[i] = v.Workspace
	}
	// Resolved before planning: planIn swallows SymbolNotFound per view.
	stop, err := resolveStopAt(wss, req.StopAt)
	if err != nil {
		return nil, err
	}
	var reach *EntryReach
	if req.EntryPackage != "" {
		reach, err = e.entryReach(views, req.EntryPackage, req.WithinFunction)
		if err != nil {
			return nil, err
		}
	}
	planner := &threadContextPlanner{engine: e, req: req, stop: stop, reach: reach, resolved: make(map[string]bool)}
	plan, err := e.planAcrossConfigs(planner)
	if err != nil {
		return nil, err
	}
	for _, spec := range req.CallSitesOf {
		if !planner.resolved[spec] {
			return nil, fmt.Errorf("unknown target %q", spec)
		}
	}
	return plan, nil
}

// addImportClosure adds path and the workspace packages it imports,
// transitively, to set. RTA cannot see the calls out of a package with no
// bodies, so what that package imports may be reached through it.
func addImportClosure(set map[string]bool, ws *types.Workspace, path string) {
	if set[path] {
		return
	}
	set[path] = true
	for _, pkg := range ws.Packages {
		if pkg.ImportPath != path || pkg.TypesPkg == nil {
			continue
		}
		for _, imp := range pkg.TypesPkg.Imports() {
			if ws.ImportToPath[imp.Path()] != "" {
				addImportClosure(set, ws, imp.Path())
			}
		}
	}
}

// entryReach merges what the program of entry reaches under each build
// configuration. A configuration whose entry cannot be analysed adds nothing,
// and is an error only when no configuration can be.
func (e *DefaultEngine) entryReach(views []*analysis.View, entry, within string) (*EntryReach, error) {
	reach := &EntryReach{Entry: entry, Funcs: make(types.SymbolKeySet), Bodiless: make(map[string]bool)}
	if within != "" {
		reach.Within = make(types.SymbolKeySet)
	}
	withinReached := false
	var entryErr error
	analysed := 0
	for _, v := range views {
		pkg := v.Workspace.Packages[types.ResolvePackagePath(v.Workspace, entry)]
		if pkg == nil {
			continue
		}
		ensureTypeChecked(v.Parser, v.Workspace)
		a := analysis.NewUnusedAnalyzer(v.Workspace, e.logger)
		if within != "" {
			key, ok, err := withinKey(v.Workspace, within)
			if err != nil {
				return nil, fmt.Errorf("entry reach: %w", err)
			}
			if ok {
				a.SetWithin(key)
			}
		}
		funcs, scoped, bodiless, err := a.ReachedFuncsWithin(pkg)
		var bodilessEntry *analysis.EntryBodilessError
		switch {
		case errors.As(err, &bodilessEntry):
			entryErr = cmp.Or(entryErr, err)
			continue
		case err != nil:
			return nil, viewError(views, v, err)
		}
		analysed++
		maps.Copy(reach.Funcs, funcs)
		if scoped != nil {
			withinReached = true
			maps.Copy(reach.Within, scoped)
		}
		addBodilessClosures(bodiless, reach, v)
	}
	if analysed == 0 {
		return nil, cmp.Or(entryErr, fmt.Errorf("entry package %q not found in any build configuration", entry))
	}
	if within != "" && !withinReached {
		return nil, fmt.Errorf("within_function %q is not declared or not reached by the entry program", within)
	}
	return reach, nil
}

func addBodilessClosures(bodiless []analysis.BodilessPackage, reach *EntryReach, v *analysis.View) {
	for _, b := range bodiless {
		if !strings.Contains(b.Package, " ") { // not a test variant
			addImportClosure(reach.Bodiless, v.Workspace, b.Package)
		}
	}
}

// threadContextPlanner plans context threading in one view.
type threadContextPlanner struct {
	viewIndexes
	engine *DefaultEngine
	req    types.ThreadContextRequest
	stop   types.SymbolKeySet
	reach  *EntryReach
	// resolved holds the req.CallSitesOf entries some view declares.
	resolved map[string]bool
}

func (p *threadContextPlanner) planIn(v *analysis.View, linked types.SymbolKeySet) (configResult, error) {
	plan, op, err := p.engine.threadContext(v.Workspace, v.Parser, p.indexOf(v), p.req, linked, p.stop, p.reach)
	if err != nil && !isSymbolNotFound(err) {
		return configResult{}, err
	}
	res := configResult{absent: err, declared: declaredFuncKeys(v.Workspace)}
	if err == nil {
		res = configResult{plan: plan, changed: op.GainedKeys(), reach: op.reach(), declared: res.declared}
		maps.Copy(p.resolved, op.ResolvedCallSites())
	}
	p.planned(v, plan)
	return res, nil
}

// AddContextParameterAcrossConfigs adds a ctx parameter to a function and passes
// a default value at its call sites, under every build configuration covering
// the workspace, and returns one merged plan. A declaration that gains ctx under
// one configuration gains it under all of them.
func (e *DefaultEngine) AddContextParameterAcrossConfigs(req types.AddContextParameterRequest) (*types.RefactoringPlan, error) {
	return e.planAcrossConfigs(&addContextParameterPlanner{engine: e, req: req})
}

// threadContext plans context threading in ws with the parser of the view
// that owns it, seeding the functions linked names next to the
// request's seed. A nil idx makes the operation build its own reference index.
// It also returns the operation the plan was built by.
func (e *DefaultEngine) threadContext(
	ws *types.Workspace,
	parser *analysis.GoParser,
	idx *analysis.ReferenceIndex,
	req types.ThreadContextRequest,
	linked types.SymbolKeySet,
	stop types.SymbolKeySet,
	reach *EntryReach,
) (*types.RefactoringPlan, *ThreadContextOperation, error) {
	operation := &ThreadContextOperation{
		FunctionName: req.FunctionName,
		SourceFile:   req.SourceFile,
		CallSitesOf:  req.CallSitesOf,
		Linked:       linked,
		StopAt:       stop,
		Reach:        reach,
		CachedIndex:  idx,
	}
	// The operation cannot reach EngineConfig through the Operation interface,
	// so the engine hands it the AllowBreaking flag, mirroring RenameField.
	if e.config != nil {
		operation.AllowBreaking = e.config.AllowBreaking
	}

	// Threading resolves every caller in the workspace by go/types object
	// identity, which requires a type-checked workspace. Type-checking is lazy
	// (LoadWorkspace does not perform it), so ensure every package is checked
	// before the operation builds its reference index — otherwise CallSitesOf
	// would silently find nothing and the operation would "succeed" having
	// changed nothing. Mirrors RenameField above.
	ensureTypeChecked(parser, ws)

	// The operation type-checks packages itself when it needs to build its own
	// reference index (CachedIndex nil), and doing that requires a parser with
	// a real importer. A parser built via analysis.NewParser has none, so a
	// self-made parser would panic partway through type-checking; the parser
	// of the view that owns ws is bound to ws's importer.
	operation.Parser = parser
	operation.Logger = e.logger

	// Validate the operation
	err := operation.Validate(ws)
	if err != nil {
		return nil, nil, fmt.Errorf("thread context operation validation failed: %w", err)
	}

	// Execute the operation to generate the plan
	plan, err := operation.Execute(ws)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to generate thread context plan: %w", err)
	}

	plan.Operations = []types.Operation{operation}

	return plan, operation, nil
}

// ExtractMethod implements method extraction from code blocks
func (e *DefaultEngine) ExtractMethod(ws *types.Workspace, req types.ExtractMethodRequest) (*types.RefactoringPlan, error) {
	// Use the engine's logger or create a discard logger
	logger := e.logger
	if logger == nil {
		logger = slog.New(slog.NewTextHandler(io.Discard, nil))
	}

	operation := &ExtractMethodOperation{
		SourceFile:    req.SourceFile,
		StartLine:     req.StartLine,
		EndLine:       req.EndLine,
		NewMethodName: req.NewMethodName,
		TargetStruct:  req.TargetStruct,
		Logger:        logger,
		Parser:        e.parserFor(ws),
	}

	// Validate the operation
	err := operation.Validate(ws)
	if err != nil {
		return nil, fmt.Errorf("extract method operation validation failed: %w", err)
	}

	// Execute the operation to generate the plan
	plan, err := operation.Execute(ws)
	if err != nil {
		return nil, fmt.Errorf("failed to generate extract method plan: %w", err)
	}

	plan.Operations = []types.Operation{operation}

	return plan, nil
}

// ExtractFunction implements function extraction from code blocks
func (e *DefaultEngine) ExtractFunction(ws *types.Workspace, req types.ExtractFunctionRequest) (*types.RefactoringPlan, error) {
	logger := e.logger
	if logger == nil {
		logger = slog.New(slog.NewTextHandler(io.Discard, nil))
	}

	// Parser is required, not optional: extract_function is type-aware now and
	// type-checking is lazy, so without it the source package carries no
	// TypesInfo and the operation refuses.
	operation := &ExtractFunctionOperation{
		SourceFile:      req.SourceFile,
		StartLine:       req.StartLine,
		EndLine:         req.EndLine,
		NewFunctionName: req.NewFunctionName,
		Logger:          logger,
		Parser:          e.parserFor(ws),
	}

	// Validate the operation
	err := operation.Validate(ws)
	if err != nil {
		return nil, fmt.Errorf("extract function operation validation failed: %w", err)
	}

	// Execute the operation to generate the plan
	plan, err := operation.Execute(ws)
	if err != nil {
		return nil, fmt.Errorf("failed to generate extract function plan: %w", err)
	}

	plan.Operations = []types.Operation{operation}

	return plan, nil
}

// ExtractInterface implements interface extraction from structs
func (e *DefaultEngine) ExtractInterface(ws *types.Workspace, req types.ExtractInterfaceRequest) (*types.RefactoringPlan, error) {
	operation := &ExtractInterfaceOperation{
		SourceStruct:  req.SourceStruct,
		InterfaceName: req.InterfaceName,
		Methods:       req.Methods,
		TargetPackage: req.TargetPackage,
	}

	// Validate the operation
	err := operation.Validate(ws)
	if err != nil {
		return nil, fmt.Errorf("extract interface operation validation failed: %w", err)
	}

	// Execute the operation to generate the plan
	plan, err := operation.Execute(ws)
	if err != nil {
		return nil, fmt.Errorf("failed to generate extract interface plan: %w", err)
	}

	plan.Operations = []types.Operation{operation}

	return plan, nil
}

// ExtractVariable implements variable extraction from expressions
func (e *DefaultEngine) ExtractVariable(ws *types.Workspace, req types.ExtractVariableRequest) (*types.RefactoringPlan, error) {
	operation := &ExtractVariableOperation{
		SourceFile:   req.SourceFile,
		StartLine:    req.StartLine,
		EndLine:      req.EndLine,
		VariableName: req.VariableName,
		Expression:   req.Expression,
	}

	// Validate the operation
	err := operation.Validate(ws)
	if err != nil {
		return nil, fmt.Errorf("extract variable operation validation failed: %w", err)
	}

	// Execute the operation to generate the plan
	plan, err := operation.Execute(ws)
	if err != nil {
		return nil, fmt.Errorf("failed to generate extract variable plan: %w", err)
	}

	plan.Operations = []types.Operation{operation}

	return plan, nil
}

// ExtractConstant declares the selected literal as a package-level constant
// and replaces every equal literal of its package with it.
func (e *DefaultEngine) ExtractConstant(ws *types.Workspace, req types.ExtractConstantRequest) (*types.RefactoringPlan, error) {
	operation := &ExtractConstantOperation{Request: req, Sources: e.set.Sources()}

	err := operation.Validate(ws)
	if err != nil {
		return nil, fmt.Errorf("extract constant operation validation failed: %w", err)
	}

	_, pkg, _ := ws.FindFile(req.SourceFile)
	if pkg != nil {
		e.EnsureTypeCheckedPackage(ws, pkg.ImportPath)
	}

	plan, err := operation.Execute(ws)
	if err != nil {
		return nil, fmt.Errorf("failed to generate extract constant plan: %w", err)
	}

	plan.Operations = []types.Operation{operation}

	return plan, nil
}

// InlineMethod implements method call inlining
func (e *DefaultEngine) InlineMethod(ws *types.Workspace, req types.InlineMethodRequest) (*types.RefactoringPlan, error) {
	operation := &InlineMethodOperation{
		MethodName:   req.MethodName,
		SourceStruct: req.SourceStruct,
		TargetFile:   req.TargetFile,
	}

	// Validate the operation
	err := operation.Validate(ws)
	if err != nil {
		return nil, fmt.Errorf("inline method operation validation failed: %w", err)
	}

	// Execute the operation to generate the plan
	plan, err := operation.Execute(ws)
	if err != nil {
		return nil, fmt.Errorf("failed to generate inline method plan: %w", err)
	}

	plan.Operations = []types.Operation{operation}

	return plan, nil
}

// InlineMethodAcrossConfigs inlines a method like InlineMethod, under every
// build configuration covering the workspace that includes the target file,
// and returns one merged plan. It refuses when the configurations declare the
// method with different bodies.
func (e *DefaultEngine) InlineMethodAcrossConfigs(req types.InlineMethodRequest) (*types.RefactoringPlan, error) {
	p := &inlineMethodPlanner{req: req}
	p.name = req.SourceStruct + "." + req.MethodName
	return e.planAcrossConfigs(p)
}

// InlineVariable implements variable inlining
func (e *DefaultEngine) InlineVariable(ws *types.Workspace, req types.InlineVariableRequest) (*types.RefactoringPlan, error) {
	operation := &InlineVariableOperation{
		VariableName: req.VariableName,
		SourceFile:   req.SourceFile,
		StartLine:    1,    // Default - could be enhanced to specify line
		EndLine:      1000, // Default - means all occurrences (large number)
	}

	// Validate the operation
	err := operation.Validate(ws)
	if err != nil {
		return nil, fmt.Errorf("inline variable operation validation failed: %w", err)
	}

	// Execute the operation to generate the plan
	plan, err := operation.Execute(ws)
	if err != nil {
		return nil, fmt.Errorf("failed to generate inline variable plan: %w", err)
	}

	plan.Operations = []types.Operation{operation}

	return plan, nil
}

// InlineFunction implements function call inlining
func (e *DefaultEngine) InlineFunction(ws *types.Workspace, req types.InlineFunctionRequest) (*types.RefactoringPlan, error) {
	operation := &InlineFunctionOperation{
		FunctionName: req.FunctionName,
		SourceFile:   req.SourceFile,
		TargetFiles:  req.TargetFiles,
	}

	// Validate the operation
	err := operation.Validate(ws)
	if err != nil {
		return nil, fmt.Errorf("inline function operation validation failed: %w", err)
	}

	// Execute the operation to generate the plan
	plan, err := operation.Execute(ws)
	if err != nil {
		return nil, fmt.Errorf("failed to generate inline function plan: %w", err)
	}

	plan.Operations = []types.Operation{operation}

	return plan, nil
}

// InlineFunctionAcrossConfigs inlines a function like InlineFunction, under
// every build configuration covering the workspace, and returns one merged
// plan. Without target files every non-test file of each configuration is one.
// It refuses when the configurations declare the function with different bodies.
func (e *DefaultEngine) InlineFunctionAcrossConfigs(req types.InlineFunctionRequest) (*types.RefactoringPlan, error) {
	p := &inlineFunctionPlanner{req: req}
	p.name = req.FunctionName
	return e.planAcrossConfigs(p)
}

// SafeDeleteBlockingReferences reports what would block a SafeDelete for req
// under any build configuration covering the workspace, regardless of
// req.Force and without deleting anything.
func (e *DefaultEngine) SafeDeleteBlockingReferences(req types.SafeDeleteRequest) ([]types.Blocker, error) {
	return e.blockersAcrossConfigs(&safeDeletePlanner{req: req})
}

// SafeDeleteAcrossConfigs deletes a symbol like SafeDelete, under every build
// configuration covering the workspace, and returns one merged plan. The
// declarations with the symbol's key are deleted under every configuration,
// and a use of any of them under any configuration, or a mention in a file no
// configuration type-checks, refuses the delete unless req.Force is set.
func (e *DefaultEngine) SafeDeleteAcrossConfigs(req types.SafeDeleteRequest) (*types.RefactoringPlan, error) {
	p := &safeDeletePlanner{req: req, errPrefix: "safe delete operation validation failed"}
	err := p.operation(nil).validateRequest()
	if err != nil {
		return nil, p.wrap(err)
	}
	return e.planAcrossConfigs(p)
}

// SafeDelete implements safe deletion of symbols
func (e *DefaultEngine) SafeDelete(ws *types.Workspace, req types.SafeDeleteRequest) (*types.RefactoringPlan, error) {
	operation := &SafeDeleteOperation{
		SymbolName: req.Symbol,
		SourceFile: req.SourceFile,
		Scope:      types.WorkspaceScope,
		Force:      req.Force,
	}

	e.EnsureTypeChecked(ws)
	err := operation.Validate(ws)
	if err != nil {
		return nil, fmt.Errorf("safe delete operation validation failed: %w", err)
	}

	plan, err := operation.Execute(ws)
	if err != nil {
		return nil, fmt.Errorf("failed to generate safe delete plan: %w", err)
	}

	plan.Operations = []types.Operation{operation}

	return plan, nil
}

// ChangeSignature implements changing function/method signatures
func (e *DefaultEngine) ChangeSignature(ws *types.Workspace, req ChangeSignatureRequest) (*types.RefactoringPlan, error) {
	operation := req.operation(e.logger)
	e.EnsureTypeChecked(ws)

	err := operation.Validate(ws)
	if err != nil {
		return nil, fmt.Errorf("change signature operation validation failed: %w", err)
	}

	plan, err := operation.Execute(ws)
	if err != nil {
		return nil, fmt.Errorf("failed to generate change signature plan: %w", err)
	}

	plan.Operations = []types.Operation{operation}

	return plan, nil
}

// ChangeSignatureAcrossConfigs changes a signature like ChangeSignature, under
// every build configuration covering the workspace, and returns one merged
// plan. A declaration with the same key as a changed one is changed under every
// configuration that declares it.
func (e *DefaultEngine) ChangeSignatureAcrossConfigs(req ChangeSignatureRequest) (*types.RefactoringPlan, error) {
	p := &changeSignaturePlanner{engine: e, req: req}
	plan, err := e.planAcrossConfigs(p)
	if err != nil {
		return nil, err
	}
	p.reportTypeChecks(plan)
	return plan, nil
}

// ValidateRefactoring validates a complete refactoring plan
func (e *DefaultEngine) ValidateRefactoring(plan *types.RefactoringPlan) error {
	return e.validator.ValidatePlanWithConfig(plan, e.config)
}

// EnsureTypeChecked type-checks every package in the workspace. Type-checking
// is lazy (LoadWorkspace does not perform it) and (*analysis.GoParser).EnsureTypeChecked
// is a no-op once a package has already been checked, so calling this
// repeatedly across operations is cheap.
func (e *DefaultEngine) EnsureTypeChecked(ws *types.Workspace) {
	ensureTypeChecked(e.parserFor(ws), ws)
}

func ensureTypeChecked(parser *analysis.GoParser, ws *types.Workspace) {
	if parser == nil {
		return
	}
	for _, pkg := range ws.Packages {
		parser.EnsureTypeChecked(ws, pkg)
	}
}

// EnsureTypeCheckedPackage type-checks only the requested package rather than
// the whole workspace. This matters because type-checking is lazy and
// workspaceImporter resolves imports on demand, so checking a single package
// transitively pulls in exactly its dependency closure as go/types encounters
// each import — there is no need to walk dependencies ourselves. On a large
// monorepo this is the difference between checking one package and checking
// all of them.
//
// An empty pkgPath falls back to the whole-workspace behavior of
// EnsureTypeChecked. If pkgPath cannot be resolved to a known package, this is
// a no-op: the subsequent analyzers.Run call already returns an empty result
// for an unknown package filter, so there is nothing useful to type-check.
func (e *DefaultEngine) EnsureTypeCheckedPackage(ws *types.Workspace, pkgPath string) {
	parser := e.parserFor(ws)
	if parser == nil {
		return
	}
	if pkgPath == "" {
		e.EnsureTypeChecked(ws)
		return
	}
	resolved := types.ResolvePackagePath(ws, pkgPath)
	pkg, ok := ws.Packages[resolved]
	if ok {
		parser.EnsureTypeChecked(ws, pkg)
	}
}

// Importer exposes the go/types.Importer that type-checked ws, so a caller
// verifying a plan with VerifyPlanTypesWithImporter can reuse the exact
// *go/types.Package instances the workspace was already checked with — see
// that function's doc comment for why a second, independently-built importer
// produces spurious type errors on cross-package references. Returns nil if
// e has no parser (e.g. a test double), which VerifyPlanTypesWithImporter
// treats like VerifyPlanTypesWithImporter with a nil importer: it builds its own throwaway importer.
func (e *DefaultEngine) Importer(ws *types.Workspace) gotypes.Importer {
	parser := e.parserFor(ws)
	if parser == nil {
		return nil
	}
	return parser.Importer()
}

// ExecutePlan applies a refactoring plan to the workspace
func (e *DefaultEngine) ExecutePlan(plan *types.RefactoringPlan) error {
	// Final validation before execution
	err := e.ValidateRefactoring(plan)
	if err != nil {
		return err // Return the validation error directly to preserve its type
	}

	// Check for critical issues
	for _, issue := range plan.Impact.PotentialIssues {
		if issue.Severity != types.Error {
			continue
		}
		return &types.RefactorError{
			Type:    types.InvalidOperation,
			Message: fmt.Sprintf("cannot execute plan due to critical issue: %s", issue.Description),
			File:    issue.File,
			Line:    issue.Line,
		}
	}

	// Apply changes
	if len(plan.Changes) > 0 {
		err := e.serializer.ApplyChanges(plan.Changes) // workspace will be inferred from changes
		if err != nil {
			return fmt.Errorf("failed to apply changes: %w", err)
		}

		// Validate that the refactored code compiles (if not skipped)
		if !e.shouldSkipCompilation() {
			err := e.validateCompilation(plan.AffectedFiles)
			if err != nil {
				return fmt.Errorf("refactored code does not compile: %w", err)
			}
		}
	}

	return nil
}

// shouldSkipCompilation returns true if compilation validation should be skipped
func (e *DefaultEngine) shouldSkipCompilation() bool {
	return e.config != nil && e.config.SkipCompilation
}

// validateCompilation checks that the modified files still compile
func (e *DefaultEngine) validateCompilation(affectedFiles []string) error {
	if len(affectedFiles) == 0 {
		return nil
	}

	// Get unique directories that need compilation checking
	dirsToCheck := make(map[string]bool)
	for _, file := range affectedFiles {
		dir := filepath.Dir(file)
		dirsToCheck[dir] = true
	}

	// Check compilation for each affected directory
	for dir := range dirsToCheck {
		err := e.checkDirectoryCompilation(dir)
		if err != nil {
			return fmt.Errorf("compilation failed in %s: %w", dir, err)
		}
	}

	return nil
}

// checkDirectoryCompilation runs go build on a directory to check compilation
func (e *DefaultEngine) checkDirectoryCompilation(dir string) error {
	// Use go build to check compilation without creating binaries
	cmd := exec.Command("go", "build", "-o", "/dev/null", ".")
	cmd.Dir = dir

	output, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("go build failed: %s", string(output))
	}

	return nil
}

// PreviewPlan generates a preview of the changes without applying them
func (e *DefaultEngine) PreviewPlan(plan *types.RefactoringPlan) (string, error) {
	return e.serializer.PreviewChanges(plan.Changes)
}

// Helper methods

func (e *DefaultEngine) modificationsToChanges(modifications []types.Modification, filePath string) []types.Change {
	var changes []types.Change

	for _, mod := range modifications {
		change := types.Change{
			File:        filePath,
			Start:       mod.Start,
			End:         mod.End,
			NewText:     mod.NewText,
			Description: e.getModificationDescription(mod),
		}

		// For modifications, we need to infer the old text
		// This would require reading the current file content
		// For now, leaving it empty - real implementation would fill this in
		change.OldText = ""

		changes = append(changes, change)
	}

	return changes
}

func (e *DefaultEngine) getModificationDescription(mod types.Modification) string {
	switch mod.Type {
	case types.Insert:
		return fmt.Sprintf("Insert text at position %d", mod.Start)
	case types.Delete:
		return fmt.Sprintf("Delete text from %d to %d", mod.Start, mod.End)
	case types.Replace:
		return fmt.Sprintf("Replace text from %d to %d", mod.Start, mod.End)
	default:
		return "Unknown modification"
	}
}

// Bulk operation implementations

// MovePackage implements moving entire packages
func (e *DefaultEngine) MovePackage(ws *types.Workspace, req types.MovePackageRequest) (*types.RefactoringPlan, error) {
	// Apply sensible defaults
	req.CreateTarget = true
	req.UpdateImports = true

	run := &typesRun{}
	views, err := e.beginRun(run)
	if err != nil {
		return nil, fmt.Errorf("move package: %w", err)
	}
	defer e.endRun(run)
	operation := &MovePackageOperation{Request: req, Sources: e.set.Sources(), Views: views}

	// Validate the operation
	if err := operation.Validate(ws); err != nil {
		return nil, fmt.Errorf("move package operation validation failed: %w", err)
	}

	// Execute the operation to generate the plan
	plan, err := operation.Execute(ws)
	if err != nil {
		return nil, fmt.Errorf("failed to generate move package plan: %w", err)
	}
	plan.Coverage.Configs = configNames(e.set.Covering().Configs)

	plan.Operations = []types.Operation{operation}

	return plan, nil
}

// MoveDir implements moving directory structures
func (e *DefaultEngine) MoveDir(ws *types.Workspace, req types.MoveDirRequest) (*types.RefactoringPlan, error) {
	// Apply sensible defaults
	req.UpdateImports = true

	run := &typesRun{}
	views, err := e.beginRun(run)
	if err != nil {
		return nil, fmt.Errorf("move dir: %w", err)
	}
	defer e.endRun(run)
	operation := &MoveDirOperation{Request: req, Sources: e.set.Sources(), Views: views}

	// Validate the operation
	if err := operation.Validate(ws); err != nil {
		return nil, fmt.Errorf("move directory operation validation failed: %w", err)
	}

	// Execute the operation to generate the plan
	plan, err := operation.Execute(ws)
	if err != nil {
		return nil, fmt.Errorf("failed to generate move directory plan: %w", err)
	}
	plan.Coverage.Configs = configNames(e.set.Covering().Configs)

	plan.Operations = []types.Operation{operation}

	return plan, nil
}

// MovePackages implements moving multiple packages atomically
func (e *DefaultEngine) MovePackages(ws *types.Workspace, req types.MovePackagesRequest) (*types.RefactoringPlan, error) {
	// Apply sensible defaults
	req.CreateTargets = true
	req.UpdateImports = true

	run := &typesRun{}
	views, err := e.beginRun(run)
	if err != nil {
		return nil, fmt.Errorf("move packages: %w", err)
	}
	defer e.endRun(run)
	operation := &MovePackagesOperation{Request: req, Sources: e.set.Sources(), Views: views}

	// Validate the operation
	if err := operation.Validate(ws); err != nil {
		return nil, fmt.Errorf("move packages operation validation failed: %w", err)
	}

	// Execute the operation to generate the plan
	plan, err := operation.Execute(ws)
	if err != nil {
		return nil, fmt.Errorf("failed to generate move packages plan: %w", err)
	}
	plan.Coverage.Configs = configNames(e.set.Covering().Configs)

	plan.Operations = []types.Operation{operation}

	return plan, nil
}

// CreateFacade implements creating facade packages
func (e *DefaultEngine) CreateFacade(ws *types.Workspace, req types.CreateFacadeRequest) (*types.RefactoringPlan, error) {
	operation := &CreateFacadeOperation{Request: req}

	// Validate the operation
	err := operation.Validate(ws)
	if err != nil {
		return nil, fmt.Errorf("create facade operation validation failed: %w", err)
	}

	// Execute the operation to generate the plan
	plan, err := operation.Execute(ws)
	if err != nil {
		return nil, fmt.Errorf("failed to generate create facade plan: %w", err)
	}

	plan.Operations = []types.Operation{operation}

	return plan, nil
}

// GenerateFacades implements auto-generating facades
func (e *DefaultEngine) GenerateFacades(ws *types.Workspace, req types.GenerateFacadesRequest) (*types.RefactoringPlan, error) {
	operation := &GenerateFacadesOperation{Request: req, Sources: e.set.Sources(), Covering: e.set.Covering()}

	// Validate the operation
	err := operation.Validate(ws)
	if err != nil {
		return nil, fmt.Errorf("generate facades operation validation failed: %w", err)
	}

	// Execute the operation to generate the plan
	plan, err := operation.Execute(ws)
	if err != nil {
		return nil, fmt.Errorf("failed to generate facades plan: %w", err)
	}

	plan.Operations = []types.Operation{operation}

	return plan, nil
}

// UpdateFacades implements updating existing facades
func (e *DefaultEngine) UpdateFacades(ws *types.Workspace, req types.UpdateFacadesRequest) (*types.RefactoringPlan, error) {
	operation := &UpdateFacadesOperation{Request: req, Sources: e.set.Sources(), Covering: e.set.Covering()}

	// Validate the operation
	err := operation.Validate(ws)
	if err != nil {
		return nil, fmt.Errorf("update facades operation validation failed: %w", err)
	}

	// Execute the operation to generate the plan
	plan, err := operation.Execute(ws)
	if err != nil {
		return nil, fmt.Errorf("failed to generate update facades plan: %w", err)
	}

	plan.Operations = []types.Operation{operation}

	return plan, nil
}

// CleanAliases implements cleaning import aliases
func (e *DefaultEngine) CleanAliases(ws *types.Workspace, req types.CleanAliasesRequest) (*types.RefactoringPlan, error) {
	operation := &CleanAliasesOperation{Request: req}

	// Validate the operation
	err := operation.Validate(ws)
	if err != nil {
		return nil, fmt.Errorf("clean aliases operation validation failed: %w", err)
	}

	// Execute the operation to generate the plan
	plan, err := operation.Execute(ws)
	if err != nil {
		return nil, fmt.Errorf("failed to generate clean aliases plan: %w", err)
	}

	plan.Operations = []types.Operation{operation}

	return plan, nil
}

// StandardizeImports implements standardizing import aliases
func (e *DefaultEngine) StandardizeImports(ws *types.Workspace, req types.StandardizeImportsRequest) (*types.RefactoringPlan, error) {
	operation := &StandardizeImportsOperation{Request: req}
	e.EnsureTypeChecked(ws)

	// Validate the operation
	err := operation.Validate(ws)
	if err != nil {
		return nil, fmt.Errorf("standardize imports operation validation failed: %w", err)
	}

	// Execute the operation to generate the plan
	plan, err := operation.Execute(ws)
	if err != nil {
		return nil, fmt.Errorf("failed to generate standardize imports plan: %w", err)
	}

	plan.Operations = []types.Operation{operation}

	return plan, nil
}

// ResolveAliasConflicts implements resolving import alias conflicts
func (e *DefaultEngine) ResolveAliasConflicts(ws *types.Workspace, req types.ResolveAliasConflictsRequest) (*types.RefactoringPlan, error) {
	operation := &ResolveAliasConflictsOperation{Request: req}

	// Validate the operation
	err := operation.Validate(ws)
	if err != nil {
		return nil, fmt.Errorf("resolve alias conflicts operation validation failed: %w", err)
	}

	// Execute the operation to generate the plan
	plan, err := operation.Execute(ws)
	if err != nil {
		return nil, fmt.Errorf("failed to generate resolve alias conflicts plan: %w", err)
	}

	plan.Operations = []types.Operation{operation}

	return plan, nil
}

// ConvertAliases implements converting between aliased and non-aliased imports
func (e *DefaultEngine) ConvertAliases(ws *types.Workspace, req types.ConvertAliasesRequest) (*types.RefactoringPlan, error) {
	operation := &ConvertAliasesOperation{Request: req}

	// Validate the operation
	err := operation.Validate(ws)
	if err != nil {
		return nil, fmt.Errorf("convert aliases operation validation failed: %w", err)
	}

	// Execute the operation to generate the plan
	plan, err := operation.Execute(ws)
	if err != nil {
		return nil, fmt.Errorf("failed to generate convert aliases plan: %w", err)
	}

	plan.Operations = []types.Operation{operation}

	return plan, nil
}

// MoveByDependencies implements moving symbols based on dependency analysis
func (e *DefaultEngine) MoveByDependencies(ws *types.Workspace, req types.MoveByDependenciesRequest) (*types.RefactoringPlan, error) {
	operation := &MoveByDependenciesOperation{Request: req}

	// Validate the operation
	if err := operation.Validate(ws); err != nil {
		return nil, fmt.Errorf("move by dependencies operation validation failed: %w", err)
	}

	// Reference matching without type information is name-based, so candidates
	// would differ depending on which tools ran earlier.
	e.EnsureTypeChecked(ws)

	// Analysis runs in the default configuration; each move is checked under all of them
	var plan *types.RefactoringPlan
	var err error
	if req.AnalyzeOnly {
		plan, err = operation.Execute(ws)
	} else {
		plan, err = e.moveSharedSymbols(ws, operation, e.MoveSymbolAcrossConfigs)
	}
	if err != nil {
		return nil, fmt.Errorf("failed to generate move by dependencies plan: %w", err)
	}

	plan.Operations = []types.Operation{operation}

	return plan, nil
}

// OrganizeByLayers implements organizing packages by architectural layers
func (e *DefaultEngine) OrganizeByLayers(ws *types.Workspace, req types.OrganizeByLayersRequest) (*types.RefactoringPlan, error) {
	operation := &OrganizeByLayersOperation{Request: req}

	// Validate the operation
	err := operation.Validate(ws)
	if err != nil {
		return nil, fmt.Errorf("organize by layers operation validation failed: %w", err)
	}

	// Execute the operation to generate the plan
	plan, err := operation.Execute(ws)
	if err != nil {
		return nil, fmt.Errorf("failed to generate organize by layers plan: %w", err)
	}

	plan.Operations = []types.Operation{operation}

	return plan, nil
}

// AnalyzeDependencies implements analyzing dependency flow
func (e *DefaultEngine) AnalyzeDependencies(ws *types.Workspace, req types.AnalyzeDependenciesRequest) (*types.RefactoringPlan, error) {
	operation := &AnalyzeDependenciesOperation{Request: req}

	// Suggested moves match references by type information when available.
	e.EnsureTypeChecked(ws)

	// Validate the operation
	err := operation.Validate(ws)
	if err != nil {
		return nil, fmt.Errorf("analyze dependencies operation validation failed: %w", err)
	}

	// Execute the operation to generate the plan
	plan, err := operation.Execute(ws)
	if err != nil {
		return nil, fmt.Errorf("failed to generate analyze dependencies plan: %w", err)
	}

	plan.Operations = []types.Operation{operation}

	return plan, nil
}

// BatchOperations plans every step of a batch with the engine method of the
// step's standalone tool and returns one merged plan. Steps that edit the same
// span of a file fail the batch.
func (e *DefaultEngine) BatchOperations(ws *types.Workspace, req types.BatchOperationRequest) (*types.RefactoringPlan, error) {
	operation := &BatchOperationOperation{Request: req, engine: e}

	err := operation.Validate(ws)
	if err != nil {
		return nil, fmt.Errorf("batch operations validation failed: %w", err)
	}

	plan, err := operation.Execute(ws)
	if err != nil {
		return nil, fmt.Errorf("failed to generate batch operations plan: %w", err)
	}
	return plan, nil
}
