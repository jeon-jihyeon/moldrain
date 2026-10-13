package tree_test

import (
	"fmt"
	"math/rand/v2"
	"slices"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jeon-jihyeon/moldrain/internal/token"
	"github.com/jeon-jihyeon/moldrain/internal/tree"
)

func TestReplay(t *testing.T) {
	for seed := uint64(0); seed < 10; seed++ {
		t.Run(fmt.Sprintf("seed %d", seed), func(t *testing.T) {
			t.Parallel()
			r := rand.New(rand.NewPCG(seed, 99))
			lines := genLines(r, 1500)
			opt := tree.Options{MaxChildren: 3 + r.IntN(8), Spans: seed%2 == 0, Respecialize: seed%3 == 0, SplitMin: 30}
			h := newHarness(t, opt)
			h.train(lines[:500]...)
			snap := h.tree.Export()
			h.events = nil
			h.train(lines[500:]...)

			replayed := newTree(t, opt)
			require.NoError(t, replayed.Import(snap))
			for _, ev := range h.events {
				require.NoError(t, replayed.Apply(ev))
			}
			want, got := h.tree.Export(), replayed.Export()
			want.Lines, got.Lines = 0, 0
			for i := range want.Clusters {
				want.Clusters[i].Hit = 0
			}
			for i := range got.Clusters {
				got.Clusters[i].Hit = 0
			}
			assert.Equal(t, want, got)
		})
	}
}

func TestRestoreMatchesSame(t *testing.T) {
	lines := genLines(rand.New(rand.NewPCG(5, 6)), 3000)
	h := newHarness(t, tree.Options{MaxChildren: 6})
	h.train(lines[:2000]...)
	restored := newTree(t, tree.Options{MaxChildren: 6})
	require.NoError(t, restored.Import(h.tree.Export()))
	va, vb := h.tree.Publish(nil), restored.Publish(nil)
	type answer struct {
		id tree.ID
		ok bool
	}
	match := func(v *tree.View, l string) answer {
		c, _, ok := v.Match([]byte(l), h.tz.Tokens([]byte(l), nil))
		if !ok {
			return answer{}
		}
		return answer{c.ID(), true}
	}
	for _, l := range lines {
		require.Equal(t, match(va, l), match(vb, l), l)
	}
}

// A state that does not describe a valid tree fails as a whole and leaves the tree empty
func TestImportRefuses(t *testing.T) {
	tcs := []struct {
		name   string
		mutate func(st *tree.State)
		want   error
		// the check that refused the state
		msg string
	}{
		{"a cluster listed twice", func(st *tree.State) { st.Clusters = append(st.Clusters, st.Clusters[0]) }, tree.ErrState, "listed twice"},
		{"an id another template hashes to", func(st *tree.State) { st.Clusters[0].ID++ }, tree.ErrState, "hashes to"},
		{"gaps that do not fit the parts", func(st *tree.State) { st.Clusters[0].Shape.Gaps = nil }, tree.ErrState, "gaps for"},
		{"a part of an unknown kind", func(st *tree.State) { st.Clusters[0].Shape.Parts[0].Kind = 99 }, tree.ErrState, "of kind 99"},
		{"a constant span", func(st *tree.State) { st.Clusters[0].Shape.Parts[0].Span = true }, tree.ErrState, "constant span"},
		{"a pinned variable", func(st *tree.State) {
			st.Clusters[0].Shape.Parts[0] = tree.Part{Kind: token.Num, Pinned: true}
		}, tree.ErrState, "pinned variable"},
		{"two spans", func(st *tree.State) {
			st.Clusters[0].Shape.Parts[0] = tree.Part{Kind: token.Var, Span: true}
			st.Clusters[0].Shape.Parts[1] = tree.Part{Kind: token.Var, Span: true}
		}, tree.ErrState, "2 spans"},
		{"an alias of a missing template", func(st *tree.State) {
			st.Clusters[0].HasAlias, st.Clusters[0].Alias = true, 7
		}, tree.ErrState, "to missing"},
		{"an alias of itself", func(st *tree.State) {
			st.Clusters[0].HasAlias, st.Clusters[0].Alias = true, st.Clusters[0].ID
		}, tree.ErrState, "alias of itself"},
		{"an alias of an alias", func(st *tree.State) {
			st.Clusters[0].HasAlias, st.Clusters[0].Alias = true, st.Clusters[1].ID
			st.Clusters[1].HasAlias, st.Clusters[1].Alias = true, st.Clusters[2].ID
		}, tree.ErrState, "itself an alias"},
		{"a cluster in no bucket", func(st *tree.State) {
			id := st.Buckets[0].Order[0]
			st.Buckets[0].Order = st.Buckets[0].Order[1:]
			dropLeaf(&st.Buckets[0].Root, id)
		}, tree.ErrState, "1 clusters in no bucket"},
		{"a cluster twice in one bucket", func(st *tree.State) {
			st.Buckets[0].Order = append(st.Buckets[0].Order, st.Buckets[0].Order[0])
		}, tree.ErrState, "in bucket 2"},
		{"a bucket listed twice", func(st *tree.State) { st.Buckets = append(st.Buckets, st.Buckets[0]) }, tree.ErrState, "bucket 2 listed twice"},
		{"a bucket whose positions do not fit", func(st *tree.State) { st.Buckets[0].Unstable = nil }, tree.ErrState, "bucket 2"},
		{"a branch position past the token count", func(st *tree.State) { st.Buckets[0].Pos = []int{99} }, tree.ErrState, "position 99"},
		{"a cluster in the bucket of another token count", func(st *tree.State) {
			st.Buckets[0].Order[0], st.Buckets[1].Order[0] = st.Buckets[1].Order[0], st.Buckets[0].Order[0]
		}, tree.ErrState, "in bucket"},
		{"more keys than children", func(st *tree.State) {
			st.Buckets[0].Root.Keys = append(st.Buckets[0].Root.Keys, "x")
		}, tree.ErrState, "keys for"},
		{"a leaf of a missing template", func(st *tree.State) {
			st.Buckets[0].Root.Leaf = append(st.Buckets[0].Root.Leaf, 7)
		}, tree.ErrState, "leaf holds missing"},
	}
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			h := newHarness(t, tree.Options{})
			h.train("a b", "c d", "e f g", "h i j")
			st := h.tree.Export()
			require.Len(t, st.Clusters, 4)
			require.Len(t, st.Buckets, 2)
			tc.mutate(&st)
			tr := newTree(t, tree.Options{})
			err := tr.Import(st)
			assert.ErrorIs(t, err, tc.want)
			assert.ErrorContains(t, err, tc.msg)
			assert.Equal(t, tree.State{}, tr.Export())
		})
	}
}

