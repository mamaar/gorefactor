package tests_test

import (
	"context"
	"encoding/json"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/mamaar/gorefactor/tests/mcptest"
)

func findImplementations(t *testing.T, files map[string]string, args map[string]any) (map[string]any, string) {
	t.Helper()
	files["go.mod"] = "module example.com/fimpl\n\ngo 1.21\n"
	dir := writeInlineWorkspace(t, files)
	sess := mcptest.Dial(context.Background(), t, mcpTransport(), dir)
	defer sess.Close()
	res, err := sess.CallTool(context.Background(), &mcpsdk.CallToolParams{Name: "find_implementations", Arguments: args})
	if err != nil {
		t.Fatalf("find_implementations: %v", err)
	}
	if res.IsError {
		return nil, errText(res)
	}
	var out map[string]any
	if err := json.Unmarshal([]byte(errText(res)), &out); err != nil {
		t.Fatalf("decode: %v", err)
	}
	return out, ""
}

// entryNames returns "Name" or "*Name" (pointer receiver needed) for each entry under key.
func entryNames(t *testing.T, out map[string]any, key string) []string {
	t.Helper()
	var names []string
	items, _ := out[key].([]any)
	for _, it := range items {
		e := it.(map[string]any)
		name := e["name"].(string)
		if ptr, _ := e["pointer_receiver"].(bool); ptr {
			name = "*" + name
		}
		names = append(names, name)
	}
	slices.Sort(names)
	return names
}

const fimplIface = "package p\n\ntype Runner interface{ Run(n int) int }\n"

func TestFindImplementations_InterfaceListsValueAndPointerImplementersOnly(t *testing.T) {
	out, errMsg := findImplementations(t, map[string]string{
		"p/iface.go": fimplIface,
		"p/a.go":     "package p\n\ntype A struct{}\n\nfunc (A) Run(n int) int { return n }\n",
		"p/b.go":     "package p\n\ntype B struct{}\n\nfunc (*B) Run(n int) int { return n }\n",
		"p/c.go":     "package p\n\ntype C struct{}\n\nfunc (C) Run(n string) int { return 0 }\n",
		"p/d.go":     "package p\n\ntype D struct{}\n\nfunc (D) Walk(n int) int { return n }\n",
	}, map[string]any{"package": "p", "type": "Runner"})
	if errMsg != "" {
		t.Fatal(errMsg)
	}
	got := entryNames(t, out, "implementations")
	if want := []string{"*B", "A"}; !slices.Equal(got, want) {
		t.Errorf("implementations = %v, want %v", got, want)
	}
}

func TestFindImplementations_InterfaceWithMethodLocatesTheImplementingMethods(t *testing.T) {
	out, errMsg := findImplementations(t, map[string]string{
		"p/iface.go": fimplIface,
		"p/a.go":     "package p\n\ntype A struct{}\n\nfunc (A) Run(n int) int { return n }\n",
	}, map[string]any{"package": "p", "type": "Runner", "method": "Run"})
	if errMsg != "" {
		t.Fatal(errMsg)
	}
	e := out["implementations"].([]any)[0].(map[string]any)
	if filepath.Base(e["file"].(string)) != "a.go" || e["line"].(float64) != 5 {
		t.Errorf("method location = %v:%v, want a.go:5", e["file"], e["line"])
	}
}

func TestFindImplementations_ConcreteTypeListsWorkspaceAndExternalInterfaces(t *testing.T) {
	out, errMsg := findImplementations(t, map[string]string{
		"p/iface.go": fimplIface,
		"p/w.go": "package p\n\nimport \"io\"\n\ntype W struct{}\n\n" +
			"func (*W) Write(b []byte) (int, error) { return len(b), nil }\n" +
			"func (*W) Run(n int) int { return n }\n\n" +
			"var _ io.Writer = (*W)(nil)\n",
	}, map[string]any{"package": "p", "type": "W"})
	if errMsg != "" {
		t.Fatal(errMsg)
	}
	got := entryNames(t, out, "interfaces")
	for _, want := range []string{"*Runner", "*Writer"} {
		if !slices.Contains(got, want) {
			t.Errorf("interfaces = %v, missing %s", got, want)
		}
	}
	for _, it := range out["interfaces"].([]any) {
		e := it.(map[string]any)
		if e["name"] == "Writer" && (e["package"] != "io" || e["external"] != true) {
			t.Errorf("io.Writer entry = %v, want package io, external", e)
		}
	}
}

func TestFindImplementations_FindsTypeDeclaredOnlyUnderAnotherConfig(t *testing.T) {
	out, errMsg := findImplementations(t, map[string]string{
		"p/iface.go":   fimplIface,
		"p/a.go":       "package p\n\ntype A struct{}\n\nfunc (A) Run(n int) int { return n }\n",
		"p/b_linux.go": "package p\n\ntype B struct{}\n\nfunc (B) Run(n int) int { return n }\n",
		"p/b_other.go": "//go:build !linux\n\npackage p\n\nfunc other() {}\n",
	}, map[string]any{"package": "p", "type": "Runner"})
	if errMsg != "" {
		t.Fatal(errMsg)
	}
	if got, want := entryNames(t, out, "implementations"), []string{"A", "B"}; !slices.Equal(got, want) {
		t.Errorf("implementations = %v, want %v", got, want)
	}
	for _, it := range out["implementations"].([]any) {
		e := it.(map[string]any)
		_, hasConfigs := e["configs"]
		if (e["name"] == "B") != hasConfigs {
			t.Errorf("entry %v: configs listed = %v, want only for B", e["name"], hasConfigs)
		}
	}
}

func TestFindImplementations_AmbiguousTypeNameListsCandidates(t *testing.T) {
	_, errMsg := findImplementations(t, map[string]string{
		"p/a.go": "package p\n\ntype T struct{}\n",
		"q/a.go": "package q\n\ntype T struct{}\n",
	}, map[string]any{"type": "T"})
	if !strings.Contains(errMsg, "p") || !strings.Contains(errMsg, "q") || !strings.Contains(errMsg, "package") {
		t.Errorf("want an error listing both packages, got %q", errMsg)
	}
}

func TestFindImplementations_UnknownMethodOnConcreteTypeIsAnError(t *testing.T) {
	_, errMsg := findImplementations(t, map[string]string{
		"p/a.go": "package p\n\ntype A struct{}\n\nfunc (A) Run() {}\n",
	}, map[string]any{"package": "p", "type": "A", "method": "Nope"})
	if !strings.Contains(errMsg, "Nope") {
		t.Errorf("want an error naming the method, got %q", errMsg)
	}
}
