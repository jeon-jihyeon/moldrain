package main

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestMeasure(t *testing.T) {
	// events a a a b b c with the templates "x <*>" and "y" and "z <*>"
	tr := &truth{events: []int32{0, 0, 0, 1, 1, 2}, templates: []string{"x <*>", "y", "z <*>"}, sizes: []int{3, 2, 1}}
	tcs := []struct {
		name      string
		groups    []uint64
		templates map[uint64]string
		want      Accuracy
	}{
		{
			"exact parse",
			[]uint64{7, 7, 7, 8, 8, 9},
			map[uint64]string{7: "x <*>", 8: "y", 9: "z  <num>"},
			Accuracy{GA: 1, FGA: 1, PA: 1, FTA: 1, TemplatesPerEvent: 1, Groups: 3, Events: 3},
		},
		{
			"event a split in two and b and c merged",
			[]uint64{1, 1, 2, 3, 3, 3},
			map[uint64]string{1: "x <*>", 2: "x 2", 3: "<*>"},
			// no group equals a truth group
			// Lines 0 and 1 have the right text
			Accuracy{GA: 0, FGA: 0, PA: 2.0 / 6, FTA: 0, TemplatesPerEvent: 4.0 / 3, Groups: 3, Events: 3},
		},
		{
			"one exact group of three with the wrong text",
			[]uint64{1, 1, 1, 2, 3, 4},
			map[uint64]string{1: "x y", 2: "y", 3: "y", 4: "z <*>"},
			// groups 1 and 4 are exact
			// Group 4 also has the right text
			Accuracy{GA: 4.0 / 6, FGA: 2 * (2.0 / 4) * (2.0 / 3) / (2.0/4 + 2.0/3), PA: 3.0 / 6,
				FTA: 2 * (1.0 / 4) * (1.0 / 3) / (1.0/4 + 1.0/3), TemplatesPerEvent: 4.0 / 3, Groups: 4, Events: 3},
		},
	}
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := measure(tc.groups, tc.templates, tr)
			assert.InDelta(t, tc.want.GA, got.GA, 1e-9)
			assert.InDelta(t, tc.want.FGA, got.FGA, 1e-9)
			assert.InDelta(t, tc.want.PA, got.PA, 1e-9)
			assert.InDelta(t, tc.want.FTA, got.FTA, 1e-9)
			assert.InDelta(t, tc.want.TemplatesPerEvent, got.TemplatesPerEvent, 1e-9)
			assert.Equal(t, tc.want.Groups, got.Groups)
		})
	}
}

func TestNormalize(t *testing.T) {
	assert.Equal(t, "a <*> b <*>", normalize("  a <num>  <*> b\t<*...>"))
}
