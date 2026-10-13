package tree

import "errors"

var (
	// An option outside its range wrapped with the field name
	ErrOptions = errors.New("invalid options")
	// Not 16 hex digits
	ErrID = errors.New("invalid template id")
	// An id the tree does not hold
	ErrNotFound = errors.New("template not found")
	// A resolver answer that would merge a template into a younger one or into itself
	ErrNotOlder = errors.New("target template is not older")
	// An alias merged again
	// It already counts as the template it was merged into
	ErrAliased = errors.New("template is already an alias")
	// A replayed change that gives another id than the one recorded
	ErrDiverged = errors.New("replay diverged from the recorded change")
	// A state that does not describe a valid tree
	ErrState = errors.New("invalid tree state")
	// Restore into a tree that already learned
	ErrNotEmpty = errors.New("tree is not empty")
)
