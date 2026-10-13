package tree_test

import (
	"fmt"
	"math/rand/v2"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jeon-jihyeon/moldrain/internal/token"
	"github.com/jeon-jihyeon/moldrain/internal/tree"
)

var t0 = time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)

type harness struct {
	tree   *tree.Tree
	tz     token.Tokenizer
	events []tree.Event
}

func newTree(t testing.TB, opt tree.Options) *tree.Tree {
	t.Helper()
	tr, err := tree.New(opt, token.NewInterner())
	require.NoError(t, err)
	return tr
}

func newHarness(t testing.TB, opt tree.Options) *harness {
	t.Helper()
	h := &harness{tree: newTree(t, opt), tz: token.NewPlain(token.Config{})}
	h.tree.OnEvent(func(ev tree.Event) {
		ev.Line = nil
		h.events = append(h.events, ev)
	})
	return h
}

// The canonical id each line ended in when it was trained
// Line i is trained i seconds after t0
func (h *harness) train(lines ...string) []tree.ID {
	ids := make([]tree.ID, len(lines))
	for i, l := range lines {
		toks := h.tz.Tokens([]byte(l), nil)
		c, _ := h.tree.Train([]byte(l), toks, t0.Add(time.Duration(i)*time.Second))
		ids[i] = c.Canonical().ID()
	}
	return ids
}

// Ids of each line after every later change as Changed events moved them
func (h *harness) finalIDs(ids []tree.ID) []tree.ID {
	moved := map[tree.ID]tree.ID{}
	for _, ev := range h.events {
		if ev.Kind == tree.Changed {
			moved[ev.From] = ev.To
		}
	}
	out := make([]tree.ID, len(ids))
	for i, id := range ids {
		for {
			next, ok := moved[id]
			if !ok || next == id {
				break
			}
			id = next
		}
		out[i] = id
	}
	return out
}

func (h *harness) templates() []string {
	var out []string
	for _, c := range h.tree.Clusters() {
		out = append(out, c.Text())
	}
	return out
}

func (h *harness) kinds() []tree.EventKind {
	out := make([]tree.EventKind, len(h.events))
	for i, ev := range h.events {
		out[i] = ev.Kind
	}
	return out
}

// Groups as sets of line indexes as grouping accuracy compares them
func groups(ids []tree.ID) [][]int {
	byID := map[tree.ID][]int{}
	var order []tree.ID
	for i, id := range ids {
		if _, ok := byID[id]; !ok {
			order = append(order, id)
		}
		byID[id] = append(byID[id], i)
	}
	out := make([][]int, 0, len(order))
	for _, id := range order {
		out = append(out, byID[id])
	}
	return out
}

