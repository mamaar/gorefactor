package refactor

import (
	"fmt"
	"go/token"
	gotypes "go/types"
	"path/filepath"

	"github.com/mamaar/gorefactor/pkg/types"
)

// checkShadowedAtReferences refuses a rename when, at a same-package reference,
// newName resolves to a declaration below package scope that the rename would
// capture the reference with.
func checkShadowedAtReferences(ws *types.Workspace, symbol *types.Symbol, refs []*types.Reference, newName string) error {
	pkg := findPackageForFile(ws, symbol.File)
	if pkg == nil || pkg.TypesPkg == nil {
		return nil
	}

	for _, ref := range refs {
		if findPackageForFile(ws, ref.File) != pkg {
			continue
		}

		scope := pkg.TypesPkg.Scope().Innermost(ref.Position)
		if scope == nil {
			continue
		}

		_, obj := scope.LookupParent(newName, ref.Position)
		if obj == nil || obj.Parent() == pkg.TypesPkg.Scope() || obj.Parent() == gotypes.Universe {
			continue
		}

		kind := "a local declaration"

		_, ok := obj.(*gotypes.PkgName)
		if ok {
			kind = "an import"
		}

		return &types.RefactorError{
			Type: types.NameConflict,
			Message: fmt.Sprintf("cannot rename %s to %s: at %s:%d the name %s refers to %s (%s)",
				symbol.Name, newName, relToRoot(ws, ref.File), ref.Line, newName, kind, relPos(ws, obj.Pos())),
			File: ref.File,
		}
	}

	return nil
}

// relPos formats pos as root-relative file:line.
func relPos(ws *types.Workspace, pos token.Pos) string {
	p := ws.FileSet.Position(pos)
	return fmt.Sprintf("%s:%d", relToRoot(ws, p.Filename), p.Line)
}

func relToRoot(ws *types.Workspace, path string) string {
	rel, err := filepath.Rel(ws.RootPath, path)
	if err == nil {
		return filepath.ToSlash(rel)
	}

	return path
}