// Takes id out of every leaf under ns
func dropLeaf(ns *tree.NodeState, id tree.ID) {
	ns.Leaf = slices.DeleteFunc(ns.Leaf, func(x tree.ID) bool { return x == id })
	for i := range ns.Kids {
		dropLeaf(&ns.Kids[i], id)
	}
	if ns.Wild != nil {
		dropLeaf(ns.Wild, id)
	}
}

func TestImportIntoTrainedTree(t *testing.T) {
	h := newHarness(t, tree.Options{})
	h.train("a b")
	assert.ErrorIs(t, h.tree.Import(h.tree.Export()), tree.ErrNotEmpty)
}

func TestApply(t *testing.T) {
	sh := tree.Shape{Parts: []tree.Part{{Text: "a"}, {Text: "b"}}, Gaps: []string{"", " ", ""}}
	bad := tree.Shape{Parts: []tree.Part{{Text: "a"}}}
	// the id of the template every case trains first
	// An id is the hash of its template so any tree gives it
	id := newHarness(t, tree.Options{}).train("x y")[0]
	tcs := []struct {
		name string
		ev   tree.Event
		want error
	}{
		{"a Created without a template", tree.Event{Kind: tree.Created, ID: 1}, tree.ErrState},
		{"a Created with gaps that do not fit", tree.Event{Kind: tree.Created, ID: 1, Shape: &bad}, tree.ErrState},
		{"a Created of another id", tree.Event{Kind: tree.Created, ID: 1, Shape: &sh, N: 1}, tree.ErrDiverged},
		{"a Changed without a template", tree.Event{Kind: tree.Changed, From: id, To: 1}, tree.ErrState},
		{"a Changed with gaps that do not fit", tree.Event{Kind: tree.Changed, From: id, To: 1, Shape: &bad}, tree.ErrState},
		{"a Changed to another id", tree.Event{Kind: tree.Changed, From: id, To: 1, Shape: &sh}, tree.ErrDiverged},
		{"a Changed of a template a replayed rebuild merged", tree.Event{Kind: tree.Changed, From: 7, To: 1}, nil},
		{"a Matched of a missing template", tree.Event{Kind: tree.Matched, ID: 7, N: 1}, nil},
		{"an Evicted of a missing template", tree.Event{Kind: tree.Evicted, ID: 7}, nil},
	}
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			h := newHarness(t, tree.Options{})
			h.train("x y")
			assert.ErrorIs(t, h.tree.Apply(tc.ev), tc.want)
		})
	}
}

// Apply emits nothing
// A replay is not recorded again
func TestApplyIsSilent(t *testing.T) {
	src := newHarness(t, tree.Options{})
	src.train("a b c", "a b d")
	dst := newHarness(t, tree.Options{})
	for _, ev := range src.events {
		require.NoError(t, dst.tree.Apply(ev))
	}
	assert.Empty(t, dst.events)
	assert.Equal(t, src.templates(), dst.templates())
}
