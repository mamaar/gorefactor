package main

import "fmt"

func deprecatedHelper() string {
	return "deprecated"
}

func main() {
	fmt.Println(deprecatedHelper())
	fmt.Println(deprecatedHelper())
}
