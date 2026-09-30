package tests_test

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/mamaar/gorefactor/tests/mcptest"
)

func TestAnalyzeDependencies_SuggestedMovesNameSymbolAndReferrers(t *testing.T) {
	dir := mbdWorkspace(t, nil)
	sess := mcptest.Dial(context.Background(), t, mcpTransport(), dir)
	defer sess.Close()

	out := callJSON(t, sess, "analyze_dependencies", map[string]any{"suggest_moves": true})

	moves, _ := out["suggested_moves"].([]any)
	if len(moves) != 1 {
		t.Fatalf("suggested_moves = %v, want one move", out["suggested_moves"])
	}
	raw, _ := json.Marshal(moves[0])
	for _, want := range []string{`"Shared"`, "threadmcp/src", "threadmcp/a", "threadmcp/b"} {
		if !strings.Contains(string(raw), want) {
			t.Errorf("move = %s, want it to mention %q", raw, want)
		}
	}
	if out["package_coupling"] == nil {
		t.Errorf("package_coupling missing from %v", out)
	}
	assertFileUnchanged(t, dir, "src/src.go", mbdSrc)
}

func TestAnalyzeDependencies_NoSharedSymbolsGivesEmptyList(t *testing.T) {
	dir := writeInlineWorkspace(t, map[string]string{
		"a/a.go": "package a\n\nfunc A() int { return 1 }\n",
		"b/b.go": "package b\n\nfunc B() int { return 2 }\n",
	})
	sess := mcptest.Dial(context.Background(), t, mcpTransport(), dir)
	defer sess.Close()

	out := callJSON(t, sess, "analyze_dependencies", map[string]any{"suggest_moves": true, "detect_backwards": true})

	for _, key := range []string{"suggested_moves", "backwards_dependencies"} {
		list, ok := out[key].([]any)
		if !ok || len(list) != 0 {
			t.Errorf("%s = %#v, want an empty list", key, out[key])
		}
	}
}

func TestAnalyzeDependencies_FlagsGateTheirSections(t *testing.T) {
	dir := mbdWorkspace(t, nil)
	sess := mcptest.Dial(context.Background(), t, mcpTransport(), dir)
	defer sess.Close()

	out := callJSON(t, sess, "analyze_dependencies", map[string]any{})

	for _, key := range []string{"suggested_moves", "backwards_dependencies"} {
		if _, ok := out[key]; ok {
			t.Errorf("%s present without its flag: %v", key, out[key])
		}
	}
}

func TestAnalyzeDependencies_InnerImportingOuterIsBackwardsDependency(t *testing.T) {
	dir := writeInlineWorkspace(t, map[string]string{
		"domain/d.go":  "package domain\n\nimport \"example.com/threadmcp/handler\"\n\nfunc D() int { return handler.H() }\n",
		"handler/h.go": "package handler\n\nfunc H() int { return 1 }\n",
	})
	sess := mcptest.Dial(context.Background(), t, mcpTransport(), dir)
	defer sess.Close()

	out := callJSON(t, sess, "analyze_dependencies", map[string]any{"detect_backwards": true})

	deps, _ := out["backwards_dependencies"].([]any)
	if len(deps) != 1 {
		t.Fatalf("backwards_dependencies = %v, want one", out["backwards_dependencies"])
	}
	raw, _ := json.Marshal(deps[0])
	for _, want := range []string{"threadmcp/domain", "threadmcp/handler"} {
		if !strings.Contains(string(raw), want) {
			t.Errorf("dependency = %s, want it to mention %q", raw, want)
		}
	}
}
