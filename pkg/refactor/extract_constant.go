package refactor

import (
	"bytes"
	"fmt"
	"go/ast"
	"go/constant"
	"go/token"
	gotypes "go/types"
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
	case req.Line < 1 || req.Column < 1:
		return invalidExtractConstant("line and column must be 1-based positions")
	}
	if pkg, _ := findWorkspaceFile(ws, req.SourceFile); pkg == nil {
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
	if pkg.TypesInfo.Types[selected].Value == nil {
		return nil, invalidExtractConstant("the literal %s has no constant value", selected.Value)
	}
	if err := op.checkPackageLevelName(pkg); err != nil {
		return nil, err
	}

	plan := &pkgtypes.RefactoringPlan{Reversible: true}
	for _, file := range constantScopeFiles(pkg) {
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
		if err := checkImportName(pkg, file, name); err != nil {
			return nil, err
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

	insert := constantInsertOffset(ws, src)
	plan.Changes = append(plan.Changes, pkgtypes.Change{
		File:        src.Path,
		Start:       insert,
		End:         insert,
		NewText:     fmt.Sprintf("\n\nconst %s = %s\n", name, selected.Value),
		Description: fmt.Sprintf("Add constant declaration %s", name),
	})
	if !slices.Contains(plan.AffectedFiles, src.Path) {
		plan.AffectedFiles = append(plan.AffectedFiles, src.Path)
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
	if pos > token.Pos(tf.Base()+tf.Size()) {
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
	if what := inert[lit]; what != "" {
		return nil, invalidExtractConstant("the literal at %d:%d is %s and cannot be extracted", req.Line, req.Column, what)
	}
	return lit, nil
}

// equalLiterals returns the literals of file that equal selected, leaving out
// import paths and struct tags.
func (op *ExtractConstantOperation) equalLiterals(pkg *pkgtypes.Package, file *pkgtypes.File, selected *ast.BasicLit) []*ast.BasicLit {
	inert := map[*ast.BasicLit]bool{}
	var lits []*ast.BasicLit
	ast.Inspect(file.AST, func(n ast.Node) bool {
		switch n := n.(type) {
		case *ast.ImportSpec:
			inert[n.Path] = true
		case *ast.Field:
			if n.Tag != nil {
				inert[n.Tag] = true
			}
		case *ast.BasicLit:
			if !inert[n] && sameLiteral(pkg.TypesInfo, selected, n) {
				lits = append(lits, n)
			}
		}
		return true
	})
	return lits
}

// sameLiteral reports whether a and b are the same kind of literal with equal constant values.
func sameLiteral(info *gotypes.Info, a, b *ast.BasicLit) bool {
	if a.Kind != b.Kind {
		return false
	}
	av, bv := info.Types[a].Value, info.Types[b].Value
	return av != nil && bv != nil && constant.Compare(av, token.EQL, bv)
}

// checkPackageLevelName refuses a name declared at package level in any file of
// the package directory, whatever its build constraints.
func (op *ExtractConstantOperation) checkPackageLevelName(pkg *pkgtypes.Package) error {
	name := op.Request.ConstantName
	files, err := sourceFilesOf(op.Sources, pkg.Dir)
	if err != nil {
		return err
	}
	for _, f := range files {
		if f.AST == nil || f.AST.Name.Name != pkg.Name {
			continue
		}
		if declaresTopLevel(f.AST, name) {
			return invalidExtractConstant("%s is already declared at package level in %s", name, f.Path)
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

// checkImportName refuses a name that file already uses for an import.
func checkImportName(pkg *pkgtypes.Package, file *pkgtypes.File, name string) error {
	for _, spec := range file.AST.Imports {
		imported := ""
		if spec.Name != nil {
			imported = spec.Name.Name
		} else if pn, ok := pkg.TypesInfo.Implicits[spec].(*gotypes.PkgName); ok {
			imported = pn.Name()
		} else if p, err := strconv.Unquote(spec.Path.Value); err == nil {
			imported = path.Base(p)
		}
		if imported == name {
			return invalidExtractConstant("%s is already the name of an import in %s", name, file.Path)
		}
	}
	return nil
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
	if i := bytes.IndexByte(file.OriginalContent[p.Offset:], '\n'); i >= 0 {
		end = p.Offset + i
	}
	return pkgtypes.SkippedSite{
		File:    file.Path,
		Line:    p.Line,
		Reason:  reason,
		Snippet: strings.TrimSpace(string(file.OriginalContent[start:end])),
	}
}

// constantInsertOffset returns the offset just after the last import
// declaration of src, or after its package clause when it has none.
func constantInsertOffset(ws *pkgtypes.Workspace, src *pkgtypes.File) int {
	end := src.AST.Name.End()
	for _, decl := range src.AST.Decls {
		if gen, ok := decl.(*ast.GenDecl); ok && gen.Tok == token.IMPORT {
			end = gen.End()
		}
	}
	return ws.FileSet.Position(end).Offset
}
