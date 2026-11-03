package main

func f() error { return nil }

// jumps: hoisting `err` into the function body puts a variable declaration
// between the goto and its label, which Go rejects.
func jumps(skip bool) error {
	if skip {
		goto done
	}
	if err := f(); err != nil {
		return err
	}
done:
	return nil
}

func main() { _ = jumps(false) }
