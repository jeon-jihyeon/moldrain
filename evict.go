package moldrain

import (
	"container/heap"
	"math"
	"time"
)

// Templates by the line number of the last line they took with the least recent first
// An entry goes stale when its template takes more lines or changes id
// It is fixed when it reaches the top
// No line pays for a heap update as with an LRU list
type recency []recent

type recent struct {
	hit uint64
	id  ID
}

func (r recency) Len() int { return len(r) }
func (r recency) Less(i, j int) bool {
	if r[i].hit != r[j].hit {
		return r[i].hit < r[j].hit
	}
	return r[i].id < r[j].id
}
func (r recency) Swap(i, j int) { r[i], r[j] = r[j], r[i] }
func (r *recency) Push(x any)   { *r = append(*r, x.(recent)) }
func (r *recency) Pop() any {
	old := *r
	x := old[len(old)-1]
	*r = old[:len(old)-1]
	return x
}

// Pushes the recency of a template that was created or changed id
// Stale entries pile up while the tree stays under the cap
// The heap is rebuilt once it holds twice the templates
func (m *Miner) track(id ID) {
	if m.opt.MaxClusters == 0 {
		return
	}
	if c := m.tree.Get(id); c != nil {
		heap.Push(&m.recency, recent{hit: c.Hit(), id: id})
	}
	if len(m.recency) > 2*max(m.tree.Len(), m.opt.MaxClusters) {
		m.recency = m.recency[:0]
		for _, c := range m.tree.Clusters() {
			m.recency = append(m.recency, recent{hit: c.Hit(), id: c.ID()})
		}
		heap.Init(&m.recency)
	}
}

// Evicts the least recently trained templates until the tree is within the cap
// keep is the template the current line went to and is never evicted for it
func (m *Miner) evictLocked(keep ID, at time.Time) {
	for m.opt.MaxClusters > 0 && m.tree.Len() > m.opt.MaxClusters && m.recency.Len() > 0 {
		top := heap.Pop(&m.recency).(recent)
		c := m.tree.Get(top.id)
		if c == nil || c.Aliased() {
			continue
		}
		if top.id == keep {
			// keep goes behind every other entry
			// Meeting it there again means none is left
			if top.hit == math.MaxUint64 {
				heap.Push(&m.recency, top)
				return
			}
			heap.Push(&m.recency, recent{hit: math.MaxUint64, id: top.id})
			continue
		}
		if c.Hit() != top.hit {
			heap.Push(&m.recency, recent{hit: c.Hit(), id: top.id})
			continue
		}
		_ = m.tree.Remove(top.id, at)
	}
}
