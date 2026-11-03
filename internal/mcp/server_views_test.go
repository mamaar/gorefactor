package mcp

import (
	"context"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"slices"
	"testing"

	"github.com/mamaar/gorefactor/pkg/analysis"
	"github.com/mamaar/gorefactor/pkg/types"
)

func TestSyncWorkspaceChanges_ReachesBuiltSecondaryViews(t *testing.T) {
	root := t.TempDir()
	files := map[string]string{
		"go.mod":   "module example.com/views\n\ngo 1.22\n",
		"p/a.go":   "package p\n\nfunc A() {}\n",
		"p/tag.go": "//go:build fts5x\n\npackage p\n\nfunc Tagged() { A() }\n",
	}
	for name, content := range files {
		path := filepath.Join(root, name)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	s := NewMCPServer(slog.New(slog.NewTextHandler(io.Discard, nil)))
	defer s.Close()
	if _, err := s.LoadWorkspace(context.Background(), root); err != nil {
		t.Fatal(err)
	}
	views, err := s.GetEngine().WorkspaceSet().Views()
	if err != nil {
		t.Fatal(err)
	}
	if len(views) != 2 || !slices.Contains(views[1].Config.Tags, "fts5x") {
		t.Fatalf("views = %d, want the primary and the fts5x view", len(views))
	}
	if ws, _ := s.GetWorkspace(); ws != views[0].Workspace {
		t.Fatal("GetWorkspace is not the primary view's workspace")
	}
	stale, err := s.EnsureReferenceIndex(views[1].Workspace)
	if err != nil {
		t.Fatal(err)
	}

	path := filepath.Join(root, "p", "a.go")
	applied := "package p\n\nfunc A() {}\n\nfunc Applied() {}\n"
	if err := os.WriteFile(path, []byte(applied), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := s.SyncWorkspaceChanges([]string{path}); err != nil {
		t.Fatal(err)
	}

	for _, v := range views {
		pkg := v.Workspace.Packages[filepath.Join(root, "p")]
		if got := string(pkg.Files["a.go"].OriginalContent); got != applied {
			t.Errorf("view %s content = %q", v.Config.Key(), got)
		}
		if pkg.Symbols.FindSymbol("Applied") == nil {
			t.Errorf("view %s lacks symbol Applied", v.Config.Key())
		}
	}
	if fresh, _ := s.EnsureReferenceIndex(views[1].Workspace); fresh == stale {
		t.Error("secondary view kept its reference index")
	}
}

// EnsureReferenceIndex returns the cached reference index of the view that
// owns ws, building one if necessary.
func (s *MCPServer) EnsureReferenceIndex(ws *types.Workspace) (*analysis.ReferenceIndex, error) {
	return s.viewOf(ws).ReferenceIndex()
}

// viewOf returns the view that owns ws, or the primary view.
func (s *MCPServer) viewOf(ws *types.Workspace) *analysis.View {
	if v := s.set.ViewOf(ws); v != nil {
		return v
	}
	return s.set.Primary()
}
