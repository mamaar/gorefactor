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
}

type liveDecl struct {
	pos, end token.Pos
	node     *callgraph.Node // the first reachable function of the declaration
}

// buildLiveness runs rapid type analysis from main of entry, the initializers
// of the packages it imports, and with includeTests their test functions.
func (ua *UnusedAnalyzer) buildLiveness(entry *types.Package, includeTests bool) (live *liveness, err error) {
	defer func() {
		if r := recover(); r != nil {
			err = fmt.Errorf("analysing %s: %v", entry.Path, r)
		}
	}()

	prog := ssa.NewProgram(ua.workspace.FileSet, ssa.InstantiateGenerics)
	sc := newSourceChecker(ua.workspace)
	ssaPkgs := make(map[*types.Package]*ssa.Package)
	var testPkgs []*ssa.Package
	created := make(map[*gotypes.Package]bool)
	create := func(c *checkedPkg, importable bool) *ssa.Package {
		created[c.pkg] = true
		return prog.CreatePackage(c.pkg, c.files, c.info, importable)
	}
	for pkg := range ua.importClosure(entry, includeTests) {
		c, err := sc.production(pkg)
		if err != nil {
			return nil, err
		}
		if pkg == entry && c.bodiless != "" {
			return nil, &EntryBodilessError{Package: entry.ImportPath, Reason: c.bodiless}
		}
		ssaPkgs[pkg] = create(c, true)
		if !includeTests {
			continue
		}
		if t := sc.withTests(pkg); t != nil {
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
	external := createImports(prog, created)
	prog.Build()

	mainFn := ssaPkgs[entry].Func("main")
	if mainFn == nil {
		return nil, fmt.Errorf("entry package %s has no func main", entry.Path)
	}
	roots := []*ssa.Function{mainFn}
	for pkg := range ua.importClosure(entry, includeTests) {
		roots = append(roots, ssaPkgs[pkg].Func("init"))
	}
	for _, sp := range testPkgs {
		roots = append(roots, sp.Func("init"))
		roots = append(roots, testFuncs(sp)...)
	}
	slices.SortFunc(roots[1:], func(a, b *ssa.Function) int { return strings.Compare(a.String(), b.String()) })

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
				converted.Set(T, true)
			}
		}
		for _, T := range converted.Keys() {
			mset := prog.MethodSets.MethodSet(T)
			for i := range mset.Len() {
				if names[mset.At(i).Obj().Name()] {
					addRoot(prog.MethodValue(mset.At(i)), &added)
				}
			}
		}
		for n := range live.pred {
			if n.Func != nil {
				for _, fn := range scans.of(n.Func).taken {
					addRoot(fn, &addedTaken)
				}
			}
		}
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

// createImports creates bodiless SSA packages for every package the created
// ones import and returns them. created holds the packages already created.
func createImports(prog *ssa.Program, created map[*gotypes.Package]bool) []*gotypes.Package {
	var external []*gotypes.Package
	var walk func(p *gotypes.Package)
	walk = func(p *gotypes.Package) {
		for _, imp := range p.Imports() {
			if !created[imp] {
				created[imp] = true
				prog.CreatePackage(imp, nil, nil, prog.ImportedPackage(imp.Path()) == nil)
				external = append(external, imp)
				walk(imp)
			}
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
			if iface, ok := tn.Type().Underlying().(*gotypes.Interface); ok {
				for m := range iface.Methods() {
					names[m.Name()] = true
				}
			}
		}
	}
	return names
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
	l := &liveness{pred: make(map[*callgraph.Node]*callgraph.Node), inits: make(map[string]*callgraph.Node)}
	seenDecl := make(map[token.Pos]bool)
	var queue []*callgraph.Node
	for _, r := range roots {
		if n := cg.CreateNode(r); !takenOnly[r] && !slices.Contains(queue, n) {
			l.pred[n] = nil
			queue = append(queue, n)
		}
	}
	for len(queue) > 0 {
		n := queue[0]
		queue = queue[1:]
		l.index(n, seenDecl)
		next := make([]*callgraph.Node, 0, len(n.Out))
		for _, e := range n.Out {
			next = append(next, e.Callee)
		}
		if n.Func != nil {
			for _, fn := range scans.of(n.Func).taken {
				next = append(next, cg.CreateNode(fn))
			}
		}
		for _, c := range next {
			if _, seen := l.pred[c]; !seen {
				l.pred[c] = n
				queue = append(queue, c)
			}
		}
	}
	slices.SortFunc(l.decls, func(a, b liveDecl) int { return int(a.pos - b.pos) })
	return l
}

// funcScan lists what a function does that RTA does not link to a call: the
// functions it uses as values, and the concrete types it converts to
// interfaces. RTA reaches a taken function only through a call of its
// signature, but taking it makes it reachable.
type funcScan struct {
	taken     []*ssa.Function
	converted []gotypes.Type
}

type scanCache map[*ssa.Function]*funcScan

func (c scanCache) of(fn *ssa.Function) *funcScan {
	if sc := c[fn]; sc != nil {
		return sc
	}
	sc := &funcScan{}
	c[fn] = sc
	var ops []*ssa.Value
	for _, b := range fn.Blocks {
		for _, instr := range b.Instrs {
			if mi, ok := instr.(*ssa.MakeInterface); ok {
				sc.converted = append(sc.converted, mi.X.Type())
			}
			ops = instr.Operands(ops[:0])
			call, _ := instr.(ssa.CallInstruction)
			for _, op := range ops {
				if f, ok := (*op).(*ssa.Function); ok && (call == nil || op != &call.Common().Value) {
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
	if decl, ok := top.Syntax().(*ast.FuncDecl); ok && !seen[decl.Pos()] {
		seen[decl.Pos()] = true
		l.decls = append(l.decls, liveDecl{decl.Pos(), decl.End(), n})
	}
}

// topFunc returns the declaration holding fn: its generic origin, and for a
// closure the function enclosing it.
func topFunc(fn *ssa.Function) *ssa.Function {
	for {
		if o := fn.Origin(); o != nil {
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
		if recv := recvTypeName(decl.Recv.List[0].Type); recv != "" {
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
