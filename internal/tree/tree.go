// Package tree is the Drain parse tree of templates per token count
package tree

import (
	"cmp"
	"slices"
	"time"

	"github.com/jeon-jihyeon/moldrain/internal/token"
)

// What the tree and every view of it share
type config struct {
	opt Options
	in  *token.Interner
	// replaces the hash before the salt
	// Tests set it to force collisions
	mix func(fnv) fnv
}

// Whether an interned constant holds a decimal digit
func (cfg *config) digit(sym uint32) bool {
	text := cfg.in.Text(sym)
	for i := 0; i < len(text); i++ {
		if text[i] >= '0' && text[i] <= '9' {
			return true
		}
	}
	return false
}

// The hash a template's salts extend
func (cfg *config) base(h fnv) fnv {
	if cfg.mix != nil {
		return cfg.mix(h)
	}
	return h
}

// One learned template with its counts
type Cluster struct {
	id ID
	// the hash the salt extends
	base fnv
	salt byte
	tpl  Template
	// the variable slots of tpl
	// They break ties of similarity on every line
	vars  int
	text  string
	count uint64
	first time.Time
	last  time.Time
	// creation order
	seq uint64
	// the line number of the last line it took
	hit uint64
	// the template a resolver merged this one into
	alias *Cluster
	// the templates aliased to this one in the live tree
	aliases []*Cluster
	// where the cluster went when it merged away
	into *Cluster
	// recent values per slot under KeepValues
	params [][]string
	// values per variable slot under Respecialize
	track []slotTrack
	// a Respecialize split already pinned its values
	// The cluster never splits again
	split bool
}

func (c *Cluster) ID() ID             { return c.id }
func (c *Cluster) Text() string       { return c.text }
func (c *Cluster) Count() uint64      { return c.count }
func (c *Cluster) First() time.Time   { return c.first }
func (c *Cluster) Last() time.Time    { return c.last }
func (c *Cluster) Hit() uint64        { return c.hit }
func (c *Cluster) Template() Template { return c.tpl }
func (c *Cluster) Aliased() bool      { return c.alias != nil }

// A copy of the recent values per slot
// nil for a constant slot
func (c *Cluster) Params() [][]string {
	if c.params == nil {
		return nil
	}
	out := make([][]string, len(c.params))
	for i, p := range c.params {
		out[i] = slices.Clone(p)
	}
	return out
}

// The parameters of a line that lined up with c with one view per variable slot
// One allocation of the exact size and none for a template without variables
func (c *Cluster) LineParams(line []byte, toks []token.Token) [][]byte {
	if c.vars == 0 {
		return nil
	}
	return c.tpl.AppendParams(make([][]byte, 0, c.vars), line, toks)
}

// The template a line that matched c is reported as
func (c *Cluster) Canonical() *Cluster {
	if c.alias != nil {
		return c.alias
	}
	return c
}

// The cluster c ended in after merges
func (c *Cluster) final() *Cluster {
	for c.into != nil {
		c = c.into
	}
	return c
}

type node struct {
	kids map[uint32]*node
	wild *node
	leaf []*Cluster
	// the constant slots of leaf sorted by key
	// A view builds it for a leaf of postMin templates or more
	// nil in the live tree whose leaves change
	post []posting
}

// Templates a leaf needs before a view indexes its constants
const postMin = 64

// One constant slot of a leaf template
type posting struct {
	// position and symbol of the constant
	key uint64
	// index of the template in the leaf
	at int32
}

func postKey(pos int, sym uint32) uint64 { return uint64(pos)<<32 | uint64(sym) }

// The postings of one key
func (nd *node) postings(key uint64) []posting {
	lo, _ := slices.BinarySearchFunc(nd.post, key, cmpPosting)
	hi := lo
	for hi < len(nd.post) && nd.post[hi].key == key {
		hi++
	}
	return nd.post[lo:hi]
}

func cmpPosting(p posting, key uint64) int { return cmp.Compare(p.key, key) }

// The templates of one token count
type bucket struct {
	n   int
	pos []int
	// distinct constants per position
	// nil once the position is unstable
	seen     []map[uint32]struct{}
	unstable []bool
	rebuilds int
	root     *node
	// every cluster of the bucket in creation order
	order []*Cluster
	byID  map[ID]*Cluster
	// the highest salt of a hash among the bucket's templates
	// Only hashes past salt 0 are kept
	salted map[fnv]byte
	dirty  bool
}

