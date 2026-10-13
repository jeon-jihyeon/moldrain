package assay_test

import (
	"encoding/json"
	"fmt"
	"math/rand/v2"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jeon-jihyeon/moldrain"
	"github.com/jeon-jihyeon/moldrain/assay"
)

// Aligned to every window width the tests use
var t0 = time.Date(2026, 1, 2, 3, 0, 0, 0, time.UTC)

type rig struct {
	m    *moldrain.Miner
	a    *assay.Assay
	mu   sync.Mutex
	recs []assay.Record
}

func newRig(t testing.TB, m *moldrain.Miner, ps []assay.Profile, opt assay.Options) *rig {
	t.Helper()
	a, err := assay.New(m, ps, opt)
	require.NoError(t, err)
	r := &rig{m: m, a: a}
	t.Cleanup(a.Close)
	a.OnRecord(func(rec assay.Record) {
		r.mu.Lock()
		r.recs = append(r.recs, rec)
		r.mu.Unlock()
	})
	return r
}

func newMiner(t testing.TB, opt moldrain.Options) *moldrain.Miner {
	t.Helper()
	m, err := moldrain.New(opt)
	require.NoError(t, err)
	return m
}

func (r *rig) train(line string, at time.Time) moldrain.Result {
	res := r.m.Train([]byte(line), at)
	r.a.Observe(res, at)
	return res
}

func (r *rig) records() []assay.Record {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]assay.Record(nil), r.recs...)
}

// Values by profile and op of every record in the group
func (r *rig) values(group string) map[string]map[assay.Op]float64 {
	out := map[string]map[assay.Op]float64{}
	for _, rec := range r.records() {
		if rec.Group != group {
			continue
		}
		if out[rec.Profile] == nil {
			out[rec.Profile] = map[assay.Op]float64{}
		}
		out[rec.Profile][rec.Op] += rec.Value
	}
	return out
}

func TestNewRefuses(t *testing.T) {
	m := newMiner(t, moldrain.Options{})
	count := []assay.Op{assay.Count}
	tcs := []struct {
		name     string
		miner    *moldrain.Miner
		profiles []assay.Profile
		opt      assay.Options
		err      error
		want     string
	}{
		{"nil miner", nil, nil, assay.Options{}, assay.ErrOptions, "miner"},
		{"negative window", m, nil, assay.Options{Window: -time.Second}, assay.ErrOptions, "Window"},
		{"negative lateness", m, nil, assay.Options{Lateness: -time.Second}, assay.ErrOptions, "Lateness"},
		{"negative distinct", m, nil, assay.Options{MaxDistinct: -1}, assay.ErrOptions, "MaxDistinct"},
		{"negative unbound", m, nil, assay.Options{UnboundWindows: -1}, assay.ErrOptions, "UnboundWindows"},
		{"bad profile", m, []assay.Profile{{Match: "x", Ops: count}}, assay.Options{}, assay.ErrProfile, "Name"},
	}
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			_, err := assay.New(tc.miner, tc.profiles, tc.opt)
			assert.ErrorIs(t, err, tc.err)
			assert.ErrorContains(t, err, tc.want)
		})
	}
}

