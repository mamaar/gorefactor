package extractsig

import gotypes "go/types"

// mustNotCopy reports whether a value of type t must not be copied: the
// go vet copylocks rule (a type whose pointer has Lock and Unlock, directly
// or through a field or array element), plus strings.Builder and
// bytes.Buffer, which panic or alias when a non-zero copy is written.
func mustNotCopy(t gotypes.Type) bool {
	return noCopy(t, map[gotypes.Type]bool{})
}

func noCopy(t gotypes.Type, seen map[gotypes.Type]bool) bool {
	t = gotypes.Unalias(t)
	if seen[t] {
		return false
	}

	seen[t] = true

	named, ok := t.(*gotypes.Named)
	if ok {
		obj := named.Obj()
		if obj.Pkg() != nil {
			switch obj.Pkg().Path() + "." + obj.Name() {
			case "strings.Builder", "bytes.Buffer":
				return true
			}
		}

		ms := gotypes.NewMethodSet(gotypes.NewPointer(t))
		if ms.Lookup(nil, "Lock") != nil && ms.Lookup(nil, "Unlock") != nil {
			return true
		}
	}

	switch u := t.Underlying().(type) {
	case *gotypes.Struct:
		for f := range u.Fields() {
			if noCopy(f.Type(), seen) {
				return true
			}
		}
	case *gotypes.Array:
		return noCopy(u.Elem(), seen)
	}

	return false
}
