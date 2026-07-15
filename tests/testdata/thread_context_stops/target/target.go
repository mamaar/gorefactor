package target

// Do is the seed. It does not yet import "context", exercising import
// insertion in a package that is not any of the packages holding a caller.
func Do() {}
