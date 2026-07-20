package main

func doA() error           { return nil }
func doB() error           { return nil }
func doC() error           { return nil }
func doPair() (int, error) { return 0, nil }

// twoSiblings: the second if's `err` collides with the first hoisted
// `err`, so it must be downgraded from `:=` to `=`.
func twoSiblings() error {
	if err := doA(); err != nil {
		return err
	}
	if err := doB(); err != nil {
		return err
	}
	return nil
}

// threeSiblings: same as twoSiblings but with a third collision.
func threeSiblings() error {
	if err := doA(); err != nil {
		return err
	}
	if err := doB(); err != nil {
		return err
	}
	if err := doC(); err != nil {
		return err
	}
	return nil
}

// partialCollision: only `err` pre-exists after the first hoist; `x` is
// new, so the second statement must stay `:=`.
func partialCollision() (int, error) {
	if err := doA(); err != nil {
		return 0, err
	}
	if x, err := doPair(); err != nil {
		return 0, err
	} else {
		return x, nil
	}
}

// paramCollision: the if's `err` collides with the function parameter
// `err`, so it must be downgraded from `:=` to `=`.
func paramCollision(err error) error {
	if err := doA(); err != nil {
		return err
	}
	return err
}

func main() {
	_ = twoSiblings()
	_ = threeSiblings()
	_, _ = partialCollision()
	_ = paramCollision(nil)
}
