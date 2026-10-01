package refactor

import (
	"strings"
	"testing"

	pkgtypes "github.com/mamaar/gorefactor/pkg/types"
)

const stopResolveModule = "example.com/m/foo"

func fooKey(recv, name string) pkgtypes.SymbolKey {
	return pkgtypes.SymbolKey{PkgPath: stopResolveModule, Recv: recv, Name: name}
}

func stopResolveFiles() map[string]string {
	return map[string]string{
		"foo/foo.go": `package foo

type Box[T any] struct{ v T }
type Ptr struct{}
type Store interface{ Save() }

func Save() {}
func (b *Box[T]) Get() T { return b.v }
func (p *Ptr) Save() {}
func (p Ptr) Load() {}
`,
		"foo/other.go": `package foo

type Other struct{}

func (Other) Save() {}
`,
		"foo/foo_test.go": `package foo_test

func helper() {}
`,
	}
}

func resolveOne(t *testing.T, ws *pkgtypes.Workspace, entry string) (pkgtypes.SymbolKeySet, error) {
	t.Helper()
	return resolveStopAt([]*pkgtypes.Workspace{ws}, []string{entry})
}

func TestResolveStopAt_Shapes(t *testing.T) {
	ws := buildScopeWorkspace(t, stopResolveFiles())
	declared := declaredFuncKeys(ws)

	cases := []struct {
		entry string
		want  pkgtypes.SymbolKey
	}{
		{"Save", fooKey("", "Save")},
		{"Ptr.Save", fooKey("Ptr", "Save")},
		{"Ptr.Load", fooKey("Ptr", "Load")},
		{"Box.Get", fooKey("Box", "Get")},
		{"Store.Save", fooKey("Store", "Save")},
		{"foo/other.go:Other.Save", fooKey("Other", "Save")},
		{"foo/foo.go:Save", fooKey("", "Save")},
	}
	for _, tc := range cases {
		t.Run(tc.entry, func(t *testing.T) {
			got, err := resolveOne(t, ws, tc.entry)
			if err != nil {
				t.Fatal(err)
			}
			if len(got) != 1 || !got.Has(tc.want) {
				t.Fatalf("got %v, want {%v}", got, tc.want)
			}
			if !declared.Has(tc.want) {
				t.Errorf("%v not in declaredFuncKeys", tc.want)
			}
		})
	}
}

func TestResolveStopAt_BareNameSkipsMethods(t *testing.T) {
	ws := buildScopeWorkspace(t, map[string]string{
		"foo/foo.go": "package foo\n\ntype T struct{}\n\nfunc (T) Run() {}\n",
	})
	_, err := resolveOne(t, ws, "Run")
	assertStopErr(t, err, `stop_at: no function "Run" in the workspace`)
}

func TestResolveStopAt_ExternalTestFile(t *testing.T) {
	ws := buildScopeWorkspace(t, stopResolveFiles())
	got, err := resolveOne(t, ws, "helper")
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 {
		t.Fatalf("got %v", got)
	}
	for k := range got {
		for fn := range buildFuncDeclIndex(ws) {
			if want, ok := pkgtypes.SymbolKeyOf(fn); ok && want.Name == "helper" && want != k {
				t.Errorf("got %v, type-checked key is %v", k, want)
			}
		}
	}
}

func TestResolveStopAt_SameSymbolInTwoViewsIsOneKey(t *testing.T) {
	files := stopResolveFiles()
	ws1 := buildScopeWorkspace(t, files)
	ws2 := buildScopeWorkspace(t, stopResolveFiles())
	got, err := resolveStopAt([]*pkgtypes.Workspace{ws1, ws2}, []string{"Ptr.Save"})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || !got.Has(fooKey("Ptr", "Save")) {
		t.Fatalf("got %v", got)
	}
}

func TestResolveStopAt_Errors(t *testing.T) {
	ws := buildScopeWorkspace(t, map[string]string{
		"foo/a.go": "package foo\n\nfunc Run() {}\n",
		"foo/b.go": "package foo\n\ntype T struct{}\n\nfunc (T) Run() {}\n",
		"bar/c.go": "package bar\n\nfunc Run() {}\n",
	})
	_, err := resolveOne(t, ws, "Nope")
	assertStopErr(t, err, `stop_at: no function "Nope" in the workspace`)

	_, err = resolveOne(t, ws, "foo/zzz.go:Run")
	assertStopErr(t, err, `stop_at: no function "foo/zzz.go:Run" in the workspace`)

	_, err = resolveOne(t, ws, "Run")
	assertStopErr(t, err, `stop_at: "Run" is ambiguous; use one of: bar/c.go:Run, foo/a.go:Run`)

	got, err := resolveOne(t, ws, "bar/c.go:Run")
	if err != nil || len(got) != 1 {
		t.Fatalf("got %v, %v", got, err)
	}
}

func TestResolveStopAt_EmptyEntries(t *testing.T) {
	got, err := resolveStopAt(nil, nil)
	if err != nil || got == nil || len(got) != 0 {
		t.Fatalf("got %v, %v", got, err)
	}
}

func assertStopErr(t *testing.T, err error, want string) {
	t.Helper()
	re, ok := err.(*pkgtypes.RefactorError)
	if !ok || re.Type != pkgtypes.InvalidOperation {
		t.Fatalf("err = %#v, want InvalidOperation RefactorError", err)
	}
	if !strings.Contains(re.Message, want) {
		t.Fatalf("message %q does not contain %q", re.Message, want)
	}
}
