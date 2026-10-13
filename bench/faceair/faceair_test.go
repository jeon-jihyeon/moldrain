package faceair_test

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/jeon-jihyeon/moldrain/bench/faceair"
)

func TestParser(t *testing.T) {
	p := faceair.New()
	var groups []uint64
	for _, l := range []string{"open file a", "open file b", "disk full"} {
		groups = append(groups, p.Train([]byte(l)))
	}
	assert.Equal(t, []uint64{1, 1, 2}, groups)
	assert.Equal(t, map[uint64]string{1: "open file <*>", 2: "disk full"}, p.Templates())
}
