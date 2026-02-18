package refactor

import (
	"strings"
	"testing"
)

func TestClassifyImport(t *testing.T) {
	modulePath := "github.com/mamaar/gorefactor"
	workspaceModules := []string{"github.com/mamaar/othermod", "github.com/mamaar/sharedlib"}

	tests := []struct {
		name       string
		importPath string
		want       ImportGroup
	}{
		{"stdlib simple", "fmt", ImportGroupStdlib},
		{"stdlib nested", "net/http", ImportGroupStdlib},
		{"stdlib go subpkg", "go/ast", ImportGroupStdlib},
		{"stdlib os", "os", ImportGroupStdlib},
		{"external", "github.com/stretchr/testify/assert", ImportGroupExternal},
		{"external other org", "golang.org/x/tools/go/ast", ImportGroupExternal},
		{"workspace exact", "github.com/mamaar/othermod", ImportGroupWorkspace},
		{"workspace subpkg", "github.com/mamaar/othermod/pkg/foo", ImportGroupWorkspace},
		{"workspace second", "github.com/mamaar/sharedlib/types", ImportGroupWorkspace},
		{"module exact", "github.com/mamaar/gorefactor", ImportGroupModule},
		{"module subpkg", "github.com/mamaar/gorefactor/pkg/types", ImportGroupModule},
		{"module deep subpkg", "github.com/mamaar/gorefactor/internal/cli/commands", ImportGroupModule},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := classifyImport(tt.importPath, modulePath, workspaceModules)
			if got != tt.want {
				t.Errorf("classifyImport(%q) = %d, want %d", tt.importPath, got, tt.want)
			}
		})
	}
}

func TestClassifyImport_EmptyModulePath(t *testing.T) {
	got := classifyImport("fmt", "", nil)
	if got != ImportGroupStdlib {
		t.Errorf("expected stdlib, got %d", got)
	}

	got = classifyImport("github.com/foo/bar", "", nil)
	if got != ImportGroupExternal {
		t.Errorf("expected external, got %d", got)
	}
}

func TestClassifyImport_NoWorkspaceModules(t *testing.T) {
	got := classifyImport("github.com/mamaar/gorefactor/pkg/types", "github.com/mamaar/gorefactor", nil)
	if got != ImportGroupModule {
		t.Errorf("expected module, got %d", got)
	}
}

func TestOrganizeImports_BasicGrouping(t *testing.T) {
	src := `package main

import (
	"github.com/mamaar/gorefactor/pkg/types"
	"fmt"
	"github.com/stretchr/testify/assert"
	"net/http"
	"github.com/mamaar/gorefactor/internal/cli"
)

func main() {}
`

	result := organizeImports(src, "github.com/mamaar/gorefactor", nil)

	// Should have stdlib group first
	fmtIdx := strings.Index(result, `"fmt"`)
	httpIdx := strings.Index(result, `"net/http"`)
	assertIdx := strings.Index(result, `"github.com/stretchr/testify/assert"`)
	typesIdx := strings.Index(result, `"github.com/mamaar/gorefactor/pkg/types"`)
	cliIdx := strings.Index(result, `"github.com/mamaar/gorefactor/internal/cli"`)

	if fmtIdx < 0 || httpIdx < 0 || assertIdx < 0 || typesIdx < 0 || cliIdx < 0 {
		t.Fatalf("missing expected imports in result:\n%s", result)
	}

	// Stdlib before external
	if httpIdx > assertIdx {
		t.Error("expected stdlib imports before external imports")
	}

	// External before module
	if assertIdx > typesIdx {
		t.Error("expected external imports before module imports")
	}

	// Alphabetical within stdlib
	if fmtIdx > httpIdx {
		t.Error("expected fmt before net/http (alphabetical)")
	}

	// Alphabetical within module
	if cliIdx > typesIdx {
		t.Error("expected internal/cli before pkg/types (alphabetical)")
	}
}

func TestOrganizeImports_WithWorkspaceModules(t *testing.T) {
	src := `package main

import (
	"github.com/mamaar/gorefactor/pkg/types"
	"fmt"
	"github.com/mamaar/othermod/pkg/foo"
	"github.com/stretchr/testify/assert"
)

func main() {}
`

	result := organizeImports(src, "github.com/mamaar/gorefactor", []string{"github.com/mamaar/othermod"})

	fmtIdx := strings.Index(result, `"fmt"`)
	assertIdx := strings.Index(result, `"github.com/stretchr/testify/assert"`)
	otherIdx := strings.Index(result, `"github.com/mamaar/othermod/pkg/foo"`)
	typesIdx := strings.Index(result, `"github.com/mamaar/gorefactor/pkg/types"`)

	if fmtIdx < 0 || assertIdx < 0 || otherIdx < 0 || typesIdx < 0 {
		t.Fatalf("missing expected imports in result:\n%s", result)
	}

	// Order: stdlib < external < workspace < module
	if fmtIdx > assertIdx {
		t.Error("expected stdlib before external")
	}
	if assertIdx > otherIdx {
		t.Error("expected external before workspace")
	}
	if otherIdx > typesIdx {
		t.Error("expected workspace before module")
	}
}

