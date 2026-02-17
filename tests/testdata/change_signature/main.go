package main

import "fmt"

func Greet(name string) string {
	return "Hello, " + name
}

func main() {
	fmt.Println(Greet("Alice"))
	fmt.Println(Greet("Bob"))
}