// The bindings follow a template that generalizes and an alias that joins it
// A pattern that fits only after a change binds then
func TestBindingFollowsChanges(t *testing.T) {
	m := newMiner(t, moldrain.Options{})
	op := m.Train([]byte("op took 120ms"), t0)
	r := newRig(t, m, []assay.Profile{
		{Name: "pay", Match: "payment failed for * code <*>", Value: &assay.Locator{After: "code"},
			Ops: []assay.Op{assay.Count, assay.Sum}},
		{Name: "disk", Match: "disk full on <*>", Ops: []assay.Op{assay.Count}},
		{Name: "op", IDs: []string{op.ID.String()}, Value: &assay.Locator{Slot: 0}, Unit: assay.Milliseconds,
			Ops: []assay.Op{assay.Count, assay.Sum}},
	}, assay.Options{})
	want := map[string]map[assay.Op]float64{
		"pay":  {assay.Count: 0, assay.Sum: 0},
		"disk": {assay.Count: 0},
		"op":   {assay.Count: 0, assay.Sum: 0},
	}
	rng := rand.New(rand.NewPCG(1, 2))
	var pay, declined moldrain.ID
	aliased := false
	for i := 0; i < 3000; i++ {
		code := rng.IntN(500)
		switch rng.IntN(4) {
		case 0:
			pay = r.train(fmt.Sprintf("payment failed for user%c code %d", 'a'+rune(rng.IntN(20)), code), t0).ID
			want["pay"][assay.Count]++
			want["pay"][assay.Sum] += float64(code)
		case 1:
			if pay == 0 {
				continue
			}
			declined = r.train(fmt.Sprintf("payment was declined for bob code %d", code), t0).ID
			if aliased {
				want["pay"][assay.Count]++
				want["pay"][assay.Sum] += float64(code)
				continue
			}
			require.NoError(t, m.Alias(declined, pay))
			aliased = true
		case 2:
			res := r.train(fmt.Sprintf("disk full on sd%c", 'a'+rune(rng.IntN(20))), t0)
			want["disk"][assay.Count] += map[bool]float64{true: 1}[res.Template == "disk full on <*>"]
		case 3:
			r.train(fmt.Sprintf("op took %dms", code), t0)
			want["op"][assay.Count]++
			want["op"][assay.Sum] += float64(code)
		}
	}
	r.train("op took bad", t0)
	want["op"][assay.Count]++
	r.a.Flush(t0.Add(time.Hour))

	assert.True(t, aliased)
	assert.Equal(t, want, r.values(""))
	st := r.a.Stats()
	require.Len(t, st.Profiles, 3)
	assert.Equal(t, []moldrain.ID{pay}, st.Profiles[0].Bound)
	assert.Equal(t, uint64(1), st.Profiles[2].Skipped)
	assert.Empty(t, st.Unbound)
}

// A restored miner binds again from its templates and their aliases
func TestNewRebinds(t *testing.T) {
	m := newMiner(t, moldrain.Options{})
	m.Train([]byte("payment failed for alice code 1"), t0)
	pay := m.Train([]byte("payment failed for bob code 2"), t0)
	declined := m.Train([]byte("payment was declined for carol code 3"), t0)
	require.NoError(t, m.Alias(declined.ID, pay.ID))
	m.Publish()
	r := newRig(t, m, []assay.Profile{
		{Name: "pay", Match: "payment failed for <*> code <*>", Value: &assay.Locator{After: "code"},
			Ops: []assay.Op{assay.Sum}},
	}, assay.Options{})
	r.train("payment failed for dave code 10", t0)
	r.train("payment was declined for erin code 20", t0)
	r.a.Flush(t0.Add(time.Hour))
	assert.Equal(t, map[string]map[assay.Op]float64{"pay": {assay.Sum: 30}}, r.values(""))
	assert.Equal(t, []moldrain.ID{pay.ID}, r.a.Stats().Profiles[0].Bound)
}

