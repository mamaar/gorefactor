package refactor

import (
	"fmt"
	"go/ast"
	"sort"
	"unicode"

	"github.com/mamaar/gorefactor/pkg/analysis"
	"github.com/mamaar/gorefactor/pkg/types"
)

// This file implements the "companion declarations" logic for MoveSymbolOperation.
//
// Background: move_symbol already moves a type's method set along with the
// type itself (see generateMethodRemovalChanges / extractMethodsForType in
// operations.go) — the tool's documented contract is that moving a type moves
// its full method set, not just the bare declaration. But a type's
// constructor (idiomatically `func NewFoo(...) *Foo { return &Foo{...} }`)
// is a plain package-level function, not a method, so it was never
// considered part of that bundle. If the constructor builds the struct via
// unexported fields — which almost all idiomatic constructors do — leaving
// it behind produces code that can never compile: an unexported-field
// composite literal is only legal in the same package as the struct
// definition.
//
// The functions below (a) detect such constructors so they can move along
// with the type, and (b) detect any other, non-constructor code that would
// be similarly stranded and can't be safely auto-moved — in which case the
// whole move is refused with a clear error rather than silently emitting
// code that doesn't compile.

// unexportedStructFields returns the set of unexported field names declared
// directly on the named struct type in file (embedded fields included, keyed
// by their implicit field name). Returns nil if typeName does not name a
// struct type in file, or the struct has no unexported fields.
func unexportedStructFields(file *ast.File, typeName string) map[string]bool {
	var result map[string]bool

	ast.Inspect(file, func(n ast.Node) bool {
		typeSpec, ok := n.(*ast.TypeSpec)
		if !ok || typeSpec.Name.Name != typeName {
			return true
		}
		structType, ok := typeSpec.Type.(*ast.StructType)
		if !ok || structType.Fields == nil {
			return false
		}

		addUnexported := func(name string) {
			if name == "" || isExportedIdent(name) {
				return
			}
			if result == nil {
				result = make(map[string]bool)
			}
			result[name] = true
		}

		for _, field := range structType.Fields.List {
			if len(field.Names) == 0 {
				// Embedded field: its implicit name is the type's own identifier.
				addUnexported(embeddedFieldName(field.Type))
				continue
			}
			for _, name := range field.Names {
				addUnexported(name.Name)
			}
		}
		return false
	})

	return result
}

func embeddedFieldName(expr ast.Expr) string {
	switch t := expr.(type) {
	case *ast.Ident:
		return t.Name
	case *ast.StarExpr:
		return embeddedFieldName(t.X)
	case *ast.SelectorExpr:
		return t.Sel.Name
	default:
		return ""
	}
}

func isExportedIdent(name string) bool {
	return len(name) > 0 && unicode.IsUpper(rune(name[0]))
}

// compositeLitNamesType reports whether lit's literal type is a bare
// identifier matching typeName (i.e. `TypeName{...}`, not `pkg.TypeName{...}`
// — a selector-qualified literal is by construction from another package and
// so can never reference unexported fields in the first place).
func compositeLitNamesType(lit *ast.CompositeLit, typeName string) bool {
	ident, ok := lit.Type.(*ast.Ident)
	return ok && ident.Name == typeName
}

// compositeLitRequiresSamePackage reports whether lit — a composite literal
// of the moved struct type — can only legally appear in the same package as
// the type's definition.
//
// Per the Go spec, an unkeyed (positional) struct literal must specify a
// value for every field in order, including unexported ones, so ANY unkeyed
// literal of a struct with at least one unexported field requires
// same-package access. A keyed literal only requires same-package access if
// it explicitly names an unexported field.
func compositeLitRequiresSamePackage(lit *ast.CompositeLit, unexportedFields map[string]bool) bool {
	if len(unexportedFields) == 0 || len(lit.Elts) == 0 {
		return false
	}

	allKeyed := true
	for _, elt := range lit.Elts {
		kv, ok := elt.(*ast.KeyValueExpr)
		if !ok {
			allKeyed = false
			continue
		}
		if keyIdent, ok := kv.Key.(*ast.Ident); ok && unexportedFields[keyIdent.Name] {
			return true
		}
	}
	return !allKeyed
}

// findCompanionFuncDecls returns the top-level (non-method) function
// declarations in file that construct typeName via a composite literal
// requiring same-package access. These are idiomatic constructors that must
// move together with the type.
func findCompanionFuncDecls(file *ast.File, typeName string, unexportedFields map[string]bool) []*ast.FuncDecl {
	if len(unexportedFields) == 0 {
		return nil
	}

	var companions []*ast.FuncDecl
	for _, decl := range file.Decls {
		fn, ok := decl.(*ast.FuncDecl)
		if !ok || fn.Recv != nil || fn.Body == nil {
			continue // only plain functions; methods already move with the type
		}
		if containsSamePackageCompositeLit(fn.Body, typeName, unexportedFields) {
			companions = append(companions, fn)
		}
	}
	return companions
}

func containsSamePackageCompositeLit(node ast.Node, typeName string, unexportedFields map[string]bool) bool {
	found := false
	ast.Inspect(node, func(n ast.Node) bool {
		if found {
			return false
		}
		lit, ok := n.(*ast.CompositeLit)
		if !ok {
			return true
		}
		if compositeLitNamesType(lit, typeName) && compositeLitRequiresSamePackage(lit, unexportedFields) {
			found = true
			return false
		}
		return true
	})
	return found
}

