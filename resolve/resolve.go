// Package resolve asks a model about lines no template fits and merges the answer back off the hot path
package resolve

import (
	"context"
	"sync"
	"time"

	"github.com/jeon-jihyeon/moldrain"
)

// Answers which older template each pending line is a variant of
// It never judges whether a line is an error or an anomaly
// That is the caller's call
// With an error the decisions it still returns are kept and the lines without one are tried again
// Without an error a line without a decision is New
type Resolver interface {
	Resolve(ctx context.Context, batch []Pending) ([]Decision, error)
}

// A line that started a provisional template with the templates nearest to it
type Pending struct {
	Line       []byte
	Template   string
	Candidates []Candidate
}

type Candidate struct {
	ID       string
	Template string
	Sim      float64
	// The latest value of each template slot when the miner keeps values
	// One entry per slot and not per variable slot as the Params of a Result
	// nil for a constant slot or one without values
	Values [][]byte
}

// The answer for the pending line at Index
// Same names the candidate id and is empty for a new template
type Decision struct {
	Index int
	Same  string
}

// Defaults of Options
const (
	DefaultResolveEvery = 5 * time.Second
	DefaultResolveBatch = 32
	DefaultMaxPending   = 1000
	DefaultCandidates   = 3
	DefaultMinSim       = 0.2
	DefaultTries        = 3
	DefaultCallTimeout  = time.Minute
)

type Options struct {
	// Batch interval
	ResolveEvery time.Duration
	// Lines per call
	// Also the queue length that triggers a call before ResolveEvery
	ResolveBatch int
	// Past it the oldest pending line is decided New without asking
	MaxPending int
	// Nearest templates of the line's token count offered per line
	Candidates int
	// A line with no candidate at or above it is New without asking
	// 0 takes the default and a negative value offers every candidate
	MinSim float64
	// Calls a line may fail before it is decided New
	Tries int
	// Deadline of one Resolve call
	CallTimeout time.Duration
}

func (o Options) withDefaults() Options {
	if o.ResolveEvery <= 0 {
		o.ResolveEvery = DefaultResolveEvery
	}
	if o.ResolveBatch <= 0 {
		o.ResolveBatch = DefaultResolveBatch
	}
	if o.MaxPending <= 0 {
		o.MaxPending = DefaultMaxPending
	}
	if o.Candidates <= 0 {
		o.Candidates = DefaultCandidates
	}
	if o.MinSim == 0 {
		o.MinSim = DefaultMinSim
	}
	if o.Tries <= 0 {
		o.Tries = DefaultTries
	}
	if o.CallTimeout <= 0 {
		o.CallTimeout = DefaultCallTimeout
	}
	return o
}

type Stats struct {
	// Lines waiting for an answer
	Pending int
	// Lines sent to the resolver counted once per try
	Asked uint64
	// Provisional templates merged into an older one
	Merged uint64
	// Lines decided New
	// 1. by the answer
	// 2. without a candidate
	// 3. after failed tries
	// 4. over MaxPending
	New uint64
	// Lines decided New because the queue passed MaxPending
	Dropped uint64
	// Resolve calls that failed
	// A call cancelled by Close or by the caller's context is no failure
	Failed uint64
	// Lines whose template was evicted or merged into another before their answer
	// Neither New nor Merged
	Gone uint64
}

type pending struct {
	id       moldrain.ID
	line     []byte
	template string
	tries    int
}

// Follows a miner's new templates and resolves them in batches on its own goroutine
type Loop struct {
	m   *moldrain.Miner
	r   Resolver
	opt Options

	mu    sync.Mutex
	queue []*pending
	byID  map[moldrain.ID]*pending
	// every template the miner holds
	// A Changed into one of them is a merge that ends a provisional template
	live    map[moldrain.ID]struct{}
	stats   Stats
	stopped bool

	// one batch at a time from the loop or Flush
	run sync.Mutex
	// cancelled by Close
	// A call in flight ends with it whether the loop or Flush made it
	ctx    context.Context
	cancel context.CancelFunc
	wake   chan struct{}
	done   chan struct{}
}

