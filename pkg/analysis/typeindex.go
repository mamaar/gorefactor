package analysis

import (
	"encoding/binary"
	"go/ast"
	"go/token"
	gotypes "go/types"
	"iter"

	"golang.org/x/tools/go/ast/edge"
	"golang.org/x/tools/go/ast/inspector"

	"github.com/mamaar/gorefactor/pkg/types"
)

// uses is a varint-compressed list of inspector.Cursor index values.
// Each entry is stored as a delta from the previous value, achieving ~2 bytes
// per reference vs ~24 bytes for objectEntry. An inline [4]byte buffer avoids
// heap allocation for symbols with 1–2 uses.
type uses struct {
	code    []byte
	last    int32
	size    int     // bytes used (tracks inline vs heap)
	initial [4]byte // inline storage for the first few bytes
}

func (u *uses) add(idx int32) {
	delta := idx - u.last
	u.last = idx

	var buf [binary.MaxVarintLen32]byte
	n := binary.PutVarint(buf[:], int64(delta))

	needed := u.size + n
	if needed <= len(u.initial) {
		// Fits in inline storage — no heap allocation
		copy(u.initial[u.size:], buf[:n])
		u.size += n
		u.code = u.initial[:u.size]
	} else {
		if u.code == nil || (len(u.code) > 0 && &u.code[0] == &u.initial[0]) {
			// Spill from inline to heap
			heap := make([]byte, u.size, u.size+n+16)
			copy(heap, u.initial[:u.size])
			u.code = heap
		}

		u.code = append(u.code, buf[:n]...)
		u.size = len(u.code)
	}
}

// decode iterates over all stored cursor indices.
func (u *uses) decode() iter.Seq[int32] {
	return func(yield func(int32) bool) {
		data := u.code
		var val int32

		for len(data) > 0 {
			delta, n := binary.Varint(data)
			if n <= 0 {
				return
			}

			data = data[n:]

			val += int32(delta)
			if !yield(val) {
				return
			}
		}
	}
}

func newUses() *uses {
	return &uses{}
}

// packageIndex is a per-package compressed index of symbol definitions and uses,
// built from a single inspector pass over the package's AST files.
type packageIndex struct {
	inspect *inspector.Inspector
	info    *gotypes.Info
	pkg     *gotypes.Package
	def     map[ObjectID]inspector.Cursor
	uses    map[ObjectID]*uses
}

func newPackageIndex(inspect *inspector.Inspector, pkg *gotypes.Package, info *gotypes.Info) *packageIndex {
	ix := &packageIndex{
		inspect: inspect,
		info:    info,
		pkg:     pkg,
		def:     make(map[ObjectID]inspector.Cursor),
		uses:    make(map[ObjectID]*uses),
	}

	for cur := range inspect.Root().Preorder((*ast.ImportSpec)(nil), (*ast.Ident)(nil)) {
		switch cur.Node().(type) {
		case *ast.ImportSpec:
			// Skip import specs — they create noise in the index
			continue
		case *ast.Ident:
			// handled below
		default:
			continue
		}

		ident := cur.Node().(*ast.Ident)

		// Check if this ident is a definition
		obj, ok := info.Defs[ident]
		if ok && obj != nil {
			ix.def[ObjectIDOf(obj)] = cur
			continue
		}

		// Check if this ident is a use
		obj, ok = info.Uses[ident]
		if !ok || obj == nil {
			continue
		}

		id := ObjectIDOf(obj)

		u := ix.uses[id]
		if u == nil {
			u = newUses()
			ix.uses[id] = u
		}

		u.add(cur.Index())
	}

	return ix
}

// Uses returns an iterator over all cursors that reference obj in this package.
func (ix *packageIndex) Uses(obj gotypes.Object) iter.Seq[inspector.Cursor] {
	return func(yield func(inspector.Cursor) bool) {
		u := ix.uses[ObjectIDOf(obj)]
		if u == nil {
			return
		}

		for idx := range u.decode() {
			cur := ix.inspect.At(idx)
			if !yield(cur) {
				return
			}
		}
	}
}

// Def returns the cursor for the definition of obj in this package, if any.
func (ix *packageIndex) Def(obj gotypes.Object) (inspector.Cursor, bool) {
	cur, ok := ix.def[ObjectIDOf(obj)]
	return cur, ok
}

// Calls returns an iterator over call sites that invoke callee within this package.
func (ix *packageIndex) Calls(callee gotypes.Object) iter.Seq[inspector.Cursor] {
	return func(yield func(inspector.Cursor) bool) {
		u := ix.uses[ObjectIDOf(callee)]
		if u == nil {
			return
		}

		for idx := range u.decode() {
			call, ok := callSiteFor(ix.inspect.At(idx))
			if !ok {
				continue
			}

			if !yield(call) {
				return
			}
		}
	}
}

// maxCallWrapDepth bounds the walk in callSiteFor. The longest legal chain between
// an ident and its CallExpr is Ident → SelectorExpr → IndexListExpr → CallExpr, so
// three steps suffice; the extra one is slack against a malformed tree looping.
const maxCallWrapDepth = 4