func TestOrganizeImports_CgoImport(t *testing.T) {
	src := `package main

// #include <stdio.h>
import "C"

import (
	"fmt"
	"github.com/mamaar/gorefactor/pkg/types"
)

func main() {}
`

	result := organizeImports(src, "github.com/mamaar/gorefactor", nil)

	cIdx := strings.Index(result, `"C"`)
	fmtIdx := strings.Index(result, `"fmt"`)

	if cIdx < 0 || fmtIdx < 0 {
		t.Fatalf("missing expected imports in result:\n%s", result)
	}

	// Cgo should come first
	if cIdx > fmtIdx {
		t.Error("expected cgo import before stdlib imports")
	}
}

func TestOrganizeImports_WithAliases(t *testing.T) {
	src := `package main

import (
	. "github.com/onsi/gomega"
	_ "github.com/lib/pq"
	myalias "github.com/mamaar/gorefactor/pkg/types"
	"fmt"
)

func main() {}
`

	result := organizeImports(src, "github.com/mamaar/gorefactor", nil)

	if !strings.Contains(result, `. "github.com/onsi/gomega"`) {
		t.Error("expected dot import to be preserved")
	}
	if !strings.Contains(result, `_ "github.com/lib/pq"`) {
		t.Error("expected blank import to be preserved")
	}
	if !strings.Contains(result, `myalias "github.com/mamaar/gorefactor/pkg/types"`) {
		t.Error("expected named alias to be preserved")
	}
}

func TestOrganizeImports_SingleImport(t *testing.T) {
	src := `package main

import "fmt"

func main() {}
`

	result := organizeImports(src, "github.com/mamaar/gorefactor", nil)

	if !strings.Contains(result, `"fmt"`) {
		t.Fatalf("expected fmt import in result:\n%s", result)
	}
}

func TestOrganizeImports_NoImports(t *testing.T) {
	src := `package main

func main() {}
`

	result := organizeImports(src, "github.com/mamaar/gorefactor", nil)
	if result != src {
		t.Error("expected source to be unchanged when there are no imports")
	}
}

func TestOrganizeImports_MultipleImportBlocks(t *testing.T) {
	src := `package main

import "fmt"

import (
	"net/http"
	"github.com/mamaar/gorefactor/pkg/types"
)

func main() {}
`

	result := organizeImports(src, "github.com/mamaar/gorefactor", nil)

	// Should merge into a single import block
	importCount := strings.Count(result, "import (")
	if importCount != 1 {
		t.Errorf("expected 1 import block, got %d in:\n%s", importCount, result)
	}

	// All imports should be present
	if !strings.Contains(result, `"fmt"`) {
		t.Error("expected fmt import")
	}
	if !strings.Contains(result, `"net/http"`) {
		t.Error("expected net/http import")
	}
	if !strings.Contains(result, `"github.com/mamaar/gorefactor/pkg/types"`) {
		t.Error("expected types import")
	}
}

func TestOrganizeImports_ParseFailure(t *testing.T) {
	src := `this is not valid go code {{{`

	result := organizeImports(src, "github.com/mamaar/gorefactor", nil)
	if result != src {
		t.Error("expected source to be returned unchanged on parse failure")
	}
}

func TestOrganizeImports_BlankLineSeparation(t *testing.T) {
	src := `package main

import (
	"github.com/mamaar/gorefactor/pkg/types"
	"fmt"
	"github.com/stretchr/testify"
)

func main() {}
`

	result := organizeImports(src, "github.com/mamaar/gorefactor", nil)

	// Find the import block
	start := strings.Index(result, "import (")
	end := strings.Index(result[start:], ")") + start
	importBlock := result[start : end+1]

	// Should have blank line separating groups
	lines := strings.Split(importBlock, "\n")
	hasBlankLine := false
	for _, line := range lines {
		if strings.TrimSpace(line) == "" {
			hasBlankLine = true
			break
		}
	}

	if !hasBlankLine {
		t.Errorf("expected blank line between import groups in:\n%s", importBlock)
	}
}

func TestOrganizeImports_WithComments(t *testing.T) {
	src := `package main

import (
	"fmt" // for printing
	"github.com/mamaar/gorefactor/pkg/types"
)

func main() {}
`

	result := organizeImports(src, "github.com/mamaar/gorefactor", nil)

	if !strings.Contains(result, `// for printing`) {
		t.Error("expected inline comment to be preserved")
	}
}

func TestOrganizeImports_AlphabeticalWithinGroup(t *testing.T) {
	src := `package main

import (
	"os"
	"fmt"
	"strings"
	"bufio"
)

func main() {}
`

	result := organizeImports(src, "github.com/mamaar/gorefactor", nil)

	bufioIdx := strings.Index(result, `"bufio"`)
	fmtIdx := strings.Index(result, `"fmt"`)
	osIdx := strings.Index(result, `"os"`)
	stringsIdx := strings.Index(result, `"strings"`)

	if bufioIdx < 0 || fmtIdx < 0 || osIdx < 0 || stringsIdx < 0 {
		t.Fatalf("missing imports in result:\n%s", result)
	}

	if bufioIdx > fmtIdx || fmtIdx > osIdx || osIdx > stringsIdx {
		t.Errorf("expected alphabetical order: bufio < fmt < os < strings in:\n%s", result)
	}
}
