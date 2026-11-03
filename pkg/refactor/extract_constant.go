package refactor

import (
	"bytes"
	"fmt"
	"go/ast"
	"go/constant"
	"go/token"
	gotypes "go/types"
	"maps"
	"path"
	"slices"
	"strconv"
	"strings"

	"github.com/mamaar/gorefactor/pkg/analysis"
	pkgtypes "github.com/mamaar/gorefactor/pkg/types"
)

// ExtractConstantOperation declares a literal as a package-level constant and
// replaces every equal literal of the package with it.
type ExtractConstantOperation struct {
	Request pkgtypes.ExtractConstantRequest
	// Sources holds every .go file of the workspace, so a name declared under
	// another build configuration is found.
	Sources *analysis.SourceTree
}

func (op *ExtractConstantOperation) Type() pkgtypes.OperationType {
	return pkgtypes.ExtractOperation
}

func (op *ExtractConstantOperation) Validate(ws *pkgtypes.Workspace) error {
	req := op.Request
	switch {
	case req.SourceFile == "":
		return invalidExtractConstant("source file cannot be empty")
	case req.ConstantName == "":
		return invalidExtractConstant("constant name cannot be empty")
	case req.ConstantName == "_" || !token.IsIdentifier(req.ConstantName):
		return invalidExtractConstant("invalid Go identifier: %s", req.ConstantName)
	case req.ConstantName == "init" || gotypes.Universe.Lookup(req.ConstantName) != nil:
		return invalidExtractConstant("%s is a predeclared name", req.ConstantName)
	case req.Line < 1 || req.Column < 1:
		return invalidExtractConstant("line and column must be 1-based positions")
	}
	pkg, _ := findWorkspaceFile(ws, req.SourceFile)
	if pkg == nil {
		return &pkgtypes.RefactorError{
			Type:    pkgtypes.FileSystemError,
			Message: fmt.Sprintf("source file not found: %s", req.SourceFile),
		}
	}
	return nil
}

func (op *ExtractConstantOperation) Execute(ws *pkgtypes.Workspace) (*pkgtypes.RefactoringPlan, error) {
	name := op.Request.ConstantName
	pkg, src := findWorkspaceFile(ws, op.Request.SourceFile)
	if pkg == nil {
		return nil, &pkgtypes.RefactorError{
			Type:    pkgtypes.FileSystemError,
			Message: fmt.Sprintf("source file not found: %s", op.Request.SourceFile),
		}
	}
	if src.Generated {
		return nil, invalidExtractConstant("%s is a generated file; change the generator's input and regenerate", src.Path)
	}
	if isExternalTest(pkg, src) {
		return nil, invalidExtractConstant("%s belongs to the external test package %s", src.Path, src.AST.Name.Name)
	}
	if pkg.TypesInfo == nil || pkg.TypesPkg == nil {
		return nil, invalidExtractConstant("package %s has no type information", pkg.ImportPath)
	}

	selected, err := op.literalAt(ws, src)
	if err != nil {
		return nil, err
	}
	_, ok := pkg.TypesInfo.Types[selected]
	if !ok {
		return nil, invalidExtractConstant("the literal %s has no type information", selected.Value)
	}
	if err := op.checkPackageLevelName(pkg); err != nil {
		return nil, err
	}
	files := constantScopeFiles(pkg)
	for _, file := range files {
		err := checkImportName(pkg, file, name)
		if err != nil {
			return nil, err
		}
	}
	home, err := op.declaringFile(pkg, src)
	if err != nil {
		return nil, err
	}

	plan := &pkgtypes.RefactoringPlan{Reversible: true}
	for _, file := range files {
		lits := op.equalLiterals(pkg, file, selected)
		if len(lits) == 0 {
			continue
		}
		if file.Generated {
			for _, lit := range lits {
				plan.SkippedSites = append(plan.SkippedSites, skippedLiteral(ws, file, lit, "generated file"))
			}
			continue
		}
		replaced := 0
		for _, lit := range lits {
			if shadowed(pkg, lit, name) {
				plan.SkippedSites = append(plan.SkippedSites, skippedLiteral(ws, file, lit,
					fmt.Sprintf("%s is declared locally here and would shadow the constant", name)))
				continue
			}
			plan.Changes = append(plan.Changes, pkgtypes.Change{
				File:        file.Path,
				Start:       ws.FileSet.Position(lit.Pos()).Offset,
				End:         ws.FileSet.Position(lit.End()).Offset,
				OldText:     lit.Value,
				NewText:     name,
				Description: fmt.Sprintf("Replace literal with constant %s", name),
			})
			replaced++
		}
		if replaced > 0 {
			plan.AffectedFiles = append(plan.AffectedFiles, file.Path)
		}
	}

	insert := constantInsertOffset(ws, home)
	plan.Changes = append(plan.Changes, pkgtypes.Change{
		File:        home.Path,
		Start:       insert,
		End:         insert,
		NewText:     fmt.Sprintf("\n\nconst %s = %s\n", name, selected.Value),
		Description: fmt.Sprintf("Add constant declaration %s", name),
	})
	if !slices.Contains(plan.AffectedFiles, home.Path) {
		plan.AffectedFiles = append(plan.AffectedFiles, home.Path)
	}
	return plan, nil
}