// callSiteFor walks up from a use of a symbol to the CallExpr that invokes it, and
// reports false when the use is not in callee position at all.
//
// The walk keys on cur.ParentEdgeKind(), which names the edge FROM the parent TO cur —
// not the edge above the parent. Reading that direction backwards is what made an
// earlier version miss every direct call: for f() the Ident f already carries
// edge.CallExpr_Fun and its parent IS the CallExpr, so asking the parent for its own
// parent edge yields whatever encloses the call (edge.ExprStmt_X, ...) and never matches.
//
// Only edges that keep the symbol in callee position are followed: the .Sel of a
// selector (x.f(), pkg.F()) and the .X of an index expression (generic instantiation
// F[int](), F[int, string]()). These compose, e.g. pkg.F[int](). Deciding on edge kind
// rather than node type is what distinguishes those from SelectorExpr_X, where the
// symbol is the receiver in f.Method() — a use of f, not a call of it.
func callSiteFor(cur inspector.Cursor) (inspector.Cursor, bool) {
	for range maxCallWrapDepth {
		parent := cur.Parent()
		if !parent.Valid() {
			return inspector.Cursor{}, false
		}

		switch cur.ParentEdgeKind() {
		case edge.CallExpr_Fun:
			return parent, true
		case edge.SelectorExpr_Sel, edge.IndexExpr_X, edge.IndexListExpr_X:
			cur = parent
		default:
			return inspector.Cursor{}, false
		}
	}

	return inspector.Cursor{}, false
}

// workspaceIndex aggregates per-package indexes across the workspace.
type workspaceIndex struct {
	packages    map[*gotypes.Package]*packageIndex
	fileSet     *token.FileSet
	tokenToFile map[*token.File]*types.File
}

func newWorkspaceIndex(fset *token.FileSet) *workspaceIndex {
	return &workspaceIndex{
		packages:    make(map[*gotypes.Package]*packageIndex),
		fileSet:     fset,
		tokenToFile: make(map[*token.File]*types.File),
	}
}

// findReferences returns all objectEntry occurrences (defs and uses) for obj
// across the entire workspace.
func (wi *workspaceIndex) findReferences(obj gotypes.Object) []objectEntry {
	if len(wi.packages) == 0 {
		return nil
	}

	var entries []objectEntry

	for _, pix := range wi.packages {
		// Check definition
		cur, ok := pix.Def(obj)
		if ok {
			entry, ok := wi.cursorToEntry(cur, true)
			if ok {
				entries = append(entries, entry)
			}
		}

		// Check uses
		for cur := range pix.Uses(obj) {
			entry, ok := wi.cursorToEntry(cur, false)
			if ok {
				entries = append(entries, entry)
			}
		}
	}

	return entries
}

// cursorToEntry converts a cursor to an objectEntry by resolving its position
// to a *types.File via the token file mapping.
func (wi *workspaceIndex) cursorToEntry(cur inspector.Cursor, isDecl bool) (objectEntry, bool) {
	node := cur.Node()
	if node == nil {
		return objectEntry{}, false
	}

	pos := node.Pos()

	tf := wi.fileSet.File(pos)
	if tf == nil {
		return objectEntry{}, false
	}

	file := wi.tokenToFile[tf]
	if file == nil {
		return objectEntry{}, false
	}

	return objectEntry{
		File:          file,
		Pos:           pos,
		IsDeclaration: isDecl,
	}, true
}

// canonicalObject returns the canonical (origin) form of a types.Object,
// handling generic type instantiations. For *types.Func with a receiver,
// returns obj.Origin() if different. For *types.Var that IsField(),
// returns obj.Origin() if different.
func canonicalObject(obj gotypes.Object) gotypes.Object {
	switch o := obj.(type) {
	case *gotypes.Func:
		orig := o.Origin()
		if orig != o {
			return orig
		}
	case *gotypes.Var:
		if o.IsField() {
			orig := o.Origin()
			if orig != o {
				return orig
			}
		}
	}

	return obj
}

// ObjectID is the identity of a declared object: its declaration position, so
// that the copy of a declaration a production-only rescue package type-checks
// compares equal to the original. Objects without a position keep pointer identity.
type ObjectID struct {
	obj gotypes.Object
	pos token.Pos
	pkg string
}

// ObjectIDOf returns the identity of obj, a generic instantiation counting as
// its origin.
func ObjectIDOf(obj gotypes.Object) ObjectID {
	obj = canonicalObject(obj)
	if obj == nil || !obj.Pos().IsValid() {
		return ObjectID{obj: obj}
	}

	id := ObjectID{pos: obj.Pos()}
	if obj.Pkg() != nil {
		id.pkg = obj.Pkg().Path()
	}

	return id
}

// sameObject reports whether a and b are the same declared object.
func sameObject(a, b gotypes.Object) bool {
	if a == nil || b == nil {
		return false
	}

	return ObjectIDOf(a) == ObjectIDOf(b)
}
