package moldrain

import (
	"errors"

	"github.com/jeon-jihyeon/moldrain/internal/tree"
)

var (
	// An option outside its range wrapped with the field name
	ErrOptions = tree.ErrOptions
	// Restore into a miner that already trained
	ErrNotEmpty = errors.New("miner has already trained")
	// A template id that is not 16 hex digits
	ErrID = tree.ErrID
	// An id the miner does not hold
	ErrNotFound = tree.ErrNotFound
	// A merge into a template that is not older than the one merged
	ErrNotOlder = tree.ErrNotOlder
	// A merge of a template that is already an alias
	ErrAliased = tree.ErrAliased
	// A replayed change that gives another template than the one recorded
	ErrDiverged = tree.ErrDiverged
	// A state that does not describe a valid tree
	ErrState = tree.ErrState
)
