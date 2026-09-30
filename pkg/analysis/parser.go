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

	// checkPassHook, if set, is invoked for every go/types pass over pkg's files.
	checkPassHook func(pkg *types.Package)

	// loadPackages stands in for packages.Load in tests.
	loadPackages func(cfg *packages.Config, patterns ...string) ([]*packages.Package, error)
}

// NewParser returns a parser for the host build configuration with an empty
// source tree of its own, for use outside a WorkspaceSet.
func NewParser(logger *slog.Logger) *GoParser {
	return newViewParser(logger, NewSourceTree(token.NewFileSet()), DefaultBuildConfig())
}

// newViewParser returns a parser that selects files from tree under cfg.
func newViewParser(logger *slog.Logger, tree *SourceTree, cfg BuildConfig) *GoParser {
	return &GoParser{fileSet: tree.fset, logger: logger, buildConfig: cfg, sources: tree, loadPackages: packages.Load}
}

// BuildConfig returns the configuration files are selected under.
func (p *GoParser) BuildConfig() BuildConfig { return p.buildConfig }

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

func absWorkspaceRoot(rootPath string) (string, error) {
	abs, err := filepath.Abs(rootPath)
	if err != nil {
		return "", &types.RefactorError{
			Type:    types.FileSystemError,
			Message: fmt.Sprintf("failed to get absolute path for workspace: %v", err),
			File:    rootPath,
		}
	}
	return abs, nil
}

func loadSources(tree *SourceTree, absRoot string) error {
	if err := tree.Load(absRoot); err != nil {
		return &types.RefactorError{
			Type:    types.FileSystemError,
			Message: fmt.Sprintf("failed to parse workspace: %v", err),
			File:    absRoot,
			Cause:   err,
		}
	}
	return nil
}

// readModule returns the module declared by absRoot/go.mod, or nil without one.
func readModule(absRoot string) *types.Module {
	content, err := os.ReadFile(filepath.Join(absRoot, "go.mod"))
	if err != nil {
		return nil
	}
	return parseGoMod(content)
}

// projectWorkspace builds the workspace the build config selects from the
// source tree, without reading or parsing anything, and binds p's importer to it.
func (p *GoParser) projectWorkspace(absRoot string, module *types.Module) *types.Workspace {
	p.logger.Info("projecting workspace", "path", absRoot, "config", p.buildConfig.Key())

	workspace := &types.Workspace{
		RootPath:     absRoot,
		Packages:     make(map[string]*types.Package),
		ImportToPath: make(map[string]string),
		FileSet:      p.fileSet,
		BuildConfig:  p.buildConfig.Key(),
	}
	if module != nil {
		own := *module
		workspace.Module = &own
	}

	var pkgDirs []string
	for _, dir := range p.sources.Dirs() {
		if p.hasGoFiles(dir) {
			pkgDirs = append(pkgDirs, dir)
		}
	}

	proj := &packageProjector{parser: p, dirs: pkgDirs, next: make(chan int, len(pkgDirs)), results: make([]projectedPackage, len(pkgDirs))}
	for i := range pkgDirs {
		proj.next <- i
	}
	close(proj.next)

	var wg sync.WaitGroup
	for range workerCount(len(pkgDirs)) {
		wg.Go(proj.work)
	}
	wg.Wait()

	for i, res := range proj.results {
		if res.err != nil {
			fmt.Fprintf(os.Stderr, "Warning: failed to parse package %s: %v\n", pkgDirs[i], res.err)
			continue
		}
		if res.pkg == nil {
			continue
		}
		workspace.Packages[res.pkg.Path] = res.pkg
	}

	if workspace.Module != nil {
		for fsPath, pkg := range workspace.Packages {
			importPath := computeImportPath(workspace, fsPath)
			pkg.ImportPath = importPath
			workspace.ImportToPath[importPath] = fsPath
		}
	}

	p.logger.Info("workspace projected", "packages", len(workspace.Packages), "module", workspace.Module)

	// One importer per workspace keeps stdlib type identities consistent across
	// all TypeCheckPackage calls.
	p.importer = &workspaceImporter{ws: workspace, fset: workspace.FileSet, parser: p}

	return workspace
}

