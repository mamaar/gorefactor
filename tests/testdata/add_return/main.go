package main

import "fmt"

// GetConfig returns a config value for the given environment.
// After add_return this should become (string, error).
func GetConfig(isProd bool) string {
	if isProd {
		return "prod-config"
	}
	return "test-config"
}

func main() {
	// Simple assignment: should become cfg, _ = GetConfig(true)
	cfg := GetConfig(true)
	fmt.Println(cfg)

	// Expression statement: result discarded, should become _ = GetConfig(false)
	GetConfig(false)

	// Another simple assignment
	val := GetConfig(true)
	_ = val
}
