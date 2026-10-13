package kafka

import (
	"bytes"
	"testing"

	"github.com/stretchr/testify/assert"
)

// The records a read of the topic would see fed in directly
func TestLoaded(t *testing.T) {
	type rec struct {
		key   string
		value []byte
	}
	type want struct {
		snap    []byte
		changes [][]byte
		stale   []string
		next    uint64
	}
	big := bytes.Repeat([]byte("s"), 2*chunkBytes+10)
	chunks := func(gen uint64, snap []byte) []rec {
		var out []rec
		for i, c := range split(gen, snap) {
			out = append(out, rec{chunkKey(gen, i), c})
		}
		return out
	}
	cat := func(parts ...[]rec) []rec {
		var out []rec
		for _, p := range parts {
			out = append(out, p...)
		}
		return out
	}
	tcs := []struct {
		name string
		recs []rec
		want want
	}{
		{"an empty topic", nil, want{next: 0}},
		{
			"changes without a snapshot",
			[]rec{{"0", []byte("a")}, {"1", []byte("b")}},
			want{nil, [][]byte{[]byte("a"), []byte("b")}, []string{"0", "1"}, 2},
		},
		{
			"a snapshot in three chunks replaces the changes it tombstones",
			cat([]rec{{"0", []byte("a")}, {"1", []byte("b")}}, chunks(2, big), []rec{{"0", nil}, {"1", nil}, {"3", []byte("c")}}),
			want{big, [][]byte{[]byte("c")}, []string{"snapshot.2.0", "snapshot.2.1", "snapshot.2.2", "3"}, 4},
		},
		{
			"an empty snapshot",
			chunks(0, nil),
			want{[]byte{}, nil, []string{"snapshot.0.0"}, 1},
		},
		{
			"a snapshot that never completed keeps the one before and its chunks are stale",
			cat(chunks(0, []byte("one")), chunks(1, big)[:1], []rec{{"2", []byte("c")}}),
			want{[]byte("one"), [][]byte{[]byte("c")}, []string{"snapshot.0.0", "snapshot.1.0", "2"}, 3},
		},
		{
			"a chunk that does not parse is skipped",
			[]rec{{"snapshot.x", []byte{0xff}}},
			want{nil, nil, []string{"snapshot.x"}, 0},
		},
	}
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			st := newLoaded()
			for _, r := range tc.recs {
				st.add([]byte(r.key), r.value)
			}
			got := want{st.snap, st.changes, st.stale(), st.next}
			assert.Equal(t, tc.want, got)
		})
	}
}