func TestStatsBindings(t *testing.T) {
	type want struct {
		bound   int
		noValue int
		unbound []string
	}
	tcs := []struct {
		name    string
		profile assay.Profile
		opt     moldrain.Options
		lines   []string
		want    want
	}{
		{
			name:    "pattern without the value slot",
			profile: assay.Profile{Name: "p", Match: "disk full on <*>", Value: &assay.Locator{Slot: 3}, Ops: []assay.Op{assay.Sum}},
			lines:   []string{"disk full on sda", "disk full on sdb"},
			want:    want{noValue: 1, unbound: []string{"p"}},
		},
		{
			name: "pattern without the group slot",
			profile: assay.Profile{Name: "p", Match: "disk full on <*>", GroupBy: &assay.Locator{After: "at"},
				Ops: []assay.Op{assay.Count}},
			lines: []string{"disk full on sda", "disk full on sdb"},
			want:  want{noValue: 1, unbound: []string{"p"}},
		},
		{
			name:    "pattern that fits nothing",
			profile: assay.Profile{Name: "p", Match: "nothing like it", Ops: []assay.Op{assay.Count}},
			lines:   []string{"disk full on sda"},
			want:    want{unbound: []string{"p"}},
		},
		{
			name:    "evicted template",
			profile: assay.Profile{Name: "p", Match: "disk full on *", Ops: []assay.Op{assay.Count}},
			opt:     moldrain.Options{MaxClusters: 1},
			lines:   []string{"disk full on sda", "disk full on sdb", "something else entirely here"},
			want:    want{unbound: []string{"p"}},
		},
		{
			name:    "star that fits nothing",
			profile: assay.Profile{Name: "p", Match: "disk full * gone", Ops: []assay.Op{assay.Count}},
			lines:   []string{"disk full on sda"},
			want:    want{unbound: []string{"p"}},
		},
		{
			name:    "after key that only a variable precedes",
			profile: assay.Profile{Name: "p", Match: "pair *", Value: &assay.Locator{After: "x"}, Ops: []assay.Op{assay.Sum}},
			lines:   []string{"pair 1 2"},
			want:    want{noValue: 1, unbound: []string{"p"}},
		},
		{
			name:    "bound",
			profile: assay.Profile{Name: "p", Match: "disk full *", Ops: []assay.Op{assay.Count}},
			lines:   []string{"disk full on sda", "disk full on sdb"},
			want:    want{bound: 1},
		},
	}
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			r := newRig(t, newMiner(t, tc.opt), []assay.Profile{tc.profile}, assay.Options{Window: time.Minute, UnboundWindows: 3})
			for _, l := range tc.lines {
				r.train(l, t0)
			}
			r.a.Flush(t0)
			r.a.Flush(t0.Add(5 * time.Minute))
			st := r.a.Stats()
			got := want{bound: len(st.Profiles[0].Bound), noValue: len(st.Profiles[0].NoValue), unbound: st.Unbound}
			assert.Equal(t, tc.want, got)
		})
	}
}

func TestWindows(t *testing.T) {
	type line struct {
		offset time.Duration
		text   string
	}
	type win struct {
		start time.Duration
		lines uint64
	}
	type want struct {
		windows []win
		late    uint64
	}
	before1970 := time.Date(1969, 12, 31, 23, 58, 0, 0, time.UTC)
	tcs := []struct {
		name  string
		base  time.Time
		lines []line
		flush time.Duration
		want  want
	}{
		{
			name: "boundary and late",
			base: t0,
			lines: []line{
				{0, "tick 1"}, {59999 * time.Millisecond, "tick 2"}, {time.Minute, "tick 3"},
				{70 * time.Second, "tick 4"}, {30 * time.Second, "tick 5"}, {69 * time.Second, "tick 6"},
			},
			flush: 130 * time.Second,
			want:  want{windows: []win{{0, 2}, {time.Minute, 3}}, late: 1},
		},
		{
			name:  "line within lateness keeps its window",
			base:  t0,
			lines: []line{{0, "tick 1"}, {65 * time.Second, "tick 2"}, {30 * time.Second, "tick 3"}},
			flush: 3 * time.Minute,
			want:  want{windows: []win{{0, 2}, {time.Minute, 1}}},
		},
		{
			name:  "flush before the lateness ends keeps the window open",
			base:  t0,
			lines: []line{{0, "tick 1"}},
			flush: 65 * time.Second,
		},
		{
			name:  "before 1970",
			base:  before1970,
			lines: []line{{0, "tick 1"}, {90 * time.Second, "tick 2"}},
			flush: 5 * time.Minute,
			want:  want{windows: []win{{0, 1}, {time.Minute, 1}}},
		},
	}
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			r := newRig(t, newMiner(t, moldrain.Options{}),
				[]assay.Profile{{Name: "tick", Match: "tick *", Ops: []assay.Op{assay.Count}}},
				assay.Options{Window: time.Minute, Lateness: 10 * time.Second})
			for _, l := range tc.lines {
				r.train(l.text, tc.base.Add(l.offset))
			}
			r.a.Flush(tc.base.Add(tc.flush))
			var got want
			for _, rec := range r.records() {
				got.windows = append(got.windows, win{rec.Start.Sub(tc.base), rec.Lines})
				assert.Equal(t, time.Minute, rec.End.Sub(rec.Start))
			}
			got.late = r.a.Stats().Profiles[0].Late
			assert.Equal(t, tc.want, got)
		})
	}
}