func TestTrain(t *testing.T) {
	tcs := []struct {
		name      string
		opt       tree.Options
		tz        token.Tokenizer
		lines     []string
		groups    [][]int
		templates []string
	}{
		{
			"ssh lines in the shape of Drain3's example",
			tree.Options{},
			token.NewPlain(token.Config{}),
			[]string{
				"Mar 3 01:02:03 hostA sshd[1101]: input_userauth_request: invalid user user7 [preauth]",
				"Mar 3 01:02:09 hostA sshd[1102]: input_userauth_request: invalid user alpha [preauth]",
				"Mar 3 01:05:11 hostA sshd[1203]: Failed password for invalid user beta from 0.0.0.0 port 40001 ssh2",
				"Mar 3 01:05:14 hostA sshd[1204]: Failed password for invalid user gamma from 0.0.0.0 port 40002 ssh2",
				"Mar 3 01:05:20 hostA sshd[1205]: Failed password for invalid user beta from 0.0.0.0 port 40003 ssh2",
				"Mar 3 01:03:30 hostA sshd[1150]: input_userauth_request: invalid user delta [preauth]",
			},
			[][]int{{0, 1, 5}, {2, 3, 4}},
			[]string{
				"Mar <*> <*> hostA <*> input_userauth_request: invalid user <*> [preauth]",
				"Mar <*> <*> hostA <*> Failed password for invalid user <*> from <*> port <*> ssh2",
			},
		},
		{
			"Drain paper example",
			tree.Options{},
			token.NewPlain(token.Config{}),
			[]string{"Receive from node 4", "Receive from node 5", "Send file over", "Receive 120 bytes", "Receive 1 bytes", "Send file 1"},
			[][]int{{0, 1}, {2, 5}, {3, 4}},
			[]string{"Send file <*>", "Receive <*> bytes", "Receive from node <*>"},
		},
		{
			"different token counts never share a template",
			tree.Options{},
			token.NewPlain(token.Config{}),
			[]string{"a b c", "a b", "a b c d"},
			[][]int{{0}, {1}, {2}},
			[]string{"a b", "a b c", "a b c d"},
		},
		{
			"empty and spacing only lines share one template",
			tree.Options{},
			token.NewPlain(token.Config{}),
			[]string{"", "   ", "x  y", "x y"},
			[][]int{{0, 1}, {2, 3}},
			[]string{"", "x y"},
		},
		{
			"kind placeholders render the kind a slot kept",
			tree.Options{KindPlaceholders: true},
			token.NewPlain(token.Config{}),
			[]string{"took 5ms from 10.0.0.1", "took 7ms from 10.0.0.2", "took x from 10.0.0.3"},
			[][]int{{0, 1, 2}},
			[]string{"took <*> from <ip>"},
		},
		{
			"punctuation at either end keeps the space next to a token",
			tree.Options{},
			token.NewPunct(token.Config{}),
			[]string{"  [ abc 1 ]  ", "[ abc 2 ]"},
			[][]int{{0, 1}},
			[]string{"[ abc <*> ]"},
		},
		{
			"a line its leaf has nothing similar for finds its template by hash",
			tree.Options{SimTh: 1},
			token.NewPlain(token.Config{}),
			[]string{"open 1 file", "open 2 file"},
			[][]int{{0, 1}},
			[]string{"open <*> file"},
		},
	}
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			h := newHarness(t, tc.opt)
			h.tz = tc.tz
			ids := h.finalIDs(h.train(tc.lines...))
			assert.Equal(t, tc.groups, groups(ids))
			assert.ElementsMatch(t, tc.templates, h.templates())
		})
	}
}

func TestEvents(t *testing.T) {
	type want struct {
		kinds    []tree.EventKind
		from, to tree.ID
		text     string
		count    uint64
	}
	h := newHarness(t, tree.Options{})
	ids := h.train("open file a", "open file a", "open file b")
	c := h.tree.Get(ids[2])
	require.NotNil(t, c)
	got := want{h.kinds(), h.events[2].From, h.events[2].To, c.Text(), c.Count()}
	assert.Equal(t, want{[]tree.EventKind{tree.Created, tree.Matched, tree.Changed}, ids[0], ids[2], "open file <*>", 3}, got)
}

func TestEventKindString(t *testing.T) {
	tcs := []struct {
		kind tree.EventKind
		want string
	}{
		{tree.Created, "created"}, {tree.Matched, "matched"}, {tree.Changed, "changed"}, {tree.Evicted, "evicted"}, {0, "none"},
	}
	for _, tc := range tcs {
		t.Run(tc.want, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tc.want, tc.kind.String())
		})
	}
}

func TestSameIDsAcrossTrees(t *testing.T) {
	lines := genLines(rand.New(rand.NewPCG(1, 2)), 2000)
	a, b := newHarness(t, tree.Options{}), newHarness(t, tree.Options{})
	assert.Equal(t, a.finalIDs(a.train(lines...)), b.finalIDs(b.train(lines...)))
	assert.Equal(t, a.tree.Export(), b.tree.Export())
}

