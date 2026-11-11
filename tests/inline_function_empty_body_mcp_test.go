package tests_test

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/mamaar/gorefactor/tests/mcptest"
)

const stubLog = "package p\n\nfunc sfLog(msg string, args ...interface{}) {\n\t//log.Debugf(msg, args...)\n}\n"

func inlineFunc(t *testing.T, files map[string]string, name, src string) (string, string) {
	t.Helper()

	dir := writeInlineWorkspace(t, files)

	sess := mcptest.Dial(context.Background(), t, mcpTransport(), dir)
	defer sess.Close()

	res := callTool(t, sess, "inline_function", map[string]any{"function_name": name, "source_file": src})

	return dir, errText(res)
}

func TestInlineFunction_CallToEmptyBodyIsRemovedKeepingSideEffectingArguments(t *testing.T) {
	use := "package p\n\nimport \"bytes\"\n\nfunc Use(out *bytes.Buffer, n int, ok bool) {\n\tsfLog(\"out %v\", out.String())\n\tsfLog(\"n %d\", n)\n\tif ok {\n\t\tsfLog(\"x\")\n\t}\n}\n"
	dir, _ := inlineFunc(t, map[string]string{"p/f.go": stubLog, "p/use.go": use}, "sfLog", "p/f.go")

	got, err := os.ReadFile(filepath.Join(dir, "p/use.go"))
	if err != nil {
		t.Fatal(err)
	}

	if strings.Contains(string(got), "//") || strings.Contains(string(got), "sfLog") || strings.Contains(string(got), "...") {
		t.Errorf("call not removed cleanly:\n%s", got)
	}

	assertFileContains(t, dir, "p/use.go", "_ = out.String()")
	assertFileOmits(t, dir, "p/use.go", "_ = n")
	goVets(t, dir, "")
}

func TestInlineFunction_TrailingLineCommentInBodyDoesNotSwallowCallerCode(t *testing.T) {
	f := "package p\n\nvar n int\n\nfunc bump() {\n\tn++ // count\n}\n"
	use := "package p\n\nfunc Use(ok bool) {\n\tif ok { bump() }\n}\n"
	dir, _ := inlineFunc(t, map[string]string{"p/f.go": f, "p/use.go": use}, "bump", "p/f.go")

	assertFileContains(t, dir, "p/use.go", "n++")
	goVets(t, dir, "")
}

func TestInlineFunction_RefusesWhenVariadicParameterIsUsed(t *testing.T) {
	f := "package p\n\nfunc count(args ...int) int { return len(args) }\n"
	use := "package p\n\nfunc Use() int { return count(1, 2) }\n"
	dir, msg := inlineFunc(t, map[string]string{"p/f.go": f, "p/use.go": use}, "count", "p/f.go")

	if !strings.Contains(msg, "variadic") {
		t.Errorf("want a refusal naming the variadic parameter, got %q", msg)
	}

	assertFileUnchanged(t, dir, "p/use.go", use)
}
