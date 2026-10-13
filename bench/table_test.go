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
		{Dataset: "A", Contender: "faceair", Lines: 10, Error: "boom"},
	}}
	got := markdown(res)
	for _, want := range []string{
		"| A | 10 | 0.900 / 0.900 / - | 0.800 / 0.800 / - | 0.700 / 0.700 / - | 0.600 / 0.600 / - |",
		"| moldrain | 0.900 | 0.800 | 0.700 | 0.600 | 1.50 | 100 | 2.0 | 50 |",
		"| drain3 | 0.900 | 0.800 | 0.700 | 0.600 | 1.50 | 10 | - | - |",
		"| spans | 0.900 | 0.800 | 0.400 A | false |",
		"| A | 10 | 30 | 50 | 5.0 |",
		"| A | 1.50 / 1.10 | 0.900 / 0.950 | 0.800 / 0.850 |",
		"| oracle | 0.950 | 0.850 | 0.000 | 0.000 | 1.10 | - | - | - |",
	} {
		assert.Contains(t, got, want)
	}
}