func TestDeterminism(t *testing.T) {
	for seed := uint64(0); seed < 20; seed++ {
		t.Run(fmt.Sprintf("seed %d", seed), func(t *testing.T) {
			t.Parallel()
			r := rand.New(rand.NewPCG(seed, seed*7+1))
			lines := genLines(r, 300+r.IntN(700))
			opt := tree.Options{MaxChildren: 2 + r.IntN(10), Spans: seed%3 == 0, Respecialize: seed%4 == 0, SplitMin: 20}
			a, b := newHarness(t, opt), newHarness(t, opt)
			a.train(lines...)
			b.train(lines...)
			assert.Equal(t, a.tree.Export(), b.tree.Export())
			assert.Equal(t, a.events, b.events)
		})
	}
}

func TestCollision(t *testing.T) {
	type want struct {
		salts     []byte
		templates int
		// the line whose template the first line of again ends in
		again int
	}
	tcs := []struct {
		name  string
		lines []string
		// lines whose template is removed before again
		remove []int
		again  []string
		want   want
	}{
		{
			"templates of one hash take the next salts",
			[]string{"alpha one", "beta two three", "gamma"},
			nil,
			[]string{"beta two three"},
			want{[]byte{0, 1, 2}, 3, 1},
		},
		{
			// at SimTh 1 a line with a variable is found by its hash only
			"a removed template leaves no hole before a higher salt",
			[]string{"alpha one", "beta 2 three", "gamma"},
			[]int{0},
			[]string{"beta 3 three"},
			want{[]byte{1, 2}, 2, 1},
		},
	}
	collide := func(uint64) uint64 { return 42 }
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			h := newHarness(t, tree.Options{SimTh: 1})
			tree.SetMix(h.tree, collide)
			ids := h.train(tc.lines...)
			for _, i := range tc.remove {
				require.NoError(t, h.tree.Remove(ids[i], t0))
			}
			again := h.train(tc.again...)
			got := want{templates: len(h.templates()), again: -1}
			st := h.tree.Export()
			for _, cs := range st.Clusters {
				got.salts = append(got.salts, cs.Salt)
			}
			slices.Sort(got.salts)
			for i, id := range ids {
				if id == again[0] {
					got.again = i
				}
			}
			assert.Equal(t, tc.want, got)

			restored := newTree(t, tree.Options{SimTh: 1})
			tree.SetMix(restored, collide)
			require.NoError(t, restored.Import(st))
			assert.Equal(t, st, restored.Export())
		})
	}
}

func TestCollisionSurvivesRebuild(t *testing.T) {
	collide := func(h uint64) uint64 { return h % 3 }
	h := newHarness(t, tree.Options{MaxChildren: 4})
	tree.SetMix(h.tree, collide)
	var lines []string
	for i := 0; i < 40; i++ {
		lines = append(lines, fmt.Sprintf("req%s GET /a done", alpha(i)), fmt.Sprintf("req%s PUT /b failed", alpha(i)))
	}
	ids := h.finalIDs(h.train(lines...))
	st := h.tree.Export()
	require.Len(t, st.Buckets, 1)
	assert.Positive(t, st.Buckets[0].Rebuilds)
	assert.Len(t, groups(ids), 2)
	for _, id := range ids {
		require.NotNil(t, h.tree.Get(id))
	}
	restored := newTree(t, tree.Options{MaxChildren: 4})
	tree.SetMix(restored, collide)
	require.NoError(t, restored.Import(st))
	assert.Equal(t, st, restored.Export())
}

