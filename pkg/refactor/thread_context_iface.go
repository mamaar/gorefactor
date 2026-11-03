package refactor

import (
	"fmt"
	"go/ast"
	gotypes "go/types"
	"slices"
	"strings"

	"github.com/mamaar/gorefactor/pkg/analysis"
	pkgtypes "github.com/mamaar/gorefactor/pkg/types"
)

// This file owns interface expansion for ThreadContextOperation: deciding which
// interface method declarations and sibling implementations must gain a ctx
// alongside a method entering the worklist, and aborting when the method
// satisfies an interface the refactoring cannot edit.

// ifaceCandidate is a named interface type paired with its completed method set.
type ifaceCandidate struct {
	named *gotypes.Named
	iface *gotypes.Interface
}

// label renders the candidate as "net/http.Handler" — the form the abort
// messages must use so the user can find the offender.
func (c ifaceCandidate) label() string {
	obj := c.named.Obj()
	if obj.Pkg() == nil {
		return obj.Name() // a builtin such as `error`
	}

	return fmt.Sprintf("%s.%s", obj.Pkg().Path(), obj.Name())
}

// ifaceSurvey is the workspace's interface landscape, computed once per run.
//
// `workspace` interfaces can be rewritten and are therefore expanded into;
// `external` ones cannot and are therefore abort triggers. `impls` is every
// named non-interface type in the workspace, i.e. the candidate implementations.
type ifaceSurvey struct {
	workspace []ifaceCandidate
	external  []ifaceCandidate
	impls     []*gotypes.Named

	// views holds every checked copy of a workspace type declaration: the one of
	// its package's own check and the ones its importers hold.
	views map[analysis.ObjectID][]*gotypes.Named
	// declared holds, per method declaration, the object its package's own
	// check created.
	declared map[analysis.ObjectID]*gotypes.Func
}

// interfaceSurvey returns the operation's survey, building it on first use.
func (op *ThreadContextOperation) interfaceSurvey(ws *pkgtypes.Workspace) *ifaceSurvey {
	if op.ifaceCache == nil {
		op.ifaceCache = surveyInterfaces(ws)
	}

	return op.ifaceCache
}

// ifaceSurveyor accumulates an ifaceSurvey.
type ifaceSurveyor struct {
	survey      *ifaceSurvey
	inWorkspace func(*gotypes.Package) bool
	seenIface   map[*gotypes.Named]bool
	seenType    map[gotypes.Type]bool
}

// surveyInterfaces surveys the interfaces and implementation types of ws.
func surveyInterfaces(ws *pkgtypes.Workspace) *ifaceSurvey {
	b := &ifaceSurveyor{
		survey: &ifaceSurvey{
			views:    make(map[analysis.ObjectID][]*gotypes.Named),
			declared: make(map[analysis.ObjectID]*gotypes.Func),
		},
		inWorkspace: workspacePkgPredicate(ws),
		seenIface:   make(map[*gotypes.Named]bool),
		seenType:    make(map[gotypes.Type]bool),
	}
	for _, pkg := range ws.Packages {
		if pkg.TypesPkg != nil {
			b.declareScope(pkg.TypesPkg)
			b.addImportedInterfaces(pkg.TypesPkg)
		}

		if pkg.TypesInfo != nil {
			b.addMentionedInterfaces(pkg.TypesInfo)
		}
	}
	// Everything the workspace itself declares, in every checked copy.
	for _, checked := range checkedPackages(ws, b.inWorkspace) {
		b.addScope(checked)
	}

	return b.survey
}

// checkedPackages returns every go/types package checked for a workspace
// package: each package's own check and the copies its importers hold.
func checkedPackages(ws *pkgtypes.Workspace, inWorkspace func(*gotypes.Package) bool) []*gotypes.Package {
	var work, out []*gotypes.Package

	for _, pkg := range ws.Packages {
		if pkg.TypesPkg != nil {
			work = append(work, pkg.TypesPkg)
		}

		if pkg.ExternalTestTypesPkg != nil {
			work = append(work, pkg.ExternalTestTypesPkg)
			work = append(work, pkg.ExternalTestTypesPkg.Imports()...)
		}
	}

	seen := make(map[*gotypes.Package]bool)

	for len(work) > 0 {
		checked := work[len(work)-1]
		work = work[:len(work)-1]

		if seen[checked] || !inWorkspace(checked) {
			continue
		}

		seen[checked] = true
		out = append(out, checked)
		work = append(work, checked.Imports()...)
	}

	return out
}

