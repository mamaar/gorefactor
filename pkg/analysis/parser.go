package analysis

import (
	"fmt"
	"go/ast"
	"go/importer"
	"go/parser"
	"go/token"
	gotypes "go/types"
	"log/slog"
	"maps"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"sync"

	"github.com/mamaar/gorefactor/pkg/types"
	"golang.org/x/tools/go/packages"
)

// Parser handles Go code parsing and AST management
type GoParser struct {
	fileSet  *token.FileSet
	logger   *slog.Logger
	importer *workspaceImporter

	buildConfig BuildConfig
	sources     *SourceTree

	// coveringMu guards covering, which is computed on read and cleared
	// whenever the source tree changes.
	coveringMu sync.Mutex
	covering   *Covering

	// checkMu serializes whole type-checking sessions. It is acquired by the
	// exported entry points (EnsureTypeChecked, TypeCheckPackage) and held for
	// the entire recursive check, including the go/types.Config.Check call. The
	// nested path — workspaceImporter.Import reaching a dependency — calls
	// typeCheckLocked directly and must NOT re-acquire it, which is what keeps
	// the recursion from deadlocking on a non-reentrant mutex.
	//
	// Serializing whole sessions is deliberate. Type-checking is expensive but
	// memoized, so contention is paid once per package; the alternative (letting
	// two goroutines check overlapping dependency closures at once) cannot
	// distinguish "this package is on my own call stack" — a real cycle, which
	// must be broken with a placeholder — from "another goroutine is checking
	// it", where a placeholder would be silently wrong. MCP handlers only take a
	// read lock on the surrounding workspace state, so concurrent requests do
	// reach here. Handing back a placeholder in that case would let a dependent
	// type-check against an empty package and then memoize that degraded result
	// permanently.
	checkMu sync.Mutex

	// typeCheckInProgress marks the packages on the current check session's call
	// stack, so Import can break an in-progress cycle. Guarded by checkMu rather
	// than mu: only the single goroutine holding a session ever touches it, so
	// membership now means exactly "on my own stack" and nothing else.
	typeCheckInProgress map[*types.Package]bool

	// mu guards reads and writes of the per-package type-check result fields
	// (TypesChecked/TypesPkg/TypesInfo/TypesErr). These are read by callers that
	// hold no session — the EnsureTypeChecked fast path, and consumers walking
	// ws.Packages — so the writes at the end of a session must still synchronize
	// with them. It is only ever held for those short field accesses; a session
	// may acquire mu while holding checkMu, never the reverse.
	mu sync.Mutex

	// typeCheckHook, if set, is invoked once per package each time
	// TypeCheckPackage actually performs a check (i.e. passes both the
	// TypesChecked memo guard and the in-progress/cycle guard). Used by tests
	// to count real check attempts without adding public API surface.
	typeCheckHook func(pkg *types.Package)
}

// ParserOption configures a GoParser.
type ParserOption func(*GoParser)

// WithBuildConfig pins the build configuration files are selected under.
func WithBuildConfig(cfg BuildConfig) ParserOption {
	return func(p *GoParser) { p.buildConfig = cfg }
}

func NewParser(logger *slog.Logger, opts ...ParserOption) *GoParser {
	fset := token.NewFileSet()
	p := &GoParser{
		fileSet:     fset,
		logger:      logger,
		buildConfig: DefaultBuildConfig(),
		sources:     NewSourceTree(fset),
	}
	for _, opt := range opts {
		opt(p)
	}
	return p
}

// BuildConfig returns the configuration files are selected under.
func (p *GoParser) BuildConfig() BuildConfig { return p.buildConfig }

// Sources returns the config-independent record of every file loaded so far.
func (p *GoParser) Sources() *SourceTree { return p.sources }

// Covering returns the build configurations that together include every
// recorded file, computing them on first use after the source tree changed.
func (p *GoParser) Covering() Covering {
	p.coveringMu.Lock()
	defer p.coveringMu.Unlock()
	if p.covering == nil {
		c := ComputeCovering(p.sources, p.buildConfig)
		p.covering = &c
	}
	return *p.covering
}

func (p *GoParser) invalidateCovering() {
	p.coveringMu.Lock()
	defer p.coveringMu.Unlock()
	p.covering = nil
}

// ParseFile parses a single Go file
func (p *GoParser) ParseFile(filename string) (*types.File, error) {
	content, err := os.ReadFile(filename)
	if err != nil {
		return nil, &types.RefactorError{
			Type:    types.FileSystemError,
			Message: fmt.Sprintf("failed to read file: %v", err),
			File:    filename,
			Cause:   err,
		}
	}

	astFile, err := parser.ParseFile(p.fileSet, filename, content, parser.ParseComments)
	if err != nil {
		return nil, &types.RefactorError{
			Type:    types.ParseError,
			Message: fmt.Sprintf("failed to parse file: %v", err),
			File:    filename,
			Cause:   err,
		}
	}

	file := &types.File{
		Path:            filename,
		AST:             astFile,
		OriginalContent: content,
		Modifications:   make([]types.Modification, 0),
	}

	return file, nil
}

