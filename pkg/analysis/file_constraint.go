package analysis

import (
	"go/build/constraint"
	"path/filepath"
	"strings"
)

// FileConstraint says when a file is part of its package: a build expression,
// a GOOS/GOARCH file name suffix, and whether it needs cgo.
type FileConstraint struct {
	Expr   constraint.Expr // nil when the file has no build line
	Suffix string          // "windows" or "linux_amd64"; empty when the name has none
	Cgo    bool            // the file imports "C"
}

// ConstraintOf returns the constraints that decide whether f is built.
func ConstraintOf(f *SourceFile) FileConstraint {
	return FileConstraint{Expr: fileConstraint(f), Suffix: nameSuffix(f.Name), Cgo: importsC(f)}
}

// nameSuffix returns the GOOS and/or GOARCH suffix of a file name, following
// the toolchain's rule that the part before the first underscore never counts.
func nameSuffix(name string) string {
	stem := strings.TrimSuffix(filepath.Base(name), ".go")
	_, rest, ok := strings.Cut(stem, "_")
	if !ok {
		return ""
	}
	parts := strings.Split(rest, "_")
	n := len(parts)
	if n >= 2 && knownOS[parts[n-2]] && knownArch[parts[n-1]] {
		return parts[n-2] + "_" + parts[n-1]
	}
	if knownOS[parts[n-1]] || knownArch[parts[n-1]] {
		return parts[n-1]
	}
	return ""
}

var knownOS = setOf("aix", "android", "darwin", "dragonfly", "freebsd", "hurd", "illumos", "ios", "js",
	"linux", "nacl", "netbsd", "openbsd", "plan9", "solaris", "wasip1", "windows", "zos")

var knownArch = setOf("386", "amd64", "amd64p32", "arm", "armbe", "arm64", "arm64be", "loong64", "mips",
	"mipsle", "mips64", "mips64le", "mips64p32", "mips64p32le", "ppc", "ppc64", "ppc64le", "riscv",
	"riscv64", "s390", "s390x", "sparc", "sparc64", "wasm")

func setOf(names ...string) map[string]bool {
	m := make(map[string]bool, len(names))
	for _, n := range names {
		m[n] = true
	}
	return m
}