func (b *ifaceSurveyor) add(c ifaceCandidate) {
	if b.seenIface[c.named] {
		return
	}

	b.seenIface[c.named] = true
	if c.named.Obj().Pkg() != nil && b.inWorkspace(c.named.Obj().Pkg()) {
		b.survey.workspace = append(b.survey.workspace, c)
	} else {
		b.survey.external = append(b.survey.external, c)
	}
}

// scopeTypes returns the named types checked declares at package level.
func scopeTypes(checked *gotypes.Package) []*gotypes.Named {
	var out []*gotypes.Named

	for _, name := range checked.Scope().Names() {
		tn, ok := checked.Scope().Lookup(name).(*gotypes.TypeName)
		if !ok {
			continue
		}

		if named, ok := tn.Type().(*gotypes.Named); ok {
			out = append(out, named)
		}
	}

	return out
}

// declareScope records the methods own declares; own is a package's own check.
func (b *ifaceSurveyor) declareScope(own *gotypes.Package) {
	for _, named := range scopeTypes(own) {
		iface, ok := asInterfaceType(named)
		if ok {
			for method := range iface.ExplicitMethods() {
				b.declare(method)
			}

			continue
		}

		for method := range named.Methods() {
			b.declare(method)
		}
	}
}

func (b *ifaceSurveyor) declare(m *gotypes.Func) {
	b.survey.declared[analysis.ObjectIDOf(m)] = m
}

// addScope adds the interfaces and candidate implementations checked declares.
func (b *ifaceSurveyor) addScope(checked *gotypes.Package) {
	for _, named := range scopeTypes(checked) {
		id := analysis.ObjectIDOf(named.Obj())
		b.survey.views[id] = append(b.survey.views[id], named)

		iface, ok := asInterfaceType(named)
		if ok {
			b.add(ifaceCandidate{named: named, iface: iface})
		} else {
			b.survey.impls = append(b.survey.impls, named)
		}
	}
}

// addImportedInterfaces adds every exported interface of every package outside
// the workspace that checked imports DIRECTLY. This is the net that catches
// fmt.Stringer and io.Writer: those never appear as the type of any expression
// in code that merely satisfies them, so a scan of type info alone would miss
// them entirely. Direct imports (not transitive) keeps this a bounded scope walk.
func (b *ifaceSurveyor) addImportedInterfaces(checked *gotypes.Package) {
	for _, imp := range checked.Imports() {
		if b.inWorkspace(imp) {
			continue
		}

		for _, named := range scopeTypes(imp) {
			if !named.Obj().Exported() {
				continue
			}

			iface, ok := asInterfaceType(named)
			if ok {
				b.add(ifaceCandidate{named: named, iface: iface})
			}
		}
	}
}

// addMentionedInterfaces adds the interfaces reachable from the types info
// mentions. This covers interfaces reached through a signature the workspace
// calls (sort.Sort's `func(data sort.Interface)`) even when the defining package
// is only an indirect import.
func (b *ifaceSurveyor) addMentionedInterfaces(info *gotypes.Info) {
	for _, tv := range info.Types {
		collectIfaceTypes(tv.Type, b.seenType, b.add)
	}

	for _, obj := range info.Defs {
		if obj != nil {
			collectIfaceTypes(obj.Type(), b.seenType, b.add)
		}
	}

	for _, obj := range info.Uses {
		if obj != nil {
			collectIfaceTypes(obj.Type(), b.seenType, b.add)
		}
	}
}

// workspacePkgPredicate reports membership of a go/types package in the
// workspace. Pointer identity is exact for anything type-checked; the import
// path fallback keeps a package whose type-checking failed from being
// misclassified as external (which would turn its interfaces into spurious
// aborts).
func workspacePkgPredicate(ws *pkgtypes.Workspace) func(*gotypes.Package) bool {
	byPtr := make(map[*gotypes.Package]bool)
	byPath := make(map[string]bool)

	for _, pkg := range ws.Packages {
		if pkg.TypesPkg != nil {
			byPtr[pkg.TypesPkg] = true
		}

		if pkg.ImportPath != "" {
			byPath[pkg.ImportPath] = true
		}
	}

	for importPath := range ws.ImportToPath {
		byPath[importPath] = true
	}

	return func(p *gotypes.Package) bool {
		if p == nil {
			return false
		}

		return byPtr[p] || byPath[p.Path()]
	}
}

