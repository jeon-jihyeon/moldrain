package assay

import (
	"encoding/json"
	"fmt"
	"io"
	"slices"
	"strings"

	"github.com/jeon-jihyeon/moldrain"
)

// What one aggregate is computed over and how
type Profile struct {
	// The unique key of every record
	Name string `json:"name"`
	// Tokens of the template text
	// <*> stands for one variable slot and * for any run of tokens
	// Set this or IDs
	Match string `json:"match,omitempty"`
	// Template ids in hex followed through Changed and Alias
	IDs []string `json:"ids,omitempty"`
	// Where the number comes from
	// nil when every op is count or distinct of lines
	Value *Locator `json:"value,omitempty"`
	Unit  Unit     `json:"unit,omitempty"`
	Ops   []Op     `json:"ops"`
	// Where the group name comes from
	// nil for one group
	GroupBy *Locator `json:"group_by,omitempty"`
	// Groups per window with 100 by default
	// Lines of any further group go to the group other
	MaxGroups int `json:"max_groups,omitempty"`
}

// A variable slot of a template
// Named by Slot or by After but not both
type Locator struct {
	// Counts variable slots from 0
	Slot int `json:"slot,omitempty"`
	// A constant token such as latency= or a JSON key
	// The slot right after it must be variable
	After string `json:"after,omitempty"`
}

type Op string

const (
	Count    Op = "count"    // lines
	Sum      Op = "sum"      // values read
	Min      Op = "min"      // smallest value
	Max      Op = "max"      // largest value
	Mean     Op = "mean"     // sum divided by the count of values read
	Distinct Op = "distinct" // distinct values or distinct lines without Value
	P50      Op = "p50"      // median from the sketch
	P90      Op = "p90"      // 90th percentile from the sketch
	P95      Op = "p95"      // 95th percentile from the sketch
	P99      Op = "p99"      // 99th percentile from the sketch
)

var allOps = []Op{Count, Sum, Min, Max, Mean, Distinct, P50, P90, P95, P99}

// The quantile of a percentile op
// 0 for the other ops
func (o Op) quantile() float64 {
	switch o {
	case P50:
		return 0.5
	case P90:
		return 0.9
	case P95:
		return 0.95
	case P99:
		return 0.99
	}
	return 0
}

// Ops that need a value of each line
func (o Op) valued() bool { return o != Count && o != Distinct }

type Unit string

const (
	NoUnit       Unit = ""      // plain numbers only
	Milliseconds Unit = "ms"    // durations such as 120ms or 1.2s read as milliseconds
	Seconds      Unit = "s"     // durations such as 120ms or 1.2s read as seconds
	Bytes        Unit = "bytes" // sizes such as 512B or 4KiB read as bytes
)

// Default of Profile
const DefaultMaxGroups = 100

// Reads a JSON array of profiles and validates them
func LoadProfiles(r io.Reader) ([]Profile, error) {
	dec := json.NewDecoder(r)
	dec.DisallowUnknownFields()
	var ps []Profile
	if err := dec.Decode(&ps); err != nil {
		return nil, fmt.Errorf("%w: %v", ErrProfile, err)
	}
	if _, err := compile(ps); err != nil {
		return nil, err
	}
	return ps, nil
}

// A validated profile with its pattern split and its ids parsed
type spec struct {
	Profile
	ids       map[moldrain.ID]struct{}
	pattern   []string
	value     *Locator
	group     *Locator
	quantiles bool
	distinct  bool
}

func compile(ps []Profile) ([]spec, error) {
	out := make([]spec, len(ps))
	seen := map[string]bool{}
	for i, p := range ps {
		s, err := compileOne(p)
		if err != nil {
			return nil, fmt.Errorf("%w: profile %d %q: %v", ErrProfile, i, p.Name, err)
		}
		if seen[p.Name] {
			return nil, fmt.Errorf("%w: profile %d %q: Name is not unique", ErrProfile, i, p.Name)
		}
		seen[p.Name] = true
		out[i] = s
	}
	return out, nil
}

