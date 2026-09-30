package tests_test

import (
	"context"
	"strings"
	"testing"

	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/mamaar/gorefactor/tests/mcptest"
)

const sdcF = "package p\n\nfunc f(n int) int { return n }\n"
const sdcPlain = "package p\n\nfunc Plain() int { return 1 }\n"
const sdcUseTagx = "//go:build tagx\n\npackage p\n\nfunc Use() int { return f(1) }\n"

// safeDeleteDeclined calls safe_delete with a client that declines the
// confirmation dialog and returns the result and the dialog's message.
func safeDeleteDeclined(t *testing.T, dir, symbol, file string) (*mcpsdk.CallToolResult, string) {
	t.Helper()
	var message string
	handler, _ := countingHandler(func(req *mcpsdk.ElicitRequest) *mcpsdk.ElicitResult {
		message = req.Params.Message
		return &mcpsdk.ElicitResult{Action: "decline"}
	})
	ctx := context.Background()
	sess := mcptest.Dial(ctx, t, mcptest.InProcessWithElicitation(handler), dir)
	defer sess.Close()
	res, _ := callSafeDelete(ctx, t, sess, symbol, file, false, false)
	return res, message
}

func TestSafeDelete_ReferenceOnlyInTaggedFileBlocks(t *testing.T) {
	for name, use := range map[string]struct{ file, content, site string }{
		"production file": {"p/use_tagx.go", sdcUseTagx, "p/use_tagx.go:5 [tags=tagx]"},
		"test file": {"p/use_tagx_test.go",
			"//go:build tagx\n\npackage p\n\nimport \"testing\"\n\nfunc TestTagx(t *testing.T) { f(2) }\n",
			"p/use_tagx_test.go:7 [tags=tagx]"},
	} {
		t.Run(name, func(t *testing.T) {
			dir := writeInlineWorkspace(t, map[string]string{"p/f.go": sdcF, use.file: use.content})

			res, message := safeDeleteDeclined(t, dir, "f", "p/f.go")

			if !res.IsError {
				t.Errorf("safe_delete succeeded, want it refused")
			}
			if !strings.Contains(message, "1 reference:") || !strings.Contains(message, use.site) {
				t.Errorf("dialog = %q, want one reference at %s", message, use.site)
			}
			assertFileUnchanged(t, dir, "p/f.go", sdcF)

			sess := mcptest.Dial(context.Background(), t, mcpTransport(), dir)
			defer sess.Close()
			res, text := callSafeDelete(context.Background(), t, sess, "f", "p/f.go", false, true)
			if !res.IsError || !strings.Contains(text, "found 1 references") || !strings.Contains(text, use.site) {
				t.Errorf("dry run = %q (IsError %v), want 1 reference at %s", text, res.IsError, use.site)
			}
		})
	}
}

func TestSafeDelete_UnreferencedTagSiblingsAreBothDeleted(t *testing.T) {
	dir := writeInlineWorkspace(t, map[string]string{
		"p/f_default.go": ctxFDefault,
		"p/f_tagx.go":    ctxFTagx,
		"p/plain.go":     sdcPlain,
	})
	sess := mcptest.Dial(context.Background(), t, mcpTransport(), dir)
	defer sess.Close()

	out := callJSON(t, sess, "safe_delete", map[string]any{"symbol": "f", "source_file": "p/f_default.go"})

	if configs, _ := out["build_configs"].([]any); len(configs) != 2 {
		t.Errorf("build_configs = %v, want 2 entries", out["build_configs"])
	}
	for _, f := range []string{"p/f_default.go", "p/f_tagx.go"} {
		if got := fileText(t, dir, f); strings.Contains(got, "func f") {
			t.Errorf("%s still declares f:\n%s", f, got)
		}
	}
	goVets(t, dir, "", "tagx")
}

func TestSafeDelete_SiblingReferencedOnlyUnderItsTagBlocksBoth(t *testing.T) {
	dir := writeInlineWorkspace(t, map[string]string{
		"p/f_default.go": ctxFDefault,
		"p/f_tagx.go":    ctxFTagx,
		"p/use_tagx.go":  sdcUseTagx,
	})

	res, message := safeDeleteDeclined(t, dir, "f", "p/f_default.go")

	if !res.IsError || !strings.Contains(message, "p/use_tagx.go:5 [tags=tagx]") {
		t.Errorf("IsError %v, dialog = %q, want the tagged call listed", res.IsError, message)
	}
	assertFileUnchanged(t, dir, "p/f_default.go", ctxFDefault)
	assertFileUnchanged(t, dir, "p/f_tagx.go", ctxFTagx)
}

