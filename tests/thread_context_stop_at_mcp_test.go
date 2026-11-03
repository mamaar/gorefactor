package tests_test

import (
	"context"
	"strings"
	"testing"

	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/mamaar/gorefactor/tests/mcptest"
)

func stopAtArgs(stopAt ...string) map[string]any {
	args := threadArgs("leaf", false)
	args["stop_at"] = stopAt
	return args
}

func callExpectingError(t *testing.T, sess *mcptest.Session, args map[string]any) string {
	t.Helper()
	res, err := sess.CallTool(context.Background(), &mcpsdk.CallToolParams{Name: "add_context_parameter", Arguments: args})
	if err != nil {
		t.Fatalf("CallTool: %v", err)
	}
	if !res.IsError {
		t.Fatalf("want an error result, got: %s", errText(res))
	}
	return errText(res)
}

func assertUntouched(t *testing.T, dir string, files ...string) {
	t.Helper()
	for _, f := range files {
		got := fileText(t, dir, f)
		if strings.Contains(got, "context") {
			t.Errorf("%s was modified:\n%s", f, got)
		}
	}
}

func TestAddContextParameter_StopAtWithoutThreadRejected(t *testing.T) {
	dir := taggedSiblingWorkspace(t)
	sess := mcptest.Dial(context.Background(), t, mcpTransport(), dir)
	defer sess.Close()

	args := stopAtArgs("open")
	delete(args, "thread")
	msg := callExpectingError(t, sess, args)
	if !strings.Contains(msg, "stop_at") {
		t.Errorf("error should mention stop_at, got: %s", msg)
	}
	assertUntouched(t, dir, "p/leaf.go", "p/use.go")
}

func TestAddContextParameter_StopAtUnknownErrorsWithoutWriting(t *testing.T) {
	dir := taggedSiblingWorkspace(t)
	sess := mcptest.Dial(context.Background(), t, mcpTransport(), dir)
	defer sess.Close()

	msg := callExpectingError(t, sess, stopAtArgs("nope"))
	if !strings.Contains(msg, "nope") {
		t.Errorf("error should name the entry, got: %s", msg)
	}
	assertUntouched(t, dir, "p/leaf.go", "p/use.go", "p/open_default.go", "p/open_tagx.go")
}

func TestAddContextParameter_StopAtUnknownErrorsWhenSeedIsInOneConfigOnly(t *testing.T) {
	dir := writeInlineWorkspace(t, map[string]string{
		"p/leaf_tagx.go": "//go:build tagx\n\npackage p\n\nfunc leaf() {}\n\nfunc Use() { leaf() }\n",
		"p/other.go":     "package p\n\nfunc Other() {}\n",
	})
	sess := mcptest.Dial(context.Background(), t, mcpTransport(), dir)
	defer sess.Close()

	args := stopAtArgs("nope")
	args["source_file"] = "p/leaf_tagx.go"
	msg := callExpectingError(t, sess, args)
	if !strings.Contains(msg, "nope") {
		t.Errorf("error should name the entry, got: %s", msg)
	}
	assertUntouched(t, dir, "p/leaf_tagx.go")
}

func TestAddContextParameter_StopAtHonoursStopDeclaredInOneConfigOnly(t *testing.T) {
	dir := writeInlineWorkspace(t, map[string]string{
		"p/open_default.go": "//go:build !tagx\n\npackage p\n\nfunc open(name string) error { return leaf(name) }\n",
		"p/open_tagx.go":    "//go:build tagx\n\npackage p\n\nfunc open(name string) error { return tagOnly(name) }\n",
		"p/only_tagx.go":    "//go:build tagx\n\npackage p\n\nfunc tagOnly(name string) error { return leaf(name) }\n",
		"p/leaf.go":         "package p\n\nfunc leaf(name string) error { return nil }\n",
		"p/use.go":          "package p\n\nfunc Use(name string) error { return open(name) }\n",
	})
	sess := mcptest.Dial(context.Background(), t, mcpTransport(), dir)
	defer sess.Close()

	out := callJSON(t, sess, "add_context_parameter", stopAtArgs("tagOnly"))

	got := fileText(t, dir, "p/only_tagx.go")
	if !strings.Contains(got, "func tagOnly(name string) error") || !strings.Contains(got, "leaf(context.TODO(), name)") {
		t.Errorf("only_tagx.go:\n%s", got)
	}
	sites, _ := out["skipped_sites"].([]any)
	var stops int
	for _, s := range sites {
		r, _ := s.(map[string]any)["reason"].(string)
		if strings.HasPrefix(r, "stop_at:") {
			stops++
		}
	}
	if stops != 1 {
		t.Errorf("skipped_sites lists %d stop_at entries, want exactly 1: %v", stops, sites)
	}
	goBuilds(t, dir, "", "tagx")
}
