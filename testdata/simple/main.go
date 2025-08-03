package main

import "fmt"

func printWelcome() {
	fmt.Println("Hello from simple test project")
	result := Add(5, 3)
	fmt.Printf("5 + 3 = %d\n", result)
}
func printResults() {
	printWelcome()
}
func main() {
	printResults()

	greet := Greet("World")
	fmt.Println(greet)
}

// Add adds two integers and returns the result
func Add(a, b int) int {
	return a + b
}

// Greet returns a greeting message
func Greet(name string) string {
	return fmt.Sprintf("Hello, %s!", name)
}
