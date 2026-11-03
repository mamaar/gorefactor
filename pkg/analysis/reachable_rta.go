package analysis

import (
	"fmt"
	"go/ast"
	"go/token"
	gotypes "go/types"
	"slices"
	"strings"

	"golang.org/x/tools/go/callgraph"
	"golang.org/x/tools/go/callgraph/rta"
	"golang.org/x/tools/go/ssa"
	"golang.org/x/tools/go/types/typeutil"

	"github.com/mamaar/gorefactor/pkg/types"
)

// liveness is the RTA call graph of the entry program, indexed by the
// declarations holding its code.
type liveness struct {
	pred  map[*callgraph.Node]*callgraph.Node // BFS tree from the graph root
	decls []liveDecl                          // reachable func declarations, by position
	inits map[string]*callgraph.Node          // package path -> its initializer

	bodiless []BodilessPackage // code the analysis could not see

	graph *callgraph.Graph
	scans scanCache
	// takenHere, when set, limits calls through func values to the functions in
	// it, the ones taken as values inside the search.
	takenHere map[*ssa.Function]bool
}

type liveDecl struct {
	pos, end token.Pos
	node     *callgraph.Node // the first reachable function of the declaration
}

// buildLiveness runs rapid type analysis from main of entry, the initializers
// of the packages it imports, and with includeTests their test functions.
// A function taken as a value is live, even where no call of its signature
// reaches it. The methods of a type converted to an interface, or reachable
// from it through exported struct fields, are live when an interface outside the
// workspace declares a method of that name, because code outside the
// workspace may call them.
func (ua *UnusedAnalyzer) buildLiveness(entry *types.Package, includeTests bool) (live *liveness, err error) {
	defer func() {
		r := recover()
		if r != nil {
			err = fmt.Errorf("analysing %s: %v", entry.Path, r)
		}
	}()

	prog := ssa.NewProgram(ua.workspace.FileSet, ssa.InstantiateGenerics|ssa.BuildSerially)
	sc := newSourceChecker(ua.workspace)
	ssaPkgs := make(map[*types.Package]*ssa.Package)
	var testPkgs []*ssa.Package
	created := make(map[*gotypes.Package]bool)
	create := func(c *checkedPkg, importable bool) *ssa.Package {
		created[c.pkg] = true
		return prog.CreatePackage(c.pkg, c.files, c.info, importable)
	}

	testPkgs, err = ua.createSSAPackages(entry, includeTests, sc, ssaPkgs, create, testPkgs)
	if err != nil {
		return nil, fmt.Errorf("build liveness: %w", err)
	}

	external := createImports(prog, created)
	prog.Build()

	mainFn := ssaPkgs[entry].Func("main")
	if mainFn == nil {
		return nil, fmt.Errorf("entry package %s has no func main", entry.Path)
	}

	roots := entryRoots(mainFn, ua, entry, includeTests, ssaPkgs, testPkgs)

	names := externalMethodNames(external)

	rootSet := make(map[*ssa.Function]bool)
	for _, r := range roots {
		rootSet[r] = true
	}

	scans := make(scanCache)
	takenOnly := make(map[*ssa.Function]bool) // roots added because a function takes them as a value

	for {
		res := rta.Analyze(roots, true)
		live = newLiveness(res.CallGraph, roots, takenOnly, scans)

		added, addedTaken := discoverRoots(rootSet, live, scans, prog, names)
		if len(added)+len(addedTaken) == 0 {
			live.bodiless = sc.bodiless
			return live, nil
		}

		for _, fn := range addedTaken {
			takenOnly[fn] = true
		}

		added = append(added, addedTaken...)
		slices.SortFunc(added, func(a, b *ssa.Function) int { return strings.Compare(a.String(), b.String()) })
		roots = append(roots, added...)
	}
}

func discoverRoots(rootSet map[*ssa.Function]bool, live *liveness, scans scanCache, prog *ssa.Program, names map[string]bool) ([]*ssa.Function, []*ssa.Function) {
	var added, addedTaken []*ssa.Function
	addRoot := func(fn *ssa.Function, list *[]*ssa.Function) {
		if fn != nil && !rootSet[fn] && (len(fn.Blocks) > 0 || fn.Synthetic != "") {
			rootSet[fn] = true
			*list = append(*list, fn)
		}
	}
	var converted typeutil.Map

	for n := range live.pred {
		if n.Func == nil {
			continue
		}

		for _, T := range scans.of(n.Func).converted {
			addReflected(&converted, T)
		}
	}

	for _, T := range converted.Keys() {
		mset := prog.MethodSets.MethodSet(T)
		for method := range mset.Methods() {
			if names[method.Obj().Name()] {
				addRoot(prog.MethodValue(method), &added)
			}
		}
	}

	for n := range live.pred {
		if n.Func == nil {
			continue
		}

		for _, fn := range scans.of(n.Func).taken {
			addRoot(fn, &addedTaken)
		}
	}

	return added, addedTaken
}

