package tests_test

import (
	"context"
	"encoding/json"
	"path/filepath"
	"testing"

	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/mamaar/gorefactor/tests/mcptest"
)

func TestReachableReferences_ReportsInterfaceReachedSiteAndOmitsUnreachableOne(t *testing.T) {
	dir := writeInlineWorkspace(t, map[string]string{
		"main.go": "package main\n\nimport \"example.com/threadmcp/p\"\n\nfunc main() { var d p.Doer = p.Impl{}; d.Do() }\n",
		"p/p.go":  "package p\n\ntype Doer interface{ Do() }\n\ntype Impl struct{}\n\nfunc (Impl) Do() { Log() }\n\nfunc Log() {}\n\nfunc Dead() { Log() }\n",
	})

	sess := mcptest.Dial(context.Background(), t, mcpTransport(), dir)
	defer sess.Close()

	res, err := sess.CallTool(context.Background(), &mcpsdk.CallToolParams{Name: "reachable_references", Arguments: map[string]any{
		"entry_package": "example.com/threadmcp",
		"targets":       []string{"example.com/threadmcp/p.Log"},
	}})

	if err != nil || res.IsError {
		t.Fatalf("reachable_references: %v %s", err, errText(res))
	}

	var out struct {
		Sites []struct {
			File      string   `json:"file"`
			Enclosing string   `json:"enclosing"`
			Target    string   `json:"target"`
			Kind      string   `json:"kind"`
			Path      []string `json:"path"`
		} `json:"sites"`
		Count   int `json:"count"`
		Summary struct {
			ByPackage map[string]int `json:"by_package"`
			ByTarget  map[string]int `json:"by_target"`
		} `json:"summary"`
	}

	err = json.Unmarshal([]byte(errText(res)), &out)
	if err != nil {
		t.Fatalf("decode: %v\n%s", err, errText(res))
	}

	if out.Count != 1 || len(out.Sites) != 1 {
		t.Fatalf("sites = %+v, want only the interface-reached one", out.Sites)
	}

	s := out.Sites[0]
	if filepath.Base(s.File) != "p.go" || s.Enclosing != "Impl.Do" || s.Target != "p.Log" || s.Kind != "call" || s.Path[0] != "main.main" {
		t.Errorf("site = %+v", s)
	}

	if out.Summary.ByTarget["p.Log"] != 1 || out.Summary.ByPackage["example.com/threadmcp/p"] != 1 {
		t.Errorf("summary = %+v", out.Summary)
	}
}
