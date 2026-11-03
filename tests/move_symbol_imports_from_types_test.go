package tests_test

import (
	"context"
	"regexp"
	"strings"
	"testing"

	"github.com/mamaar/gorefactor/tests/mcptest"
)

const importsOPkg = "package o\n\ntype T struct{ Field int }\n\nfunc X() int { return 1 }\n"

const importsOPath = `"example.com/threadmcp/o"`

func moveImportsFixture(t *testing.T, files map[string]string, symbol string) string {
	t.Helper()

	files["o/o.go"] = importsOPkg
	dir := writeInlineWorkspace(t, files)

	sess := mcptest.Dial(t.Context(), t, mcpTransport(), dir)
	defer sess.Close()

	res := moveSymbolCall(t, sess, symbol, "src", "dst")
	if res.IsError {
		t.Fatalf("move %s: %s", symbol, errText(res))
	}

	return dir
}

func TestMoveSymbol_UsesTargetsPlainImportWhenSourceAliasedThePath(t *testing.T) {
	dir := moveImportsFixture(t, map[string]string{
		"src/src.go": "package src\n\nimport osrc " + importsOPath + "\n\nfunc Use() int { return osrc.X() }\n",
		"dst/dst.go": "package dst\n\nimport " + importsOPath + "\n\nfunc D() int { return o.X() }\n",
	}, "Use")

	got := fileText(t, dir, "dst/dst.go")
	if !strings.Contains(got, "func Use() int { return o.X() }") || strings.Contains(got, "osrc") {
		t.Errorf("moved code does not use the target's name:\n%s", got)
	}

	n := strings.Count(got, importsOPath)
	if n != 1 {
		t.Errorf("dst imports the path %d times, want 1:\n%s", n, got)
	}

	goVets(t, dir, "")
}

func TestMoveSymbol_UsesTargetsAliasWhenSourceImportedThePathPlainly(t *testing.T) {
	dir := moveImportsFixture(t, map[string]string{
		"src/src.go": "package src\n\nimport " + importsOPath + "\n\nfunc Use() int { return o.X() }\n",
		"dst/dst.go": "package dst\n\nimport oo " + importsOPath + "\n\nfunc D() int { return oo.X() }\n",
	}, "Use")

	got := fileText(t, dir, "dst/dst.go")
	if !strings.Contains(got, "func Use() int { return oo.X() }") {
		t.Errorf("moved code does not use the target's alias:\n%s", got)
	}

	n := strings.Count(got, importsOPath)
	if n != 1 {
		t.Errorf("dst imports the path %d times, want 1:\n%s", n, got)
	}

	goVets(t, dir, "")
}

func TestMoveSymbol_ChoosesFreshAliasWhenTargetDeclaresTheImportName(t *testing.T) {
	dir := moveImportsFixture(t, map[string]string{
		"src/src.go": "package src\n\nimport " + importsOPath + "\n\nfunc Use() int { return o.X() }\n",
		"dst/dst.go": "package dst\n\nvar o int\n",
	}, "Use")

	got := fileText(t, dir, "dst/dst.go")

	m := regexp.MustCompile(`(\w+) ` + importsOPath).FindStringSubmatch(got)
	if m == nil || m[1] == "o" {
		t.Fatalf("dst has no aliased import of the path:\n%s", got)
	}

	if !strings.Contains(got, "return "+m[1]+".X()") {
		t.Errorf("moved code does not use the alias %s:\n%s", m[1], got)
	}

	goVets(t, dir, "")
}

func TestMoveSymbol_ImportsPathOnceWhenMovedFilesAliasItDifferently(t *testing.T) {
	dir := moveImportsFixture(t, map[string]string{
		"src/t.go": "package src\n\nimport a1 " + importsOPath + "\n\ntype Thing struct{ F a1.T }\n",
		"src/m.go": "package src\n\nimport a2 " + importsOPath + "\n\nfunc (t Thing) Get() int { return a2.X() + t.F.Field }\n",
	}, "Thing")

	got := fileText(t, dir, "dst/dst.go")

	n := strings.Count(got, importsOPath)
	if n != 1 {
		t.Errorf("dst imports the path %d times, want 1:\n%s", n, got)
	}

	if strings.Contains(got, "a2.") == strings.Contains(got, "a1.") {
		t.Errorf("moved code uses two different qualifiers:\n%s", got)
	}

	goVets(t, dir, "")
}

