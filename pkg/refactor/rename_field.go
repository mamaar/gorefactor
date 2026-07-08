package refactor

import (
	"fmt"
	"go/ast"
	gotypes "go/types"
	"io"
	"log/slog"

	"github.com/mamaar/gorefactor/pkg/analysis"
	"github.com/mamaar/gorefactor/pkg/types"
)

// RenameFieldOperation renames a struct field on a named type and updates every
// reference to it. Reference discovery is done exclusively through
// SymbolResolver.FindFieldReferences, which resolves the field's go/types object
// and matches by object identity. It never falls back to name matching (which
// would rewrite unrelated identically-named identifiers across the workspace),
// and propagates FindFieldReferences' fail-closed error unchanged.
type RenameFieldOperation struct {
	Request     types.RenameFieldRequest
	CachedIndex *analysis.ReferenceIndex // optional pre-built index

	// AllowBreaking permits an export-status flip (exported <-> unexported) even
	// when the field has references from a different package. Populated by the
	// engine from EngineConfig.AllowBreaking; the operation cannot reach
	// EngineConfig through the Operation interface, so the engine sets it.
	AllowBreaking bool
}

func (op *RenameFieldOperation) Type() types.OperationType {
	return types.RenameFieldOperationType
}

func (op *RenameFieldOperation) Description() string {
	return fmt.Sprintf("Rename field %s.%s to %s",
		op.Request.TypeName, op.Request.FieldName, op.Request.NewFieldName)
}

func (op *RenameFieldOperation) logger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

// initResolver builds (or reuses) the reference index. The batch path constructs
// the op with a nil CachedIndex, so the fallback branch must exist.
func (op *RenameFieldOperation) initResolver(ws *types.Workspace) (*analysis.SymbolResolver, *analysis.ReferenceIndex) {
	resolver := analysis.NewSymbolResolver(ws, op.logger())
	idx := op.CachedIndex
	if idx == nil {
		idx = resolver.BuildReferenceIndex()
	}
	return resolver, idx
}

// findOwningPackage locates the package that declares Request.TypeName. If
// PackagePath is set it is used directly; otherwise every package is scanned and
// an ambiguity error is returned when more than one declares the type name.
func (op *RenameFieldOperation) findOwningPackage(ws *types.Workspace) (*types.Package, error) {
	if op.Request.PackagePath != "" {
		pkg, exists := ws.Packages[op.Request.PackagePath]
		if !exists {
			return nil, &types.RefactorError{
				Type:    types.SymbolNotFound,
				Message: fmt.Sprintf("package %s not found", op.Request.PackagePath),
			}
		}
		if pkg.Symbols == nil || pkg.Symbols.Types[op.Request.TypeName] == nil {
			return nil, &types.RefactorError{
				Type:    types.SymbolNotFound,
				Message: fmt.Sprintf("type %s not found in package %s", op.Request.TypeName, op.Request.PackagePath),
			}
		}
		return pkg, nil
	}

	var candidates []*types.Package
	var candidatePaths []string
	for _, pkg := range ws.Packages {
		if pkg.Symbols != nil && pkg.Symbols.Types[op.Request.TypeName] != nil {
			candidates = append(candidates, pkg)
			candidatePaths = append(candidatePaths, pkg.Path)
		}
	}

	switch len(candidates) {
	case 0:
		return nil, &types.RefactorError{
			Type:    types.SymbolNotFound,
			Message: fmt.Sprintf("type %s not found in workspace", op.Request.TypeName),
		}
	case 1:
		return candidates[0], nil
	default:
		return nil, &types.RefactorError{
			Type: types.InvalidOperation,
			Message: fmt.Sprintf(
				"ambiguous type %q: declared in %d packages: %v; specify PackagePath to disambiguate",
				op.Request.TypeName, len(candidates), candidatePaths),
		}
	}
}

