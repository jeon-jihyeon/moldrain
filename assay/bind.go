package assay

import (
	"slices"

	"github.com/jeon-jihyeon/moldrain"
)

// Earlier ids kept per live template for Results taken before a change
const maxForwards = 32

// Where a value and a group sit in the params of one template form
type form struct {
	params int
	// index into the params
	// -1 when the form has no such slot
	value int
	group int
}

// A profile bound to a live template with the forms its lines come in
type route struct {
	p  *profile
	id moldrain.ID
	// by the id of each form
	// The template itself and its aliases and its earlier ids
	forms map[moldrain.ID]form
}

// The form of a Result's own shape
// The alias it matched or else its id
// A form unknown or with another slot count reads nothing
// The value counts as skipped
func (r *route) pick(res *moldrain.Result) form {
	key := res.ID
	if res.Alias != 0 {
		key = res.Alias
	}
	if f, ok := r.forms[key]; ok && f.params == len(res.Params) {
		return f
	}
	return form{params: len(res.Params), value: -1, group: -1}
}

// Binding state guarded by the Assay's mu
type binds struct {
	// every live template
	// A later Alias needs the shape of a template no profile bound
	shapes map[moldrain.ID]moldrain.Shape
	// shapes of the aliases and earlier ids of each live template by their own id
	forms map[moldrain.ID]map[moldrain.ID]moldrain.Shape
	// earlier ids of each live template with the oldest first
	// A Result taken before the change still carries one and routes to the live template
	forwards map[moldrain.ID][]moldrain.ID
	// ids that live on as aliases
	// Their form stays after their forward goes
	aliases map[moldrain.ID]struct{}
	bound   map[moldrain.ID][]*profile
}

func newBinds() binds {
	return binds{
		shapes:   map[moldrain.ID]moldrain.Shape{},
		forms:    map[moldrain.ID]map[moldrain.ID]moldrain.Shape{},
		forwards: map[moldrain.ID][]moldrain.ID{},
		aliases:  map[moldrain.ID]struct{}{},
		bound:    map[moldrain.ID][]*profile{},
	}
}

