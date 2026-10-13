// Package moldrain learns log templates from a stream the way Drain does
package moldrain

import (
	"cmp"
	"iter"
	"slices"
	"sync"
	"sync/atomic"
	"time"

	"github.com/jeon-jihyeon/moldrain/internal/token"
	"github.com/jeon-jihyeon/moldrain/internal/tree"
)

// The tree and token types callers see through the miner
// They are aliases so a snapshot or an event crosses the boundary without a copy
// Their fields are documented in the internal packages they come from
type (
	// 16 hex digits of FNV-1a 64 over the template rendered with kinds
	// The same template has the same id in every process and after every restart
	ID = tree.ID
	// One change to the template set in the order it happened
	Event = tree.Event
	// What happened to a template
	EventKind = tree.EventKind
	// A template in a form that does not depend on the interner of one process
	// Parts and the text between them with one gap more than parts
	Shape = tree.Shape
	// One position of a Shape: a constant text or a variable of a kind
	Part = tree.Part
	// The whole tree in a form a store can keep
	// Snapshot gives it and Restore takes it back
	// Its fields are the tree as it is and change with it
	// A store keeps it in a format of its own with a version
	State = tree.State
	// The branch positions and the nodes of one token count
	BucketState = tree.BucketState
	// One node of a bucket with its children and its leaf
	NodeState = tree.NodeState
	// One template with its counts and the template it is an alias of
	ClusterState = tree.ClusterState
	// What a token is as the typed scanners decide
	Kind = token.Kind
	// A caller's scanner over the bytes of one token
	// It runs after the built in scanners and claims the token when ok
	Mask = token.Mask
)

const (
	Created = tree.Created
	Matched = tree.Matched
	Changed = tree.Changed
	Evicted = tree.Evicted
)

const (
	Const = token.Const // a word that may be part of a template
	Num   = token.Num   // a number with an optional unit
	Hex   = token.Hex   // 0x prefixed or 8 or more mixed hex digits
	IP    = token.IP    // IPv4 with an optional port or IPv6
	UUID  = token.UUID  // 8-4-4-4-12 hex
	Time  = token.Time  // RFC 3339, a clock time or an epoch with EpochTokens
	Var   = token.Var   // variable by position
)

func ParseID(s string) (ID, error) { return tree.ParseID(s) }

// The unit suffixes a number may carry when Options leaves Units nil
func DefaultUnits() []string { return token.DefaultUnits() }

// What Train or Match found for one line
type Result struct {
	ID       ID
	Template string
	// The alias of ID the line matched
	// 0 when it matched ID itself
	// Params follow the slots of this alias
	Alias ID
	// Views into the line with one per variable slot
	Params [][]byte
	Event  EventKind
}

// One template as the published view holds it
type Cluster struct {
	ID       ID
	Template string
	Count    uint64
	First    time.Time
	Last     time.Time
	// Recent values per slot when KeepValues is set
	Params [][]string
}

type Stats struct {
	Lines     uint64
	Created   uint64
	Changed   uint64
	Evicted   uint64
	Truncated uint64
	// Templates the tree holds without their aliases
	Clusters int
	// Time since the oldest change Match cannot see yet
	// 0 when the view is current
	PublishLag time.Duration
}

// Learns templates from lines on one writer at a time and matches lines on any number of goroutines
type Miner struct {
	opt  Options
	tz   token.Tokenizer
	view atomic.Pointer[tree.View]
	pool sync.Pool

	// mu serializes writers
	// It guards the fields from tree to pub
	// The counters after them are atomic and the counts lock themselves
	mu      sync.Mutex
	tree    *tree.Tree
	toks    []token.Token
	subs    []func(Event)
	recency recency
	counts  *counts
	pub     publisher

	lines     atomic.Uint64
	created   atomic.Uint64
	changed   atomic.Uint64
	evicted   atomic.Uint64
	truncated atomic.Uint64
}

// A miner that has learned nothing
func New(opt Options) (*Miner, error) {
	opt = opt.withDefaults()
	if err := opt.validate(); err != nil {
		return nil, err
	}
	tr, err := tree.New(opt.tree(), token.NewInterner())
	if err != nil {
		return nil, err
	}
	m := &Miner{
		opt:    opt,
		tz:     opt.tokenizer(),
		tree:   tr,
		counts: newCounts(opt.Bucket, opt.KeepBuckets),
	}
	m.pool.New = func() any {
		buf := make([]token.Token, 0, 64)
		return &buf
	}
	m.tree.OnEvent(m.onEvent)
	m.view.Store(m.tree.Publish(nil))
	return m, nil
}

// The options with every default filled in
func (m *Miner) Options() Options { return m.opt }

// Hash of the options that change templates
func (m *Miner) Fingerprint() uint64 { return m.opt.Hash() }

func (m *Miner) cut(line []byte) []byte {
	if len(line) > m.opt.MaxLineBytes {
		return line[:m.opt.MaxLineBytes]
	}
	return line
}