// Puts c in the bucket's id map
func (b *bucket) index(c *Cluster) {
	b.byID[c.id] = c
	if c.salt == 0 {
		return
	}
	if b.salted == nil {
		b.salted = map[fnv]byte{}
	}
	b.salted[c.base] = max(b.salted[c.base], c.salt)
}

// The live tree that one goroutine at a time changes
type Tree struct {
	cfg     config
	buckets []*bucket
	byID    map[ID]*Cluster
	// the highest salt of a hash
	// Only hashes past salt 0 are kept
	salted map[fnv]byte
	gaps   map[string]string
	seq    uint64
	lines  uint64
	emit   func(Event)
	live   int
}

// A tree that has learned nothing
// ErrOptions for an option out of range
func New(opt Options, in *token.Interner) (*Tree, error) {
	opt = opt.withDefaults()
	if err := opt.validate(); err != nil {
		return nil, err
	}
	return &Tree{
		cfg:    config{opt: opt, in: in},
		byID:   map[ID]*Cluster{},
		salted: map[fnv]byte{},
		gaps:   map[string]string{},
	}, nil
}

// Every event goes to fn in order on the goroutine that changes the tree
func (t *Tree) OnEvent(fn func(Event)) { t.emit = fn }

func (t *Tree) send(ev Event) {
	if t.emit != nil {
		t.emit(ev)
	}
}

// Read access to buckets shared by the live tree and its views
type forest interface {
	at(n int) *bucket
}

func (t *Tree) at(n int) *bucket {
	if n < 0 || n >= len(t.buckets) {
		return nil
	}
	return t.buckets[n]
}

func (t *Tree) bucketFor(n int) *bucket {
	for len(t.buckets) <= n {
		t.buckets = append(t.buckets, nil)
	}
	if b := t.buckets[n]; b != nil {
		return b
	}
	b := &bucket{
		n:        n,
		seen:     make([]map[uint32]struct{}, n),
		unstable: make([]bool, n),
		root:     &node{},
		byID:     map[ID]*Cluster{},
	}
	b.pos = b.choose(t.cfg.opt.Depth)
	t.buckets[n] = b
	return b
}

// The cluster of an id
// nil when the tree does not hold it
func (t *Tree) Get(id ID) *Cluster { return t.byID[id] }

// Canonical clusters that are no alias of another
func (t *Tree) Len() int { return t.live }

// Every canonical cluster of the live tree by token count and then creation
func (t *Tree) Clusters() []*Cluster { return canonical(t.buckets) }

// The clusters of buckets that are no alias of another
// By token count and then creation
func canonical(buckets []*bucket) []*Cluster {
	var out []*Cluster
	for _, b := range buckets {
		if b == nil {
			continue
		}
		for _, c := range b.order {
			if c.alias == nil {
				out = append(out, c)
			}
		}
	}
	return out
}

// Sets Sym on the constants of a line without interning anything
func lookup(in *token.Interner, line []byte, toks []token.Token) {
	for i := range toks {
		if toks[i].Kind == token.Const {
			toks[i].Sym, _ = in.Lookup(toks[i].Bytes(line))
		} else {
			toks[i].Sym = 0
		}
	}
}