func TestAlias(t *testing.T) {
	type want struct {
		count uint64
		last  time.Time
		live  int
		// a line of the alias ends in the target
		// joined in the tree and viewed in a view published after
		joined, viewed bool
	}
	tcs := []struct {
		name  string
		lines []string
		// line indexes of the alias and its target
		from, to int
		// the lines the target changes by after the alias
		change []string
		want   want
	}{
		{
			"lines of the alias count for the older template",
			[]string{"disk full on sda", "disk full on sdb", "no space left on device sda"},
			2, 1, nil,
			want{4, t0.Add(2 * time.Second), 1, true, true},
		},
		{
			"an alias in another bucket follows a change of its target",
			[]string{"alpha beta gamma", "one two three four"},
			1, 0, []string{"alpha beta delta"},
			want{4, t0.Add(time.Second), 1, true, true},
		},
	}
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			h := newHarness(t, tree.Options{})
			ids := h.train(tc.lines...)
			require.NoError(t, h.tree.Alias(ids[tc.from], ids[tc.to], t0))
			before := h.tree.Publish(nil)
			assert.ErrorIs(t, h.tree.Alias(ids[tc.to], ids[tc.from], t0), tree.ErrNotOlder)
			assert.ErrorIs(t, h.tree.Alias(ids[tc.from], ids[tc.to], t0), tree.ErrAliased)
			assert.ErrorIs(t, h.tree.Alias(0, ids[tc.to], t0), tree.ErrNotFound)
			h.train(tc.change...)
			target := h.finalIDs(ids[tc.to : tc.to+1])[0]
			again := h.train(tc.lines[tc.from])
			line := []byte(tc.lines[tc.from])
			viewed, _, ok := h.tree.Publish(before).Match(line, h.tz.Tokens(line, nil))
			require.True(t, ok)
			x := h.tree.Get(target)
			require.NotNil(t, x)
			got := want{x.Count(), x.Last(), h.tree.Len(), again[0] == target, viewed.ID() == target}
			assert.Equal(t, tc.want, got)
		})
	}
}

// The aliases of a template follow it when it becomes an alias itself or merges away
func TestAliasChains(t *testing.T) {
	type want struct {
		// the template the first alias's line counts for
		template string
		live     int
	}
	tcs := []struct {
		name  string
		opt   tree.Options
		lines []string
		// pairs of line indexes made alias and target in order
		aliases [][2]int
		// trained after the aliases
		after []string
		want  want
	}{
		{
			"an alias of an alias moves to the new target",
			tree.Options{},
			[]string{"a b c d", "x y", "p q r s t"},
			[][2]int{{2, 1}, {1, 0}},
			nil,
			want{"a b c d", 1},
		},
		{
			"an alias moves with a template a rebuild merges",
			tree.Options{MaxChildren: 3},
			[]string{"alpha b", "beta b", "zzz yyy www"},
			[][2]int{{2, 1}},
			[]string{"gamma b"},
			want{"<*> b", 1},
		},
	}
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			h := newHarness(t, tc.opt)
			ids := h.train(tc.lines...)
			for _, a := range tc.aliases {
				require.NoError(t, h.tree.Alias(ids[a[0]], ids[a[1]], t0))
			}
			h.train(tc.after...)
			id := h.train(tc.lines[tc.aliases[0][0]])[0]
			x := h.tree.Get(id)
			require.NotNil(t, x)
			assert.Equal(t, tc.want, want{x.Text(), h.tree.Len()})
		})
	}
}

// Removing a template drops its aliases with it with one Evicted each
func TestRemove(t *testing.T) {
	h := newHarness(t, tree.Options{})
	ids := h.train("a b c", "x y z w", "p q r s t")
	require.NoError(t, h.tree.Alias(ids[2], ids[0], t0))
	h.events = nil
	require.NoError(t, h.tree.Remove(ids[0], t0))
	assert.ErrorIs(t, h.tree.Remove(ids[0], t0), tree.ErrNotFound)
	assert.Equal(t, []tree.Event{{Kind: tree.Evicted, ID: ids[0], At: t0}, {Kind: tree.Evicted, ID: ids[2], At: t0}}, h.events)
	assert.Nil(t, h.tree.Get(ids[2]))
	assert.Equal(t, []string{"x y z w"}, h.templates())
	assert.Equal(t, ids[0], h.train("a b c")[0])
}