// ParsePackage builds the package in dir from the files that match the parser's
// build config. It returns (nil, nil) when dir has .go files but none usable
// as a non-test file under that config.
func (p *GoParser) ParsePackage(dir string) (*types.Package, error) {
	if err := p.sources.AddDir(dir); err != nil {
		return nil, &types.RefactorError{
			Type:    types.FileSystemError,
			Message: fmt.Sprintf("failed to parse package: %v", err),
			File:    dir,
			Cause:   err,
		}
	}

	srcs := p.sources.Dir(dir)
	if len(srcs) == 0 {
		return nil, &types.RefactorError{
			Type:    types.ParseError,
			Message: "no non-test Go files found in package",
			File:    dir,
		}
	}

	pkg := &types.Package{
		Dir:       dir,
		Files:     make(map[string]*types.File),
		TestFiles: make(map[string]*types.File),
		Imports:   make([]string, 0),
	}

	for _, src := range srcs {
		if p.admits(src) {
			p.addSource(pkg, src)
		}
	}
	collectImports(pkg)

	if pkg.Name == "" {
		return nil, nil
	}

	return pkg, nil
}

// SyncSource brings the source tree in line with path on disk: a missing file
// is forgotten (nil result), a recorded one is re-read, a new one is recorded.
func (p *GoParser) SyncSource(path string) (*SourceFile, error) {
	if !strings.HasSuffix(path, ".go") {
		return nil, nil
	}
	defer p.invalidateCovering()
	path = absDir(path)
	if _, err := os.Stat(path); err != nil {
		if !os.IsNotExist(err) {
			return nil, err
		}
		p.sources.Remove(path)
		return nil, nil
	}

	dir := filepath.Dir(path)
	if !p.sources.loaded(dir) {
		if err := p.sources.AddDir(dir); err != nil {
			return nil, err
		}
		return p.sources.File(path), nil
	}
	if p.sources.File(path) != nil {
		return p.sources.Replace(path)
	}
	return p.sources.Add(path)
}

// ProjectFile re-evaluates whether the recorded file at path belongs in pkg
// under the build config and updates pkg to match. It reports whether the
// file was or is a member, i.e. whether pkg needs re-evaluating.
func (p *GoParser) ProjectFile(pkg *types.Package, path string) bool {
	name := filepath.Base(path)
	_, wasFile := pkg.Files[name]
	_, wasTest := pkg.TestFiles[name]
	delete(pkg.Files, name)
	delete(pkg.TestFiles, name)

	src := p.sources.File(path)
	admitted := src != nil && p.admits(src)
	if admitted {
		p.addSource(pkg, src)
	}
	if wasFile || wasTest || admitted {
		collectImports(pkg)
		return true
	}
	return false
}

// addSource adds src to pkg as a test or non-test file; the first non-test
// file names the package.
func (p *GoParser) addSource(pkg *types.Package, src *SourceFile) {
	file := &types.File{
		Path:            src.Path,
		Package:         pkg,
		AST:             src.AST,
		OriginalContent: src.Content,
		Modifications:   make([]types.Modification, 0),
	}
	if strings.HasSuffix(src.Name, "_test.go") {
		pkg.TestFiles[src.Name] = file
		return
	}
	pkg.Files[src.Name] = file
	if pkg.Name == "" {
		pkg.Name = src.AST.Name.Name
		pkg.Path = p.inferPackagePath(pkg.Dir, src.AST.Name.Name)
	}
}

// collectImports sets pkg.Imports to the distinct imports of its non-test
// files, in file-name order.
func collectImports(pkg *types.Package) {
	pkg.Imports = make([]string, 0)
	for _, name := range slices.Sorted(maps.Keys(pkg.Files)) {
		for _, imp := range pkg.Files[name].AST.Imports {
			importPath := strings.Trim(imp.Path.Value, "\"")
			if !contains(pkg.Imports, importPath) {
				pkg.Imports = append(pkg.Imports, importPath)
			}
		}
	}
}

// admits reports whether src parsed cleanly and matches the build config.
func (p *GoParser) admits(src *SourceFile) bool {
	if src.Err != nil {
		p.logger.Debug("skipping unparsable file", "path", src.Path, "err", src.Err)
		return false
	}
	return p.buildConfig.MatchesFile(src)
}

