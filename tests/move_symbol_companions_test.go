package tests_test

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/mamaar/gorefactor/tests/mcptest"
)

// TestMoveSymbolTypeWithConstructorProducesCompilableCode is a regression
// test for three correctness bugs found in move_symbol when moving a struct
// type (with unexported fields, a constructor, and mixed import kinds) into
// a brand-new package — the same shape as `common.MemCache` in a real
// production workspace. All three bugs were reported "success": true by the
// tool while silently producing code that could not compile:
//
//  1. Wrong import path inserted for the moved-to package. Any leftover
//     reference to the moved symbol (e.g. from a sibling file still in the
//     source package) got an import like `"pkg/memcache"` instead of the
//     full module-qualified `"reprotest/pkg/memcache"`. Root cause: Execute()
//     in pkg/refactor/operations.go passed the caller's raw, possibly
//     unresolved op.Request.ToPackage (e.g. "pkg/memcache") into
//     generateReferenceUpdateChange/generateImportChanges instead of
//     targetPackage.Path — the workspace-absolute path that
//     getOrCreateTargetFile resolves (and creates on disk) when the target
//     package doesn't exist yet. packagePathToImportPath requires an
//     absolute path to compute filepath.Rel(ws.RootPath, ...) correctly; fed
//     a relative path it silently fell back to returning the path unchanged.
//
//  2. The newly-created destination file had no import block at all, even
//     though the moved code referenced stdlib, aliased-stdlib and
//     intra-module identifiers. Root cause: generateSymbolAdditionChange
//     (now generateSymbolAdditionChanges, in pkg/refactor/operations.go)
//     copied only the symbol's raw text and never inspected what
//     identifiers it referenced. Fixed via move_imports.go, which
//     re-parses the extracted code, collects package-qualifier
//     identifiers, and matches them against the source file's own import
//     list (carrying over any explicit alias).
//
//  3. The constructor (NewMemCache) was left behind in the source package
//     but rewritten to build the moved struct via its unexported fields —
//     code that can never compile from a different package. Root cause:
//     move_symbol only ever bundled a type's *method set* with the move,
//     never plain constructor functions. Fixed via move_companions.go,
//     which moves any package-level function that builds the type through
//     unexported-field access, and — per the task's explicit
//     "move or refuse" requirement — refuses the whole move with a clear
//     error (types.VisibilityViolation) if anything else would be
//     stranded that the tool won't auto-move.
//
// The acceptance criterion that actually would have caught all three bugs
// (a string-diff test would not have): after the move, `go build ./...`
// must succeed against the whole fixture module.
func TestMoveSymbolTypeWithConstructorProducesCompilableCode(t *testing.T) {
	tmpDir := t.TempDir()

	mustWrite(t, filepath.Join(tmpDir, "go.mod"), "module reprotest\n\ngo 1.21\n")

	// An intra-module dependency package, mirroring the real workspace's
	// `cache.CacheServerResponse` referenced by MemCache's cacheMap field —
	// exercises import resolution for a non-stdlib, non-aliased package
	// whose local name differs from nothing special (last path segment).
	mustWrite(t, filepath.Join(tmpDir, "internal", "cache", "cache.go"), `package cache

// CacheServerResponse mirrors the real cache.CacheServerResponse type.
type CacheServerResponse struct {
	Body []byte
}
`)

	// The source file: unexported fields of several different import kinds
	// (stdlib pointer, aliased stdlib pointer, intra-module value, map),
	// exported and unexported methods, and a constructor that touches every
	// unexported field via a keyed composite literal — the idiomatic
	// pattern that bug 3 silently broke.
	mustWrite(t, filepath.Join(tmpDir, "common", "memcache.go"), `package common

import (
	"errors"
	"net/http"
	rwmutex "sync"

	"reprotest/internal/cache"
)

// MemCache mirrors common.MemCache from the real workspace.
type MemCache struct {
	httpClient *http.Client
	cacheMap   map[string]cache.CacheServerResponse
	lock       *rwmutex.RWMutex
}

// NewMemCache builds MemCache via its unexported fields — after MemCache
// moves to another package, this only keeps compiling if it moves too.
func NewMemCache(httpClient *http.Client) *MemCache {
	return &MemCache{
		httpClient: httpClient,
		cacheMap:   make(map[string]cache.CacheServerResponse),
		lock:       &rwmutex.RWMutex{},
	}
}

// Get is exported and touches unexported fields.
func (c *MemCache) Get(key string) ([]byte, bool) {
	c.lock.Lock()
	defer c.lock.Unlock()
	resp, ok := c.cacheMap[key]
	return resp.Body, ok
}

// Port is exported and uses an import (errors) that only appears inside a
// method body, not the struct fields — the moved code must pull this import
// in too.
func (c *MemCache) Port() (int, error) {
	if c.httpClient == nil {
		return 0, errors.New("no http client configured")
	}
	return 0, nil
}

// persistValueForKey is unexported and touches an unexported field.
func (c *MemCache) persistValueForKey(key string, value []byte) {
	c.lock.Lock()
	defer c.lock.Unlock()
	c.cacheMap[key] = cache.CacheServerResponse{Body: value}
}

// Unrelated has nothing to do with MemCache and must stay behind.
func Unrelated() string {
	return "still here"
}
`)

	// A sibling file, still in package common, that calls the constructor
	// and holds a *MemCache — but never touches an unexported field itself,
	// so it must NOT be treated as a companion (it should stay in package
	// common with its references rewritten to the new package). This is
	// exactly the shape of bug 1's repro: a same-package leftover reference
	// that needs a correctly module-qualified new import.
	mustWrite(t, filepath.Join(tmpDir, "common", "helper.go"), `package common

// StartupCache stays behind: it only calls the exported constructor, it
// never accesses an unexported field of MemCache directly.
func StartupCache() *MemCache {
	return NewMemCache(nil)
}
`)

	// A separate consumer package, to exercise cross-package reference
	// updates alongside the same-package one above.
	mustWrite(t, filepath.Join(tmpDir, "consumer", "consumer.go"), `package consumer

import "reprotest/common"

func UseCache() {
	c := common.NewMemCache(nil)
	c.Get("k")
	_ = common.Unrelated()
}
`)

	// Sanity check: the target package directory must not exist yet — this
	// is the exact condition that triggered bug 1 (ResolvePackagePath falls
	// through to the raw, unresolved "pkg/memcache" when nothing in
	// ws.Packages matches yet).
	_, err := os.Stat(filepath.Join(tmpDir, "pkg", "memcache"))
	if !os.IsNotExist(err) {
		t.Fatalf("test setup invariant violated: pkg/memcache must not exist yet, stat err=%v", err)
	}

	ctx := context.Background()
	sess := mcptest.Dial(ctx, t, mcpTransport(), tmpDir)
	defer sess.Close()

	result, err := sess.CallTool(ctx, &mcpsdk.CallToolParams{
		Name: "move_symbol",
		Arguments: map[string]any{
			"symbol":       "MemCache",
			"from_package": "common",
			"to_package":   "pkg/memcache",
		},
	})
	if err != nil {
		t.Fatalf("CallTool transport error (server likely crashed/disconnected): %v", err)
	}
	if result.IsError {
		t.Fatalf("move_symbol returned an error result: %+v", result.Content)
	}

	t.Run("moved file has declarations and imports", func(t *testing.T) {
		const target = "pkg/memcache/memcache.go"
		for _, want := range []string{
			"type MemCache struct",
			"func NewMemCache(",
			"func (c *MemCache) Get(",
			"func (c *MemCache) Port(",
			"func (c *MemCache) persistValueForKey(",
			// Bug 2: every import the moved code actually uses must be present.
			`"net/http"`,
			`rwmutex "sync"`,
			`"reprotest/internal/cache"`,
			`"errors"`,
		} {
			assertFileContains(t, tmpDir, target, want)
		}
		// There is no "was moved from" banner to leak a filesystem path through.
		assertFileOmits(t, tmpDir, target, tmpDir)
		assertFileOmits(t, tmpDir, target, "was moved from")
	})

	t.Run("source file keeps only unrelated code", func(t *testing.T) {
		const common = "common/memcache.go"
		for _, notWant := range []string{
			"type MemCache struct",
			"func NewMemCache(",
			"func (c *MemCache)",
		} {
			assertFileOmits(t, tmpDir, common, notWant)
		}
		assertFileContains(t, tmpDir, common, "func Unrelated()")
	})

	t.Run("same-package leftover gets module-qualified import", func(t *testing.T) {
		// Bug 1: not the bare "pkg/memcache".
		const helper = "common/helper.go"
		assertFileContains(t, tmpDir, helper, `"reprotest/pkg/memcache"`)
		assertFileContains(t, tmpDir, helper, "memcache.NewMemCache")
		assertFileContains(t, tmpDir, helper, "*memcache.MemCache")
	})

	t.Run("cross-package consumer is updated", func(t *testing.T) {
		const consumer = "consumer/consumer.go"
		assertFileContains(t, tmpDir, consumer, "memcache.NewMemCache")
		assertFileContains(t, tmpDir, consumer, "common.Unrelated")
	})

	// --- The acceptance criterion: the whole fixture module must compile. ---
	// A string-diff-only test would not have caught any of the three bugs
	// above; only actually building the result does.
	cmd := exec.Command("go", "build", "./...")
	cmd.Dir = tmpDir
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("fixture module does not compile after move_symbol (this is the actual bug):\n%s", out)
	}
}