type projectedPackage struct {
	pkg *types.Package
	err error
}

// packageProjector builds the packages of dirs on several goroutines; each
// worker writes only its own slots of results.
type packageProjector struct {
	parser  *GoParser
	dirs    []string
	next    chan int
	results []projectedPackage
}

func (j *packageProjector) work() {
	for i := range j.next {
		pkg, err := j.parser.ParsePackage(j.dirs[i])
		j.results[i] = projectedPackage{pkg: pkg, err: err}
	}
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

func parseGoMod(content []byte) *types.Module {
	module := &types.Module{GoMod: string(content)}
	for _, line := range strings.Split(string(content), "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "module ") {
			module.Path = strings.TrimSpace(strings.TrimPrefix(line, "module"))
		}
	}
	return module
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
	pkg.ExcludedTestFiles = nil
	pkg.UnattributedTestErr = nil
	pkg.TypesChecked = false
}

// releaseTypeChecks invalidates every package of ws once no check session is
// running. The production-only packages go with them: one kept across a
// re-check would be a second, distinct copy of declarations checked anew.
func (p *GoParser) releaseTypeChecks(ws *types.Workspace) {
	p.checkMu.Lock()
	defer p.checkMu.Unlock()
	if p.importer != nil {
		p.importer.productionOnly = nil
	}
	for _, pkg := range ws.Packages {
		p.InvalidateTypeCheck(pkg)
	}
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

	files := productionASTs(pkg)
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
	inPackage, external := testASTs(pkg, pkgName)

	res := p.checkSheddingTests(ws, pkg, files, inPackage)
	if res.err != nil {
		p.logger.Debug("type-checking failed (falling back to AST inference)", "package", pkg.ImportPath, "err", res.err)
	}

	p.mu.Lock()
	// go/types.Config.Check returns a non-nil, Complete() package even on
	// error (partial results from whatever it managed to resolve), so store
	// it unconditionally — dependents can use best-effort info instead of
	// nothing at all.
	pkg.TypesPkg = res.pkg
	pkg.TypesInfo = res.info
	pkg.TypesErr = res.err
	pkg.ExcludedTestFiles = res.excluded
	pkg.UnattributedTestErr = res.unattributed
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

	p.checkExternalTestPackage(ws, pkg, external)
}

// productionASTs returns the parsed non-test files of pkg in file-name order.
func productionASTs(pkg *types.Package) []*ast.File {
	var files []*ast.File
	for _, name := range slices.Sorted(maps.Keys(pkg.Files)) {
		if f := pkg.Files[name]; f.AST != nil {
			files = append(files, f.AST)
		}
	}
	return files
}

// testAST is a parsed test file under its key in Package.TestFiles.
type testAST struct {
	name string
	ast  *ast.File
}

// testASTs splits pkg's parsed test files, in file-name order, into those
// declaring pkgName and those declaring another package.
func testASTs(pkg *types.Package, pkgName string) (inPackage, external []testAST) {
	for _, name := range slices.Sorted(maps.Keys(pkg.TestFiles)) {
		f := pkg.TestFiles[name]
		switch {
		case f.AST == nil:
		case f.AST.Name.Name == pkgName:
			inPackage = append(inPackage, testAST{name: name, ast: f.AST})
		default:
			external = append(external, testAST{name: name, ast: f.AST})
		}
	}
	return inPackage, external
}

// testCheck is the outcome of checking files together with the test files
// that type-check alongside them.
type testCheck struct {
	pkg  *gotypes.Package
	info *gotypes.Info
	err  error // first error of the last pass

	excluded     map[string]error
	unattributed error
}

