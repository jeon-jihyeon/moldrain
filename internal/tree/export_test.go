package tree

import "github.com/jeon-jihyeon/moldrain/internal/token"

// Replaces the hash of every template before its salt so a test forces collisions
func SetMix(t *Tree, mix func(uint64) uint64) {
	t.cfg.mix = func(h fnv) fnv { return fnv(mix(uint64(h))) }
}

// The template the live tree finds for a line by a full scan of its leaf
func FindLive(t *Tree, line []byte, toks []token.Token) *Cluster {
	lookup(t.cfg.in, line, toks)
	return t.cfg.find(t, t.at(len(toks)), line, toks).c
}

// The largest leaf of a view in templates
func LargestLeaf(v *View) int {
	n := 0
	for _, b := range v.buckets {
		if b != nil {
			b.walk(func(nd *node) { n = max(n, len(nd.leaf)) })
		}
	}
	return n
}
