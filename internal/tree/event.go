package tree

import "time"

type EventKind uint8

const (
	Created EventKind = iota + 1 // a new template
	Matched                      // a line joined a template without changing it
	Changed                      // a template changed and its id moved from From to To
	Evicted                      // a template left under the cluster cap
)

func (k EventKind) String() string {
	switch k {
	case Created:
		return "created"
	case Matched:
		return "matched"
	case Changed:
		return "changed"
	case Evicted:
		return "evicted"
	}
	return "none"
}

// One change to the template set in the order it happened
// A caller that keys anything by id moves it on Changed
type Event struct {
	Kind EventKind
	// The template of Created and Matched and Evicted
	ID ID
	// The id before and after a Changed
	From, To ID
	// A Changed from a resolver answer
	// From stays in the tree as another form of To instead of turning into it
	Alias bool
	// The template of a Created and of To in a Changed
	// nil for Matched and Evicted and an Alias Changed
	Shape *Shape
	// Lines this event added to ID or To
	N  uint64
	At time.Time
	// The line that caused the event
	// Valid only during the callback
	Line []byte
}
