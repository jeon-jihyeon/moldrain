package assay_test

import (
	"fmt"
	"math"
	"math/rand/v2"
	"slices"
	"strconv"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jeon-jihyeon/moldrain"
	"github.com/jeon-jihyeon/moldrain/assay"
)

// One latency written the way a log might with the value it stands for in ms
type sample struct {
	text string
	ms   float64
}

func latencies(n int) []sample {
	rng := rand.New(rand.NewPCG(7, 11))
	out := make([]sample, n)
	for i := range out {
		v := math.Exp(rng.NormFloat64()*1.5) * 80
		switch i % 4 {
		case 0:
			out[i] = sample{fmt.Sprintf("%dms", int(v)+1), float64(int(v) + 1)}
		case 1:
			s := strconv.FormatFloat(v, 'f', 2, 64)
			f, _ := strconv.ParseFloat(s, 64)
			out[i] = sample{s + "ms", f}
		case 2:
			s := strconv.FormatFloat(v/1000, 'f', 4, 64)
			f, _ := strconv.ParseFloat(s, 64)
			out[i] = sample{s + "s", f * 1000}
		default:
			out[i] = sample{strconv.Itoa(int(v) + 1), float64(int(v) + 1)}
		}
	}
	return out
}

// The value at rank q times n-1 rounded down of the sorted values
// The sketch answers for the same rank
func naiveQuantile(sorted []float64, q float64) float64 {
	return sorted[int(q*float64(len(sorted)-1))]
}

// Every op against a computation over the same values
// Percentiles within 1% relative error
func TestOpsMatchNaive(t *testing.T) {
	m := newMiner(t, moldrain.Options{Delimiters: "="})
	m.Train([]byte("req done latency=1ms user=a"), t0)
	m.Train([]byte("req done latency=2ms user=b"), t0)
	r := newRig(t, m, []assay.Profile{
		{Name: "lat", Match: "req done *", Value: &assay.Locator{After: "latency="}, Unit: assay.Milliseconds,
			Ops: []assay.Op{assay.Count, assay.Sum, assay.Min, assay.Max, assay.Mean, assay.Distinct,
				assay.P50, assay.P90, assay.P95, assay.P99}},
		{Name: "users", Match: "req done *", Value: &assay.Locator{After: "user"}, Ops: []assay.Op{assay.Distinct}},
		{Name: "lines", Match: "req done *", Ops: []assay.Op{assay.Distinct}},
	}, assay.Options{})
	samples := latencies(5000)
	values := make([]float64, len(samples))
	texts, users, lines := map[string]bool{}, map[string]bool{}, map[string]bool{}
	sum := 0.0
	for i, s := range samples {
		user := fmt.Sprintf("user%d", i%731)
		line := fmt.Sprintf("req done latency=%s user=%s", s.text, user)
		r.train(line, t0)
		values[i] = s.ms
		sum += s.ms
		texts[s.text], users[user], lines[line] = true, true, true
	}
	r.a.Flush(t0.Add(time.Hour))
	sorted := slices.Clone(values)
	slices.Sort(sorted)
	got := r.values("")

	tcs := []struct {
		profile string
		op      assay.Op
		want    float64
		epsilon float64
	}{
		{"lat", assay.Count, float64(len(values)), 1e-12},
		{"lat", assay.Sum, sum, 1e-12},
		{"lat", assay.Min, sorted[0], 1e-12},
		{"lat", assay.Max, sorted[len(sorted)-1], 1e-12},
		{"lat", assay.Mean, sum / float64(len(values)), 1e-12},
		{"lat", assay.Distinct, float64(len(texts)), 1e-12},
		{"lat", assay.P50, naiveQuantile(sorted, 0.5), 0.01},
		{"lat", assay.P90, naiveQuantile(sorted, 0.9), 0.01},
		{"lat", assay.P95, naiveQuantile(sorted, 0.95), 0.01},
		{"lat", assay.P99, naiveQuantile(sorted, 0.99), 0.01},
		{"users", assay.Distinct, float64(len(users)), 1e-12},
		{"lines", assay.Distinct, float64(len(lines)), 1e-12},
	}
	for _, tc := range tcs {
		t.Run(tc.profile+" "+string(tc.op), func(t *testing.T) {
			assert.InEpsilon(t, tc.want, got[tc.profile][tc.op], tc.epsilon)
		})
	}
}

