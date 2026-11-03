package refactor

import (
	"fmt"
	"go/ast"
	"go/importer"
	"go/parser"
	"go/token"
	gotypes "go/types"
	"strings"
	"testing"
)

// checkStdSource type-checks src and returns its package.
func checkStdSource(t *testing.T, src string) *gotypes.Package {
	t.Helper()
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, "p.go", src, 0)
	if err != nil {
		t.Fatal(err)
	}
	conf := gotypes.Config{Importer: importer.Default(), Error: func(err error) { t.Error(err) }}
	pkg, _ := conf.Check("p", fset, []*ast.File{file}, nil)
	return pkg
}

// methodOf returns the method name of the named type typeName in pkg.
func methodOf(t *testing.T, pkg *gotypes.Package, typeName, name string) *gotypes.Func {
	t.Helper()
	obj, _, _ := gotypes.LookupFieldOrMethod(pkg.Scope().Lookup(typeName).Type(), false, pkg, name)
	f, ok := obj.(*gotypes.Func)
	if !ok {
		t.Fatalf("no method %s.%s", typeName, name)
	}
	return f
}

// TestIsStdMethodRecognisesEveryTableEntry declares each stdMethods entry in Go
// source, so a misspelt entry fails to type-check or fails to match.
func TestIsStdMethodRecognisesEveryTableEntry(t *testing.T) {
	var src strings.Builder
	src.WriteString("package p\n\nimport (\n\t\"encoding/xml\"\n\t\"fmt\"\n\t\"io\"\n)\n\n")
	src.WriteString("var (\n\t_ fmt.State\n\t_ fmt.ScanState\n\t_ io.Reader\n\t_ io.Writer\n\t_ xml.Encoder\n\t_ xml.Decoder\n\t_ xml.StartElement\n)\n\n")
	src.WriteString("type base struct{}\n\nfunc (base) Error() string { return \"\" }\n\n")
	type entry struct{ name, sig string }
	var entries []entry
	for name, list := range stdMethods {
		for _, m := range list {
			entries = append(entries, entry{name, m.signature})
		}
	}
	for i, e := range entries {
		fmt.Fprintf(&src, "type s%d struct{ base }\n\nfunc (s%d) %s%s { panic(0) }\n\n", i, i, e.name, strings.ReplaceAll(e.sig, "encoding/xml.", "xml."))
	}
	pkg := checkStdSource(t, src.String())

	for i, e := range entries {
		f := methodOf(t, pkg, fmt.Sprintf("s%d", i), e.name)
		if stdMethodOf(f.Name(), f) == nil {
			t.Errorf("%s%s: not recognised as a standard method", e.name, e.sig)
		}
	}
}

func TestIsStdMethodRejectsNearMisses(t *testing.T) {
	src := `package p

import "fmt"

var _ fmt.State

type base struct{}

func (base) Error() string { return "" }

type plain struct{}

func (plain) Is(error) bool { return false }

type wrongParam struct{ base }

func (wrongParam) Format(fmt.State, string) {}

type variadicDecode struct{ base }

func (variadicDecode) GobDecode(...byte) error { return nil }

type wrongText struct{ base }

func (wrongText) MarshalText() string { return "" }

type otherResult struct{ base }

func (otherResult) Error() int { return 0 }

func Error() string { return "" }
`
	pkg := checkStdSource(t, src)

	for _, c := range []struct{ typ, method string }{
		{"plain", "Is"}, {"wrongParam", "Format"}, {"otherResult", "Error"}, {"wrongText", "MarshalText"}, {"variadicDecode", "GobDecode"},
	} {
		f := methodOf(t, pkg, c.typ, c.method)
		if stdMethodOf(f.Name(), f) != nil {
			t.Errorf("%s.%s: recognised as a standard method, want not", c.typ, c.method)
		}
	}
	f := pkg.Scope().Lookup("Error").(*gotypes.Func)
	if stdMethodOf(f.Name(), f) != nil {
		t.Error("the package-level function Error: recognised as a method")
	}
}

// TestStdMethodOfMatchesTheNameItIsGiven checks a method against a name other
// than its own, as a rename does with the new name.
func TestStdMethodOfMatchesTheNameItIsGiven(t *testing.T) {
	pkg := checkStdSource(t, "package p\n\ntype T struct{}\n\nfunc (T) Name() string { return \"\" }\n")
	f := methodOf(t, pkg, "T", "Name")

	if stdMethodOf("String", f) == nil {
		t.Error("Name() string named String: not matched")
	}
	if stdMethodOf("Name", f) != nil {
		t.Error("Name() string named Name: matched")
	}
}
