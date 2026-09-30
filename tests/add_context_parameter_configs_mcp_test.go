package tests_test

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/mamaar/gorefactor/tests/mcptest"
)

func goVets(t *testing.T, dir string, tagSets ...string) {
	t.Helper()
	for _, tags := range tagSets {
		cmd := exec.Command("go", "vet", "-tags="+tags, "./...")
		cmd.Dir = dir
		cmd.Env = append(os.Environ(), "CGO_ENABLED=0", "GOFLAGS=")
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Errorf("go vet -tags=%q failed: %v\n%s", tags, err, out)
		}
	}
}

func addContext(t *testing.T, sess *mcptest.Session, args map[string]any) *mcpsdk.CallToolResult {
	t.Helper()
	res, err := sess.CallTool(context.Background(), &mcpsdk.CallToolParams{Name: "add_context_parameter", Arguments: args})
	if err != nil {
		t.Fatalf("CallTool: %v", err)
	}
	return res
}

func addContextOK(t *testing.T, sess *mcptest.Session, args map[string]any) map[string]any {
	t.Helper()
	return callJSON(t, sess, "add_context_parameter", args)
}

func fArgs(file string, extra map[string]any) map[string]any {
	args := map[string]any{"function_name": "f", "source_file": file}
	for k, v := range extra {
		args[k] = v
	}
	return args
}

const ctxFDefault = "//go:build !tagx\n\npackage p\n\nfunc f(n int) int { return n }\n"
const ctxFTagx = "//go:build tagx\n\npackage p\n\nfunc f(n int) int { return n + 1 }\n"

func TestAddContextParameter_RewritesCallSiteInTaggedTestFile(t *testing.T) {
	dir := writeInlineWorkspace(t, map[string]string{
		"p/f.go":             "package p\n\nfunc f(n int) int { return n }\n",
		"p/use.go":           "package p\n\nfunc Use() int { return f(1) }\n",
		"p/use_tagx_test.go": "//go:build tagx\n\npackage p\n\nimport \"testing\"\n\nfunc TestTagx(t *testing.T) { f(2) }\n",
	})
	sess := mcptest.Dial(context.Background(), t, mcpTransport(), dir)
	defer sess.Close()

	addContextOK(t, sess, fArgs("p/f.go", nil))

	assertFileContains(t, dir, "p/f.go", "func f(ctx context.Context, n int) int")
	assertFileContains(t, dir, "p/use.go", "f(context.TODO(), 1)")
	assertFileContains(t, dir, "p/use_tagx_test.go", "f(context.TODO(), 2)")
	goVets(t, dir, "", "tagx")
}

func TestAddContextParameter_TagSiblingsGainCtxAndSharedCallerPassesDefaultOnce(t *testing.T) {
	dir := writeInlineWorkspace(t, map[string]string{
		"p/f_default.go": ctxFDefault,
		"p/f_tagx.go":    ctxFTagx,
		"p/use.go":       "package p\n\nfunc Use() int { return f(1) }\n",
	})
	sess := mcptest.Dial(context.Background(), t, mcpTransport(), dir)
	defer sess.Close()

	out := addContextOK(t, sess, fArgs("p/f_default.go", nil))

	if configs, _ := out["build_configs"].([]any); len(configs) != 2 {
		t.Errorf("build_configs = %v, want 2 entries", out["build_configs"])
	}
	for _, f := range []string{"p/f_default.go", "p/f_tagx.go"} {
		if got := fileText(t, dir, f); strings.Count(got, "func f(ctx context.Context, n int) int") != 1 {
			t.Errorf("%s:\n%s", f, got)
		}
	}
	if got := fileText(t, dir, "p/use.go"); strings.Count(got, "f(context.TODO(), 1)") != 1 || strings.Count(got, "context.TODO()") != 1 {
		t.Errorf("use.go:\n%s", got)
	}
	goVets(t, dir, "", "tagx")
}

