package main

// init is a root: it cannot gain a parameter, so it fabricates
// context.TODO() at its call to seed.
func init() {
	seed()
}

// spawn is not itself climbed: the call to seed lives inside a goroutine
// closure, which is a threading root of its own because it may outlive
// spawn. spawn's signature must stay untouched.
func spawn() {
	go func() {
		seed()
	}()
}

// main is a root: it cannot gain a parameter either.
func main() {
	seed()
	spawn()
}

// seed is the seed function.
func seed() {}