// Learns one line and returns the cluster it ended in and what happened to it
// The cluster may be an alias
// Its canonical cluster is the template to report
// toks come from a tokenizer over line and get their Sym set here
func (t *Tree) Train(line []byte, toks []token.Token, at time.Time) (*Cluster, EventKind) {
	t.lines++
	lookup(t.cfg.in, line, toks)
	b := t.bucketFor(len(toks))
	f := t.cfg.find(t, b, line, toks)
	switch {
	case f.c == nil:
		c, _, kind := t.addTemplate(t.lineTemplate(line, toks), at, line, 1)
		x := c.Canonical()
		t.count(x, at, 1, line, toks)
		if kind == Matched {
			t.send(Event{Kind: Matched, ID: x.id, N: 1, At: at, Line: line})
		}
		return c, kind
	case f.c.alias != nil:
		x := f.c.alias
		t.count(x, at, 1, line, nil)
		t.send(Event{Kind: Matched, ID: x.id, N: 1, At: at, Line: line})
		return f.c, Matched
	case f.span != nil && !sameTemplate(*f.span, f.c.tpl):
		c := t.setTemplate(f.c, *f.span, at, 1, line)
		t.count(c.Canonical(), at, 1, line, nil)
		return c, Changed
	}
	c := f.c
	if !f.exact && f.span == nil {
		if tpl, changed := mergeLine(c.tpl, toks); changed {
			c = t.setTemplate(c, tpl, at, 1, line)
			t.count(c.Canonical(), at, 1, line, toks)
			return c, Changed
		}
	}
	t.count(c, at, 1, line, toks)
	t.send(Event{Kind: Matched, ID: c.id, N: 1, At: at, Line: line})
	if t.cfg.opt.Respecialize {
		t.observe(c, line, toks)
	}
	return c, Matched
}

func (t *Tree) count(c *Cluster, at time.Time, n uint64, line []byte, toks []token.Token) {
	c.count += n
	if at.After(c.last) {
		c.last = at
	}
	if c.first.IsZero() || at.Before(c.first) {
		c.first = at
	}
	c.hit = t.lines
	t.bucketOf(c).dirty = true
	if k := t.cfg.opt.KeepValues; k > 0 && toks != nil && len(toks) == len(c.tpl.Slots) {
		keep(c, line, toks, k)
	}
}

func keep(c *Cluster, line []byte, toks []token.Token, k int) {
	if c.params == nil {
		c.params = make([][]string, len(c.tpl.Slots))
	}
	for i, s := range c.tpl.Slots {
		if !s.variable() {
			continue
		}
		vals := c.params[i]
		if len(vals) == k {
			vals = append(vals[:0:0], vals[1:]...)
		}
		c.params[i] = append(vals, string(toks[i].Bytes(line)))
	}
}

func (t *Tree) bucketOf(c *Cluster) *bucket { return t.buckets[len(c.tpl.Slots)] }

// How a line matched
type found struct {
	c *Cluster
	// the line equals the template
	// It was found by its hash after the leaf had nothing similar enough
	exact bool
	// the template c becomes when the line joins it through a span
	span *Template
}

// Finds the template for a line without changing anything
// Train and Match both use it
func (cfg *config) find(f forest, b *bucket, line []byte, toks []token.Token) found {
	if b != nil {
		if leaf := b.leaf(toks); leaf != nil {
			if c := cfg.best(leaf, toks); c != nil {
				return found{c: c}
			}
		}
		if c := cfg.exact(b, line, toks); c != nil {
			return found{c: c, exact: true}
		}
	}
	if cfg.opt.Spans {
		if c, tpl := cfg.spanFind(f, toks); c != nil {
			return found{c: c, span: &tpl}
		}
	}
	return found{}
}

// The leaf a line reaches
// At each branch position it takes the child of its token or else the wildcard child
func (b *bucket) leaf(toks []token.Token) *node {
	nd := b.root
	for _, p := range b.pos {
		tk := toks[p]
		var next *node
		if tk.Kind == token.Const {
			next = nd.kids[tk.Sym]
		}
		if next == nil {
			next = nd.wild
		}
		if next == nil {
			return nil
		}
		nd = next
	}
	return nd
}

// The cluster whose template equals the template the line would start
// Every salt up to the highest the bucket used is looked at
// A removed template leaves a hole
func (cfg *config) exact(b *bucket, line []byte, toks []token.Token) *Cluster {
	h := cfg.base(lineHash(line, toks))
	for salt := 0; salt <= int(b.salted[h]); salt++ {
		if c := b.byID[h.salted(byte(salt))]; c != nil && sameAsLine(c.tpl, line, toks) {
			return c
		}
	}
	return nil
}

// Similarity of a line to a template
// ok is false when a pinned slot differs
func similarity(tpl Template, toks []token.Token) (sim float64, ok bool) {
	if len(toks) == 0 {
		return 1, true
	}
	score := 0
	for i, s := range tpl.Slots {
		if s.variable() {
			continue
		}
		tk := toks[i]
		if tk.Kind == token.Const && tk.Sym == s.Sym {
			score++
		} else if s.Pinned {
			return 0, false
		}
	}
	return float64(score) / float64(len(toks)), true
}