// findStructTypeSpec locates the retained TypeSpec for TypeName in pkg and, when
// the type is a struct, returns its *ast.StructType. The typeSpec is returned
// even for non-struct types so callers can distinguish "not a struct" (structType
// == nil) from "not found" (typeSpec == nil).
func findStructTypeSpec(pkg *types.Package, typeName string, typeSymbol *types.Symbol) (*ast.TypeSpec, *ast.StructType) {
	files := make([]*types.File, 0, len(pkg.Files)+len(pkg.TestFiles))
	for _, f := range pkg.Files {
		files = append(files, f)
	}
	for _, f := range pkg.TestFiles {
		files = append(files, f)
	}

	for _, file := range files {
		if file.AST == nil {
			continue
		}
		for _, decl := range file.AST.Decls {
			genDecl, ok := decl.(*ast.GenDecl)
			if !ok {
				continue
			}
			for _, spec := range genDecl.Specs {
				ts, ok := spec.(*ast.TypeSpec)
				if !ok || ts.Name.Name != typeName {
					continue
				}
				// Prefer the exact retained TypeSpec (matched by position) so a
				// _test.go redeclaration doesn't shadow the kept one.
				if typeSymbol != nil && ts.Name.Pos() != typeSymbol.Position {
					continue
				}
				structType, _ := ts.Type.(*ast.StructType)
				return ts, structType
			}
		}
	}
	return nil, nil
}

// embeddedFieldTypeName returns the type name of an embedded (anonymous) struct
// field expression, or "" if it cannot be determined.
func embeddedFieldTypeName(expr ast.Expr) string {
	switch t := expr.(type) {
	case *ast.Ident:
		return t.Name
	case *ast.StarExpr:
		return embeddedFieldTypeName(t.X)
	case *ast.SelectorExpr:
		return t.Sel.Name
	}
	return ""
}

func (op *RenameFieldOperation) Validate(ws *types.Workspace) error {
	req := op.Request

	// New name must be a valid identifier and actually different.
	if !isValidGoIdentifier(req.NewFieldName) {
		return &types.RefactorError{
			Type:    types.InvalidOperation,
			Message: fmt.Sprintf("invalid Go identifier: %s", req.NewFieldName),
		}
	}
	if req.FieldName == req.NewFieldName {
		return &types.RefactorError{
			Type:    types.InvalidOperation,
			Message: "new field name cannot be the same as the current name",
		}
	}

	pkg, err := op.findOwningPackage(ws)
	if err != nil {
		return err
	}
	typeSymbol := pkg.Symbols.Types[req.TypeName]

	// The type must be a struct.
	typeSpec, structType := findStructTypeSpec(pkg, req.TypeName, typeSymbol)
	if typeSpec == nil {
		return &types.RefactorError{
			Type:    types.SymbolNotFound,
			Message: fmt.Sprintf("could not locate declaration of type %s", req.TypeName),
		}
	}
	if structType == nil {
		return &types.RefactorError{
			Type: types.InvalidOperation,
			Message: fmt.Sprintf(
				"type %s is not a struct; rename_field only operates on struct fields", req.TypeName),
		}
	}

	// The field must exist on the struct.
	var fieldSym *types.Symbol
	for _, f := range pkg.Symbols.Fields[req.TypeName] {
		if f.Name == req.FieldName {
			fieldSym = f
			break
		}
	}
	if fieldSym == nil {
		// Distinguish an embedded field (a type rename in disguise) from a plain
		// unknown field. Embedded fields are deliberately absent from
		// Symbols.Fields, so re-read the struct's field list directly.
		if structType.Fields != nil {
			for _, field := range structType.Fields.List {
				if len(field.Names) != 0 {
					continue
				}
				if embeddedFieldTypeName(field.Type) == req.FieldName {
					return &types.RefactorError{
						Type: types.InvalidOperation,
						Message: fmt.Sprintf(
							"%q is an embedded field on %s: renaming it is a type rename, not a field rename; use rename_symbol on the type %q instead",
							req.FieldName, req.TypeName, req.FieldName),
					}
				}
			}
		}
		return &types.RefactorError{
			Type:    types.SymbolNotFound,
			Message: fmt.Sprintf("field %s not found on type %s", req.FieldName, req.TypeName),
		}
	}

	// No existing field on the same struct may already carry the new name.
	for _, f := range pkg.Symbols.Fields[req.TypeName] {
		if f.Name == req.NewFieldName {
			return &types.RefactorError{
				Type:    types.NameConflict,
				Message: fmt.Sprintf("field %s already exists on type %s", req.NewFieldName, req.TypeName),
			}
		}
	}

	// Go forbids a field and a method sharing a name on the same type.
	for _, m := range pkg.Symbols.Methods[req.TypeName] {
		if m.Name == req.NewFieldName {
			return &types.RefactorError{
				Type:    types.NameConflict,
				Message: fmt.Sprintf("type %s already has a method named %s", req.TypeName, req.NewFieldName),
			}
		}
	}

	// Promoted field/method collision from an embedded type. Use go/types rather
	// than walking the AST: LookupFieldOrMethod resolves promotion for us.
	if pkg.TypesPkg != nil {
		if obj := pkg.TypesPkg.Scope().Lookup(req.TypeName); obj != nil {
			if named, ok := obj.Type().(*gotypes.Named); ok {
				found, _, _ := gotypes.LookupFieldOrMethod(named, true, pkg.TypesPkg, req.NewFieldName)
				if found != nil {
					return &types.RefactorError{
						Type: types.NameConflict,
						Message: fmt.Sprintf(
							"renaming to %s collides with a promoted field or method on type %s",
							req.NewFieldName, req.TypeName),
					}
				}
			}
		}
	}

	return nil
}

