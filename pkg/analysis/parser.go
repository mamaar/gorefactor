package analysis

import (
	"fmt"
	"go/ast"
	"go/importer"
	"go/parser"
	"go/token"
	gotypes "go/types"
	"io/fs"
	"log/slog"
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

func NewParser(logger *slog.Logger) *GoParser {
	return &GoParser{
		fileSet: token.NewFileSet(),
		logger:  logger,
	}
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

// ParsePackage parses all Go files in a package directory
func (p *GoParser) ParsePackage(dir string) (*types.Package, error) {
	pkg := &types.Package{
		Dir:       dir,
		Files:     make(map[string]*types.File),
		TestFiles: make(map[string]*types.File),
		Imports:   make([]string, 0),
	}

	err := filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}

		// Skip subdirectories for package parsing
		if d.IsDir() && path != dir {
			return filepath.SkipDir
		}

		// Only process .go files
		if !strings.HasSuffix(path, ".go") {
			return nil
		}

		file, err := p.ParseFile(path)
		if err != nil {
			return err
		}

		file.Package = pkg

		// Separate test files from regular files
		if strings.HasSuffix(path, "_test.go") {
			pkg.TestFiles[filepath.Base(path)] = file
		} else {
			pkg.Files[filepath.Base(path)] = file

			// Set package name and path from first non-test file
			if pkg.Name == "" {
				pkg.Name = file.AST.Name.Name
				pkg.Path = p.inferPackagePath(dir, file.AST.Name.Name)
			}

			// Collect imports
			for _, imp := range file.AST.Imports {
				importPath := strings.Trim(imp.Path.Value, "\"")
				if !contains(pkg.Imports, importPath) {
					pkg.Imports = append(pkg.Imports, importPath)
				}
			}
		}

		return nil
	})

	if err != nil {
		return nil, &types.RefactorError{
			Type:    types.FileSystemError,
			Message: fmt.Sprintf("failed to parse package: %v", err),
			File:    dir,
			Cause:   err,
		}
	}

	if pkg.Name == "" {
		return nil, &types.RefactorError{
			Type:    types.ParseError,
			Message: "no non-test Go files found in package",
			File:    dir,
		}
	}

	return pkg, nil
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

	// Phase 1: Discover package directories (sequential — filesystem walk is I/O bound and fast)
	var pkgDirs []string
	err = filepath.WalkDir(absRootPath, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}

		// Skip hidden directories and vendor
		if d.IsDir() {
			name := d.Name()
			if strings.HasPrefix(name, ".") || name == "vendor" {
				return filepath.SkipDir
			}
		}

		// Collect directories containing .go files
		if d.IsDir() {
			hasGoFiles, err := p.hasGoFiles(path)
			if err != nil {
				return err
			}
			if hasGoFiles {
				pkgDirs = append(pkgDirs, path)
			}
		}

		return nil
	})

	if err != nil {
		p.logger.Error("workspace discovery failed", "path", rootPath, "err", err)
		return nil, &types.RefactorError{
			Type:    types.FileSystemError,
			Message: fmt.Sprintf("failed to parse workspace: %v", err),
			File:    rootPath,
			Cause:   err,
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

func (p *GoParser) hasGoFiles(dir string) (bool, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return false, err
	}

	for _, entry := range entries {
		if !entry.IsDir() && strings.HasSuffix(entry.Name(), ".go") {
			return true, nil
		}
	}

	return false, nil
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
	if err != nil && len(withTests) > len(files) {
		// A test file that fails to type-check must not cost the whole package its
		// type info, so retry without them. The retry's result — success or
		// failure — is what gets recorded below.
		p.logger.Debug("type-checking with test files failed, retrying without", "package", pkg.ImportPath, "err", err)
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
	pkg.TypesChecked = true
	p.mu.Unlock()
}

// checkFiles runs go/types over the given file set, returning the populated Info
// even when checking fails (go/types fills it in partially on error).
func (p *GoParser) checkFiles(ws *types.Workspace, pkg *types.Package, files []*ast.File) (*gotypes.Package, *gotypes.Info, error) {
	conf := gotypes.Config{
		Importer: p.importer,
		Error:    func(err error) {}, // silently ignore type errors
	}
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
	var walk func(pkg *packages.Package)
	walk = func(pkg *packages.Package) {
		if pkg == nil || seen[pkg.PkgPath] {
			return
		}
		seen[pkg.PkgPath] = true

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
				// This package is being type-checked further up our own call
				// stack (a cycle reached through in-package _test.go files —
				// those are folded into the same check, so "A_test.go imports
				// B, B imports A" is reachable despite Go forbidding import
				// cycles outside tests). Recursing would not terminate, so
				// hand go/types a placeholder: it records a resolution error
				// for this import and unwinds normally.
				return gotypes.NewPackage(path, pkg.Name), nil
			}

			// Type-check this dependency first (lazy/recursive). Call the
			// locked variant: re-entering TypeCheckPackage would try to take
			// checkMu a second time on this goroutine and deadlock.
			imp.parser.typeCheckLocked(imp.ws, pkg)

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
