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
		err := os.MkdirAll(filepath.Dir(path), 0o755)
		if err != nil {
			t.Fatal(err)
		}
		err = os.WriteFile(path, []byte(content), 0o644)
		if err != nil {
			t.Fatal(err)
		}
	}

	s := NewMCPServer(slog.New(slog.NewTextHandler(io.Discard, nil)))
	defer s.Close()
	_, err := s.LoadWorkspace(context.Background(), root)
	if err != nil {
		t.Fatal(err)
	}
	views, err := s.GetEngine().WorkspaceSet().Views()
	if err != nil {
		t.Fatal(err)
	}
	if len(views) != 2 || !slices.Contains(views[1].Config.Tags, "fts5x") {
		t.Fatalf("views = %d, want the primary and the fts5x view", len(views))
	}
	ws, _ := s.GetWorkspace()
	if ws != views[0].Workspace {
		t.Fatal("GetWorkspace is not the primary view's workspace")
	}
	stale, err := s.EnsureReferenceIndex(views[1].Workspace)
	if err != nil {
		t.Fatal(err)
	}

	path := filepath.Join(root, "p", "a.go")
	applied := "package p\n\nfunc A() {}\n\nfunc Applied() {}\n"
	err = os.WriteFile(path, []byte(applied), 0o644)
	if err != nil {
		t.Fatal(err)
	}
	s.SyncWorkspaceChanges([]string{path})

	for _, v := range views {
		pkg := v.Workspace.Packages[filepath.Join(root, "p")]
		got := string(pkg.Files["a.go"].OriginalContent)
		if got != applied {
			t.Errorf("view %s content = %q", v.Config.Key(), got)
		}
		if pkg.Symbols.FindSymbol("Applied") == nil {
			t.Errorf("view %s lacks symbol Applied", v.Config.Key())
		}
	}
	fresh, _ := s.EnsureReferenceIndex(views[1].Workspace)
	if fresh == stale {
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
	v := s.set.ViewOf(ws)
	if v != nil {
		return v
	}
	return s.set.Primary()
}
