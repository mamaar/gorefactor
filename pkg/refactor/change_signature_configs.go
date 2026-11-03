package refactor

import (
	"fmt"
	"go/ast"
	"go/build"
	"go/parser"
	gotypes "go/types"
	"path"
	"regexp"
	"slices"
	"strconv"
	"strings"

	"github.com/mamaar/gorefactor/pkg/analysis"
	"github.com/mamaar/gorefactor/pkg/types"
)

// changeSignaturePlanner plans change_signature in one view.
type changeSignaturePlanner struct {
	viewIndexes
	engine *DefaultEngine
	req    ChangeSignatureRequest
	// before holds the signature of each altered declaration as first seen; a
	// linked declaration must match it to receive the same change.
	before map[types.SymbolKey]declSignature
	// checks holds the latest type check of each view's plan, in planning order.
	checks []viewTypeCheck
}

// viewTypeCheck is the type check of one view's plan.
type viewTypeCheck struct {
	view   *analysis.View
	result *TypeCheckResult
}

// declSignature is a declaration's parameters and result types, as text.
type declSignature struct {
	params  []Parameter
	returns []string
}

func (s declSignature) String() string {
	params := make([]string, len(s.params))
	for i, p := range s.params {
		params[i] = strings.TrimSpace(p.Name + " " + p.Type)
	}
	out := "(" + strings.Join(params, ", ") + ")"
	if len(s.returns) > 0 {
		out += " (" + strings.Join(s.returns, ", ") + ")"
	}
	return out
}

func (s declSignature) equal(o declSignature) bool {
	return paramsEqual(s.params, o.params) && slices.Equal(s.returns, o.returns)
}

func paramsEqual(a, b []Parameter) bool {
	return slices.EqualFunc(a, b, paramEqual)
}

func paramEqual(x, y Parameter) bool {
	return x.Name == y.Name && normalizeType(x.Type) == normalizeType(y.Type)
}

// normalizeType prints a Go type expression canonically; text that does not
// parse is returned unchanged.
func normalizeType(text string) string {
	expr, err := parser.ParseExpr(text)
	if err != nil {
		return text
	}
	return gotypes.ExprString(expr)
}

// signatureOf returns ft's parameters and results, one entry per name.
func signatureOf(ft *ast.FuncType) declSignature {
	var s declSignature
	for _, f := range listOf(ft.Params) {
		typ := gotypes.ExprString(f.Type)
		if len(f.Names) == 0 {
			s.params = append(s.params, Parameter{Type: typ})
		}
		for _, n := range f.Names {
			s.params = append(s.params, Parameter{Name: n.Name, Type: typ})
		}
	}
	for _, f := range listOf(ft.Results) {
		typ := gotypes.ExprString(f.Type)
		for range max(len(f.Names), 1) {
			s.returns = append(s.returns, typ)
		}
	}
	return s
}

func listOf(fl *ast.FieldList) []*ast.Field {
	if fl == nil {
		return nil
	}
	return fl.List
}

func (p *changeSignaturePlanner) planIn(v *analysis.View, linked types.SymbolKeySet) (configResult, error) {
	return planSignatureView(&p.viewIndexes, p, v, linked)
}

// verify type-checks plan in v and keeps the result for reportTypeChecks. It
// checks only the packages the plan touches, under v's build configuration.
func (p *changeSignaturePlanner) verify(v *analysis.View, plan *types.RefactoringPlan) {
	check := viewTypeCheck{view: v, result: VerifyPlanTypesWithImporter(v.Workspace, plan, v.Parser.Importer())}
	for i, c := range p.checks {
		if c.view == v {
			p.checks[i] = check
			return
		}
	}
	p.checks = append(p.checks, check)
}

// reportTypeChecks marks plan blocked by the type errors it would introduce.
// Errors in files the plan already reports as left unedited are not counted,
// and a check that could not run is listed in skipped_sites: the plan is unverified.
func (p *changeSignaturePlanner) reportTypeChecks(plan *types.RefactoringPlan) {
	var unedited []string
	for _, u := range plan.Coverage.Uncovered {
		unedited = append(unedited, u.Path+":")
	}
	for _, s := range plan.SkippedSites {
		unedited = append(unedited, s.File+":")
	}
	var newErrors []string
	for _, c := range p.checks {
		for _, e := range c.result.NewErrors {
			if slices.ContainsFunc(unedited, func(prefix string) bool { return strings.HasPrefix(e, prefix) }) {
				continue
			}
			if len(p.checks) > 1 {
				e = fmt.Sprintf("[%s] %s", c.view.Config, e)
			}
			newErrors = append(newErrors, e)
		}
		if c.result.Skipped != "" {
			// PlanResult has no field for an unverified plan; a site without a file is the closest.
			plan.SkippedSites = append(plan.SkippedSites, types.SkippedSite{
				Reason: "the plan could not be type-checked and is unverified: " + c.result.Skipped,
			})
		}
	}
	if len(newErrors) == 0 {
		return
	}
	if plan.Impact == nil {
		plan.Impact = &types.ImpactAnalysis{}
	}
	plan.Impact.PotentialIssues = append(plan.Impact.PotentialIssues, types.Issue{
		Type:        types.IssueTypeMismatch,
		Severity:    types.Error,
		Description: "the change would introduce type errors:\n" + strings.Join(newErrors, "\n"),
	})
}

// requestedIn validates the request in ws. A function ws does not declare is a
// SymbolNotFound error.
func (p *changeSignaturePlanner) requestedIn(ws *types.Workspace) (*funcTarget, error) {
	op := p.operation(p.req.FunctionName, p.req.SourceFile)
	err := op.Validate(ws)
	if err != nil {
		return nil, fmt.Errorf("change signature operation validation failed: %w", err)
	}
	return &funcTarget{name: op.FunctionName, file: op.SourceFile}, nil
}

