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
		if i := strings.LastIndex(entry, ":"); i >= 0 {
			file, name = entry[:i], entry[i+1:]
		}
		matches := stopMatches(wss, file, name)
		keys := make(map[pkgtypes.SymbolKey]string)
		for _, m := range matches {
			if prev, ok := keys[m.key]; !ok || m.file < prev {
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
		files := stopCandidateFiles(ws, file)
		for _, h := range files {
			rel := h.file.Path
			if r, err := filepath.Rel(ws.RootPath, h.file.Path); err == nil {
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

// stopCandidateFiles returns the parsed files of ws, or only those matching
// file under findSourceFile's rules: exact path or loader key, else unique-suffix.
func stopCandidateFiles(ws *pkgtypes.Workspace, file string) []fileHit {
	var all, exact, loose []fileHit
	want := filepath.ToSlash(file)
	for _, pkg := range ws.Packages {
		for key, f := range allNamedFilesOf(pkg) {
			if f.AST == nil {
				continue
			}
			h := fileHit{file: f, pkg: pkg}
			all = append(all, h)
			switch {
			case f.Path == file || key == file:
				exact = append(exact, h)
			case strings.HasSuffix(filepath.ToSlash(f.Path), "/"+want):
				loose = append(loose, h)
			}
		}
	}
	switch {
	case file == "":
		return all
	case len(exact) > 0:
		return exact
	default:
		return loose
	}
}
