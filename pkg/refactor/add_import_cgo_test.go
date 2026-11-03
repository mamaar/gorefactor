package refactor

import (
	"strings"
	"testing"
)

func addContextImport(t *testing.T, src string) string {
	t.Helper()
	ws := buildScopeWorkspace(t, map[string]string{"p/p.go": src})
	pkg := findPackageByName(t, ws, "p")
	var path string
	for _, f := range pkg.Files {
		path = f.Path
	}
	c := GenerateAddImportChange(ws, path, "", "context")
	if c == nil {
		t.Fatal("no import change")
	}
	content := originalContent(t, ws, path)
	return string(content[:c.Start]) + c.NewText + string(content[c.End:])
}

const cgoPreamble = "package p\n\n/*\n#include <stdlib.h>\n*/\nimport \"C\"\n"

func TestGenerateAddImportChange_KeepsCgoImportStandalone(t *testing.T) {
	cases := map[string]struct{ src, want string }{
		"grouped": {
			cgoPreamble + "\nimport (\n\t\"fmt\"\n)\n\nvar _ = fmt.Sprint\n",
			"import \"C\"\n\nimport (\n\t\"fmt\"\n\t\"context\"\n)",
		},
		"single": {
			cgoPreamble + "\nimport \"fmt\"\n\nvar _ = fmt.Sprint\n",
			"import \"C\"\n\nimport (\n\t\"fmt\"\n\t\"context\"\n)",
		},
		"only C": {
			cgoPreamble + "\nfunc f() {}\n",
			"import \"C\"\nimport \"context\"\n",
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			got := addContextImport(t, tc.src)
			if !strings.Contains(got, tc.want) {
				t.Errorf("want %q in:\n%s", tc.want, got)
			}
		})
	}
}
