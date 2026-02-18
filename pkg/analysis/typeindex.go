package analysis

import (
	"encoding/binary"
	"go/ast"
	"go/token"
	gotypes "go/types"
	"iter"

	"github.com/mamaar/gorefactor/pkg/types"
	"golang.org/x/tools/go/ast/edge"
	"golang.org/x/tools/go/ast/inspector"
	"golang.org/x/tools/go/types/typeutil"
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
	def     map[gotypes.Object]inspector.Cursor
	uses    map[gotypes.Object]*uses
}

func newPackageIndex(inspect *inspector.Inspector, pkg *gotypes.Package, info *gotypes.Info) *packageIndex {
	ix := &packageIndex{
		inspect: inspect,
		info:    info,
		pkg:     pkg,
		def:     make(map[gotypes.Object]inspector.Cursor),
		uses:    make(map[gotypes.Object]*uses),
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
		if obj, ok := info.Defs[ident]; ok && obj != nil {
			obj = canonicalObject(obj)
			ix.def[obj] = cur
			continue
		}

		// Check if this ident is a use
		if obj, ok := info.Uses[ident]; ok && obj != nil {
			obj = canonicalObject(obj)
			u := ix.uses[obj]
			if u == nil {
				u = newUses()
				ix.uses[obj] = u
			}
			u.add(cur.Index())
		}
	}

	return ix
}

// Uses returns an iterator over all cursors that reference obj in this package.
func (ix *packageIndex) Uses(obj gotypes.Object) iter.Seq[inspector.Cursor] {
	return func(yield func(inspector.Cursor) bool) {
		u := ix.uses[obj]
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

// Used reports whether any of the given objects are referenced in this package.
func (ix *packageIndex) Used(objs ...gotypes.Object) bool {
	for _, obj := range objs {
		if u := ix.uses[obj]; u != nil && len(u.code) > 0 {
			return true
		}
	}
	return false
}

// Def returns the cursor for the definition of obj in this package, if any.
func (ix *packageIndex) Def(obj gotypes.Object) (inspector.Cursor, bool) {
	cur, ok := ix.def[obj]
	return cur, ok
}

// Calls returns an iterator over call sites that invoke callee within this package.
func (ix *packageIndex) Calls(callee gotypes.Object) iter.Seq[inspector.Cursor] {
	return func(yield func(inspector.Cursor) bool) {
		u := ix.uses[callee]
		if u == nil {
			return
		}
		for idx := range u.decode() {
			cur := ix.inspect.At(idx)
			// Walk up to find if this ident is within a CallExpr
			// The ident might be the function name in CallExpr.Fun
			// or the Sel in a SelectorExpr that is CallExpr.Fun
			parent := cur.Parent()
			if !parent.Valid() {
				continue
			}
			switch parent.ParentEdgeKind() {
			case edge.CallExpr_Fun:
				// Direct call: f()
				if !yield(parent.Parent()) {
					return
				}
			default:
				// Check for selector call: x.f()
				if sel, ok := parent.Node().(*ast.SelectorExpr); ok && sel.Sel == cur.Node().(*ast.Ident) {
					gp := parent.Parent()
					if gp.Valid() {
						if _, ok := gp.Node().(*ast.CallExpr); ok {
							if !yield(gp) {
								return
							}
						}
					}
				}
			}
		}
	}
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
	if wi == nil || len(wi.packages) == 0 {
		return nil
	}

	obj = canonicalObject(obj)
	var entries []objectEntry

	for _, pix := range wi.packages {
		// Check definition
		if cur, ok := pix.Def(obj); ok {
			if entry, ok := wi.cursorToEntry(cur, true); ok {
				entries = append(entries, entry)
			}
		}

		// Check uses
		for cur := range pix.Uses(obj) {
			if entry, ok := wi.cursorToEntry(cur, false); ok {
				entries = append(entries, entry)
			}
		}
	}

	return entries
}

// hasNonDeclarationReference reports whether obj has at least one non-declaration
// reference in the workspace (early-exit for unused detection).
func (wi *workspaceIndex) hasNonDeclarationReference(obj gotypes.Object) bool {
	if wi == nil || len(wi.packages) == 0 {
		return false
	}

	obj = canonicalObject(obj)

	for _, pix := range wi.packages {
		for range pix.Uses(obj) {
			return true
		}
	}
	return false
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
		if orig := o.Origin(); orig != o {
			return orig
		}
	case *gotypes.Var:
		if o.IsField() {
			if orig := o.Origin(); orig != o {
				return orig
			}
		}
	}
	return obj
}

// isPackageLevel reports whether obj is a package-level symbol.
func isPackageLevel(obj gotypes.Object) bool {
	return obj.Pkg() != nil && obj.Parent() == obj.Pkg().Scope()
}

// Ensure typeutil.Callee is available (used by Calls method).
var _ = typeutil.Callee
