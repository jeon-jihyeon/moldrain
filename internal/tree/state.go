package tree

import (
	"cmp"
	"fmt"
	"slices"
	"time"
)

// The whole tree in a form that does not depend on one process
// Restoring it gives the same leaves
// Match answers the same before and after
type State struct {
	Seq      uint64
	Lines    uint64
	Buckets  []BucketState
	Clusters []ClusterState
}

type BucketState struct {
	N        int
	Pos      []int
	Unstable []bool
	// distinct constants per position
	// nil for an unstable or unseen position
	Seen     [][]string
	Rebuilds int
	Root     NodeState
	// cluster ids in creation order
	Order []ID
}

type NodeState struct {
	// constant texts of the children in sorted order
	// Kids follow the same order
	Keys []string
	Kids []NodeState
	Wild *NodeState
	Leaf []ID
}

type ClusterState struct {
	ID       ID
	Salt     byte
	Shape    Shape
	Count    uint64
	First    time.Time
	Last     time.Time
	Seq      uint64
	Hit      uint64
	Alias    ID
	HasAlias bool
	Split    bool
}

// A deep copy of the tree
// Buckets by token count and clusters in id order
func (t *Tree) Export() State {
	st := State{Seq: t.seq, Lines: t.lines}
	for _, b := range t.buckets {
		if b == nil {
			continue
		}
		bs := BucketState{
			N:        b.n,
			Pos:      slices.Clone(b.pos),
			Unstable: slices.Clone(b.unstable),
			Seen:     make([][]string, b.n),
			Rebuilds: b.rebuilds,
			Root:     t.exportNode(b.root),
		}
		for p, set := range b.seen {
			for sym := range set {
				bs.Seen[p] = append(bs.Seen[p], t.cfg.in.Text(sym))
			}
			slices.Sort(bs.Seen[p])
		}
		for _, c := range b.order {
			bs.Order = append(bs.Order, c.id)
			cs := ClusterState{
				ID: c.id, Salt: c.salt, Shape: t.shape(c.tpl), Count: c.count,
				First: c.first, Last: c.last, Seq: c.seq, Hit: c.hit, Split: c.split,
			}
			if c.alias != nil {
				cs.Alias, cs.HasAlias = c.alias.id, true
			}
			st.Clusters = append(st.Clusters, cs)
		}
		st.Buckets = append(st.Buckets, bs)
	}
	slices.SortFunc(st.Clusters, func(a, b ClusterState) int { return cmp.Compare(a.ID, b.ID) })
	return st
}

func (t *Tree) exportNode(nd *node) NodeState {
	var ns NodeState
	syms := make([]uint32, 0, len(nd.kids))
	for sym := range nd.kids {
		syms = append(syms, sym)
	}
	slices.SortFunc(syms, func(a, b uint32) int { return cmp.Compare(t.cfg.in.Text(a), t.cfg.in.Text(b)) })
	for _, sym := range syms {
		ns.Keys = append(ns.Keys, t.cfg.in.Text(sym))
		ns.Kids = append(ns.Kids, t.exportNode(nd.kids[sym]))
	}
	if nd.wild != nil {
		w := t.exportNode(nd.wild)
		ns.Wild = &w
	}
	for _, c := range nd.leaf {
		ns.Leaf = append(ns.Leaf, c.id)
	}
	return ns
}

// Rebuilds the tree from a state into a tree that has learned nothing
// The state is checked whole before the tree takes it
// A bad state leaves the tree empty
// The interner may keep texts of a bad state
func (t *Tree) Import(st State) error {
	if len(t.byID) > 0 || t.seq > 0 {
		return ErrNotEmpty
	}
	byID, err := t.importClusters(st.Clusters)
	if err != nil {
		return err
	}
	buckets, err := t.importBuckets(st.Buckets, byID)
	if err != nil {
		return err
	}
	live := 0
	for _, c := range byID {
		if c.alias == nil {
			live++
		}
		if c.salt > 0 {
			t.salted[c.base] = max(t.salted[c.base], c.salt)
		}
	}
	t.buckets, t.byID, t.seq, t.lines, t.live = buckets, byID, st.Seq, st.Lines, live
	return nil
}

