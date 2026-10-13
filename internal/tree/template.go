package tree

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/jeon-jihyeon/moldrain/internal/token"
)

// 16 hex digits of FNV-1a 64 over the template rendered with kinds
// The same template has the same id in every process and after every restart
type ID uint64

func (id ID) String() string {
	var b [16]byte
	const hexdigits = "0123456789abcdef"
	for i := 15; i >= 0; i-- {
		b[i] = hexdigits[id&0xf]
		id >>= 4
	}
	return string(b[:])
}

func ParseID(s string) (ID, error) {
	if len(s) != 16 {
		return 0, fmt.Errorf("%w: %q", ErrID, s)
	}
	v, err := strconv.ParseUint(s, 16, 64)
	if err != nil {
		return 0, fmt.Errorf("%w: %q", ErrID, s)
	}
	return ID(v), nil
}

// One template position
type Slot struct {
	// Interned id of a constant
	// 0 for a variable
	Sym uint32
	// Const for a constant
	// A variable keeps the kind all its values had
	// Var otherwise
	Kind token.Kind
	// Stands for a run of tokens under Spans
	Span bool
	// A constant a Respecialize split fixed
	// A line with another value never merges into it
	Pinned bool
}

func (s Slot) variable() bool { return s.Kind != token.Const }

func (tpl Template) variables() int {
	n := 0
	for _, s := range tpl.Slots {
		if s.variable() {
			n++
		}
	}
	return n
}

// Slots and the text between them
// Gaps has one more entry than Slots
// They sit before the first slot, between slots and after the last
type Template struct {
	Slots []Slot
	Gaps  []string
}

// A template in a form that does not depend on the interner of one process
type Shape struct {
	Parts []Part
	Gaps  []string
}

type Part struct {
	// The constant text
	// Empty for a variable
	Text   string
	Kind   token.Kind
	Span   bool
	Pinned bool
}

// The template with <*> for every variable and <*...> for a span
func (s Shape) String() string {
	var b strings.Builder
	for i, p := range s.Parts {
		if i < len(s.Gaps) {
			b.WriteString(s.Gaps[i])
		}
		if p.Kind == token.Const {
			b.WriteString(p.Text)
		} else {
			b.WriteString(placeholder(Slot{Kind: p.Kind, Span: p.Span}, false))
		}
	}
	if len(s.Gaps) > len(s.Parts) {
		b.WriteString(s.Gaps[len(s.Parts)])
	}
	return b.String()
}

func (t *Tree) shape(tpl Template) Shape {
	parts := make([]Part, len(tpl.Slots))
	for i, s := range tpl.Slots {
		parts[i] = Part{Kind: s.Kind, Span: s.Span, Pinned: s.Pinned}
		if !s.variable() {
			parts[i].Text = t.cfg.in.Text(s.Sym)
		}
	}
	return Shape{Parts: parts, Gaps: append([]string(nil), tpl.Gaps...)}
}

// The template of a recorded shape
// ErrState for a shape no tree makes
func (t *Tree) template(sh Shape) (Template, error) {
	if err := sh.check(); err != nil {
		return Template{}, err
	}
	slots := make([]Slot, len(sh.Parts))
	for i, p := range sh.Parts {
		slots[i] = Slot{Kind: p.Kind, Span: p.Span, Pinned: p.Pinned}
		if p.Kind == token.Const {
			slots[i].Sym = t.cfg.in.Intern([]byte(p.Text))
		}
	}
	gaps := make([]string, len(sh.Gaps))
	for i, g := range sh.Gaps {
		gaps[i] = t.gap(g)
	}
	return Template{Slots: slots, Gaps: gaps}, nil
}

// Checks what the tree assumes of every template
// 1. one gap more than parts
// 2. a known kind
// 3. a span only on a variable and at most one
// 4. a pin only on a constant
func (sh Shape) check() error {
	if len(sh.Gaps) != len(sh.Parts)+1 {
		return fmt.Errorf("%w: %d gaps for %d parts", ErrState, len(sh.Gaps), len(sh.Parts))
	}
	spans := 0
	for i, p := range sh.Parts {
		switch {
		case p.Kind > token.Var:
			return fmt.Errorf("%w: part %d of kind %d", ErrState, i, p.Kind)
		case p.Span && p.Kind == token.Const:
			return fmt.Errorf("%w: part %d is a constant span", ErrState, i)
		case p.Pinned && p.Kind != token.Const:
			return fmt.Errorf("%w: part %d is a pinned variable", ErrState, i)
		}
		if p.Span {
			spans++
		}
	}
	if spans > 1 {
		return fmt.Errorf("%w: %d spans", ErrState, spans)
	}
	return nil
}

// One shared copy of each gap text
// Most templates repeat the same few
func (t *Tree) gap(g string) string {
	if s, ok := t.gaps[g]; ok {
		return s
	}
	t.gaps[g] = g
	return g
}

// Text of a variable slot
// The id always renders kinds
// The display does only with KindPlaceholders
func placeholder(s Slot, kinds bool) string {
	switch {
	case s.Span:
		return "<*...>"
	case kinds:
		return s.Kind.Placeholder()
	default:
		return "<*>"
	}
}

func (t *Tree) render(tpl Template, kinds bool) string {
	var b strings.Builder
	for i, s := range tpl.Slots {
		b.WriteString(tpl.Gaps[i])
		if s.variable() {
			b.WriteString(placeholder(s, kinds))
		} else {
			b.WriteString(t.cfg.in.Text(s.Sym))
		}
	}
	if len(tpl.Gaps) > 0 {
		b.WriteString(tpl.Gaps[len(tpl.Gaps)-1])
	}
	return b.String()
}

// FNV-1a 64 in a streaming form so a line hashes without rendering it
type fnv uint64

