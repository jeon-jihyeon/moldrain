package tree

import "github.com/jeon-jihyeon/moldrain/internal/token"

// The values one variable slot took since the template last changed
type slotTrack struct {
	vals  []string
	lines int
	// more distinct values than SplitValues
	// The slot never splits
	over bool
}

// Records the values of a line in each variable slot and splits a slot whose values stayed few
func (t *Tree) observe(c *Cluster, line []byte, toks []token.Token) {
	if c.split || len(toks) != len(c.tpl.Slots) {
		return
	}
	if c.track == nil {
		c.track = make([]slotTrack, len(c.tpl.Slots))
	}
	for i, s := range c.tpl.Slots {
		tr := &c.track[i]
		if !s.variable() || s.Span || tr.over {
			continue
		}
		v := toks[i].Bytes(line)
		seen := false
		for _, x := range tr.vals {
			if x == string(v) {
				seen = true
				break
			}
		}
		if !seen {
			if len(tr.vals) == t.cfg.opt.SplitValues {
				tr.over = true
				tr.vals = nil
				continue
			}
			tr.vals = append(tr.vals, string(v))
		}
		tr.lines++
		if tr.lines >= t.cfg.opt.SplitMin && len(tr.vals) > 1 {
			t.split(c, i, tr.vals)
			return
		}
	}
}

// One pinned template per value of slot i
// The source keeps its variable slot for any later value
func (t *Tree) split(c *Cluster, i int, vals []string) {
	c.split = true
	c.track = nil
	for _, v := range vals {
		slots := append([]Slot(nil), c.tpl.Slots...)
		slots[i] = Slot{Sym: t.cfg.in.Intern([]byte(v)), Pinned: true}
		t.addTemplate(Template{Slots: slots, Gaps: c.tpl.Gaps}, c.last, nil, 0)
	}
}
