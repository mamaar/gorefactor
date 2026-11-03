package tests_test

import (
	"context"
	"maps"
	"strings"
	"testing"

	"github.com/mamaar/gorefactor/tests/mcptest"
)

const mvSrc = "package src\n\nfunc Helper() int { return 1 }\n\nfunc Stay() int { return 2 }\n"
const mvDst = "package dst\n\nfunc Existing() int { return 3 }\n"

func moveHelperArgs() map[string]any {
	return map[string]any{"symbol": "Helper", "from_package": "src", "to_package": "dst"}
}

func mvWorkspace(t *testing.T, extra map[string]string) string {
	t.Helper()
	files := map[string]string{"src/src.go": mvSrc, "dst/dst.go": mvDst}
	maps.Copy(files, extra)
	return writeInlineWorkspace(t, files)
}

func TestMoveSymbol_ReferenceOnlyInTaggedFileRefusesNamingIt(t *testing.T) {
	const use = "//go:build tagx\n\npackage use\n\nimport \"example.com/threadmcp/src\"\n\nfunc Tagged() int { return src.Helper() }\n"
	dir := mvWorkspace(t, map[string]string{"use/use.go": "package use\n", "use/use_tagx.go": use})
	sess := mcptest.Dial(context.Background(), t, mcpTransport(), dir)
	defer sess.Close()

	res := callTool(t, sess, "move_symbol", moveHelperArgs())

	msg := errText(res)
	if !res.IsError || !strings.Contains(msg, "use/use_tagx.go:7") || !strings.Contains(msg, "tags=tagx") {
		t.Errorf("error = %q (IsError %v), want a refusal naming use/use_tagx.go:7 under tags=tagx", msg, res.IsError)
	}
	assertFileUnchanged(t, dir, "src/src.go", mvSrc)
	assertFileUnchanged(t, dir, "dst/dst.go", mvDst)
	goVets(t, dir, "", "tagx")
}

func TestMoveSymbol_SiblingDeclarationsRefuseNamingThem(t *testing.T) {
	dir := mvWorkspace(t, map[string]string{
		"src/h_default.go": "//go:build !tagx\n\npackage src\n\nfunc Sib() int { return 1 }\n",
		"src/h_tagx.go":    "//go:build tagx\n\npackage src\n\nfunc Sib() int { return 2 }\n",
	})
	sess := mcptest.Dial(context.Background(), t, mcpTransport(), dir)
	defer sess.Close()

	res := callTool(t, sess, "move_symbol", map[string]any{"symbol": "Sib", "from_package": "src", "to_package": "dst"})

	msg := errText(res)
	if !res.IsError || !strings.Contains(msg, "src/h_tagx.go") || !strings.Contains(msg, "tags=tagx") {
		t.Errorf("error = %q (IsError %v), want a refusal naming src/h_tagx.go under tags=tagx", msg, res.IsError)
	}
	assertFileUnchanged(t, dir, "dst/dst.go", mvDst)
	goVets(t, dir, "", "tagx")
}

func TestMoveSymbol_TypeWithMethodInTaggedFileRefusesNamingIt(t *testing.T) {
	const typ = "package src\n\ntype Box struct{ N int }\n"
	dir := mvWorkspace(t, map[string]string{
		"src/box.go":      typ,
		"src/box_tagx.go": "//go:build tagx\n\npackage src\n\nfunc (b Box) Twice() int { return b.N * 2 }\n",
	})
	sess := mcptest.Dial(context.Background(), t, mcpTransport(), dir)
	defer sess.Close()

	res := callTool(t, sess, "move_symbol", map[string]any{"symbol": "Box", "from_package": "src", "to_package": "dst"})

	msg := errText(res)
	if !res.IsError || !strings.Contains(msg, "src/box_tagx.go:5") || !strings.Contains(msg, "tags=tagx") {
		t.Errorf("error = %q (IsError %v), want a refusal naming src/box_tagx.go:5 under tags=tagx", msg, res.IsError)
	}
	assertFileUnchanged(t, dir, "src/box.go", typ)
}

func TestMoveSymbol_DeclaredOnlyUnderTagRefusesNamingTheConfig(t *testing.T) {
	dir := mvWorkspace(t, map[string]string{"src/only_tagx.go": "//go:build tagx\n\npackage src\n\nfunc Only() int { return 1 }\n"})
	sess := mcptest.Dial(context.Background(), t, mcpTransport(), dir)
	defer sess.Close()

	res := callTool(t, sess, "move_symbol", map[string]any{"symbol": "Only", "from_package": "src", "to_package": "dst"})

	msg := errText(res)
	if !res.IsError || !strings.Contains(msg, "src/only_tagx.go") || !strings.Contains(msg, "tags=tagx") {
		t.Errorf("error = %q (IsError %v), want a refusal naming src/only_tagx.go under tags=tagx", msg, res.IsError)
	}
}

func TestMoveSymbol_NameTakenInTargetOnlyUnderTagRefuses(t *testing.T) {
	dir := mvWorkspace(t, map[string]string{"dst/taken_tagx.go": "//go:build tagx\n\npackage dst\n\nfunc Helper() int { return 9 }\n"})
	sess := mcptest.Dial(context.Background(), t, mcpTransport(), dir)
	defer sess.Close()

	res := callTool(t, sess, "move_symbol", moveHelperArgs())

	msg := errText(res)
	if !res.IsError || !strings.Contains(msg, "already exists") || !strings.Contains(msg, "tags=tagx") {
		t.Errorf("error = %q (IsError %v), want a name conflict under tags=tagx", msg, res.IsError)
	}
	assertFileUnchanged(t, dir, "src/src.go", mvSrc)
}

func TestMoveSymbol_UnrelatedTaggedFilesDoNotBlockAndReferencesAreRewritten(t *testing.T) {
	const use = "package use\n\nimport \"example.com/threadmcp/src\"\n\nfunc Shared() int { return src.Helper() }\n"
	dir := mvWorkspace(t, map[string]string{
		"use/use.go":      use,
		"use/use_tagx.go": "//go:build tagx\n\npackage use\n\nfunc Tagged() int { return Shared() }\n",
	})
	sess := mcptest.Dial(context.Background(), t, mcpTransport(), dir)
	defer sess.Close()

	out := callJSON(t, sess, "move_symbol", moveHelperArgs())

	configs, _ := out["build_configs"].([]any)
	if len(configs) != 2 {
		t.Errorf("build_configs = %v, want 2 entries", out["build_configs"])
	}
	assertFileContains(t, dir, "use/use.go", "dst.Helper()")
	goVets(t, dir, "", "tagx")
}

func TestMoveSymbol_SingleConfigOmitsBuildConfigsAndKeepsErrorText(t *testing.T) {
	dir := mvWorkspace(t, nil)
	sess := mcptest.Dial(context.Background(), t, mcpTransport(), dir)
	defer sess.Close()

	res := callTool(t, sess, "move_symbol", map[string]any{"symbol": "Nope", "from_package": "src", "to_package": "dst"})
	want := "move operation validation failed: symbol not found: Nope"
	if !res.IsError || errText(res) != want {
		t.Errorf("error = %q (IsError %v), want %q", errText(res), res.IsError, want)
	}

	out := callJSON(t, sess, "move_symbol", moveHelperArgs())
	_, ok := out["build_configs"]
	if ok {
		t.Errorf("build_configs = %v, want the field omitted", out["build_configs"])
	}
	assertFileContains(t, dir, "dst/dst.go", "func Helper() int { return 1 }")
}