// Starts resolving the provisional templates m creates from now on
// Train never waits on the resolver: the callback copies the line into a queue and returns
func Attach(m *moldrain.Miner, r Resolver, opt Options) *Loop {
	ctx, cancel := context.WithCancel(context.Background())
	l := &Loop{
		m: m, r: r, opt: opt.withDefaults(), byID: map[moldrain.ID]*pending{}, live: map[moldrain.ID]struct{}{},
		ctx: ctx, cancel: cancel, wake: make(chan struct{}, 1), done: make(chan struct{}),
	}
	m.Subscribe(l.onEvent)
	// training waits during the snapshot
	// With the events after it the set stays whole
	m.Snapshot(func(st moldrain.State) {
		l.mu.Lock()
		defer l.mu.Unlock()
		for _, c := range st.Clusters {
			l.live[c.ID] = struct{}{}
		}
	})
	go l.loop()
	return l
}

func (l *Loop) onEvent(ev moldrain.Event) {
	l.mu.Lock()
	if l.stopped {
		l.mu.Unlock()
		return
	}
	switch ev.Kind {
	case moldrain.Created:
		l.live[ev.ID] = struct{}{}
		l.created(ev)
	case moldrain.Changed:
		l.changed(ev)
	case moldrain.Evicted:
		delete(l.live, ev.ID)
		l.forget(ev.ID)
	}
	full := len(l.queue) >= l.opt.ResolveBatch
	l.mu.Unlock()
	if full {
		select {
		case l.wake <- struct{}{}:
		default:
		}
	}
}

func (l *Loop) created(ev moldrain.Event) {
	if ev.Line == nil || ev.Shape == nil {
		return
	}
	p := &pending{id: ev.ID, line: append([]byte(nil), ev.Line...), template: ev.Shape.String()}
	l.queue = append(l.queue, p)
	l.byID[ev.ID] = p
	l.trim()
}

// A pending template follows a rename and leaves the queue when it merges into another
// An alias leaves it too as it was decided
func (l *Loop) changed(ev moldrain.Event) {
	if ev.Alias {
		l.forget(ev.From)
		return
	}
	_, merged := l.live[ev.To]
	delete(l.live, ev.From)
	l.live[ev.To] = struct{}{}
	p := l.byID[ev.From]
	if p == nil {
		return
	}
	if merged {
		l.forget(ev.From)
		return
	}
	delete(l.byID, ev.From)
	p.id = ev.To
	if ev.Shape != nil {
		p.template = ev.Shape.String()
	}
	l.byID[ev.To] = p
}

// Drops the pending line of a template without deciding it
func (l *Loop) forget(id moldrain.ID) {
	p := l.byID[id]
	if p == nil {
		return
	}
	delete(l.byID, id)
	for i, q := range l.queue {
		if q == p {
			l.queue = append(l.queue[:i:i], l.queue[i+1:]...)
			return
		}
	}
}

// Decides New for the oldest lines past MaxPending
func (l *Loop) trim() {
	for len(l.queue) > l.opt.MaxPending {
		delete(l.byID, l.queue[0].id)
		l.queue = l.queue[1:]
		l.stats.Dropped++
		l.stats.New++
	}
}

func (l *Loop) loop() {
	defer close(l.done)
	t := time.NewTicker(l.opt.ResolveEvery)
	defer t.Stop()
	for {
		select {
		case <-l.ctx.Done():
			return
		case <-t.C:
		case <-l.wake:
		}
		l.batch(l.ctx)
	}
}

// Resolves every pending line now batch after batch
// It stops at the first call that fails so one Flush is at most one try per line
// Lines of that call stay pending for a later try
// After Close it does nothing
func (l *Loop) Flush(ctx context.Context) {
	for {
		l.mu.Lock()
		n := len(l.queue)
		l.mu.Unlock()
		if n == 0 || ctx.Err() != nil {
			return
		}
		if !l.batch(ctx) {
			return
		}
	}
}

// One pending line as the batch took it
// The id and the template are copied under mu
// Training may change them during the call
type ask struct {
	p        *pending
	id       moldrain.ID
	template string
}

