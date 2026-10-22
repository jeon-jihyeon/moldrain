package persist

import (
	"encoding/binary"
	"fmt"
	"hash/crc32"
	"math"
	"time"

	"github.com/jeon-jihyeon/moldrain"
)

// Snapshot header
// 1. magic
// 2. format version
// 3. options hash
// 4. seq of the last change the snapshot holds
const (
	magic     = "MOLD"
	formatV1  = 1
	headerLen = 4 + 1 + 8 + 8
)

// Bytes before the payload of a change record
// Its length and its CRC32C
const frameHeadLen = 8

// Record kinds of the change log
const (
	recCreated byte = 'C' // a template created with its shape
	recChanged byte = 'X' // a template that changed id or became an alias
	recEvicted byte = 'E' // a template evicted under the cap
	recCounts  byte = 'K' // the count deltas of one flush
)

var crcTable = crc32.MakeTable(crc32.Castagnoli)

// Frames one record
// The length and CRC32C of the payload come before it
// A torn last record fails its length or its CRC and is dropped on load
func frame(payload []byte) []byte {
	out := make([]byte, frameHeadLen, frameHeadLen+len(payload))
	binary.BigEndian.PutUint32(out[0:4], uint32(len(payload)))
	binary.BigEndian.PutUint32(out[4:8], crc32.Checksum(payload, crcTable))
	return append(out, payload...)
}

// The payload of one framed record
// ErrCorrupt when the frame does not hold
func unframe(rec []byte) ([]byte, error) {
	if len(rec) < frameHeadLen {
		return nil, fmt.Errorf("%w: record of %d bytes", ErrCorrupt, len(rec))
	}
	n := binary.BigEndian.Uint32(rec[0:4])
	if uint64(len(rec)-frameHeadLen) != uint64(n) {
		return nil, fmt.Errorf("%w: record holds %d bytes, says %d", ErrCorrupt, len(rec)-frameHeadLen, n)
	}
	payload := rec[frameHeadLen:]
	if crc32.Checksum(payload, crcTable) != binary.BigEndian.Uint32(rec[4:8]) {
		return nil, fmt.Errorf("%w: record checksum", ErrCorrupt)
	}
	return payload, nil
}

// Splits concatenated frames for stores that keep the log as one byte stream
// good is the length of the whole frames
// A caller cuts a torn tail there
// A torn tail is a frame that runs past the end or a bad frame nothing follows
// A bad frame with bytes after it fails with ErrCorrupt
func splitFrames(b []byte) (frames [][]byte, good int, err error) {
	for len(b)-good >= frameHeadLen {
		n := uint64(binary.BigEndian.Uint32(b[good : good+4]))
		if n > uint64(len(b)-good-frameHeadLen) {
			break
		}
		end := good + frameHeadLen + int(n)
		if _, err := unframe(b[good:end]); err != nil {
			if end < len(b) {
				return nil, good, fmt.Errorf("%w: bad record at byte %d", err, good)
			}
			break
		}
		frames = append(frames, b[good:end])
		good = end
	}
	return frames, good, nil
}

type enc struct{ b []byte }

func (e *enc) u(v uint64)   { e.b = binary.AppendUvarint(e.b, v) }
func (e *enc) i(v int64)    { e.b = binary.AppendVarint(e.b, v) }
func (e *enc) byte1(v byte) { e.b = append(e.b, v) }
func (e *enc) id(v moldrain.ID) {
	e.b = binary.BigEndian.AppendUint64(e.b, uint64(v))
}
func (e *enc) bool(v bool) {
	if v {
		e.byte1(1)
	} else {
		e.byte1(0)
	}
}
func (e *enc) str(s string) {
	e.u(uint64(len(s)))
	e.b = append(e.b, s...)
}

// A zero time stays zero after a round trip
func (e *enc) time(t time.Time) {
	if t.IsZero() {
		e.byte1(0)
		return
	}
	e.byte1(1)
	e.i(t.UnixNano())
}

func (e *enc) shape(s moldrain.Shape) {
	e.u(uint64(len(s.Parts)))
	for _, p := range s.Parts {
		e.byte1(byte(p.Kind))
		var flags byte
		if p.Span {
			flags |= 1
		}
		if p.Pinned {
			flags |= 2
		}
		e.byte1(flags)
		e.str(p.Text)
	}
	e.u(uint64(len(s.Gaps)))
	for _, g := range s.Gaps {
		e.str(g)
	}
}

type dec struct {
	b   []byte
	err error
}

func (d *dec) fail(what string) {
	if d.err == nil {
		d.err = fmt.Errorf("%w: short %s", ErrCorrupt, what)
	}
}

func (d *dec) u() uint64 {
	v, n := binary.Uvarint(d.b)
	if n <= 0 {
		d.fail("uvarint")
		return 0
	}
	d.b = d.b[n:]
	return v
}

// A length that must fit the bytes left
// A corrupt length never allocates much
func (d *dec) n() int {
	v := d.u()
	if v > uint64(len(d.b)) || v > math.MaxInt32 {
		d.fail("length")
		return 0
	}
	return int(v)
}

func (d *dec) i() int64 {
	v, n := binary.Varint(d.b)
	if n <= 0 {
		d.fail("varint")
		return 0
	}
	d.b = d.b[n:]
	return v
}

func (d *dec) byte1() byte {
	if len(d.b) < 1 {
		d.fail("byte")
		return 0
	}
	v := d.b[0]
	d.b = d.b[1:]
	return v
}

func (d *dec) bool() bool { return d.byte1() == 1 }

func (d *dec) id() moldrain.ID {
	if len(d.b) < 8 {
		d.fail("id")
		return 0
	}
	v := binary.BigEndian.Uint64(d.b)
	d.b = d.b[8:]
	return moldrain.ID(v)
}

func (d *dec) str() string {
	n := d.n()
	if d.err != nil {
		return ""
	}
	s := string(d.b[:n])
	d.b = d.b[n:]
	return s
}

func (d *dec) time() time.Time {
	if d.byte1() == 0 {
		return time.Time{}
	}
	return time.Unix(0, d.i()).UTC()
}

func (d *dec) shape() moldrain.Shape {
	var s moldrain.Shape
	parts := d.n()
	for i := 0; i < parts && d.err == nil; i++ {
		kind := d.byte1()
		flags := d.byte1()
		s.Parts = append(s.Parts, moldrain.Part{Kind: moldrain.Kind(kind), Span: flags&1 != 0, Pinned: flags&2 != 0, Text: d.str()})
	}
	gaps := d.n()
	for i := 0; i < gaps && d.err == nil; i++ {
		s.Gaps = append(s.Gaps, d.str())
	}
	return s
}