// Groups past MaxGroups go to one record marked Other
// It stays apart from a real group of any name
func TestGroups(t *testing.T) {
	r := newRig(t, newMiner(t, moldrain.Options{}), []assay.Profile{
		{Name: "calls", Match: "call to <*> took <*>", GroupBy: &assay.Locator{Slot: 0}, Value: &assay.Locator{Slot: 1},
			MaxGroups: 2, Ops: []assay.Op{assay.Count, assay.Max}},
	}, assay.Options{})
	for _, l := range []string{
		"call to a took 1", "call to other took 2", "call to a took 3", "call to c took 4", "call to d took 5", "call to other took 6",
	} {
		r.train(l, t0)
	}
	r.a.Flush(t0.Add(time.Hour))
	type row struct {
		group string
		other bool
		op    assay.Op
		value float64
	}
	var got []row
	for _, rec := range r.records() {
		got = append(got, row{rec.Group, rec.Other, rec.Op, rec.Value})
	}
	// call to a took 1 creates the template with a as a constant and binds nothing
	assert.Equal(t, []row{
		{"a", false, assay.Count, 1}, {"a", false, assay.Max, 3},
		{"other", false, assay.Count, 2}, {"other", false, assay.Max, 6},
		{"", true, assay.Count, 2}, {"", true, assay.Max, 5},
	}, got)
}

func TestRecordJSON(t *testing.T) {
	r := newRig(t, newMiner(t, moldrain.Options{Delimiters: "="}), []assay.Profile{
		{Name: "lat", Match: "done *", Value: &assay.Locator{After: "latency="}, Unit: assay.Milliseconds,
			Ops: []assay.Op{assay.Count, assay.Max, assay.P99, assay.Count}},
	}, assay.Options{})
	id := r.train("done latency=12ms", t0.In(time.FixedZone("KST", 9*3600))).ID
	r.a.Flush(t0.Add(time.Hour))
	var got []string
	for _, rec := range r.records() {
		b, err := json.Marshal(rec)
		require.NoError(t, err)
		got = append(got, string(b))
	}
	assert.Equal(t, []string{
		`{"start":"2026-01-02T03:00:00Z","end":"2026-01-02T03:05:00Z","profile":"lat","op":"count","value":1,` +
			`"lines":1,"skipped":0,"templates":["` + id.String() + `"],"approx":false}`,
		`{"start":"2026-01-02T03:00:00Z","end":"2026-01-02T03:05:00Z","profile":"lat","op":"max","value":12,` +
			`"unit":"ms","lines":1,"skipped":0,"templates":["` + id.String() + `"],"approx":false}`,
		`{"start":"2026-01-02T03:00:00Z","end":"2026-01-02T03:05:00Z","profile":"lat","op":"p99","value":12,` +
			`"unit":"ms","lines":1,"skipped":0,"templates":["` + id.String() + `"],"approx":false}`,
	}, got)
}

// Value ops leave no record for a group that read no value
func TestNoValueNoRecord(t *testing.T) {
	r := newRig(t, newMiner(t, moldrain.Options{}), []assay.Profile{
		{Name: "p", Match: "value is <*>", Value: &assay.Locator{Slot: 0}, Ops: []assay.Op{assay.Count, assay.Mean}},
	}, assay.Options{})
	r.train("value is five", t0)
	r.train("value is six", t0)
	r.a.Flush(t0.Add(time.Hour))
	assert.Equal(t, map[string]map[assay.Op]float64{"p": {assay.Count: 1}}, r.values(""))
	assert.Equal(t, uint64(1), r.records()[0].Skipped)
}

