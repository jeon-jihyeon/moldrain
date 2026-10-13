package tree_test

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/jeon-jihyeon/moldrain/internal/token"
	"github.com/jeon-jihyeon/moldrain/internal/tree"
)

func TestNew(t *testing.T) {
	tcs := []struct {
		name string
		opt  tree.Options
		want string
	}{
		{"negative depth", tree.Options{Depth: -1}, "Depth"},
		{"one child per node", tree.Options{MaxChildren: 1}, "MaxChildren"},
		{"negative rebuilds", tree.Options{MaxRebuilds: -1}, "MaxRebuilds"},
		{"negative kept values", tree.Options{KeepValues: -1}, "KeepValues"},
		{"negative split values", tree.Options{SplitValues: -1}, "SplitValues"},
		{"negative split lines", tree.Options{SplitMin: -1}, "SplitMin"},
		{"negative span", tree.Options{MaxSpan: -1}, "MaxSpan"},
		{"similarity above 1", tree.Options{SimTh: 1.5}, "SimTh"},
	}
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			_, err := tree.New(tc.opt, token.NewInterner())
			assert.ErrorIs(t, err, tree.ErrOptions)
			assert.ErrorContains(t, err, tc.want)
		})
	}
}
