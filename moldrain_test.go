package moldrain_test

import (
	"errors"
	"fmt"
	"iter"
	"math/rand/v2"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jeon-jihyeon/moldrain"
)

var t0 = time.Date(2026, 1, 2, 3, 4, 0, 0, time.UTC)

func newMiner(t testing.TB, opt moldrain.Options) *moldrain.Miner {
	t.Helper()
	m, err := moldrain.New(opt)
	require.NoError(t, err)
	return m
}

func TestNewRefusesOptions(t *testing.T) {
	tcs := []struct {
		name string
		opt  moldrain.Options
		want string
	}{
		{"similarity above 1", moldrain.Options{SimTh: 1.5}, "SimTh"},
		{"negative similarity", moldrain.Options{SimTh: -0.1}, "SimTh"},
		{"unknown tokenizer", moldrain.Options{Tokenizer: "xml"}, "Tokenizer"},
		{"one child per node", moldrain.Options{MaxChildren: 1}, "MaxChildren"},
		{"negative cap", moldrain.Options{MaxClusters: -1}, "MaxClusters"},
		{"negative bucket", moldrain.Options{Bucket: -time.Second}, "Bucket"},
	}
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			_, err := moldrain.New(tc.opt)
			assert.ErrorIs(t, err, moldrain.ErrOptions)
			assert.ErrorContains(t, err, tc.want)
		})
	}
}

func TestTrainThenMatch(t *testing.T) {
	type step struct {
		train    bool
		line     string
		ok       bool
		template string
		params   []string
		event    moldrain.EventKind
	}
	tcs := []struct {
		name  string
		opt   moldrain.Options
		steps []step
	}{
		{"match finds what train learned", moldrain.Options{MatchAfterTrain: true}, []step{
			{true, "login ok for alice from 192.0.2.1", true, "login ok for alice from <*>", []string{"192.0.2.1"}, moldrain.Created},
			{true, "login ok for bob from 192.0.2.2", true, "login ok for <*> from <*>", []string{"bob", "192.0.2.2"}, moldrain.Changed},
			{false, "login ok for carol from 192.0.2.99", true, "login ok for <*> from <*>", []string{"carol", "192.0.2.99"}, moldrain.Matched},
			{false, "something else entirely here", false, "", nil, 0},
		}},
		{"match without a publish sees nothing", moldrain.Options{PublishEvery: time.Hour}, []step{
			{true, "disk full", true, "disk full", nil, moldrain.Created},
			{false, "disk full", false, "", nil, 0},
		}},
		{"json keys stay constant", moldrain.Options{Tokenizer: moldrain.JSON, MatchAfterTrain: true}, []step{
			{true, `{"msg":"saved","id":17}`, true, `{"msg":"saved","id":<*>}`, []string{"17"}, moldrain.Created},
			{false, `{"msg":"saved","id":99}`, true, `{"msg":"saved","id":<*>}`, []string{"99"}, moldrain.Matched},
		}},
	}
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			m := newMiner(t, tc.opt)
			for i, s := range tc.steps {
				r, ok := run(m, s.train, s.line)
				require.Equal(t, s.ok, ok, "step %d", i)
				assert.Equal(t, s.template, r.Template, "step %d", i)
				assert.Equal(t, s.event, r.Event, "step %d", i)
				var params []string
				for _, p := range r.Params {
					params = append(params, string(p))
				}
				assert.Equal(t, s.params, params, "step %d", i)
			}
		})
	}
}

