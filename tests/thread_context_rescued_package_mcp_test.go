package tests_test

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/mamaar/gorefactor/tests/mcptest"
)

func TestAddContextParameter_Thread_SeedOfPackageWhoseTestsImportACycleBack(t *testing.T) {
	dir := writeInlineWorkspace(t, map[string]string{
		"common/common.go":      "package common\n\ntype TM struct{}\n\nfunc (*TM) Get() {}\n",
		"common/common_test.go": "package common\n\nimport \"example.com/threadmcp/x\"\n\nvar _ = x.X\n",
		"x/x.go":                "package x\n\nvar X = 1\n",
		"x/x_test.go":           "package x\n\nimport \"example.com/threadmcp/common\"\n\nvar _ = common.TM{}\n",
		"tile/tile.go":          "package tile\n\nimport \"example.com/threadmcp/common\"\n\nfunc Use(tm *common.TM) { tm.Get() }\n",
	})

	sess := mcptest.Dial(context.Background(), t, mcpTransport(), dir)
	defer sess.Close()

	callJSON(t, sess, "add_context_parameter", map[string]any{
		"function_name": "TM.Get", "source_file": filepath.Join(dir, "common/common.go"), "thread": true,
	})

	assertFileContains(t, dir, "tile/tile.go", "func Use(ctx context.Context, tm *common.TM) { tm.Get(ctx) }")
	goVets(t, dir, "")
}