// Called on the training goroutine under the miner's writer mutex
// An event before the snapshot of New is applied too
// The snapshot then repeats it to the same bindings
func (a *Assay) onEvent(ev moldrain.Event) {
	if ev.Kind == moldrain.Matched || a.closed.Load() {
		return
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	switch {
	case ev.Kind == moldrain.Created && ev.Shape != nil:
		a.created(ev.ID, *ev.Shape)
		a.publish(ev.ID, nil)
	case ev.Kind == moldrain.Changed && ev.Alias:
		a.b.aliases[ev.From] = struct{}{}
		a.publish(ev.To, a.absorb(ev.From, ev.To))
	case ev.Kind == moldrain.Changed && ev.Shape != nil:
		gone := a.absorb(ev.From, ev.To)
		a.changed(ev.To, *ev.Shape)
		a.publish(ev.To, gone)
	case ev.Kind == moldrain.Evicted:
		a.drop(ev.ID)
		gone := a.b.forwards[ev.ID]
		delete(a.b.forwards, ev.ID)
		delete(a.b.forms, ev.ID)
		delete(a.b.aliases, ev.ID)
		a.publish(ev.ID, gone)
	}
}

// Rebuilds the bindings from the miner's templates
// Runs under the miner's writer mutex after Subscribe
// No event slips between the state and the subscription
func (a *Assay) load(st moldrain.State) {
	a.mu.Lock()
	defer a.mu.Unlock()
	for _, c := range st.Clusters {
		if !c.HasAlias {
			a.created(c.ID, c.Shape)
		}
	}
	for _, c := range st.Clusters {
		if c.HasAlias {
			a.formsOf(c.Alias)[c.ID] = c.Shape
			a.b.aliases[c.ID] = struct{}{}
		}
	}
	for id := range a.b.bound {
		a.publish(id, nil)
	}
}

func (a *Assay) formsOf(id moldrain.ID) map[moldrain.ID]moldrain.Shape {
	fs := a.b.forms[id]
	if fs == nil {
		fs = map[moldrain.ID]moldrain.Shape{}
		a.b.forms[id] = fs
	}
	return fs
}

// Matches a new template against every profile
func (a *Assay) created(id moldrain.ID, sh moldrain.Shape) {
	a.b.shapes[id] = sh
	ws := words(sh)
	for _, p := range a.profiles {
		a.try(p, id, sh, ws)
	}
}

func (a *Assay) try(p *profile, id moldrain.ID, sh moldrain.Shape, ws []string) {
	if !p.matches(id, ws) {
		return
	}
	if !p.fits(sh) {
		p.noValue[id] = struct{}{}
		return
	}
	a.bind(p, id)
}

// Whether the shape has the slots the profile reads
func (p *profile) fits(sh moldrain.Shape) bool {
	f := p.form(sh)
	return (p.value == nil || f.value >= 0) && (p.group == nil || f.group >= 0)
}

func (a *Assay) bind(p *profile, id moldrain.ID) {
	delete(p.noValue, id)
	if _, ok := p.bound[id]; ok {
		return
	}
	p.bound[id] = struct{}{}
	a.b.bound[id] = append(a.b.bound[id], p)
}

// Forgets a live template and returns the profiles that were bound to it
func (a *Assay) drop(id moldrain.ID) []*profile {
	ps := a.b.bound[id]
	for _, p := range ps {
		delete(p.bound, id)
	}
	for _, p := range a.profiles {
		delete(p.noValue, id)
	}
	delete(a.b.bound, id)
	delete(a.b.shapes, id)
	return ps
}

// Moves from into to
// 1. its bindings
// 2. its shape as a form of to
// 3. its id as a forward to to
// A moved binding stays even when to lacks the value slot
// Its lines then count as skipped
// Returns the forwards dropped past maxForwards
func (a *Assay) absorb(from, to moldrain.ID) []moldrain.ID {
	sh, ok := a.b.shapes[from]
	for _, p := range a.drop(from) {
		a.bind(p, to)
	}
	fs := a.formsOf(to)
	if ok {
		fs[from] = sh
	}
	for id, s := range a.b.forms[from] {
		fs[id] = s
	}
	delete(a.b.forms, from)
	fw := a.b.forwards[to]
	for _, id := range append(a.b.forwards[from], from) {
		if !slices.Contains(fw, id) {
			fw = append(fw, id)
		}
	}
	delete(a.b.forwards, from)
	var gone []moldrain.ID
	for len(fw) > maxForwards {
		old := fw[0]
		fw = fw[1:]
		gone = append(gone, old)
		if _, alias := a.b.aliases[old]; !alias {
			delete(fs, old)
		}
	}
	a.b.forwards[to] = fw
	return gone
}

// Takes the new shape of to and matches it again
// A profile that fits it only now binds too
func (a *Assay) changed(to moldrain.ID, sh moldrain.Shape) {
	a.b.shapes[to] = sh
	ws := words(sh)
	for _, p := range a.profiles {
		if _, ok := p.bound[to]; !ok {
			a.try(p, to, sh, ws)
		}
	}
}

// Swaps in a copy of the routes with the live template id and its forwards rebuilt and the gone ids removed
// Observe reads them without a lock
// A template no profile binds and none routed leaves the routes as they are
// Most Created events copy nothing
func (a *Assay) publish(id moldrain.ID, gone []moldrain.ID) {
	old := *a.routes.Load()
	_, had := old[id]
	touched := had || len(a.b.bound[id]) > 0 || len(a.b.forwards[id]) > 0
	for _, g := range gone {
		_, routed := old[g]
		touched = touched || routed
	}
	if !touched {
		return
	}
	next := make(map[moldrain.ID][]route, len(old)+1)
	for k, rs := range old {
		next[k] = rs
	}
	for _, g := range gone {
		delete(next, g)
	}
	rs := a.routesOf(id)
	for _, f := range append(a.b.forwards[id], id) {
		delete(next, f)
		if rs != nil {
			next[f] = rs
		}
	}
	a.routes.Store(&next)
}

func (a *Assay) routesOf(id moldrain.ID) []route {
	ps := a.b.bound[id]
	if len(ps) == 0 {
		return nil
	}
	rs := make([]route, len(ps))
	for i, p := range ps {
		forms := make(map[moldrain.ID]form, len(a.b.forms[id])+1)
		forms[id] = p.form(a.b.shapes[id])
		for fid, sh := range a.b.forms[id] {
			forms[fid] = p.form(sh)
		}
		rs[i] = route{p: p, id: id, forms: forms}
	}
	return rs
}

func (p *profile) form(sh moldrain.Shape) form {
	f := form{value: -1, group: -1}
	for _, part := range sh.Parts {
		if part.Kind != moldrain.Const {
			f.params++
		}
	}
	if p.value != nil {
		if i, ok := locate(p.value, sh); ok {
			f.value = i
		}
	}
	if p.group != nil {
		if i, ok := locate(p.group, sh); ok {
			f.group = i
		}
	}
	return f
}