// collectIfaceTypes walks t structurally and reports every named interface it
// reaches.
//
// The walk deliberately stops at a named interface rather than descending into
// its methods' signatures, and never descends into struct fields: those two
// edges are what would turn this into an unbounded crawl of every package the
// workspace transitively touches. What is walked is the shape of types the
// workspace literally mentions — pointers, containers, tuples and signatures —
// which is where an un-editable interface can hide (a call to
// `http.Handle(pattern string, handler Handler)` mentions Handler only inside
// that signature).
func collectIfaceTypes(t gotypes.Type, seen map[gotypes.Type]bool, add func(ifaceCandidate)) {
	if t == nil || seen[t] {
		return
	}

	seen[t] = true

	switch t := t.(type) {
	case *gotypes.Named:
		iface, ok := t.Underlying().(*gotypes.Interface)
		if ok {
			add(ifaceCandidate{named: t, iface: iface})
			return
		}
		// A defined non-interface type: only its shape can carry an interface
		// through (e.g. `type HandlerFunc func(ResponseWriter, *Request)`).
		_, isStruct := t.Underlying().(*gotypes.Struct)
		if !isStruct {
			collectIfaceTypes(t.Underlying(), seen, add)
		}
	case *gotypes.Pointer:
		collectIfaceTypes(t.Elem(), seen, add)
	case *gotypes.Slice:
		collectIfaceTypes(t.Elem(), seen, add)
	case *gotypes.Array:
		collectIfaceTypes(t.Elem(), seen, add)
	case *gotypes.Chan:
		collectIfaceTypes(t.Elem(), seen, add)
	case *gotypes.Map:
		collectIfaceTypes(t.Key(), seen, add)
		collectIfaceTypes(t.Elem(), seen, add)
	case *gotypes.Tuple:
		for v := range t.Variables() {
			collectIfaceTypes(v.Type(), seen, add)
		}
	case *gotypes.Signature:
		collectIfaceTypes(t.Params(), seen, add)
		collectIfaceTypes(t.Results(), seen, add)
	}
}

// externalInterfaceAbort fails when f's receiver satisfies an interface declared
// outside the workspace that declares a method of f's name.
//
// Only the survey sees interfaces outside the workspace — http.Handler,
// io.Writer, sort.Interface — where the damage is silent: nothing warns, the
// type simply stops satisfying the interface.
func (op *ThreadContextOperation) externalInterfaceAbort(ws *pkgtypes.Workspace, f *gotypes.Func) error {
	ext, ok := op.interfaceSurvey(ws).externalInterfaceOf(f)
	if ok {
		return op.externalIfaceError(f, ext)
	}

	return nil
}

// externalInterfaceOf returns the first interface declared outside the
// workspace that declares a method of f's name and that a type holding f in its
// method set satisfies.
func (s *ifaceSurvey) externalInterfaceOf(f *gotypes.Func) (ifaceCandidate, bool) {
	for _, recv := range s.receiversOf(f) {
		for _, ext := range s.external {
			if interfaceMethodNamed(ext.iface, f.Name()) != nil && typeImplements(recv, ext.iface) {
				return ext, true
			}
		}
	}

	return ifaceCandidate{}, false
}

// receiversOf returns the types whose method set holds f: every checked copy of
// its receiver, and the workspace types that get it by embedding.
func (s *ifaceSurvey) receiversOf(f *gotypes.Func) []gotypes.Type {
	recv := methodRecvType(f)
	if recv == nil {
		return nil
	}

	out := s.viewsOf(recv)

	id := analysis.ObjectIDOf(f)
	for _, named := range s.impls {
		m := lookupMethod(named, f.Name())
		if m != nil && analysis.ObjectIDOf(m) == id {
			out = append(out, named)
		}
	}

	return out
}

