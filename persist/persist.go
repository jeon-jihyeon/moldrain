// Package persist keeps a miner's templates across restarts as a snapshot and a log of the changes after it
package persist

import (
	"context"
	"fmt"
	"iter"
	"sync"
	"sync/atomic"
	"time"

	"github.com/jeon-jihyeon/moldrain"
)

// Where the snapshot and the change log live
// Records reach Append in log order and come back from Load in the same order
type Store interface {
	// The latest snapshot and the records appended after it
	// The snapshot is nil when there is none
	Load(ctx context.Context) (snapshot []byte, changes iter.Seq2[[]byte, error], err error)
	Append(ctx context.Context, records [][]byte) error
	// Replaces the snapshot and starts the change log over
	Replace(ctx context.Context, snapshot []byte) error
}

// Defaults of Options
const (
	DefaultCompactBytes = 4 << 20
	DefaultCompactEvery = time.Hour
	DefaultFlushEvery   = 10 * time.Second
	DefaultMaxBacklog   = 10000
)

type Options struct {
	// A new snapshot once the log passes this many bytes
	CompactBytes int64
	// A new snapshot once the last is this old
	CompactEvery time.Duration
	// How often queued records go to the store
	FlushEvery time.Duration
	// Queued records that trigger a flush before FlushEvery
	// Count deltas merge per template
	// Only template changes grow the queue
	MaxBacklog int
	// The clock CompactEvery reads
	// time.Now by default
	Clock func() time.Time
}

func (o Options) withDefaults() Options {
	if o.CompactBytes <= 0 {
		o.CompactBytes = DefaultCompactBytes
	}
	if o.CompactEvery <= 0 {
		o.CompactEvery = DefaultCompactEvery
	}
	if o.FlushEvery <= 0 {
		o.FlushEvery = DefaultFlushEvery
	}
	if o.MaxBacklog <= 0 {
		o.MaxBacklog = DefaultMaxBacklog
	}
	if o.Clock == nil {
		o.Clock = time.Now
	}
	return o
}

// Writes a miner's changes to a store from its own goroutine
type Persister struct {
	m     *moldrain.Miner
	store Store
	opt   Options

	// mu guards the queue the miner's training goroutine fills
	mu      sync.Mutex
	changes []moldrain.Event
	deltas  map[moldrain.ID]*delta
	order   []moldrain.ID
	stopped bool
	// set from a failed write until the next snapshot lands
	// Nothing queues meanwhile
	// The backlog stays bounded while the store is down
	resync bool

	// io serializes flushes and compactions
	// It guards everything below
	io        sync.Mutex
	seq       uint64
	writer    *recordWriter
	logBytes  int64
	lastSnap  time.Time
	closed    bool
	lastError error

	// the context of the goroutine's flushes
	// Close cancels it so a flush in flight never holds Close up
	// The snapshot of Close holds whatever that flush did not write
	ctx    context.Context
	cancel context.CancelFunc
	// set by the first Close before it waits for the goroutine
	closing atomic.Bool
	wake    chan struct{}
	stop    chan struct{}
	done    chan struct{}
}

// Loads the store into a miner that has not trained
// Then it follows the miner and writes its changes
// A store learned under other template options fails with ErrOptionsChanged and leaves the miner empty
// Train never waits on the store: records queue in memory and a goroutine writes them
// A failed first snapshot leaves the miner a subscriber that ignores every event
// The miner restored from the store is then not empty and a retry needs a new miner
func Attach(ctx context.Context, m *moldrain.Miner, store Store, opt Options) (*Persister, error) {
	p := &Persister{
		m: m, store: store, opt: opt.withDefaults(),
		deltas: map[moldrain.ID]*delta{}, writer: newRecordWriter(),
		wake: make(chan struct{}, 1), stop: make(chan struct{}), done: make(chan struct{}),
	}
	p.ctx, p.cancel = context.WithCancel(context.Background())
	found, logged, err := p.load(ctx)
	if err != nil {
		return nil, err
	}
	// the snapshot taken next holds every event from here on
	// Subscribing first leaves no gap
	m.Subscribe(p.onEvent)
	p.lastSnap = p.opt.Clock()
	// a first snapshot records the options hash before any change is logged
	// A loaded log is replaced too
	// This writer cannot continue its count index
	// A load that evicted under a lower MaxClusters is replaced too
	// Its log would otherwise create again what the snapshot still holds
	if !found || logged || m.Stats().Evicted > 0 {
		if err := p.compact(ctx); err != nil {
			p.mu.Lock()
			p.stopped = true
			p.mu.Unlock()
			p.cancel()
			return nil, err
		}
	}
	go p.loop()
	return p, nil
}

