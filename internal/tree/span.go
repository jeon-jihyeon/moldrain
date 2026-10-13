package tree

import "github.com/jeon-jihyeon/moldrain/internal/token"

// The best template of a neighbor token count the line can join through one span slot
// Also the template it would become
func (cfg *config) spanFind(f forest, toks []token.Token) (*Cluster, Template) {
	n := len(toks)
	var best *Cluster
	var bestTpl Template
	bestSim := -1.0
	for d := 1; d <= cfg.opt.MaxSpan; d++ {
		for _, m := range [2]int{n - d, n + d} {
			b := f.at(m)
			if m < 1 || b == nil {
				continue
			}
			for _, c := range b.order {
				if c.alias != nil {
					continue
				}
				sim, tpl, ok := spanAlign(c.tpl, toks)
				if !ok || sim < cfg.opt.SimTh {
					continue
				}
				// ties go to the template that keeps fewer variable slots
				if sim > bestSim || (sim == bestSim && tpl.variables() < bestTpl.variables()) {
					best, bestTpl, bestSim = c, tpl, sim
				}
			}
		}
	}
	return best, bestTpl
}

// Aligns a line with a template of another length through one span slot
// A template that has a span keeps it
// One without gets it between the longest common prefix and suffix
func spanAlign(tpl Template, toks []token.Token) (sim float64, out Template, ok bool) {
	if k := spanIndex(tpl); k >= 0 {
		return spanKeep(tpl, toks, k)
	}
	return spanNew(tpl, toks)
}

// Aligns a line with a template that has a span at k
// The slots before it align from the start and the rest from the end
func spanKeep(tpl Template, toks []token.Token, k int) (sim float64, out Template, ok bool) {
	m, n := len(tpl.Slots), len(toks)
	pre, suf := k, m-k-1
	if pre+suf == 0 || n < pre+suf {
		return 0, Template{}, false
	}
	slots := append([]Slot(nil), tpl.Slots...)
	score := 0
	for i := 0; i < m; i++ {
		if i == k {
			continue
		}
		j := i
		if i > k {
			j = n - (m - i)
		}
		s, tk := tpl.Slots[i], toks[j]
		hit, ok := slotHit(s, tk)
		if !ok {
			return 0, Template{}, false
		}
		if hit {
			score++
		}
		slots[i] = mergeSlot(s, tk)
	}
	return float64(score) / float64(pre+suf), Template{Slots: slots, Gaps: tpl.Gaps}, true
}

// Gives a template without a span one between its longest common prefix and suffix with the line
func spanNew(tpl Template, toks []token.Token) (sim float64, out Template, ok bool) {
	m, n := len(tpl.Slots), len(toks)
	lim := min(m, n)
	p := 0
	for p < lim && aligned(tpl.Slots[p], toks[p]) {
		p++
	}
	s := 0
	for s < lim-p && aligned(tpl.Slots[m-1-s], toks[n-1-s]) {
		s++
	}
	if p+s == 0 {
		return 0, Template{}, false
	}
	score := 0
	for i := 0; i < p; i++ {
		if hit, _ := slotHit(tpl.Slots[i], toks[i]); hit {
			score++
		}
	}
	for i := 0; i < s; i++ {
		if hit, _ := slotHit(tpl.Slots[m-1-i], toks[n-1-i]); hit {
			score++
		}
	}
	slots := make([]Slot, 0, p+s+1)
	slots = append(slots, tpl.Slots[:p]...)
	slots = append(slots, Slot{Kind: token.Var, Span: true})
	slots = append(slots, tpl.Slots[m-s:]...)
	gaps := make([]string, 0, p+s+2)
	gaps = append(gaps, tpl.Gaps[:p+1]...)
	gaps = append(gaps, tpl.Gaps[m-s:]...)
	return float64(score) / float64(max(m, n)), Template{Slots: slots, Gaps: gaps}, true
}

// Whether a token lines up with a slot when looking for the common prefix and suffix
func aligned(s Slot, tk token.Token) bool {
	if s.Span {
		return false
	}
	if s.variable() {
		return true
	}
	return tk.Kind == token.Const && tk.Sym == s.Sym
}

// Whether a slot counts toward similarity for a token
// Only a constant equal to the token counts as Drain3 does while it trains
// Counting variable slots too lets a template with many variables absorb unrelated lines of its length
// ok is false when a pinned slot differs
// The template is then no candidate at all
func slotHit(s Slot, tk token.Token) (hit, ok bool) {
	switch {
	case s.variable():
		return false, true
	case tk.Kind == token.Const && tk.Sym == s.Sym:
		return true, true
	}
	return false, !s.Pinned
}
