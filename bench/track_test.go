package main

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jeon-jihyeon/moldrain"
	"github.com/jeon-jihyeon/moldrain/resolve"
)

func TestTracker(t *testing.T) {
	// a step is an event or a line of the template id when kind is 0
	type step struct {
		kind     moldrain.EventKind
		id       moldrain.ID
		from, to moldrain.ID
	}
	line := func(id moldrain.ID) step { return step{id: id} }
	tcs := []struct {
		name  string
		steps []step
		// lines in the same group share a number
		want []int
	}{
		{
			"a template that changes keeps its lines",
			[]step{{kind: moldrain.Created, id: 1}, line(1), {kind: moldrain.Changed, from: 1, to: 2}, line(2)},
			[]int{0, 0},
		},
		{
			"a change into a template that exists joins both groups",
			[]step{{kind: moldrain.Created, id: 1}, line(1), {kind: moldrain.Created, id: 2}, line(2), {kind: moldrain.Changed, from: 1, to: 2}, line(2)},
			[]int{0, 0, 0},
		},
		{
			"an id created again after eviction is a group of its own",
			[]step{{kind: moldrain.Created, id: 1}, line(1), {kind: moldrain.Evicted, id: 1}, {kind: moldrain.Created, id: 1}, line(1)},
			[]int{0, 1},
		},
	}
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			tr := newTracker(false)
			var labels []int32
			for _, s := range tc.steps {
				if s.kind == 0 {
					labels = append(labels, tr.line(s.id, 0))
					continue
				}
				tr.onEvent(moldrain.Event{Kind: s.kind, ID: s.id, From: s.from, To: s.to})
			}
			groups := map[int32]int{}
			got := make([]int, len(labels))
			for i, l := range labels {
				root := tr.find(l)
				if _, ok := groups[root]; !ok {
					groups[root] = len(groups)
				}
				got[i] = groups[root]
			}
			assert.Equal(t, tc.want, got)
		})
	}
}

// The oracle names the candidate whose lines are mostly the pending line's event
func TestOracle(t *testing.T) {
	tr := newTracker(true)
	tr.next(7)
	tr.onEvent(moldrain.Event{Kind: moldrain.Created, ID: 1, Line: []byte("a")})
	tr.line(1, 7)
	tr.next(8)
	tr.onEvent(moldrain.Event{Kind: moldrain.Created, ID: 2, Line: []byte("b")})
	tr.line(2, 8)
	tr.line(2, 8)
	tr.next(8)
	tr.onEvent(moldrain.Event{Kind: moldrain.Created, ID: 3, Line: []byte("c")})
	ds, err := tr.Resolve(context.Background(), []resolve.Pending{
		{Line: []byte("c"), Candidates: []resolve.Candidate{{ID: moldrain.ID(1).String()}, {ID: moldrain.ID(2).String()}}},
		{Line: []byte("unknown"), Candidates: []resolve.Candidate{{ID: moldrain.ID(2).String()}}},
		{Line: []byte("c"), Candidates: []resolve.Candidate{{ID: "not an id"}, {ID: moldrain.ID(9).String()}}},
	})
	require.NoError(t, err)
	assert.Equal(t, []resolve.Decision{{Index: 0, Same: moldrain.ID(2).String()}, {Index: 1}, {Index: 2}}, ds)
}