func (op *ExtractConstantOperation) Description() string {
	return fmt.Sprintf("Extract constant %s from %s", op.Request.ConstantName, op.Request.SourceFile)
}

func invalidExtractConstant(format string, args ...any) error {
	return &pkgtypes.RefactorError{Type: pkgtypes.InvalidOperation, Message: fmt.Sprintf(format, args...)}
}

// findWorkspaceFile returns the package and file loaded from path.
func findWorkspaceFile(ws *pkgtypes.Workspace, path string) (*pkgtypes.Package, *pkgtypes.File) {
	for _, pkg := range ws.Packages {
		for _, files := range []map[string]*pkgtypes.File{pkg.Files, pkg.TestFiles} {
			for key, f := range files {
				if key == path || f.Path == path {
					return pkg, f
				}
			}
		}
	}
	return nil, nil
}

// constantScopeFiles returns the parsed non-external files of pkg ordered by path.
func constantScopeFiles(pkg *pkgtypes.Package) []*pkgtypes.File {
	var files []*pkgtypes.File
	for _, m := range []map[string]*pkgtypes.File{pkg.Files, pkg.TestFiles} {
		for _, f := range m {
			if f.AST != nil && !isExternalTest(pkg, f) {
				files = append(files, f)
			}
		}
	}
	slices.SortFunc(files, func(a, b *pkgtypes.File) int { return strings.Compare(a.Path, b.Path) })
	return files
}

func isExternalTest(pkg *pkgtypes.Package, f *pkgtypes.File) bool {
	return f.AST.Name.Name != pkg.Name
}

// literalAt returns the literal under the requested line and column of src.
func (op *ExtractConstantOperation) literalAt(ws *pkgtypes.Workspace, src *pkgtypes.File) (*ast.BasicLit, error) {
	req := op.Request
	tf := ws.FileSet.File(src.AST.Pos())
	if tf == nil || req.Line > tf.LineCount() {
		return nil, invalidExtractConstant("line %d is outside %s", req.Line, src.Path)
	}
	pos := tf.LineStart(req.Line) + token.Pos(req.Column-1)
	lineEnd := token.Pos(tf.Base() + tf.Size())
	if req.Line < tf.LineCount() {
		lineEnd = tf.LineStart(req.Line+1) - 1
	}
	if pos >= lineEnd {
		return nil, invalidExtractConstant("column %d is outside line %d", req.Column, req.Line)
	}

	var found ast.Node
	var inert = map[*ast.BasicLit]string{}
	ast.Inspect(src.AST, func(n ast.Node) bool {
		switch n := n.(type) {
		case nil:
			return false
		case *ast.ImportSpec:
			inert[n.Path] = "an import path"
		case *ast.Field:
			if n.Tag != nil {
				inert[n.Tag] = "a struct tag"
			}
		}
		if n.Pos() <= pos && pos < n.End() {
			found = n
			return true
		}
		return false
	})
	lit, ok := found.(*ast.BasicLit)
	if !ok {
		return nil, invalidExtractConstant("no literal at %d:%d: found %T; only INT, FLOAT, IMAG, CHAR and STRING literals can be extracted",
			req.Line, req.Column, found)
	}
	what := inert[lit]
	if what != "" {
		return nil, invalidExtractConstant("the literal at %d:%d is %s and cannot be extracted", req.Line, req.Column, what)
	}
	return lit, nil
}

