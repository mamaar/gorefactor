package types

import (
	"go/ast"
	"go/token"
)

// Workspace represents a complete Go workspace (module or GOPATH)
type Workspace struct {
	RootPath     string
	Module       *Module
	Packages     map[string]*Package  // package path -> Package
	FileSet      *token.FileSet
	Dependencies *DependencyGraph
}

// Package represents a single Go package
type Package struct {
	Path         string              // Import path
	Name         string              // Package name
	Dir          string              // Filesystem directory
	Files        map[string]*File    // filename -> File
	Symbols      *SymbolTable
	Imports      []string            // Direct imports
	TestFiles    map[string]*File    // Test files
}

// File represents a single Go source file
type File struct {
	Path            string
	Package         *Package
	AST             *ast.File
	OriginalContent []byte
	Modifications   []Modification
}

// Module represents Go module information
type Module struct {
	Path    string
	Version string
	GoMod   string  // Contents of go.mod
}

// Modification tracks changes to be made to a file
type Modification struct {
	Start   int     // Byte offset start
	End     int     // Byte offset end
	NewText string  // Replacement text
	Type    ModificationType
}

type ModificationType int

const (
	Insert ModificationType = iota
	Delete
	Replace
)

// DependencyGraph represents package and symbol dependencies
type DependencyGraph struct {
	PackageImports   map[string][]string           // package -> direct imports
	PackageDeps      map[string][]string           // package -> all dependencies
	SymbolDeps       map[string]map[string][]string // package -> symbol -> dependencies
	ImportCycles     [][]string                    // detected import cycles
}