// The most similar template of a leaf at or above SimTh
// With postings only the templates that share a constant with the line are scored
// The rest score 0 and stay below SimTh which is above 0 after its default
// The answer is the same as a full scan since wins orders every pair of templates
// The two loops repeat one comparison inline since a call per template slows a leaf of thousands
// A template met twice through two postings changes nothing
func (cfg *config) best(nd *node, toks []token.Token) *Cluster {
	if nd.posted(toks) {
		return cfg.bestPosted(nd, toks)
	}
	var best *Cluster
	bestSim := -1.0
	for _, c := range nd.leaf {
		sim, ok := similarity(c.tpl, toks)
		if ok && sim >= cfg.opt.SimTh && (sim > bestSim || (sim == bestSim && c.wins(best))) {
			best, bestSim = c, sim
		}
	}
	return best
}

// The most similar template among those sharing a constant with the line
func (cfg *config) bestPosted(nd *node, toks []token.Token) *Cluster {
	var best *Cluster
	bestSim := -1.0
	for i, tk := range toks {
		if tk.Kind != token.Const {
			continue
		}
		for _, ps := range nd.postings(postKey(i, tk.Sym)) {
			c := nd.leaf[ps.at]
			sim, ok := similarity(c.tpl, toks)
			if ok && sim >= cfg.opt.SimTh && (sim > bestSim || (sim == bestSim && c.wins(best))) {
				best, bestSim = c, sim
			}
		}
	}
	return best
}

// Whether the postings of a line name fewer templates than the leaf holds
// Only then is the posted scan the shorter one
func (nd *node) posted(toks []token.Token) bool {
	if nd.post == nil || len(toks) == 0 {
		return false
	}
	n := 0
	for i, tk := range toks {
		if tk.Kind == token.Const {
			n += len(nd.postings(postKey(i, tk.Sym)))
		}
	}
	return n < len(nd.leaf)
}

// Whether c wins a tie of similarity against o
// 1. fewer variable slots
// 2. the older template
func (c *Cluster) wins(o *Cluster) bool {
	return c.vars < o.vars || (c.vars == o.vars && c.seq < o.seq)
}

// The template after a line joins it
// A constant that differs becomes a variable of the token's kind
// Var when kinds differ
func mergeLine(tpl Template, toks []token.Token) (Template, bool) {
	var slots []Slot
	for i, s := range tpl.Slots {
		ns := mergeSlot(s, toks[i])
		if ns == s {
			continue
		}
		if slots == nil {
			slots = append([]Slot(nil), tpl.Slots...)
		}
		slots[i] = ns
	}
	if slots == nil {
		return tpl, false
	}
	return Template{Slots: slots, Gaps: tpl.Gaps}, true
}

func mergeSlot(s Slot, tk token.Token) Slot {
	switch {
	case s.Span:
		return s
	case s.variable():
		if s.Kind != tk.Kind && s.Kind != token.Var {
			return Slot{Kind: token.Var}
		}
		return s
	case tk.Kind == token.Const && tk.Sym == s.Sym:
		return s
	case tk.Kind == token.Const:
		return Slot{Kind: token.Var}
	}
	return Slot{Kind: tk.Kind}
}

// Starts a template or returns the one with the same text
// Returns the cluster the template ended in after any rebuild and the id it was created with
// n lines are counted by the caller
func (t *Tree) addTemplate(tpl Template, at time.Time, line []byte, n uint64) (*Cluster, ID, EventKind) {
	b := t.bucketFor(len(tpl.Slots))
	base, salt, same := t.assign(tpl)
	if same != nil {
		return same.final(), same.id, Matched
	}
	t.seq++
	id := base.salted(salt)
	c := &Cluster{id: id, base: base, salt: salt, tpl: tpl, vars: tpl.variables(), first: at, last: at, seq: t.seq, hit: t.lines}
	c.text = t.render(tpl, t.cfg.opt.KindPlaceholders)
	t.byID[id] = c
	b.index(c)
	t.live++
	b.insert(c, &t.cfg)
	b.order = append(b.order, c)
	b.dirty = true
	sh := t.shape(tpl)
	t.send(Event{Kind: Created, ID: id, Shape: &sh, N: n, At: at, Line: line})
	if t.countPositions(b, c) {
		t.rebuild(b)
	}
	return c.final(), id, Created
}

