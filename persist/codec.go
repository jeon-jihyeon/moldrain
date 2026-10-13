package persist

import (
	"encoding/binary"
	"fmt"
	"hash/crc32"
	"time"

	"github.com/jeon-jihyeon/moldrain"
)

// A snapshot
// The header and the state with CRC32C over both
func encodeSnapshot(hash, seq uint64, st moldrain.State) []byte {
	e := enc{b: make([]byte, 0, 4096)}
	e.b = append(e.b, magic...)
	e.byte1(formatV1)
	e.b = binary.BigEndian.AppendUint64(e.b, hash)
	e.b = binary.BigEndian.AppendUint64(e.b, seq)
	e.u(st.Seq)
	e.u(st.Lines)
	e.u(uint64(len(st.Buckets)))
	for _, b := range st.Buckets {
		encodeBucket(&e, b)
	}
	e.u(uint64(len(st.Clusters)))
	for _, c := range st.Clusters {
		e.id(c.ID)
		e.byte1(c.Salt)
		e.shape(c.Shape)
		e.u(c.Count)
		e.time(c.First)
		e.time(c.Last)
		e.u(c.Seq)
		e.u(c.Hit)
		e.bool(c.HasAlias)
		e.id(c.Alias)
		e.bool(c.Split)
	}
	return binary.BigEndian.AppendUint32(e.b, crc32.Checksum(e.b, crcTable))
}

func encodeBucket(e *enc, b moldrain.BucketState) {
	e.u(uint64(b.N))
	e.u(uint64(len(b.Pos)))
	for _, p := range b.Pos {
		e.u(uint64(p))
	}
	for _, u := range b.Unstable {
		e.bool(u)
	}
	for _, words := range b.Seen {
		// 0 for a position without a set
		// The word count plus 1 otherwise
		if words == nil {
			e.u(0)
			continue
		}
		e.u(uint64(len(words)) + 1)
		for _, w := range words {
			e.str(w)
		}
	}
	e.u(uint64(b.Rebuilds))
	encodeNode(e, b.Root)
	e.u(uint64(len(b.Order)))
	for _, id := range b.Order {
		e.id(id)
	}
}

func encodeNode(e *enc, n moldrain.NodeState) {
	e.u(uint64(len(n.Keys)))
	for i, k := range n.Keys {
		e.str(k)
		encodeNode(e, n.Kids[i])
	}
	e.bool(n.Wild != nil)
	if n.Wild != nil {
		encodeNode(e, *n.Wild)
	}
	e.u(uint64(len(n.Leaf)))
	for _, id := range n.Leaf {
		e.id(id)
	}
}

type header struct {
	hash uint64
	seq  uint64
}

func decodeSnapshot(b []byte) (header, moldrain.State, error) {
	var h header
	var st moldrain.State
	if len(b) < headerLen+4 || string(b[:4]) != magic {
		return h, st, fmt.Errorf("%w: not a moldrain snapshot", ErrCorrupt)
	}
	if b[4] != formatV1 {
		return h, st, fmt.Errorf("%w: snapshot format %d", ErrFormat, b[4])
	}
	body, sum := b[:len(b)-4], binary.BigEndian.Uint32(b[len(b)-4:])
	if crc32.Checksum(body, crcTable) != sum {
		return h, st, fmt.Errorf("%w: snapshot checksum", ErrCorrupt)
	}
	h.hash = binary.BigEndian.Uint64(b[5:13])
	h.seq = binary.BigEndian.Uint64(b[13:21])
	d := dec{b: body[headerLen:]}
	st.Seq = d.u()
	st.Lines = d.u()
	buckets := d.n()
	for i := 0; i < buckets && d.err == nil; i++ {
		st.Buckets = append(st.Buckets, decodeBucket(&d))
	}
	clusters := d.n()
	for i := 0; i < clusters && d.err == nil; i++ {
		c := moldrain.ClusterState{ID: d.id(), Salt: d.byte1(), Shape: d.shape(), Count: d.u()}
		c.First, c.Last = d.time(), d.time()
		c.Seq, c.Hit = d.u(), d.u()
		c.HasAlias, c.Alias, c.Split = d.bool(), d.id(), d.bool()
		st.Clusters = append(st.Clusters, c)
	}
	if d.err == nil && len(d.b) > 0 {
		d.err = fmt.Errorf("%w: %d bytes after the snapshot", ErrCorrupt, len(d.b))
	}
	return h, st, d.err
}

func decodeBucket(d *dec) moldrain.BucketState {
	b := moldrain.BucketState{N: d.n()}
	pos := d.n()
	for i := 0; i < pos && d.err == nil; i++ {
		b.Pos = append(b.Pos, int(d.u()))
	}
	if d.err != nil || b.N > len(d.b) {
		d.fail("bucket")
		return b
	}
	b.Unstable = make([]bool, b.N)
	for i := range b.Unstable {
		b.Unstable[i] = d.bool()
	}
	b.Seen = make([][]string, b.N)
	for p := 0; p < b.N && d.err == nil; p++ {
		n := d.u()
		if n == 0 {
			continue
		}
		if n-1 > uint64(len(d.b)) {
			d.fail("seen")
			break
		}
		b.Seen[p] = make([]string, 0, n-1)
		for j := uint64(0); j < n-1 && d.err == nil; j++ {
			b.Seen[p] = append(b.Seen[p], d.str())
		}
	}
	b.Rebuilds = int(d.u())
	b.Root = decodeNode(d, 0)
	order := d.n()
	for i := 0; i < order && d.err == nil; i++ {
		b.Order = append(b.Order, d.id())
	}
	return b
}

