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
	"strings"
	"sync"

	"github.com/mamaar/gorefactor/pkg/types"
)

// Parser handles Go code parsing and AST management
type GoParser struct {
	fileSet  *token.FileSet
	logger   *slog.Logger
	importer *workspaceImporter
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
		wg.Add(1)
		go func() {
			defer wg.Done()
			for idx := range dirCh {
				pkg, err := p.ParsePackage(pkgDirs[idx])
				results[idx] = pkgResult{pkg: pkg, err: err}
			}
		}()
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
	for _, s := range slice {
		if s == item {
			return true
		}
	}
	return false
}

// EnsureTypeChecked runs type-checking on a package if it hasn't been done yet.
// This enables lazy/on-demand type-checking instead of eager upfront checking.
func (p *GoParser) EnsureTypeChecked(ws *types.Workspace, pkg *types.Package) {
	if pkg.TypesPkg != nil {
		return
	}
	p.TypeCheckPackage(ws, pkg)
}

// TypeCheckPackage runs go/types type-checking on a package.
// Results are stored in pkg.TypesInfo and pkg.TypesPkg.
// Errors are silently ignored — packages that fail type-checking
// will have nil TypesInfo and fall back to AST-based inference.
func (p *GoParser) TypeCheckPackage(ws *types.Workspace, pkg *types.Package) {
	var files []*ast.File
	for _, f := range pkg.Files {
		if f.AST != nil {
			files = append(files, f.AST)
		}
	}
	if len(files) == 0 {
		return
	}

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
	if err != nil {
		p.logger.Debug("type-checking failed (falling back to AST inference)", "package", pkg.ImportPath, "err", err)
		// Still store partial results — go/types populates info even on errors
		pkg.TypesInfo = info
		return
	}
	pkg.TypesInfo = info
	pkg.TypesPkg = typesPkg
}

// workspaceImporter implements go/types.Importer using workspace-local packages
// with fallback to source-based importing for stdlib/external packages.
type workspaceImporter struct {
	ws     *types.Workspace
	fset   *token.FileSet
	parser *GoParser
	std    gotypes.Importer
}

func (imp *workspaceImporter) Import(path string) (*gotypes.Package, error) {
	// Check if this is a workspace-local package
	if fsPath, ok := imp.ws.ImportToPath[path]; ok {
		if pkg, ok := imp.ws.Packages[fsPath]; ok {
			if pkg.TypesPkg != nil {
				return pkg.TypesPkg, nil
			}
			// Type-check this dependency first (lazy/recursive)
			imp.parser.TypeCheckPackage(imp.ws, pkg)
			if pkg.TypesPkg != nil {
				return pkg.TypesPkg, nil
			}
		}
	}
	// Fall back to stdlib/export data
	if imp.std == nil {
		imp.std = importer.Default()
	}
	return imp.std.Import(path)
}
