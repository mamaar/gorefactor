package main

type Store struct {
	data map[string]string
}

func (s *Store) Get(key string) string {
	return s.data[key]
}

func (s *Store) Set(key, value string) {
	s.data[key] = value
}

func main() {
	s := &Store{data: make(map[string]string)}
	s.Set("key", "value")
	_ = s.Get("key")
}