// Match allocates the parameters once at their size and nothing for a template without variables
func TestMatchAllocs(t *testing.T) {
	tcs := []struct {
		name  string
		lines []string
		want  float64
	}{
		{"no variables", []string{"disk is full"}, 0},
		{"two variables", []string{"user alice from 192.0.2.1", "user bob from 192.0.2.2"}, 1},
		{"five variables", []string{"a 1 b 2 c 3 d 4 e 5", "a 6 b 7 c 8 d 9 e 10"}, 1},
	}
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			m := newMiner(t, moldrain.Options{MatchAfterTrain: true})
			for _, l := range tc.lines {
				m.Train([]byte(l), t0)
			}
			line := []byte(tc.lines[0])
			_, ok := m.Match(line)
			require.True(t, ok)
			assert.Equal(t, tc.want, testing.AllocsPerRun(100, func() { m.Match(line) }))
		})
	}
}

// Trains the line or matches it
// A trained line is always ok
func run(m *moldrain.Miner, train bool, line string) (moldrain.Result, bool) {
	if train {
		return m.Train([]byte(line), t0), true
	}
	return m.Match([]byte(line))
}

func TestTruncated(t *testing.T) {
	m := newMiner(t, moldrain.Options{MaxLineBytes: 8})
	r := m.Train([]byte("abcd efgh ijkl"), t0)
	assert.Equal(t, "abcd efg", r.Template)
	assert.Equal(t, uint64(1), m.Stats().Truncated)
}

// Counts over a window equal the lines trained in it for a generated stream
func TestCounts(t *testing.T) {
	m := newMiner(t, moldrain.Options{Bucket: time.Minute, KeepBuckets: 60})
	r := rand.New(rand.NewPCG(3, 4))
	want := map[int]map[moldrain.ID]uint64{}
	for i := 0; i < 5000; i++ {
		at := t0.Add(time.Duration(i) * 500 * time.Millisecond)
		line := fmt.Sprintf("event %d happened to item%d", r.IntN(1000), r.IntN(4))
		res := m.Train([]byte(line), at)
		minute := int(at.Sub(t0) / time.Minute)
		if want[minute] == nil {
			want[minute] = map[moldrain.ID]uint64{}
		}
		want[minute][res.ID]++
	}
	// windows of 1 to 5 minutes
	for start := 0; start < 40; start += 7 {
		for width := 1; width <= 5; width++ {
			from, to := t0.Add(time.Duration(start)*time.Minute), t0.Add(time.Duration(start+width)*time.Minute)
			var total uint64
			for _, c := range m.Counts(from, to) {
				total += c.N
			}
			var expected uint64
			for mnt := start; mnt < start+width; mnt++ {
				for _, n := range want[mnt] {
					expected += n
				}
			}
			assert.Equal(t, expected, total, "window %d+%d", start, width)
		}
	}
	// every counted id is a template the miner still holds
	held := map[moldrain.ID]bool{}
	m.Publish()
	for _, c := range m.Clusters() {
		held[c.ID] = true
	}
	for _, c := range m.Counts(t0, t0.Add(time.Hour)) {
		assert.True(t, held[c.ID], "count for dropped id %s", c.ID)
	}
}

func TestCountsDropOldBuckets(t *testing.T) {
	m := newMiner(t, moldrain.Options{Bucket: time.Minute, KeepBuckets: 2})
	m.Train([]byte("a"), t0)
	m.Train([]byte("a"), t0.Add(time.Minute))
	m.Train([]byte("a"), t0.Add(2*time.Minute))
	m.Train([]byte("a"), t0)
	got := m.Counts(t0, t0.Add(3*time.Minute))
	require.Len(t, got, 1)
	assert.Equal(t, uint64(2), got[0].N)
}