// equalLiterals returns the literals of file that equal selected. Import paths
// and struct tags never match: go/types records no type for them.
func (op *ExtractConstantOperation) equalLiterals(pkg *pkgtypes.Package, file *pkgtypes.File, selected *ast.BasicLit) []*ast.BasicLit {
	var lits []*ast.BasicLit
	ast.Inspect(file.AST, func(n ast.Node) bool {
		lit, ok := n.(*ast.BasicLit)
		if ok && sameLiteral(pkg.TypesInfo, selected, lit) {
			lits = append(lits, lit)
		}
		return true
	})
	return lits
}

// sameLiteral reports whether b is type-checked and a and b are the same kind
// of literal with equal values. The values come from the literal text, not
// from the type-checker, which records them converted to the context type.
func sameLiteral(info *gotypes.Info, a, b *ast.BasicLit) bool {
	if a.Kind != b.Kind {
		return false
	}
	_, ok := info.Types[b]
	if !ok {
		return false
	}
	av, bv := constant.MakeFromLiteral(a.Value, a.Kind, 0), constant.MakeFromLiteral(b.Value, b.Kind, 0)
	return av.Kind() != constant.Unknown && bv.Kind() != constant.Unknown && constant.Compare(av, token.EQL, bv)
}

// checkPackageLevelName refuses a name declared at package level, or used as an
// explicit import alias, in any file of the package directory, whatever its
// build constraints.
func (op *ExtractConstantOperation) checkPackageLevelName(pkg *pkgtypes.Package) error {
	name := op.Request.ConstantName
	files, err := sourceFilesOf(op.Sources, pkg.Dir)
	if err != nil {
		return fmt.Errorf("check package level name: %w", err)
	}
	for _, f := range files {
		if f.AST == nil || f.AST.Name.Name != pkg.Name {
			continue
		}
		if declaresTopLevel(f.AST, name) {
			return invalidExtractConstant("%s is already declared at package level in %s", name, f.Path)
		}
		for _, spec := range f.AST.Imports {
			if spec.Name != nil && spec.Name.Name == name {
				return invalidExtractConstant("%s is already the name of an import in %s", name, f.Path)
			}
		}
	}
	return nil
}

func declaresTopLevel(file *ast.File, name string) bool {
	for _, decl := range file.Decls {
		switch d := decl.(type) {
		case *ast.FuncDecl:
			if d.Recv == nil && d.Name.Name == name {
				return true
			}
		case *ast.GenDecl:
			for _, spec := range d.Specs {
				switch s := spec.(type) {
				case *ast.TypeSpec:
					if s.Name.Name == name {
						return true
					}
				case *ast.ValueSpec:
					for _, id := range s.Names {
						if id.Name == name {
							return true
						}
					}
				}
			}
		}
	}
	return false
}