// checkSheddingTests checks base together with tests, drops the test files
// that carry errors and checks again, at most maxTestExclusionPasses times.
// A test file broken only by the loss of a dropped one is dropped in the next
// pass with its own error. If test files still carry errors after the last
// pass, all remaining ones are dropped and the first such error is unattributed.
func (p *GoParser) checkSheddingTests(ws *types.Workspace, pkg *types.Package, base []*ast.File, tests []testAST) testCheck {
	var res testCheck
	for pass := 0; ; pass++ {
		files := slices.Clone(base)
		for _, t := range tests {
			files = append(files, t.ast)
		}
		if len(files) == 0 {
			res.pkg, res.info, res.err = nil, nil, nil
			return res
		}

		errs := &typeErrors{}
		res.pkg, res.info, res.err = p.checkFiles(ws, pkg, files, errs)
		bad := errs.firstPerFile(ws.FileSet, tests)
		if len(bad) == 0 {
			return res
		}
		if pass == maxTestExclusionPasses {
			res.unattributed = firstTestError(tests, bad)
			p.logger.Debug("test files still fail after dropping those with errors, dropping all",
				"package", pkg.ImportPath, "err", res.unattributed)
		}
		tests = res.drop(tests, bad)
	}
}

// drop records the tests named in bad as excluded, or every test once the
// check is unattributed, and returns the tests that stay.
func (c *testCheck) drop(tests []testAST, bad map[string]error) []testAST {
	if c.excluded == nil {
		c.excluded = make(map[string]error)
	}
	var kept []testAST
	for _, t := range tests {
		err, isBad := bad[t.name]
		switch {
		case isBad:
			c.excluded[t.name] = err
		case c.unattributed != nil:
			c.excluded[t.name] = c.unattributed
		default:
			kept = append(kept, t)
		}
	}
	return kept
}

// firstTestError returns the error of the first test, in order, that has one.
func firstTestError(tests []testAST, bad map[string]error) error {
	for _, t := range tests {
		if err, ok := bad[t.name]; ok {
			return err
		}
	}
	return nil
}

// typeErrors collects the errors go/types reports during one check.
type typeErrors struct {
	errs []gotypes.Error
}

func (c *typeErrors) add(err error) {
	if terr, ok := err.(gotypes.Error); ok {
		c.errs = append(c.errs, terr)
	}
}

