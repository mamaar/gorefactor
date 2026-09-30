package tests_test

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/mamaar/gorefactor/tests/mcptest"
)

func taggedSiblingWorkspace(t *testing.T) string {
	t.Helper()
	return writeInlineWorkspace(t, map[string]string{
		"p/open_default.go": "//go:build !tagx\n\npackage p\n\nfunc open(name string) error { helper(); return nil }\n",
		"p/open_tagx.go":    "//go:build tagx\n\npackage p\n\nfunc open(name string) error { helper(); return leaf(name) }\n",
		"p/leaf.go":         "package p\n\nfunc leaf(name string) error { return nil }\n\nfunc helper() {}\n",
		"p/use.go":          "package p\n\nfunc Use(name string) error { return open(name) }\n",
	})
}

func threadArgs(fn string, dryRun bool) map[string]any {
	return map[string]any{"function_name": fn, "source_file": "p/leaf.go", "thread": true, "dry_run": dryRun}
}

func goBuilds(t *testing.T, dir string, tagSets ...string) {
	t.Helper()
	for _, tags := range tagSets {
		cmd := exec.Command("go", "build", "-tags="+tags, "./...")
		cmd.Dir = dir
		cmd.Env = append(os.Environ(), "CGO_ENABLED=0", "GOFLAGS=")
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Errorf("go build -tags=%q failed: %v\n%s", tags, err, out)
		}
	}
}

func diffFiles(out map[string]any) []string {
	var names []string
	diffs, _ := out["diffs"].([]any)
	for _, d := range diffs {
		file, _ := d.(map[string]any)["file"].(string)
		names = append(names, filepath.Base(file))
	}
	slices.Sort(names)
	return names
}

func fileText(t *testing.T, dir, rel string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(dir, rel))
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func TestAddContextParameter_ThreadCoversTaggedSiblings(t *testing.T) {
	dir := taggedSiblingWorkspace(t)
	sess := mcptest.Dial(context.Background(), t, mcpTransport(), dir)
	defer sess.Close()

	preview := callJSON(t, sess, "add_context_parameter", threadArgs("leaf", true))
	if configs, _ := preview["build_configs"].([]any); len(configs) != 2 || configs[1] != "tags=tagx" {
		t.Errorf("dry-run build_configs = %v, want the default config and tags=tagx", preview["build_configs"])
	}
	want := []string{"leaf.go", "open_default.go", "open_tagx.go", "use.go"}
	if got := diffFiles(preview); !slices.Equal(got, want) {
		t.Errorf("dry-run diffs touch %v, want %v", got, want)
	}
	if got := fileText(t, dir, "p/leaf.go"); strings.Contains(got, "context") {
		t.Fatalf("dry run wrote leaf.go:\n%s", got)
	}

	applied := callJSON(t, sess, "add_context_parameter", threadArgs("leaf", false))
	if configs, _ := applied["build_configs"].([]any); len(configs) != 2 {
		t.Errorf("apply build_configs = %v, want 2 entries", applied["build_configs"])
	}
	for _, f := range []string{"p/open_default.go", "p/open_tagx.go"} {
		if got := fileText(t, dir, f); strings.Count(got, "func open(ctx context.Context, name string) error") != 1 {
			t.Errorf("%s:\n%s", f, got)
		}
	}
	if got := fileText(t, dir, "p/use.go"); strings.Count(got, "open(ctx, name)") != 1 {
		t.Errorf("use.go:\n%s", got)
	}
	goBuilds(t, dir, "", "tagx")
}

func TestAddContextParameter_SecondThreadSeesFirstApplyInEveryView(t *testing.T) {
	dir := taggedSiblingWorkspace(t)
	sess := mcptest.Dial(context.Background(), t, mcpTransport(), dir)
	defer sess.Close()

	callJSON(t, sess, "add_context_parameter", threadArgs("leaf", false))
	callJSON(t, sess, "add_context_parameter", threadArgs("helper", false))

	for _, f := range []string{"p/open_default.go", "p/open_tagx.go"} {
		got := fileText(t, dir, f)
		if strings.Count(got, "ctx context.Context") != 1 || strings.Count(got, "helper(ctx)") != 1 {
			t.Errorf("%s: want the ctx from the first call passed to helper, got:\n%s", f, got)
		}
	}
	goBuilds(t, dir, "", "tagx")
}

func TestAddContextParameter_ThreadSingleConfigOmitsBuildConfigs(t *testing.T) {
	dir := writeInlineWorkspace(t, map[string]string{
		"p/leaf.go": "package p\n\nfunc leaf() {}\n\nfunc Use() { leaf() }\n",
	})
	sess := mcptest.Dial(context.Background(), t, mcpTransport(), dir)
	defer sess.Close()

	for _, dryRun := range []bool{true, false} {
		out := callJSON(t, sess, "add_context_parameter", threadArgs("leaf", dryRun))
		if _, ok := out["build_configs"]; ok {
			t.Errorf("dry_run=%v: build_configs = %v, want the field omitted", dryRun, out["build_configs"])
		}
	}
}