// ParseWorkspace parses an entire Go workspace/module.
// Package directories are discovered sequentially, then parsed in parallel
// using a bounded worker pool (runtime.NumCPU goroutines).
func (p *GoParser) ParseWorkspace(rootPath string) (*types.Workspace, error) {
	p.logger.Info("parsing workspace", "path", rootPath)

	// Convert to absolute path for consistency
	absRootPath, err := filepath.Abs(rootPath)
	if err != nil {
		p.logger.Error("failed to get absolute path", "path", rootPath, "err", err)
		return nil, &types.RefactorError{
			Type:    types.FileSystemError,
			Message: fmt.Sprintf("failed to get absolute path for workspace: %v", err),
			File:    rootPath,
		}
	}

	workspace := &types.Workspace{
		RootPath:     absRootPath,
		Packages:     make(map[string]*types.Package),
		ImportToPath: make(map[string]string),
		FileSet:      p.fileSet,
		BuildConfig:  p.buildConfig.Key(),
	}

	// Try to find and parse go.mod
	goModPath := filepath.Join(absRootPath, "go.mod")
	if modContent, err := os.ReadFile(goModPath); err == nil {
		module, err := p.parseGoMod(modContent)
		if err != nil {
			return nil, err
		}
		workspace.Module = module
	}

	// Phase 1: Load every .go file under the root into a fresh source tree, then
	// keep the directories that hold at least one file admitted by the build config.
	p.sources = NewSourceTree(p.fileSet)
	p.invalidateCovering()
	if err := p.sources.Load(absRootPath); err != nil {
		p.logger.Error("workspace discovery failed", "path", rootPath, "err", err)
		return nil, &types.RefactorError{
			Type:    types.FileSystemError,
			Message: fmt.Sprintf("failed to parse workspace: %v", err),
			File:    rootPath,
			Cause:   err,
		}
	}

	var pkgDirs []string
	for _, dir := range p.sources.Dirs() {
		if p.hasGoFiles(dir) {
			pkgDirs = append(pkgDirs, dir)
		}
	}

	p.logger.Debug("discovered packages", "count", len(pkgDirs))

	// Phase 2: Parse packages in parallel with bounded concurrency.
	// Each package parse is independent (reads its own files, creates its own AST nodes).
	// The shared fileSet is safe for concurrent use via its internal mutex.
	type pkgResult struct {
		pkg *types.Package
		err error
	}

	results := make([]pkgResult, len(pkgDirs))
	workers := runtime.NumCPU()
	if workers > len(pkgDirs) {
		workers = len(pkgDirs)
	}

	var wg sync.WaitGroup
	dirCh := make(chan int, len(pkgDirs))

	for i := range pkgDirs {
		dirCh <- i
	}
	close(dirCh)

	for w := 0; w < workers; w++ {
		wg.Go(func() {
			for idx := range dirCh {
				pkg, err := p.ParsePackage(pkgDirs[idx])
				results[idx] = pkgResult{pkg: pkg, err: err}
			}
		})
	}

	wg.Wait()

	// Collect results
	for i, res := range results {
		if res.err != nil {
			// Log error but continue parsing other packages (same as before)
			fmt.Fprintf(os.Stderr, "Warning: failed to parse package %s: %v\n", pkgDirs[i], res.err)
			continue
		}
		if res.pkg == nil {
			continue
		}
		workspace.Packages[res.pkg.Path] = res.pkg
	}

	// After parsing packages, build import path mapping
	if workspace.Module != nil {
		for fsPath, pkg := range workspace.Packages {
			importPath := computeImportPath(workspace, fsPath)
			pkg.ImportPath = importPath
			workspace.ImportToPath[importPath] = fsPath
		}
	}

	p.logger.Info("workspace parsed successfully", "packages", len(workspace.Packages), "module", workspace.Module)

	// Create a single importer instance for this workspace to ensure consistent
	// stdlib type identities across all TypeCheckPackage calls.
	p.importer = &workspaceImporter{ws: workspace, fset: workspace.FileSet, parser: p}

	return workspace, nil
}

// UpdateFile updates AST after file modifications
func (p *GoParser) UpdateFile(file *types.File) error {
	if len(file.Modifications) == 0 {
		return nil
	}

	// Apply modifications to original content
	content := p.applyModifications(file.OriginalContent, file.Modifications)

	// Re-parse the modified content
	astFile, err := parser.ParseFile(p.fileSet, file.Path, content, parser.ParseComments)
	if err != nil {
		return &types.RefactorError{
			Type:    types.ParseError,
			Message: fmt.Sprintf("failed to re-parse modified file: %v", err),
			File:    file.Path,
			Cause:   err,
		}
	}

	// Update AST and clear modifications
	file.AST = astFile
	file.Modifications = make([]types.Modification, 0)

	return nil
}

// Helper functions

func (p *GoParser) parseGoMod(content []byte) (*types.Module, error) {
	lines := strings.Split(string(content), "\n")
	module := &types.Module{
		GoMod: string(content),
	}

	for _, line := range lines {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "module ") {
			module.Path = strings.TrimSpace(strings.TrimPrefix(line, "module"))
		}
		// Note: Version parsing would be more complex in real implementation
	}

	return module, nil
}

func (p *GoParser) inferPackagePath(dir, packageName string) string {
	// Return the absolute filesystem path for now
	// The proper import path will be computed after workspace parsing
	abs, err := filepath.Abs(dir)
	if err != nil {
		return packageName
	}
	return abs
}

// ComputeImportPath computes the Go import path for a package given its filesystem path.
func ComputeImportPath(ws *types.Workspace, fsPath string) string {
	return computeImportPath(ws, fsPath)
}

// computeImportPath computes the Go import path for a package given its filesystem path
func computeImportPath(ws *types.Workspace, fsPath string) string {
	if ws.Module == nil {
		return ""
	}
	relPath, err := filepath.Rel(ws.RootPath, fsPath)
	if err != nil || relPath == "." {
		return ws.Module.Path
	}
	return ws.Module.Path + "/" + filepath.ToSlash(relPath)
}

// hasGoFiles reports whether dir holds a file admitted by the build config.
func (p *GoParser) hasGoFiles(dir string) bool {
	return slices.ContainsFunc(p.sources.Dir(dir), p.admits)
}

func (p *GoParser) applyModifications(content []byte, modifications []types.Modification) []byte {
	// Sort modifications by position in reverse order to maintain offsets
	// This is a simplified implementation
	result := make([]byte, len(content))
	copy(result, content)

	for _, mod := range modifications {
		switch mod.Type {
		case types.Insert:
			// Insert new text at position
			result = append(result[:mod.Start], append([]byte(mod.NewText), result[mod.Start:]...)...)
		case types.Delete:
			// Delete text from Start to End
			result = append(result[:mod.Start], result[mod.End:]...)
		case types.Replace:
			// Replace text from Start to End with NewText
			result = append(result[:mod.Start], append([]byte(mod.NewText), result[mod.End:]...)...)
		}
	}

	return result
}

func contains(slice []string, item string) bool {
	return slices.Contains(slice, item)
}

// EnsureTypeChecked runs type-checking on a package if it hasn't been done yet.
// This enables lazy/on-demand type-checking instead of eager upfront checking.
//
// Guards on TypesChecked, not TypesPkg != nil: go/types.Config.Check returns a
// populated package even when checking fails (see TypeCheckPackage), so
// TypesPkg is normally non-nil after a completed attempt regardless of
// success. Guarding on TypesPkg would treat "checked and failed" the same as
// "never checked" and re-run — and recursively re-run every dependency of —
// the check on every single call.
func (p *GoParser) EnsureTypeChecked(ws *types.Workspace, pkg *types.Package) {
	p.mu.Lock()
	checked := pkg.TypesChecked
	p.mu.Unlock()
	if checked {
		return
	}
	p.TypeCheckPackage(ws, pkg)
}

