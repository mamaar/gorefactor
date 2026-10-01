package refactor

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"sort"
	"strings"

	refactorTypes "github.com/mamaar/gorefactor/pkg/types"
)

// IsGeneratedSource reports whether src carries the standard
// "Code generated ... DO NOT EDIT." header before its package clause.
func IsGeneratedSource(filename string, src []byte) bool {
	file, err := parser.ParseFile(token.NewFileSet(), filename, src, parser.ParseComments|parser.PackageClauseOnly)
	if err != nil {
		return false
	}
	return ast.IsGenerated(file)
}

// GeneratedFileEdits rejects a change set that edits or deletes an existing
// generated Go file. Relocation changes and files not yet on disk are exempt.
func GeneratedFileEdits(changes []refactorTypes.Change) error {
	seen := make(map[string]bool)
	var offending []string
	for _, change := range changes {
		if change.Relocation || seen[change.File] || filepath.Ext(change.File) != ".go" {
			continue
		}
		seen[change.File] = true
		src, err := os.ReadFile(change.File)
		if err != nil || !IsGeneratedSource(change.File, src) {
			continue
		}
		offending = append(offending, change.File)
	}
	if len(offending) == 0 {
		return nil
	}
	sort.Strings(offending)
	return &refactorTypes.RefactorError{
		Type: refactorTypes.InvalidOperation,
		Message: fmt.Sprintf(
			"refusing to edit generated files: %s carry a \"Code generated ... DO NOT EDIT\" header, "+
				"and gorefactor does not edit generated files; change the generator's input and regenerate instead",
			strings.Join(offending, ", ")),
		File: offending[0],
	}
}
