package analysis

import (
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/mamaar/gorefactor/pkg/types"
)

func TestNewParser(t *testing.T) {
	parser := NewParser(slog.New(slog.NewTextHandler(io.Discard, nil)))
	if parser == nil {
		t.Fatal("Expected NewParser to return a non-nil parser")
	}

	if parser.fileSet == nil {
		t.Error("Expected parser to have a non-nil fileSet")
	}
}

func TestParser_ParseFile(t *testing.T) {
	parser := NewParser(slog.New(slog.NewTextHandler(io.Discard, nil)))

	// Create a temporary test file
	tempDir := t.TempDir()
	testFile := filepath.Join(tempDir, "test.go")
	testContent := `package test

import "fmt"

// TestFunction is a test function
func TestFunction() {
	fmt.Println("Hello, World!")
}

const TestConst = 42
var testVar = "test"
`

	err := os.WriteFile(testFile, []byte(testContent), 0644)
	if err != nil {
		t.Fatalf("Failed to create test file: %v", err)
	}

	// Parse the file
	file, err := parser.ParseFile(testFile)
	if err != nil {
		t.Fatalf("Failed to parse file: %v", err)
	}

	// Validate the parsed file
	if file.Path != testFile {
		t.Errorf("Expected Path to be '%s', got '%s'", testFile, file.Path)
	}

	if file.AST == nil {
		t.Error("Expected AST to be non-nil")
	}

	if file.AST.Name.Name != "test" {
		t.Errorf("Expected package name to be 'test', got '%s'", file.AST.Name.Name)
	}

	if string(file.OriginalContent) != testContent {
		t.Error("Expected OriginalContent to match test content")
	}

	if len(file.Modifications) != 0 {
		t.Errorf("Expected 0 modifications, got %d", len(file.Modifications))
	}

	// Check imports
	if len(file.AST.Imports) != 1 {
		t.Errorf("Expected 1 import, got %d", len(file.AST.Imports))
	}

	if file.AST.Imports[0].Path.Value != `"fmt"` {
		t.Errorf("Expected import to be '\"fmt\"', got %s", file.AST.Imports[0].Path.Value)
	}
}

func TestParser_ParseFile_NonExistent(t *testing.T) {
	parser := NewParser(slog.New(slog.NewTextHandler(io.Discard, nil)))

	// Try to parse a non-existent file
	_, err := parser.ParseFile("/non/existent/file.go")
	if err == nil {
		t.Error("Expected error when parsing non-existent file")
	}

	// Check that it's a RefactorError
	if refErr, ok := err.(*types.RefactorError); ok {
		if refErr.Type != types.FileSystemError {
			t.Errorf("Expected FileSystemError, got %v", refErr.Type)
		}
	} else {
		t.Error("Expected RefactorError")
	}
}

func TestParser_ParseFile_InvalidSyntax(t *testing.T) {
	parser := NewParser(slog.New(slog.NewTextHandler(io.Discard, nil)))

	// Create a temporary file with invalid syntax
	tempDir := t.TempDir()
	testFile := filepath.Join(tempDir, "invalid.go")
	invalidContent := `package test

func InvalidSyntax( {
	// Missing closing parenthesis
}
`

	err := os.WriteFile(testFile, []byte(invalidContent), 0644)
	if err != nil {
		t.Fatalf("Failed to create test file: %v", err)
	}

	// Try to parse the invalid file
	_, err = parser.ParseFile(testFile)
	if err == nil {
		t.Error("Expected error when parsing invalid syntax")
	}

	// Check that it's a RefactorError with ParseError type
	if refErr, ok := err.(*types.RefactorError); ok {
		if refErr.Type != types.ParseError {
			t.Errorf("Expected ParseError, got %v", refErr.Type)
		}
	} else {
		t.Error("Expected RefactorError")
	}
}

