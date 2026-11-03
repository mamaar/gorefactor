package analysis

import (
	"go/ast"
	"go/token"
	gotypes "go/types"

	"github.com/mamaar/gorefactor/pkg/types"
)

// InterfaceConversions records the methods non-test code can reach through an
// interface value: those a concrete type gets converted to an interface that
// declares them.
type InterfaceConversions struct {
	used map[token.Pos]bool
}

// KeepsMethod reports whether the method declared at pos is reachable through
// an interface.
func (c *InterfaceConversions) KeepsMethod(pos token.Pos) bool { return c.used[pos] }

// CollectInterfaceConversions finds every place a type-checked, non-test
// expression of a concrete type becomes an interface value: assignments, call
// arguments, returns, composite literal elements, explicit conversions, channel
// sends, map keys and arguments passed for a type parameter.
//
// Ceiling: a conversion to any, or to an interface from outside the workspace,
// keeps the methods named like those of any known interface (callers may assert
// them, as fmt does for Stringer), following fields and elements. An
// interface value passed on that way keeps what was converted to its interface
// the same way, one hop deep. A converted type also keeps the methods of every
// interface the workspace asserts to that it implements.
func CollectInterfaceConversions(ws *types.Workspace) *InterfaceConversions {
	b := &conversionCollector{
		ws:          ws,
		names:       interfaceMethodNames(ws),
		inWorkspace: make(map[string]bool),
		used:        make(map[token.Pos]bool),
	}
	for _, pkg := range ws.Packages {
		if pkg.TypesPkg != nil {
			b.inWorkspace[pkg.TypesPkg.Path()] = true
		}
	}

	for _, pkg := range ws.Packages {
		if pkg.TypesInfo != nil {
			b.collectPackage(pkg)
		}
	}

	b.resolve()

	return &InterfaceConversions{used: b.used}
}

type conversion struct{ src, dst gotypes.Type }

type conversionCollector struct {
	ws          *types.Workspace
	names       map[string]bool
	inWorkspace map[string]bool
	info        *gotypes.Info
	convs       []conversion
	asserted    []*gotypes.Interface
	used        map[token.Pos]bool
}

func (b *conversionCollector) collectPackage(pkg *types.Package) {
	b.info = pkg.TypesInfo
	for _, file := range pkg.Files {
		if file.AST == nil {
			continue
		}

		for _, decl := range file.AST.Decls {
			fn, ok := decl.(*ast.FuncDecl)
			if !ok || fn.Body == nil {
				b.walk(decl, nil)
				continue
			}

			b.walk(fn.Body, resultsOf(b.info.TypeOf(fn.Name)))
		}
	}
}

func resultsOf(t gotypes.Type) *gotypes.Tuple {
	sig, ok := underlying(t).(*gotypes.Signature)
	if !ok {
		return nil
	}

	return sig.Results()
}

// walk records the conversions under root; results are those of the enclosing function.
func (b *conversionCollector) walk(root ast.Node, results *gotypes.Tuple) {
	ast.Inspect(root, func(n ast.Node) bool {
		switch n := n.(type) {
		case *ast.FuncLit:
			b.walk(n.Body, resultsOf(b.info.TypeOf(n)))
			return false
		case *ast.AssignStmt:
			if n.Tok == token.ASSIGN && len(n.Lhs) == len(n.Rhs) {
				for i, rhs := range n.Rhs {
					b.add(b.info.TypeOf(rhs), b.info.TypeOf(n.Lhs[i]))
				}
			}
		case *ast.ValueSpec:
			if n.Type != nil {
				for _, v := range n.Values {
					b.add(b.info.TypeOf(v), b.info.TypeOf(n.Type))
				}
			}
		case *ast.CallExpr:
			b.call(n)
		case *ast.ReturnStmt:
			if results != nil && results.Len() == len(n.Results) {
				for i, r := range n.Results {
					b.add(b.info.TypeOf(r), results.At(i).Type())
				}
			}
		case *ast.CompositeLit:
			b.compositeLit(n)
		case *ast.SendStmt:
			ch, ok := underlying(b.info.TypeOf(n.Chan)).(*gotypes.Chan)
			if ok {
				b.add(b.info.TypeOf(n.Value), ch.Elem())
			}
		case *ast.TypeAssertExpr:
			b.assert(n.Type)
		case *ast.CaseClause:
			for _, e := range n.List {
				b.assert(e)
			}
		case *ast.IndexExpr:
			m, ok := underlying(b.info.TypeOf(n.X)).(*gotypes.Map)
			if ok {
				b.add(b.info.TypeOf(n.Index), m.Key())
			}
		}

		return true
	})
}

