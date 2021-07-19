package main

import (
	"math/rand"
	"sync"

	"github.com/oklog/ulid/v2"
)

type syncMonotonicReader struct {
	ulid.MonotonicReader
	mutex *sync.Mutex
}

func NewSyncMonotonicReader(seed int64) ulid.MonotonicReader {
	return &syncMonotonicReader{
		MonotonicReader: ulid.Monotonic(rand.New(rand.NewSource(seed)), 0),
		mutex:           new(sync.Mutex),
	}
}

func (m *syncMonotonicReader) MonotonicRead(ms uint64, entropy []byte) (err error) {
	m.mutex.Lock()
	defer m.mutex.Unlock()

	return m.MonotonicReader.MonotonicRead(ms, entropy)
}
