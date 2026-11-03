package main

import "strconv"

func parse(s string) (int, error) {
	n, err := strconv.Atoi(s)
	if err != nil {
		return 0, err
	}
	return n, nil
}

func skipMe(s string) error {
	_, err := strconv.Atoi(s)
	if err != nil {
		return err
	}
	return nil
}

type T struct{}

func (T) run(s string) error {
	_, err := strconv.Atoi(s)
	if err != nil {
		return err
	}
	return nil
}

func main() {
	_, _ = parse("1")
	_ = skipMe("1")
	_ = T{}.run("1")
}