// The id of a template: its hash with the lowest salt no other template took
// same is the cluster that already holds this exact template
// Every salt up to the highest used is looked at
// A removed template leaves a hole
func (t *Tree) assign(tpl Template) (base fnv, salt byte, same *Cluster) {
	base = t.cfg.base(t.hash(tpl))
	top := t.salted[base]
	free := -1
	for s := 0; s < 256 && (s <= int(top) || free < 0); s++ {
		o := t.byID[base.salted(byte(s))]
		switch {
		case o == nil:
			if free < 0 {
				free = s
			}
		case sameTemplate(o.tpl, tpl):
			return base, o.salt, o
		}
	}
	if free < 0 {
		panic("tree: 256 templates share one hash")
	}
	if free > 0 {
		t.salted[base] = max(top, byte(free))
	}
	return base, byte(free), nil
}

// Follows the insert path of a template
// Creates children while the node has room
func (b *bucket) insert(c *Cluster, cfg *config) {
	nd := b.path(c.tpl, cfg)
	nd.leaf = append(nd.leaf, c)
}

// A constant with a digit takes the wildcard child as Drain does
// Such words are mostly values
// A line's token with a digit then finds no child of its own and takes the wildcard child too
func (b *bucket) path(tpl Template, cfg *config) *node {
	maxChildren := cfg.opt.MaxChildren
	nd := b.root
	for _, p := range b.pos {
		s := tpl.Slots[p]
		if s.variable() || cfg.digit(s.Sym) {
			if nd.wild == nil {
				nd.wild = &node{}
			}
			nd = nd.wild
			continue
		}
		next := nd.kids[s.Sym]
		if next == nil {
			children := len(nd.kids)
			if nd.wild != nil {
				children++
			}
			if children < maxChildren-1 || (children < maxChildren && nd.wild != nil) {
				if nd.kids == nil {
					nd.kids = map[uint32]*node{}
				}
				next = &node{}
				nd.kids[s.Sym] = next
			} else {
				if nd.wild == nil {
					nd.wild = &node{}
				}
				next = nd.wild
			}
		}
		nd = next
	}
	return nd
}

// Replaces the template of c and gives it the id of the new template
// When another cluster already holds that template c merges into it
// The other is returned then
// n is the lines the change carries for the event
func (t *Tree) setTemplate(c *Cluster, tpl Template, at time.Time, n uint64, line []byte) *Cluster {
	old := c.id
	from := t.bucketOf(c)
	delete(t.byID, old)
	delete(from.byID, old)
	moved := len(tpl.Slots) != from.n
	if moved {
		from.remove(c)
		from.dirty = true
	}
	c.tpl, c.vars = tpl, tpl.variables()
	c.track = nil
	to := t.bucketFor(len(tpl.Slots))
	base, salt, same := t.assign(tpl)
	if same != nil {
		// a moved c already left every leaf and order of its old bucket
		// mergeInto finds nothing of it in the new one
		t.mergeInto(c, same)
		x := same.Canonical()
		sh := t.shape(x.tpl)
		t.send(Event{Kind: Changed, From: old, To: x.id, Shape: &sh, N: n, At: at, Line: line})
		return same
	}
	c.id, c.base, c.salt = base.salted(salt), base, salt
	c.text = t.render(tpl, t.cfg.opt.KindPlaceholders)
	t.byID[c.id] = c
	to.index(c)
	if moved {
		to.order = append(to.order, c)
		to.insert(c, &t.cfg)
	}
	to.dirty = true
	t.touchAliases(c)
	if c.id != old {
		sh := t.shape(tpl)
		t.send(Event{Kind: Changed, From: old, To: c.id, Shape: &sh, N: n, At: at, Line: line})
	}
	return c
}

// Marks the buckets of x's aliases dirty
// A view freezes an alias target in another bucket
func (t *Tree) touchAliases(x *Cluster) {
	for _, a := range x.aliases {
		t.bucketOf(a).dirty = true
	}
}