func (p *changeSignaturePlanner) operation(name, file string) *ChangeSignatureOperation {
	req := p.req
	req.FunctionName, req.SourceFile = name, file
	return req.operation(p.engine.logger)
}

// run changes t to the requested signature. A linked declaration that already
// has it is left alone, and one whose signature differs from that of the
// declaration the change was planned against aborts the run.
func (p *changeSignaturePlanner) run(r *signatureRun, t funcTarget, requested bool) error {
	ws := r.view.Workspace
	op := p.operation(t.name, t.file)
	if !requested {
		skip, err := p.checkLinked(t)
		if skip || err != nil {
			return err
		}
		if err := op.Validate(ws); err != nil {
			return fmt.Errorf("change signature operation validation failed: %w", err)
		}
	}
	altered, err := r.execute(op)
	if err != nil {
		return fmt.Errorf("failed to generate change signature plan: %w", err)
	}
	if p.before == nil {
		p.before = make(map[types.SymbolKey]declSignature)
	}
	for _, d := range altered {
		_, seen := p.before[d.Key]
		decl, ok := r.decls[d.Key]
		if ok && !seen {
			p.before[d.Key] = signatureOf(decl.ft)
		}
	}
	return nil
}

// checkLinked reports whether t already has the requested signature, and fails
// when it differs from the signature the change was planned against.
func (p *changeSignaturePlanner) checkLinked(t funcTarget) (skip bool, err error) {
	cur := signatureOf(t.ft)
	want, ok := p.before[t.key]
	if !ok || cur.equal(want) {
		return false, nil
	}
	if p.satisfiedBy(cur) {
		return true, nil
	}
	return false, fmt.Errorf(
		"%s in %s has signature %s, which differs from %s, the signature it is changed from under another build configuration; make the declarations agree first",
		qualifiedName(t.key), t.file, cur, want)
}

// satisfiedBy reports whether cur already has what the request asks for.
func (p *changeSignaturePlanner) satisfiedBy(cur declSignature) bool {
	changesParams := len(p.req.NewParams) > 0 || strings.HasSuffix(p.req.Subcommand, "_param")
	changesReturns := len(p.req.NewReturns) > 0 || strings.HasSuffix(p.req.Subcommand, "_return")
	if changesParams && !paramsEqual(cur.params, p.req.NewParams) {
		return false
	}
	if changesReturns {
		want := make([]string, len(p.req.NewReturns))
		for i, ret := range p.req.NewReturns {
			want[i] = normalizeType(ret)
		}
		return slices.Equal(cur.returns, want)
	}
	return true
}

// complete imports the packages of the new parameter and return types in each
// altered declaration's file.
func (p *changeSignaturePlanner) complete(r *signatureRun) {
	var typeTexts []string
	for _, param := range p.req.NewParams {
		typeTexts = append(typeTexts, param.Type)
	}
	typeTexts = append(typeTexts, p.req.NewReturns...)
	var paths []string
	for _, q := range typeQualifiers(typeTexts) {
		ipath := importPathFor(r.view.Workspace, q, p.req.SourceFile)
		if ipath != "" && !slices.Contains(paths, ipath) {
			paths = append(paths, ipath)
		}
	}
	r.addImports(paths)
}

// typeQualifiers returns the package qualifiers the type expressions use, sorted.
func typeQualifiers(typeTexts []string) []string {
	var c qualifierCollector
	for _, text := range typeTexts {
		expr, err := parser.ParseExpr(text)
		if err == nil {
			ast.Inspect(expr, c.visit)
		}
	}
	slices.Sort(c.names)
	return c.names
}

type qualifierCollector struct{ names []string }

func (c *qualifierCollector) visit(n ast.Node) bool {
	sel, ok := n.(*ast.SelectorExpr)
	if ok {
		id, ok := sel.X.(*ast.Ident)
		if ok && !slices.Contains(c.names, id.Name) {
			c.names = append(c.names, id.Name)
		}
	}
	return true
}

// importPathFor returns the import path qualifier names in ws: the one
// preferFile imports it as, else the only one any file does, else the standard
// library package of that name. It is empty when the path is ambiguous or unknown.
func importPathFor(ws *types.Workspace, qualifier, preferFile string) string {
	found := make(map[string]bool)
	preferred := ""
	for _, pkg := range ws.Packages {
		for _, f := range allFilesOf(pkg) {
			if f.AST == nil {
				continue
			}
			for _, imp := range f.AST.Imports {
				ipath, err := strconv.Unquote(imp.Path.Value)
				if err != nil || importedAs(imp, ipath) != qualifier {
					continue
				}
				found[ipath] = true
				if f.Path == preferFile {
					preferred = ipath
				}
			}
		}
	}
	if preferred != "" {
		return preferred
	}
	for ipath := range found {
		if len(found) == 1 {
			return ipath
		}
	}
	if len(found) == 0 {
		pkg, err := build.Default.Import(qualifier, "", build.FindOnly)
		if err == nil && pkg.Goroot {
			return qualifier
		}
	}
	return ""
}

var majorVersionElem = regexp.MustCompile(`^v[0-9]+$`)

// importedAs returns the name an import declaration binds in its file.
func importedAs(imp *ast.ImportSpec, ipath string) string {
	if imp.Name != nil {
		return imp.Name.Name
	}
	base := path.Base(ipath)
	if majorVersionElem.MatchString(base) && strings.Contains(ipath, "/") {
		base = path.Base(path.Dir(ipath))
	}
	return base
}