func TestAddContextParameter_CustomDefaultAppliesInTaggedCallSites(t *testing.T) {
	dir := writeInlineWorkspace(t, map[string]string{
		"p/f.go":             "package p\n\nfunc f(n int) int { return n }\n",
		"p/bg.go":            "package p\n\nimport \"context\"\n\nvar bg = context.Background()\n",
		"p/use.go":           "package p\n\nfunc Use() int { return f(1) }\n",
		"p/use_tagx_test.go": "//go:build tagx\n\npackage p\n\nimport \"testing\"\n\nfunc TestTagx(t *testing.T) { f(2) }\n",
	})
	sess := mcptest.Dial(context.Background(), t, mcpTransport(), dir)
	defer sess.Close()

	addContextOK(t, sess, fArgs("p/f.go", map[string]any{"default_value": "bg"}))

	assertFileContains(t, dir, "p/use.go", "f(bg, 1)")
	assertFileContains(t, dir, "p/use_tagx_test.go", "f(bg, 2)")
	goVets(t, dir, "", "tagx")
}

func TestAddContextParameter_PropagateReachesImplementationOnlyUnderTag(t *testing.T) {
	dir := writeInlineWorkspace(t, map[string]string{
		"p/iface.go":  "package p\n\ntype Runner interface{ Run(n int) int }\n\nfunc Use(r Runner) int { return r.Run(1) }\n",
		"p/a.go":      "//go:build !tagx\n\npackage p\n\ntype A struct{}\n\nfunc (A) Run(n int) int { return n }\n",
		"p/b_tagx.go": "//go:build tagx\n\npackage p\n\ntype B struct{}\n\nfunc (B) Run(n int) int { return n + 1 }\n",
	})
	sess := mcptest.Dial(context.Background(), t, mcpTransport(), dir)
	defer sess.Close()

	addContextOK(t, sess, map[string]any{"function_name": "A.Run", "source_file": "p/a.go", "propagate": true})

	for _, f := range []string{"p/iface.go", "p/a.go", "p/b_tagx.go"} {
		if got := fileText(t, dir, f); strings.Count(got, "Run(ctx context.Context, n int) int") != 1 {
			t.Errorf("%s:\n%s", f, got)
		}
	}
	assertFileContains(t, dir, "p/iface.go", "r.Run(context.TODO(), 1)")
	goVets(t, dir, "", "tagx")
}

func TestAddContextParameter_FindsTargetDeclaredOnlyUnderTag(t *testing.T) {
	dir := writeInlineWorkspace(t, map[string]string{
		"p/f_tagx.go":   ctxFTagx,
		"p/use_tagx.go": "//go:build tagx\n\npackage p\n\nfunc Use() int { return f(1) }\n",
		"p/plain.go":    "package p\n\nfunc plain() int { return 1 }\n",
	})
	sess := mcptest.Dial(context.Background(), t, mcpTransport(), dir)
	defer sess.Close()

	addContextOK(t, sess, fArgs("p/f_tagx.go", nil))

	assertFileContains(t, dir, "p/f_tagx.go", "func f(ctx context.Context, n int) int")
	assertFileContains(t, dir, "p/use_tagx.go", "f(context.TODO(), 1)")
	assertUnchanged(t, dir, "p/plain.go")
	goVets(t, dir, "tagx")
}

func TestAddContextParameter_MissingFunctionErrorTextIsUnchanged(t *testing.T) {
	dir := writeInlineWorkspace(t, map[string]string{"p/f.go": "package p\n\nfunc f(n int) int { return n }\n"})
	sess := mcptest.Dial(context.Background(), t, mcpTransport(), dir)
	defer sess.Close()

	res := addContext(t, sess, map[string]any{"function_name": "nope", "source_file": "p/f.go"})

	want := "function nope not found in " + filepath.Join(dir, "p/f.go")
	if !res.IsError || errText(res) != want {
		t.Errorf("error = %q (IsError %v), want %q", errText(res), res.IsError, want)
	}
}

func TestAddContextParameter_AlreadyHasContextErrorTextIsUnchanged(t *testing.T) {
	dir := writeInlineWorkspace(t, map[string]string{
		"p/f.go": "package p\n\nimport \"context\"\n\nfunc f(ctx context.Context, n int) int { return n }\n",
	})
	sess := mcptest.Dial(context.Background(), t, mcpTransport(), dir)
	defer sess.Close()

	res := addContext(t, sess, fArgs("p/f.go", nil))

	want := "function f already has context.Context as first parameter"
	if !res.IsError || errText(res) != want {
		t.Errorf("error = %q (IsError %v), want %q", errText(res), res.IsError, want)
	}
}

