package tree_test

import (
	"fmt"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jeon-jihyeon/moldrain/internal/tree"
)

func TestRebuild(t *testing.T) {
	type want struct {
		counts map[string]uint64
		// Changed events whose From equals To
		selfMoves int
		rebuilds  int
	}
	states := make([]string, 30)
	for i := range states {
		states[i] = "state changed to " + []string{"up", "down"}[i%2]
	}
	tcs := []struct {
		name   string
		opt    tree.Options
		before []string
		// line indexes of before made alias and target in order
		aliases [][2]int
		after   []string
		want    want
	}{
		{
			"a template merged into one placed later keeps its lines",
			tree.Options{MaxChildren: 4},
			[]string{"A a B", "A b B", "A x1 B", "A x2 B", "C c D", "C d D"},
			nil,
			nil,
			want{map[string]uint64{"A <*> B": 4, "C <*> D": 2}, 0, 1},
		},
		{
			"a template that turns into the text of an alias counts for its target",
			tree.Options{MaxChildren: 4},
			[]string{"zzz", "A a B", "A b B", "A x1 B", "A x2 B"},
			[][2]int{{4, 0}},
			[]string{"C c D", "C d D"},
			want{map[string]uint64{"zzz": 5, "C <*> D": 2}, 0, 1},
		},
		{
			"a pinned template stays out of its source",
			tree.Options{MaxChildren: 4, Respecialize: true, SplitMin: 10},
			states,
			nil,
			[]string{"a b c d", "e f g h", "i j k l"},
			want{map[string]uint64{
				"state changed to <*>": 12, "state changed to up": 9, "state changed to down": 9,
				"a b c d": 1, "e f g h": 1, "i j k l": 1,
			}, 0, 1},
		},
	}
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			h := newHarness(t, tc.opt)
			ids := h.train(tc.before...)
			for _, a := range tc.aliases {
				require.NoError(t, h.tree.Alias(ids[a[0]], ids[a[1]], t0))
			}
			h.train(tc.after...)
			got := want{counts: map[string]uint64{}}
			for _, c := range h.tree.Clusters() {
				got.counts[c.Text()] = c.Count()
			}
			for _, ev := range h.events {
				if ev.Kind == tree.Changed && ev.From == ev.To {
					got.selfMoves++
				}
			}
			for _, b := range h.tree.Export().Buckets {
				got.rebuilds += b.Rebuilds
			}
			assert.Equal(t, tc.want, got)
		})
	}
}

// A stream led by a unique id per line keeps one branch per event instead of one per id
func TestStableBranches(t *testing.T) {
	h := newHarness(t, tree.Options{})
	events := []string{"GET /users done", "POST /orders failed", "DELETE /carts retried"}
	var lines []string
	for i := 0; i < 3000; i++ {
		lines = append(lines, fmt.Sprintf("req-%s %s", alpha(i), events[i%3]))
	}
	ids := h.finalIDs(h.train(lines...))
	st := h.tree.Export()
	require.Len(t, st.Buckets, 1)
	b := st.Buckets[0]
	assert.Len(t, groups(ids), 3)
	assert.Less(t, len(b.Root.Keys)+1, tree.DefaultMaxChildren)
	assert.Equal(t, []int{1, 2}, b.Pos)
	assert.ElementsMatch(t, []string{"<*> GET /users done", "<*> POST /orders failed", "<*> DELETE /carts retried"}, h.templates())
}

func TestRebuildsStop(t *testing.T) {
	// SimTh 1 keeps every line its own template
	// Every position keeps turning unstable
	h := newHarness(t, tree.Options{MaxChildren: 3, SimTh: 1})
	// position p starts to vary at line 20p
	// Positions turn unstable one after another
	var lines []string
	for i := 0; i < 200; i++ {
		words := make([]string, 6)
		for p := range words {
			words[p] = "same"
			if i >= 20*p {
				words[p] = fmt.Sprintf("w%s_%s", alpha(p), alpha(i))
			}
		}
		lines = append(lines, strings.Join(words, " "))
	}
	h.train(lines...)
	st := h.tree.Export()
	require.Len(t, st.Buckets, 1)
	b := st.Buckets[0]
	assert.Equal(t, tree.DefaultMaxRebuilds, b.Rebuilds)
	assert.Equal(t, []int{3, 4}, b.Pos)
	assert.True(t, b.Unstable[3])
}
