package token_test

import (
	"testing"

	"github.com/jeon-jihyeon/moldrain/internal/token"
)

var benchLines = []struct {
	name string
	tz   token.Tokenizer
	line string
}{
	{"Plain", token.NewPlain(token.Config{}), "260301 101500 42 INFO store.Node$Writer: Writer 1 for block blk_12345678901234567 terminating"},
	{"Punct", token.NewPunct(token.Config{}), "Accepted password for root from 10.0.0.1 port 22 ssh2 user=root uid=0 took=15ms"},
	{"JSON", token.NewJSON(token.Config{}), `{"level":"info","msg":"request done","status":200,"ms":12.5,"ip":"10.0.0.1"}`},
	{"Logfmt", token.NewLogfmt(token.Config{}), `level=info msg="request done" status=200 ms=12.5 ip=10.0.0.1`},
}

func BenchmarkTokens(b *testing.B) {
	for _, bl := range benchLines {
		b.Run(bl.name, func(b *testing.B) {
			line := []byte(bl.line)
			dst := bl.tz.Tokens(line, nil)
			b.ReportAllocs()
			b.SetBytes(int64(len(line)))
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				dst = bl.tz.Tokens(line, dst[:0])
			}
		})
	}
}

func BenchmarkLookup(b *testing.B) {
	in := token.NewInterner()
	words := [][]byte{[]byte("Writer"), []byte("terminating"), []byte("block"), []byte("INFO")}
	for _, w := range words {
		in.Intern(w)
	}
	b.ReportAllocs()
	b.RunParallel(func(pb *testing.PB) {
		i := 0
		for pb.Next() {
			in.Lookup(words[i%len(words)])
			i++
		}
	})
}

// Allocation per op after warm up
// The success criterion for Plain and Punct
func TestZeroAllocs(t *testing.T) {
	for _, bl := range benchLines[:2] {
		line := []byte(bl.line)
		dst := bl.tz.Tokens(line, nil)
		got := testing.AllocsPerRun(100, func() { dst = bl.tz.Tokens(line, dst[:0]) })
		if got != 0 {
			t.Errorf("%s allocates %v per line", bl.name, got)
		}
	}
}