// A moved binding whose value key turned variable counts as skipped
// So does a Result whose slots no form has
func TestValueSlotLost(t *testing.T) {
	r := newRig(t, newMiner(t, moldrain.Options{}), []assay.Profile{
		{Name: "p", Match: "fail now *", Value: &assay.Locator{After: "code"}, Ops: []assay.Op{assay.Count, assay.Sum}},
	}, assay.Options{})
	first := r.train("fail now code 5", t0)
	second := r.train("fail now kode 6", t0)
	r.a.Observe(moldrain.Result{ID: second.ID}, t0)
	r.a.Flush(t0.Add(time.Hour))
	assert.Equal(t, map[string]map[assay.Op]float64{"p": {assay.Count: 3, assay.Sum: 5}}, r.values(""))
	assert.Equal(t, uint64(2), r.a.Stats().Profiles[0].Skipped)
	assert.Equal(t, []string{ids(first.ID, second.ID)}, []string{strings.Join(r.records()[0].Templates, " ")})
}

// An alias between two templates the profile both bound keeps one binding
func TestAliasIntoBoundTemplate(t *testing.T) {
	r := newRig(t, newMiner(t, moldrain.Options{}), []assay.Profile{
		{Name: "all", Match: "*", Ops: []assay.Op{assay.Count}},
	}, assay.Options{})
	to := r.train("alpha one two", t0)
	from := r.train("beta three four five", t0)
	require.NoError(t, r.m.Alias(from.ID, to.ID))
	again := r.train("beta three four five", t0)
	r.a.Flush(t0.Add(time.Hour))
	assert.Equal(t, to.ID, again.ID)
	assert.Equal(t, map[string]map[assay.Op]float64{"all": {assay.Count: 3}}, r.values(""))
	assert.Equal(t, []moldrain.ID{to.ID}, r.a.Stats().Profiles[0].Bound)
}

// An alias keeps its form after 35 changes of its target push its id out of the 32 earlier ids kept
func TestAliasFormOutlivesForwards(t *testing.T) {
	r := newRig(t, newMiner(t, moldrain.Options{}), []assay.Profile{
		{Name: "all", Match: "*", Value: &assay.Locator{After: "val"}, Ops: []assay.Op{assay.Sum}},
	}, assay.Options{})
	words := make([]string, 60)
	for i := range words {
		words[i] = fmt.Sprintf("w%d", i)
	}
	line := func(changed int) string {
		ws := slices.Clone(words)
		if changed >= 0 {
			ws[changed] = fmt.Sprintf("x%d", changed)
		}
		return "head tail " + strings.Join(ws, " ") + " val 1"
	}
	to := r.train(line(-1), t0)
	alias := r.train("alias form val 100", t0)
	require.NoError(t, r.m.Alias(alias.ID, to.ID))
	// each line turns one more word into a variable while the similarity stays above SimTh
	changes := 0
	for i := 0; i < 35; i++ {
		if r.train(line(i), t0).Event == moldrain.Changed {
			changes++
		}
	}
	again := r.train("alias form val 1000", t0)
	r.a.Flush(t0.Add(time.Hour))
	assert.Equal(t, 35, changes)
	assert.Equal(t, alias.ID, again.Alias)
	assert.Equal(t, map[string]map[assay.Op]float64{"all": {assay.Sum: 1136}}, r.values(""))
	assert.Zero(t, r.a.Stats().Profiles[0].Skipped)
}

func ids(xs ...moldrain.ID) string {
	slices.Sort(xs)
	out := make([]string, len(xs))
	for i, x := range xs {
		out[i] = x.String()
	}
	return strings.Join(out, " ")
}