func (ua *UnusedAnalyzer) createSSAPackages(entry *types.Package, includeTests bool, sc *sourceChecker, ssaPkgs map[*types.Package]*ssa.Package, create func(c *checkedPkg, importable bool) *ssa.Package, testPkgs []*ssa.Package) ([]*ssa.Package, error) {
	for pkg := range ua.importClosure(entry, includeTests) {
		c, err := sc.production(pkg)
		if err != nil {
			return testPkgs, fmt.Errorf("build liveness: %w", err)
		}

		if pkg == entry && c.bodiless != "" {
			return testPkgs, &EntryBodilessError{Package: entry.ImportPath, Reason: c.bodiless}
		}

		ssaPkgs[pkg] = create(c, true)

		if !includeTests {
			continue
		}

		t := sc.withTests(pkg)
		if t != nil {
			testPkgs = append(testPkgs, create(t, false))
		}

		x, rebuilt := sc.externalTests(pkg)
		if x != nil {
			testPkgs = append(testPkgs, create(x, false))
		}

		for _, r := range rebuilt {
			create(r, false)
		}
	}

	return testPkgs, nil
}

func entryRoots(mainFn *ssa.Function, ua *UnusedAnalyzer, entry *types.Package, includeTests bool, ssaPkgs map[*types.Package]*ssa.Package, testPkgs []*ssa.Package) []*ssa.Function {
	roots := []*ssa.Function{mainFn}
	for pkg := range ua.importClosure(entry, includeTests) {
		roots = append(roots, ssaPkgs[pkg].Func("init"))
	}

	for _, sp := range testPkgs {
		roots = append(roots, sp.Func("init"))
		roots = append(roots, testFuncs(sp)...)
	}

	slices.SortFunc(roots[1:], func(a, b *ssa.Function) int { return strings.Compare(a.String(), b.String()) })

	return roots
}

// createImports creates bodiless SSA packages for every package the created
// ones import and returns them. created holds the packages already created.
func createImports(prog *ssa.Program, created map[*gotypes.Package]bool) []*gotypes.Package {
	var external []*gotypes.Package
	var walk func(p *gotypes.Package)

	walk = func(p *gotypes.Package) {
		for _, imp := range p.Imports() {
			if created[imp] {
				continue
			}

			created[imp] = true
			prog.CreatePackage(imp, nil, nil, prog.ImportedPackage(imp.Path()) == nil)
			external = append(external, imp)
			walk(imp)
		}
	}
	for _, sp := range prog.AllPackages() {
		walk(sp.Pkg)
	}

	return external
}

// externalMethodNames returns the names of the methods that interfaces
// declared outside the workspace require, error included. Code outside the
// workspace has no SSA bodies, so it may call any method by such a name.
func externalMethodNames(external []*gotypes.Package) map[string]bool {
	names := map[string]bool{"Error": true, "Is": true, "As": true, "Unwrap": true}

	for _, p := range external {
		for _, name := range p.Scope().Names() {
			tn, ok := p.Scope().Lookup(name).(*gotypes.TypeName)
			if !ok {
				continue
			}

			iface, ok := tn.Type().Underlying().(*gotypes.Interface)
			if !ok {
				continue
			}

			for m := range iface.Methods() {
				names[m.Name()] = true
			}
		}
	}

	return names
}

// addReflected adds t to set, and the types reflection reaches from it through
// exported or embedded struct fields and element types, with the pointer to
// each field type. Reflection cannot call methods through other unexported
// fields; method parameters and results are not followed.
func addReflected(set *typeutil.Map, t gotypes.Type) {
	if set.At(t) != nil {
		return
	}

	set.Set(t, true)

	switch u := t.Underlying().(type) {
	case *gotypes.Struct:
		for f := range u.Fields() {
			if !f.Exported() && !f.Embedded() {
				continue
			}

			addReflected(set, f.Type())

			_, isIface := f.Type().Underlying().(*gotypes.Interface)
			if !isIface {
				addReflected(set, gotypes.NewPointer(f.Type()))
			}
		}
	case *gotypes.Pointer:
		addReflected(set, u.Elem())
	case *gotypes.Slice:
		addReflected(set, u.Elem())
	case *gotypes.Array:
		addReflected(set, u.Elem())
	case *gotypes.Map:
		addReflected(set, u.Key())
		addReflected(set, u.Elem())
	}
}

