package refactor

import (
	"fmt"
	"go/ast"
	gotypes "go/types"
	"sort"
	"strings"
	"unicode"

	"github.com/mamaar/gorefactor/pkg/analysis"
	"github.com/mamaar/gorefactor/pkg/types"
)

// This file implements the "companion declarations" logic for MoveSymbolOperation.
//
// Background: move_symbol already moves a type's method set along with the
// type itself (see methodEntries in operations.go) — the tool's documented
// contract is that moving a type moves its full method set, not just the bare declaration. But a type's
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
// declarations in the package's production files that construct typeName via
// a composite literal requiring same-package access. Outside the type's own
// file, only functions returning typeName or *typeName qualify. These are idiomatic
// constructors that must move together with the type. The type's own file
// comes first in declaration order, then the other files by path.
func findCompanionFuncDecls(pkg *types.Package, typeFile *types.File, typeName string, unexportedFields map[string]bool) []*ast.FuncDecl {
	if len(unexportedFields) == 0 {
		return nil
	}

	files := []*types.File{typeFile}
	var others []*types.File

	for _, f := range pkg.Files {
		if f != typeFile && f.AST != nil {
			others = append(others, f)
		}
	}

	sort.Slice(others, func(i, j int) bool { return others[i].Path < others[j].Path })
	files = append(files, others...)

	var companions []*ast.FuncDecl

	for _, file := range files {
		for _, decl := range file.AST.Decls {
			fn, ok := decl.(*ast.FuncDecl)
			if !ok || fn.Recv != nil || fn.Body == nil {
				continue // only plain functions; methods already move with the type
			}

			if (file == typeFile || returnsType(fn, typeName)) && containsSamePackageCompositeLit(fn.Body, typeName, unexportedFields) {
				companions = append(companions, fn)
			}
		}
	}

	return companions
}