// Loads the store into a miner that has not trained
// It does not follow the miner afterwards
// ok is false when the store holds no snapshot yet
func Load(ctx context.Context, m *moldrain.Miner, store Store) (ok bool, err error) {
	p := &Persister{m: m, store: store}
	ok, _, err = p.load(ctx)
	return ok, err
}

// found is false when the store holds no snapshot
// logged is true when the store held change records
// Records the snapshot already holds count too
func (p *Persister) load(ctx context.Context) (found, logged bool, err error) {
	snap, changes, err := p.store.Load(ctx)
	if err != nil {
		return false, false, fmt.Errorf("load: %w", err)
	}
	if snap == nil {
		// Attach writes a snapshot before the first record
		// Records without one are leftovers of a store that never finished its first start
		// The snapshot written next drops them
		return false, false, nil
	}
	h, st, err := decodeSnapshot(snap)
	if err != nil {
		return false, false, err
	}
	if h.hash != p.m.Fingerprint() {
		return false, false, ErrOptionsChanged
	}
	events, log := readEvents(changes, h.seq)
	if err := p.m.Restore(&st, events); err != nil {
		return false, false, err
	}
	p.seq = log.applied
	return true, log.records > 0, nil
}

// What a replay of a change log read
// Known once its events are consumed
type replay struct {
	// the seq of the last record applied
	// The snapshot's seq when none was
	applied uint64
	// records in the log whether applied or skipped or torn
	records int
}

// The events of a change log after a seq
// A torn last record is dropped
// A record at or below the last applied seq is a copy and is skipped
// A seq past the next one means a lost record and fails with ErrCorrupt
func readEvents(changes iter.Seq2[[]byte, error], after uint64) (iter.Seq2[moldrain.Event, error], *replay) {
	log := &replay{applied: after}
	return func(yield func(moldrain.Event, error) bool) {
		if changes == nil {
			return
		}
		r := recordReader{}
		// reports whether to go on
		apply := func(rec []byte, last bool) bool {
			log.records++
			seq, evs, err := r.read(rec, log.applied)
			switch {
			case err != nil && last:
				// the last record may be torn by a crash mid write
				// A bad one is dropped
				return false
			case err != nil:
				yield(moldrain.Event{}, err)
				return false
			}
			if seq <= log.applied {
				return true
			}
			if seq != log.applied+1 {
				yield(moldrain.Event{}, fmt.Errorf("%w: record %d follows %d", ErrCorrupt, seq, log.applied))
				return false
			}
			log.applied = seq
			for _, ev := range evs {
				if !yield(ev, nil) {
					return false
				}
			}
			return true
		}
		var held []byte
		for rec, err := range changes {
			if err != nil {
				yield(moldrain.Event{}, err)
				return
			}
			if held != nil && !apply(held, false) {
				return
			}
			held = rec
		}
		if held != nil {
			apply(held, true)
		}
	}, log
}

// The events of a change log in order
// A torn last record is dropped
// For tools and the store contract suite
func ReadChanges(changes iter.Seq2[[]byte, error]) ([]moldrain.Event, error) {
	events, _ := readEvents(changes, 0)
	var out []moldrain.Event
	for ev, err := range events {
		if err != nil {
			return out, err
		}
		out = append(out, ev)
	}
	return out, nil
}

// Encodes one event as a change record for tools and the store contract suite
func EncodeChange(seq uint64, ev moldrain.Event) []byte {
	if ev.Kind == moldrain.Matched {
		return newRecordWriter().counts(seq, []delta{{id: ev.ID, n: ev.N, at: ev.At}})
	}
	return newRecordWriter().event(seq, ev)
}

// Runs on the training goroutine
// It only queues
func (p *Persister) onEvent(ev moldrain.Event) {
	p.mu.Lock()
	if p.stopped || p.resync {
		p.mu.Unlock()
		return
	}
	switch ev.Kind {
	case moldrain.Matched:
		d := p.deltas[ev.ID]
		if d == nil {
			d = &delta{id: ev.ID}
			p.deltas[ev.ID] = d
			p.order = append(p.order, ev.ID)
		}
		d.n += ev.N
		if ev.At.After(d.at) {
			d.at = ev.At
		}
	case moldrain.Changed:
		p.queue(ev)
		if d := p.deltas[ev.From]; d != nil {
			delete(p.deltas, ev.From)
			if to := p.deltas[ev.To]; to != nil {
				to.n += d.n
				if d.at.After(to.at) {
					to.at = d.at
				}
			} else {
				d.id = ev.To
				p.deltas[ev.To] = d
				p.order = append(p.order, ev.To)
			}
		}
	case moldrain.Created:
		p.queue(ev)
	case moldrain.Evicted:
		p.queue(ev)
		delete(p.deltas, ev.ID)
	}
	full := len(p.changes)+len(p.deltas) >= p.opt.MaxBacklog
	p.mu.Unlock()
	if full {
		select {
		case p.wake <- struct{}{}:
		default:
		}
	}
}

