package tree_test

import (
	"fmt"
	"math/rand/v2"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jeon-jihyeon/moldrain/internal/tree"
)

func TestView(t *testing.T) {
	h := newHarness(t, tree.Options{})
	ids := h.train("disk full on sda", "disk is full on sdb", "disk was full on sdc", "cache miss")
	view := h.tree.Publish(nil)
	h.train("brand new line here")
	var texts []string
	for _, c := range view.Clusters() {
		texts = append(texts, c.Text())
	}
	assert.Equal(t, []string{"cache miss", "disk full on sda", "disk is full on sdb", "disk was full on sdc"}, texts)
	require.NotNil(t, view.Get(ids[1]))
	assert.Equal(t, "disk is full on sdb", view.Get(ids[1]).Text())
	assert.Nil(t, view.Get(7))
	assert.True(t, h.tree.Dirty())
	assert.Same(t, view.Get(ids[3]), h.tree.Publish(view).Get(ids[3]))
	assert.False(t, h.tree.Dirty())
}

// A view finds through the postings of a large leaf what the live tree finds by a full scan
func TestViewPostings(t *testing.T) {
	r := rand.New(rand.NewPCG(1, 2))
	words := make([]string, 40)
	for i := range words {
		words[i] = alpha(i)
	}
	line := func() string {
		return fmt.Sprintf("%s %s %s %s %d", words[r.IntN(len(words))], words[r.IntN(len(words))],
			words[r.IntN(len(words))], words[r.IntN(len(words))], r.IntN(100))
	}
	h := newHarness(t, tree.Options{MaxChildren: 4, SimTh: 0.5})
	for i := 0; i < 3000; i++ {
		h.train(line())
	}
	view := h.tree.Publish(nil)
	require.GreaterOrEqual(t, tree.LargestLeaf(view), 64)
	id := func(c *tree.Cluster) tree.ID {
		if c == nil {
			return 0
		}
		return c.ID()
	}
	for i := 0; i < 3000; i++ {
		l := []byte(line())
		live := tree.FindLive(h.tree, l, h.tz.Tokens(l, nil))
		_, matched, _ := view.Match(l, h.tz.Tokens(l, nil))
		assert.Equal(t, id(live), id(matched), "%s", l)
	}
}

func TestNearest(t *testing.T) {
	type near struct {
		text string
		sim  float64
	}
	h := newHarness(t, tree.Options{})
	ids := h.train("disk full on sda now", "disk is full on sdb", "disk was full on sdc", "x y z w v")
	view := h.tree.Publish(nil)
	tcs := []struct {
		name   string
		line   string
		k      int
		before tree.ID
		want   []near
	}{
		{"most similar first", "disk is full on sdq", 2, 0, []near{{"disk is full on sdb", 0.8}, {"disk was full on sdc", 0.6}}},
		{"only templates older than before", "disk is full on sdq", 3, ids[2], []near{{"disk is full on sdb", 0.8}, {"disk full on sda now", 0.2}}},
		{"a line of a token count nothing has", "one two", 3, 0, nil},
		{"no k", "disk is full on sdq", 0, 0, nil},
		{"a negative k", "disk is full on sdq", -1, 0, nil},
	}
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			var got []near
			for _, n := range view.Nearest([]byte(tc.line), h.tz.Tokens([]byte(tc.line), nil), tc.k, tc.before) {
				got = append(got, near{n.Cluster.Text(), n.Sim})
			}
			assert.Equal(t, tc.want, got)
		})
	}
}
