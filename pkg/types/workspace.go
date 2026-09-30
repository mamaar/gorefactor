package types

import (
	"go/ast"
	"go/token"
	gotypes "go/types"
)

// Workspace represents a complete Go workspace (module or GOPATH)
type Workspace struct {
	RootPath     string
	Module       *Module
	Packages     map[string]*Package // filesystem path -> Package
	ImportToPath map[string]string   // import path -> filesystem path
	FileSet      *token.FileSet
	Dependencies *DependencyGraph

	// BuildConfig is the Key() of the analysis.BuildConfig the packages were
	// selected under.
	BuildConfig string
}

// Package represents a single Go package
type Package struct {
	Path       string           // Filesystem path
	ImportPath string           // Go import path (e.g., "github.com/foo/bar")
	Name       string           // Package name
	Dir        string           // Filesystem directory
	Files      map[string]*File // filename -> File
	Symbols    *SymbolTable
	Imports    []string         // Direct imports
	TestFiles  map[string]*File // Test files
	TypesInfo  *gotypes.Info    // Semantic type info (may be nil if type-checking failed)

	// TypesPkg is the type-checked package. go/types.Config.Check returns a
	// non-nil, Complete() package even when checking fails (partial results
	// from whatever it managed to resolve), so this is populated on both
	// success and failure once TypesChecked is true. It stays nil only for
	// packages that had zero parseable files to check at all.
	TypesPkg *gotypes.Package

	// TypesChecked reports whether a type-check attempt has run to completion,
	// success or failure. This is the memoisation guard used by
	// (*analysis.GoParser).EnsureTypeChecked/TypeCheckPackage and by
	// workspaceImporter.Import. Guarding on TypesPkg != nil instead would
	// treat "checked and failed" as "never checked" and so re-check — and
	// recursively re-check every dependency of — any failing package on every
	// import, which is exponential over a deep import graph.
	TypesChecked bool

	// TypesErr is the error returned by go/types.Config.Check, or nil when the
	// package checked cleanly. Consumers that need to know whether
	// TypesInfo/TypesPkg are complete rather than best-effort should consult
	// this rather than inferring it from nil-ness.
	TypesErr error

	// ExcludedTestFiles maps the name of each test file left out of TypesInfo
	// to the first type error located in it. Defs and Uses hold nothing for
	// these files, so lookups by go/types object identity do not see them.
	ExcludedTestFiles map[string]error

	// ProductionErrs maps the name of each non-test file to the first type
	// error located in it. TypesInfo holds only what the checker resolved in
	// such a file: identifiers inside erroring expressions may be absent.
	ProductionErrs map[string]error

	// UnattributedTestErr is set when dropping the test files that carry errors
	// did not settle and every remaining test file was dropped. Files without
	// an error of their own then map to this error in ExcludedTestFiles.
	UnattributedTestErr error
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
	GoMod   string // Contents of go.mod
}

// Modification tracks changes to be made to a file
type Modification struct {
	Start   int    // Byte offset start
	End     int    // Byte offset end
	NewText string // Replacement text
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
	PackageImports map[string][]string            // package -> direct imports
	PackageDeps    map[string][]string            // package -> all dependencies
	SymbolDeps     map[string]map[string][]string // package -> symbol -> dependencies
	ImportCycles   [][]string                     // detected import cycles
}
