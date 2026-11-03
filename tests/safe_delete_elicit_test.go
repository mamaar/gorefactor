package tests_test

import (
	"context"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"

	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/mamaar/gorefactor/tests/mcptest"
)

// countingHandler wraps an elicitation answer function with a call counter,
// so tests can prove whether the safe_delete confirmation dialog was shown
// at all - the crux of most of the cases below.
func countingHandler(answer func(*mcpsdk.ElicitRequest) *mcpsdk.ElicitResult) (func(context.Context, *mcpsdk.ElicitRequest) (*mcpsdk.ElicitResult, error), *atomic.Int32) {
	var calls atomic.Int32

	return func(ctx context.Context, req *mcpsdk.ElicitRequest) (*mcpsdk.ElicitResult, error) {
		calls.Add(1)
		return answer(req), nil
	}, &calls
}

// callSafeDelete invokes the safe_delete tool and returns the result along
// with the concatenated text of its content blocks (populated on both
// success and error results, see internal/mcp/result.go).
func callSafeDelete(ctx context.Context, t *testing.T, sess *mcptest.Session, symbol, sourceFile string, force, dryRun bool) (*mcpsdk.CallToolResult, string) {
	t.Helper()

	args := map[string]any{
		"symbol":      symbol,
		"source_file": sourceFile,
	}
	if force {
		args["force"] = true
	}

	if dryRun {
		args["dry_run"] = true
	}

	result, err := sess.CallTool(ctx, &mcpsdk.CallToolParams{
		Name:      "safe_delete",
		Arguments: args,
	})
	if err != nil {
		t.Fatalf("CallTool(safe_delete): %v", err)
	}
	var text strings.Builder

	for _, c := range result.Content {
		tc, ok := c.(*mcpsdk.TextContent)
		if ok {
			text.WriteString(tc.Text)
		}
	}

	return result, text.String()
}

// readFixtureMain returns the pristine content of main.go from the given
// testdata fixture, for byte-for-byte "nothing was written" comparisons.
func readFixtureMain(t *testing.T, fixture string) string {
	t.Helper()

	data, err := os.ReadFile(filepath.Join("testdata", fixture, "main.go"))
	if err != nil {
		t.Fatalf("readFixtureMain(%s): %v", fixture, err)
	}

	return string(data)
}

func readTmpMain(t *testing.T, dir string) string {
	t.Helper()

	data, err := os.ReadFile(filepath.Join(dir, "main.go"))
	if err != nil {
		t.Fatalf("read tmp main.go: %v", err)
	}

	return string(data)
}

// wantExactBlockedError is the error safe_delete's underlying engine
// produces for deleting a symbol with N live references when Force is
// false: pkg/refactor/safe_delete.go's Validate builds "cannot safely
// delete %s: found %d references (use --force to delete anyway)", which
// pkg/refactor/engine.go's SafeDelete wraps with "safe delete operation
// validation failed: %w".
func wantExactBlockedError(symbol string, refCount int) string {
	return "safe delete operation validation failed: cannot safely delete " + symbol +
		": found " + strconv.Itoa(refCount) + " references (use --force to delete anyway)"
}

// --- force:false, dry_run:false, 0 blocking references: deletes, no dialog ---

func TestSafeDeleteElicit_NoRefs_NoDialog(t *testing.T) {
	handler, calls := countingHandler(func(*mcpsdk.ElicitRequest) *mcpsdk.ElicitResult {
		return &mcpsdk.ElicitResult{Action: "accept", Content: map[string]any{"confirm": true}}
	})

	tmpDir := copyFixture(t, "safe_delete")
	ctx := context.Background()

	sess := mcptest.Dial(ctx, t, mcptest.InProcessWithElicitation(handler), tmpDir)
	defer sess.Close()

	result, text := callSafeDelete(ctx, t, sess, "deprecatedHelper", "main.go", false, false)
	if result.IsError {
		t.Fatalf("safe_delete returned error: %s", text)
	}

	if got := calls.Load(); got != 0 {
		t.Fatalf("elicitation handler called %d times, want 0 (no blocking references)", got)
	}

	got := readTmpMain(t, tmpDir)
	if strings.Contains(got, "deprecatedHelper") {
		t.Fatalf("deprecatedHelper still present after delete:\n%s", got)
	}
}

