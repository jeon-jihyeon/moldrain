package tree_test

import (
	"math/rand/v2"
	"testing"

	"github.com/jeon-jihyeon/moldrain/internal/token"
	"github.com/jeon-jihyeon/moldrain/internal/tree"
)

func BenchmarkTrain(b *testing.B) {
	lines := genLines(rand.New(rand.NewPCG(1, 1)), 10000)
	tz := token.NewPlain(token.Config{})
	tr := newTree(b, tree.Options{})
	var toks []token.Token
	for _, l := range lines {
		toks = tz.Tokens([]byte(l), toks[:0])
		tr.Train([]byte(l), toks, t0)
	}
	bl := make([][]byte, len(lines))
	for i, l := range lines {
		bl[i] = []byte(l)
	}
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		l := bl[i%len(bl)]
		toks = tz.Tokens(l, toks[:0])
		tr.Train(l, toks, t0)
	}
}

func BenchmarkMatch(b *testing.B) {
	lines := genLines(rand.New(rand.NewPCG(1, 1)), 10000)
	tz := token.NewPlain(token.Config{})
	tr := newTree(b, tree.Options{})
	var toks []token.Token
	bl := make([][]byte, len(lines))
	for i, l := range lines {
		bl[i] = []byte(l)
		toks = tz.Tokens(bl[i], toks[:0])
		tr.Train(bl[i], toks, t0)
	}
	v := tr.Publish(nil)
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		l := bl[i%len(bl)]
		toks = tz.Tokens(l, toks[:0])
		v.Match(l, toks)
	}
}