func (op *ThreadContextOperation) externalIfaceError(f *gotypes.Func, ext ifaceCandidate) error {
	var lead string
	if f == op.seed {
		lead = fmt.Sprintf("cannot thread context through %s: the seed itself implements", funcLabel(f))
	} else {
		lead = fmt.Sprintf(
			"cannot thread context: %s must gain a ctx parameter to reach the seed, but it implements",
			funcLabel(f))
	}

	return &pkgtypes.RefactorError{
		Type: pkgtypes.InvalidOperation,
		Message: fmt.Sprintf(
			"%s %s, which is declared outside the workspace and cannot be changed; adding a ctx "+
				"parameter to %s would silently stop %s satisfying %s. No changes were made.",
			lead, ext.label(), f.Name(), gotypes.TypeString(methodRecvType(f), nil), ext.label()),
	}
}

// implementationsOf returns the method named methodName on every workspace type
// that satisfies iface. The seed's own receiver is not filtered out: it is
// already in the visited set by the time this runs, so discover drops it.
func (s *ifaceSurvey) implementationsOf(iface *gotypes.Interface, methodName string) []*gotypes.Func {
	var out []*gotypes.Func

	for _, named := range s.impls {
		if !typeImplements(named, iface) {
			continue
		}

		m := lookupMethod(named, methodName)
		if m != nil {
			out = append(out, canonicalFunc(m))
		}
	}

	return out
}

// lookupMethod finds the method named name in T's method set, searching the
// POINTER's method set so that pointer-receiver methods are found too — the
// value's method set omits them, and skipping them here would silently leave a
// real implementation unthreaded.
func lookupMethod(named *gotypes.Named, name string) *gotypes.Func {
	var pkg *gotypes.Package
	if named.Obj() != nil {
		pkg = named.Obj().Pkg()
	}

	obj, _, _ := gotypes.LookupFieldOrMethod(gotypes.NewPointer(named), true, pkg, name)
	m, _ := obj.(*gotypes.Func)

	return m
}

// typeImplements reports whether t satisfies iface, testing t AND *t.
//
// Both must be tested. A method declared with a VALUE receiver belongs to the
// method sets of both T and *T, so a type whose OTHER methods take pointer
// receivers satisfies the interface only as *T. Testing just Implements(t,
// iface) — the intuitive check — therefore misses a real satisfaction that the
// signature change would break, which for an external interface means a silent
// break. Note *T's method set is a superset of T's, so the pointer test alone
// can never be less permissive.
func typeImplements(t gotypes.Type, iface *gotypes.Interface) bool {
	if t == nil || iface == nil {
		return false
	}

	if gotypes.Implements(t, iface) {
		return true
	}
	// An interface's own "pointer" has an empty method set, and taking a pointer
	// to a pointer is never meaningful for satisfaction.
	_, isPtr := t.(*gotypes.Pointer)
	if isPtr {
		return false
	}

	if gotypes.IsInterface(t) {
		return false
	}

	return gotypes.Implements(gotypes.NewPointer(t), iface)
}

// interfaceMethodNamed returns iface's method called name, or nil. NumMethods
// spans the COMPLETED method set, so a method promoted from an embedded
// interface (io.WriteCloser's Write, from io.Writer) is found.
func interfaceMethodNamed(iface *gotypes.Interface, name string) *gotypes.Func {
	for m := range iface.Methods() {
		if m.Name() == name {
			return m
		}
	}

	return nil
}

// methodRecvType returns f's receiver type, or nil when f is not a method.
func methodRecvType(f *gotypes.Func) gotypes.Type {
	sig, ok := f.Type().(*gotypes.Signature)
	if !ok || sig.Recv() == nil {
		return nil
	}

	return sig.Recv().Type()
}

