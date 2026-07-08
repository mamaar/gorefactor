package model

// Greet uses a value-receiver plain selector on the field being renamed.
func (u User) Greet() string {
	return "Hello, " + u.Name
}

// NewLegacyUser builds a User with an unkeyed composite literal. It lives in
// the same package as User (unlike an equivalent cross-package literal,
// which go vet's composites check would flag), so this exercises the
// unkeyed-literal code path cleanly: the literal has no field names at all,
// so renaming User.Name -> FullName must leave it byte-identical.
func NewLegacyUser() User {
	return User{"Eve", 22}
}