func TestEviction(t *testing.T) {
	m := newMiner(t, moldrain.Options{MaxClusters: 3, MatchAfterTrain: true})
	var evicted []moldrain.ID
	m.Subscribe(func(ev moldrain.Event) {
		if ev.Kind == moldrain.Evicted {
			evicted = append(evicted, ev.ID)
		}
	})
	a := m.Train([]byte("alpha"), t0)
	b := m.Train([]byte("beta one"), t0)
	c := m.Train([]byte("gamma one two"), t0)
	m.Train([]byte("alpha"), t0)
	m.Train([]byte("gamma one two"), t0)
	d := m.Train([]byte("delta one two three"), t0)
	assert.Equal(t, []moldrain.ID{b.ID}, evicted)
	assert.Equal(t, 3, m.Stats().Clusters)
	ids := map[moldrain.ID]bool{}
	for _, cl := range m.Clusters() {
		ids[cl.ID] = true
	}
	assert.Equal(t, map[moldrain.ID]bool{a.ID: true, c.ID: true, d.ID: true}, ids)
	// the evicted template keeps its counts until its bucket drops
	var n uint64
	for _, cnt := range m.Counts(t0, t0.Add(time.Minute)) {
		if cnt.ID == b.ID {
			n = cnt.N
		}
	}
	assert.Equal(t, uint64(1), n)
}

func TestChangedMovesCounts(t *testing.T) {
	m := newMiner(t, moldrain.Options{})
	var changes []moldrain.Event
	m.Subscribe(func(ev moldrain.Event) {
		if ev.Kind == moldrain.Changed {
			changes = append(changes, ev)
		}
	})
	first := m.Train([]byte("open file a"), t0)
	second := m.Train([]byte("open file b"), t0)
	require.Len(t, changes, 1)
	assert.Equal(t, first.ID, changes[0].From)
	assert.Equal(t, second.ID, changes[0].To)
	assert.Equal(t, []moldrain.Count{{ID: second.ID, N: 2}}, m.Counts(t0, t0.Add(time.Minute)))
}

func TestAlias(t *testing.T) {
	m := newMiner(t, moldrain.Options{MatchAfterTrain: true})
	m.Train([]byte("disk full on sda"), t0)
	m.Train([]byte("disk full on sdb"), t0)
	old, _ := m.Match([]byte("disk full on sda"))
	prov := m.Train([]byte("no space left on device sdc"), t0)
	near := m.Nearest([]byte("no space left on device sdc"), 3, prov.ID)
	assert.Empty(t, near)
	require.NoError(t, m.Alias(prov.ID, old.ID))
	assert.ErrorIs(t, m.Alias(prov.ID, old.ID), moldrain.ErrAliased)
	assert.ErrorIs(t, m.Alias(old.ID, prov.ID), moldrain.ErrNotOlder)
	r := m.Train([]byte("no space left on device sdd"), t0)
	assert.Equal(t, old.ID, r.ID)
	got, ok := m.Match([]byte("no space left on device sde"))
	require.True(t, ok)
	assert.Equal(t, old.ID, got.ID)
	assert.Equal(t, []moldrain.Count{{ID: old.ID, N: 4}}, m.Counts(t0, t0.Add(time.Minute)))
}

// The published view carries the kept values and an alias whose target shares its bucket
func TestViewValuesAndAlias(t *testing.T) {
	m := newMiner(t, moldrain.Options{KeepValues: 2, MatchAfterTrain: true})
	full := m.Train([]byte("disk full on sda"), t0)
	for _, l := range []string{"disk full on sdb", "disk full on sdc", "disk full on sdd"} {
		full = m.Train([]byte(l), t0)
	}
	gone := m.Train([]byte("disk gone off sdx"), t0)
	require.Equal(t, moldrain.Created, gone.Event)
	require.NoError(t, m.Alias(gone.ID, full.ID))

	got, ok := m.Match([]byte("disk gone off sdx"))
	require.True(t, ok)
	assert.Equal(t, full.ID, got.ID)
	assert.Equal(t, gone.ID, got.Alias)
	cs := m.Clusters()
	require.Len(t, cs, 1)
	assert.Equal(t, [][]string{nil, nil, nil, {"sdc", "sdd"}}, cs[0].Params)
	cs[0].Params[3][0] = "changed"
	assert.Equal(t, []string{"sdc", "sdd"}, m.Clusters()[0].Params[3])
}

