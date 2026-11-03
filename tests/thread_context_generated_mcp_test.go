package tests_test

import (
	"context"
	"strings"
	"testing"

	"github.com/mamaar/gorefactor/tests/mcptest"
)

const (
	generatedBridge = "bridge/bridge_gen.go"
	suggestedStop   = "bridge/wrapper.go:NativeBridgeWrapper.MapSearch"
)

func generatedThreadArgs(dryRun bool, stopAt ...string) map[string]any {
	args := map[string]any{
		"function_name": "MapSearch",
		"source_file":   "search/search.go",
		"thread":        true,
		"dry_run":       dryRun,
	}
	if len(stopAt) > 0 {
		args["stop_at"] = stopAt
	}
	return args
}

func TestThreadContextGenerated_RefusesThenAcceptsSuggestedStop(t *testing.T) {
	t.Run("refuses without stop_at", func(t *testing.T) {
		dir := copyFixture(t, "thread_context_generated")
		before := readTree(t, dir)
		sess := mcptest.Dial(context.Background(), t, mcpTransport(), dir)
		defer sess.Close()

		msg := callExpectingError(t, sess, generatedThreadArgs(false))
		if !strings.Contains(msg, generatedBridge) || !strings.Contains(msg, suggestedStop) {
			t.Errorf("error should name %s and suggest %s, got: %s", generatedBridge, suggestedStop, msg)
		}
		after := readTree(t, dir)
		if !equalTrees(before, after) {
			t.Errorf("refused climb modified the workspace")
		}
	})

	t.Run("dry run with the suggested stop", func(t *testing.T) {
		dir := copyFixture(t, "thread_context_generated")
		before := readTree(t, dir)
		sess := mcptest.Dial(context.Background(), t, mcpTransport(), dir)
		defer sess.Close()

		out := decodePlanResult(t, callTool(t, sess, "add_context_parameter", generatedThreadArgs(true, suggestedStop)))
		if !out.Success {
			t.Errorf("success = false, blocking_issue = %q", out.BlockingIssue)
		}
		for _, d := range out.Diffs {
			if strings.HasSuffix(d.File, "bridge_gen.go") {
				t.Errorf("diffs include generated file %s", d.File)
			}
		}
		var stops int
		for _, s := range out.SkippedSites {
			if strings.HasPrefix(s.Reason, "stop_at:") {
				stops++
			}
		}
		if stops != 1 {
			t.Errorf("skipped_sites lists %d stop_at entries, want 1: %+v", stops, out.SkippedSites)
		}
		after := readTree(t, dir)
		if !equalTrees(before, after) {
			t.Errorf("dry run modified the workspace")
		}
	})

	t.Run("applies the suggested stop", func(t *testing.T) {
		dir := copyFixture(t, "thread_context_generated")
		before := readTree(t, dir)
		sess := mcptest.Dial(context.Background(), t, mcpTransport(), dir)
		defer sess.Close()

		out := decodePlanResult(t, callTool(t, sess, "add_context_parameter", generatedThreadArgs(false, suggestedStop)))
		if !out.Success {
			t.Errorf("success = false, blocking_issue = %q", out.BlockingIssue)
		}
		after := readTree(t, dir)
		if after[generatedBridge] != before[generatedBridge] {
			t.Errorf("%s was modified:\n%s", generatedBridge, after[generatedBridge])
		}
		got := fileText(t, dir, "planner/planner.go")
		if !strings.Contains(got, "func Plan(ctx context.Context, query string)") {
			t.Errorf("planner.go:\n%s", got)
		}
		goBuilds(t, dir, "")
	})
}
