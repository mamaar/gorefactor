package main

// errCount looks like an error to the analyzer but is the pointer result.
func count() (*int, error) {
	one := 1
	errCount := &one
	if errCount != nil {
		return errCount, nil
	}
	return nil, nil
}

func main() {
	_, _ = count()
}