// The clusters of a state with their aliases linked
// An alias must point to another cluster that is no alias itself
func (t *Tree) importClusters(states []ClusterState) (map[ID]*Cluster, error) {
	byID := make(map[ID]*Cluster, len(states))
	for _, cs := range states {
		if byID[cs.ID] != nil {
			return nil, fmt.Errorf("%w: cluster %s listed twice", ErrState, cs.ID)
		}
		tpl, err := t.template(cs.Shape)
		if err != nil {
			return nil, err
		}
		base := t.cfg.base(t.hash(tpl))
		if got := base.salted(cs.Salt); got != cs.ID {
			return nil, fmt.Errorf("%w: template %s hashes to %s", ErrState, cs.ID, got)
		}
		byID[cs.ID] = &Cluster{
			id: cs.ID, base: base, salt: cs.Salt, tpl: tpl, vars: tpl.variables(), text: t.render(tpl, t.cfg.opt.KindPlaceholders),
			count: cs.Count, first: cs.First, last: cs.Last, seq: cs.Seq, hit: cs.Hit, split: cs.Split,
		}
	}
	for _, cs := range states {
		if !cs.HasAlias {
			continue
		}
		c, x := byID[cs.ID], byID[cs.Alias]
		switch x {
		case nil:
			return nil, fmt.Errorf("%w: alias of %s to missing %s", ErrState, cs.ID, cs.Alias)
		case c:
			return nil, fmt.Errorf("%w: %s is an alias of itself", ErrState, cs.ID)
		}
		c.alias = x
		x.aliases = append(x.aliases, c)
	}
	for _, c := range byID {
		if c.alias != nil && c.alias.alias != nil {
			return nil, fmt.Errorf("%w: alias of %s to %s, itself an alias", ErrState, c.id, c.alias.id)
		}
	}
	return byID, nil
}

// The buckets of a state
// Every cluster must sit in the creation order of exactly one bucket of its token count
func (t *Tree) importBuckets(states []BucketState, byID map[ID]*Cluster) ([]*bucket, error) {
	var buckets []*bucket
	// a bucket holds a cluster once and only clusters of its token count
	// The orders never overlap
	placed := 0
	for _, bs := range states {
		b, err := t.importBucket(bs, byID)
		if err != nil {
			return nil, err
		}
		if bs.N < len(buckets) && buckets[bs.N] != nil {
			return nil, fmt.Errorf("%w: bucket %d listed twice", ErrState, bs.N)
		}
		placed += len(b.order)
		for len(buckets) <= bs.N {
			buckets = append(buckets, nil)
		}
		buckets[bs.N] = b
	}
	if placed != len(byID) {
		return nil, fmt.Errorf("%w: %d clusters in no bucket", ErrState, len(byID)-placed)
	}
	return buckets, nil
}

func (t *Tree) importBucket(bs BucketState, byID map[ID]*Cluster) (*bucket, error) {
	if bs.N < 0 || len(bs.Unstable) != bs.N || len(bs.Seen) != bs.N {
		return nil, fmt.Errorf("%w: bucket %d", ErrState, bs.N)
	}
	b := &bucket{
		n: bs.N, pos: slices.Clone(bs.Pos), unstable: slices.Clone(bs.Unstable),
		seen: make([]map[uint32]struct{}, bs.N), rebuilds: bs.Rebuilds,
		byID: map[ID]*Cluster{}, dirty: true,
	}
	for _, p := range b.pos {
		if p < 0 || p >= b.n {
			return nil, fmt.Errorf("%w: position %d in bucket %d", ErrState, p, bs.N)
		}
	}
	for p, words := range bs.Seen {
		if words == nil {
			continue
		}
		b.seen[p] = make(map[uint32]struct{}, len(words))
		for _, w := range words {
			b.seen[p][t.cfg.in.Intern([]byte(w))] = struct{}{}
		}
	}
	for _, id := range bs.Order {
		c := byID[id]
		if c == nil || len(c.tpl.Slots) != bs.N || b.byID[id] != nil {
			return nil, fmt.Errorf("%w: cluster %s in bucket %d", ErrState, id, bs.N)
		}
		b.order = append(b.order, c)
		b.index(c)
	}
	root, err := t.importNode(bs.Root, b.byID)
	if err != nil {
		return nil, err
	}
	b.root = root
	return b, nil
}