// testFuncs returns the test, benchmark, fuzz and example functions of sp
// declared in test files.
func testFuncs(sp *ssa.Package) []*ssa.Function {
	var fns []*ssa.Function

	for _, m := range sp.Members {
		fn, ok := m.(*ssa.Function)
		if !ok || !strings.HasSuffix(sp.Prog.Fset.Position(fn.Pos()).Filename, "_test.go") {
			continue
		}

		for _, prefix := range []string{"Test", "Benchmark", "Fuzz", "Example"} {
			if strings.HasPrefix(fn.Name(), prefix) {
				fns = append(fns, fn)
				break
			}
		}
	}

	return fns
}

// newLiveness searches cg breadth-first from the roots not in takenOnly,
// following calls and the functions each scanned function takes.
func newLiveness(cg *callgraph.Graph, roots []*ssa.Function, takenOnly map[*ssa.Function]bool, scans scanCache) *liveness {
	l := &liveness{pred: make(map[*callgraph.Node]*callgraph.Node), inits: make(map[string]*callgraph.Node), graph: cg, scans: scans}
	var queue []*callgraph.Node

	for _, r := range roots {
		n := cg.CreateNode(r)
		if !takenOnly[r] && !slices.Contains(queue, n) {
			l.pred[n] = nil
			queue = append(queue, n)
		}
	}

	l.search(queue)

	return l
}

// within returns what the declaration key reaches, over the call graph of l, or
// nil when l does not reach it. The graph, and so the live types, are l's.
//
// RTA links a call through a func value to every function taken as a value in
// the program, so such a call only reaches the functions the search itself takes
// as values; the search is repeated until that set stops growing.
func (l *liveness) within(key types.SymbolKey) *liveness {
	taken := make(map[*ssa.Function]bool)
	for {
		w := l.searchWithin(key, taken)
		if w == nil {
			return nil
		}

		grew := false

		for n := range w.pred {
			if n.Func == nil {
				continue
			}

			for _, fn := range l.scans.of(n.Func).taken {
				if !taken[fn] {
					taken[fn] = true
					grew = true
				}
			}
		}

		if !grew {
			return w
		}
	}
}

func (l *liveness) searchWithin(key types.SymbolKey, taken map[*ssa.Function]bool) *liveness {
	w := &liveness{pred: make(map[*callgraph.Node]*callgraph.Node), inits: make(map[string]*callgraph.Node), bodiless: l.bodiless, graph: l.graph, scans: l.scans, takenHere: taken}
	var queue []*callgraph.Node

	for n := range l.pred {
		if n.Func == nil || n.Func.Parent() != nil || n.Func.Synthetic != "" {
			continue
		}

		fn, ok := n.Func.Object().(*gotypes.Func)
		if !ok {
			continue
		}

		k, ok := types.SymbolKeyOf(fn)
		if ok && k == key {
			w.pred[n] = nil
			queue = append(queue, n)
		}
	}

	if len(queue) == 0 {
		return nil
	}

	w.search(queue)

	return w
}

// isFuncValueCall reports whether e is a call through a func-typed value.
func isFuncValueCall(e *callgraph.Edge) bool {
	if e.Site == nil || e.Callee.Func == nil {
		return false
	}

	c := e.Site.Common()

	return !c.IsInvoke() && c.StaticCallee() == nil
}

// search extends l breadth-first from queue, following calls and the
// functions each scanned function takes.
func (l *liveness) search(queue []*callgraph.Node) {
	cg, scans := l.graph, l.scans
	seenDecl := make(map[token.Pos]bool)

	for len(queue) > 0 {
		n := queue[0]
		queue = queue[1:]

		l.index(n, seenDecl)

		next := make([]*callgraph.Node, 0, len(n.Out))
		for _, e := range n.Out {
			if l.takenHere != nil && isFuncValueCall(e) && !l.takenHere[e.Callee.Func] && !l.takenHere[e.Callee.Func.Origin()] {
				continue
			}

			next = append(next, e.Callee)
		}

		if n.Func != nil {
			for _, fn := range scans.of(n.Func).taken {
				next = append(next, cg.CreateNode(fn))
			}
		}

		for _, c := range next {
			_, seen := l.pred[c]
			if !seen {
				l.pred[c] = n
				queue = append(queue, c)
			}
		}
	}

	slices.SortFunc(l.decls, func(a, b liveDecl) int { return int(a.pos - b.pos) })
}

