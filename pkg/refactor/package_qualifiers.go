package refactor

import (
	"fmt"
	"go/ast"
	"go/token"
	gotypes "go/types"
	"slices"
	"strconv"
	"strings"

	"github.com/mamaar/gorefactor/pkg/analysis"
	"github.com/mamaar/gorefactor/pkg/types"
)

// pkgRename is a package whose name changes: importers must stop writing
// oldName where they call into oldPath.
type pkgRename struct {
	oldPath, oldName, newName string
}

// qualifierRewrite returns file's content with extra and the edits of
// packageQualifierEdits applied, plus the imports that kept their old name.
func qualifierRewrite(views []*analysis.View, fset *token.FileSet, file *analysis.SourceFile, renames []pkgRename, extra ...textEdit) (string, []types.SkippedSite, error) {
	changes, skipped, err := packageQualifierEdits(views, fset, file, renames)
	if err != nil {
		return "", nil, err
	}
	edits := extra
	for _, c := range changes {
		edits = append(edits, textEdit{start: c.Start, end: c.End, text: c.NewText})
	}
	return copyWithEdits(file.Content, 0, len(file.Content), edits), skipped, nil
}

// packageQualifierEdits renames the uses of each import of a renamed package in file,
// using type information from every build configuration that includes the file.
// An import that cannot be renamed safely keeps its old name as an explicit
// alias and is reported as a skipped site. Aliased and dot imports are left to
// the caller's import-path rewrite. It fails when the file needs edits but does
// not parse.
func packageQualifierEdits(views []*analysis.View, fset *token.FileSet, file *analysis.SourceFile, renames []pkgRename) ([]types.Change, []types.SkippedSite, error) {
	var imported []pkgRename
	for _, r := range renames {
		if strings.Contains(string(file.Content), `"`+r.oldPath+`"`) || strings.Contains(string(file.Content), "`"+r.oldPath+"`") {
			imported = append(imported, r)
		}
	}
	if len(imported) == 0 {
		return nil, nil, nil
	}
	if file.AST == nil || file.Err != nil {
		return nil, nil, fmt.Errorf("cannot rename package qualifiers in %s: file does not parse: %v", file.Path, file.Err)
	}

	var changes []types.Change
	var skipped []types.SkippedSite
	claimed := make(map[string]bool)
	for _, spec := range file.AST.Imports {
		path, err := strconv.Unquote(spec.Path.Value)
		if spec.Name != nil || err != nil {
			continue
		}
		i := slices.IndexFunc(imported, func(r pkgRename) bool { return r.oldPath == path })
		if i < 0 {
			continue
		}
		r := imported[i]

		uses, reason := renamableUses(views, fset, file.Path, r)
		if reason == "" && claimed[r.newName] {
			reason = fmt.Sprintf("name %q is already taken by another renamed package", r.newName)
		}
		if reason != "" {
			at := fset.Position(spec.Path.Pos())
			changes = append(changes, types.Change{
				File:        file.Path,
				Start:       at.Offset,
				End:         at.Offset,
				NewText:     r.oldName + " ",
				Description: fmt.Sprintf("Keep import name '%s' for %s", r.oldName, path),
			})
			skipped = append(skipped, types.SkippedSite{
				File:    file.Path,
				Line:    at.Line,
				Reason:  fmt.Sprintf("kept import name %q: %s", r.oldName, reason),
				Snippet: spec.Path.Value,
			})
			continue
		}
		claimed[r.newName] = true
		for _, id := range uses {
			changes = append(changes, types.Change{
				File:        file.Path,
				Start:       fset.Position(id.Pos()).Offset,
				End:         fset.Position(id.End()).Offset,
				OldText:     id.Name,
				NewText:     r.newName,
				Description: fmt.Sprintf("Update qualified references from %s to %s", r.oldName, r.newName),
			})
		}
	}
	return changes, skipped, nil
}

// renamableUses returns the identifiers that qualify r.oldPath in the file at
// path, or the reason they cannot be renamed to r.newName in some build
// configuration.
func renamableUses(views []*analysis.View, fset *token.FileSet, path string, r pkgRename) ([]*ast.Ident, string) {
	var uses []*ast.Ident
	seen := make(map[token.Pos]bool)
	included := false
	for _, v := range views {
		file := findParsedFile(v.Workspace, path)
		if file == nil {
			continue
		}
		included = true
		pkg := file.Package
		v.Parser.EnsureTypeChecked(v.Workspace, pkg)
		if reason := uncheckedReason(pkg, file); reason != "" {
			return nil, reason
		}
		viewUses, pkgName := importUses(pkg, file, r)
		if pkgName == nil {
			continue
		}
		if taken := nameTakenAt(typesPkgOf(pkg, file), pkgName, r.newName, viewUses); taken != nil {
			return nil, fmt.Sprintf("name %q is already declared at %s", r.newName, fset.Position(taken.Pos()))
		}
		for _, id := range viewUses {
			if !seen[id.Pos()] {
				seen[id.Pos()] = true
				uses = append(uses, id)
			}
		}
	}
	if !included {
		return nil, "file is not part of any type-checked build configuration"
	}
	return uses, ""
}

// importUses returns the identifiers in file that resolve to the unaliased
// import of r.oldPath, and that import's PkgName.
func importUses(pkg *types.Package, file *types.File, r pkgRename) ([]*ast.Ident, *gotypes.PkgName) {
	var uses []*ast.Ident
	var pkgName *gotypes.PkgName
	ast.Inspect(file.AST, func(n ast.Node) bool {
		id, ok := n.(*ast.Ident)
		if !ok || id.Name != r.oldName {
			return true
		}
		if pn, ok := pkg.TypesInfo.Uses[id].(*gotypes.PkgName); ok && pn.Imported().Path() == r.oldPath {
			pkgName = pn
			uses = append(uses, id)
		}
		return true
	})
	return uses, pkgName
}

// typesPkgOf returns the go/types package file was checked as: an external
// test file is checked apart from the package under test.
func typesPkgOf(pkg *types.Package, file *types.File) *gotypes.Package {
	if strings.HasSuffix(file.AST.Name.Name, "_test") && pkg.ExternalTestTypesPkg != nil {
		return pkg.ExternalTestTypesPkg
	}
	return pkg.TypesPkg
}