func (op *RenameFieldOperation) Execute(ws *types.Workspace) (*types.RefactoringPlan, error) {
	req := op.Request

	pkg, err := op.findOwningPackage(ws)
	if err != nil {
		return nil, err
	}

	// Locate the field symbol.
	var fieldSym *types.Symbol
	for _, f := range pkg.Symbols.Fields[req.TypeName] {
		if f.Name == req.FieldName {
			fieldSym = f
			break
		}
	}
	if fieldSym == nil {
		return nil, &types.RefactorError{
			Type:    types.SymbolNotFound,
			Message: fmt.Sprintf("field %s not found on type %s", req.FieldName, req.TypeName),
		}
	}

	resolver, idx := op.initResolver(ws)

	// Object-identity reference discovery. Fail closed on any error — never
	// degrade to a name match, which would corrupt unrelated identifiers.
	refs, err := resolver.FindFieldReferences(fieldSym, idx)
	if err != nil {
		return nil, err
	}

	// Export-status flip guard. Flipping exported<->unexported while the field is
	// referenced from another package breaks compilation. The engine populates
	// AllowBreaking from EngineConfig.
	if isExportedIdent(req.FieldName) != isExportedIdent(req.NewFieldName) && !op.AllowBreaking {
		ownFiles := make(map[string]bool)
		for _, f := range pkg.Files {
			ownFiles[f.Path] = true
		}
		for _, f := range pkg.TestFiles {
			ownFiles[f.Path] = true
		}
		for _, ref := range refs {
			if !ownFiles[ref.File] {
				return nil, &types.RefactorError{
					Type: types.VisibilityViolation,
					Message: fmt.Sprintf(
						"changing export status of field %s.%s to %s would break references in package other than %s; re-run with AllowBreaking to proceed",
						req.TypeName, req.FieldName, req.NewFieldName, pkg.Path),
					File: ref.File,
					Line: ref.Line,
				}
			}
		}
	}

	var changes []types.Change
	var affectedFiles []string
	addFile := func(path string) {
		if !contains(affectedFiles, path) {
			affectedFiles = append(affectedFiles, path)
		}
	}

	// Definition change (shared helper; no duplicated offset logic).
	defChange := definitionRenameChange(fieldSym, req.NewFieldName)
	changes = append(changes, defChange)
	addFile(defChange.File)

	// Reference changes built directly from the already-resolved byte offset.
	for _, ref := range refs {
		changes = append(changes, types.Change{
			File:        ref.File,
			Start:       ref.Offset,
			End:         ref.Offset + len(req.FieldName),
			OldText:     req.FieldName,
			NewText:     req.NewFieldName,
			Description: fmt.Sprintf("Rename reference to field %s.%s", req.TypeName, req.FieldName),
		})
		addFile(ref.File)
	}

	return &types.RefactoringPlan{
		Operations:    []types.Operation{op},
		Changes:       changes,
		AffectedFiles: affectedFiles,
		Reversible:    true,
	}, nil
}