// assert records the interface a type assertion or type switch case names.
func (b *conversionCollector) assert(typ ast.Expr) {
	t := b.info.TypeOf(typ)
	if t != nil && gotypes.IsInterface(t) {
		b.asserted = append(b.asserted, t.Underlying().(*gotypes.Interface))
	}
}

func underlying(t gotypes.Type) gotypes.Type {
	if t == nil {
		return nil
	}

	return t.Underlying()
}

func (b *conversionCollector) call(call *ast.CallExpr) {
	tv := b.info.Types[call.Fun]
	if tv.IsType() {
		if len(call.Args) == 1 {
			b.add(b.info.TypeOf(call.Args[0]), tv.Type)
		}

		return
	}

	sig, ok := underlying(tv.Type).(*gotypes.Signature)
	if !ok {
		return
	}
	// A generic callee's declared parameters name the type parameters, whose
	// constraints the arguments are converted to; the instantiated ones do not.
	generic := b.genericCallee(call.Fun)
	if generic != nil {
		sig = generic
	}

	params := sig.Params()

	for i, arg := range call.Args {
		var dst gotypes.Type

		switch {
		case sig.Variadic() && i >= params.Len()-1:
			slice, ok := params.At(params.Len() - 1).Type().(*gotypes.Slice)
			if ok && !call.Ellipsis.IsValid() {
				dst = slice.Elem()
			}
		case i < params.Len():
			dst = params.At(i).Type()
		}

		b.add(b.info.TypeOf(arg), dst)
	}
}

// genericCallee returns the declared signature of the generic function fun
// calls, and records explicit type arguments against its constraints.
func (b *conversionCollector) genericCallee(fun ast.Expr) *gotypes.Signature {
	var typeArgs []ast.Expr

	switch f := fun.(type) {
	case *ast.IndexExpr:
		fun, typeArgs = f.X, []ast.Expr{f.Index}
	case *ast.IndexListExpr:
		fun, typeArgs = f.X, f.Indices
	}
	var id *ast.Ident

	switch f := fun.(type) {
	case *ast.Ident:
		id = f
	case *ast.SelectorExpr:
		id = f.Sel
	default:
		return nil
	}

	callee, ok := b.info.Uses[id].(*gotypes.Func)
	if !ok {
		return nil
	}

	sig, ok := callee.Type().(*gotypes.Signature)
	if !ok || sig.TypeParams().Len() == 0 {
		return nil
	}

	for i, arg := range typeArgs {
		if i < sig.TypeParams().Len() {
			b.add(b.info.TypeOf(arg), sig.TypeParams().At(i).Constraint())
		}
	}

	return sig
}

func (b *conversionCollector) compositeLit(lit *ast.CompositeLit) {
	t := b.info.TypeOf(lit)

	ptr, ok := underlying(t).(*gotypes.Pointer)
	if ok {
		t = ptr.Elem()
	}

	switch u := underlying(t).(type) {
	case *gotypes.Struct:
		for i, elt := range lit.Elts {
			field, value := structElement(u, i, elt)
			if field != nil {
				b.add(b.info.TypeOf(value), field.Type())
			}
		}
	case *gotypes.Slice:
		b.addElements(lit, u.Elem())
	case *gotypes.Array:
		b.addElements(lit, u.Elem())
	case *gotypes.Map:
		for _, elt := range lit.Elts {
			kv, ok := elt.(*ast.KeyValueExpr)
			if ok {
				b.add(b.info.TypeOf(kv.Key), u.Key())
				b.add(b.info.TypeOf(kv.Value), u.Elem())
			}
		}
	}
}

func (b *conversionCollector) addElements(lit *ast.CompositeLit, elem gotypes.Type) {
	for _, elt := range lit.Elts {
		kv, ok := elt.(*ast.KeyValueExpr)
		if ok {
			elt = kv.Value
		}

		b.add(b.info.TypeOf(elt), elem)
	}
}

