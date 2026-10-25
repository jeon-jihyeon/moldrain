package moldrain_test

import (
	"math"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jeon-jihyeon/moldrain"
)

func TestCountsTrustedClock(t *testing.T) {
	t.Parallel()
	type args struct {
		offsets      []time.Duration
		beforeEpoch  bool
		defaultClock bool
	}
	type want struct {
		count  uint64
		future uint64
		late   uint64
	}
	tcs := []struct {
		name string
		args args
		want want
	}{
		{name: "future cannot erase normal counts", args: args{offsets: []time.Duration{0, 48 * time.Hour, 0}},
			want: want{count: 2, future: 1}},
		{name: "future first cannot set retention", args: args{offsets: []time.Duration{48 * time.Hour, 0}},
			want: want{count: 1, future: 1}},
		{name: "accepted future cannot evict oldest", args: args{offsets: []time.Duration{-time.Minute, time.Minute, 0}},
			want: want{count: 3}},
		{name: "out of order inside retention", args: args{offsets: []time.Duration{0, -time.Minute, 0}},
			want: want{count: 3}},
		{name: "too old is visible", args: args{offsets: []time.Duration{0, -2 * time.Minute, 0}},
			want: want{count: 2, late: 1}},
		{name: "before Unix epoch", args: args{offsets: []time.Duration{0, -time.Minute, 0}, beforeEpoch: true},
			want: want{count: 3}},
		{name: "default clock protects live counts",
			args: args{offsets: []time.Duration{0, 48 * time.Hour, 0}, defaultClock: true},
			want: want{count: 2, future: 1}},
	}
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			at := time.Date(2026, 1, 2, 3, 4, 0, 0, time.UTC)
			if tc.args.beforeEpoch {
				at = time.Date(1969, 12, 31, 23, 59, 0, 0, time.UTC)
			}
			var clock func() time.Time
			if tc.args.defaultClock {
				at = time.Now().Truncate(time.Minute)
			} else {
				clock = func() time.Time { return at }
			}
			m, err := moldrain.New(moldrain.Options{Bucket: time.Minute, KeepBuckets: 2,
				CountClock: clock, MatchAfterTrain: true})
			require.NoError(t, err)
			for _, offset := range tc.args.offsets {
				m.Train([]byte("acme request 200"), at.Add(offset))
			}
			var total uint64
			for _, count := range m.Counts(at.Add(-time.Minute), at.Add(2*time.Minute)) {
				total += count.N
			}
			assert.Equal(t, tc.want.count, total)
			assert.Equal(t, uint64(len(tc.args.offsets)), m.Stats().Lines)
			assert.Equal(t, tc.want.future, m.Stats().CountFuture)
			assert.Equal(t, tc.want.late, m.Stats().CountLate)
			assert.Empty(t, m.Counts(at.Add(30*time.Second), at.Add(30*time.Second)))
			assert.Empty(t, m.Counts(at.Add(30*time.Second), at))
		})
	}
}

func TestCountsNanosecondBoundary(t *testing.T) {
	t.Parallel()
	tcs := []struct {
		name string
		at   int64
	}{
		{name: "lower boundary", at: math.MinInt64},
		{name: "upper boundary", at: math.MaxInt64 - 1},
	}
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			at := time.Unix(0, tc.at)
			m, err := moldrain.New(moldrain.Options{Bucket: time.Nanosecond, KeepBuckets: 2,
				CountClock: func() time.Time { return at.Add(time.Nanosecond) }, MatchAfterTrain: true})
			require.NoError(t, err)
			m.Train([]byte("acme"), at)
			m.Train([]byte("acme"), at.Add(time.Nanosecond))
			counts := m.Counts(at, at.Add(2*time.Nanosecond))
			require.Len(t, counts, 1)
			assert.Equal(t, uint64(2), counts[0].N)
			assert.Zero(t, m.Stats().CountLate)
		})
	}
}

func TestCountsOutsideNanosecondRange(t *testing.T) {
	t.Parallel()
	type want struct {
		late   uint64
		future uint64
	}
	tcs := []struct {
		name string
		year int
		want want
	}{
		{name: "ancient time cannot advance retention", year: 1000, want: want{late: 1}},
		{name: "remote future cannot advance retention", year: 9999, want: want{future: 1}},
	}
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			now := time.Date(2026, 1, 2, 3, 4, 0, 0, time.UTC)
			at := now.Add(-2 * time.Minute)
			m, err := moldrain.New(moldrain.Options{Bucket: time.Minute, KeepBuckets: 2,
				CountClock: func() time.Time { return now }, MatchAfterTrain: true})
			require.NoError(t, err)
			for _, stamp := range []time.Time{at, time.Date(tc.year, 1, 1, 0, 0, 0, 0, time.UTC), at} {
				m.Train([]byte("acme"), stamp)
			}
			counts := m.Counts(at, at.Add(time.Minute))
			require.Len(t, counts, 1)
			assert.Equal(t, uint64(2), counts[0].N)
			assert.Equal(t, tc.want.late, m.Stats().CountLate)
			assert.Equal(t, tc.want.future, m.Stats().CountFuture)
		})
	}
}
