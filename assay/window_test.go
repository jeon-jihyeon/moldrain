package assay

import (
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
)

// Records handed over after Close go as those of an open window
func TestOutletDropsAfterClose(t *testing.T) {
	o := newOutlet()
	o.close()
	p := &profile{queue: make([]Record, 1)}
	assert.Zero(t, o.take(p))
	assert.Nil(t, p.queue)
}

// A profile that hands records to a full outlet waits until the sink takes some
func TestOutletFullWaits(t *testing.T) {
	o := newOutlet()
	entered, release := make(chan struct{}), make(chan struct{})
	var once sync.Once
	o.push(func(Record) {
		once.Do(func() { close(entered) })
		<-release
	})
	// the first record keeps the sink busy so the next ones stay queued
	o.take(&profile{queue: make([]Record, 1)})
	<-entered
	o.take(&profile{queue: make([]Record, outletRecords)})
	done := make(chan uint64)
	go func() { done <- o.take(&profile{queue: make([]Record, 1)}) }()
	select {
	case <-done:
		t.Fatal("a full outlet took more records")
	case <-time.After(100 * time.Millisecond):
	}
	close(release)
	assert.Equal(t, uint64(outletRecords+2), <-done)
	o.close()
}