// structElement returns the field of s that the i-th literal element sets, and its value.
func structElement(s *gotypes.Struct, i int, elt ast.Expr) (*gotypes.Var, ast.Expr) {
	kv, ok := elt.(*ast.KeyValueExpr)
	if !ok {
		if i < s.NumFields() {
			return s.Field(i), elt
		}

		return nil, nil
	}

	key, ok := kv.Key.(*ast.Ident)
	if !ok {
		return nil, nil
	}

	for f := range s.Fields() {
		if f.Name() == key.Name {
			return f, kv.Value
		}
	}

	return nil, nil
}

func (b *conversionCollector) add(src, dst gotypes.Type) {
	if src == nil || dst == nil {
		return
	}

	_, ok := src.(*gotypes.Tuple)
	if ok {
		return
	}

	_, ok = dst.Underlying().(*gotypes.Interface)
	if ok {
		b.convs = append(b.convs, conversion{src, dst})
	}
}

// broad reports whether dst is any or an interface from outside the workspace.
func (b *conversionCollector) broad(dst gotypes.Type) bool {
	if dst.Underlying().(*gotypes.Interface).NumMethods() == 0 {
		return true
	}

	named, ok := gotypes.Unalias(dst).(*gotypes.Named)

	return ok && (named.Obj().Pkg() == nil || !b.inWorkspace[named.Obj().Pkg().Path()])
}

// resolve turns the collected conversions into used methods.
func (b *conversionCollector) resolve() {
	var escaped []gotypes.Type

	for _, c := range b.convs {
		if gotypes.IsInterface(c.src) && b.broad(c.dst) {
			escaped = append(escaped, c.src)
		}
	}

	for _, c := range b.convs {
		if gotypes.IsInterface(c.src) {
			continue
		}

		b.keepDeclared(c.src, c.dst.Underlying().(*gotypes.Interface))

		for _, iface := range b.asserted {
			if gotypes.Implements(c.src, iface) {
				b.keepDeclared(c.src, iface)
			}
		}

		if b.broad(c.dst) || b.escapes(escaped, c.dst) {
			b.keepKnown(c.src, make(map[gotypes.Type]bool))
		}
	}
}

func (b *conversionCollector) escapes(escaped []gotypes.Type, dst gotypes.Type) bool {
	for _, e := range escaped {
		if gotypes.Identical(e, dst) {
			return true
		}
	}

	return false
}

// keepDeclared keeps the methods of src that iface declares, embedded ones included.
func (b *conversionCollector) keepDeclared(src gotypes.Type, iface *gotypes.Interface) {
	ms := gotypes.NewMethodSet(src)
	for m := range iface.Methods() {
		sel := ms.Lookup(m.Pkg(), m.Name())
		if sel != nil {
			b.keep(sel.Obj())
		}
	}
}

// keepKnown keeps the methods of t, and of the types it holds, whose name a
// known interface declares.
func (b *conversionCollector) keepKnown(t gotypes.Type, seen map[gotypes.Type]bool) {
	if seen[t] {
		return
	}

	seen[t] = true

	ms := gotypes.NewMethodSet(t)
	for method := range ms.Methods() {
		if b.names[method.Obj().Name()] {
			b.keep(method.Obj())
		}
	}

	switch u := t.Underlying().(type) {
	case *gotypes.Pointer:
		b.keepKnown(u.Elem(), seen)
	case *gotypes.Slice:
		b.keepKnown(u.Elem(), seen)
	case *gotypes.Array:
		b.keepKnown(u.Elem(), seen)
	case *gotypes.Chan:
		b.keepKnown(u.Elem(), seen)
	case *gotypes.Map:
		b.keepKnown(u.Key(), seen)
		b.keepKnown(u.Elem(), seen)
	case *gotypes.Struct:
		for f := range u.Fields() {
			b.keepKnown(f.Type(), seen)
		}
	}
}

func (b *conversionCollector) keep(obj gotypes.Object) {
	f, ok := obj.(*gotypes.Func)
	if ok {
		b.used[f.Origin().Pos()] = true
	}
}