// Learns one line seen at a time
// Safe for many goroutines that take turns
// It never waits on a store or a resolver
func (m *Miner) Train(line []byte, at time.Time) Result {
	if len(line) > m.opt.MaxLineBytes {
		m.truncated.Add(1)
		line = line[:m.opt.MaxLineBytes]
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.toks = m.tz.Tokens(line, m.toks[:0])
	c, kind := m.tree.Train(line, m.toks, at)
	x := c.Canonical()
	m.counts.add(x.ID(), at, 1)
	m.lines.Add(1)
	res := Result{
		ID:       x.ID(),
		Template: x.Text(),
		Params:   c.LineParams(line, m.toks),
		Event:    kind,
	}
	if c != x {
		res.Alias = c.ID()
	}
	// a Respecialize split adds templates on a Matched line too
	m.evictLocked(x.ID(), at)
	m.changedLocked()
	return res
}

// Looks a line up in the published view without changing anything as the inference mode of Drain3 does
// ok is false when no template is similar enough
// Takes no lock so any number of goroutines match while one trains
func (m *Miner) Match(line []byte) (Result, bool) {
	line = m.cut(line)
	buf := m.pool.Get().(*[]token.Token)
	toks := m.tz.Tokens(line, (*buf)[:0])
	c, matched, ok := m.view.Load().Match(line, toks)
	var res Result
	if ok {
		res = Result{
			ID:       c.ID(),
			Template: c.Text(),
			Params:   matched.LineParams(line, toks),
			Event:    Matched,
		}
		if matched != c {
			res.Alias = matched.ID()
		}
	}
	*buf = toks
	m.pool.Put(buf)
	return res, ok
}

// Called on the training goroutine for every tree event before subscribers see it
func (m *Miner) onEvent(ev Event) {
	switch ev.Kind {
	case Created:
		m.created.Add(1)
		m.track(ev.ID)
	case Changed:
		m.changed.Add(1)
		m.counts.move(ev.From, ev.To)
		if !ev.Alias {
			m.track(ev.To)
		}
	case Evicted:
		m.evicted.Add(1)
	}
	for _, fn := range m.subs {
		fn(ev)
	}
}

// Calls fn on the training goroutine for every event in order
// fn must return quickly and must not call the miner's writing methods
func (m *Miner) Subscribe(fn func(Event)) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.subs = append(m.subs, fn)
}

// The published view's clusters by count descending and then by id
func (m *Miner) Clusters() []Cluster {
	cs := m.view.Load().Clusters()
	out := make([]Cluster, len(cs))
	for i, c := range cs {
		out[i] = Cluster{ID: c.ID(), Template: c.Text(), Count: c.Count(), First: c.First(), Last: c.Last(), Params: c.Params()}
	}
	slices.SortFunc(out, func(a, b Cluster) int {
		if a.Count != b.Count {
			return cmp.Compare(b.Count, a.Count)
		}
		return cmp.Compare(a.ID, b.ID)
	})
	return out
}

// Lines trained per template in the count buckets that overlap [from, to)
func (m *Miner) Counts(from, to time.Time) []Count { return m.counts.window(from, to) }

func (m *Miner) Stats() Stats {
	m.mu.Lock()
	n := m.tree.Len()
	lag := m.pub.lag()
	m.mu.Unlock()
	return Stats{
		Lines: m.lines.Load(), Created: m.created.Load(), Changed: m.changed.Load(),
		Evicted: m.evicted.Load(), Truncated: m.truncated.Load(), Clusters: n, PublishLag: lag,
	}
}

// A template near a line
type Neighbor struct {
	ID       ID
	Template string
	Sim      float64
	Params   [][]string
}

// The k templates of the line's token count in the published view most similar to it
// Only templates created before the template before count
// A before the view does not hold yet is newer than every template in it
func (m *Miner) Nearest(line []byte, k int, before ID) []Neighbor {
	line = m.cut(line)
	buf := m.pool.Get().(*[]token.Token)
	toks := m.tz.Tokens(line, (*buf)[:0])
	near := m.view.Load().Nearest(line, toks, k, before)
	*buf = toks
	m.pool.Put(buf)
	out := make([]Neighbor, len(near))
	for i, n := range near {
		out[i] = Neighbor{ID: n.Cluster.ID(), Template: n.Cluster.Text(), Sim: n.Sim, Params: n.Cluster.Params()}
	}
	return out
}

// Makes the template from another form of the older template to
// Lines that match from count as to from now on
// A Changed with Alias set tells subscribers
func (m *Miner) Alias(from, to ID) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if err := m.tree.Alias(from, to, time.Now()); err != nil {
		return err
	}
	m.changedLocked()
	return nil
}

// Runs fn with a deep copy of the state while training waits
// The caller can cut its own log at the same point
func (m *Miner) Snapshot(fn func(State)) {
	m.mu.Lock()
	defer m.mu.Unlock()
	fn(m.tree.Export())
}

// Rebuilds a miner that has not trained from a state and the changes recorded after it
// st may be nil when there is no snapshot
// Subscribers see none of the replayed events
// The replay runs on a tree of its own that replaces the miner's only once it succeeds
// A failed Restore therefore leaves the miner empty to be tried again
// Templates past MaxClusters are evicted after it as of now
func (m *Miner) Restore(st *State, changes iter.Seq2[Event, error]) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.lines.Load() > 0 || m.tree.Len() > 0 {
		return ErrNotEmpty
	}
	// the options passed New so the tree takes them too
	tr, _ := tree.New(m.opt.tree(), token.NewInterner())
	if st != nil {
		if err := tr.Import(*st); err != nil {
			return err
		}
	}
	if changes != nil {
		for ev, err := range changes {
			if err != nil {
				return err
			}
			if err := tr.Apply(ev); err != nil {
				return err
			}
		}
	}
	tr.OnEvent(m.onEvent)
	m.tree = tr
	m.recency = m.recency[:0]
	for _, c := range tr.Clusters() {
		m.track(c.ID())
	}
	m.evictLocked(0, time.Now())
	m.view.Store(tr.Publish(nil))
	m.pub.lines, m.pub.since = 0, time.Time{}
	return nil
}
