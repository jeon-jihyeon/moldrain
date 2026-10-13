package main

import (
	"regexp"
	"strings"
)

// The ground truth of one dataset
type truth struct {
	// event index of each line
	events []int32
	// template of each event index
	templates []string
	sizes     []int
}

// Accuracy of one parse against the ground truth
// The template texts are normalized for this bench before they are compared
type Accuracy struct {
	// Grouping accuracy: share of lines whose predicted group is exactly a ground truth group
	GA float64 `json:"ga"`
	// F1 of groups
	FGA float64 `json:"fga"`
	// Parsing accuracy: share of lines whose template equals the ground truth template
	PA float64 `json:"pa"`
	// F1 of templates: a template counts when its group is exact and its text equal
	FTA float64 `json:"fta"`
	// Predicted groups among the lines of one ground truth event
	// Averaged over events
	TemplatesPerEvent float64 `json:"templates_per_event"`
	Groups            int     `json:"groups"`
	Events            int     `json:"events"`
}

var (
	placeholder = regexp.MustCompile(`<\*\.\.\.>|<(num|hex|ip|uuid|time)>`)
	spaces      = regexp.MustCompile(`\s+`)
	repeated    = regexp.MustCompile(`<\*>(\s*<\*>)+`)
)

// The same light correction for every parser and the ground truth
// 1. placeholders as <*>
// 2. runs of whitespace as one space
// 3. adjacent <*> as one
func normalize(t string) string {
	t = placeholder.ReplaceAllString(t, "<*>")
	t = spaces.ReplaceAllString(strings.TrimSpace(t), " ")
	return repeated.ReplaceAllString(t, "<*>")
}

func measure(groups []uint64, templates map[uint64]string, tr *truth) Accuracy {
	n := len(groups)
	dense := map[uint64]int32{}
	pg := make([]int32, n)
	for i, g := range groups {
		d, ok := dense[g]
		if !ok {
			d = int32(len(dense))
			dense[g] = d
		}
		pg[i] = d
	}
	np := len(dense)
	size := make([]int, np)
	first := make([]int32, np)
	mixed := make([]bool, np)
	for i := range first {
		first[i] = -1
	}
	for i, p := range pg {
		e := tr.events[i]
		size[p]++
		if first[p] < 0 {
			first[p] = e
		} else if first[p] != e {
			mixed[p] = true
		}
	}
	normTruth := make([]string, len(tr.templates))
	for e, t := range tr.templates {
		normTruth[e] = normalize(t)
	}
	normPred := make([]string, np)
	for g, d := range dense {
		normPred[d] = normalize(templates[g])
	}
	exact := make([]bool, np)
	correctGroups, correctTemplates, gaLines := 0, 0, 0
	for p := 0; p < np; p++ {
		e := first[p]
		if mixed[p] || size[p] != tr.sizes[e] {
			continue
		}
		exact[p] = true
		correctGroups++
		gaLines += size[p]
		if normPred[p] == normTruth[e] {
			correctTemplates++
		}
	}
	paLines := 0
	pairs := map[[2]int32]struct{}{}
	for i, p := range pg {
		e := tr.events[i]
		if normPred[p] == normTruth[e] {
			paLines++
		}
		pairs[[2]int32{e, p}] = struct{}{}
	}
	events := 0
	for _, s := range tr.sizes {
		if s > 0 {
			events++
		}
	}
	return Accuracy{
		GA:                float64(gaLines) / float64(n),
		FGA:               f1(correctGroups, np, events),
		PA:                float64(paLines) / float64(n),
		FTA:               f1(correctTemplates, np, events),
		TemplatesPerEvent: float64(len(pairs)) / float64(events),
		Groups:            np,
		Events:            events,
	}
}

func f1(correct, predicted, actual int) float64 {
	if correct == 0 {
		return 0
	}
	p := float64(correct) / float64(predicted)
	r := float64(correct) / float64(actual)
	return 2 * p * r / (p + r)
}