// firstPerFile returns, for each of tests that an error is located in, the
// first such error by position. Continuation lines of an error, which point
// at related declarations in other files, are not counted.
func (c *typeErrors) firstPerFile(fset *token.FileSet, tests []testAST) map[string]error {
	names := make(map[*token.File]string, len(tests))
	for _, t := range tests {
		names[fset.File(t.ast.Package)] = t.name
	}
	first := make(map[string]gotypes.Error)
	for _, e := range c.errs {
		if !e.Pos.IsValid() || strings.HasPrefix(e.Msg, "\t") {
			continue
		}
		name, ok := names[fset.File(e.Pos)]
		if !ok {
			continue
		}
		if prev, seen := first[name]; !seen || e.Pos < prev.Pos {
			first[name] = e
		}
	}
	out := make(map[string]error, len(first))
	for name, e := range first {
		out[name] = e
	}
	return out
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
//
// External test files that carry errors are excluded like in-package ones and
// recorded in the same per-file record.
func (p *GoParser) checkExternalTestPackage(ws *types.Workspace, pkg *types.Package, tests []testAST) {
	if len(tests) == 0 {
		return
	}

	// checkFiles uses pkg.ImportPath as the Check() path argument, which is
	// harmless here: this call's resulting *types.Package is discarded
	// (nothing ever imports an external test package, so there is nothing to
	// store it as), and go/types.Checker verifies package-name consistency
	// only among the files passed to ONE Check() call, never against that
	// path string — it is purely a label on the (unused) result.
	res := p.checkSheddingTests(ws, pkg, nil, tests)
	if res.err != nil {
		p.logger.Debug("external test package type-checking failed", "package", pkg.ImportPath, "err", res.err)
	}

	p.mu.Lock()
	defer p.mu.Unlock()
	if len(res.excluded) > 0 {
		// A fresh map: readers may already hold the one published above.
		excluded := maps.Clone(pkg.ExcludedTestFiles)
		if excluded == nil {
			excluded = make(map[string]error, len(res.excluded))
		}
		maps.Copy(excluded, res.excluded)
		pkg.ExcludedTestFiles = excluded
	}
	if pkg.UnattributedTestErr == nil {
		pkg.UnattributedTestErr = res.unattributed
	}
	if res.info == nil || pkg.TypesInfo == nil {
		// Every external test file was excluded, or pkg's own check had
		// nowhere to merge into.
		return
	}
	for ident, obj := range res.info.Defs {
		pkg.TypesInfo.Defs[ident] = obj
	}
	for ident, obj := range res.info.Uses {
		pkg.TypesInfo.Uses[ident] = obj
	}
}

// BindWorkspace attaches p to ws as ws's workspace importer, without going
// through a WorkspaceSet. It exists for one-off type-checking sessions that
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
// yet via a WorkspaceSet or BindWorkspace.
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

// maxTestExclusionPasses bounds how often the test files that carry errors
// are dropped and the rest checked again.
const maxTestExclusionPasses = 3

// checkFiles runs go/types over the given file set, returning the populated Info
// even when checking fails (go/types fills it in partially on error).
// Every error go/types reports is added to errs.
func (p *GoParser) checkFiles(ws *types.Workspace, pkg *types.Package, files []*ast.File, errs *typeErrors) (*gotypes.Package, *gotypes.Info, error) {
	if p.checkPassHook != nil {
		p.checkPassHook(pkg)
	}
	conf := NewTypesConfig(p.importer, errs.add)
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
	// projectWorkspace: most callers only ever need a handful of stdlib/module
	// packages, and eagerly loading the whole dependency graph for every
	// workspace (including the many synthetic, no-dependency workspaces the
	// test suite builds in t.TempDir()) would be pure waste.
	extOnce sync.Once
	// ext holds every package the go/packages load of the workspace listed.
	// Set once by extOnce; nil when nothing was loaded.
	ext *externalSet
	// extErr records why the load produced no (or a partial) result, purely
	// for the Debug log below — a failure here is not fatal, it just means
	// the importer.Default() fallback is all that's left.
	extErr error

	// fallback is the primary view's importer; its external packages stand in
	// when this view's own load fails. Nil on the primary.
	fallback *workspaceImporter
	// fellBack is set, under parser.mu, once ext was taken from fallback.
	fellBack bool

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

	if imp.parser.buildConfig.isPort(runtime.GOOS, runtime.GOARCH) && !imp.hasExternalImports() {
		// Nothing in this workspace imports a third-party module, so the whole
		// point of the load — resolving module-cache packages that
		// importer.Default() cannot see — does not apply. Skipping matters
		// because packages.Load shells out to the go tool: paying that on a
		// workspace whose imports are entirely stdlib and local is pure
		// overhead, and the test suite builds dozens of exactly such synthetic
		// workspaces. Any other port needs the load even for the stdlib:
		// importer.Default() only serves the host's.
		return
	}

	env, flags := externalLoadSettings(imp.parser.buildConfig, runtime.GOOS, runtime.GOARCH)
	cfg := &packages.Config{
		// No NeedTypes: go/packages would type-check every listed package from
		// source, function bodies and the workspace's own packages included.
		Mode: packages.NeedName | packages.NeedCompiledGoFiles | packages.NeedImports | packages.NeedDeps,
		Dir:  imp.ws.RootPath,
		Fset: imp.fset,
		// Tests: true adds each package's test-binary variants (and their
		// Imports) to the load, which is the only way a third-party module
		// that is imported EXCLUSIVELY from _test.go files anywhere in the
		// workspace ever reaches imp.ext. Without it, walk below never sees
		// that import edge at all — not even the workspace-local package that
		// carries it — because a bare `packages.Load(cfg, "./...")` only
		// resolves each package's non-test Imports. TypeCheckPackage then
		// fails to resolve that import and excludes the test files that carry
		// it, which blinds CallSitesOf/NonCallUsesOf to their call sites.
		Tests: true,
		// GOPROXY=off is load-bearing, not a performance nicety: this runs
		// inside an MCP request, potentially against a workspace whose module
		// cache is cold. Without it, a missing module triggers a network
		// fetch that can hang the request. GOPROXY=off makes resolution use
		// only what's already in the local module cache and fail fast
		// otherwise, which is exactly the "degrade gracefully" behavior this
		// fallback needs — a failed load just leaves imp.ext empty and control
		// falls through to importer.Default().
		Env:        append(os.Environ(), env...),
		BuildFlags: flags,
	}

	roots, err := imp.parser.loadPackages(cfg, "./...")
	if err != nil {
		imp.parser.logger.Debug("external package load failed", "config", imp.parser.buildConfig.Key(), "err", err)
		imp.failExternal(err)
		return
	}

	// Workspace packages are listed too, since their imports are what reaches
	// the third-party ones; Import resolves them before it consults ext, so
	// they are never type-checked here.
	imp.ext = newExternalSet(imp.fset, imp.parser.buildConfig.GOARCH, roots)
}

// externalLoadSettings returns the environment additions and build flags that
// make packages.Load resolve third-party modules under cfg. Cgo stays off for
// any port but the host's, which would need a C cross toolchain.
func externalLoadSettings(cfg BuildConfig, hostOS, hostArch string) (env, flags []string) {
	cgo := "0"
	if cfg.Cgo && cfg.isPort(hostOS, hostArch) {
		cgo = "1"
	}
	env = []string{"GOPROXY=off", "GOOS=" + cfg.GOOS, "GOARCH=" + cfg.GOARCH, "CGO_ENABLED=" + cgo}
	if tags := normalizeTags(cfg.Tags); len(tags) > 0 {
		flags = []string{"-tags=" + strings.Join(tags, ",")}
	}
	return env, flags
}

// failExternal records a failed load and, on a secondary view, adopts the
// primary view's external packages in its place.
func (imp *workspaceImporter) failExternal(err error) {
	if imp.fallback != nil {
		imp.fallback.extOnce.Do(imp.fallback.loadExternal)
		imp.ext = imp.fallback.ext
	}
	imp.parser.mu.Lock()
	defer imp.parser.mu.Unlock()
	imp.extErr = err
	imp.fellBack = imp.fallback != nil
}

// externalFallback reports whether the external packages were taken from
// another view, and the load error that caused it.
func (p *GoParser) externalFallback() (bool, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.importer == nil || !p.importer.fellBack {
		return false, nil
	}
	return true, p.importer.extErr
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
				// an empty placeholder. A package with no symbols at all
				// would make every reference into pkg from the caller
				// "undefined", in its production files as much as its tests.
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
			// value that crossed both: one package's own field (typed via
			// the rescue view of a dependency) fails to type-check as an
			// argument to a DIFFERENT package's function (typed via that
			// same dependency's real, combined view), with a spurious
			// "cannot use ... as ... value" error.
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
	if pkg := imp.ext.lookup(path); pkg != nil {
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

	files := productionASTs(pkg)
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
	typesPkg, _, _ := imp.parser.checkFiles(imp.ws, pkg, files, &typeErrors{})

	if imp.productionOnly == nil {
		imp.productionOnly = make(map[*types.Package]*gotypes.Package)
	}
	imp.productionOnly[pkg] = typesPkg

	return typesPkg, nil
}