// TestMoveSymbolRefusesWhenConstructorCannotSafelyMove is a regression test
// for the "refuse rather than corrupt" half of bug 3's fix: when a type's
// unexported fields are reached from code move_symbol has no safe,
// mechanical way to bring along (here: a composite literal sitting in an
// unrelated function, not a constructor pattern move_symbol recognizes),
// the move must fail loudly instead of silently emitting code that leaves
// that code unable to compile.
func TestMoveSymbolRefusesWhenConstructorCannotSafelyMove(t *testing.T) {
	tmpDir := t.TempDir()

	mustWrite(t, filepath.Join(tmpDir, "go.mod"), "module reprotest\n\ngo 1.21\n")
	mustWrite(t, filepath.Join(tmpDir, "common", "memcache.go"), `package common

type MemCache struct {
	data map[string][]byte
}

func NewMemCache() *MemCache {
	return &MemCache{data: make(map[string][]byte)}
}
`)
	// Debug does NOT fit the "constructor" pattern move_symbol auto-moves
	// (it's not just constructing and returning the type — it constructs it
	// unkeyed as a side effect while doing something else), but it still
	// reaches into MemCache's unexported field and would be stranded.
	mustWrite(t, filepath.Join(tmpDir, "common", "debug.go"), `package common

import "fmt"

func DebugDump() {
	mc := MemCache{data: map[string][]byte{"k": []byte("v")}}
	fmt.Println(mc.data)
}
`)

	ctx := context.Background()
	sess := mcptest.Dial(ctx, t, mcpTransport(), tmpDir)
	defer sess.Close()

	result, err := sess.CallTool(ctx, &mcpsdk.CallToolParams{
		Name: "move_symbol",
		Arguments: map[string]any{
			"symbol":       "MemCache",
			"from_package": "common",
			"to_package":   "pkg/memcache",
		},
	})
	if err != nil {
		t.Fatalf("CallTool transport error (server likely crashed/disconnected): %v", err)
	}
	if !result.IsError {
		t.Fatalf("expected move_symbol to refuse the move (DebugDump would be stranded), got success: %+v", result.Content)
	}

	// The source file must be untouched — a refused move must not partially
	// apply changes.
	common := mustRead(t, filepath.Join(tmpDir, "common", "memcache.go"))
	if !strings.Contains(common, "type MemCache struct") {
		t.Errorf("expected refused move to leave MemCache in place, got:\n%s", common)
	}
	if _, err := os.Stat(filepath.Join(tmpDir, "pkg", "memcache")); !os.IsNotExist(err) {
		t.Errorf("expected refused move to not create the target package, stat err=%v", err)
	}
}

func mustRead(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading %s: %v", path, err)
	}
	return string(data)
}
