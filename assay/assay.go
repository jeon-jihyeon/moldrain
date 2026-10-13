// Package assay turns trained lines into window aggregates of profiles bound to learned templates
package assay

import (
	"fmt"
	"slices"
	"sync"
	"sync/atomic"
	"time"

	"github.com/jeon-jihyeon/moldrain"
)

// Defaults of Options
const (
	DefaultWindow         = 5 * time.Minute
	DefaultLateness       = 30 * time.Second
	DefaultMaxDistinct    = 10000
	DefaultUnboundWindows = 12
)

// A zero field takes its default
type Options struct {
	// Tumbling window width aligned to Unix time
	Window time.Duration
	// How long a window stays open after its end for lines that arrive late
	Lateness time.Duration
	// Distinct values per group and window counted exactly
	// Past it a HyperLogLog estimates
	MaxDistinct int
	// Closed windows in a row without a bound template before Stats lists the profile as unbound
	UnboundWindows int
	// The time a line stamped in the future is held to
	// A line more than one Window past it is dropped as future
	// time.Now by default
	Clock func() time.Time
}

func (o Options) withDefaults() Options {
	if o.Window == 0 {
		o.Window = DefaultWindow
	}
	if o.Lateness == 0 {
		o.Lateness = DefaultLateness
	}
	if o.MaxDistinct == 0 {
		o.MaxDistinct = DefaultMaxDistinct
	}
	if o.UnboundWindows == 0 {
		o.UnboundWindows = DefaultUnboundWindows
	}
	if o.Clock == nil {
		o.Clock = time.Now
	}
	return o
}

func (o Options) validate() error {
	switch {
	case o.Window < 0:
		return fmt.Errorf("%w: Window %v is negative", ErrOptions, o.Window)
	case o.Lateness < 0:
		return fmt.Errorf("%w: Lateness %v is negative", ErrOptions, o.Lateness)
	case o.MaxDistinct < 0:
		return fmt.Errorf("%w: MaxDistinct %d is negative", ErrOptions, o.MaxDistinct)
	case o.UnboundWindows < 0:
		return fmt.Errorf("%w: UnboundWindows %d is negative", ErrOptions, o.UnboundWindows)
	}
	return nil
}

// Aggregates the lines of a miner per profile and window
type Assay struct {
	opt      Options
	profiles []*profile
	routes   atomic.Pointer[map[moldrain.ID][]route]
	out      *outlet
	closed   atomic.Bool

	// mu guards the binding state and every profile's bound and noValue
	mu sync.Mutex
	b  binds
}

// Validates the profiles and binds them to the miner's templates
// Follows its events from now on
func New(m *moldrain.Miner, profiles []Profile, opt Options) (*Assay, error) {
	opt = opt.withDefaults()
	if err := opt.validate(); err != nil {
		return nil, err
	}
	if m == nil {
		return nil, fmt.Errorf("%w: the miner is nil", ErrOptions)
	}
	specs, err := compile(profiles)
	if err != nil {
		return nil, err
	}
	a := &Assay{opt: opt, b: newBinds(), out: newOutlet()}
	for _, s := range specs {
		a.profiles = append(a.profiles, newProfile(s, opt, a.out))
	}
	empty := map[moldrain.ID][]route{}
	a.routes.Store(&empty)
	m.Subscribe(a.onEvent)
	m.Snapshot(a.load)
	return a, nil
}

// Adds one trained or matched line seen at a time to every profile bound to its template
// Safe for many goroutines since each profile takes its own lock
// A line of an unbound template returns at once
func (a *Assay) Observe(r moldrain.Result, at time.Time) {
	if a.closed.Load() {
		return
	}
	rs := (*a.routes.Load())[r.ID]
	for i := range rs {
		rt := &rs[i]
		rt.p.observe(rt.id, &r, rt.pick(&r), at)
	}
}

// Calls fn with every record of a window that closes
// Records of one profile come in window order and no two calls run at once
// fn runs on the assay's delivery goroutine with no profile locked
// It may call Stats and OnRecord but not Observe or Flush or Close
func (a *Assay) OnRecord(fn func(Record)) { a.out.push(fn) }

// Closes every window that ended before now minus Lateness
// Returns once the sinks saw the records of every window closed so far
func (a *Assay) Flush(now time.Time) {
	if a.closed.Load() {
		return
	}
	var moved uint64
	for _, p := range a.profiles {
		a.mu.Lock()
		bound := len(p.bound) > 0
		a.mu.Unlock()
		p.mu.Lock()
		p.close(floorDiv(now.UnixNano()-p.lateness, p.width), bound)
		p.mu.Unlock()
		moved = a.out.take(p)
	}
	a.out.wait(moved)
}

// Stops following the miner and returns once the sinks saw every record of a closed window
// Observe and Flush do nothing after it and the windows still open emit no record
func (a *Assay) Close() {
	a.closed.Store(true)
	a.out.close()
}

type Stats struct {
	Profiles []ProfileStats
	// Profiles that bound no template for UnboundWindows closed windows in a row
	// The logs they watched may have changed shape
	Unbound []string
}

type ProfileStats struct {
	Name string
	// Lines of bound templates with the late and future ones
	Observed uint64
	// Lines whose value could not be read
	Skipped uint64
	// Lines dropped since their window had closed
	Late uint64
	// Lines dropped since they were stamped more than one window past the clock
	Future uint64
	// Templates the profile aggregates
	Bound []moldrain.ID
	// Templates that fit the pattern but lack the value or group slot
	NoValue []moldrain.ID
}

func (a *Assay) Stats() Stats {
	var st Stats
	for _, p := range a.profiles {
		a.mu.Lock()
		ps := ProfileStats{Name: p.Name, Bound: sortedIDs(p.bound), NoValue: sortedIDs(p.noValue)}
		a.mu.Unlock()
		p.mu.Lock()
		ps.Observed, ps.Skipped, ps.Late, ps.Future = p.observed, p.skipped, p.late, p.future
		unbound := p.unbound
		p.mu.Unlock()
		st.Profiles = append(st.Profiles, ps)
		if unbound >= int64(a.opt.UnboundWindows) {
			st.Unbound = append(st.Unbound, p.Name)
		}
	}
	return st
}

func sortedIDs(set map[moldrain.ID]struct{}) []moldrain.ID {
	out := make([]moldrain.ID, 0, len(set))
	for id := range set {
		out = append(out, id)
	}
	slices.Sort(out)
	return out
}