func TestParser_ParsePackage(t *testing.T) {
	parser := NewParser(slog.New(slog.NewTextHandler(io.Discard, nil)))

	// Create a temporary package directory
	tempDir := t.TempDir()

	// Create main.go
	mainContent := `package main

import "fmt"

func main() {
	fmt.Println("Hello, World!")
}
`
	err := os.WriteFile(filepath.Join(tempDir, "main.go"), []byte(mainContent), 0644)
	if err != nil {
		t.Fatalf("Failed to create main.go: %v", err)
	}

	// Create utils.go
	utilsContent := `package main

import "strings"

func ToUpper(s string) string {
	return strings.ToUpper(s)
}
`
	err = os.WriteFile(filepath.Join(tempDir, "utils.go"), []byte(utilsContent), 0644)
	if err != nil {
		t.Fatalf("Failed to create utils.go: %v", err)
	}

	// Create a test file
	testContent := `package main

import "testing"

func TestToUpper(t *testing.T) {
	result := ToUpper("hello")
	if result != "HELLO" {
		t.Errorf("Expected HELLO, got %s", result)
	}
}
`
	err = os.WriteFile(filepath.Join(tempDir, "main_test.go"), []byte(testContent), 0644)
	if err != nil {
		t.Fatalf("Failed to create main_test.go: %v", err)
	}

	// Parse the package
	pkg, err := parser.ParsePackage(tempDir)
	if err != nil {
		t.Fatalf("Failed to parse package: %v", err)
	}

	// Validate the package
	if pkg.Name != "main" {
		t.Errorf("Expected package name to be 'main', got '%s'", pkg.Name)
	}

	if pkg.Dir != tempDir {
		t.Errorf("Expected Dir to be '%s', got '%s'", tempDir, pkg.Dir)
	}

	if len(pkg.Files) != 2 {
		t.Errorf("Expected 2 regular files, got %d", len(pkg.Files))
	}

	if len(pkg.TestFiles) != 1 {
		t.Errorf("Expected 1 test file, got %d", len(pkg.TestFiles))
	}

	// Check imports
	expectedImports := []string{"fmt", "strings"}
	if len(pkg.Imports) != len(expectedImports) {
		t.Errorf("Expected %d imports, got %d", len(expectedImports), len(pkg.Imports))
	}

	for _, expectedImport := range expectedImports {
		found := slices.Contains(pkg.Imports, expectedImport)
		if !found {
			t.Errorf("Expected import '%s' not found", expectedImport)
		}
	}

	// Check that files have back-references to package
	for _, file := range pkg.Files {
		if file.Package != pkg {
			t.Error("Expected file to have back-reference to package")
		}
	}

	for _, file := range pkg.TestFiles {
		if file.Package != pkg {
			t.Error("Expected test file to have back-reference to package")
		}
	}
}

func TestParser_ParsePackage_Empty(t *testing.T) {
	parser := NewParser(slog.New(slog.NewTextHandler(io.Discard, nil)))

	// Create an empty directory
	tempDir := t.TempDir()

	// Try to parse empty package
	_, err := parser.ParsePackage(tempDir)
	if err == nil {
		t.Error("Expected error when parsing empty package")
	}

	// Check that it's a RefactorError
	if refErr, ok := err.(*types.RefactorError); ok {
		if refErr.Type != types.ParseError {
			t.Errorf("Expected ParseError, got %v", refErr.Type)
		}
	} else {
		t.Error("Expected RefactorError")
	}
}

