package main

// errCount looks like an error to the analyzer but is the int result.
func count() (int, error) {
	errCount := 1
	return errCount, nil
}

func main() {
	_, _ = count()
}