// Nodes nest one level per branch position
// A deeper nesting is corrupt
const maxNodeDepth = 1 << 10

func decodeNode(d *dec, depth int) moldrain.NodeState {
	var n moldrain.NodeState
	if depth > maxNodeDepth {
		d.fail("node nesting")
		return n
	}
	keys := d.n()
	for i := 0; i < keys && d.err == nil; i++ {
		n.Keys = append(n.Keys, d.str())
		n.Kids = append(n.Kids, decodeNode(d, depth+1))
	}
	if d.bool() {
		w := decodeNode(d, depth+1)
		n.Wild = &w
	}
	leaf := d.n()
	for i := 0; i < leaf && d.err == nil; i++ {
		n.Leaf = append(n.Leaf, d.id())
	}
	return n
}

// Writes change records
// Count deltas name a template by a small index after its first mention since the last snapshot
// A steady stream writes a few bytes per active template per flush
type recordWriter struct {
	refs map[moldrain.ID]uint64
}

func newRecordWriter() *recordWriter { return &recordWriter{refs: map[moldrain.ID]uint64{}} }

func (w *recordWriter) event(seq uint64, ev moldrain.Event) []byte {
	e := enc{}
	e.u(seq)
	switch ev.Kind {
	case moldrain.Created:
		e.byte1(recCreated)
		e.id(ev.ID)
		e.shape(*ev.Shape)
		e.u(ev.N)
		e.time(ev.At)
	case moldrain.Changed:
		e.byte1(recChanged)
		e.id(ev.From)
		e.id(ev.To)
		e.bool(ev.Alias)
		e.bool(ev.Shape != nil)
		if ev.Shape != nil {
			e.shape(*ev.Shape)
		}
		e.u(ev.N)
		e.time(ev.At)
	case moldrain.Evicted:
		e.byte1(recEvicted)
		e.id(ev.ID)
		e.time(ev.At)
	}
	return frame(e.b)
}

// One count delta of a template since the last flush
type delta struct {
	id moldrain.ID
	n  uint64
	at time.Time
}

// All count deltas of one flush in one record
// Each entry's time is kept in whole seconds before the newest
// That is enough for Last
func (w *recordWriter) counts(seq uint64, ds []delta) []byte {
	var newest time.Time
	for _, d := range ds {
		if d.at.After(newest) {
			newest = d.at
		}
	}
	e := enc{}
	e.u(seq)
	e.byte1(recCounts)
	e.time(newest)
	e.u(uint64(len(ds)))
	for _, d := range ds {
		if ref, ok := w.refs[d.id]; ok {
			e.u(ref + 1)
		} else {
			e.u(0)
			e.id(d.id)
			w.refs[d.id] = uint64(len(w.refs))
		}
		e.u(d.n)
		e.u(uint64(newest.Sub(d.at) / time.Second))
	}
	return frame(e.b)
}

// Reads change records in log order
// Keeps the count index the writer built
type recordReader struct {
	refs []moldrain.ID
}

// The seq of one record and its events
// A record at or below after is not decoded
// A copy of an earlier record leaves the count index alone
func (r *recordReader) read(rec []byte, after uint64) (uint64, []moldrain.Event, error) {
	payload, err := unframe(rec)
	if err != nil {
		return 0, nil, err
	}
	d := dec{b: payload}
	seq := d.u()
	if d.err != nil {
		return 0, nil, d.err
	}
	if seq <= after {
		return seq, nil, nil
	}
	var evs []moldrain.Event
	switch kind := d.byte1(); kind {
	case recCreated:
		ev := moldrain.Event{Kind: moldrain.Created, ID: d.id()}
		sh := d.shape()
		ev.Shape, ev.N, ev.At = &sh, d.u(), d.time()
		evs = append(evs, ev)
	case recChanged:
		ev := moldrain.Event{Kind: moldrain.Changed, From: d.id(), To: d.id(), Alias: d.bool()}
		if d.bool() {
			sh := d.shape()
			ev.Shape = &sh
		}
		ev.N, ev.At = d.u(), d.time()
		evs = append(evs, ev)
	case recEvicted:
		evs = append(evs, moldrain.Event{Kind: moldrain.Evicted, ID: d.id(), At: d.time()})
	case recCounts:
		newest := d.time()
		n := d.n()
		for i := 0; i < n && d.err == nil; i++ {
			var id moldrain.ID
			if ref := d.u(); ref == 0 {
				id = d.id()
				r.refs = append(r.refs, id)
			} else if ref-1 < uint64(len(r.refs)) {
				id = r.refs[ref-1]
			} else {
				d.fail("count reference")
			}
			cnt := d.u()
			age := time.Duration(d.u()) * time.Second
			evs = append(evs, moldrain.Event{Kind: moldrain.Matched, ID: id, N: cnt, At: newest.Add(-age)})
		}
	default:
		return 0, nil, fmt.Errorf("%w: record kind %q", ErrCorrupt, kind)
	}
	if d.err != nil {
		return 0, nil, d.err
	}
	return seq, evs, nil
}