// --- force:false, dry_run:false, N>0 blocking references: dialog shown ---

func TestSafeDeleteElicit_Accept(t *testing.T) {
	var seenMessage string
	handler, calls := countingHandler(func(req *mcpsdk.ElicitRequest) *mcpsdk.ElicitResult {
		seenMessage = req.Params.Message
		return &mcpsdk.ElicitResult{Action: "accept", Content: map[string]any{"confirm": true}}
	})

	tmpDir := copyFixture(t, "safe_delete_elicit")
	ctx := context.Background()

	sess := mcptest.Dial(ctx, t, mcptest.InProcessWithElicitation(handler), tmpDir)
	defer sess.Close()

	result, text := callSafeDelete(ctx, t, sess, "deprecatedHelper", "main.go", false, false)
	if result.IsError {
		t.Fatalf("safe_delete returned error: %s", text)
	}

	if got := calls.Load(); got != 1 {
		t.Fatalf("elicitation handler called %d times, want 1", got)
	}

	// The dialog message must name the symbol, the reference count, and each
	// blocking reference's file:line.
	if !strings.Contains(seenMessage, `"deprecatedHelper"`) {
		t.Errorf("dialog message missing symbol name:\n%s", seenMessage)
	}

	if !strings.Contains(seenMessage, "2 references") {
		t.Errorf("dialog message missing reference count:\n%s", seenMessage)
	}

	if !strings.Contains(seenMessage, "main.go:10") || !strings.Contains(seenMessage, "main.go:11") {
		t.Errorf("dialog message missing file:line entries:\n%s", seenMessage)
	}

	// The declaration is removed, but the two call sites are left in place
	// (rewriting them safely is out of scope) and reported as skipped sites.
	got := readTmpMain(t, tmpDir)
	if strings.Contains(got, "func deprecatedHelper") {
		t.Fatalf("deprecatedHelper declaration still present after accepted delete:\n%s", got)
	}

	if strings.Count(got, "deprecatedHelper()") != 2 {
		t.Fatalf("want both call sites left in place after accepted delete:\n%s", got)
	}

	if !strings.Contains(text, `"skipped_sites"`) {
		t.Fatalf("result missing skipped_sites: %s", text)
	}

	if strings.Count(text, "main.go") < 2 || !strings.Contains(text, `"line": 10`) || !strings.Contains(text, `"line": 11`) {
		t.Errorf("skipped_sites missing file:line entries:\n%s", text)
	}
}

func TestSafeDeleteElicit_DeclineOrCancel(t *testing.T) {
	for _, action := range []string{"decline", "cancel"} {
		t.Run(action, func(t *testing.T) {
			handler, calls := countingHandler(func(*mcpsdk.ElicitRequest) *mcpsdk.ElicitResult {
				return &mcpsdk.ElicitResult{Action: action}
			})

			tmpDir := copyFixture(t, "safe_delete_elicit")
			ctx := context.Background()

			sess := mcptest.Dial(ctx, t, mcptest.InProcessWithElicitation(handler), tmpDir)
			defer sess.Close()

			result, text := callSafeDelete(ctx, t, sess, "deprecatedHelper", "main.go", false, false)
			if !result.IsError {
				t.Fatalf("safe_delete succeeded, want an error (user %s)", action)
			}

			if got := calls.Load(); got != 1 {
				t.Fatalf("elicitation handler called %d times, want 1", got)
			}

			if !strings.Contains(text, "cancelled") {
				t.Errorf("error text does not mention cancellation: %q", text)
			}

			want := readFixtureMain(t, "safe_delete_elicit")

			got := readTmpMain(t, tmpDir)
			if got != want {
				t.Fatalf("main.go was modified after %s; want byte-for-byte unchanged\n--- want ---\n%s\n--- got ---\n%s", action, want, got)
			}
		})
	}
}

