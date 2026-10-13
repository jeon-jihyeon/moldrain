package resolve_test

//go:generate mockgen -destination resolver_mock_test.go -package resolve_test -typed=false github.com/jeon-jihyeon/moldrain/resolve Resolver

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"

	"github.com/jeon-jihyeon/moldrain"
	"github.com/jeon-jihyeon/moldrain/resolve"
)

var t0 = time.Date(2026, 1, 2, 3, 4, 0, 0, time.UTC)

// The changes the miner emitted as the training goroutine recorded them
type changes struct {
	mu  sync.Mutex
	evs []moldrain.Event
}

func (c *changes) list() []moldrain.Event {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]moldrain.Event(nil), c.evs...)
}

func setup(t *testing.T, r resolve.Resolver, opt moldrain.Options, ropt resolve.Options) (*moldrain.Miner, *resolve.Loop, *changes) {
	t.Helper()
	m, err := moldrain.New(opt)
	require.NoError(t, err)
	ch := &changes{}
	m.Subscribe(func(ev moldrain.Event) {
		if ev.Kind == moldrain.Changed && ev.Alias {
			ch.mu.Lock()
			ch.evs = append(ch.evs, ev)
			ch.mu.Unlock()
		}
	})
	if ropt.ResolveEvery == 0 {
		ropt.ResolveEvery = time.Hour
	}
	l := resolve.Attach(m, r, ropt)
	t.Cleanup(l.Close)
	return m, l, ch
}

func TestMergeBack(t *testing.T) {
	// a template id is the hash of its template so any miner gives it
	m0, err := moldrain.New(moldrain.Options{})
	require.NoError(t, err)
	old := m0.Train([]byte("disk full on sda now"), t0).ID.String()
	asked := []resolve.Pending{{
		Line:     []byte("disk is full on sdb"),
		Template: "disk is full on sdb",
		Candidates: []resolve.Candidate{{
			ID: old, Template: "disk full on sda now", Sim: 0.2, Values: make([][]byte, 5),
		}},
	}}
	type want struct {
		aliased bool
		counts  []uint64
		stats   resolve.Stats
	}
	tcs := []struct {
		name   string
		answer []resolve.Decision
		err    error
		want   want
	}{
		{
			"a Same answer aliases the provisional template",
			[]resolve.Decision{{Index: 0, Same: old}}, nil,
			want{true, []uint64{3}, resolve.Stats{Asked: 1, Merged: 1, New: 1}},
		},
		{
			"a Same that names no candidate is New",
			[]resolve.Decision{{Index: 0, Same: "0000000000000001"}}, nil,
			want{false, []uint64{2, 1}, resolve.Stats{Asked: 1, New: 2}},
		},
		{
			"an index outside the batch is ignored",
			[]resolve.Decision{{Index: 5, Same: "x"}}, nil,
			want{false, []uint64{2, 1}, resolve.Stats{Asked: 1, New: 2}},
		},
		{
			"an answer of a call that failed is kept",
			[]resolve.Decision{{Index: 0, Same: old}}, errors.New("model down for another line"),
			want{true, []uint64{3}, resolve.Stats{Asked: 1, Merged: 1, New: 1, Failed: 1}},
		},
	}
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			r := NewMockResolver(gomock.NewController(t))
			r.EXPECT().Resolve(gomock.Any(), asked).Return(tc.answer, tc.err)
			m, l, ch := setup(t, r, moldrain.Options{KeepValues: 2}, resolve.Options{})
			m.Train([]byte("disk full on sda now"), t0)
			m.Publish()
			m.Train([]byte("disk is full on sdb"), t0)
			m.Publish()
			l.Flush(context.Background())
			m.Train([]byte("disk is full on sdc"), t0)
			m.Publish()

			got := want{aliased: len(ch.list()) == 1, stats: l.Stats()}
			for _, c := range m.Clusters() {
				got.counts = append(got.counts, c.Count)
			}
			assert.Equal(t, tc.want, got)
		})
	}
}

func TestNoCandidateIsNewWithoutAsking(t *testing.T) {
	r := NewMockResolver(gomock.NewController(t))
	m, l, _ := setup(t, r, moldrain.Options{}, resolve.Options{})
	m.Train([]byte("alpha beta"), t0)
	m.Publish()
	l.Flush(context.Background())
	assert.Equal(t, resolve.Stats{New: 1}, l.Stats())
}

