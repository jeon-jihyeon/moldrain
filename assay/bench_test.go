package assay_test

import (
	"testing"
	"time"

	"github.com/jeon-jihyeon/moldrain"
	"github.com/jeon-jihyeon/moldrain/assay"
)

func BenchmarkObserve(b *testing.B) {
	tcs := []struct {
		name string
		line string
		// how far each line is stamped after the one before
		step time.Duration
	}{
		{"bound", "req 42 took 120ms", 0},
		{"bound moving forward", "req 42 took 120ms", time.Millisecond},
		{"unbound", "something else happened", 0},
	}
	for _, tc := range tcs {
		b.Run(tc.name, func(b *testing.B) {
			r := newRig(b, newMiner(b, moldrain.Options{}), []assay.Profile{
				{Name: "req", Match: "req <*> took <*>", Value: &assay.Locator{Slot: 1}, Unit: assay.Milliseconds,
					Ops: []assay.Op{assay.Count, assay.Mean, assay.P99}},
			}, assay.Options{})
			r.m.Train([]byte("req 1 took 3ms"), t0)
			res := r.m.Train([]byte(tc.line), t0)
			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				r.a.Observe(res, t0.Add(time.Duration(i)*tc.step))
			}
		})
	}
}
