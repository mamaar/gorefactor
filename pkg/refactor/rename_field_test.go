package refactor

import (
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/mamaar/gorefactor/pkg/types"
)

// writeRenameFieldWorkspace writes the given files (relative path -> content)
// under a temp module directory and loads a fully type-checked workspace.
func writeRenameFieldWorkspace(t *testing.T, files map[string]string) (RefactorEngine, *types.Workspace) {
	t.Helper()
	dir := t.TempDir()

	files["go.mod"] = "module example.com/m\n\ngo 1.21\n"
	for rel, content := range files {
		full := filepath.Join(dir, rel)
		err := os.MkdirAll(filepath.Dir(full), 0o755)
		if err != nil {
			t.Fatalf("mkdir %s: %v", full, err)
		}
		err = os.WriteFile(full, []byte(content), 0o644)
		if err != nil {
			t.Fatalf("write %s: %v", full, err)
		}
	}

	engine := CreateEngineWithConfig(&EngineConfig{SkipCompilation: true}, slog.New(slog.NewTextHandler(io.Discard, nil)))
	ws, err := engine.LoadWorkspace(dir)
	if err != nil {
		t.Fatalf("LoadWorkspace: %v", err)
	}
	return engine, ws
}

func TestRenameField_Success(t *testing.T) {
	engine, ws := writeRenameFieldWorkspace(t, map[string]string{
		"foo/user.go": `package foo

type User struct {
	Name string
	age  int
}

func (u User) Describe() string {
	return u.Name
}
`,
		"app/main.go": `package app

import "example.com/m/foo"

func Run() string {
	u := foo.User{Name: "bob"}
	return u.Name
}
`,
	})

	plan, err := engine.RenameField(ws, types.RenameFieldRequest{
		TypeName:     "User",
		FieldName:    "Name",
		NewFieldName: "FullName",
	})
	if err != nil {
		t.Fatalf("RenameField: %v", err)
	}

	// Every change must rewrite exactly "Name" -> "FullName".
	for _, ch := range plan.Changes {
		if ch.OldText != "Name" || ch.NewText != "FullName" {
			t.Errorf("unexpected change %+v", ch)
		}
		if ch.End-ch.Start != len("Name") {
			t.Errorf("change span %d != len(Name); %+v", ch.End-ch.Start, ch)
		}
	}

	// One definition change plus three references:
	//   u.Name in Describe, foo.User{Name:...}, u.Name in Run.
	if len(plan.Changes) != 4 {
		t.Fatalf("expected 4 changes, got %d: %+v", len(plan.Changes), plan.Changes)
	}

	// Both files must be affected (cross-package reference).
	userFile := filepath.Join(ws.RootPath, "foo", "user.go")
	appFile := filepath.Join(ws.RootPath, "app", "main.go")
	if !contains(plan.AffectedFiles, userFile) {
		t.Errorf("affected files missing %s: %v", userFile, plan.AffectedFiles)
	}
	if !contains(plan.AffectedFiles, appFile) {
		t.Errorf("affected files missing %s: %v", appFile, plan.AffectedFiles)
	}

	if !plan.Reversible {
		t.Error("plan should be reversible")
	}
}

func TestRenameField_EmbeddedFieldError(t *testing.T) {
	engine, ws := writeRenameFieldWorkspace(t, map[string]string{
		"foo/user.go": `package foo

type Base struct {
	ID int
}

type User struct {
	Base
	Name string
}
`,
	})

	_, err := engine.RenameField(ws, types.RenameFieldRequest{
		TypeName:     "User",
		FieldName:    "Base",
		NewFieldName: "Embedded",
	})
	if err == nil {
		t.Fatal("expected error renaming embedded field, got nil")
	}
	if !strings.Contains(err.Error(), "embedded") || !strings.Contains(err.Error(), "rename_symbol") {
		t.Errorf("expected targeted embedded-field error, got: %v", err)
	}
}

func TestRenameField_CollisionWithExistingField(t *testing.T) {
	engine, ws := writeRenameFieldWorkspace(t, map[string]string{
		"foo/user.go": `package foo

type User struct {
	Name     string
	FullName string
}
`,
	})

	_, err := engine.RenameField(ws, types.RenameFieldRequest{
		TypeName:     "User",
		FieldName:    "Name",
		NewFieldName: "FullName",
	})
	if err == nil {
		t.Fatal("expected field-collision error, got nil")
	}
	if !strings.Contains(err.Error(), "already exists") {
		t.Errorf("expected 'already exists' error, got: %v", err)
	}
}

func TestRenameField_CollisionWithMethod(t *testing.T) {
	engine, ws := writeRenameFieldWorkspace(t, map[string]string{
		"foo/user.go": `package foo

type User struct {
	Name string
}

func (u User) FullName() string {
	return u.Name
}
`,
	})

	_, err := engine.RenameField(ws, types.RenameFieldRequest{
		TypeName:     "User",
		FieldName:    "Name",
		NewFieldName: "FullName",
	})
	if err == nil {
		t.Fatal("expected method-collision error, got nil")
	}
	if !strings.Contains(err.Error(), "method") && !strings.Contains(err.Error(), "promoted") {
		t.Errorf("expected method-collision error, got: %v", err)
	}
}

func TestRenameField_TypeNotFound(t *testing.T) {
	engine, ws := writeRenameFieldWorkspace(t, map[string]string{
		"foo/user.go": `package foo

type User struct {
	Name string
}
`,
	})

	_, err := engine.RenameField(ws, types.RenameFieldRequest{
		TypeName:     "Ghost",
		FieldName:    "Name",
		NewFieldName: "FullName",
	})
	if err == nil {
		t.Fatal("expected type-not-found error, got nil")
	}
	if !strings.Contains(err.Error(), "not found") {
		t.Errorf("expected 'not found' error, got: %v", err)
	}
}