func TestFailingResolverTriesThenNew(t *testing.T) {
	r := NewMockResolver(gomock.NewController(t))
	r.EXPECT().Resolve(gomock.Any(), gomock.Any()).Return(nil, errors.New("model down")).Times(3)
	m, l, ch := setup(t, r, moldrain.Options{}, resolve.Options{})
	m.Train([]byte("disk full on sda now"), t0)
	m.Publish()
	m.Train([]byte("disk is full on sdb"), t0)
	m.Publish()
	// a failed call ends a flush even after the batch decided a line without asking
	// Each flush is one try
	l.Flush(context.Background())
	assert.Equal(t, resolve.Stats{Pending: 1, Asked: 1, New: 1, Failed: 1}, l.Stats())
	for i := 0; i < 3; i++ {
		l.Flush(context.Background())
	}
	assert.Empty(t, ch.list())
	assert.Equal(t, resolve.Stats{Asked: 3, New: 2, Failed: 3}, l.Stats())
}

func TestPending(t *testing.T) {
	tcs := []struct {
		name  string
		opt   moldrain.Options
		ropt  resolve.Options
		lines []string
		want  resolve.Stats
	}{
		{
			"past MaxPending the oldest is New",
			moldrain.Options{},
			resolve.Options{MaxPending: 2, ResolveBatch: 100},
			[]string{"w0 x0 y0", "w1 x1 y1 z1", "w2 x2", "w3", "w4 x4 y4 z4 v4"},
			resolve.Stats{Pending: 2, New: 3, Dropped: 3},
		},
		{
			"a provisional template that merges into another leaves the queue",
			moldrain.Options{MaxChildren: 4},
			resolve.Options{ResolveBatch: 100},
			[]string{"A a B", "A b B", "A x1 B", "A x2 B", "C c D", "C d D"},
			resolve.Stats{Pending: 2},
		},
		{
			"an evicted provisional template leaves the queue",
			moldrain.Options{MaxClusters: 1},
			resolve.Options{ResolveBatch: 100},
			[]string{"w0 x0 y0", "w1 x1 y1 z1", "w2 x2"},
			resolve.Stats{Pending: 1},
		},
	}
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			m, l, _ := setup(t, NewMockResolver(gomock.NewController(t)), tc.opt, tc.ropt)
			for _, line := range tc.lines {
				m.Train([]byte(line), t0)
			}
			assert.Equal(t, tc.want, l.Stats())
		})
	}
}

// A provisional template that a rebuild merges into a template learned before Attach leaves the queue
func TestMergeIntoTemplateBeforeAttach(t *testing.T) {
	m, err := moldrain.New(moldrain.Options{MaxChildren: 3})
	require.NoError(t, err)
	// "1 b" and "z b" make "<*> b" and "x c" gives the first position a child
	for _, line := range []string{"1 b", "z b", "x c"} {
		m.Train([]byte(line), t0)
	}
	l := resolve.Attach(m, NewMockResolver(gomock.NewController(t)), resolve.Options{ResolveEvery: time.Hour, ResolveBatch: 100})
	t.Cleanup(l.Close)
	// "x b" starts its own template under the child of "x"
	// "w c" turns the first position unstable and the rebuild merges "x b" into "<*> b"
	for _, line := range []string{"x b", "y c", "w c"} {
		m.Train([]byte(line), t0)
	}
	assert.Equal(t, resolve.Stats{}, l.Stats())
}

// A failed call puts its lines back within MaxPending and never puts back an evicted one
func TestFailedCallRequeues(t *testing.T) {
	tcs := []struct {
		name string
		opt  moldrain.Options
		// trained while the call runs
		lines []string
		want  resolve.Stats
	}{
		{
			"the line asked about is the oldest past MaxPending",
			moldrain.Options{},
			[]string{"job a b c d e f", "job g h i j k l m"},
			resolve.Stats{Pending: 2, Asked: 1, New: 2, Dropped: 1, Failed: 1},
		},
		{
			"the line asked about was evicted",
			moldrain.Options{MaxClusters: 3},
			[]string{"disk full on sda now", "job a b c d e f", "job g h i j k l m"},
			resolve.Stats{Pending: 2, Asked: 1, New: 1, Failed: 1, Gone: 1},
		},
	}
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			r := NewMockResolver(gomock.NewController(t))
			m, l, _ := setup(t, r, tc.opt, resolve.Options{MaxPending: 2, ResolveBatch: 100})
			m.Train([]byte("disk full on sda now"), t0)
			m.Publish()
			l.Flush(context.Background())
			r.EXPECT().Resolve(gomock.Any(), gomock.Any()).DoAndReturn(func(context.Context, []resolve.Pending) ([]resolve.Decision, error) {
				for _, line := range tc.lines {
					m.Train([]byte(line), t0)
				}
				return nil, errors.New("model down")
			})
			m.Train([]byte("disk is full on sdb"), t0)
			m.Publish()
			l.Flush(context.Background())
			assert.Equal(t, tc.want, l.Stats())
		})
	}
}

