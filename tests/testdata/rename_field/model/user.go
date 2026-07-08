package model

// User is the struct whose Name field is being renamed to FullName.
type User struct {
	Name string
	Age  int
}

// Admin embeds User, so its Name field is promoted.
type Admin struct {
	User
	Level int
}

// Company is unrelated to User and also has a field called Name.
// This is the regression guard: Company.Name is a distinct go/types
// object from User.Name and MUST NOT be renamed. If a future
// implementation regresses to name-based matching instead of
// object-identity matching, this is what catches it.
type Company struct {
	Name string
}
