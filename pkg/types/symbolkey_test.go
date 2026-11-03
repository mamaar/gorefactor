package types

import (
	"go/ast"
	"go/importer"
	"go/parser"
	"go/token"
	gotypes "go/types"
	"testing"
)

const symbolKeySrc = `package p

type Store interface{ Save(x int) }

type Mem[T any] struct{}

func (m *Mem[T]) Save(x int) {}

type Alias = Mem[int]

func Top() {
	type local struct{}
	_ = local{}
}

func init() {}

func _() {}

var Anon interface{ Hidden() }

var V int
`

func checkSymbolKeySrc(t *testing.T) *gotypes.Package {
	t.Helper()

	fset := token.NewFileSet()

	f, err := parser.ParseFile(fset, "p.go", symbolKeySrc, 0)
	if err != nil {
		t.Fatal(err)
	}

	conf := gotypes.Config{Importer: importer.Default()}

	pkg, err := conf.Check("example.com/p", fset, []*ast.File{f}, nil)
	if err != nil {
		t.Fatal(err)
	}

	return pkg
}

func TestSymbolKeyOf(t *testing.T) {
	pkg := checkSymbolKeySrc(t)
	scope := pkg.Scope()

	mem := scope.Lookup("Mem").Type().(*gotypes.Named)
	store := scope.Lookup("Store").Type().(*gotypes.Named)
	anon := scope.Lookup("Anon").Type().(*gotypes.Interface)

	tests := []struct {
		name string
		obj  gotypes.Object
		want SymbolKey
		ok   bool
	}{
		{"function", scope.Lookup("Top"), SymbolKey{"example.com/p", "", "Top"}, true},
		{"package variable", scope.Lookup("V"), SymbolKey{"example.com/p", "", "V"}, true},
		{"pointer-receiver method on generic type", mem.Method(0), SymbolKey{"example.com/p", "Mem", "Save"}, true},
		{"interface method keyed by interface name", store.Underlying().(*gotypes.Interface).Method(0), SymbolKey{"example.com/p", "Store", "Save"}, true},
		{"method of unnamed interface", anon.Method(0), SymbolKey{}, false},
		{"nil object", nil, SymbolKey{}, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, ok := SymbolKeyOf(tt.obj)
			if ok != tt.ok || got != tt.want {
				t.Errorf("SymbolKeyOf = %v, %v; want %v, %v", got, ok, tt.want, tt.ok)
			}
		})
	}
}

func TestSymbolKeyOf_InstantiatedMethodSharesTheGenericKey(t *testing.T) {
	pkg := checkSymbolKeySrc(t)
	alias := gotypes.Unalias(pkg.Scope().Lookup("Alias").Type()).(*gotypes.Named)
	got, ok := SymbolKeyOf(alias.Method(0))

	want := (SymbolKey{"example.com/p", "Mem", "Save"})
	if !ok || got != want {
		t.Errorf("SymbolKeyOf = %v, %v; want %v", got, ok, want)
	}
}

func TestSymbolKeyOf_UnstableNamesHaveNoKey(t *testing.T) {
	fset := token.NewFileSet()

	f, err := parser.ParseFile(fset, "p.go", symbolKeySrc, 0)
	if err != nil {
		t.Fatal(err)
	}

	info := &gotypes.Info{Defs: make(map[*ast.Ident]gotypes.Object)}
	conf := gotypes.Config{Importer: importer.Default()}

	_, err = conf.Check("example.com/p", fset, []*ast.File{f}, info)
	if err != nil {
		t.Fatal(err)
	}

	for ident, obj := range info.Defs {
		switch ident.Name {
		case "init", "_", "local":
			key, ok := SymbolKeyOf(obj)
			if ok {
				t.Errorf("%s has key %v, want none", ident.Name, key)
			}
		}
	}
}

func TestSymbolKeySet(t *testing.T) {
	a, b := SymbolKey{"p", "", "a"}, SymbolKey{"p", "T", "b"}

	set := SymbolKeySet{}
	if !set.AddAll(SymbolKeySet{a: {}}) {
		t.Error("adding a new key reported no growth")
	}

	if set.AddAll(SymbolKeySet{a: {}}) {
		t.Error("adding a present key reported growth")
	}

	if !set.Has(a) || set.Has(b) {
		t.Errorf("membership wrong: %v", set)
	}

	if !set.ContainsAll(SymbolKeySet{a: {}}) || set.ContainsAll(SymbolKeySet{a: {}, b: {}}) {
		t.Error("ContainsAll wrong")
	}

	clone := set.Clone()
	clone.AddAll(SymbolKeySet{b: {}})

	if set.Has(b) {
		t.Error("Clone shares storage")
	}

	got := b.String()
	if got != "p.T.b" {
		t.Errorf("String = %q", got)
	}
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
