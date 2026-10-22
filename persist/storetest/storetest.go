// Package storetest is the contract suite every persist store passes
package storetest

import (
	"bytes"
	"context"
	"errors"
	"testing"
	"time"

	"github.com/jeon-jihyeon/moldrain"
	"github.com/jeon-jihyeon/moldrain/persist"
)

// Runs the suite. open returns a store over an empty backend each time it is called
func Run(t *testing.T, open func(t *testing.T) persist.Store) {
	t.Run("load empty", func(t *testing.T) {
		snap, changes := load(t, open(t))
		if snap != nil || len(changes) != 0 {
			t.Fatalf("empty store loads %d snapshot bytes and %d records", len(snap), len(changes))
		}
	})
	t.Run("append keeps order", func(t *testing.T) {
		s := open(t)
		recs := records(1, 5)
		appendAll(t, s, recs[:2])
		appendAll(t, s, recs[2:])
		_, got := load(t, s)
		equal(t, recs, got)
	})
	t.Run("replace starts the log over", func(t *testing.T) {
		s := open(t)
		appendAll(t, s, records(1, 3))
		snap := []byte("snapshot one")
		if err := s.Replace(context.Background(), snap); err != nil {
			t.Fatal(err)
		}
		gotSnap, got := load(t, s)
		if !bytes.Equal(gotSnap, snap) || len(got) != 0 {
			t.Fatalf("after replace: snapshot %q and %d records", gotSnap, len(got))
		}
		after := records(4, 2)
		appendAll(t, s, after)
		if err := s.Replace(context.Background(), []byte("snapshot two")); err != nil {
			t.Fatal(err)
		}
		appendAll(t, s, records(6, 1))
		gotSnap, got = load(t, s)
		if string(gotSnap) != "snapshot two" {
			t.Fatalf("snapshot %q", gotSnap)
		}
		equal(t, records(6, 1), got)
	})
	t.Run("a snapshot larger than one message", func(t *testing.T) {
		s := open(t)
		snap := bytes.Repeat([]byte("moldrain"), 3<<17)
		if err := s.Replace(context.Background(), snap); err != nil {
			t.Fatal(err)
		}
		appendAll(t, s, records(1, 1))
		gotSnap, got := load(t, s)
		if !bytes.Equal(gotSnap, snap) {
			t.Fatalf("snapshot of %d bytes loads as %d bytes", len(snap), len(gotSnap))
		}
		equal(t, records(1, 1), got)
	})
	t.Run("torn record dropped", func(t *testing.T) {
		s := open(t)
		recs := records(1, 3)
		torn := records(4, 1)[0]
		appendAll(t, s, recs)
		appendAll(t, s, [][]byte{torn[:len(torn)-3]})
		_, changes, err := s.Load(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		evs, err := persist.ReadChanges(changes)
		if err != nil {
			t.Fatal(err)
		}
		if len(evs) != len(recs) {
			t.Fatalf("read %d events, want the %d whole records", len(evs), len(recs))
		}
	})
	t.Run("records without a snapshot refused", func(t *testing.T) { lostSnapshot(t, open(t)) })
}

// A log whose snapshot was lost fails the load and stays in the store
func lostSnapshot(t *testing.T, s persist.Store) {
	recs := records(1, 2)
	appendAll(t, s, recs)
	m, err := moldrain.New(moldrain.Options{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := persist.Load(context.Background(), m, s); !errors.Is(err, persist.ErrCorrupt) {
		t.Fatalf("load of a log whose snapshot was lost: %v, want ErrCorrupt", err)
	}
	snap, got := load(t, s)
	if snap != nil {
		t.Fatalf("a refused load left a snapshot of %d bytes", len(snap))
	}
	equal(t, recs, got)
}

func load(t *testing.T, s persist.Store) ([]byte, [][]byte) {
	t.Helper()
	snap, changes, err := s.Load(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	var out [][]byte
	if changes == nil {
		return snap, nil
	}
	for r, err := range changes {
		if err != nil {
			t.Fatal(err)
		}
		out = append(out, append([]byte(nil), r...))
	}
	return snap, out
}

func appendAll(t *testing.T, s persist.Store, recs [][]byte) {
	t.Helper()
	if err := s.Append(context.Background(), recs); err != nil {
		t.Fatal(err)
	}
}

func equal(t *testing.T, want, got [][]byte) {
	t.Helper()
	if len(want) != len(got) {
		t.Fatalf("%d records, want %d", len(got), len(want))
	}
	for i := range want {
		if !bytes.Equal(want[i], got[i]) {
			t.Fatalf("record %d differs", i)
		}
	}
}

// n evicted records from seq on
// Each is a valid framed change
func records(seq uint64, n int) [][]byte {
	out := make([][]byte, n)
	at := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	for i := range out {
		out[i] = persist.EncodeChange(seq+uint64(i), moldrain.Event{Kind: moldrain.Evicted, ID: moldrain.ID(seq + uint64(i)), At: at})
	}
	return out
}