func compileOne(p Profile) (spec, error) {
	s := spec{Profile: p, pattern: strings.Fields(p.Match), value: p.Value, group: p.GroupBy}
	if s.MaxGroups == 0 {
		s.MaxGroups = DefaultMaxGroups
	}
	switch {
	case p.Name == "":
		return s, fmt.Errorf("field Name is empty")
	case len(s.pattern) == 0 && len(p.IDs) == 0:
		return s, fmt.Errorf("fields Match and IDs are both empty")
	case len(s.pattern) > 0 && len(p.IDs) > 0:
		return s, fmt.Errorf("fields Match and IDs are both set")
	case p.MaxGroups < 0:
		return s, fmt.Errorf("field MaxGroups %d is negative", p.MaxGroups)
	case len(p.Ops) == 0:
		return s, fmt.Errorf("field Ops is empty")
	}
	if _, ok := unitFactors[p.Unit]; !ok {
		return s, fmt.Errorf("field Unit %q is none of ms, s and bytes", p.Unit)
	}
	if err := checkLocator("Value", p.Value); err != nil {
		return s, err
	}
	if err := checkLocator("GroupBy", p.GroupBy); err != nil {
		return s, err
	}
	if err := s.parseIDs(); err != nil {
		return s, err
	}
	return s, s.compileOps()
}

func (s *spec) parseIDs() error {
	if len(s.IDs) == 0 {
		return nil
	}
	s.ids = make(map[moldrain.ID]struct{}, len(s.IDs))
	for _, h := range s.IDs {
		id, err := moldrain.ParseID(h)
		if err != nil {
			return fmt.Errorf("field IDs: %v", err)
		}
		s.ids[id] = struct{}{}
	}
	return nil
}

// An op listed twice is kept once
// Each op gives one record
func (s *spec) compileOps() error {
	var ops []Op
	for _, op := range s.Ops {
		switch {
		case !slices.Contains(allOps, op):
			return fmt.Errorf("field Ops %q is not an op", op)
		case op.valued() && s.value == nil:
			return fmt.Errorf("field Ops %q needs Value", op)
		case slices.Contains(ops, op):
			continue
		}
		ops = append(ops, op)
		s.quantiles = s.quantiles || op.quantile() > 0
		s.distinct = s.distinct || op == Distinct
	}
	s.Ops = ops
	return nil
}

func checkLocator(field string, l *Locator) error {
	switch {
	case l == nil:
		return nil
	case l.Slot < 0:
		return fmt.Errorf("field %s Slot %d is negative", field, l.Slot)
	case l.After != "" && l.Slot != 0:
		return fmt.Errorf("field %s sets both Slot and After", field)
	}
	return nil
}

// Whether the template fits the profile's ids or pattern
func (s *spec) matches(id moldrain.ID, words []string) bool {
	if s.ids != nil {
		_, ok := s.ids[id]
		return ok
	}
	return matchWords(s.pattern, words)
}

// Glob over whole words
// 1. * takes any run of words
// 2. every other pattern word must equal its word
// A template renders each variable as <*> so <*> in a pattern meets exactly one variable
func matchWords(pat, words []string) bool {
	if len(pat) == 0 {
		return len(words) == 0
	}
	if pat[0] == "*" {
		for i := 0; i <= len(words); i++ {
			if matchWords(pat[1:], words[i:]) {
				return true
			}
		}
		return false
	}
	return len(words) > 0 && pat[0] == words[0] && matchWords(pat[1:], words[1:])
}

// The words of a shape with every variable as <*> and a span as <*...>
// KindPlaceholders does not change them
func words(sh moldrain.Shape) []string { return strings.Fields(sh.String()) }

// Index among the params of the slot a locator names
// false when the shape has no such slot
// After matches a constant whose text alone or with the gap after it equals the key
func locate(l *Locator, sh moldrain.Shape) (int, bool) {
	vars := 0
	for i, p := range sh.Parts {
		if p.Kind == moldrain.Const {
			continue
		}
		if l.After == "" && vars == l.Slot {
			return vars, true
		}
		if l.After != "" && i > 0 && isKey(sh, i-1, l.After) {
			return vars, true
		}
		vars++
	}
	return 0, false
}

func isKey(sh moldrain.Shape, i int, key string) bool {
	p := sh.Parts[i]
	if p.Kind != moldrain.Const {
		return false
	}
	gap := ""
	if i+1 < len(sh.Gaps) {
		gap = strings.TrimSpace(sh.Gaps[i+1])
	}
	return p.Text == key || p.Text+gap == key
}