func TestParser_ParseWorkspace(t *testing.T) {
	parser := NewParser(slog.New(slog.NewTextHandler(io.Discard, nil)))

	// Create a temporary workspace
	tempDir := t.TempDir()

	// Create go.mod
	goModContent := `module test/workspace

go 1.21
`
	err := os.WriteFile(filepath.Join(tempDir, "go.mod"), []byte(goModContent), 0644)
	if err != nil {
		t.Fatalf("Failed to create go.mod: %v", err)
	}

	// Create main package
	mainDir := filepath.Join(tempDir, "cmd", "main")
	err = os.MkdirAll(mainDir, 0755)
	if err != nil {
		t.Fatalf("Failed to create main directory: %v", err)
	}

	mainContent := `package main

import "fmt"

func main() {
	fmt.Println("Hello, World!")
}
`
	err = os.WriteFile(filepath.Join(mainDir, "main.go"), []byte(mainContent), 0644)
	if err != nil {
		t.Fatalf("Failed to create main.go: %v", err)
	}

	// Create a library package
	libDir := filepath.Join(tempDir, "pkg", "lib")
	err = os.MkdirAll(libDir, 0755)
	if err != nil {
		t.Fatalf("Failed to create lib directory: %v", err)
	}

	libContent := `package lib

func Add(a, b int) int {
	return a + b
}
`
	err = os.WriteFile(filepath.Join(libDir, "lib.go"), []byte(libContent), 0644)
	if err != nil {
		t.Fatalf("Failed to create lib.go: %v", err)
	}

	// Parse the workspace
	ws, err := parser.ParseWorkspace(tempDir)
	if err != nil {
		t.Fatalf("Failed to parse workspace: %v", err)
	}

	// Validate the workspace
	if ws.RootPath != tempDir {
		t.Errorf("Expected RootPath to be '%s', got '%s'", tempDir, ws.RootPath)
	}

	if ws.Module == nil {
		t.Error("Expected Module to be non-nil")
	} else if ws.Module.Path != "test/workspace" {
		t.Errorf("Expected module path to be 'test/workspace', got '%s'", ws.Module.Path)
	}

	if len(ws.Packages) < 2 {
		t.Errorf("Expected at least 2 packages, got %d", len(ws.Packages))
	}

	if ws.FileSet == nil {
		t.Error("Expected FileSet to be non-nil")
	}

	// Check that packages are properly linked
	for _, pkg := range ws.Packages {
		if pkg.Name == "" {
			t.Error("Expected package to have a name")
		}
		if len(pkg.Files) == 0 {
			t.Error("Expected package to have files")
		}
	}
}

func TestParser_UpdateFile(t *testing.T) {
	parser := NewParser(slog.New(slog.NewTextHandler(io.Discard, nil)))

	// Create a temporary test file
	tempDir := t.TempDir()
	testFile := filepath.Join(tempDir, "test.go")
	originalContent := `package test

func Original() {
	// original function
}
`

	err := os.WriteFile(testFile, []byte(originalContent), 0644)
	if err != nil {
		t.Fatalf("Failed to create test file: %v", err)
	}

	// Parse the file
	file, err := parser.ParseFile(testFile)
	if err != nil {
		t.Fatalf("Failed to parse file: %v", err)
	}

	// Add some modifications
	file.Modifications = []types.Modification{
		{
			Start:   strings.Index(originalContent, "Original"),
			End:     strings.Index(originalContent, "Original") + len("Original"),
			NewText: "Modified",
			Type:    types.Replace,
		},
	}

	// Update the file
	err = parser.UpdateFile(file)
	if err != nil {
		t.Fatalf("Failed to update file: %v", err)
	}

	// Check that modifications were cleared
	if len(file.Modifications) != 0 {
		t.Errorf("Expected modifications to be cleared, got %d", len(file.Modifications))
	}

	// Check that AST was updated (this is a basic check)
	if file.AST == nil {
		t.Error("Expected AST to be non-nil after update")
	}
}

func TestParser_UpdateFile_NoModifications(t *testing.T) {
	parser := NewParser(slog.New(slog.NewTextHandler(io.Discard, nil)))

	// Create a temporary test file
	tempDir := t.TempDir()
	testFile := filepath.Join(tempDir, "test.go")
	originalContent := `package test

func Test() {
	// test function
}
`

	err := os.WriteFile(testFile, []byte(originalContent), 0644)
	if err != nil {
		t.Fatalf("Failed to create test file: %v", err)
	}

	// Parse the file
	file, err := parser.ParseFile(testFile)
	if err != nil {
		t.Fatalf("Failed to parse file: %v", err)
	}

	originalAST := file.AST

	// Update file with no modifications
	err = parser.UpdateFile(file)
	if err != nil {
		t.Fatalf("Failed to update file: %v", err)
	}

	// AST should remain the same
	if file.AST != originalAST {
		t.Error("Expected AST to remain unchanged when no modifications")
	}
}