func TestSpans(t *testing.T) {
	h := newHarness(t, tree.Options{Spans: true})
	ids := h.finalIDs(h.train("connection closed by peer", "connection closed by remote peer now"))
	assert.Equal(t, ids[0], ids[1])
	assert.Equal(t, []string{"connection closed by <*...>"}, h.templates())
	line := []byte("connection closed by a b c")
	toks := h.tz.Tokens(line, nil)
	c, matched, ok := h.tree.Publish(nil).Match(line, toks)
	require.True(t, ok)
	assert.Equal(t, ids[1], c.ID())
	assert.Equal(t, [][]byte{[]byte("a b c")}, matched.Template().AppendParams(nil, line, toks))
}

func TestRespecialize(t *testing.T) {
	h := newHarness(t, tree.Options{Respecialize: true, SplitMin: 10})
	var lines []string
	for i := 0; i < 30; i++ {
		lines = append(lines, "state changed to "+[]string{"up", "down"}[i%2])
	}
	h.train(lines...)
	assert.ElementsMatch(t, []string{"state changed to <*>", "state changed to up", "state changed to down"}, h.templates())
	got := h.train("state changed to up", "state changed to down", "state changed to flapping")
	texts := make([]string, len(got))
	for i, id := range got {
		texts[i] = h.tree.Get(id).Text()
	}
	assert.Equal(t, []string{"state changed to up", "state changed to down", "state changed to <*>"}, texts)
}

// The values a cluster keeps come back as a copy a caller may change
func TestParams(t *testing.T) {
	h := newHarness(t, tree.Options{KeepValues: 2})
	ids := h.train("user logged in as alice", "user logged in as bob", "user logged in as carol")
	c := h.tree.Get(ids[2])
	require.NotNil(t, c)
	params := c.Params()
	require.Len(t, params, 5)
	assert.Equal(t, [][]string{nil, nil, nil, nil, {"bob", "carol"}}, params)
	params[4][0] = "mallory"
	assert.Equal(t, []string{"bob", "carol"}, c.Params()[4])
}

func TestGetMissing(t *testing.T) {
	assert.Nil(t, newHarness(t, tree.Options{}).tree.Get(0))
}

// A word of letters only for i
// A word with a digit never makes a branch
func alpha(i int) string {
	b := []byte{'a' + byte(i%26)}
	for i /= 26; i > 0; i /= 26 {
		b = append(b, 'a'+byte(i%26))
	}
	return string(b)
}

// Lines from a small set of events with variable words, numbers and ids
func genLines(r *rand.Rand, n int) []string {
	events := []string{
		"user %w logged in from %ip",
		"user %w logged out",
		"request %id took %n ms",
		"%id request failed with code %n",
		"disk %w usage %n percent",
		"job %w finished in %n s with %n items",
		"cache miss for key %w",
		"connection from %ip closed after %n bytes",
		"%w %w %w",
	}
	words := []string{"alice", "bob", "carol", "sda", "sdb", "x1", "pay", "ship", "auth", "zeta"}
	out := make([]string, n)
	for i := range out {
		ev := events[r.IntN(len(events))]
		var b strings.Builder
		for _, f := range strings.Fields(ev) {
			if b.Len() > 0 {
				b.WriteByte(' ')
			}
			switch f {
			case "%w":
				b.WriteString(words[r.IntN(len(words))])
			case "%n":
				fmt.Fprintf(&b, "%d", r.IntN(5000))
			case "%ip":
				fmt.Fprintf(&b, "10.0.%d.%d", r.IntN(256), r.IntN(256))
			case "%id":
				fmt.Fprintf(&b, "id-%x", r.Uint32())
			default:
				b.WriteString(f)
			}
		}
		out[i] = b.String()
	}
	return out
}
