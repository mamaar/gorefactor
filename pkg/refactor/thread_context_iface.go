package refactor

import (
	"fmt"
	"go/ast"
	gotypes "go/types"
	"strings"

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
}

// interfaceSurvey builds (and memoizes) the survey.
func (op *ThreadContextOperation) interfaceSurvey(ws *pkgtypes.Workspace) *ifaceSurvey {
	if op.ifaceCache != nil {
		return op.ifaceCache
	}

	inWorkspace := workspacePkgPredicate(ws)
	survey := &ifaceSurvey{}
	seenIface := make(map[*gotypes.Named]bool)

	add := func(c ifaceCandidate) {
		if seenIface[c.named] {
			return
		}
		seenIface[c.named] = true
		if c.named.Obj().Pkg() != nil && inWorkspace(c.named.Obj().Pkg()) {
			survey.workspace = append(survey.workspace, c)
		} else {
			survey.external = append(survey.external, c)
		}
	}

	seenType := make(map[gotypes.Type]bool)
	for _, pkg := range ws.Packages {
		if pkg.TypesPkg != nil {
			// Everything the workspace itself declares.
			for _, name := range pkg.TypesPkg.Scope().Names() {
				tn, ok := pkg.TypesPkg.Scope().Lookup(name).(*gotypes.TypeName)
				if !ok {
					continue
				}
				named, ok := tn.Type().(*gotypes.Named)
				if !ok {
					continue
				}
				if iface, ok := asInterfaceType(named); ok {
					add(ifaceCandidate{named: named, iface: iface})
				} else {
					survey.impls = append(survey.impls, named)
				}
			}

			// Every exported interface of every DIRECTLY imported package. This is
			// the net that catches fmt.Stringer and io.Writer: those never appear as
			// the type of any expression in code that merely satisfies them, so a
			// scan of type info alone would miss them entirely. Direct imports (not
			// transitive) keeps this a bounded scope walk.
			for _, imp := range pkg.TypesPkg.Imports() {
				if inWorkspace(imp) {
					continue
				}
				for _, name := range imp.Scope().Names() {
					tn, ok := imp.Scope().Lookup(name).(*gotypes.TypeName)
					if !ok || !tn.Exported() {
						continue
					}
					named, ok := tn.Type().(*gotypes.Named)
					if !ok {
						continue
					}
					if iface, ok := asInterfaceType(named); ok {
						add(ifaceCandidate{named: named, iface: iface})
					}
				}
			}
		}

		if pkg.TypesInfo == nil {
			continue
		}
		// Interfaces reachable from the types the workspace actually mentions.
		// This covers interfaces reached through a signature the workspace calls
		// (sort.Sort's `func(data sort.Interface)`) even when the defining package
		// is only an indirect import.
		for _, tv := range pkg.TypesInfo.Types {
			collectIfaceTypes(tv.Type, seenType, add)
		}
		for _, obj := range pkg.TypesInfo.Defs {
			if obj != nil {
				collectIfaceTypes(obj.Type(), seenType, add)
			}
		}
		for _, obj := range pkg.TypesInfo.Uses {
			if obj != nil {
				collectIfaceTypes(obj.Type(), seenType, add)
			}
		}
	}

	op.ifaceCache = survey
	return survey
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
		if iface, ok := t.Underlying().(*gotypes.Interface); ok {
			add(ifaceCandidate{named: t, iface: iface})
			return
		}
		// A defined non-interface type: only its shape can carry an interface
		// through (e.g. `type HandlerFunc func(ResponseWriter, *Request)`).
		if _, isStruct := t.Underlying().(*gotypes.Struct); !isStruct {
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
		for i := range t.Len() {
			collectIfaceTypes(t.At(i).Type(), seen, add)
		}
	case *gotypes.Signature:
		collectIfaceTypes(t.Params(), seen, add)
		collectIfaceTypes(t.Results(), seen, add)
	}
}

// externalInterfaceAbort fails when f's receiver satisfies an interface declared
// outside the workspace that declares a method of f's name.
//
// findInterfacesForMethod (change_signature.go) cannot serve here: it only ever
// looks at workspace symbols, so it reports nothing for exactly the case that
// matters most — http.Handler, io.Writer, sort.Interface — where the damage is
// silent. Nothing warns; the type simply stops satisfying the interface.
func (op *ThreadContextOperation) externalInterfaceAbort(ws *pkgtypes.Workspace, f *gotypes.Func) error {
	recv := methodRecvType(f)
	if recv == nil {
		return nil
	}
	for _, ext := range op.interfaceSurvey(ws).external {
		if interfaceMethodNamed(ext.iface, f.Name()) == nil {
			continue
		}
		if !typeImplements(recv, ext.iface) {
			continue
		}
		return op.externalIfaceError(f, ext)
	}
	return nil
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
func (op *ThreadContextOperation) implementationsOf(
	survey *ifaceSurvey,
	iface *gotypes.Interface,
	methodName string,
) []*gotypes.Func {
	var out []*gotypes.Func
	for _, named := range survey.impls {
		if !typeImplements(named, iface) {
			continue
		}
		if m := lookupMethod(named, methodName); m != nil {
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
	if _, isPtr := t.(*gotypes.Pointer); isPtr {
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
	for i := range iface.NumMethods() {
		if m := iface.Method(i); m.Name() == name {
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
			if _, ok := field.Type.(*ast.FuncType); ok {
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
