package tree_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jeon-jihyeon/moldrain/internal/token"
	"github.com/jeon-jihyeon/moldrain/internal/tree"
)

func TestParseID(t *testing.T) {
	tcs := []struct {
		name string
		text string
		want tree.ID
		err  error
	}{
		{"16 hex digits", "00000000000000ff", 255, nil},
		{"the largest", "ffffffffffffffff", ^tree.ID(0), nil},
		{"too short", "ff", 0, tree.ErrID},
		{"not hex", "000000000000000g", 0, tree.ErrID},
	}
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			id, err := tree.ParseID(tc.text)
			assert.ErrorIs(t, err, tc.err)
			assert.Equal(t, tc.want, id)
		})
	}
}

func TestIDString(t *testing.T) {
	tcs := []struct {
		id   tree.ID
		want string
	}{
		{0, "0000000000000000"}, {255, "00000000000000ff"}, {^tree.ID(0), "ffffffffffffffff"},
	}
	for _, tc := range tcs {
		t.Run(tc.want, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tc.want, tc.id.String())
			back, err := tree.ParseID(tc.want)
			require.NoError(t, err)
			assert.Equal(t, tc.id, back)
		})
	}
}

func TestShapeString(t *testing.T) {
	sh := tree.Shape{
		Parts: []tree.Part{{Text: "took"}, {Kind: token.Num}, {Kind: token.Var, Span: true}},
		Gaps:  []string{"[", " ", " ", "]"},
	}
	assert.Equal(t, "[took <*> <*...>]", sh.String())
}

// A span's parameter is the run of tokens it stands for
// It is empty when the run is
func TestAppendParams(t *testing.T) {
	tcs := []struct {
		name string
		line string
		want []string
	}{
		{"a run of three", "conn closed by a b c", []string{"a b c"}},
		{"a run of one", "conn closed by x", []string{"x"}},
	}
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			h := newHarness(t, tree.Options{Spans: true})
			h.train("conn closed by peer", "conn closed by remote peer now")
			toks := h.tz.Tokens([]byte(tc.line), nil)
			_, matched, ok := h.tree.Publish(nil).Match([]byte(tc.line), toks)
			require.True(t, ok)
			var got []string
			for _, p := range matched.Template().AppendParams(nil, []byte(tc.line), toks) {
				got = append(got, string(p))
			}
			assert.Equal(t, tc.want, got)
		})
	}
}
