// Package faceair runs the Go port of Drain3 by faceair over the bench lines
package faceair

import (
	"strings"

	"github.com/faceair/drain"
)

// A drain at its defaults with the group each of its clusters stands for
// A cluster keeps its identity while its template changes
// The pointer is the group
type Parser struct {
	d   *drain.Drain
	ids map[*drain.LogCluster]uint64
}

func New() *Parser {
	return &Parser{d: drain.New(drain.DefaultConfig()), ids: map[*drain.LogCluster]uint64{}}
}

// Trains one line and returns its group
func (p *Parser) Train(line []byte) uint64 {
	c := p.d.Train(string(line))
	id, ok := p.ids[c]
	if !ok {
		id = uint64(len(p.ids) + 1)
		p.ids[c] = id
	}
	return id
}

// The final template of each group
func (p *Parser) Templates() map[uint64]string {
	out := make(map[uint64]string, len(p.ids))
	for c, id := range p.ids {
		out[id] = template(c)
	}
	return out
}

// The template from the cluster's String
// Its fields are unexported
func template(c *drain.LogCluster) string {
	parts := strings.SplitN(c.String(), " : ", 3)
	if len(parts) < 3 {
		return ""
	}
	return parts[2]
}