func TestSafeDelete_SymbolDeclaredOnlyUnderTag(t *testing.T) {
	t.Run("unreferenced is deleted", func(t *testing.T) {
		dir := writeInlineWorkspace(t, map[string]string{"p/f_tagx.go": ctxFTagx, "p/plain.go": sdcPlain})
		sess := mcptest.Dial(context.Background(), t, mcpTransport(), dir)
		defer sess.Close()

		callJSON(t, sess, "safe_delete", map[string]any{"symbol": "f", "source_file": "p/f_tagx.go"})

		if got := fileText(t, dir, "p/f_tagx.go"); strings.Contains(got, "func f") {
			t.Errorf("f_tagx.go still declares f:\n%s", got)
		}
		goVets(t, dir, "", "tagx")
	})
	t.Run("referenced under the tag is refused", func(t *testing.T) {
		dir := writeInlineWorkspace(t, map[string]string{"p/f_tagx.go": ctxFTagx, "p/plain.go": sdcPlain, "p/use_tagx.go": sdcUseTagx})
		sess := mcptest.Dial(context.Background(), t, mcpTransport(), dir)
		defer sess.Close()

		res, text := callSafeDelete(context.Background(), t, sess, "f", "p/f_tagx.go", false, false)

		if !res.IsError || !strings.Contains(text, "found 1 references") {
			t.Errorf("result = %q (IsError %v), want 1 blocking reference", text, res.IsError)
		}
		assertFileUnchanged(t, dir, "p/f_tagx.go", ctxFTagx)
	})
}

func TestSafeDelete_MentionInUncheckedFileBlocksUnlessForced(t *testing.T) {
	dir := brokenTaggedTestWorkspace(t)
	sess := mcptest.Dial(context.Background(), t, mcpTransport(), dir)
	defer sess.Close()
	ctx := context.Background()

	res, text := callSafeDelete(ctx, t, sess, "f", "p/f_default.go", false, false)

	if !res.IsError {
		t.Fatalf("safe_delete succeeded, want it refused: %s", text)
	}
	for _, want := range []string{"found 1 references", "p/bad_tagx_test.go:12", "tags=tagx", "could not import"} {
		if !strings.Contains(text, want) {
			t.Errorf("error %q does not contain %q", text, want)
		}
	}
	assertFileUnchanged(t, dir, "p/f_default.go", ctxFDefault)
	assertFileUnchanged(t, dir, "p/f_tagx.go", ctxFTagx)

	forced, _ := callSafeDelete(ctx, t, sess, "f", "p/f_default.go", true, false)

	assertBrokenTaggedTestReported(t, decodePlanResult(t, forced))
	for _, f := range []string{"p/f_default.go", "p/f_tagx.go"} {
		if got := fileText(t, dir, f); strings.Contains(got, "func f") {
			t.Errorf("%s still declares f after a forced delete:\n%s", f, got)
		}
	}
}

func TestSafeDelete_TypeTakesMethodDeclaredOnlyUnderTag(t *testing.T) {
	dir := writeInlineWorkspace(t, map[string]string{
		"p/t.go":      "package p\n\ntype T struct{}\n\nfunc (T) A() {}\n",
		"p/t_tagx.go": "//go:build tagx\n\npackage p\n\nfunc (T) B() {}\n",
		"p/plain.go":  sdcPlain,
	})
	sess := mcptest.Dial(context.Background(), t, mcpTransport(), dir)
	defer sess.Close()

	callJSON(t, sess, "safe_delete", map[string]any{"symbol": "T", "source_file": "p/t.go"})

	for _, f := range []string{"p/t.go", "p/t_tagx.go"} {
		if got := fileText(t, dir, f); strings.Contains(got, "T") {
			t.Errorf("%s still mentions T:\n%s", f, got)
		}
	}
	goVets(t, dir, "", "tagx")
}

func TestSafeDelete_FieldReferencedOnlyInTaggedKeyedLiteralBlocks(t *testing.T) {
	const model = "package p\n\ntype T struct {\n\tA int\n\tB int\n}\n\nfunc Get(t T) int { return t.A }\n"
	dir := writeInlineWorkspace(t, map[string]string{
		"p/t.go":        model,
		"p/lit_tagx.go": "//go:build tagx\n\npackage p\n\nvar V = T{B: 1}\n",
	})
	sess := mcptest.Dial(context.Background(), t, mcpTransport(), dir)
	defer sess.Close()

	res, text := callSafeDelete(context.Background(), t, sess, "T.B", "p/t.go", false, false)

	if !res.IsError || !strings.Contains(text, "found 1 references") || !strings.Contains(text, "p/lit_tagx.go:5 [tags=tagx]") {
		t.Errorf("result = %q (IsError %v), want the tagged literal key to block", text, res.IsError)
	}
	assertFileUnchanged(t, dir, "p/t.go", model)
}

func TestSafeDelete_SingleConfigRefusalAndResultAreUnchanged(t *testing.T) {
	dir := writeInlineWorkspace(t, map[string]string{
		"p/f.go":   sdcF + "\nfunc g() {}\n",
		"p/use.go": "package p\n\nfunc Use() int { return f(1) }\n",
	})
	sess := mcptest.Dial(context.Background(), t, mcpTransport(), dir)
	defer sess.Close()

	res, text := callSafeDelete(context.Background(), t, sess, "f", "p/f.go", false, false)
	if !res.IsError || text != wantExactBlockedError("f", 1) {
		t.Errorf("error = %q (IsError %v), want %q", text, res.IsError, wantExactBlockedError("f", 1))
	}

	for _, dryRun := range []bool{true, false} {
		out := callJSON(t, sess, "safe_delete", map[string]any{"symbol": "g", "source_file": "p/f.go", "dry_run": dryRun})
		if _, ok := out["build_configs"]; ok {
			t.Errorf("dry_run=%v: build_configs = %v, want the field omitted", dryRun, out["build_configs"])
		}
	}
	assertFileUnchanged(t, dir, "p/f.go", sdcF)
}