// InvalidateTypeCheck drops semantic data keyed by a package's current AST.
// Incremental workspace updates replace *ast.File nodes, so retaining the old
// go/types maps would make every lookup on the reparsed identifiers miss while
// TypesChecked incorrectly prevents a fresh check.
func (p *GoParser) InvalidateTypeCheck(pkg *types.Package) {
	if pkg == nil {
		return
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	pkg.TypesInfo = nil
	pkg.TypesPkg = nil
	pkg.TypesErr = nil
	pkg.TestTypesExcluded = false
	pkg.TestTypesExcludedErr = nil
	pkg.TypesChecked = false
}

// TypeCheckPackage runs go/types type-checking on a package.
// Results are stored in pkg.TypesInfo, pkg.TypesPkg and pkg.TypesErr, and
// pkg.TypesChecked is set once the attempt completes — on success AND on
// failure. Errors are logged but not returned — packages that fail
// type-checking still get their (partial) TypesInfo/TypesPkg stored so
// dependents can use best-effort results instead of falling back to nothing.
//
// Memoisation and re-entrancy: the check recurses into itself through
// workspaceImporter.Import for every workspace-local import encountered by
// go/types.Config.Check. Two guards make that safe:
//   - pkg.TypesChecked (set at the very end) stops a package from ever being
//     checked twice.
//   - p.typeCheckInProgress (set for the duration of one call) closes an
//     in-progress cycle: the check folds in-package _test.go files into the
//     same pass (see below), so "A_test.go imports B, B imports A" is
//     reachable even though Go forbids import cycles outside tests. When that
//     happens, Import (not this function) breaks the cycle by handing
//     go/types a placeholder package instead of recursing back in here.
//
// TypeCheckPackage is the exported entry point and takes the session lock.
// Callers already inside a session (Import) use typeCheckLocked instead.
func (p *GoParser) TypeCheckPackage(ws *types.Workspace, pkg *types.Package) {
	p.checkMu.Lock()
	defer p.checkMu.Unlock()
	p.typeCheckLocked(ws, pkg)
}

// typeCheckLocked performs the actual check. It requires p.checkMu to be held,
// which makes p.typeCheckInProgress private to this goroutine and lets the
// in-progress marker mean precisely "on my own call stack".
func (p *GoParser) typeCheckLocked(ws *types.Workspace, pkg *types.Package) {
	p.mu.Lock()
	checked := pkg.TypesChecked
	p.mu.Unlock()
	if checked {
		return
	}

	if p.typeCheckInProgress == nil {
		p.typeCheckInProgress = make(map[*types.Package]bool)
	}
	if p.typeCheckInProgress[pkg] {
		// Already being checked further up this same call stack (a cycle
		// reached through in-package test files). Import's placeholder
		// handles the recursive import; there is nothing more to do here.
		return
	}
	p.typeCheckInProgress[pkg] = true
	defer delete(p.typeCheckInProgress, pkg)

	if p.typeCheckHook != nil {
		p.typeCheckHook(pkg)
	}

	var files []*ast.File
	for _, f := range pkg.Files {
		if f.AST != nil {
			files = append(files, f.AST)
		}
	}
	if len(files) == 0 {
		// Nothing to check (e.g. every file in the package failed to parse).
		// Still mark it checked: leaving TypesChecked false would make every
		// future import of this package re-enter here on the (cheap, but
		// pointless) chance that files might appear.
		p.mu.Lock()
		pkg.TypesChecked = true
		p.mu.Unlock()
		return
	}

	// Include in-package test files so identifiers declared or used in _test.go
	// land in Defs/Uses. Without them, object-identity lookups (FindFieldReferences)
	// silently miss every test-file reference. External test packages (package x_test)
	// carry a different package name and cannot be checked in the same pass.
	pkgName := files[0].Name.Name
	withTests := files
	for _, f := range pkg.TestFiles {
		if f.AST != nil && f.AST.Name.Name == pkgName {
			withTests = append(withTests, f.AST)
		}
	}

	typesPkg, info, err := p.checkFiles(ws, pkg, withTests)
	var testTypesExcluded bool
	var testTypesExcludedErr error
	if err != nil && len(withTests) > len(files) {
		// A test file that fails to type-check must not cost the whole package its
		// type info, so retry without them. The retry's result — success or
		// failure — is what gets recorded below.
		//
		// This withTests failure is recorded on the package (TestTypesExcluded/
		// TestTypesExcludedErr) regardless of whether the retry that follows
		// succeeds: a successful retry reports a nil TypesErr, which would
		// otherwise look identical to a package whose test files WERE included.
		// Consumers that resolve references by go/types object identity
		// (CallSitesOf, NonCallUsesOf, ...) need to be able to tell the two
		// apart, because in the excluded case they are silently blind to every
		// call site in this package's tests.
		p.logger.Debug("type-checking with test files failed, retrying without", "package", pkg.ImportPath, "err", err)
		testTypesExcluded = true
		testTypesExcludedErr = err
		typesPkg, info, err = p.checkFiles(ws, pkg, files)
	}
	if err != nil {
		p.logger.Debug("type-checking failed (falling back to AST inference)", "package", pkg.ImportPath, "err", err)
	}

	p.mu.Lock()
	// go/types.Config.Check returns a non-nil, Complete() package even on
	// error (partial results from whatever it managed to resolve), so store
	// it unconditionally — dependents can use best-effort info instead of
	// nothing at all.
	pkg.TypesPkg = typesPkg
	pkg.TypesInfo = info
	pkg.TypesErr = err
	pkg.TestTypesExcluded = testTypesExcluded
	pkg.TestTypesExcludedErr = testTypesExcludedErr
	pkg.TypesChecked = true
	p.mu.Unlock()

	// pkg's own combined (production + in-package test) check is complete.
	// Clear typeCheckInProgress for it now, explicitly, rather than waiting
	// for this function's own deferred delete: an external test package
	// (package foo_test, checked next) almost always imports pkg itself —
	// that is the whole reason such a file exists — and it must resolve to
	// the real, just-completed check above. Left set, Import would wrongly
	// treat pkg as still on the stack and hand back the narrower
	// production-only rescue view (see Import's in-progress branch) for a
	// plain, non-cyclic self-import. delete on an absent key is a no-op, so
	// running this again via the deferred delete below is harmless.
	delete(p.typeCheckInProgress, pkg)

	p.checkExternalTestPackage(ws, pkg, pkgName)
}

// checkExternalTestPackage type-checks pkg's external test files — files
// declaring `package <pkgName>_test`, which is a different package name
// than pkg's own despite living in the same directory — as their own,
// separate compilation unit, the way `go test` itself does: nothing ever
// imports an external test package, so it is checked purely for its own
// Defs/Uses, never stored as anything importable.
//
// Called only from typeCheckLocked, after pkg's own in-progress marker has
// already been cleared (see that call site) so that this file set's own
// import of pkg — the near-universal reason an external test package
// exists — resolves to pkg's real, complete check rather than the
// production-only rescue view Import hands to a genuinely in-progress
// package.
//
// The resulting Defs/Uses are merged into pkg.TypesInfo so every consumer
// that already reads pkg.TypesInfo for pkg.TestFiles (BuildReferenceIndex
// chief among them, since it does not distinguish in-package from external
// test files) sees these identifiers exactly the way it already sees
// in-package ones. The merge is safe because the two Info maps are keyed by
// *ast.Ident pointers drawn from disjoint file sets — nothing in pkg's own
// combined check can share a key with a file checked here.
func (p *GoParser) checkExternalTestPackage(ws *types.Workspace, pkg *types.Package, pkgName string) {
	var files []*ast.File
	for _, f := range pkg.TestFiles {
		if f.AST != nil && f.AST.Name.Name != pkgName {
			files = append(files, f.AST)
		}
	}
	if len(files) == 0 {
		return
	}

	// checkFiles uses pkg.ImportPath as the Check() path argument, which is
	// harmless here: this call's resulting *types.Package is discarded
	// (nothing ever imports an external test package, so there is nothing to
	// store it as), and go/types.Checker verifies package-name consistency
	// only among the files passed to ONE Check() call, never against that
	// path string — it is purely a label on the (unused) result.
	_, info, err := p.checkFiles(ws, pkg, files)
	if err != nil {
		p.logger.Debug("external test package type-checking failed", "package", pkg.ImportPath, "err", err)
	}
	if info == nil {
		return
	}

	p.mu.Lock()
	defer p.mu.Unlock()
	if pkg.TypesInfo == nil {
		// pkg's own check had zero parseable files (or failed hard enough
		// that checkFiles returned a nil Info there too) — nowhere to merge.
		return
	}
	for ident, obj := range info.Defs {
		pkg.TypesInfo.Defs[ident] = obj
	}
	for ident, obj := range info.Uses {
		pkg.TypesInfo.Uses[ident] = obj
	}
}

// BindWorkspace attaches p to ws as ws's workspace importer, without going
// through ParseWorkspace. It exists for one-off type-checking sessions that
// need TypeCheckPackage's exact package-resolution logic (workspace-local
// lookup, then the go/packages-backed external-module fallback, then
// importer.Default()) against a *types.Workspace some other GoParser already
// parsed — most notably pkg/refactor.VerifyPlanTypes, which type-checks
// throwaway in-memory copies of a package's files and must not touch the
// GoParser that owns ws's cached TypesChecked/TypesInfo state. Safe to call
// on a fresh GoParser; overwrites any importer p already had.
func (p *GoParser) BindWorkspace(ws *types.Workspace) {
	p.importer = &workspaceImporter{ws: ws, fset: ws.FileSet, parser: p}
}

// Importer exposes p's workspace importer as a go/types.Importer. It exists
// for callers that need to drive their own go/types.Config — most notably
// pkg/refactor.VerifyPlanTypes, which needs a custom Config.Error that
// collects every error instead of Config.Check's default of stopping (or, as
// checkFiles below does, silently discarding) after the first — so they can
// reuse the same import-resolution logic as checkFiles without duplicating
// workspaceImporter. Returns nil if p has not been attached to a workspace
// yet via ParseWorkspace or BindWorkspace.
func (p *GoParser) Importer() gotypes.Importer {
	return p.importer
}

// NewTypesConfig returns the go/types.Config used for every workspace
// type-check. FakeImportC makes cgo files check cleanly; C.x expressions get
// no type information and never appear in Info.Uses.
func NewTypesConfig(importer gotypes.Importer, onError func(error)) gotypes.Config {
	return gotypes.Config{
		Importer:    importer,
		Error:       onError,
		FakeImportC: true,
	}
}

func ignoreTypeError(error) {}

// checkFiles runs go/types over the given file set, returning the populated Info
// even when checking fails (go/types fills it in partially on error).
func (p *GoParser) checkFiles(ws *types.Workspace, pkg *types.Package, files []*ast.File) (*gotypes.Package, *gotypes.Info, error) {
	conf := NewTypesConfig(p.importer, ignoreTypeError)
	info := &gotypes.Info{
		Types: make(map[ast.Expr]gotypes.TypeAndValue),
		Defs:  make(map[*ast.Ident]gotypes.Object),
		Uses:  make(map[*ast.Ident]gotypes.Object),
	}
	typesPkg, err := conf.Check(pkg.ImportPath, ws.FileSet, files, info)
	return typesPkg, info, err
}

// workspaceImporter implements go/types.Importer using workspace-local packages
// with fallback to source-based importing for stdlib/external packages.
type workspaceImporter struct {
	ws     *types.Workspace
	fset   *token.FileSet
	parser *GoParser
	std    gotypes.Importer

	// extOnce guards the one-time, workspace-wide load of external
	// (non-workspace-local) packages via go/packages. It runs lazily, on the
	// first Import call that misses ws.ImportToPath, rather than eagerly in
	// ParseWorkspace: most callers only ever need a handful of stdlib/module
	// packages, and eagerly loading the whole dependency graph for every
	// workspace (including the many synthetic, no-dependency workspaces the
	// test suite builds in t.TempDir()) would be pure waste.
	extOnce sync.Once
	// ext holds every externally-resolved package keyed by import path, found
	// by loading the workspace with go/packages and walking the roots plus
	// their transitive Imports. Populated once by extOnce; safe to read
	// afterward without additional synchronization since extOnce.Do's happens
	// -before guarantees visibility to every subsequent Import call.
	ext map[string]*gotypes.Package
	// extErr records why the load produced no (or a partial) result, purely
	// for the Debug log below — a failure here is not fatal, it just means
	// the importer.Default() fallback is all that's left.
	extErr error

	// productionOnly memoizes the production-files-only (no _test.go at all)
	// check of a workspace-local package, computed on demand by Import when
	// that package's own real check (production + in-package tests, owned by
	// typeCheckLocked) is already in progress further up this call stack —
	// see the comment at that call site for why. Guarded by checkMu like
	// typeCheckInProgress: only the single goroutine holding a type-check
	// session ever touches it.
	productionOnly map[*types.Package]*gotypes.Package
}

// loadExternal populates imp.ext by loading the whole workspace module with
// go/packages and collecting every package it resolves that is not
// workspace-local (those are already handled by the ws.ImportToPath branch
// above and by the recursive type-check). This is what lets third-party
// module dependencies (golang.org/x/..., etc.) resolve instead of falling
// through to the GOPATH-only importer.Default(), which cannot see anything
// in the module cache.
//
// Runs at most once per workspaceImporter (guarded by extOnce), triggered
// lazily from Import. Must not be called while holding any lock: it does
// its own network-free package load, which can take seconds on a large
// workspace, and it takes none of GoParser's locks itself.
func (imp *workspaceImporter) loadExternal() {
	if imp.ws.Module == nil || imp.ws.RootPath == "" {
		// No go.mod (or no root) means go/packages has nothing to resolve
		// against; skip straight to the importer.Default() fallback.
		return
	}

	if !imp.hasExternalImports() {
		// Nothing in this workspace imports a third-party module, so the whole
		// point of the load — resolving module-cache packages that
		// importer.Default() cannot see — does not apply. Skipping matters
		// because packages.Load shells out to the go tool: paying that on a
		// workspace whose imports are entirely stdlib and local is pure
		// overhead, and the test suite builds dozens of exactly such synthetic
		// workspaces.
		return
	}

	cfg := &packages.Config{
		Mode: packages.NeedName | packages.NeedTypes | packages.NeedImports | packages.NeedDeps,
		Dir:  imp.ws.RootPath,
		Fset: imp.fset,
		// Tests: true adds each package's test-binary variants (and their
		// Imports) to the load, which is the only way a third-party module
		// that is imported EXCLUSIVELY from _test.go files anywhere in the
		// workspace ever reaches imp.ext. Without it, walk below never sees
		// that import edge at all — not even the workspace-local package that
		// carries it — because a bare `packages.Load(cfg, "./...")` only
		// resolves each package's non-test Imports. TypeCheckPackage then
		// fails to resolve that import while checking the package WITH its
		// test files, and its fallback (see the "retrying without" comment
		// there) silently drops every test file in that package from
		// TypesInfo — not just the one that imports the unresolvable module.
		// That blinds CallSitesOf/NonCallUsesOf to every _test.go call site in
		// the package, which is how this was first found: add_context_parameter
		// threading a context through a function silently produced no edits
		// for the test file that called it, even though the test file itself
		// had nothing wrong with it — a sibling test file's go-sqlmock import
		// (present only in tests, never in production code) was enough to
		// take every test file in the package down with it.
		Tests: true,
		// GOPROXY=off is load-bearing, not a performance nicety: this runs
		// inside an MCP request, potentially against a workspace whose module
		// cache is cold. Without it, a missing module triggers a network
		// fetch that can hang the request. GOPROXY=off makes resolution use
		// only what's already in the local module cache and fail fast
		// otherwise, which is exactly the "degrade gracefully" behavior this
		// fallback needs — a failed load just leaves imp.ext empty and control
		// falls through to importer.Default().
		Env: append(os.Environ(), "GOPROXY=off"),
	}

	roots, err := packages.Load(cfg, "./...")
	if err != nil {
		imp.extErr = err
		imp.parser.logger.Debug("external package load failed", "err", err)
		return
	}

	imp.ext = make(map[string]*gotypes.Package)
	seen := make(map[string]bool)

	// The import graph is a DAG with heavy sharing (many packages depend on
	// the same handful of leaves), so without a seen-set this recursion is
	// exponential rather than linear in the number of packages.
	//
	// Deduping on pkg.ID, not pkg.PkgPath, is load-bearing now that cfg.Tests
	// is true: for a workspace-local package with tests, "./..." then returns
	// TWO *packages.Package values sharing one PkgPath — the production
	// variant and the "p [p.test]" in-package-test variant — distinguished
	// only by ID. Their Imports sets differ (the test variant adds whatever
	// its _test.go files import), and roots is unordered, so whichever
	// variant walk visits first would, keyed on PkgPath, mark the PkgPath
	// seen and make walk skip the other variant's edges entirely. That
	// silently dropped any third-party module imported ONLY from _test.go
	// files workspace-wide (nothing on the production side ever reaches it)
	// from imp.ext, which made Import() fail for it — and that failure is
	// exactly what made TypeCheckPackage's withTests attempt fail and fall
	// back to checking the package without ANY of its test files (see the
	// "retrying without" logic below), silently blinding CallSitesOf to
	// every _test.go call site in that package.
	var walk func(pkg *packages.Package)
	walk = func(pkg *packages.Package) {
		if pkg == nil || seen[pkg.ID] {
			return
		}
		seen[pkg.ID] = true

		// Keep only non-workspace packages. Import resolves workspace-local
		// paths from ws.Packages before it ever consults ext, so go/packages'
		// separately type-checked copy of them would never be read — and
		// holding it alive costs real memory, since the workspace's own
		// packages are the largest in the graph. Their dependencies are still
		// reached by continuing the walk below; nothing outside the workspace
		// imports them, so dropping the reference lets those copies be
		// collected.
		if pkg.Types != nil && pkg.Types.Complete() {
			imp.ext[pkg.PkgPath] = pkg.Types
		}
		for _, dep := range pkg.Imports {
			walk(dep)
		}
	}
	for _, root := range roots {
		walk(root)
	}
}

// hasExternalImports reports whether any file in the workspace imports a
// package that is neither workspace-local nor stdlib — i.e. whether anything
// here actually needs the module cache.
//
// It reads import paths straight from the parsed ASTs rather than from
// pkg.Imports, because ParsePackage only accumulates pkg.Imports from non-test
// files. A package whose only third-party dependency appears in a _test.go
// would otherwise look dependency-free, and since TypeCheckPackage folds
// in-package test files into the same check, that would silently cost those
// test files their type information.
func (imp *workspaceImporter) hasExternalImports() bool {
	external := func(file *types.File) bool {
		if file == nil || file.AST == nil {
			return false
		}
		for _, spec := range file.AST.Imports {
			path := strings.Trim(spec.Path.Value, `"`)
			if _, local := imp.ws.ImportToPath[path]; local {
				continue
			}
			if isStdlibImportPath(path) {
				continue
			}
			return true
		}
		return false
	}

	for _, pkg := range imp.ws.Packages {
		for _, file := range pkg.Files {
			if external(file) {
				return true
			}
		}
		for _, file := range pkg.TestFiles {
			if external(file) {
				return true
			}
		}
	}
	return false
}

// isStdlibImportPath reports whether path refers to a standard library
// package, using the same rule the go tool does: the first path segment of
// every non-stdlib module path contains a dot (a domain name), and no stdlib
// path ever does.
func isStdlibImportPath(path string) bool {
	first, _, _ := strings.Cut(path, "/")
	return !strings.Contains(first, ".")
}

func (imp *workspaceImporter) Import(path string) (*gotypes.Package, error) {
	// Check if this is a workspace-local package
	if fsPath, ok := imp.ws.ImportToPath[path]; ok {
		if pkg, ok := imp.ws.Packages[fsPath]; ok {
			// Import is only ever reached from inside go/types.Config.Check,
			// which runs under a type-check session, so p.checkMu is already
			// held by this goroutine and typeCheckInProgress belongs to this
			// call stack alone.
			if imp.parser.typeCheckInProgress[pkg] {
				// pkg's own real check — production files folded together
				// with its in-package _test.go files, owned by
				// typeCheckLocked — is already running further up this call
				// stack. That combined graph can have cycles that would not
				// exist in production code alone: "A_test.go imports B, B's
				// own test files (folded into B's check the same way) import
				// back to A" is reachable despite Go forbidding import
				// cycles outside tests. Recursing into typeCheckLocked here
				// would not terminate.
				//
				// Go itself resolves exactly this by never letting anything
				// import a package's test-augmented compiled form: only that
				// package's own test binary ever sees it, and every other
				// importer — including another package's test files — links
				// against the plain, non-test-augmented package. Mirror that
				// here: hand back pkg's production-files-only view instead of
				// an empty placeholder. A previous version of this function
				// returned gotypes.NewPackage(path, pkg.Name) — a package
				// with no symbols at all — which made every reference into
				// pkg from here "undefined". When the caller was itself a
				// PRODUCTION file (as in the case that motivated this fix:
				// package server's production go_server.go uses a type from
				// package syncdb, and syncdb's own in-package tests happen to
				// import something that imports back to server), that
				// undefined reference failed the caller's own "with tests"
				// type-check attempt — which cost the caller's ENTIRE test
				// suite its call-site visibility via the "retry without
				// tests" fallback below, even though nothing about the
				// caller's OWN test files was at fault.
				//
				// This rescue view is deliberately never written to
				// pkg.TypesPkg/TypesInfo/TypesChecked: those remain owned
				// exclusively by pkg's real, combined check, which still runs
				// (once, normally, non-reentrantly) whenever the traversal
				// reaches pkg directly rather than through this in-progress
				// path — and that real check is what every OTHER, non-cyclic
				// importer of pkg continues to see. Only a caller that hits
				// pkg while it is genuinely on the stack gets this narrower,
				// tests-excluded substitute, exactly mirroring the narrower
				// (production-only) view Go itself hands to such a caller.
				return imp.productionOnlyPackage(pkg)
			}

			// Type-check this dependency first (lazy/recursive). Call the
			// locked variant: re-entering TypeCheckPackage would try to take
			// checkMu a second time on this goroutine and deadlock.
			imp.parser.typeCheckLocked(imp.ws, pkg)

			// If, DURING that call, pkg was itself reached reentrantly —
			// its own combined check was still on this very call stack when
			// some OTHER import needed it, triggering the production-only
			// rescue in the in-progress branch above — then pkg.TypesPkg
			// (the real, combined check that just finished) and that rescue
			// value are two INDEPENDENTLY type-checked *types.Package
			// instances for the very same declarations: structurally
			// identical but nominally distinct types to go/types, since
			// Check() never reuses an Object across two separate
			// invocations. Handing out pkg.TypesPkg here, to this caller,
			// while some OTHER caller (reached earlier, nested inside this
			// very check) already received the rescue view, would let both
			// views collide the moment anything downstream tries to use a
			// value that crossed both — this is not hypothetical: it is
			// exactly how, before this guard, one package's own field
			// (typed via the rescue view of a dependency) failed to
			// type-check as an argument to a DIFFERENT package's function
			// (typed via that same dependency's real, combined view),
			// producing a spurious "cannot use ... as ... value" error that
			// then cost pkg's own test files their coverage via the
			// "retrying without" fallback below — the very failure mode
			// this whole rescue mechanism exists to avoid.
			//
			// Once pkg has been rescued anywhere in this importer's
			// lifetime, keep returning that SAME rescue value to every
			// caller from here on, including this one, so nothing
			// downstream ever sees both. pkg.TypesInfo (used for pkg's OWN
			// reference index — finding call sites inside pkg's own test
			// files) is unaffected: this only changes what OTHER packages
			// see when THEY import pkg.
			if rescued, ok := imp.productionOnly[pkg]; ok {
				return rescued, nil
			}

			imp.parser.mu.Lock()
			typesPkg := pkg.TypesPkg
			imp.parser.mu.Unlock()

			// Whenever a check attempt has completed — success or failure —
			// use whatever it produced. go/types.Config.Check returns a
			// populated package even on error, so re-checking a failed
			// package would not improve on this and would re-trigger the
			// same cascade of dependency re-checks the memoisation exists to
			// prevent. typesPkg is nil here only if the package had zero
			// parseable files, in which case there is nothing useful
			// to hand back and the stdlib fallback below is the last resort.
			if typesPkg != nil {
				return typesPkg, nil
			}
		}
	}

	// Not a workspace-local package. Load (once) and consult the set of
	// externally-resolved packages before falling back to importer.Default(),
	// which is GOPATH-based and cannot see anything in the module cache — on
	// a real workspace that fails on essentially every third-party import.
	imp.extOnce.Do(imp.loadExternal)
	if pkg, ok := imp.ext[path]; ok {
		return pkg, nil
	}

	// Last resort: stdlib/export data via the GOPATH-based importer. Reached
	// when the go/packages load failed entirely, found nothing for this path
	// (e.g. a deliberately-unresolvable import in a synthetic test
	// workspace), or the workspace had no go.mod to load against.
	if imp.std == nil {
		imp.std = importer.Default()
	}
	return imp.std.Import(path)
}

// productionOnlyPackage returns pkg's production-files-only *types.Package —
// no _test.go files at all, in-package or external — computing and
// memoizing it on first use. Called only from Import's in-progress branch
// above; see that comment for why this exists and what it deliberately does
// NOT touch (pkg.TypesPkg/TypesInfo/TypesChecked).
//
// This check can recurse into Import again for whatever pkg's production
// files themselves import, and can therefore hit this same in-progress
// rescue path for some OTHER package. That recursion is guaranteed to
// terminate: Go requires the production (non-test) import graph to be
// acyclic — a real cycle there would already be a compile error outside this
// tool — so walking it can revisit a package already resolved (returned from
// the memo map below) but can never re-enter one that is still being
// resolved on the current call stack.
func (imp *workspaceImporter) productionOnlyPackage(pkg *types.Package) (*gotypes.Package, error) {
	if cached, ok := imp.productionOnly[pkg]; ok {
		return cached, nil
	}

	var files []*ast.File
	for _, f := range pkg.Files {
		if f.AST != nil {
			files = append(files, f.AST)
		}
	}
	if len(files) == 0 {
		// Nothing to check — same "zero parseable files" case typeCheckLocked
		// itself guards, just without anywhere to store the result.
		return gotypes.NewPackage(pkg.ImportPath, pkg.Name), nil
	}

	// checkFiles routes back through imp (the same workspaceImporter) for
	// whatever these files import, so a second in-progress package reached
	// from here takes this same rescue path recursively. go/types.Config.Check
	// returns a non-nil, Complete() package even when checking fails (partial
	// results from whatever it managed to resolve) — see typeCheckLocked's
	// own comment on the same guarantee — so the result is usable regardless
	// of whether err is nil.
	typesPkg, _, _ := imp.parser.checkFiles(imp.ws, pkg, files)

	if imp.productionOnly == nil {
		imp.productionOnly = make(map[*types.Package]*gotypes.Package)
	}
	imp.productionOnly[pkg] = typesPkg

	return typesPkg, nil
}
