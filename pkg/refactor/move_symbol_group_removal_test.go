package refactor

import (
	"go/format"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/mamaar/gorefactor/pkg/types"
)

// moveGroupNames moves names from src to dst in one request, vets the result
// and returns the gofmt-ed source and the target text.
func moveGroupNames(t *testing.T, srcText string, names ...string) (src, dst string) {
	t.Helper()
	eng, dir := loadConfigsEngine(t, map[string]string{"src/src.go": srcText})
	ws := eng.WorkspaceSet().Primary().Workspace
	eng.EnsureTypeChecked(ws)
	req := types.MoveSymbolRequest{ToPackage: filepath.Join(dir, "dst"), CreateTarget: true}
	for _, n := range names {
		req.Symbols = append(req.Symbols, types.QualifiedSymbol{Package: filepath.Join(dir, "src"), Name: n})
	}
	plan, err := (&MoveSymbolOperation{Request: req}).Execute(ws)
	if err != nil {
		t.Fatal(err)
	}
	srcPath := filepath.Join(dir, "src", "src.go")
	var srcChanges []types.Change
	for _, c := range plan.Changes {
		switch c.File {
		case srcPath:
			srcChanges = append(srcChanges, c)
		case filepath.Join(dir, "dst", "dst.go"):
			dst = c.NewText
		}
	}
	assertNoOverlap(t, srcChanges)
	src = string(applyChangesInMemory([]byte(srcText), srcChanges))
	writeAndVet(t, dir, src, dst)
	if formatted, err := format.Source([]byte(src)); err == nil {
		src = string(formatted)
	}
	return src, dst
}

// assertNoOverlap fails the test for each pair of overlapping changes.
func assertNoOverlap(t *testing.T, changes []types.Change) {
	t.Helper()
	for i, a := range changes {
		for _, b := range changes[i+1:] {
			if a.Start < b.End && b.Start < a.End {
				t.Errorf("overlapping source changes %+v and %+v", a, b)
			}
		}
	}
}

// writeAndVet writes the moved source and target under dir and fails the test
// if go vet rejects the result.
func writeAndVet(t *testing.T, dir, src, dst string) {
	t.Helper()
	err := os.MkdirAll(filepath.Join(dir, "dst"), 0o755)
	if err != nil {
		t.Fatal(err)
	}
	for p, text := range map[string]string{filepath.Join(dir, "src", "src.go"): src, filepath.Join(dir, "dst", "dst.go"): dst} {
		err := os.WriteFile(p, []byte(text), 0o644)
		if err != nil {
			t.Fatal(err)
		}
	}
	cmd := exec.Command("go", "vet", "./...")
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "CGO_ENABLED=0", "GOFLAGS=")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Errorf("go vet failed: %v\n%s\nsrc:\n%s\ndst:\n%s", err, out, src, dst)
	}
}

func TestMoveSymbol_GroupWhoseSpecsAllMoveLeavesNoShell(t *testing.T) {
	src, dst := moveGroupNames(t, "package src\n\ntype (\n\tA int\n\tB int\n)\n\nfunc Keep() {}\n", "A", "B")

	if strings.Contains(src, "type (") {
		t.Errorf("source keeps the group shell:\n%s", src)
	}
	want := "package src\n\nfunc Keep() {}\n"
	if src != want {
		t.Errorf("source = %q, want %q", src, want)
	}
	if !strings.Contains(dst, "type A int") || !strings.Contains(dst, "type B int") {
		t.Errorf("target lacks the types:\n%s", dst)
	}
}

func TestMoveSymbol_GroupDocAndSpecDocsRemovedWithFullyMovedGroup(t *testing.T) {
	src, dst := moveGroupNames(t, "package src\n\n// Group doc.\ntype (\n\t// A doc.\n\tA int\n\n\t// B doc.\n\tB int\n)\n\nfunc Keep() {}\n", "A", "B")

	if strings.Contains(src, "Group doc") || strings.Contains(src, "type (") || strings.Contains(src, "A doc") {
		t.Errorf("source keeps part of the group:\n%s", src)
	}
	if strings.Contains(dst, "Group doc") || !strings.Contains(dst, "A doc") || !strings.Contains(dst, "B doc") {
		t.Errorf("target should carry spec docs only:\n%s", dst)
	}
}

