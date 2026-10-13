package assay

import (
	"cmp"
	"math"
	"slices"
	"sync"
	"sync/atomic"
	"time"

	"github.com/jeon-jihyeon/moldrain"
)

// One op of one group over one window
type Record struct {
	Start   time.Time `json:"start"`
	End     time.Time `json:"end"`
	Profile string    `json:"profile"`
	Group   string    `json:"group,omitempty"`
	// The lines of every group past MaxGroups
	// Group is empty then
	Other bool    `json:"other,omitempty"`
	Op    Op      `json:"op"`
	Value float64 `json:"value"`
	// The profile's unit on ops over values
	// Empty on count and distinct
	Unit Unit `json:"unit,omitempty"`
	// Lines in the group
	// Skipped among them had no value assay could read
	Lines   uint64 `json:"lines"`
	Skipped uint64 `json:"skipped"`
	// Ids in hex of the templates whose lines the group took
	Templates []string `json:"templates"`
	// A distinct count past MaxDistinct estimated by HyperLogLog
	Approx bool `json:"approx"`
}

// Aggregates of one group in one window
type acc struct {
	lines, skipped, n uint64
	sum, lo, hi       float64
	sk                *sketch
	dist              *distinct
	tpls              []moldrain.ID
}

func newAcc() *acc { return &acc{lo: math.Inf(1), hi: math.Inf(-1)} }

type window struct {
	key    int64
	groups map[string]*acc
	// the lines of every group past MaxGroups
	other *acc
}

// Records an outlet holds before a profile that hands it more waits
const outletRecords = 4096

// Hands records to the sinks from a goroutine of its own
// A profile queues its records under its own lock and moves them here after it lets go
// A slow sink holds up the lines of a profile only once the outlet is full
type outlet struct {
	// copied on write
	// A sink may add another while it runs
	sinks atomic.Pointer[[]func(Record)]
	add   sync.Mutex

	// mu guards the queue and the counts below
	// It is taken before a profile's lock and never while a sink runs
	mu    sync.Mutex
	cond  *sync.Cond
	queue []Record
	// records moved in and records the sinks saw
	moved, delivered uint64
	closed           bool
	done             chan struct{}
}

func newOutlet() *outlet {
	o := &outlet{done: make(chan struct{})}
	o.cond = sync.NewCond(&o.mu)
	go o.run()
	return o
}

func (o *outlet) load() []func(Record) {
	if s := o.sinks.Load(); s != nil {
		return *s
	}
	return nil
}

// Adds a sink one add at a time
func (o *outlet) push(fn func(Record)) {
	o.add.Lock()
	defer o.add.Unlock()
	next := append(slices.Clip(o.load()), fn)
	o.sinks.Store(&next)
}

// Moves the records a profile queued in window order
// Waits while the outlet is full
// Returns the count of records moved so far for wait
func (o *outlet) take(p *profile) uint64 {
	o.mu.Lock()
	defer o.mu.Unlock()
	for len(o.queue) >= outletRecords && !o.closed {
		o.cond.Wait()
	}
	p.mu.Lock()
	recs := p.queue
	p.queue = nil
	p.mu.Unlock()
	if o.closed {
		// Close came first so these records go as those of the windows still open
		return o.moved
	}
	o.queue = append(o.queue, recs...)
	o.moved += uint64(len(recs))
	o.cond.Broadcast()
	return o.moved
}

// Waits until the sinks saw the first n records moved or the outlet closed
func (o *outlet) wait(n uint64) {
	o.mu.Lock()
	defer o.mu.Unlock()
	for o.delivered < n && !o.closed {
		o.cond.Wait()
	}
}

// Delivers until close and then the records left
func (o *outlet) run() {
	defer close(o.done)
	o.mu.Lock()
	defer o.mu.Unlock()
	for {
		for len(o.queue) == 0 && !o.closed {
			o.cond.Wait()
		}
		if len(o.queue) == 0 {
			return
		}
		recs := o.queue
		o.queue = nil
		o.cond.Broadcast()
		o.mu.Unlock()
		sinks := o.load()
		for _, r := range recs {
			for _, fn := range sinks {
				fn(r)
			}
		}
		o.mu.Lock()
		o.delivered += uint64(len(recs))
		o.cond.Broadcast()
	}
}