func TestAddContextParameter_RemovesInternalCtxCreationInBothSiblings(t *testing.T) {
	body := "func f(n int) int {\n\tctx := context.Background()\n\t_ = ctx\n\treturn n\n}\n"
	dir := writeInlineWorkspace(t, map[string]string{
		"p/f_default.go": "//go:build !tagx\n\npackage p\n\nimport \"context\"\n\n" + body,
		"p/f_tagx.go":    "//go:build tagx\n\npackage p\n\nimport \"context\"\n\n" + body,
	})
	sess := mcptest.Dial(context.Background(), t, mcpTransport(), dir)
	defer sess.Close()

	addContextOK(t, sess, fArgs("p/f_default.go", nil))

	for _, f := range []string{"p/f_default.go", "p/f_tagx.go"} {
		if got := fileText(t, dir, f); strings.Contains(got, "context.Background()") || strings.Count(got, "ctx context.Context") != 1 {
			t.Errorf("%s:\n%s", f, got)
		}
	}
	goVets(t, dir, "", "tagx")
}

func brokenTaggedTestWorkspace(t *testing.T) string {
	t.Helper()
	return writeInlineWorkspace(t, map[string]string{
		"p/f_default.go":     ctxFDefault,
		"p/f_tagx.go":        ctxFTagx,
		"p/bad_tagx_test.go": "//go:build tagx\n\npackage p\n\nimport (\n\t\"testing\"\n\n\t_ \"example.com/this-module-does-not-exist/thirdparty\"\n)\n\nfunc TestBad(t *testing.T) {\n\tf(3)\n}\n",
	})
}

func assertBrokenTaggedTestReported(t *testing.T, out planResultJSON) {
	t.Helper()
	if len(out.UncoveredFiles) != 1 {
		t.Fatalf("uncovered_files = %+v, want p/bad_tagx_test.go", out.UncoveredFiles)
	}
	u := out.UncoveredFiles[0]
	if u.File != "p/bad_tagx_test.go" || len(u.Configs) != 1 || u.Configs[0] != "tags=tagx" || !strings.Contains(u.Reason, "could not import") {
		t.Errorf("uncovered file = %+v", u)
	}
	if len(out.SkippedSites) != 1 || !strings.HasSuffix(out.SkippedSites[0].File, "p/bad_tagx_test.go") || out.SkippedSites[0].Line != 12 {
		t.Errorf("skipped_sites = %+v, want the f call at bad_tagx_test.go:12", out.SkippedSites)
	}
}

func TestAddContextParameter_ReportsBrokenTaggedTestFileAsUncovered(t *testing.T) {
	dir := brokenTaggedTestWorkspace(t)
	sess := mcptest.Dial(context.Background(), t, mcpTransport(), dir)
	defer sess.Close()

	preview := decodePlanResult(t, addContext(t, sess, fArgs("p/f_default.go", map[string]any{"dry_run": true})))
	assertUnchanged(t, dir, "p/f_default.go")
	applied := decodePlanResult(t, addContext(t, sess, fArgs("p/f_default.go", nil)))

	assertBrokenTaggedTestReported(t, preview)
	assertBrokenTaggedTestReported(t, applied)
	assertFileContains(t, dir, "p/f_tagx.go", "func f(ctx context.Context, n int) int")
	assertUnchanged(t, dir, "p/bad_tagx_test.go")
}

func TestAddContextParameter_SingleConfigOmitsBuildConfigs(t *testing.T) {
	dir := writeInlineWorkspace(t, map[string]string{
		"p/f.go": "package p\n\nfunc f(n int) int { return n }\n\nfunc Use() int { return f(1) }\n",
	})
	sess := mcptest.Dial(context.Background(), t, mcpTransport(), dir)
	defer sess.Close()

	for _, dryRun := range []bool{true, false} {
		out := addContextOK(t, sess, fArgs("p/f.go", map[string]any{"dry_run": dryRun}))
		if _, ok := out["build_configs"]; ok {
			t.Errorf("dry_run=%v: build_configs = %v, want the field omitted", dryRun, out["build_configs"])
		}
	}
}