// 8 goroutines match while one trains
// Run under -race
func TestConcurrentMatchAndTrain(t *testing.T) {
	m := newMiner(t, moldrain.Options{PublishEvery: time.Millisecond, PublishLines: 50})
	lines := genLines(2000)
	for _, l := range lines[:200] {
		m.Train([]byte(l), t0)
	}
	m.Publish()
	var wg sync.WaitGroup
	stop := make(chan struct{})
	for g := 0; g < 8; g++ {
		wg.Add(1)
		go func(g int) {
			defer wg.Done()
			for i := g; ; i++ {
				select {
				case <-stop:
					return
				default:
				}
				r, ok := m.Match([]byte(lines[i%len(lines)]))
				if ok && r.Template == "" {
					t.Errorf("empty template for %q", lines[i%len(lines)])
				}
				m.Clusters()
				m.Counts(t0, t0.Add(time.Hour))
			}
		}(g)
	}
	for _, l := range lines {
		m.Train([]byte(l), t0)
	}
	close(stop)
	wg.Wait()
	m.Publish()
	var total uint64
	for _, c := range m.Clusters() {
		total += c.Count
	}
	assert.Equal(t, uint64(2200), total)
	assert.Equal(t, uint64(2200), m.Stats().Lines)
}

func genLines(n int) []string {
	r := rand.New(rand.NewPCG(7, 8))
	formats := []string{
		"user %s logged in from 2001:db8::%x:%x",
		"request %x took %d ms",
		"disk %s usage %d percent",
		"job %s finished with %d items",
	}
	words := []string{"alice", "bob", "sda", "pay", "ship"}
	out := make([]string, n)
	for i := range out {
		switch f := formats[r.IntN(len(formats))]; f {
		case formats[0]:
			out[i] = fmt.Sprintf(f, words[r.IntN(len(words))], r.IntN(256), r.IntN(256))
		case formats[1]:
			out[i] = fmt.Sprintf(f, r.Uint32(), r.IntN(900))
		default:
			out[i] = fmt.Sprintf(f, words[r.IntN(len(words))], r.IntN(100))
		}
	}
	return out
}

// A template a Respecialize split adds on a Matched line counts against the cap too
func TestEvictionAfterSplit(t *testing.T) {
	m := newMiner(t, moldrain.Options{MaxClusters: 2, Respecialize: true, SplitMin: 10})
	for i := 0; i < 30; i++ {
		m.Train([]byte("state changed to "+[]string{"up", "down"}[i%2]), t0)
	}
	st := m.Stats()
	assert.Equal(t, 2, st.Clusters)
	assert.Positive(t, st.Evicted)
}

// An alias left in the recency heap is passed over
// Its target's line count decides
func TestEvictionSkipsAliases(t *testing.T) {
	m := newMiner(t, moldrain.Options{MaxClusters: 2, MatchAfterTrain: true})
	var evicted []moldrain.ID
	m.Subscribe(func(ev moldrain.Event) {
		if ev.Kind == moldrain.Evicted {
			evicted = append(evicted, ev.ID)
		}
	})
	a := m.Train([]byte("alpha"), t0)
	b := m.Train([]byte("beta one"), t0)
	require.NoError(t, m.Alias(b.ID, a.ID))
	c := m.Train([]byte("gamma one two"), t0)
	m.Train([]byte("alpha"), t0)
	m.Train([]byte("delta one two three"), t0)
	assert.Equal(t, []moldrain.ID{c.ID}, evicted)
	got, ok := m.Match([]byte("beta one"))
	require.True(t, ok)
	assert.Equal(t, a.ID, got.ID)
	assert.Equal(t, b.ID, got.Alias)
}

