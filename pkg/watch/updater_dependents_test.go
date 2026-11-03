package watch

import (
	gotypes "go/types"
	"path/filepath"
	"testing"

	"github.com/fsnotify/fsnotify"
)

func dependentsWorkspace() map[string]string {
	const mod = "example.com/test/"

	return map[string]string{
		"a/a.go":      "package a\n\ntype T struct{}\n\nfunc (T) M() {}\n\nfunc unrelated() int { return 1 }\n",
		"b/b.go":      "package b\n\nimport \"" + mod + "a\"\n\nfunc Use() { a.T{}.M() }\n",
		"c/c.go":      "package c\n\nfunc C() {}\n",
		"c/c_test.go": "package c\n\nimport \"" + mod + "a\"\n\nfunc init() { a.T{}.M() }\n",
		"d/d.go":      "package d\n\nfunc D() {}\n",
		"d/d_test.go": "package d_test\n\nimport \"" + mod + "a\"\n\nfunc init() { a.T{}.M() }\n",
		"e/e.go":      "package e\n\nimport \"" + mod + "b\"\n\nfunc E() { b.Use() }\n",
		"f/f.go":      "package f\n\nfunc F() {}\n",
	}
}

func TestUpdater_ModifyDropsTypeInformationOfEveryDependent(t *testing.T) {
	u, set, root := setupViews(t, dependentsWorkspace())
	views := builtViews(t, set, 1)

	v := views[0]
	for _, dir := range []string{"a", "b", "c", "d", "e", "f"} {
		typedPackage(t, v, filepath.Join(root, dir))
	}

	path := filepath.Join(root, "a", "a.go")
	writeFile(t, path, "package a\n\ntype T struct{}\n\nfunc (T) M() {}\n\nfunc unrelated() int { return 2 }\n")
	u.HandleChanges([]ChangeEvent{{Path: path, Op: fsnotify.Write}})

	for _, dir := range []string{"a", "b", "c", "d", "e"} {
		if v.Workspace.Packages[filepath.Join(root, dir)].TypesChecked {
			t.Errorf("package %s kept type information of the replaced package a", dir)
		}
	}

	if !v.Workspace.Packages[filepath.Join(root, "f")].TypesChecked {
		t.Error("package f does not depend on a and must keep its type information")
	}
}

func TestUpdater_ModifyLeavesDependentUsesPointingAtTheNewMethod(t *testing.T) {
	u, set, root := setupViews(t, dependentsWorkspace())
	v := builtViews(t, set, 1)[0]
	typedPackage(t, v, filepath.Join(root, "b"))

	path := filepath.Join(root, "a", "a.go")
	writeFile(t, path, "package a\n\ntype T struct{}\n\nfunc (T) M() {}\n\nfunc unrelated() int { return 2 }\n")
	u.HandleChanges([]ChangeEvent{{Path: path, Op: fsnotify.Write}})

	a := typedPackage(t, v, filepath.Join(root, "a"))
	b := typedPackage(t, v, filepath.Join(root, "b"))
	method := a.TypesPkg.Scope().Lookup("T").Type().(*gotypes.Named).Method(0)
	var used gotypes.Object

	for id, obj := range b.TypesInfo.Uses {
		if id.Name == "M" {
			used = obj
		}
	}

	if used != method {
		t.Errorf("b resolves M to %v (%p), want the method of the current package a (%p)", used, used, method)
	}
}
