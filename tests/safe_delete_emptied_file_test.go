package tests_test

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/mamaar/gorefactor/tests/mcptest"
)

func TestSafeDelete_EmptiedFileIsKeptWhenItSaysSomethingOnItsOwn(t *testing.T) {
	for _, tc := range []struct {
		name, src string
		deleted   bool
	}{
		{"blank import", "package p\n\nimport _ \"net/http/pprof\"\n\nfunc dead() {}\n", false},
		{"go directive", "package p\n\n//go:generate echo hi\n\nfunc dead() {}\n", false},
		{"free comment block", "package p\n\n// Notes about the package.\n\nfunc dead() {}\n", false},
		{"plain", "package p\n\nfunc dead() {}\n", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := writeInlineWorkspace(t, map[string]string{"p/a.go": "package p\n\nfunc Live() {}\n", "p/drivers.go": tc.src})
			sess := mcptest.Dial(context.Background(), t, mcpTransport(), dir)
			defer sess.Close()

			callJSON(t, sess, "safe_delete", map[string]any{"symbol": "dead", "source_file": "p/drivers.go"})

			_, err := os.Stat(filepath.Join(dir, "p/drivers.go"))
			if gone := os.IsNotExist(err); gone != tc.deleted {
				t.Fatalf("file deleted = %v, want %v", gone, tc.deleted)
			}
			if !tc.deleted {
				assertFileOmits(t, dir, "p/drivers.go", "dead")
			}
		})
	}
}

func TestSafeDelete_RefusesANameSourceFileDoesNotDeclare(t *testing.T) {
	dir := writeInlineWorkspace(t, map[string]string{"p/a.go": "package p\n\nfunc Live() {}\n", "p/b.go": "package p\n\nfunc other() {}\n"})
	sess := mcptest.Dial(context.Background(), t, mcpTransport(), dir)
	defer sess.Close()

	res := callTool(t, sess, "safe_delete", map[string]any{"symbol": "Live", "source_file": "p/b.go", "dry_run": true})

	if !res.IsError || !strings.Contains(errText(res), "Live is not declared in") {
		t.Fatalf("want a not-declared refusal, got error=%v: %s", res.IsError, errText(res))
	}
}
