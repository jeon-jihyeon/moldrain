package moldrain

import (
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Each Changed pushes an entry while the tree stays under the cap
// The heap is rebuilt before it doubles
func TestRecencyStaysBounded(t *testing.T) {
	const maxClusters = 50
	m, err := New(Options{MaxClusters: maxClusters})
	require.NoError(t, err)
	at := time.Date(2026, 1, 2, 3, 4, 0, 0, time.UTC)
	for k := 0; k < 40; k++ {
		words := strings.Fields("lead fixed s0 s1 s2 s3 s4 s5 s6 s7 s8 s9")
		words[0] = strings.Repeat(string(rune('a'+k%26)), 1+k/26)
		m.Train([]byte(strings.Join(words, " ")), at)
		// each line turns one more slot variable
		// The template keeps changing id
		for j := 2; j < len(words); j++ {
			words[j] = "other"
			m.Train([]byte(strings.Join(words, " ")), at)
		}
	}
	require.Greater(t, m.Stats().Changed, uint64(2*maxClusters))
	assert.LessOrEqual(t, len(m.recency), 2*maxClusters)
}