// Delivers what was moved and stops the goroutine
func (o *outlet) close() {
	o.mu.Lock()
	o.closed = true
	o.cond.Broadcast()
	o.mu.Unlock()
	<-o.done
}

// A profile with its windows
// mu guards the windows and the counters and the queue
// No profile takes the lock of another
type profile struct {
	spec
	width       int64
	lateness    int64
	maxDistinct int
	clock       func() time.Time
	out         *outlet

	mu   sync.Mutex
	open []*window
	// the clock plus one window as last read
	// A line past it is future once the clock is read again
	horizon int64
	// windows with a key below it are closed
	closed   int64
	observed uint64
	skipped  uint64
	late     uint64
	future   uint64
	// closed windows in a row during which the profile bound no template
	unbound int64
	// records of closed windows not yet delivered in window order
	queue []Record

	// the templates bound and those that fit without a value slot
	// Guarded by the Assay's mu
	bound   map[moldrain.ID]struct{}
	noValue map[moldrain.ID]struct{}
}

func newProfile(s spec, opt Options, out *outlet) *profile {
	return &profile{
		spec: s, width: int64(opt.Window), lateness: int64(opt.Lateness), maxDistinct: opt.MaxDistinct,
		clock: opt.Clock, out: out, horizon: math.MinInt64, closed: math.MinInt64,
		bound: map[moldrain.ID]struct{}{}, noValue: map[moldrain.ID]struct{}{},
	}
}

// Floor division so times before 1970 land in the window they start
func floorDiv(ns, width int64) int64 {
	k := ns / width
	if ns%width < 0 {
		k--
	}
	return k
}

// Adds one line of the live template id
// Then closes the windows the line has left behind by more than Lateness
// 1. a line of a closed window is late and dropped
// 2. a line more than one window past the clock is future and dropped
// 3. a line past the clock by less counts in its window but closes windows only up to the clock
// One bad timestamp never closes the windows the next lines belong to nor holds a window open
// The clock is read only when a line may be future or may close a window
func (p *profile) observe(id moldrain.ID, r *moldrain.Result, f form, at time.Time) {
	ns := at.UnixNano()
	key := floorDiv(ns, p.width)
	p.mu.Lock()
	p.observed++
	if key < p.closed {
		p.late++
		p.mu.Unlock()
		return
	}
	if ns > p.horizon {
		p.horizon = p.clock().UnixNano() + p.width
		if ns > p.horizon {
			p.future++
			p.mu.Unlock()
			return
		}
	}
	g := p.window(key).group(p.groupName(r, f), p.MaxGroups)
	p.add(g, id, r, f)
	if floorDiv(ns-p.lateness, p.width) > p.closed {
		now := p.clock().UnixNano()
		p.horizon = now + p.width
		p.close(floorDiv(min(ns, now)-p.lateness, p.width), true)
	}
	queued := len(p.queue) > 0
	p.mu.Unlock()
	if queued {
		p.out.take(p)
	}
}

func (p *profile) groupName(r *moldrain.Result, f form) []byte {
	if p.group == nil || f.group < 0 {
		return nil
	}
	return r.Params[f.group]
}

func (p *profile) window(key int64) *window {
	i, found := slices.BinarySearchFunc(p.open, key, func(w *window, k int64) int { return cmp.Compare(w.key, k) })
	if found {
		return p.open[i]
	}
	w := &window{key: key, groups: map[string]*acc{}}
	p.open = slices.Insert(p.open, i, w)
	return w
}

