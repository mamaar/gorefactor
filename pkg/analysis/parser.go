package analysis

import (
	"fmt"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"github.com/mamaar/gorefactor/pkg/types"
)

// Parser handles Go code parsing and AST management
type GoParser struct {
	fileSet *token.FileSet
}

func NewParser() *GoParser {
	return &GoParser{
		fileSet: token.NewFileSet(),
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

// ParseWorkspace parses an entire Go workspace/module
func (p *GoParser) ParseWorkspace(rootPath string) (*types.Workspace, error) {
	// Convert to absolute path for consistency
	absRootPath, err := filepath.Abs(rootPath)
	if err != nil {
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

	// Walk directory tree to find Go packages
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

		// Look for directories containing .go files
		if d.IsDir() {
			hasGoFiles, err := p.hasGoFiles(path)
			if err != nil {
				return err
			}

			if hasGoFiles {
				pkg, err := p.ParsePackage(path)
				if err != nil {
					// Log error but continue parsing other packages
					fmt.Fprintf(os.Stderr, "Warning: failed to parse package %s: %v\n", path, err)
					return nil
				}

				workspace.Packages[pkg.Path] = pkg
			}
		}

		return nil
	})

	if err != nil {
		return nil, &types.RefactorError{
			Type:    types.FileSystemError,
			Message: fmt.Sprintf("failed to parse workspace: %v", err),
			File:    rootPath,
			Cause:   err,
		}
	}

	// After parsing packages, build import path mapping
	if workspace.Module != nil {
		for fsPath, pkg := range workspace.Packages {
			importPath := computeImportPath(workspace, fsPath)
			pkg.ImportPath = importPath
			workspace.ImportToPath[importPath] = fsPath
		}
	}

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
