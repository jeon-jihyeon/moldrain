package tree

import "fmt"

// Defaults of Options
// Drain3's where Drain3 has one
const (
	DefaultSimTh       = 0.4
	DefaultDepth       = 2
	DefaultMaxChildren = 100
	DefaultMaxRebuilds = 3
	DefaultSplitValues = 4
	DefaultSplitMin    = 1000
	DefaultMaxSpan     = 8
)

// How the tree matches and merges
// A zero field takes its default
type Options struct {
	// Similarity a line needs to join a template
	SimTh float64
	// Branch positions
	// 2 matches Drain3's depth 4 that counts the root and the length layer
	Depth int
	// Children per branch node
	// The overflow goes to the wildcard child
	MaxChildren int
	// Rebuilds per bucket when a branch position turns unstable
	MaxRebuilds int
	// Recent values kept per variable slot of each cluster
	KeepValues int
	// Renders <num>, <ip> and so on instead of <*>
	KindPlaceholders bool

	// Splits a variable slot whose values stayed within SplitValues over SplitMin lines
	Respecialize bool
	// Distinct values a slot may take and still split
	SplitValues int
	// Lines a slot needs before it splits
	SplitMin int

	// Lets a line up to MaxSpan tokens longer or shorter than a template match it through one <*...> slot
	Spans bool
	// Tokens a line may be longer or shorter than the template it joins through a span
	MaxSpan int
}

func (o Options) withDefaults() Options {
	if o.SimTh == 0 {
		o.SimTh = DefaultSimTh
	}
	if o.Depth == 0 {
		o.Depth = DefaultDepth
	}
	if o.MaxChildren == 0 {
		o.MaxChildren = DefaultMaxChildren
	}
	if o.MaxRebuilds == 0 {
		o.MaxRebuilds = DefaultMaxRebuilds
	}
	if o.SplitValues == 0 {
		o.SplitValues = DefaultSplitValues
	}
	if o.SplitMin == 0 {
		o.SplitMin = DefaultSplitMin
	}
	if o.MaxSpan == 0 {
		o.MaxSpan = DefaultMaxSpan
	}
	return o
}

// Checks the options after their defaults
// Names the first field out of range
func (o Options) validate() error {
	ints := []struct {
		name string
		v    int
		min  int
	}{
		{"Depth", o.Depth, 1}, {"MaxChildren", o.MaxChildren, 2}, {"MaxRebuilds", o.MaxRebuilds, 1},
		{"KeepValues", o.KeepValues, 0}, {"SplitValues", o.SplitValues, 2}, {"SplitMin", o.SplitMin, 1},
		{"MaxSpan", o.MaxSpan, 1},
	}
	for _, f := range ints {
		if f.v < f.min {
			return fmt.Errorf("%w: %s %d is below %d", ErrOptions, f.name, f.v, f.min)
		}
	}
	if o.SimTh < 0 || o.SimTh > 1 {
		return fmt.Errorf("%w: SimTh %v is outside 0 to 1", ErrOptions, o.SimTh)
	}
	return nil
}
