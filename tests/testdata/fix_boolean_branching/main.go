package main

import "fmt"

func handleFormat(format string) {
	isJSON := format == "json"
	isXML := format == "xml"
	isCSV := format == "csv"

	if isJSON {
		fmt.Println("handling json")
	} else if isXML {
		fmt.Println("handling xml")
	} else if isCSV {
		fmt.Println("handling csv")
	} else {
		fmt.Println("unknown format")
	}
}

func main() {
	handleFormat("json")
}