func TestSafeDeleteElicit_LegacyProtocolClient(t *testing.T) {
	for action, wantDeleted := range map[string]bool{"accept": true, "decline": false} {
		t.Run(action, func(t *testing.T) {
			handler, calls := countingHandler(func(*mcpsdk.ElicitRequest) *mcpsdk.ElicitResult {
				return &mcpsdk.ElicitResult{Action: action}
			})

			tmpDir := copyFixture(t, "safe_delete_elicit")
			ctx := context.Background()

			sess := mcptest.Dial(ctx, t, mcptest.InProcessWithClient(
				&mcpsdk.ClientOptions{ElicitationHandler: handler},
				&mcpsdk.ClientSessionOptions{ProtocolVersion: "2025-11-25"},
			), tmpDir)
			defer sess.Close()

			got := sess.InitializeResult().ProtocolVersion
			if got != "2025-11-25" {
				t.Fatalf("negotiated protocol %s, want 2025-11-25", got)
			}

			result, text := callSafeDelete(ctx, t, sess, "deprecatedHelper", "main.go", false, false)
			if result.IsError == wantDeleted {
				t.Fatalf("IsError = %v after %s: %s", result.IsError, action, text)
			}

			if got := calls.Load(); got != 1 {
				t.Fatalf("elicitation handler called %d times, want 1", got)
			}

			if got := strings.Contains(readTmpMain(t, tmpDir), "func deprecatedHelper"); got == wantDeleted {
				t.Fatalf("declaration present = %v after %s", got, action)
			}
		})
	}
}

func TestSafeDeleteElicit_URLOnlyClient_ExactError(t *testing.T) {
	handler, calls := countingHandler(func(*mcpsdk.ElicitRequest) *mcpsdk.ElicitResult {
		return &mcpsdk.ElicitResult{Action: "accept"}
	})

	for _, version := range []string{"2025-11-25", "2026-07-28"} {
		t.Run(version, func(t *testing.T) {
			tmpDir := copyFixture(t, "safe_delete_elicit")
			ctx := context.Background()

			sess := mcptest.Dial(ctx, t, mcptest.InProcessWithClient(
				&mcpsdk.ClientOptions{
					ElicitationHandler: handler,
					Capabilities: &mcpsdk.ClientCapabilities{
						Elicitation: &mcpsdk.ElicitationCapabilities{URL: &mcpsdk.URLElicitationCapabilities{}},
					},
				},
				&mcpsdk.ClientSessionOptions{ProtocolVersion: version},
			), tmpDir)
			defer sess.Close()

			result, text := callSafeDelete(ctx, t, sess, "deprecatedHelper", "main.go", false, false)

			want := wantExactBlockedError("deprecatedHelper", 2)
			if !result.IsError || text != want {
				t.Fatalf("IsError = %v, text = %q, want refusal %q", result.IsError, text, want)
			}

			got := calls.Load()
			if got != 0 {
				t.Fatalf("elicitation handler called %d times, want 0", got)
			}
		})
	}
}

// --- force:false, dry_run:true, N>0 blocking references: no dialog, dry-run unchanged ---

