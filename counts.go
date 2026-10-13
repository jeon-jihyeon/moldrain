package moldrain

import (
	"cmp"
	"slices"
	"sync"
	"time"
)

// Lines of one template in a time range
type Count struct {
	ID ID
	N  uint64
}

// Lines per template per time bucket aligned to Unix time
// Older buckets drop once KeepBuckets newer ones exist
type counts struct {
	mu      sync.RWMutex
	width   int64
	keep    int64
	buckets map[int64]map[ID]uint64
	newest  int64
	// whether any line was counted and newest holds a bucket
	started bool
}

func newCounts(width time.Duration, keep int) *counts {
	return &counts{width: int64(width), keep: int64(keep), buckets: map[int64]map[ID]uint64{}}
}

// Floor division so times before 1970 land in the bucket they start
func (c *counts) key(at time.Time) int64 {
	ns := at.UnixNano()
	k := ns / c.width
	if ns%c.width < 0 {
		k--
	}
	return k
}

func (c *counts) add(id ID, at time.Time, n uint64) {
	k := c.key(at)
	c.mu.Lock()
	defer c.mu.Unlock()
	if !c.started || k > c.newest {
		c.newest, c.started = k, true
		for old := range c.buckets {
			if old <= c.newest-c.keep {
				delete(c.buckets, old)
			}
		}
	}
	if k <= c.newest-c.keep {
		return
	}
	b := c.buckets[k]
	if b == nil {
		b = map[ID]uint64{}
		c.buckets[k] = b
	}
	b[id] += n
}

// Moves the counts of a template whose id changed in every kept bucket
func (c *counts) move(from, to ID) {
	c.mu.Lock()
	defer c.mu.Unlock()
	for _, b := range c.buckets {
		if n, ok := b[from]; ok {
			b[to] += n
			delete(b, from)
		}
	}
}

// Lines per template in the buckets that overlap the range from inclusive to exclusive
// By count descending
func (c *counts) window(from, to time.Time) []Count {
	lo, hi := c.key(from), c.key(to.Add(-1))
	sum := map[ID]uint64{}
	c.mu.RLock()
	for k, b := range c.buckets {
		if k < lo || k > hi {
			continue
		}
		for id, n := range b {
			sum[id] += n
		}
	}
	c.mu.RUnlock()
	out := make([]Count, 0, len(sum))
	for id, n := range sum {
		out = append(out, Count{ID: id, N: n})
	}
	slices.SortFunc(out, func(a, b Count) int {
		if a.N != b.N {
			return cmp.Compare(b.N, a.N)
		}
		return cmp.Compare(a.ID, b.ID)
	})
	return out
}
