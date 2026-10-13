package token

import (
	"hash/maphash"
	"sync"
	"sync/atomic"
)

// No symbol: the id Lookup gives a constant the interner has never seen
// It equals no interned symbol
// An unseen word never matches a template constant
const Unknown uint32 = 1<<32 - 1

const internShards = 32

// Constant text to a small id
// Append only and shared by every bucket
// 1. lookups take no lock since each shard publishes a read only map by atomic pointer
// 2. an insert works on a dirty copy under the shard lock until enough misses promote it
type Interner struct {
	seed   maphash.Seed
	shards [internShards]shard

	mu    sync.RWMutex
	texts []string
}

type shard struct {
	read atomic.Pointer[map[string]uint32]
	// set while dirty holds entries read lacks
	amended atomic.Bool

	mu     sync.Mutex
	dirty  map[string]uint32
	misses int
}

func NewInterner() *Interner {
	in := &Interner{seed: maphash.MakeSeed(), texts: []string{""}}
	for i := range in.shards {
		m := map[string]uint32{}
		in.shards[i].read.Store(&m)
	}
	return in
}

func (in *Interner) shard(b []byte) *shard {
	return &in.shards[maphash.Bytes(in.seed, b)%internShards]
}

// The id of b
// Unknown when b was never interned
// Safe for any number of goroutines and allocation free
func (in *Interner) Lookup(b []byte) (uint32, bool) {
	s := in.shard(b)
	if sym, ok := (*s.read.Load())[string(b)]; ok {
		return sym, true
	}
	if !s.amended.Load() {
		return Unknown, false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if sym, ok := (*s.read.Load())[string(b)]; ok {
		return sym, true
	}
	if s.dirty == nil {
		return Unknown, false
	}
	sym, ok := s.dirty[string(b)]
	s.missLocked()
	if !ok {
		return Unknown, false
	}
	return sym, true
}

// Promotes dirty to read once misses have paid for the copy the next insert makes
func (s *shard) missLocked() {
	s.misses++
	if s.misses < len(s.dirty) {
		return
	}
	m := s.dirty
	s.read.Store(&m)
	s.dirty = nil
	s.misses = 0
	s.amended.Store(false)
}

// The id of b assigned on first sight
// Ids count up from 1 in insert order
// One insert order gives the same ids
func (in *Interner) Intern(b []byte) uint32 {
	if sym, ok := in.Lookup(b); ok {
		return sym
	}
	s := in.shard(b)
	s.mu.Lock()
	defer s.mu.Unlock()
	read := *s.read.Load()
	if sym, ok := read[string(b)]; ok {
		return sym
	}
	if s.dirty == nil {
		s.dirty = make(map[string]uint32, len(read)+1)
		for k, v := range read {
			s.dirty[k] = v
		}
	} else if sym, ok := s.dirty[string(b)]; ok {
		return sym
	}
	text := string(b)
	in.mu.Lock()
	sym := uint32(len(in.texts))
	in.texts = append(in.texts, text)
	in.mu.Unlock()
	s.dirty[text] = sym
	s.amended.Store(true)
	return sym
}

// The text of an interned id
// Empty for 0 and Unknown
func (in *Interner) Text(sym uint32) string {
	in.mu.RLock()
	defer in.mu.RUnlock()
	if sym == Unknown || int(sym) >= len(in.texts) {
		return ""
	}
	return in.texts[sym]
}
