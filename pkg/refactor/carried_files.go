package refactor

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

// fileMove is one file relocated byte for byte (apart from Go import paths).
type fileMove struct {
	source, target string
	mode           os.FileMode
}

// treeEntry is one non-directory under a moved directory, as found on disk.
type treeEntry struct {
	path string
	rel  string
	mode fs.FileMode
	size int64
}

// walkTree lists every file and symlink under root; symlinks are never followed.
func walkTree(root string) ([]treeEntry, error) {
	var entries []treeEntry
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		entries = append(entries, treeEntry{path: path, rel: rel, mode: info.Mode(), size: info.Size()})
		return nil
	})
	return entries, err
}

func pathParts(rel string) []string { return strings.Split(rel, string(filepath.Separator)) }

// hasPart reports whether any directory component of rel is name.
func hasPart(rel, name string) bool {
	parts := pathParts(rel)
	for _, p := range parts[:len(parts)-1] {
		if p == name {
			return true
		}
	}
	return false
}

// isGoSource reports whether e is a non-empty .go file, which the Go-file path
// moves; an empty one has no package clause and is carried verbatim.
func (e treeEntry) isGoSource() bool { return filepath.Ext(e.rel) == ".go" && e.size > 0 }

// carriedByPackages returns the files of each moved package that belong to no
// other package: a file stays behind when a directory from just below the
// package down to its own directly holds non-testdata Go files or a go.mod.
func carriedByPackages(moves []dirMove) ([]fileMove, error) {
	var carried []fileMove
	for _, m := range moves {
		entries, err := walkTree(m.source)
		if err != nil {
			return nil, err
		}
		boundaries := make(map[string]bool)
		for _, e := range entries {
			name := filepath.Base(e.rel)
			if !hasPart(e.rel, "testdata") && (name == "go.mod" || filepath.Ext(name) == ".go") {
				boundaries[filepath.Dir(e.rel)] = true
			}
		}
		var keep []treeEntry
		for _, e := range entries {
			if e.isGoSource() && filepath.Dir(e.rel) == "." || inOtherPackage(e.rel, boundaries) {
				continue
			}
			keep = append(keep, e)
		}
		moved, err := toFileMoves(m, keep)
		if err != nil {
			return nil, err
		}
		carried = append(carried, moved...)
	}
	return carried, nil
}

// inOtherPackage reports whether a directory of rel below the package root is a boundary.
func inOtherPackage(rel string, boundaries map[string]bool) bool {
	for dir := filepath.Dir(rel); dir != "."; dir = filepath.Dir(dir) {
		if boundaries[dir] {
			return true
		}
	}
	return false
}

// carriedByDir returns every file under the moved directory that is not a Go
// file of one of its packages, which the Go-file path moves.
func carriedByDir(root dirMove, packages []dirMove) ([]fileMove, error) {
	isPackageDir := make(map[string]bool, len(packages))
	for _, p := range packages {
		isPackageDir[p.source] = true
	}
	entries, err := walkTree(root.source)
	if err != nil {
		return nil, err
	}
	var keep []treeEntry
	for _, e := range entries {
		if filepath.Base(e.rel) == "go.mod" && e.rel != "go.mod" && !hasPart(e.rel, "testdata") {
			return nil, fmt.Errorf("cannot move %s: it is a module boundary inside the directory", e.path)
		}
		if e.isGoSource() && isPackageDir[filepath.Dir(e.path)] {
			continue
		}
		keep = append(keep, e)
	}
	return toFileMoves(root, keep)
}

// toFileMoves turns entries into moves into m.target, refusing symlinks, a
// .git directory outside testdata, and any target that exists.
func toFileMoves(m dirMove, entries []treeEntry) ([]fileMove, error) {
	moves := make([]fileMove, 0, len(entries))
	for _, e := range entries {
		switch {
		case e.mode&fs.ModeSymlink != 0:
			return nil, fmt.Errorf("cannot move %s: it is a symlink, and a move never follows or recreates one", e.path)
		case !e.mode.IsRegular(): // reading a FIFO or device would block
			return nil, fmt.Errorf("cannot move %s: it is not a regular file", e.path)
		case holdsRepositoryDir(e.rel):
			return nil, fmt.Errorf("cannot move %s: it is inside a .git directory", e.path)
		}
		target := filepath.Join(m.target, e.rel)
		if _, err := os.Lstat(target); err == nil {
			return nil, fmt.Errorf("cannot move %s: %s already exists", e.path, target)
		}
		moves = append(moves, fileMove{source: e.path, target: target, mode: e.mode.Perm()})
	}
	return moves, nil
}

// holdsRepositoryDir reports whether rel lies in a .git directory that is not
// itself under testdata, where such names are fixtures.
func holdsRepositoryDir(rel string) bool {
	parts := pathParts(rel)
	for _, p := range parts[:len(parts)-1] {
		switch p {
		case "testdata":
			return false
		case ".git":
			return true
		}
	}
	return parts[len(parts)-1] == ".git"
}