// checkImportName refuses a name that file imports or dot-imports. Implicit
// import names and dot-imports are only known for files in the type-checked
// package, so those in files of other build configurations go undetected.
func checkImportName(pkg *pkgtypes.Package, file *pkgtypes.File, name string) error {
	for _, spec := range file.AST.Imports {
		pn, _ := pkg.TypesInfo.Implicits[spec].(*gotypes.PkgName)
		switch {
		case spec.Name != nil && spec.Name.Name == ".":
			p, _ := strconv.Unquote(spec.Path.Value)
			for _, imp := range pkg.TypesPkg.Imports() {
				obj := imp.Scope().Lookup(name)
				if imp.Path() == p && obj != nil && obj.Exported() {
					return invalidExtractConstant("%s is dot-imported into %s", name, file.Path)
				}
			}
			continue
		case spec.Name != nil && spec.Name.Name == name:
			return invalidExtractConstant("%s is already the name of an import in %s", name, file.Path)
		case spec.Name != nil:
			continue
		}
		imported := ""
		if pn != nil {
			imported = pn.Name()
		} else {
			p, err := strconv.Unquote(spec.Path.Value)
			if err == nil {
				imported = path.Base(p)
			}
		}
		if imported == name {
			return invalidExtractConstant("%s is already the name of an import in %s", name, file.Path)
		}
	}
	return nil
}

// declaringFile returns src when it is an in-package, non-test, non-generated
// file without build constraints, else the first such file of pkg by path. A
// constant declared anywhere else would be undefined in some build.
func (op *ExtractConstantOperation) declaringFile(pkg *pkgtypes.Package, src *pkgtypes.File) (*pkgtypes.File, error) {
	production := slices.Collect(maps.Values(pkg.Files))
	eligible := func(f *pkgtypes.File) bool {
		if f.Generated || !slices.Contains(production, f) {
			return false
		}
		sf := op.Sources.File(f.Path)
		if sf == nil {
			return false
		}
		c := analysis.ConstraintOf(sf)
		return c.Expr == nil && c.Suffix == "" && !c.Cgo
	}
	if eligible(src) {
		return src, nil
	}
	for _, f := range constantScopeFiles(pkg) {
		if eligible(f) {
			return f, nil
		}
	}
	return nil, invalidExtractConstant("package %s has no unconstrained, non-test, non-generated file to declare the constant in", pkg.ImportPath)
}

// shadowed reports whether name at lit resolves to something other than a package-level object.
func shadowed(pkg *pkgtypes.Package, lit *ast.BasicLit, name string) bool {
	scope := pkg.TypesPkg.Scope().Innermost(lit.Pos())
	if scope == nil {
		return false
	}
	_, obj := scope.LookupParent(name, lit.Pos())
	return obj != nil && obj.Parent() != pkg.TypesPkg.Scope()
}

func skippedLiteral(ws *pkgtypes.Workspace, file *pkgtypes.File, lit *ast.BasicLit, reason string) pkgtypes.SkippedSite {
	p := ws.FileSet.Position(lit.Pos())
	start := bytes.LastIndexByte(file.OriginalContent[:p.Offset], '\n') + 1
	end := len(file.OriginalContent)
	i := bytes.IndexByte(file.OriginalContent[p.Offset:], '\n')
	if i >= 0 {
		end = p.Offset + i
	}
	return pkgtypes.SkippedSite{
		File:    file.Path,
		Line:    p.Line,
		Reason:  reason,
		Snippet: strings.TrimSpace(string(file.OriginalContent[start:end])),
	}
}

// constantInsertOffset returns the end of the line holding the last import
// declaration of file, or its package clause when it has none, so a trailing
// comment stays on its line.
func constantInsertOffset(ws *pkgtypes.Workspace, file *pkgtypes.File) int {
	end := file.AST.Name.End()
	for _, decl := range file.AST.Decls {
		gen, ok := decl.(*ast.GenDecl)
		if ok && gen.Tok == token.IMPORT {
			end = gen.End()
		}
	}
	off := ws.FileSet.Position(end).Offset
	i := bytes.IndexByte(file.OriginalContent[off:], '\n')
	if i >= 0 {
		return off + i
	}
	return len(file.OriginalContent)
}
