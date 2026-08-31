package main

func parse(matches []string) bool {
	if len(matches) > 1 {
		isRepeated := matches[1] == "repeated"
		hasPresence := matches[1] == "optional"
		value := "plain"
		if hasPresence {
			value = "optional"
		}
		// repeated values use a collection shape
		if isRepeated {
			value = "repeated"
		}
		println(value)
		return hasPresence
	}
	return false
}

func main() {
	parse([]string{"field", "optional"})
}
