package tests_test

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/mamaar/gorefactor/tests/mcptest"
)

type typeErrorsJSON struct {
	TypeErrors []struct {
		Package string `json:"package"`
		Config  string `json:"config"`
		Error   string `json:"error"`
	} `json:"type_errors"`
	SkippedSites []struct {
		File string `json:"file"`
		Line int    `json:"line"`
	} `json:"skipped_sites"`
}

const teBrokenImport = "//go:build tagx\n\npackage p\n\nimport \"example.com/this-module-does-not-exist/thirdparty\"\n\n"

func TestRenameSymbol_ReportsProductionTypeErrorsOfAReachedPackage(t *testing.T) {
	dir := writeInlineWorkspace(t, map[string]string{
		"p/f.go":        "package p\n\nfunc f(n int) int { return n }\n\nfunc Use() int { return f(1) }\n",
		"p/bad_tagx.go": teBrokenImport + "func Bad() int { return thirdparty.Value }\n",
	})
	sess := mcptest.Dial(context.Background(), t, mcpTransport(), dir)
	defer sess.Close()

	res := renameSymbol(t, sess, renameFToG(nil))

	if res.IsError {
		t.Fatalf("rename_symbol failed: %s", errText(res))
	}
	var out typeErrorsJSON
	err := json.Unmarshal([]byte(errText(res)), &out)
	if err != nil {
		t.Fatal(err)
	}
	if len(out.TypeErrors) != 1 {
		t.Fatalf("type_errors = %+v, want one entry for package p under tagx", out.TypeErrors)
	}
	te := out.TypeErrors[0]
	if te.Package != "example.com/threadmcp/p" || te.Config != "tags=tagx" || !strings.Contains(te.Error, "bad_tagx.go") || !strings.Contains(te.Error, "could not import") {
		t.Errorf("type_errors[0] = %+v", te)
	}
	assertFileContains(t, dir, "p/f.go", "func g(n int) int")
}

func TestRenameSymbol_NoTypeErrorsOmitsTheField(t *testing.T) {
	dir := writeInlineWorkspace(t, map[string]string{
		"p/f.go": "package p\n\nfunc f(n int) int { return n }\n\nfunc Use() int { return f(1) }\n",
	})
	sess := mcptest.Dial(context.Background(), t, mcpTransport(), dir)
	defer sess.Close()

	out := callJSON(t, sess, "rename_symbol", renameFToG(nil))

	_, ok := out["type_errors"]
	if ok {
		t.Errorf("type_errors = %v, want the field omitted", out["type_errors"])
	}
}