func TestRestore(t *testing.T) {
	src := newMiner(t, moldrain.Options{})
	var events []moldrain.Event
	src.Subscribe(func(ev moldrain.Event) {
		ev.Line = nil
		events = append(events, ev)
	})
	lines := genLines(300)
	for _, l := range lines[:150] {
		src.Train([]byte(l), t0)
	}
	var st moldrain.State
	src.Snapshot(func(s moldrain.State) { st = s })
	half := len(events)
	for _, l := range lines[150:] {
		src.Train([]byte(l), t0)
	}
	all := src.Stats().Clusters
	broken := errors.New("log unreadable")
	replay := func(evs []moldrain.Event, fail error) iter.Seq2[moldrain.Event, error] {
		return func(yield func(moldrain.Event, error) bool) {
			for _, ev := range evs {
				if !yield(ev, nil) {
					return
				}
			}
			if fail != nil {
				yield(moldrain.Event{}, fail)
			}
		}
	}
	bad := st
	bad.Clusters = slices.Clone(st.Clusters)
	bad.Clusters[0].ID++
	// a template the state already holds cannot be created again
	again := moldrain.Event{Kind: moldrain.Created, ID: st.Clusters[0].ID, Shape: &st.Clusters[0].Shape, N: 1, At: t0}
	type args struct {
		opt     moldrain.Options
		st      *moldrain.State
		changes iter.Seq2[moldrain.Event, error]
	}
	type want struct {
		err error
		// templates after the first Restore
		clusters int
		// the error of a second Restore from the state and the changes after it
		retry error
		// templates after the second Restore
		final int
	}
	tcs := []struct {
		name string
		args args
		want want
	}{
		{"a replay restores every template", args{moldrain.Options{}, nil, replay(events, nil)}, want{nil, all, moldrain.ErrNotEmpty, all}},
		{"a state and the changes after it", args{moldrain.Options{}, &st, replay(events[half:], nil)}, want{nil, all, moldrain.ErrNotEmpty, all}},
		{"a failed read leaves the miner empty for a retry", args{moldrain.Options{}, nil, replay(events[:half], broken)}, want{broken, 0, nil, all}},
		{"a state that is no tree", args{moldrain.Options{}, &bad, nil}, want{moldrain.ErrState, 0, nil, all}},
		{"a change that diverges from the state", args{moldrain.Options{}, &st, replay([]moldrain.Event{again}, nil)}, want{moldrain.ErrDiverged, 0, nil, all}},
		{"templates past the cap are evicted", args{moldrain.Options{MaxClusters: 3}, nil, replay(events, nil)}, want{nil, 3, moldrain.ErrNotEmpty, 3}},
	}
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			m := newMiner(t, tc.args.opt)
			assert.ErrorIs(t, m.Restore(tc.args.st, tc.args.changes), tc.want.err)
			assert.Equal(t, tc.want.clusters, m.Stats().Clusters)
			assert.ErrorIs(t, m.Restore(&st, replay(events[half:], nil)), tc.want.retry)
			assert.Equal(t, tc.want.final, m.Stats().Clusters)
		})
	}
}

func TestOptionsHash(t *testing.T) {
	tcs := []struct {
		name string
		a, b moldrain.Options
		same bool
	}{
		{"defaults spelled out", moldrain.Options{}, moldrain.Options{SimTh: 0.4, Depth: 2, MaxChildren: 100}, true},
		{"display, publishing, the cap and counts", moldrain.Options{}, moldrain.Options{
			KindPlaceholders: true, PublishEvery: time.Second, MaxClusters: 9, Bucket: time.Hour, KeepValues: 3,
		}, true},
		{"another similarity", moldrain.Options{}, moldrain.Options{SimTh: 0.5}, false},
		{"units joined by a comma", moldrain.Options{Units: []string{"a,b"}}, moldrain.Options{Units: []string{"a", "b"}}, false},
		{"no units against the default units", moldrain.Options{Units: []string{}}, moldrain.Options{}, false},
	}
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tc.same, tc.a.Hash() == tc.b.Hash())
			m := newMiner(t, tc.a)
			assert.Equal(t, tc.a.Hash(), m.Fingerprint())
		})
	}
}
