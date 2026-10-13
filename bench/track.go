package main

import (
	"context"
	"sync"

	"github.com/jeon-jihyeon/moldrain"
	"github.com/jeon-jihyeon/moldrain/resolve"
)

// Follows template ids through Changed and Evicted so every line ends in the group its template ended in
// An id is the hash of its template
// It can come back later as a new cluster
// Groups are therefore labels joined by union find
type tracker struct {
	mu     sync.Mutex
	cur    map[moldrain.ID]int32
	parent []int32
	// truth event counts per label root
	// Kept only for the oracle
	counts []map[int32]int
	oracle bool
	// truth event of the line that created each provisional template
	// Kept only for the oracle
	created map[string]int32
	event   int32
}

func newTracker(oracle bool) *tracker {
	return &tracker{cur: map[moldrain.ID]int32{}, oracle: oracle, created: map[string]int32{}}
}

func (t *tracker) find(x int32) int32 {
	for t.parent[x] != x {
		t.parent[x] = t.parent[t.parent[x]]
		x = t.parent[x]
	}
	return x
}

func (t *tracker) label(id moldrain.ID) int32 {
	if l, ok := t.cur[id]; ok {
		return t.find(l)
	}
	l := int32(len(t.parent))
	t.parent = append(t.parent, l)
	if t.oracle {
		t.counts = append(t.counts, map[int32]int{})
	}
	t.cur[id] = l
	return l
}

func (t *tracker) union(a, b int32) int32 {
	a, b = t.find(a), t.find(b)
	if a == b {
		return a
	}
	t.parent[a] = b
	if t.oracle {
		for e, n := range t.counts[a] {
			t.counts[b][e] += n
		}
		t.counts[a] = nil
	}
	return b
}

// Called on the training goroutine for every miner event
func (t *tracker) onEvent(ev moldrain.Event) {
	t.mu.Lock()
	defer t.mu.Unlock()
	switch ev.Kind {
	case moldrain.Created:
		t.label(ev.ID)
		if t.oracle && ev.Line != nil {
			t.created[string(ev.Line)] = t.event
		}
	case moldrain.Changed:
		from := t.label(ev.From)
		delete(t.cur, ev.From)
		if to, ok := t.cur[ev.To]; ok {
			t.union(from, to)
		} else {
			t.cur[ev.To] = from
		}
	case moldrain.Evicted:
		delete(t.cur, ev.ID)
	}
}

// Records the group of a line
// The oracle also counts its truth event
func (t *tracker) line(id moldrain.ID, event int32) int32 {
	t.mu.Lock()
	defer t.mu.Unlock()
	l := t.label(id)
	if t.oracle {
		t.counts[l][event]++
	}
	return l
}

// The truth event of the line about to be trained
// Set before Train so a Created event can record it
func (t *tracker) next(event int32) {
	t.mu.Lock()
	t.event = event
	t.mu.Unlock()
}

// Answers from the ground truth: the candidate whose lines are mostly the pending line's event
func (t *tracker) Resolve(_ context.Context, batch []resolve.Pending) ([]resolve.Decision, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	out := make([]resolve.Decision, len(batch))
	for i, p := range batch {
		out[i].Index = i
		want, ok := t.created[string(p.Line)]
		if !ok {
			continue
		}
		for _, c := range p.Candidates {
			id, err := moldrain.ParseID(c.ID)
			if err != nil {
				continue
			}
			l, ok := t.cur[id]
			if !ok {
				continue
			}
			if majority(t.counts[t.find(l)]) == want {
				out[i].Same = c.ID
				break
			}
		}
	}
	return out, nil
}

func majority(counts map[int32]int) int32 {
	best, bestN := int32(-1), 0
	for e, n := range counts {
		if n > bestN || (n == bestN && e < best) {
			best, bestN = e, n
		}
	}
	return best
}
