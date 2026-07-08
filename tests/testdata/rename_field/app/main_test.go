package app

import (
	"testing"

	"tests/rename_field/model"
)

// TestUserName exercises a _test.go reference to model.User.Name.
func TestUserName(t *testing.T) {
	u := model.User{Name: "Dave", Age: 50}
	if u.Name != "Dave" {
		t.Fatalf("got %q, want %q", u.Name, "Dave")
	}
}