// returnsType reports whether fn has a result of type typeName or *typeName.
func returnsType(fn *ast.FuncDecl, typeName string) bool {
	if fn.Type.Results == nil {
		return false
	}

	for _, field := range fn.Type.Results.List {
		expr := field.Type

		star, ok := expr.(*ast.StarExpr)
		if ok {
			expr = star.X
		}

		ident, ok := expr.(*ast.Ident)
		if ok && ident.Name == typeName {
			return true
		}
	}

	return false
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

// findStrandedCompositeLiterals scans every file in pkg, tests included, for
// composite literals of typeName that require same-package access (see
// compositeLitRequiresSamePackage) and are not inside a declaration that
// moves. It returns "file:line" locations describing each one.
func findStrandedCompositeLiterals(ws *types.Workspace, pkg *types.Package, typeName string, unexportedFields map[string]bool, moving []*movingSymbol) []string {
	if len(unexportedFields) == 0 {
		return nil
	}

	var stranded []string

	for _, files := range []map[string]*types.File{pkg.Files, pkg.TestFiles} {
		for _, file := range files {
			if file.AST == nil {
				continue
			}

			findCompositeLitsInto(file.AST, typeName, unexportedFields, file.Path, func(lit *ast.CompositeLit, filePath string) {
				pos := ws.FileSet.Position(lit.Pos())
				if movingAt(moving, filePath, pos.Offset) != nil {
					return
				}

				stranded = append(stranded, fmt.Sprintf("%s:%d", filePath, pos.Line))
			})
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

	companionDecls := findCompanionFuncDecls(sourcePackage, sourceFile, symbol.Name, unexportedFields)
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

// checkForStrandedReferences returns an error blaming one moving symbol when
// the move would leave code that cannot compile: moved code that uses an
// unexported member (function, type, variable, constant, field or method) of
// its source package that stays behind, or code left behind that uses an
// unexported member that moves. Positional literals of a moved struct type
// carry no identifier to find, so they are looked for separately.
func (op *MoveSymbolOperation) checkForStrandedReferences(ws *types.Workspace, moving []*movingSymbol) error {
	for _, m := range moving {
		file := m.file
		if m.sym.Kind != types.TypeSymbol || file == nil || file.AST == nil || op.staysInPackage(ws, m.pkg) {
			continue
		}

		unexportedFields := unexportedStructFields(file.AST, m.sym.Name)

		stranded := findStrandedCompositeLiterals(ws, m.pkg, m.sym.Name, unexportedFields, moving)
		if len(stranded) == 0 {
			continue
		}

		return &groupMemberError{index: m.owner, err: &types.RefactorError{
			Type: types.VisibilityViolation,
			Message: fmt.Sprintf(
				"moving %s would strand code that constructs it via unexported fields (%s) and cannot be safely auto-moved: %s\n"+
					"Refactor these to only set exported fields (or move them manually alongside %s first), then retry.",
				m.sym.Name, joinFieldNames(unexportedFields), joinLocations(stranded), m.sym.Name,
			),
		}}
	}

	checked := make(map[*types.Package]bool)
	for _, m := range moving {
		if checked[m.pkg] || m.pkg.TypesInfo == nil || m.pkg.TypesPkg == nil || op.staysInPackage(ws, m.pkg) {
			continue
		}

		checked[m.pkg] = true
		var found []strandedUse

		for ident, obj := range m.pkg.TypesInfo.Uses {
			if !unexportedMember(obj, m.pkg.TypesPkg) {
				continue
			}

			use := ws.FileSet.Position(ident.Pos())
			def := ws.FileSet.Position(obj.Pos())

			user, owner := movingAt(moving, use.Filename, use.Offset), movingAt(moving, def.Filename, def.Offset)
			if (user == nil) != (owner == nil) {
				found = append(found, strandedUse{user: user, owner: owner, name: obj.Name(), file: use.Filename, line: use.Line, offset: use.Offset})
			}
		}

		if len(found) > 0 {
			return found[firstStrandedUse(found)].error(m.pkg.Path)
		}

		err := checkSplitInterfaceImplementers(ws, m.pkg, moving)
		if err != nil {
			return fmt.Errorf("check for stranded references: %w", err)
		}
	}

	return nil
}

// checkSplitInterfaceImplementers refuses a move that separates a named
// interface with an unexported method from a type that implements it: the
// method can satisfy the interface only from the package that declares it.
// Pairs are taken in name order.
func checkSplitInterfaceImplementers(ws *types.Workspace, pkg *types.Package, moving []*movingSymbol) error {
	scope := pkg.TypesPkg.Scope()
	moverOf := func(obj gotypes.Object) *movingSymbol {
		pos := ws.FileSet.Position(obj.Pos())
		return movingAt(moving, pos.Filename, pos.Offset)
	}

	for _, ifaceName := range scope.Names() {
		ifaceObj, ok := scope.Lookup(ifaceName).(*gotypes.TypeName)
		if !ok || ifaceObj.IsAlias() {
			continue
		}

		iface, ok := ifaceObj.Type().Underlying().(*gotypes.Interface)

		named, isNamed := ifaceObj.Type().(*gotypes.Named)
		if !ok || !isNamed || named.TypeParams().Len() > 0 {
			continue
		}

		method := firstUnexportedMethod(iface, pkg.TypesPkg)
		if method == nil {
			continue
		}

		for _, typeName := range scope.Names() {
			typeObj, ok := scope.Lookup(typeName).(*gotypes.TypeName)
			if !ok || typeObj == ifaceObj || typeObj.IsAlias() {
				continue
			}

			named, ok := typeObj.Type().(*gotypes.Named)
			if !ok || named.TypeParams().Len() > 0 || gotypes.IsInterface(named) || !typeImplements(named, iface) {
				continue
			}

			typeMover, ifaceMover := moverOf(typeObj), moverOf(ifaceObj)
			if (typeMover == nil) == (ifaceMover == nil) {
				continue
			}

			message := fmt.Sprintf("cannot move %s: it implements %s through the unexported method %s, and %s stays in package %s",
				typeName, ifaceName, method.Name(), ifaceName, pkg.Path)

			blamed := typeMover
			if typeMover == nil {
				blamed = ifaceMover
				message = fmt.Sprintf("cannot move %s: %s implements it through the unexported method %s, and %s stays in package %s",
					ifaceName, typeName, method.Name(), typeName, pkg.Path)
			}

			return &groupMemberError{index: blamed.owner, err: &types.RefactorError{Type: types.VisibilityViolation, Message: message}}
		}
	}

	return nil
}

// firstUnexportedMethod returns the first method of iface, by name, that is
// unexported and declared in pkg; nil when there is none.
func firstUnexportedMethod(iface *gotypes.Interface, pkg *gotypes.Package) *gotypes.Func {
	var first *gotypes.Func
	for m := range iface.Methods() {
		if !m.Exported() && m.Pkg() == pkg && (first == nil || m.Name() < first.Name()) {
			first = m
		}
	}

	return first
}

// strandedUse is a use of an unexported member that crosses the boundary of
// the moved declarations: exactly one of user and owner moves.
type strandedUse struct {
	user, owner *movingSymbol
	name        string
	file        string
	line        int
	offset      int
}

// firstStrandedUse is the index of the use first in file and position order.
func firstStrandedUse(uses []strandedUse) int {
	first := 0
	for i, u := range uses {
		if u.file < uses[first].file || u.file == uses[first].file && u.offset < uses[first].offset {
			first = i
		}
	}

	return first
}

func (u strandedUse) error(sourcePackage string) error {
	if u.user != nil {
		return &groupMemberError{index: u.user.owner, err: &types.RefactorError{
			Type: types.VisibilityViolation,
			Message: fmt.Sprintf("%s cannot move: it uses %s, which is unexported and stays in package %s; move %s along or export it first",
				u.user.name(), u.name, sourcePackage, u.name),
			File: u.file,
			Line: u.line,
		}}
	}

	return &groupMemberError{index: u.owner.owner, err: &types.RefactorError{
		Type: types.VisibilityViolation,
		Message: fmt.Sprintf("%s cannot move: unexported %s moves with it but is used by code that stays in package %s; move that code along or export %s first",
			u.owner.name(), u.name, sourcePackage, u.name),
		File: u.file,
		Line: u.line,
	}}
}

// unexportedMember reports whether obj is an unexported package-level object,
// field or method of pkg.
func unexportedMember(obj gotypes.Object, pkg *gotypes.Package) bool {
	if obj == nil || obj.Pkg() != pkg || obj.Exported() {
		return false
	}

	switch o := obj.(type) {
	case *gotypes.Var:
		return o.IsField() || o.Parent() == pkg.Scope()
	case *gotypes.Func:
		return o.Parent() == pkg.Scope() || o.Type().(*gotypes.Signature).Recv() != nil
	case *gotypes.TypeName, *gotypes.Const:
		return o.Parent() == pkg.Scope()
	}

	return false
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
	var out strings.Builder

	for i, item := range items {
		if i > 0 {
			out.WriteString(", ")
		}

		out.WriteString(item)
	}

	return out.String()
}