func (w *window) group(name []byte, maxGroups int) *acc {
	if g, ok := w.groups[string(name)]; ok {
		return g
	}
	if len(w.groups) >= maxGroups {
		if w.other == nil {
			w.other = newAcc()
		}
		return w.other
	}
	g := newAcc()
	w.groups[string(name)] = g
	return g
}

func (p *profile) add(g *acc, id moldrain.ID, r *moldrain.Result, f form) {
	g.lines++
	if !slices.Contains(g.tpls, id) {
		g.tpls = append(g.tpls, id)
	}
	if p.distinct && g.dist == nil {
		g.dist = newDistinct(p.maxDistinct)
	}
	if p.value == nil {
		if g.dist != nil {
			h := hashSeed.u64(uint64(id))
			for _, b := range r.Params {
				h = h.bytes(b).u64(math.MaxUint64)
			}
			g.dist.add(h.sum())
		}
		return
	}
	if f.value < 0 {
		g.skipped++
		p.skipped++
		return
	}
	raw := r.Params[f.value]
	if g.dist != nil {
		g.dist.add(hashSeed.bytes(raw).sum())
	}
	v, ok := readValue(raw, p.Unit)
	// a value or a sum past the float range would leave a record JSON cannot encode
	if !ok || math.IsInf(g.sum+v, 0) {
		g.skipped++
		p.skipped++
		return
	}
	g.n++
	g.sum += v
	g.lo = min(g.lo, v)
	g.hi = max(g.hi, v)
	if p.quantiles {
		if g.sk == nil {
			g.sk = newSketch()
		}
		g.sk.add(v)
	}
}

// Closes the windows with a key below upTo and queues their records in window order
// bound says whether the profile binds a template
// It feeds the count of unbound windows
func (p *profile) close(upTo int64, bound bool) {
	if upTo <= p.closed {
		return
	}
	switch {
	case bound:
		p.unbound = 0
	case p.closed != math.MinInt64:
		p.unbound += upTo - p.closed
	}
	p.closed = upTo
	n := 0
	for n < len(p.open) && p.open[n].key < upTo {
		n++
	}
	for _, w := range p.open[:n] {
		p.queue = p.records(p.queue, w)
	}
	p.open = slices.Delete(p.open, 0, n)
}

func (p *profile) records(dst []Record, w *window) []Record {
	start := time.Unix(0, w.key*p.width).UTC()
	end := start.Add(time.Duration(p.width))
	names := make([]string, 0, len(w.groups))
	for name := range w.groups {
		names = append(names, name)
	}
	slices.Sort(names)
	for _, name := range names {
		dst = p.groupRecords(dst, start, end, name, false, w.groups[name])
	}
	if w.other != nil {
		dst = p.groupRecords(dst, start, end, "", true, w.other)
	}
	return dst
}

func (p *profile) groupRecords(dst []Record, start, end time.Time, name string, other bool, g *acc) []Record {
	slices.Sort(g.tpls)
	for _, op := range p.Ops {
		v, ok := g.value(op)
		if !ok {
			continue
		}
		tpls := make([]string, len(g.tpls))
		for i, id := range g.tpls {
			tpls[i] = id.String()
		}
		rec := Record{
			Start: start, End: end, Profile: p.Name, Group: name, Other: other, Op: op, Value: v,
			Lines: g.lines, Skipped: g.skipped, Templates: tpls,
		}
		if op.valued() {
			rec.Unit = p.Unit
		}
		rec.Approx = op == Distinct && g.dist.approx()
		dst = append(dst, rec)
	}
	return dst
}

// The op's value
// false for an op over values when the group read none
func (g *acc) value(op Op) (float64, bool) {
	switch op {
	case Count:
		return float64(g.lines), true
	case Distinct:
		return g.dist.count(), true
	}
	if g.n == 0 {
		return 0, false
	}
	switch op {
	case Sum:
		return g.sum, true
	case Min:
		return g.lo, true
	case Max:
		return g.hi, true
	case Mean:
		return g.sum / float64(g.n), true
	}
	return min(max(g.sk.quantile(op.quantile()), g.lo), g.hi), true
}
