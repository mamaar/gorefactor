package main

// Store is a workspace-declared interface. Threading one implementation's
// method must rewrite the interface declaration and every implementation
// together, so the result compiles regardless of which implementation a
// caller holds.
type Store interface {
	Save(x int)
}

type MemStore struct{}

func (m *MemStore) Save(x int) {}

type FileStore struct{}

func (f *FileStore) Save(x int) {}

// useMem calls a concrete implementation directly.
func useMem(m *MemStore) {
	m.Save(1)
}

// use calls through the interface, so its call site is only reachable via
// the interface method object.
func use(s Store) {
	s.Save(2)
}

func main() {
	useMem(&MemStore{})
	use(&MemStore{})
}
