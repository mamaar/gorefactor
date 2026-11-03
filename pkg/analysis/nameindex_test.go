package analysis

import (
	"fmt"
	"path/filepath"
	"slices"
	"testing"
)

func typedNameEntries(t *testing.T, v *View, name string) []string {
	t.Helper()

	entries, _ := v.Resolver.BuildReferenceIndex().NameEntries(name)
	var out []string

	for _, e := range entries {
		pos := v.Workspace.FileSet.Position(e.Pos)

		kind := "use"
		if e.IsDeclaration {
			kind = "decl"
		}

		sel := ""
		if e.IsSelector {
			sel = " sel=" + e.PkgAlias
		}

		typed := ""
		if e.TypesObject == nil {
			typed = " untyped"
		}

		if e.File.Path != pos.Filename {
			t.Errorf("%s entry at %s is attributed to %s", name, pos, e.File.Path)
		}

		out = append(out, fmt.Sprintf("%s:%d:%d %s%s%s", filepath.Base(pos.Filename), pos.Line, pos.Column, kind, sel, typed))
	}

	slices.Sort(out)

	return out
}

func TestBuildReferenceIndex_TypedNameEntries(t *testing.T) {
	set, root := loadSet(t, map[string]string{
		"a/a.go":      "package a\n\ntype T struct{ F int }\n\nfunc New() *T { return &T{} }\n\nfunc (t *T) Get() int { return t.F }\n",
		"b/b.go":      "package b\n\nimport alias \"example.com/w/a\"\n\nfunc Use() int {\n\tv := alias.New()\n\treturn v.Get() + v.F + wrap().F\n}\n\nfunc wrap() *alias.T { return alias.New() }\n",
		"b/c.go":      "package b\n\nfunc Other() int { return Use() }\n",
		"b/b_test.go": "package b\n\nfunc helper() int { return Other() }\n",
	})
	v := set.Primary()
	checkedPackage(t, v, filepath.Join(root, "a"))
	checkedPackage(t, v, filepath.Join(root, "b"))

	for name, want := range map[string][]string{
		"New":   {"a.go:5:6 decl", "b.go:10:37 use sel=alias", "b.go:6:13 use sel=alias"},
		"Get":   {"a.go:7:13 decl", "b.go:7:11 use sel=v"},
		"F":     {"a.go:3:16 decl", "a.go:7:34 use sel=t", "b.go:7:21 use sel=v", "b.go:7:32 use"},
		"Use":   {"b.go:5:6 decl", "c.go:3:27 use"},
		"Other": {"b_test.go:3:28 use", "c.go:3:6 decl"},
		"alias": {"b.go:10:14 use", "b.go:10:31 use", "b.go:3:8 decl", "b.go:6:7 use"},
		"v":     {"b.go:6:2 decl", "b.go:7:19 use", "b.go:7:9 use"},
	} {
		got := typedNameEntries(t, v, name)
		if !slices.Equal(got, want) {
			t.Errorf("%s entries = %q, want %q", name, got, want)
		}
	}
}
