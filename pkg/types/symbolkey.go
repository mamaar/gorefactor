package types

import (
	gotypes "go/types"
	"maps"
)

// SymbolKey identifies a package-level object or a method by value, so one
// declaration can be matched across build-configuration views, whose go/types
// objects are distinct.
type SymbolKey struct {
	PkgPath string
	Recv    string // named receiver or interface type; empty for package-level objects
	Name    string
}

func (k SymbolKey) String() string {
	if k.Recv == "" {
		return k.PkgPath + "." + k.Name
	}
	return k.PkgPath + "." + k.Recv + "." + k.Name
}

// SymbolKeyOf returns obj's key. It reports false for an object without a
// stable one: a local object, a method of an unnamed or local type, init and
// the blank identifier.
func SymbolKeyOf(obj gotypes.Object) (SymbolKey, bool) {
	if obj == nil || obj.Pkg() == nil || obj.Name() == "_" {
		return SymbolKey{}, false
	}
	if fn, ok := obj.(*gotypes.Func); ok {
		if sig, ok := fn.Type().(*gotypes.Signature); ok && sig.Recv() != nil {
			recv, ok := packageLevelTypeName(sig.Recv().Type())
			if !ok {
				return SymbolKey{}, false
			}
			return SymbolKey{PkgPath: obj.Pkg().Path(), Recv: recv, Name: obj.Name()}, true
		}
		if obj.Name() == "init" {
			return SymbolKey{}, false
		}
	}
	if obj.Parent() != obj.Pkg().Scope() {
		return SymbolKey{}, false
	}
	return SymbolKey{PkgPath: obj.Pkg().Path(), Name: obj.Name()}, true
}

// packageLevelTypeName returns the name of the package-level named type t is,
// or points to.
func packageLevelTypeName(t gotypes.Type) (string, bool) {
	t = gotypes.Unalias(t)
	if ptr, ok := t.(*gotypes.Pointer); ok {
		t = gotypes.Unalias(ptr.Elem())
	}
	named, ok := t.(*gotypes.Named)
	if !ok {
		return "", false
	}
	obj := named.Obj()
	if obj.Pkg() == nil || obj.Parent() != obj.Pkg().Scope() {
		return "", false
	}
	return obj.Name(), true
}

// SymbolKeySet is a set of symbol keys.
type SymbolKeySet map[SymbolKey]struct{}

func (s SymbolKeySet) Has(k SymbolKey) bool {
	_, ok := s[k]
	return ok
}

// AddAll adds other's keys and reports whether the set grew.
func (s SymbolKeySet) AddAll(other SymbolKeySet) bool {
	before := len(s)
	maps.Copy(s, other)
	return len(s) > before
}

// ContainsAll reports whether every key of other is in s.
func (s SymbolKeySet) ContainsAll(other SymbolKeySet) bool {
	for k := range other {
		if !s.Has(k) {
			return false
		}
	}
	return true
}

// Clone returns an independent copy.
func (s SymbolKeySet) Clone() SymbolKeySet {
	out := make(SymbolKeySet, len(s))
	maps.Copy(out, s)
	return out
}