// funcScan lists what a function does that RTA does not link to a call: the
// functions it takes as values and the concrete types it converts to interfaces.
type funcScan struct {
	taken     []*ssa.Function
	converted []gotypes.Type
}

type scanCache map[*ssa.Function]*funcScan

func (c scanCache) of(fn *ssa.Function) *funcScan {
	sc := c[fn]
	if sc != nil {
		return sc
	}

	sc = &funcScan{}
	c[fn] = sc
	var ops []*ssa.Value

	for _, b := range fn.Blocks {
		for _, instr := range b.Instrs {
			mi, ok := instr.(*ssa.MakeInterface)
			if ok {
				sc.converted = append(sc.converted, mi.X.Type())
			}

			ops = instr.Operands(ops[:0])
			call, _ := instr.(ssa.CallInstruction)

			for _, op := range ops {
				f, ok := (*op).(*ssa.Function)
				if ok && (call == nil || op != &call.Common().Value) {
					sc.taken = append(sc.taken, f)
				}
			}
		}
	}

	return sc
}

// index records n under its top-level declaration, or its package when n is
// a package initializer. n is the first node found for it.
func (l *liveness) index(n *callgraph.Node, seen map[token.Pos]bool) {
	if n.Func == nil {
		return
	}

	top := topFunc(n.Func)
	if top.Synthetic == "package initializer" && top.Pkg != nil {
		if l.inits[top.Pkg.Pkg.Path()] == nil {
			l.inits[top.Pkg.Pkg.Path()] = n
		}

		return
	}

	decl, ok := top.Syntax().(*ast.FuncDecl)
	if ok && !seen[decl.Pos()] {
		seen[decl.Pos()] = true
		l.decls = append(l.decls, liveDecl{decl.Pos(), decl.End(), n})
	}
}

// topFunc returns the declaration holding fn: its generic origin, and for a
// closure the function enclosing it.
func topFunc(fn *ssa.Function) *ssa.Function {
	for {
		o := fn.Origin()
		if o != nil {
			fn = o
		}

		if fn.Parent() == nil {
			return fn
		}

		fn = fn.Parent()
	}
}

// pathTo returns the declarations from a root to the code of owner holding
// pos, or nil when that code is not reachable. Code in a package variable
// initializer is reachable when its package initializer is.
func (l *liveness) pathTo(owner *usageNode, pos token.Pos) []string {
	var node *callgraph.Node

	i, found := slices.BinarySearchFunc(l.decls, pos, func(d liveDecl, p token.Pos) int { return int(d.pos - p) })
	if !found {
		i--
	}

	switch {
	case i >= 0 && pos < l.decls[i].end:
		node = l.decls[i].node
	case isVarOwner(owner):
		node = l.inits[owner.pkg.ImportPath]
	}

	if node == nil {
		return nil
	}
	var path []string

	for ; node != nil; node = l.pred[node] {
		label := declLabel(node.Func)
		if label != "" && (len(path) == 0 || path[len(path)-1] != label) {
			path = append(path, label)
		}
	}

	slices.Reverse(path)

	return path
}

// isVarOwner reports whether n is a package variable. The symbol table has no
// symbol for a test variable sharing a name with another variable.
func isVarOwner(n *usageNode) bool {
	if n.pkg == nil {
		return false
	}

	if n.symbol == nil {
		return n.name != "init" && n.name != "_"
	}

	return n.symbol.Kind == types.VariableSymbol
}

// declLabel is pkg.Recv.Name for the declaration holding fn, pkg.init for a
// package initializer, and "" for functions without source.
func declLabel(fn *ssa.Function) string {
	if fn == nil {
		return ""
	}

	top := topFunc(fn)
	if top.Pkg == nil {
		return ""
	}

	pkg := top.Pkg.Pkg.Name()
	if top.Synthetic == "package initializer" {
		return pkg + ".init"
	}

	decl, ok := top.Syntax().(*ast.FuncDecl)
	if !ok {
		return ""
	}

	if decl.Recv != nil && len(decl.Recv.List) == 1 {
		recv := recvTypeName(decl.Recv.List[0].Type)
		if recv != "" {
			return pkg + "." + recv + "." + decl.Name.Name
		}
	}

	return pkg + "." + decl.Name.Name
}

func recvTypeName(e ast.Expr) string {
	for {
		switch t := e.(type) {
		case *ast.StarExpr:
			e = t.X
		case *ast.ParenExpr:
			e = t.X
		case *ast.IndexExpr:
			e = t.X
		case *ast.IndexListExpr:
			e = t.X
		case *ast.Ident:
			return t.Name
		default:
			return ""
		}
	}
}
