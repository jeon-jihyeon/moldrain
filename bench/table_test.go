package main

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestMarkdown(t *testing.T) {
	acc := Accuracy{GA: 0.9, FGA: 0.8, PA: 0.7, FTA: 0.6, TemplatesPerEvent: 1.5}
	res := Run{Date: "d", Machine: "m", Go: "go", Commit: "c", Rows: []Row{
		{Dataset: "A", Contender: "moldrain", Lines: 10, Accuracy: acc, LinesPerSec: 100, AllocsPerLine: 2, BytesPerLine: 50,
			MatchPerSec: map[string]float64{"1": 10, "4": 30, "8": 50}},
		{Dataset: "A", Contender: "drain3", Lines: 10, Accuracy: acc, LinesPerSec: 10},
		{Dataset: "A", Contender: "spans", Lines: 10, Accuracy: Accuracy{GA: 0.5, FGA: 0.9}, LinesPerSec: 50},
		{Dataset: "A", Contender: "oracle", Lines: 10, Accuracy: Accuracy{GA: 0.95, FGA: 0.85, TemplatesPerEvent: 1.1}},
		{Dataset: "A", Contender: "faceair", Lines: 10, Accuracy: acc, LinesPerSec: 20},
		// a dataset one contender failed stays out of every average
		{Dataset: "B", Contender: "moldrain", Lines: 10, Accuracy: Accuracy{GA: 0.1}, LinesPerSec: 100},
		{Dataset: "B", Contender: "drain3", Lines: 10, Accuracy: Accuracy{GA: 0.2}, LinesPerSec: 10},
		{Dataset: "B", Contender: "spans", Lines: 10, Accuracy: Accuracy{GA: 0.1}, LinesPerSec: 50},
		{Dataset: "B", Contender: "oracle", Lines: 10, Accuracy: Accuracy{GA: 0.1}},
		{Dataset: "B", Contender: "faceair", Lines: 10, Error: "boom"},
	}}
	got := markdown(res)
	for _, want := range []string{
		"| A | 10 | 0.900 / 0.900 / 0.900 | 0.800 / 0.800 / 0.800 | 0.700 / 0.700 / 0.700 | 0.600 / 0.600 / 0.600 |",
		"| B | 10 | 0.100 / 0.200 / - | 0.000 / 0.000 / - | 0.000 / 0.000 / - | 0.000 / 0.000 / - |",
		"| moldrain | 1 | 0.900 | 0.800 | 0.700 | 0.600 | 1.50 | 100 | 2.0 | 50 |",
		"| drain3 | 1 | 0.900 | 0.800 | 0.700 | 0.600 | 1.50 | 10 | - | - |",
		"| faceair | 1 | 0.900 | 0.800 | 0.700 | 0.600 | 1.50 | 20 | - | - |",
		"| spans | 0.000 | 0.000 | 0.000 - | incomplete 0 of 14 |",
		"| A | 10 | 30 | 50 | 5.0 |",
		"| A | 1.50 / 1.10 | 0.900 / 0.950 | 0.800 / 0.850 |",
		"| oracle | 1 | 0.950 | 0.850 | 0.000 | 0.000 | 1.10 | - | - | - |",
	} {
		assert.Contains(t, got, want)
	}
}

// A gate judges only a run of every dataset without an error
func TestMarkdownGate(t *testing.T) {
	type args struct {
		// datasets of the run from the start of allDatasets
		datasets int
		// the option fails on the first dataset
		failed bool
		// GA of the option on every dataset against 0.9 without it
		ga      float64
		replace bool
		extra   bool
	}
	tcs := []struct {
		name string
		args args
		want string
	}{
		{"every dataset at the same accuracy", args{datasets: len(allDatasets), ga: 0.9}, "| 0.000 - | true |"},
		{"every dataset with a GA drop", args{datasets: len(allDatasets), ga: 0.8}, "| 0.100 Apache | false |"},
		{"a dataset left out", args{datasets: len(allDatasets) - 1, ga: 0.9}, "| incomplete 13 of 14 |"},
		{
			"a dataset the option failed",
			args{datasets: len(allDatasets), failed: true, ga: 0.9},
			"| incomplete 13 of 14 |",
		},
		{
			"a required dataset replaced by another",
			args{datasets: len(allDatasets), ga: 0.9, replace: true},
			"| incomplete 13 of 14 |",
		},
		{
			"an extra dataset does not enter the gate",
			args{datasets: len(allDatasets), ga: 0.9, extra: true},
			"| 0.000 - | true |",
		},
	}
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			var res Run
			for i, ds := range allDatasets[:tc.args.datasets] {
				if i == 0 && tc.args.replace {
					ds = "Other"
				}
				opt := Row{Dataset: ds, Contender: "punct", Lines: 1, Accuracy: Accuracy{GA: tc.args.ga, FGA: 0.5}}
				if i == 0 && tc.args.failed {
					opt = Row{Dataset: ds, Contender: "punct", Error: "boom"}
				}
				res.Rows = append(res.Rows,
					Row{Dataset: ds, Contender: "moldrain", Lines: 1, Accuracy: Accuracy{GA: 0.9, FGA: 0.5}},
					opt)
			}
			if tc.args.extra {
				res.Rows = append(res.Rows,
					Row{Dataset: "Other", Contender: "moldrain", Accuracy: Accuracy{GA: 1, FGA: 1}},
					Row{Dataset: "Other", Contender: "punct"})
			}
			assert.Contains(t, markdown(res), tc.want)
		})
	}
}