func TestMoveSymbol_FloatingCommentBetweenSpecsRemovedWithFullyMovedGroup(t *testing.T) {
	src, _ := moveGroupNames(t, "package src\n\ntype (\n\tA int\n\n\t// floating note\n\n\tB int\n)\n\nfunc Keep() {}\n", "A", "B")

	want := "package src\n\nfunc Keep() {}\n"
	if src != want {
		t.Errorf("source = %q, want %q", src, want)
	}
}

func TestMoveSymbol_GroupWithRemainingSpecKeepsItsShell(t *testing.T) {
	src, _ := moveGroupNames(t, "package src\n\ntype (\n\tA int\n\tB int\n\tC int\n)\n", "A", "C")

	want := "package src\n\ntype (\n\tB int\n)\n"
	if src != want {
		t.Errorf("source = %q, want %q", src, want)
	}
}

func TestMoveSymbol_DeclarationAfterFullyMovedGroupStaysSeparated(t *testing.T) {
	src, _ := moveGroupNames(t, "package src\n\nfunc Before() {}\n\ntype (\n\tA int\n\tB int\n)\nfunc After() {}\n", "A", "B")

	want := "package src\n\nfunc Before() {}\n\nfunc After() {}\n"
	if src != want {
		t.Errorf("source = %q, want %q", src, want)
	}
}

func TestMoveSymbol_FullyMovedGroupAtEndOfFileWithoutTrailingNewline(t *testing.T) {
	src, _ := moveGroupNames(t, "package src\n\nfunc Keep() {}\n\ntype (\n\tA int\n\tB int\n)", "A", "B")

	if strings.Contains(src, "type") {
		t.Errorf("source keeps the group:\n%s", src)
	}
	if !strings.HasPrefix(src, "package src\n\nfunc Keep() {}\n") {
		t.Errorf("source = %q", src)
	}
}

func TestMoveSymbol_ConstantGroupWhoseSpecsAllMoveLeavesNoShell(t *testing.T) {
	src, dst := moveGroupNames(t, "package src\n\n// Group doc.\nconst (\n\t// A doc.\n\tA = 1\n\n\t// B doc.\n\tB = 2\n)\n\nfunc Keep() {}\n", "A", "B")

	want := "package src\n\nfunc Keep() {}\n"
	if src != want {
		t.Errorf("source = %q, want %q", src, want)
	}
	if !strings.Contains(dst, "// A doc.\nconst A = 1") || !strings.Contains(dst, "// B doc.\nconst B = 2") {
		t.Errorf("target lacks the constants with their docs:\n%s", dst)
	}
}

func TestMoveSymbol_VariableGroupWithMultiNameSpecMovesAndLeavesNoShell(t *testing.T) {
	src, dst := moveGroupNames(t, "package src\n\nfunc Pair() (int, int) { return 1, 2 }\n\nvar (\n\ta, b = Pair()\n\tc    int\n)\n", "a", "b", "c")

	want := "package src\n\nfunc Pair() (int, int) { return 1, 2 }\n"
	if src != want {
		t.Errorf("source = %q, want %q", src, want)
	}
	if !strings.Contains(dst, "var a, b = src.Pair()") || !strings.Contains(dst, "var c ") {
		t.Errorf("target lacks the variables:\n%s", dst)
	}
}

func TestMoveSymbol_IotaGroupMovesVerbatimAndLeavesNothingBehind(t *testing.T) {
	src, dst := moveGroupNames(t, "package src\n\nfunc Keep() {}\n\n// Doc.\nconst (\n\tA = iota\n\tB\n\t_\n\tC\n)\n", "A", "B", "C")

	want := "package src\n\nfunc Keep() {}\n"
	if src != want {
		t.Errorf("source = %q, want %q", src, want)
	}
	if !strings.Contains(dst, "// Doc.\nconst (\n\tA = iota\n\tB\n\t_\n\tC\n)") {
		t.Errorf("target lacks the verbatim group:\n%s", dst)
	}
}
