package main

import "fmt"

func Process(name string) string {
	return fmt.Sprintf("processing %s", name)
}

func main() {
	fmt.Println(Process("task"))
	fmt.Println(Process("job"))
}