// 8 goroutines observe the results of a trainer while Flush and Stats run beside them
func TestObserveRace(t *testing.T) {
	m := newMiner(t, moldrain.Options{})
	r := newRig(t, m, []assay.Profile{
		{Name: "all", Match: "*", Ops: []assay.Op{assay.Count}},
		{Name: "req", Match: "req <*> took <*>", Value: &assay.Locator{Slot: 1},
			Ops: []assay.Op{assay.Count, assay.Sum, assay.P99, assay.Distinct}},
	}, assay.Options{Window: time.Second, Lateness: time.Hour})
	type obs struct {
		res moldrain.Result
		at  time.Time
	}
	ch := make(chan obs, 256)
	var wg sync.WaitGroup
	for g := 0; g < 8; g++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for o := range ch {
				r.a.Observe(o.res, o.at)
			}
		}()
	}
	done := make(chan struct{})
	go func() {
		defer close(done)
		for i := 0; i < 50; i++ {
			r.a.Flush(t0)
			r.a.Stats()
		}
	}()
	const n = 20000
	for i := 0; i < n; i++ {
		at := t0.Add(time.Duration(i) * time.Millisecond)
		line := fmt.Sprintf("req %d took %d", i%37, i%101)
		if i%3 == 0 {
			line = fmt.Sprintf("other event %d of kind %c", i, 'a'+rune(i%26))
		}
		ch <- obs{m.Train([]byte(line), at), at}
	}
	close(ch)
	wg.Wait()
	<-done
	r.a.Flush(t0.Add(48 * time.Hour))
	got := r.values("")
	assert.Equal(t, float64(n), got["all"][assay.Count])
	assert.Positive(t, got["req"][assay.Count])
}

func TestObserveUnboundAllocatesNothing(t *testing.T) {
	r := newRig(t, newMiner(t, moldrain.Options{}), []assay.Profile{
		{Name: "p", Match: "bound <*>", Ops: []assay.Op{assay.Count}},
	}, assay.Options{})
	res := r.m.Train([]byte("unbound line 1"), t0)
	allocs := testing.AllocsPerRun(1000, func() { r.a.Observe(res, t0) })
	assert.Zero(t, allocs)
}

// A line reads its value from the slots of the form it matched
func TestForms(t *testing.T) {
	tcs := []struct {
		name string
		// trained before the alias with the profile's template first
		lines []string
		// trained after the first line's template took the second's as an alias
		after []string
		want  float64
	}{
		{
			"an alias with the same slot count reads its own slot",
			[]string{"req 1 took 5 ms", "took 100 for 9"},
			[]string{"took 200 for 7"},
			305,
		},
		{
			"the template itself reads its slot",
			[]string{"req 1 took 5 ms", "took 100 for 9"},
			[]string{"req 2 took 6 ms"},
			111,
		},
	}
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			r := newRig(t, newMiner(t, moldrain.Options{}), []assay.Profile{
				{Name: "lat", Match: "req <*> took <*> ms", Value: &assay.Locator{After: "took"}, Ops: []assay.Op{assay.Sum}},
			}, assay.Options{})
			var got []moldrain.ID
			for _, l := range tc.lines {
				got = append(got, r.train(l, t0).ID)
			}
			require.Len(t, got, 2)
			require.NoError(t, r.m.Alias(got[1], got[0]))
			// the line trained before the alias counts once it joins
			r.a.Observe(moldrain.Result{ID: got[0], Alias: got[1], Params: [][]byte{[]byte("100"), []byte("9")}}, t0)
			for _, l := range tc.after {
				r.train(l, t0)
			}
			r.a.Flush(t0.Add(time.Hour))
			assert.Equal(t, map[string]map[assay.Op]float64{"lat": {assay.Sum: tc.want}}, r.values(""))
		})
	}
}