// Percentiles over values of both signs and zeros stay within 1% relative error of the naive rank
func TestPercentilesSigned(t *testing.T) {
	rng := rand.New(rand.NewPCG(3, 5))
	r := newRig(t, newMiner(t, moldrain.Options{}), []assay.Profile{
		{Name: "d", Match: "delta of <*>", Value: &assay.Locator{Slot: 0},
			Ops: []assay.Op{assay.P50, assay.P90, assay.P99, assay.Min}},
	}, assay.Options{})
	r.train("delta of x", t0)
	var values []float64
	for i := 0; i < 4000; i++ {
		v := math.Round((rng.Float64()*2-1.4)*1e6) / 100
		if i%50 == 0 {
			v = 0
		}
		r.train("delta of "+strconv.FormatFloat(v, 'f', -1, 64), t0)
		values = append(values, v)
	}
	r.a.Flush(t0.Add(time.Hour))
	slices.Sort(values)
	got := r.values("")["d"]
	tcs := []struct {
		op   assay.Op
		want float64
	}{
		{assay.P50, naiveQuantile(values, 0.5)},
		{assay.P90, naiveQuantile(values, 0.9)},
		{assay.P99, naiveQuantile(values, 0.99)},
		{assay.Min, values[0]},
	}
	for _, tc := range tcs {
		t.Run(string(tc.op), func(t *testing.T) {
			assert.LessOrEqual(t, math.Abs(got[tc.op]-tc.want), 0.01*math.Abs(tc.want))
		})
	}
}

func TestDistinctPastLimit(t *testing.T) {
	tcs := []struct {
		name   string
		values int
		limit  int
		approx bool
	}{
		{"exact under the limit", 90, 100, false},
		{"estimate of few values by linear counting", 60, 10, true},
		{"estimate past the limit", 20000, 100, true},
	}
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			r := newRig(t, newMiner(t, moldrain.Options{}), []assay.Profile{
				{Name: "u", Match: "login by <*>", Value: &assay.Locator{Slot: 0}, Ops: []assay.Op{assay.Distinct}},
			}, assay.Options{MaxDistinct: tc.limit})
			r.train("login by x", t0)
			for i := 0; i < tc.values*2; i++ {
				r.train(fmt.Sprintf("login by u%d", i%tc.values), t0)
			}
			r.a.Flush(t0.Add(time.Hour))
			recs := r.records()
			require.Len(t, recs, 1)
			assert.Equal(t, tc.approx, recs[0].Approx)
			// linear counting over 16384 registers estimates these counts within a few percent
			assert.InEpsilon(t, float64(tc.values), recs[0].Value, 0.05)
		})
	}
}

func TestUnits(t *testing.T) {
	type want struct {
		sum     float64
		skipped uint64
	}
	tcs := []struct {
		name   string
		unit   assay.Unit
		values []string
		want   want
	}{
		{"ms", assay.Milliseconds, []string{"120ms", "1.5s", "7", "250us", "2m", "3ns"}, want{120 + 1500 + 7 + 0.25 + 120000 + 3e-6, 0}},
		{"s", assay.Seconds, []string{"1500ms", "2", "1h", "500µs", "1min"}, want{1.5 + 2 + 3600 + 0.0005 + 60, 0}},
		{"bytes", assay.Bytes, []string{"512B", "4KiB", "2KB", "1MiB", "3", "1GB", "1TiB", "1MB", "1GiB", "1TB"},
			want{512 + 4096 + 2000 + 1<<20 + 3 + 1e9 + 1<<40 + 1e6 + 1<<30 + 1e12, 0}},
		{"none", assay.NoUnit, []string{"5", "-2.5", "+1", "1e3", "2E-1", "5ms"}, want{5 - 2.5 + 1 + 1000 + 0.2, 1}},
		{"unreadable", assay.NoUnit, []string{"x1", "1.2.3", ".", "-", "1e", "1e+", "12KB", "1"}, want{1, 7}},
		{"past the exact range", assay.NoUnit, []string{"12345678901234567890", "1e30", "0.1234567890123456789"},
			want{12345678901234567890 + 1e30 + 0.1234567890123456789, 0}},
		{"exponent too large", assay.NoUnit, []string{"1e999", "3"}, want{3, 1}},
	}
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			r := newRig(t, newMiner(t, moldrain.Options{}), []assay.Profile{
				{Name: "v", Match: "size was <*>", Value: &assay.Locator{Slot: 0}, Unit: tc.unit,
					Ops: []assay.Op{assay.Count, assay.Sum}},
			}, assay.Options{})
			r.train("size was unknown", t0)
			for _, v := range tc.values {
				r.train("size was "+v, t0)
			}
			r.a.Flush(t0.Add(time.Hour))
			recs := r.records()
			require.Len(t, recs, 2)
			assert.InEpsilon(t, tc.want.sum, recs[1].Value, 1e-12)
			assert.Equal(t, tc.want.skipped, recs[1].Skipped)
		})
	}
}
