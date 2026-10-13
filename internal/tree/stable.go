package tree

import "github.com/jeon-jihyeon/moldrain/internal/token"

// The first depth positions not marked unstable
// With none left the bucket branches on nothing
// That is the same as a depth 2 Drain tree
func (b *bucket) choose(depth int) []int {
	pos := make([]int, 0, depth)
	for p := 0; p < b.n && len(pos) < depth; p++ {
		if !b.unstable[p] {
			pos = append(pos, p)
		}
	}
	return pos
}

// Counts the constants of a new template per position
// A position whose distinct constants reach MaxChildren turns unstable and drops its set
// Reports whether a chosen position turned unstable and the bucket may still rebuild
func (t *Tree) countPositions(b *bucket, c *Cluster) bool {
	limit := t.cfg.opt.MaxChildren
	rebuild := false
	for p, s := range c.tpl.Slots {
		// a constant with a digit never makes a child
		// It never makes a position unstable
		if b.unstable[p] || s.variable() || t.cfg.digit(s.Sym) {
			continue
		}
		set := b.seen[p]
		if set == nil {
			set = map[uint32]struct{}{}
			b.seen[p] = set
		}
		set[s.Sym] = struct{}{}
		if len(set) < limit {
			continue
		}
		b.unstable[p] = true
		b.seen[p] = nil
		for _, q := range b.pos {
			if q == p {
				rebuild = true
			}
		}
	}
	return rebuild && b.rebuilds < t.cfg.opt.MaxRebuilds
}

// Rebuilds a bucket on its next stable positions from its templates alone
// Past lines are never read
// A template that now shares a leaf with a similar older one merges into it
func (t *Tree) rebuild(b *bucket) {
	b.rebuilds++
	b.pos = b.choose(t.cfg.opt.Depth)
	old := b.order
	b.order = make([]*Cluster, 0, len(old))
	b.root = &node{}
	b.dirty = true
	// a merge can put a later template into a leaf before the loop reaches it
	placed := make(map[*Cluster]bool, len(old))
	for _, c := range old {
		if placed[c] {
			b.order = append(b.order, c)
			continue
		}
		nd := b.path(c.tpl, &t.cfg)
		if c.alias == nil {
			if o := t.cfg.bestTemplate(nd.leaf, c.tpl); o != nil {
				placed[t.absorb(c, o)] = true
				continue
			}
		}
		nd.leaf = append(nd.leaf, c)
		placed[c] = true
		b.order = append(b.order, c)
	}
}

// The most similar template of a leaf for a template taken as a line
func (cfg *config) bestTemplate(leaf []*Cluster, tpl Template) *Cluster {
	var best *Cluster
	bestSim := -1.0
	for _, o := range leaf {
		if o.alias != nil {
			continue
		}
		sim, ok := templateSimilarity(o.tpl, tpl)
		if !ok || sim < cfg.opt.SimTh {
			continue
		}
		if sim > bestSim || (sim == bestSim && o.wins(best)) {
			best, bestSim = o, sim
		}
	}
	return best
}

// Similarity of template c to template o
// Scored as if c were a line
// A variable slot of c matches only a variable slot of o
func templateSimilarity(o, c Template) (sim float64, ok bool) {
	if len(o.Slots) == 0 {
		return 1, true
	}
	score := 0
	for i, s := range o.Slots {
		x := c.Slots[i]
		if s.Span != x.Span {
			return 0, false
		}
		switch {
		case !s.variable() && !x.variable() && x.Sym == s.Sym:
			score++
		case s.Pinned || x.Pinned:
			// a pinned template never absorbs another value nor goes into its source
			return 0, false
		}
	}
	return float64(score) / float64(len(o.Slots)), true
}

// The template o becomes when it absorbs template c
func mergeTemplates(o, c Template) (Template, bool) {
	var slots []Slot
	for i, s := range o.Slots {
		x := c.Slots[i]
		ns := s
		switch {
		case s.Span:
		case s.variable():
			if x.Kind != s.Kind && s.Kind != token.Var {
				ns = Slot{Kind: token.Var}
			}
		case x.variable():
			ns = Slot{Kind: x.Kind}
		case x.Sym != s.Sym:
			ns = Slot{Kind: token.Var}
		}
		if ns == s {
			continue
		}
		if slots == nil {
			slots = append([]Slot(nil), o.Slots...)
		}
		slots[i] = ns
	}
	if slots == nil {
		return o, false
	}
	return Template{Slots: slots, Gaps: o.Gaps}, true
}

// Merges c into o during a rebuild
// o takes the template both fit with the counts and aliases of c
// Returns the cluster that holds the merged template
// It is another one when o's new template was already taken
func (t *Tree) absorb(c, o *Cluster) *Cluster {
	from := c.id
	b := t.bucketOf(c)
	delete(t.byID, c.id)
	delete(b.byID, c.id)
	tpl, changed := mergeTemplates(o.tpl, c.tpl)
	t.mergeInto(c, o)
	if changed {
		o = t.setTemplate(o, tpl, o.last, 0, nil)
	}
	x := o.Canonical()
	sh := t.shape(x.tpl)
	t.send(Event{Kind: Changed, From: from, To: x.id, Shape: &sh, At: o.last})
	return o
}