// A line stamped far in the future is dropped as future and closes none of the windows the next lines need
func TestFutureLine(t *testing.T) {
	r := newRig(t, newMiner(t, moldrain.Options{}), []assay.Profile{
		{Name: "tick", Match: "tick *", Ops: []assay.Op{assay.Count}},
	}, assay.Options{Window: time.Minute, Lateness: 10 * time.Second, Clock: func() time.Time { return t0.Add(time.Minute) }})
	r.train("tick 1", t0)
	r.train("tick 2", t0.Add(24*time.Hour))
	r.train("tick 3", t0.Add(time.Second))
	r.a.Flush(t0.Add(time.Hour))
	type win struct {
		start time.Duration
		lines uint64
	}
	var got []win
	for _, rec := range r.records() {
		got = append(got, win{rec.Start.Sub(t0), rec.Lines})
	}
	assert.Equal(t, []win{{0, 2}}, got)
	st := r.a.Stats().Profiles[0]
	assert.Equal(t, []uint64{0, 1}, []uint64{st.Late, st.Future})
}

// A value or a sum past the float range counts as skipped
// Every record encodes as JSON
func TestValueOutOfRange(t *testing.T) {
	r := newRig(t, newMiner(t, moldrain.Options{}), []assay.Profile{
		{Name: "v", Match: "v <*>", Value: &assay.Locator{Slot: 0}, Unit: assay.Milliseconds, Ops: []assay.Op{assay.Sum, assay.Max}},
	}, assay.Options{})
	for _, l := range []string{"v 1e308", "v 1e308", "v 1e305h", "v 1e400"} {
		r.train(l, t0)
	}
	r.a.Flush(t0.Add(time.Hour))
	recs := r.records()
	require.Len(t, recs, 2)
	_, err := json.Marshal(recs)
	require.NoError(t, err)
	assert.Equal(t, 1e308, recs[0].Value)
	assert.Equal(t, uint64(3), recs[0].Skipped)
}

// A sink runs with no profile locked so it may read Stats and add a sink
// While it blocks the lines of every profile go on and close their windows
func TestSlowSink(t *testing.T) {
	m := newMiner(t, moldrain.Options{})
	a, err := assay.New(m, []assay.Profile{
		{Name: "tick", Match: "tick *", Ops: []assay.Op{assay.Count}},
		{Name: "tock", Match: "tock *", Ops: []assay.Op{assay.Count}},
	}, assay.Options{Window: time.Minute, Lateness: time.Second, Clock: func() time.Time { return t0.Add(time.Hour) }})
	require.NoError(t, err)
	entered, release := make(chan struct{}), make(chan struct{})
	var once sync.Once
	a.OnRecord(func(assay.Record) {
		once.Do(func() {
			a.Stats()
			a.OnRecord(func(assay.Record) {})
			close(entered)
			<-release
		})
	})
	observe := func(line string, at time.Time) { a.Observe(m.Train([]byte(line), at), at) }
	observe("tick 1", t0)
	observe("tock 1", t0)
	// closes the window of tick whose record blocks the sink
	observe("tick 2", t0.Add(2*time.Minute))
	<-entered
	done := make(chan struct{})
	go func() {
		defer close(done)
		// closes the window of tock while the sink blocks
		observe("tock 2", t0.Add(2*time.Minute))
		observe("tick 3", t0.Add(2*time.Minute))
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("a slow sink held up the lines of the profiles")
	}
	st := a.Stats()
	assert.Equal(t, []uint64{3, 2}, []uint64{st.Profiles[0].Observed, st.Profiles[1].Observed})
	close(release)
	a.Close()
}

// After Close the assay neither binds nor counts
func TestClose(t *testing.T) {
	r := newRig(t, newMiner(t, moldrain.Options{}), []assay.Profile{{Name: "all", Match: "*", Ops: []assay.Op{assay.Count}}}, assay.Options{})
	r.train("alpha beta", t0)
	r.a.Close()
	r.train("gamma delta epsilon", t0)
	r.a.Flush(t0.Add(time.Hour))
	assert.Empty(t, r.records())
	st := r.a.Stats()
	require.Len(t, st.Profiles, 1)
	assert.Equal(t, uint64(1), st.Profiles[0].Observed)
	assert.Len(t, st.Profiles[0].Bound, 1)
}