// findStrandedCompositeLiterals scans every file in pkg for composite
// literals of typeName that require same-package access (see
// compositeLitRequiresSamePackage) and are not inside one of the
// already-selected companion declarations, and are not part of the moved
// type's own method set. It returns "file:line" locations describing each
// one, for use in an error message when a move must be refused outright
// rather than risk emitting code that cannot compile.
func findStrandedCompositeLiterals(ws *types.Workspace, pkg *types.Package, typeName string, unexportedFields map[string]bool, companions map[*ast.FuncDecl]bool) []string {
	if len(unexportedFields) == 0 {
		return nil
	}

	var stranded []string
	report := func(lit *ast.CompositeLit, filePath string) {
		loc := filePath
		if ws.FileSet != nil {
			pos := ws.FileSet.Position(lit.Pos())
			loc = fmt.Sprintf("%s:%d", filePath, pos.Line)
		}
		stranded = append(stranded, loc)
	}

	for _, file := range pkg.Files {
		if file.AST == nil {
			continue
		}
		for _, decl := range file.AST.Decls {
			switch d := decl.(type) {
			case *ast.FuncDecl:
				if d.Body == nil || companions[d] {
					continue
				}
				if d.Recv != nil && receiverTypeName(d) == typeName {
					continue // one of the type's own methods; moves with the type
				}
				findCompositeLitsInto(d.Body, typeName, unexportedFields, file.Path, report)
			default:
				findCompositeLitsInto(decl, typeName, unexportedFields, file.Path, report)
			}
		}
	}
	sort.Strings(stranded)
	return stranded
}

func findCompositeLitsInto(node ast.Node, typeName string, unexportedFields map[string]bool, filePath string, report func(*ast.CompositeLit, string)) {
	ast.Inspect(node, func(n ast.Node) bool {
		lit, ok := n.(*ast.CompositeLit)
		if !ok {
			return true
		}
		if compositeLitNamesType(lit, typeName) && compositeLitRequiresSamePackage(lit, unexportedFields) {
			report(lit, filePath)
		}
		return true
	})
}

// resolveCompanionSymbols returns the *types.Symbol entries (already present
// in sourcePackage's symbol table) for every companion function that must
// move together with symbol. It returns (nil, nil) when symbol isn't a
// struct type, or the struct has no unexported fields (nothing can be
// stranded in that case, since any code anywhere can already construct it).
func (op *MoveSymbolOperation) resolveCompanionSymbols(resolver *analysis.SymbolResolver, sourcePackage *types.Package, sourceFile *types.File, symbol *types.Symbol) ([]*types.Symbol, error) {
	if symbol.Kind != types.TypeSymbol || sourceFile == nil || sourceFile.AST == nil {
		return nil, nil
	}

	unexportedFields := unexportedStructFields(sourceFile.AST, symbol.Name)
	if len(unexportedFields) == 0 {
		return nil, nil
	}

	companionDecls := findCompanionFuncDecls(sourceFile.AST, symbol.Name, unexportedFields)
	if len(companionDecls) == 0 {
		return nil, nil
	}

	companions := make([]*types.Symbol, 0, len(companionDecls))
	for _, decl := range companionDecls {
		companionSymbol, err := resolver.ResolveSymbol(sourcePackage, decl.Name.Name)
		if err != nil {
			return nil, fmt.Errorf("resolving companion function %s for moved type %s: %w", decl.Name.Name, symbol.Name, err)
		}
		companions = append(companions, companionSymbol)
	}
	return companions, nil
}

// checkForStrandedDependents returns a RefactorError if moving symbol (a
// struct type) would strand code elsewhere in sourcePackage that reaches
// into its unexported fields and isn't covered by the companion functions
// resolveCompanionSymbols already picked up. Returns nil if the move is
// safe (either nothing depends on unexported fields, or everything that
// does is moving along with the type).
func (op *MoveSymbolOperation) checkForStrandedDependents(ws *types.Workspace, sourcePackage *types.Package, sourceFile *types.File, symbol *types.Symbol, companions []*types.Symbol) error {
	if symbol.Kind != types.TypeSymbol || sourceFile == nil || sourceFile.AST == nil {
		return nil
	}

	unexportedFields := unexportedStructFields(sourceFile.AST, symbol.Name)
	if len(unexportedFields) == 0 {
		return nil
	}

	companionDecls := findCompanionFuncDecls(sourceFile.AST, symbol.Name, unexportedFields)
	moving := make(map[*ast.FuncDecl]bool, len(companionDecls))
	for _, d := range companionDecls {
		moving[d] = true
	}

	stranded := findStrandedCompositeLiterals(ws, sourcePackage, symbol.Name, unexportedFields, moving)
	if len(stranded) == 0 {
		return nil
	}

	return &types.RefactorError{
		Type: types.VisibilityViolation,
		Message: fmt.Sprintf(
			"moving %s would strand code that constructs it via unexported fields (%s) and cannot be safely auto-moved: %s\n"+
				"Refactor these to only set exported fields (or move them manually alongside %s first), then retry.",
			symbol.Name, joinFieldNames(unexportedFields), joinLocations(stranded), symbol.Name,
		),
	}
}

func joinFieldNames(fields map[string]bool) string {
	names := make([]string, 0, len(fields))
	for name := range fields {
		names = append(names, name)
	}
	sort.Strings(names)
	return joinStrings(names)
}

func joinLocations(locs []string) string {
	return joinStrings(locs)
}

func joinStrings(items []string) string {
	out := ""
	for i, item := range items {
		if i > 0 {
			out += ", "
		}
		out += item
	}
	return out
}