func TestSafeDeleteElicit_DryRun_NoDialog(t *testing.T) {
	handler, calls := countingHandler(func(*mcpsdk.ElicitRequest) *mcpsdk.ElicitResult {
		return &mcpsdk.ElicitResult{Action: "accept", Content: map[string]any{"confirm": true}}
	})

	tmpDir := copyFixture(t, "safe_delete_elicit")
	ctx := context.Background()

	sess := mcptest.Dial(ctx, t, mcptest.InProcessWithElicitation(handler), tmpDir)
	defer sess.Close()

	result, text := callSafeDelete(ctx, t, sess, "deprecatedHelper", "main.go", false, true)
	if !result.IsError {
		t.Fatalf("safe_delete(dry_run) succeeded, want an error (2 blocking references, force=false)")
	}

	if got := calls.Load(); got != 0 {
		t.Fatalf("elicitation handler called %d times, want 0 (dry_run must never show the dialog)", got)
	}

	if !strings.Contains(text, "found 2 references") {
		t.Errorf("dry-run error text = %q, want it to mention the blocking references", text)
	}

	want := readFixtureMain(t, "safe_delete_elicit")

	got := readTmpMain(t, tmpDir)
	if got != want {
		t.Fatalf("main.go was modified by a dry run; want byte-for-byte unchanged\n--- want ---\n%s\n--- got ---\n%s", want, got)
	}
}

// --- force:true, N>0 blocking references: deletes immediately, no dialog ---

func TestSafeDeleteElicit_Force_NoDialog(t *testing.T) {
	handler, calls := countingHandler(func(*mcpsdk.ElicitRequest) *mcpsdk.ElicitResult {
		return &mcpsdk.ElicitResult{Action: "accept", Content: map[string]any{"confirm": true}}
	})

	tmpDir := copyFixture(t, "safe_delete_elicit")
	ctx := context.Background()

	sess := mcptest.Dial(ctx, t, mcptest.InProcessWithElicitation(handler), tmpDir)
	defer sess.Close()

	result, text := callSafeDelete(ctx, t, sess, "deprecatedHelper", "main.go", true, false)
	if result.IsError {
		t.Fatalf("safe_delete(force) returned error: %s", text)
	}

	if got := calls.Load(); got != 0 {
		t.Fatalf("elicitation handler called %d times, want 0 (force must never show the dialog)", got)
	}

	// The declaration is removed, but the two call sites are left in place
	// (rewriting them safely is out of scope) and reported as skipped sites.
	got := readTmpMain(t, tmpDir)
	if strings.Contains(got, "func deprecatedHelper") {
		t.Fatalf("deprecatedHelper declaration still present after forced delete:\n%s", got)
	}

	if strings.Count(got, "deprecatedHelper()") != 2 {
		t.Fatalf("want both call sites left in place after forced delete:\n%s", got)
	}

	if !strings.Contains(text, `"skipped_sites"`) {
		t.Fatalf("result missing skipped_sites: %s", text)
	}

	if strings.Count(text, "main.go") < 2 || !strings.Contains(text, `"line": 10`) || !strings.Contains(text, `"line": 11`) {
		t.Errorf("skipped_sites missing file:line entries:\n%s", text)
	}
}

// --- N>0 blocking references, client has no elicitation handler: today's error, verbatim ---

func TestSafeDeleteElicit_NoHandler_ExactError(t *testing.T) {
	tmpDir := copyFixture(t, "safe_delete_elicit")
	ctx := context.Background()
	// InProcess (no elicitation handler) - the client never advertises the
	// elicitation capability, matching every pre-existing test in this
	// package.
	sess := mcptest.Dial(ctx, t, mcptest.InProcess(), tmpDir)
	defer sess.Close()

	result, text := callSafeDelete(ctx, t, sess, "deprecatedHelper", "main.go", false, false)
	if !result.IsError {
		t.Fatalf("safe_delete succeeded, want an error (no elicitation handler, 2 blocking references)")
	}

	want := wantExactBlockedError("deprecatedHelper", 2)
	if text != want {
		t.Fatalf("error text mismatch:\n got:  %q\n want: %q", text, want)
	}

	wantFile := readFixtureMain(t, "safe_delete_elicit")

	gotFile := readTmpMain(t, tmpDir)
	if gotFile != wantFile {
		t.Fatalf("main.go was modified; want byte-for-byte unchanged\n--- want ---\n%s\n--- got ---\n%s", wantFile, gotFile)
	}
}