// findInterfaceMethodIdent resolves a seed written as "Store.Save" to the name
// identifier of Save inside Store's declaration.
//
// Threading an interface method directly is a first-class request (it is the
// natural way to say "everyone who implements this must take a ctx"), but the
// method is an *ast.Field, so FindFuncDeclByName — which only ever matches
// *ast.FuncDecl — cannot see it and reports the seed as missing.
func findInterfaceMethodIdent(file *ast.File, name string) *ast.Ident {
	typeName, methodName, ok := strings.Cut(name, ".")
	if !ok {
		return nil
	}
	var found *ast.Ident

	ast.Inspect(file, func(n ast.Node) bool {
		if found != nil {
			return false
		}

		ts, ok := n.(*ast.TypeSpec)
		if !ok || ts.Name.Name != typeName {
			return true
		}

		iface, ok := ts.Type.(*ast.InterfaceType)
		if !ok || iface.Methods == nil {
			return false
		}

		for _, field := range iface.Methods.List {
			if len(field.Names) == 0 || field.Names[0].Name != methodName {
				continue
			}

			_, ok := field.Type.(*ast.FuncType)
			if ok {
				found = field.Names[0]
				return false
			}
		}

		return false
	})

	return found
}

// asInterfaceType reports whether t is (or names) an interface.
func asInterfaceType(t gotypes.Type) (*gotypes.Interface, bool) {
	if t == nil {
		return nil, false
	}

	iface, ok := t.Underlying().(*gotypes.Interface)

	return iface, ok
}

// expansionOf returns the functions that must change alongside f: for a
// concrete method, the workspace interface methods it satisfies; for an
// interface method, every workspace implementation of it. Each hop reaches the
// other direction in turn, so a worklist over it covers an interface, its
// implementations and the interfaces those satisfy.
func (s *ifaceSurvey) expansionOf(f *gotypes.Func) []*gotypes.Func {
	var out []*gotypes.Func

	for _, view := range s.receiversOf(f) {
		iface, ok := asInterfaceType(view)
		if ok {
			out = append(out, s.implementationsOf(iface, f.Name())...)
			continue
		}

		for _, cand := range s.workspace {
			m := interfaceMethodNamed(cand.iface, f.Name())
			if m == nil || !typeImplements(view, cand.iface) {
				continue
			}

			out = append(out, canonicalFunc(m))
		}
	}

	return s.declaredFuncs(out)
}

// viewsOf returns every checked copy of the named type recv is or points to,
// or recv alone when the workspace does not declare it at package level.
func (s *ifaceSurvey) viewsOf(recv gotypes.Type) []gotypes.Type {
	t := gotypes.Unalias(recv)

	ptr, ok := t.(*gotypes.Pointer)
	if ok {
		t = gotypes.Unalias(ptr.Elem())
	}

	named, ok := t.(*gotypes.Named)
	if !ok {
		return []gotypes.Type{recv}
	}

	copies := s.views[analysis.ObjectIDOf(named.Obj())]
	if len(copies) == 0 {
		return []gotypes.Type{recv}
	}

	out := make([]gotypes.Type, len(copies))
	for i, c := range copies {
		out[i] = c
	}

	return out
}

// declaredFuncs maps each method to the object its package's own check
// created, without repeats.
//
// Importers of a package whose tests close an import cycle hold a separately
// checked copy of it, whose objects and types differ by pointer. Callers key
// by object, so nothing found through types may leave here unmapped.
func (s *ifaceSurvey) declaredFuncs(funcs []*gotypes.Func) []*gotypes.Func {
	var out []*gotypes.Func

	for _, f := range funcs {
		own, ok := s.declared[analysis.ObjectIDOf(f)]
		if ok {
			f = own
		}

		if !slices.Contains(out, f) {
			out = append(out, f)
		}
	}

	return out
}

// methodClosure returns seed and every method that must change with it: the
// workspace interface methods a method satisfies, the implementations of those,
// and so on. refuse builds the error for a method that satisfies an interface
// outside the workspace.
func methodClosure(
	survey *ifaceSurvey, seed *gotypes.Func, refuse func(*gotypes.Func, ifaceCandidate) error,
) ([]*gotypes.Func, error) {
	var members []*gotypes.Func
	visited := make(map[gotypes.Object]bool)

	for work := []*gotypes.Func{seed}; len(work) > 0; work = work[1:] {
		f := work[0]
		if visited[f] {
			continue
		}

		visited[f] = true

		ext, ok := survey.externalInterfaceOf(f)
		if ok {
			return nil, refuse(f, ext)
		}

		members = append(members, f)
		for _, next := range survey.expansionOf(f) {
			work = append(work, canonicalFunc(next))
		}
	}

	return members, nil
}