func (t *Tree) importNode(ns NodeState, byID map[ID]*Cluster) (*node, error) {
	nd := &node{}
	if len(ns.Keys) != len(ns.Kids) {
		return nil, fmt.Errorf("%w: %d keys for %d children", ErrState, len(ns.Keys), len(ns.Kids))
	}
	for i, k := range ns.Keys {
		kid, err := t.importNode(ns.Kids[i], byID)
		if err != nil {
			return nil, err
		}
		if nd.kids == nil {
			nd.kids = make(map[uint32]*node, len(ns.Keys))
		}
		nd.kids[t.cfg.in.Intern([]byte(k))] = kid
	}
	if ns.Wild != nil {
		w, err := t.importNode(*ns.Wild, byID)
		if err != nil {
			return nil, err
		}
		nd.wild = w
	}
	for _, id := range ns.Leaf {
		c := byID[id]
		if c == nil {
			return nil, fmt.Errorf("%w: leaf holds missing %s", ErrState, id)
		}
		nd.leaf = append(nd.leaf, c)
	}
	return nd, nil
}

// Replays one recorded event onto a tree restored from the state before it
// Events a replayed change causes again are skipped when they come
// The merges of a rebuild are such events
// The tree emits nothing while it replays
func (t *Tree) Apply(ev Event) error {
	emit := t.emit
	t.emit = nil
	defer func() { t.emit = emit }()
	switch ev.Kind {
	case Created:
		if ev.Shape == nil {
			return fmt.Errorf("%w: created %s without a template", ErrState, ev.ID)
		}
		tpl, err := t.template(*ev.Shape)
		if err != nil {
			return err
		}
		c, id, kind := t.addTemplate(tpl, ev.At, nil, ev.N)
		if kind != Created || id != ev.ID {
			return fmt.Errorf("%w: created %s, recorded %s", ErrDiverged, id, ev.ID)
		}
		t.count(c.Canonical(), ev.At, ev.N, nil, nil)
	case Changed:
		return t.applyChanged(ev)
	case Evicted:
		if c := t.byID[ev.ID]; c != nil {
			t.drop(c, nil)
		}
	case Matched:
		if c := t.byID[ev.ID]; c != nil {
			t.count(c.Canonical(), ev.At, ev.N, nil, nil)
		}
	}
	return nil
}

func (t *Tree) applyChanged(ev Event) error {
	c := t.byID[ev.From]
	if c == nil {
		// a rebuild the replay already ran merged it
		return nil
	}
	if ev.Alias {
		return t.Alias(ev.From, ev.To, ev.At)
	}
	if ev.Shape == nil {
		return fmt.Errorf("%w: changed %s without a template", ErrState, ev.From)
	}
	tpl, err := t.template(*ev.Shape)
	if err != nil {
		return err
	}
	c = t.setTemplate(c, tpl, ev.At, ev.N, nil)
	if c.Canonical().id != ev.To {
		return fmt.Errorf("%w: changed %s to %s, recorded %s", ErrDiverged, ev.From, c.id, ev.To)
	}
	t.count(c.Canonical(), ev.At, ev.N, nil, nil)
	return nil
}
