package tree

import (
	"cmp"
	"maps"
	"slices"

	"github.com/jeon-jihyeon/moldrain/internal/token"
)

// An immutable copy of the tree that any number of goroutines match against without a lock
type View struct {
	cfg     *config
	buckets []*bucket
}

func (v *View) at(n int) *bucket {
	if v == nil || n < 0 || n >= len(v.buckets) {
		return nil
	}
	return v.buckets[n]
}

// Whether a bucket changed since the last Publish
func (t *Tree) Dirty() bool {
	for _, b := range t.buckets {
		if b != nil && b.dirty {
			return true
		}
	}
	return false
}

// A new view that clones the buckets changed since prev and shares the rest with it
func (t *Tree) Publish(prev *View) *View {
	nb := make([]*bucket, len(t.buckets))
	if prev != nil {
		copy(nb, prev.buckets)
	}
	for i, b := range t.buckets {
		if b == nil || (!b.dirty && nb[i] != nil) {
			continue
		}
		nb[i] = b.clone()
		b.dirty = false
	}
	return &View{cfg: &t.cfg, buckets: nb}
}

// A copy that shares only immutable parts
// Templates and gaps and positions
func (b *bucket) clone() *bucket {
	nb := &bucket{
		n:      b.n,
		pos:    b.pos,
		order:  make([]*Cluster, len(b.order)),
		byID:   make(map[ID]*Cluster, len(b.byID)),
		salted: maps.Clone(b.salted),
	}
	copies := make(map[*Cluster]*Cluster, len(b.order))
	for i, c := range b.order {
		cc := *c
		cc.aliases, cc.track, cc.into = nil, nil, nil
		if c.params != nil {
			cc.params = make([][]string, len(c.params))
			for j, p := range c.params {
				cc.params[j] = slices.Clone(p)
			}
		}
		nb.order[i] = &cc
		copies[c] = &cc
		nb.byID[c.id] = &cc
	}
	for _, cc := range nb.order {
		if cc.alias == nil {
			continue
		}
		if x, ok := copies[cc.alias]; ok {
			cc.alias = x
			continue
		}
		// a target in another bucket is frozen as it is now
		x := *cc.alias
		x.alias, x.aliases, x.track, x.into, x.params = nil, nil, nil, nil, nil
		cc.alias = &x
	}
	nb.root = cloneNode(b.root, copies)
	return nb
}

func cloneNode(nd *node, copies map[*Cluster]*Cluster) *node {
	out := &node{}
	if len(nd.kids) > 0 {
		out.kids = make(map[uint32]*node, len(nd.kids))
		for k, kid := range nd.kids {
			out.kids[k] = cloneNode(kid, copies)
		}
	}
	if nd.wild != nil {
		out.wild = cloneNode(nd.wild, copies)
	}
	if len(nd.leaf) > 0 {
		out.leaf = make([]*Cluster, len(nd.leaf))
		for i, c := range nd.leaf {
			out.leaf[i] = copies[c]
		}
	}
	if len(out.leaf) >= postMin {
		out.post = postLeaf(out.leaf)
	}
	return out
}

// The constant slots of a leaf sorted by key
func postLeaf(leaf []*Cluster) []posting {
	n := 0
	for _, c := range leaf {
		n += len(c.tpl.Slots) - c.vars
	}
	post := make([]posting, 0, n)
	for j, c := range leaf {
		for i, s := range c.tpl.Slots {
			if !s.variable() {
				post = append(post, posting{key: postKey(i, s.Sym), at: int32(j)})
			}
		}
	}
	slices.SortFunc(post, func(a, b posting) int { return cmp.Compare(a.key, b.key) })
	return post
}

// The template a line matches without changing anything
// The cluster is canonical
// An alias answers as the template it was merged into
// matched is the cluster the line lined up with
// Its slots give the parameters
func (v *View) Match(line []byte, toks []token.Token) (c, matched *Cluster, ok bool) {
	lookup(v.cfg.in, line, toks)
	f := v.cfg.find(v, v.at(len(toks)), line, toks)
	if f.c == nil {
		return nil, nil, false
	}
	return f.c.Canonical(), f.c, true
}

// Every canonical cluster of the view by token count and then creation
func (v *View) Clusters() []*Cluster { return canonical(v.buckets) }

// The cluster of an id in the view
func (v *View) Get(id ID) *Cluster {
	for _, b := range v.buckets {
		if b == nil {
			continue
		}
		if c, ok := b.byID[id]; ok {
			return c
		}
	}
	return nil
}

// A template near a line and its similarity
type Near struct {
	Cluster *Cluster
	Sim     float64
}

// The k canonical templates of the line's token count most similar to it that are older than before
// A before the view does not hold yet is newer than all it holds
// Ties go to the older template
func (v *View) Nearest(line []byte, toks []token.Token, k int, before ID) []Near {
	b := v.at(len(toks))
	if b == nil || k <= 0 {
		return nil
	}
	lookup(v.cfg.in, line, toks)
	limit := ^uint64(0)
	if c := v.Get(before); c != nil {
		limit = c.seq
	}
	var out []Near
	for _, c := range b.order {
		if c.alias != nil || c.seq >= limit {
			continue
		}
		sim, ok := similarity(c.tpl, toks)
		if !ok {
			continue
		}
		out = append(out, Near{Cluster: c, Sim: sim})
	}
	slices.SortStableFunc(out, func(a, b Near) int {
		switch {
		case a.Sim > b.Sim:
			return -1
		case a.Sim < b.Sim:
			return 1
		}
		return cmp.Compare(a.Cluster.seq, b.Cluster.seq)
	})
	if len(out) > k {
		out = out[:k]
	}
	return out
}
