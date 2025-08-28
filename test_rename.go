package main

import "fmt"

type Writer interface {
	WriteData(data string) error
}

type FileWriter struct {
	filename string
}

func (fw *FileWriter) WriteData(data string) error {
	fmt.Printf("Writing '%s' to file: %s\n", data, fw.filename)
	return nil
}

func main() {
	var w Writer = &FileWriter{filename: "test.txt"}
	w.WriteData("Hello, World!")
}