const (
	fnvOffset fnv = 14695981039346656037
	fnvPrime  fnv = 1099511628211
)

func (h fnv) bytes(b []byte) fnv {
	for _, c := range b {
		h ^= fnv(c)
		h *= fnvPrime
	}
	return h
}

func (h fnv) str(s string) fnv {
	for i := 0; i < len(s); i++ {
		h ^= fnv(s[i])
		h *= fnvPrime
	}
	return h
}

// A salt is appended after the text
// Salt 0 is the plain hash of the text
func (h fnv) salted(salt byte) ID {
	if salt > 0 {
		h ^= fnv(salt)
		h *= fnvPrime
	}
	return ID(h)
}

// Hash of the template rendered with kinds before the salt
func (t *Tree) hash(tpl Template) fnv {
	h := fnvOffset
	for i, s := range tpl.Slots {
		h = h.str(tpl.Gaps[i])
		if s.variable() {
			h = h.str(placeholder(s, true))
		} else {
			h = h.str(t.cfg.in.Text(s.Sym))
		}
	}
	return h.str(tpl.Gaps[len(tpl.Gaps)-1])
}

// Hash of the template a line would start
// It equals the hash of that template
func lineHash(line []byte, toks []token.Token) fnv {
	h := fnvOffset
	var buf [64]byte
	for i, tk := range toks {
		h = h.bytes(normGap(line, toks, i, buf[:0]))
		if tk.Kind != token.Const {
			h = h.str(tk.Kind.Placeholder())
		} else {
			h = h.bytes(tk.Bytes(line))
		}
	}
	return h.bytes(normGap(line, toks, len(toks), buf[:0]))
}

// The gap before token i
// The gap after the last when i is the token count
// A run of whitespace becomes one space and whitespace at either end of the line goes
// Lines that differ only in spacing share a template
func normGap(line []byte, toks []token.Token, i int, dst []byte) []byte {
	start, end := 0, len(line)
	if i > 0 {
		start = toks[i-1].End
	}
	if i < len(toks) {
		end = toks[i].Start
	}
	// the first gap starts the line and the last ends it
	first, last := i == 0, i == len(toks)
	space := false
	for _, c := range line[start:end] {
		if token.IsSpace(c) {
			space = true
			continue
		}
		if space && (len(dst) > 0 || !first) {
			dst = append(dst, ' ')
		}
		space = false
		dst = append(dst, c)
	}
	if space && !last && (len(dst) > 0 || !first) {
		dst = append(dst, ' ')
	}
	return dst
}

// The template a line starts
// Constants are interned and scanned tokens are variables of their kind
func (t *Tree) lineTemplate(line []byte, toks []token.Token) Template {
	slots := make([]Slot, len(toks))
	for i, tk := range toks {
		if tk.Kind == token.Const {
			slots[i] = Slot{Sym: t.cfg.in.Intern(tk.Bytes(line))}
		} else {
			slots[i] = Slot{Kind: tk.Kind}
		}
	}
	gaps := make([]string, len(toks)+1)
	var buf [64]byte
	for i := range gaps {
		gaps[i] = t.gapBytes(normGap(line, toks, i, buf[:0]))
	}
	return Template{Slots: slots, Gaps: gaps}
}

func (t *Tree) gapBytes(b []byte) string {
	if s, ok := t.gaps[string(b)]; ok {
		return s
	}
	s := string(b)
	t.gaps[s] = s
	return s
}

// Whether the line would start exactly this template
// Renders neither
func sameAsLine(tpl Template, line []byte, toks []token.Token) bool {
	if len(tpl.Slots) != len(toks) {
		return false
	}
	var buf [64]byte
	for i, s := range tpl.Slots {
		tk := toks[i]
		if string(normGap(line, toks, i, buf[:0])) != tpl.Gaps[i] {
			return false
		}
		switch {
		case s.Span:
			return false
		case s.variable():
			if tk.Kind != s.Kind {
				return false
			}
		case tk.Kind != token.Const || tk.Sym != s.Sym:
			return false
		}
	}
	return string(normGap(line, toks, len(toks), buf[:0])) == tpl.Gaps[len(tpl.Gaps)-1]
}

func sameTemplate(a, b Template) bool {
	if len(a.Slots) != len(b.Slots) {
		return false
	}
	for i := range a.Slots {
		x, y := a.Slots[i], b.Slots[i]
		if x.Kind != y.Kind || x.Span != y.Span || (!x.variable() && x.Sym != y.Sym) {
			return false
		}
	}
	for i := range a.Gaps {
		if a.Gaps[i] != b.Gaps[i] {
			return false
		}
	}
	return true
}

// Appends the parameters of a line to dst with one view per variable slot
// A span gives the run of tokens it stands for
func (tpl Template) AppendParams(dst [][]byte, line []byte, toks []token.Token) [][]byte {
	n, m := len(toks), len(tpl.Slots)
	k := spanIndex(tpl)
	for i, s := range tpl.Slots {
		if !s.variable() {
			continue
		}
		if !s.Span {
			j := i
			if k >= 0 && i > k {
				j = n - (m - i)
			}
			if j >= 0 && j < n {
				dst = append(dst, toks[j].Bytes(line))
			}
			continue
		}
		from, to := i, n-(m-i-1)
		switch {
		case to > from && from < n:
			dst = append(dst, line[toks[from].Start:toks[to-1].End])
		case from < n:
			dst = append(dst, line[toks[from].Start:toks[from].Start])
		default:
			dst = append(dst, line[len(line):])
		}
	}
	return dst
}

func spanIndex(tpl Template) int {
	for i, s := range tpl.Slots {
		if s.Span {
			return i
		}
	}
	return -1
}
