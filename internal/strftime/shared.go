package strftime

import "sync"

// Shared is Compile, each format compiled once and its Format shared, which nothing
// writes once it is compiled: a kernel compiled a format for every batch it formatted
// or parsed (step 399, B5 of v0.8-scope.md). A format that does not compile is
// compiled again, and refused again.
//
// At most maxShared formats are kept, so a service passing its users' formats does
// not grow the cache without end: past it a format is compiled each time, as before.
func Shared(src string) (*Format, error) {
	shared.mu.Lock()
	f, ok := shared.m[src]
	shared.mu.Unlock()
	if ok {
		return f, nil
	}
	f, err := Compile(src)
	if err != nil {
		return nil, err
	}
	shared.mu.Lock()
	if len(shared.m) < maxShared {
		shared.m[src] = f
	}
	shared.mu.Unlock()
	return f, nil
}

const maxShared = 256

var shared = struct {
	mu sync.Mutex
	m  map[string]*Format
}{m: map[string]*Format{}}
