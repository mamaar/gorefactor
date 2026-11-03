package refactor

import (
	"fmt"
	"go/ast"
	"path/filepath"
	"sort"
	"strings"

	"github.com/mamaar/gorefactor/pkg/analysis"
	pkgtypes "github.com/mamaar/gorefactor/pkg/types"
)

// stopMatch is one declaration an entry names, with the file it sits in.
type stopMatch struct {
	key  pkgtypes.SymbolKey
	file string
}

// resolveStopAt turns stop_at entries ("Name", "Type.Method" or
// "file.go:Name") into the keys of the functions they name in any workspace.
func resolveStopAt(wss []*pkgtypes.Workspace, entries []string) (pkgtypes.SymbolKeySet, error) {
	out := make(pkgtypes.SymbolKeySet)
	for _, entry := range entries {
		file, name := "", entry
		before, after, ok := strings.CutLast(entry, ":")
		if ok {
			file, name = before, after
		}
		matches := stopMatches(wss, file, name)
		keys := make(map[pkgtypes.SymbolKey]string)
		for _, m := range matches {
			prev, ok := keys[m.key]
			if !ok || m.file < prev {
				keys[m.key] = m.file
			}
		}
		switch len(keys) {
		case 0:
			return nil, stopAtError(fmt.Sprintf("stop_at: no function %q in the workspace", entry))
		case 1:
			for k := range keys {
				out[k] = struct{}{}
			}
		default:
			cands := make([]string, 0, len(keys))
			for k, f := range keys {
				cands = append(cands, f+":"+strings.TrimPrefix(k.Recv+"."+k.Name, "."))
			}
			sort.Strings(cands)
			return nil, stopAtError(fmt.Sprintf("stop_at: %q is ambiguous; use one of: %s", entry, strings.Join(cands, ", ")))
		}
	}
	return out, nil
}

func stopAtError(msg string) error {
	return &pkgtypes.RefactorError{Type: pkgtypes.InvalidOperation, Message: msg}
}

// stopMatches returns every declaration of name in the workspaces, limited to
// the files matching file when it is not empty. Relative file paths are
// reported against each workspace's root.
func stopMatches(wss []*pkgtypes.Workspace, file, name string) []stopMatch {
	typeName, method, isMethod := strings.Cut(name, ".")
	var out []stopMatch
	for _, ws := range wss {
		for _, h := range matchSourceFiles(ws, file) {
			rel := h.file.Path
			r, err := filepath.Rel(ws.RootPath, h.file.Path)
			if err == nil {
				rel = filepath.ToSlash(r)
			}
			add := func(recv, n string) {
				out = append(out, stopMatch{
					key:  pkgtypes.SymbolKey{PkgPath: h.pkg.ImportPath, Recv: recv, Name: n},
					file: rel,
				})
			}
			for _, decl := range h.file.AST.Decls {
				fd, ok := decl.(*ast.FuncDecl)
				if !ok || fd.Name == nil {
					continue
				}
				switch {
				case !isMethod && fd.Recv == nil && fd.Name.Name == name:
					add("", name)
				case isMethod && fd.Recv != nil && fd.Name.Name == method &&
					len(fd.Recv.List) == 1 && analysis.MatchesReceiverType(fd.Recv.List[0].Type, typeName):
					add(typeName, method)
				}
			}
			if isMethod && findInterfaceMethodIdent(h.file.AST, name) != nil {
				add(typeName, method)
			}
		}
	}
	return out
}
