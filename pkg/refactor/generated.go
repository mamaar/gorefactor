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

// generatedFile returns the file at path when the workspace loaded it as generated.
func generatedFile(ws *refactorTypes.Workspace, path string) *refactorTypes.File {
	for _, pkg := range ws.Packages {
		for _, files := range []map[string]*refactorTypes.File{pkg.Files, pkg.TestFiles} {
			for _, f := range files {
				if f.Path == path && f.Generated {
					return f
				}
			}
		}
	}

	return nil
}

// rootRelative returns path relative to the workspace root, slash-separated.
func rootRelative(ws *refactorTypes.Workspace, path string) string {
	rel, err := filepath.Rel(ws.RootPath, path)
	if err == nil {
		path = rel
	}

	return filepath.ToSlash(path)
}

// checkGeneratedDeclarations refuses a move of a symbol, companion or method
// declared in a generated file, blaming the requested symbol.
func (op *MoveSymbolOperation) checkGeneratedDeclarations(ws *refactorTypes.Workspace, moving []*movingSymbol) error {
	refs := op.Request.Refs()

	for _, m := range moving {
		if m.file == nil || !m.file.Generated {
			continue
		}

		what := "it is declared"

		switch {
		case m.typ != nil:
			what = fmt.Sprintf("its method %s is declared", m.sym.Name)
		case m.sym.Name != refs[m.owner].Name:
			what = fmt.Sprintf("its companion %s is declared", m.sym.Name)
		}

		return op.blame(len(refs), m.owner, &refactorTypes.RefactorError{
			Type: refactorTypes.InvalidOperation,
			Message: fmt.Sprintf("cannot move %s: %s in the generated file %s; change the generator's input and regenerate",
				m.name(), what, rootRelative(ws, m.file.Path)),
			File: m.file.Path,
		})
	}

	return nil
}

// checkGeneratedEdits refuses a plan that edits a generated file, blaming the
// moving symbol whose reference sits in it, or naming the file when none does.
func (op *MoveSymbolOperation) checkGeneratedEdits(ws *refactorTypes.Workspace, moving []*movingSymbol, references []*refactorTypes.Reference, changes []refactorTypes.Change) error {
	n := len(op.Request.Refs())

	for _, c := range changes {
		if generatedFile(ws, c.File) == nil {
			continue
		}

		for _, ref := range references {
			if ref.File != c.File {
				continue
			}

			for _, m := range moving {
				if m.typ != nil || m.sym != ref.Symbol {
					continue
				}

				return op.blame(n, m.owner, &refactorTypes.RefactorError{
					Type: refactorTypes.InvalidOperation,
					Message: fmt.Sprintf("cannot move %s: it is referenced at %s:%d, in a generated file; change the generator's input and regenerate",
						m.name(), rootRelative(ws, ref.File), ref.Line),
					File: ref.File,
					Line: ref.Line,
				})
			}
		}

		return &refactorTypes.RefactorError{
			Type: refactorTypes.InvalidOperation,
			Message: fmt.Sprintf("cannot move %s: the move would edit the generated file %s; change the generator's input and regenerate",
				symbolNames(moving), rootRelative(ws, c.File)),
			File: c.File,
		}
	}

	return nil
}

// blame attributes err to the requested symbol at index when the move names several.
func (op *MoveSymbolOperation) blame(requested, index int, err error) error {
	if requested > 1 {
		return &groupMemberError{index: index, err: err}
	}

	return err
}
