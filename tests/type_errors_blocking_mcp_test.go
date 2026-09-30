package tests_test

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/mamaar/gorefactor/tests/mcptest"
)

const teType = "package p\n\ntype T struct{}\n\nfunc (T) Run() {}\n\nfunc (T) Idle() {}\n"

func teWorkspace(t *testing.T) string {
	t.Helper()
	return writeInlineWorkspace(t, map[string]string{
		"p/t.go":        teType,
		"p/bad_tagx.go": teBrokenImport + "func Bad() { thirdparty.Make().Run() }\n",
	})
}

func TestSafeDelete_UnresolvedMentionInErroringProductionFileBlocksUnlessForced(t *testing.T) {
	dir := teWorkspace(t)
	sess := mcptest.Dial(context.Background(), t, mcpTransport(), dir)
	defer sess.Close()
	ctx := context.Background()

	res, text := callSafeDelete(ctx, t, sess, "T.Run", "p/t.go", false, false)

	if !res.IsError {
		t.Fatalf("safe_delete succeeded, want it refused: %s", text)
	}
	for _, want := range []string{"found 1 references", "p/bad_tagx.go:7", "tags=tagx", "could not import"} {
		if !strings.Contains(text, want) {
			t.Errorf("error %q does not contain %q", text, want)
		}
	}
	assertFileUnchanged(t, dir, "p/t.go", teType)

	forced, text := callSafeDelete(ctx, t, sess, "T.Run", "p/t.go", true, false)

	if forced.IsError {
		t.Fatalf("forced safe_delete failed: %s", text)
	}
	var out typeErrorsJSON
	if err := json.Unmarshal([]byte(text), &out); err != nil {
		t.Fatal(err)
	}
	if len(out.SkippedSites) != 1 || !strings.HasSuffix(out.SkippedSites[0].File, "p/bad_tagx.go") || out.SkippedSites[0].Line != 7 {
		t.Errorf("skipped_sites = %+v, want bad_tagx.go:7", out.SkippedSites)
	}
	if len(out.TypeErrors) != 1 || out.TypeErrors[0].Config != "tags=tagx" {
		t.Errorf("type_errors = %+v, want package p under tagx", out.TypeErrors)
	}
	if got := fileText(t, dir, "p/t.go"); strings.Contains(got, "Run") {
		t.Errorf("Run still declared after a forced delete:\n%s", got)
	}
}

func TestSafeDelete_ErroringProductionFileThatDoesNotMentionTheNameOnlyReports(t *testing.T) {
	dir := teWorkspace(t)
	sess := mcptest.Dial(context.Background(), t, mcpTransport(), dir)
	defer sess.Close()

	res, text := callSafeDelete(context.Background(), t, sess, "T.Idle", "p/t.go", false, false)

	if res.IsError {
		t.Fatalf("safe_delete refused: %s", text)
	}
	var out typeErrorsJSON
	if err := json.Unmarshal([]byte(text), &out); err != nil {
		t.Fatal(err)
	}
	if len(out.TypeErrors) != 1 || out.TypeErrors[0].Config != "tags=tagx" {
		t.Errorf("type_errors = %+v, want package p under tagx", out.TypeErrors)
	}
	if got := fileText(t, dir, "p/t.go"); strings.Contains(got, "Idle") {
		t.Errorf("Idle still declared:\n%s", got)
	}
}

func TestUnused_UnresolvedMentionInErroringProductionFileIsPossiblyUsed(t *testing.T) {
	out, _ := callUnused(t, map[string]string{
		"p/t.go":        "package p\n\ntype T struct{}\n\nfunc (T) run() {}\n\nfunc (T) idle() {}\n\nvar V T\n",
		"p/bad_tagx.go": teBrokenImport + "func Bad() { thirdparty.Make().run() }\n",
	})

	if got := declarations(out.Unused); len(got) != 1 || got[0] != "t.go:idle" {
		t.Errorf("unused = %v, want only idle", got)
	}
	if got := declarations(out.PossiblyUsed); len(got) != 1 || got[0] != "t.go:run" {
		t.Fatalf("possibly_used = %v, want run", got)
	}
	if use := out.PossiblyUsed[0].PossibleUse; use == nil || !strings.HasSuffix(use.File, "p/bad_tagx.go") || use.Line != 7 {
		t.Errorf("possible_use = %+v, want bad_tagx.go:7", use)
	}
}