// Train goes on while the resolver blocks inside a call
func TestSlowResolverNeverBlocksTrain(t *testing.T) {
	r := NewMockResolver(gomock.NewController(t))
	entered := make(chan struct{})
	release := make(chan struct{})
	var once sync.Once
	r.EXPECT().Resolve(gomock.Any(), gomock.Any()).DoAndReturn(func(ctx context.Context, _ []resolve.Pending) ([]resolve.Decision, error) {
		once.Do(func() { close(entered) })
		<-release
		return nil, ctx.Err()
	}).MinTimes(1)
	m, l, _ := setup(t, r, moldrain.Options{MatchAfterTrain: true}, resolve.Options{ResolveBatch: 1, Tries: 100})
	m.Train([]byte("disk full on sda now"), t0)
	m.Train([]byte("disk is full on sdb"), t0)
	go l.Flush(context.Background())
	<-entered
	for i := 0; i < 1000; i++ {
		m.Train([]byte(fmt.Sprintf("event%d word%d thing%d happened", i%50, i%7, i)), t0)
	}
	assert.Equal(t, uint64(1002), m.Stats().Lines)
	close(release)
}

// The batch reads a pending line while training renames its template
// Run under -race
func TestBatchWhileTraining(t *testing.T) {
	r := NewMockResolver(gomock.NewController(t))
	r.EXPECT().Resolve(gomock.Any(), gomock.Any()).Return(nil, nil).AnyTimes()
	m, l, _ := setup(t, r, moldrain.Options{MatchAfterTrain: true}, resolve.Options{ResolveBatch: 4, MinSim: 0.01})
	done := make(chan struct{})
	go func() {
		defer close(done)
		for i := 0; i < 2000; i++ {
			word := string(rune('a'+i%26)) + string(rune('a'+i/26%26))
			m.Train([]byte("svc "+word+" started on alpha"), t0)
			m.Train([]byte("svc "+word+" started on beta"), t0)
		}
	}()
	for {
		select {
		case <-done:
			l.Flush(context.Background())
			assert.Zero(t, l.Stats().Pending)
			return
		default:
			l.Flush(context.Background())
		}
	}
}

// Close stops a call in flight of the loop or of a Flush instead of waiting for its CallTimeout
// The cancelled call is no try and a Flush after Close asks nothing
func TestCloseCancelsCall(t *testing.T) {
	tcs := []struct {
		name string
		// 2 wakes the loop once both lines queue and 3 leaves them to a Flush
		batch int
		// a Flush makes the call instead of the loop
		flush bool
	}{
		{"a call of the loop", 2, false},
		{"a call of a Flush", 3, true},
	}
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			r := NewMockResolver(gomock.NewController(t))
			entered := make(chan struct{})
			r.EXPECT().Resolve(gomock.Any(), gomock.Any()).DoAndReturn(func(ctx context.Context, _ []resolve.Pending) ([]resolve.Decision, error) {
				close(entered)
				<-ctx.Done()
				return nil, ctx.Err()
			})
			m, err := moldrain.New(moldrain.Options{MatchAfterTrain: true})
			require.NoError(t, err)
			l := resolve.Attach(m, r, resolve.Options{ResolveBatch: tc.batch, ResolveEvery: time.Hour, CallTimeout: time.Hour})
			m.Train([]byte("disk full on sda now"), t0)
			m.Train([]byte("disk is full on sdb"), t0)
			flushed := make(chan struct{})
			go func() {
				defer close(flushed)
				if tc.flush {
					l.Flush(context.Background())
				}
			}()
			<-entered
			l.Close()
			<-flushed
			l.Flush(context.Background())
			assert.Equal(t, resolve.Stats{Pending: 1, Asked: 1, New: 1}, l.Stats())
		})
	}
}