func (p *Persister) queue(ev moldrain.Event) {
	ev.Line = nil
	p.changes = append(p.changes, ev)
}

// Takes the queue: template changes in order and the count deltas still held
func (p *Persister) take() ([]moldrain.Event, []delta) {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.takeLocked()
}

func (p *Persister) takeLocked() ([]moldrain.Event, []delta) {
	changes := p.changes
	p.changes = nil
	var ds []delta
	for _, id := range p.order {
		if d := p.deltas[id]; d != nil && d.id == id {
			ds = append(ds, *d)
			// an id that came back after it moved away is listed twice in order
			d.id = 0
		}
	}
	p.deltas = map[moldrain.ID]*delta{}
	p.order = nil
	return changes, ds
}

// Drops the queue until the next snapshot lands
// That snapshot holds every change
// No event queues between the drop and the flag
func (p *Persister) resyncLater() {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.takeLocked()
	p.resync = true
}

func (p *Persister) loop() {
	defer close(p.done)
	t := time.NewTicker(p.opt.FlushEvery)
	defer t.Stop()
	for {
		select {
		case <-p.stop:
			return
		case <-t.C:
		case <-p.wake:
		}
		ctx, cancel := context.WithTimeout(p.ctx, p.opt.FlushEvery)
		_ = p.Flush(ctx)
		cancel()
	}
}

// Writes queued records now and then compacts when the log is large or old
// A failed write may have left part of its records in the store
// The next flush writes a snapshot instead of a retry
func (p *Persister) Flush(ctx context.Context) error {
	p.io.Lock()
	defer p.io.Unlock()
	if p.closed {
		return ErrClosed
	}
	p.lastError = p.flushLocked(ctx)
	return p.lastError
}

func (p *Persister) flushLocked(ctx context.Context) error {
	p.mu.Lock()
	resync := p.resync
	p.mu.Unlock()
	if resync {
		return p.compactLocked(ctx)
	}
	changes, ds := p.take()
	recs := make([][]byte, 0, len(changes)+1)
	for _, ev := range changes {
		p.seq++
		recs = append(recs, p.writer.event(p.seq, ev))
	}
	if len(ds) > 0 {
		p.seq++
		recs = append(recs, p.writer.counts(p.seq, ds))
	}
	if len(recs) > 0 {
		if err := p.store.Append(ctx, recs); err != nil {
			p.resyncLater()
			return fmt.Errorf("append: %w", err)
		}
		for _, r := range recs {
			p.logBytes += int64(len(r))
		}
	}
	if p.logBytes >= p.opt.CompactBytes || p.opt.Clock().Sub(p.lastSnap) >= p.opt.CompactEvery {
		return p.compactLocked(ctx)
	}
	return nil
}

// Writes a new snapshot and starts the log over
func (p *Persister) compact(ctx context.Context) error {
	p.io.Lock()
	defer p.io.Unlock()
	return p.compactLocked(ctx)
}

func (p *Persister) compactLocked(ctx context.Context) error {
	var snap []byte
	p.m.Snapshot(func(st moldrain.State) {
		// the state holds every queued change
		// The queue goes
		// Training waits meanwhile
		// The next event lands after the state
		p.mu.Lock()
		p.takeLocked()
		p.resync = false
		p.mu.Unlock()
		snap = encodeSnapshot(p.m.Fingerprint(), p.seq, st)
	})
	if err := p.store.Replace(ctx, snap); err != nil {
		p.resyncLater()
		return fmt.Errorf("replace: %w", err)
	}
	p.writer = newRecordWriter()
	p.logBytes = 0
	p.lastSnap = p.opt.Clock()
	return nil
}

// The error of the latest flush
// nil once one succeeds
func (p *Persister) Err() error {
	p.io.Lock()
	defer p.io.Unlock()
	return p.lastError
}

// Stops the goroutine and writes a final snapshot
// Changes the miner makes after Close are not written
func (p *Persister) Close(ctx context.Context) error {
	if !p.closing.CompareAndSwap(false, true) {
		return ErrClosed
	}
	// the flush in flight holds io until its context ends
	close(p.stop)
	p.cancel()
	<-p.done
	p.io.Lock()
	defer p.io.Unlock()
	p.closed = true
	// the final snapshot holds every queued change
	// No flush is needed before it
	err := p.compactLocked(ctx)
	p.mu.Lock()
	p.stopped = true
	p.mu.Unlock()
	return err
}