// Moves c into o
// 1. its place in the leaves to o
// 2. its counts and aliases to the template o reports as
// c must already be out of the id maps
func (t *Tree) mergeInto(c, o *Cluster) {
	x := o.Canonical()
	x.count += c.count
	if c.first.Before(x.first) {
		x.first = c.first
	}
	if c.last.After(x.last) {
		x.last = c.last
	}
	x.hit = max(x.hit, c.hit)
	b := t.bucketOf(c)
	b.replace(c, o)
	b.dropOrder(c)
	for _, a := range c.aliases {
		a.alias = x
		x.aliases = append(x.aliases, a)
	}
	c.aliases = nil
	c.into = o
	if c.alias == nil {
		t.live--
	}
	b.dirty = true
	t.bucketOf(x).dirty = true
	t.touchAliases(x)
}

// Puts o in place of c in every leaf once per leaf
func (b *bucket) replace(c, o *Cluster) {
	b.walk(func(nd *node) {
		at, has := -1, false
		for i, x := range nd.leaf {
			if x == c {
				at = i
			}
			if x == o {
				has = true
			}
		}
		switch {
		case at < 0:
		case has:
			nd.leaf = append(nd.leaf[:at:at], nd.leaf[at+1:]...)
		default:
			leaf := append([]*Cluster(nil), nd.leaf...)
			leaf[at] = o
			nd.leaf = leaf
		}
	})
}

// Takes c out of every leaf and the creation order
func (b *bucket) remove(c *Cluster) {
	b.walk(func(nd *node) {
		for i, x := range nd.leaf {
			if x == c {
				nd.leaf = append(nd.leaf[:i:i], nd.leaf[i+1:]...)
				return
			}
		}
	})
	b.dropOrder(c)
}

func (b *bucket) dropOrder(c *Cluster) {
	for i, x := range b.order {
		if x == c {
			b.order = append(b.order[:i:i], b.order[i+1:]...)
			return
		}
	}
}

func (b *bucket) walk(fn func(*node)) {
	var visit func(nd *node)
	visit = func(nd *node) {
		fn(nd)
		for _, k := range nd.kids {
			visit(k)
		}
		if nd.wild != nil {
			visit(nd.wild)
		}
	}
	visit(b.root)
}

// Drops a template and the templates aliased to it
// Emits one Evicted for each
func (t *Tree) Remove(id ID, at time.Time) error {
	c := t.byID[id]
	if c == nil {
		return ErrNotFound
	}
	for _, gone := range t.drop(c, nil) {
		t.send(Event{Kind: Evicted, ID: gone, At: at})
	}
	return nil
}

// Drops c and its aliases and appends their ids to dst
// c comes first
func (t *Tree) drop(c *Cluster, dst []ID) []ID {
	dst = append(dst, c.id)
	for _, a := range c.aliases {
		dst = t.drop(a, dst)
	}
	b := t.bucketOf(c)
	b.remove(c)
	delete(t.byID, c.id)
	delete(b.byID, c.id)
	if c.alias == nil {
		t.live--
	} else {
		x := c.alias
		for i, a := range x.aliases {
			if a == c {
				x.aliases = append(x.aliases[:i:i], x.aliases[i+1:]...)
				break
			}
		}
	}
	b.dirty = true
	return dst
}

// Makes from another form of an older template to: lines that match from count as to
// from keeps its place in the tree and its template stays frozen
func (t *Tree) Alias(from, to ID, at time.Time) error {
	f, x := t.byID[from], t.byID[to]
	if f == nil || x == nil {
		return ErrNotFound
	}
	if f.alias != nil {
		return ErrAliased
	}
	x = x.Canonical()
	if f == x || x.seq > f.seq {
		return ErrNotOlder
	}
	f.alias = x
	x.aliases = append(x.aliases, f)
	for _, a := range f.aliases {
		a.alias = x
		x.aliases = append(x.aliases, a)
	}
	f.aliases = nil
	x.count += f.count
	f.count = 0
	if f.first.Before(x.first) {
		x.first = f.first
	}
	if f.last.After(x.last) {
		x.last = f.last
	}
	x.hit = max(x.hit, f.hit)
	t.live--
	t.bucketOf(f).dirty = true
	t.bucketOf(x).dirty = true
	t.touchAliases(x)
	t.send(Event{Kind: Changed, From: from, To: x.id, Alias: true, At: at})
	return nil
}
