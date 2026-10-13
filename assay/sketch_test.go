package assay

import (
	"math"
	"testing"

	"github.com/stretchr/testify/assert"
)

// The estimate past the default limit stays within its stated error on average and without bias
// Each trial counts n distinct hashes of its own
func TestDistinctError(t *testing.T) {
	const trials = 40
	tcs := []struct {
		name string
		n    int
	}{
		{"just past the limit", DefaultMaxDistinct + 1},
		{"past the limit by 5%", DefaultMaxDistinct * 105 / 100},
		{"twice the limit", 2 * DefaultMaxDistinct},
		{"before the switch to the raw estimate", 50000},
		{"after the switch to the raw estimate", 56000},
		{"ten times the limit", 10 * DefaultMaxDistinct},
		{"fifty times the limit", 50 * DefaultMaxDistinct},
	}
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			var sum, abs float64
			for trial := 0; trial < trials; trial++ {
				d := newDistinct(DefaultMaxDistinct)
				for i := 0; i < tc.n; i++ {
					d.add(hashSeed.u64(uint64(trial)<<32 | uint64(i)).sum())
				}
				e := (d.count() - float64(tc.n)) / float64(tc.n)
				sum += e
				abs += math.Abs(e)
			}
			t.Logf("n %d bias %.4f mean error %.4f", tc.n, sum/trials, abs/trials)
			assert.Less(t, math.Abs(sum/trials), 0.008)
			assert.Less(t, abs/trials, 0.012)
		})
	}
}
