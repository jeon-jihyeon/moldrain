package persist

import "errors"

var (
	// State learned under options that change templates
	// Refused so two template spaces never mix
	ErrOptionsChanged = errors.New("stored state was learned with other options")
	// Bytes that are not a valid snapshot or record
	ErrCorrupt = errors.New("corrupt moldrain state")
	// A snapshot of a format this version does not read
	ErrFormat = errors.New("unknown snapshot format")
	// A call after Close
	ErrClosed = errors.New("persister is closed")
)