func TestMoveSymbol_AddsNamedImportWhenTargetOnlyImportsPathBlank(t *testing.T) {
	dir := moveImportsFixture(t, map[string]string{
		"src/src.go": "package src\n\nimport " + importsOPath + "\n\nfunc Use() int { return o.X() }\n",
		"dst/dst.go": "package dst\n\nimport _ " + importsOPath + "\n\nvar D = 1\n",
	}, "Use")

	assertFileContains(t, dir, "dst/dst.go", "_ "+importsOPath)
	assertFileContains(t, dir, "dst/dst.go", "return o.X()")
	goVets(t, dir, "")
}

func TestMoveSymbol_DoesNotImportForLocalVariableNamedLikeAnImport(t *testing.T) {
	dir := moveImportsFixture(t, map[string]string{
		"src/src.go": "package src\n\nimport \"strings\"\n\nfunc Other() string { return strings.ToUpper(\"x\") }\n\nfunc Pick() int {\n\tstrings := struct{ N int }{3}\n\treturn strings.N\n}\n",
	}, "Pick")

	got := fileText(t, dir, "dst/dst.go")
	if strings.Contains(got, `"strings"`) {
		t.Errorf("dst imports strings for a local variable:\n%s", got)
	}

	goVets(t, dir, "")
}

func TestMoveSymbol_ImportsPackageWhoseNameDiffersFromItsPathSegmentWithoutAlias(t *testing.T) {
	dir := moveImportsFixture(t, map[string]string{
		"yaml.v2/y.go": "package yaml\n\nfunc Y() int { return 1 }\n",
		"src/src.go":   "package src\n\nimport \"example.com/threadmcp/yaml.v2\"\n\nfunc Use() int { return yaml.Y() }\n",
	}, "Use")

	got := fileText(t, dir, "dst/dst.go")
	if !strings.Contains(got, "\t\"example.com/threadmcp/yaml.v2\"\n") || !strings.Contains(got, "yaml.Y()") {
		t.Errorf("dst does not import yaml.v2 under its own name:\n%s", got)
	}

	goVets(t, dir, "")
}

func TestMoveSymbol_RefusesCodeUsingNamesFromADotImport(t *testing.T) {
	src := "package src\n\nimport . " + importsOPath + "\n\nfunc Use() int { return X() }\n"
	dir := writeInlineWorkspace(t, map[string]string{"o/o.go": importsOPkg, "src/src.go": src})

	sess := mcptest.Dial(context.Background(), t, mcpTransport(), dir)
	defer sess.Close()

	res := moveSymbolCall(t, sess, "Use", "src", "dst")

	if !res.IsError {
		t.Fatalf("move Use succeeded, want a refusal")
	}

	for _, want := range []string{"Use", "dot import", "example.com/threadmcp/o", "src.go"} {
		got := errText(res)
		if !strings.Contains(got, want) {
			t.Errorf("error %q does not mention %q", got, want)
		}
	}

	assertFileUnchanged(t, dir, "src/src.go", src)
}

func TestMoveSymbol_DropsQualifierOfTheTargetPackageItself(t *testing.T) {
	dir := moveImportsFixture(t, map[string]string{
		"src/src.go": "package src\n\nimport d \"example.com/threadmcp/dst\"\n\nfunc Use() d.Thing { return d.Thing{} }\n",
		"dst/dst.go": "package dst\n\ntype Thing struct{}\n",
	}, "Use")

	got := fileText(t, dir, "dst/dst.go")
	if !strings.Contains(got, "func Use() Thing { return Thing{} }") || strings.Contains(got, "example.com/threadmcp/dst") {
		t.Errorf("moved code still qualifies the target package:\n%s", got)
	}

	goVets(t, dir, "")
}
