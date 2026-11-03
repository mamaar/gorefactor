package analysis

import (
	"go/token"
	"os"
	"path/filepath"
	"testing"
)

func newTreeWithDir(t *testing.T, files map[string]string) (*SourceTree, string) {
	t.Helper()
	dir := t.TempDir()
	for name, content := range files {
		err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o644)
		if err != nil {
			t.Fatal(err)
		}
	}
	tree := NewSourceTree(token.NewFileSet())
	err := tree.AddDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	return tree, dir
}

func dirNames(tree *SourceTree, dir string) []string {
	var names []string
	for _, f := range tree.Dir(dir) {
		names = append(names, f.Name)
	}
	return names
}

func TestSourceTree_ReplaceRereadsContentAndAST(t *testing.T) {
	tree, dir := newTreeWithDir(t, map[string]string{"a.go": "package a\n\nfunc One() {}\n"})
	path := filepath.Join(dir, "a.go")
	before := tree.File(path)

	err := os.WriteFile(path, []byte("package a\n\nfunc Two() {}\n"), 0o644)
	if err != nil {
		t.Fatal(err)
	}
	got, err := tree.Replace(path)
	if err != nil {
		t.Fatal(err)
	}

	if got == before || tree.File(path) != got || tree.Dir(dir)[0] != got {
		t.Fatal("Replace must swap the recorded file in both lookups")
	}
	if string(got.Content) != "package a\n\nfunc Two() {}\n" || got.AST.Decls[0] == before.AST.Decls[0] {
		t.Fatal("Replace did not re-read content and AST")
	}
}

func TestSourceTree_ReplaceRecordsParseError(t *testing.T) {
	tree, dir := newTreeWithDir(t, map[string]string{"a.go": "package a\n"})
	path := filepath.Join(dir, "a.go")

	err := os.WriteFile(path, []byte("package a\n\nfunc {"), 0o644)
	if err != nil {
		t.Fatal(err)
	}
	got, err := tree.Replace(path)
	if err != nil {
		t.Fatal(err)
	}
	if got.Err == nil {
		t.Fatal("expected the parse error to be recorded on the file")
	}
}

func TestSourceTree_ReplaceUnknownFileFails(t *testing.T) {
	tree, dir := newTreeWithDir(t, map[string]string{"a.go": "package a\n"})
	_, err := tree.Replace(filepath.Join(dir, "missing.go"))
	if err == nil {
		t.Fatal("expected an error for a file the tree does not record")
	}
}

func TestSourceTree_AddKeepsDirectorySorted(t *testing.T) {
	tree, dir := newTreeWithDir(t, map[string]string{
		"a.go": "package a\n",
		"c.go": "package a\n",
	})
	path := filepath.Join(dir, "b.go")
	err := os.WriteFile(path, []byte("package a\n"), 0o644)
	if err != nil {
		t.Fatal(err)
	}

	added, err := tree.Add(path)
	if err != nil {
		t.Fatal(err)
	}

	got := dirNames(tree, dir)
	if len(got) != 3 || got[0] != "a.go" || got[1] != "b.go" || got[2] != "c.go" {
		t.Fatalf("directory order = %v", got)
	}
	if tree.File(path) != added {
		t.Fatal("Add did not record the file by path")
	}
}

func TestSourceTree_RemoveDropsFileFromBothLookups(t *testing.T) {
	tree, dir := newTreeWithDir(t, map[string]string{
		"a.go": "package a\n",
		"b.go": "package a\n",
	})
	path := filepath.Join(dir, "a.go")

	tree.Remove(path)

	if tree.File(path) != nil {
		t.Fatal("removed file still found by path")
	}
	got := dirNames(tree, dir)
	if len(got) != 1 || got[0] != "b.go" {
		t.Fatalf("directory contents = %v", got)
	}
}
