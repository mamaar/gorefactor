package analysis

import (
	gotypes "go/types"
)

// namedOf resolves t to its *gotypes.Named, unwrapping aliases first
// (type Ctx = context.Context resolves to the aliased Named, not a
// distinct Alias object) and reports ok=false for nil, non-Named types
// (interfaces, basic types like error's underlying, etc.), or Named types
// whose Obj().Pkg() is nil (universe scope, e.g. error, any).
func namedOf(t gotypes.Type) (named *gotypes.Named, pkgPath, name string, ok bool) {
	if t == nil {
		return nil, "", "", false
	}
	named, ok = gotypes.Unalias(t).(*gotypes.Named)
	if !ok {
		return nil, "", "", false
	}
	obj := named.Obj()
	if obj == nil {
		return named, "", "", false
	}
	pkg := obj.Pkg()
	if pkg == nil {
		// Universe scope (error, any, comparable, ...) has no package;
		// treat as "does not match any concrete package type".
		return named, "", obj.Name(), false
	}
	return named, pkg.Path(), obj.Name(), true
}

// isNamed reports whether t (after unwrapping aliases) is the named type
// wantPkgPath.wantName, resolved by package path and type name rather than
// by any source-level identifier.
func isNamed(t gotypes.Type, wantPkgPath, wantName string) bool {
	_, pkgPath, name, ok := namedOf(t)
	if !ok {
		return false
	}
	return pkgPath == wantPkgPath && name == wantName
}

// isPointerToNamed reports whether t is a pointer whose element type is the
// named type wantPkgPath.wantName.
func isPointerToNamed(t gotypes.Type, wantPkgPath, wantName string) bool {
	if t == nil {
		return false
	}
	ptr, ok := gotypes.Unalias(t).(*gotypes.Pointer)
	if !ok {
		return false
	}
	return isNamed(ptr.Elem(), wantPkgPath, wantName)
}

// IsContextType reports whether t is the context.Context interface type
// itself, resolved by package path and name rather than by matching the
// "context" identifier in source. Matching by identifier breaks under
// import renaming (import ctxpkg "context") and false-positives on any
// locally declared type named Context in a package that happens to be
// named context; resolving through go/types' Obj().Pkg().Path() avoids
// both.
//
// This intentionally checks for the concrete context.Context type, not
// merely a type that structurally implements the context.Context
// interface (i.e. no gotypes.Implements / gotypes.AssignableTo check).
// Threading logic needs to know it is dealing with the real
// context.Context, not an unrelated interface that happens to share its
// method set.
func IsContextType(t gotypes.Type) bool {
	return isNamed(t, "context", "Context")
}

// IsHTTPRequestPtr reports whether t is *net/http.Request.
func IsHTTPRequestPtr(t gotypes.Type) bool {
	return isPointerToNamed(t, "net/http", "Request")
}

// IsTestingTB reports whether t is *testing.T, *testing.B, *testing.F, or the
// testing.TB interface. Note *testing.M (the test-binary entry point) is
// deliberately excluded; use IsTestingM for that.
func IsTestingTB(t gotypes.Type) bool {
	return isNamed(t, "testing", "TB") ||
		isPointerToNamed(t, "testing", "T") ||
		isPointerToNamed(t, "testing", "B") ||
		isPointerToNamed(t, "testing", "F")
}

// IsTestingM reports whether t is *testing.M.
func IsTestingM(t gotypes.Type) bool {
	return isPointerToNamed(t, "testing", "M")
}
