package analysis

import (
	"go/version"
	"os"
	"path/filepath"
	"runtime"
	"strings"

	"golang.org/x/mod/modfile"

	"github.com/mamaar/gorefactor/pkg/types"
)

// PackageGoVersion returns the language version ("go1.22") that applies to the
// package in dir, the way the go command resolves it: from the nearest go.mod
// at or above dir, within the workspace root. A go.mod without a go directive
// means go1.16, and a version newer than the running toolchain is lowered to
// the toolchain's, which go/types would otherwise reject. It returns "" when
// no go.mod applies, which leaves the check unrestricted.
func PackageGoVersion(ws *types.Workspace, dir string) string {
	content, ok := nearestGoMod(ws, dir)
	if !ok {
		return ""
	}

	mf, err := modfile.ParseLax("go.mod", content, nil)
	if err != nil {
		return ""
	}

	v := "go1.16"
	if mf.Go != nil {
		v = version.Lang("go" + mf.Go.Version)
	}

	if v == "" {
		return ""
	}

	if latest := version.Lang(runtime.Version()); latest != "" && version.Compare(v, latest) > 0 {
		return latest
	}

	return v
}

// nearestGoMod returns the contents of the go.mod governing dir. Without one
// on disk it falls back to the workspace module's, for workspaces built in memory.
func nearestGoMod(ws *types.Workspace, dir string) ([]byte, bool) {
	root := filepath.Clean(ws.RootPath)

	for d := filepath.Clean(dir); d == root || strings.HasPrefix(d, root+string(filepath.Separator)); d = filepath.Dir(d) {
		content, err := os.ReadFile(filepath.Join(d, "go.mod"))
		if err == nil {
			return content, true
		}

		if d == root {
			break
		}
	}

	if ws.Module == nil || ws.Module.GoMod == "" {
		return nil, false
	}

	return []byte(ws.Module.GoMod), true
}