// Takes up to ResolveBatch lines
// Asks about those with candidates and merges the answers
// Reports whether another batch may follow
// false when nothing was taken or the call failed or the loop is closed
func (l *Loop) batch(ctx context.Context) bool {
	l.run.Lock()
	defer l.run.Unlock()
	l.mu.Lock()
	if l.stopped {
		l.mu.Unlock()
		return false
	}
	n := min(len(l.queue), l.opt.ResolveBatch)
	taken := make([]ask, n)
	for i, p := range l.queue[:n] {
		taken[i] = ask{p: p, id: p.id, template: p.template}
	}
	l.queue = l.queue[n:]
	l.mu.Unlock()
	if n == 0 {
		return false
	}
	var asked []*pending
	var batch []Pending
	for _, a := range taken {
		cands := l.candidates(a)
		if len(cands) == 0 {
			l.decide(a.p, "")
			continue
		}
		asked = append(asked, a.p)
		batch = append(batch, Pending{Line: a.p.line, Template: a.template, Candidates: cands})
	}
	if len(batch) == 0 {
		return true
	}
	ds, err := l.call(ctx, batch)
	l.mu.Lock()
	l.stats.Asked += uint64(len(batch))
	l.mu.Unlock()
	answers, answered := read(batch, ds)
	var unanswered []*pending
	for i, p := range asked {
		if err != nil && !answered[i] {
			unanswered = append(unanswered, p)
			continue
		}
		l.decide(p, answers[i])
	}
	if err != nil {
		// a call that Close or the caller cancelled is no try
		l.failed(unanswered, ctx.Err() == nil && l.ctx.Err() == nil)
		return false
	}
	return true
}

// The nearest templates of a line at or above MinSim
func (l *Loop) candidates(a ask) []Candidate {
	var cands []Candidate
	for _, nb := range l.m.Nearest(a.p.line, l.opt.Candidates, a.id) {
		if nb.Sim >= l.opt.MinSim {
			cands = append(cands, Candidate{ID: nb.ID.String(), Template: nb.Template, Sim: nb.Sim, Values: values(nb.Params)})
		}
	}
	return cands
}

// The answer of each line of a batch and whether the resolver gave one
// An answer that names no candidate is New
// A decision outside the batch is ignored
func read(batch []Pending, ds []Decision) (answers []string, answered []bool) {
	answers = make([]string, len(batch))
	answered = make([]bool, len(batch))
	for _, d := range ds {
		if d.Index < 0 || d.Index >= len(batch) {
			continue
		}
		answered[d.Index] = true
		if offered(batch[d.Index], d.Same) {
			answers[d.Index] = d.Same
		}
	}
	return answers, answered
}

// One Resolve call within CallTimeout
// Close cancels it too
func (l *Loop) call(ctx context.Context, batch []Pending) ([]Decision, error) {
	cctx, cancel := context.WithTimeout(ctx, l.opt.CallTimeout)
	defer cancel()
	stop := context.AfterFunc(l.ctx, cancel)
	defer stop()
	return l.r.Resolve(cctx, batch)
}

func offered(p Pending, id string) bool {
	for _, c := range p.Candidates {
		if c.ID == id {
			return true
		}
	}
	return false
}

// Puts the unanswered lines of a failed call back
// tried counts the call as a try and decides New for the lines out of tries
// A line whose template was evicted or merged during the call is not put back
func (l *Loop) failed(unanswered []*pending, tried bool) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if tried {
		l.stats.Failed++
	}
	var back []*pending
	for _, p := range unanswered {
		if l.byID[p.id] != p {
			l.stats.Gone++
			continue
		}
		if tried {
			p.tries++
		}
		if p.tries >= l.opt.Tries {
			delete(l.byID, p.id)
			l.stats.New++
			continue
		}
		back = append(back, p)
	}
	l.queue = append(back, l.queue...)
	l.trim()
}

// Merges a provisional template into the answer
// Otherwise leaves it as a template of its own
// A template evicted or merged into another meanwhile counts as gone
func (l *Loop) decide(p *pending, same string) {
	l.mu.Lock()
	id, live := p.id, l.byID[p.id] == p
	l.mu.Unlock()
	merged := false
	if live && same != "" {
		if to, err := moldrain.ParseID(same); err == nil {
			merged = l.m.Alias(id, to) == nil
		}
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	switch {
	case merged:
		l.stats.Merged++
	case l.byID[p.id] != p:
		l.stats.Gone++
	default:
		delete(l.byID, p.id)
		l.stats.New++
	}
}

// The latest value of each slot
// nil for a slot without one
func values(vals [][]string) [][]byte {
	if len(vals) == 0 {
		return nil
	}
	out := make([][]byte, len(vals))
	for i, v := range vals {
		if len(v) > 0 {
			out[i] = []byte(v[len(v)-1])
		}
	}
	return out
}

func (l *Loop) Stats() Stats {
	l.mu.Lock()
	defer l.mu.Unlock()
	s := l.stats
	s.Pending = len(l.queue)
	return s
}

// Stops the goroutine and cancels a call in flight
// Lines still pending stay templates of their own
func (l *Loop) Close() {
	l.mu.Lock()
	if l.stopped {
		l.mu.Unlock()
		return
	}
	l.stopped = true
	l.mu.Unlock()
	l.cancel()
	<-l.done
}
