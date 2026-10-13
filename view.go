package moldrain

import "time"

// When the published view catches up with training
// Train changes buckets and marks them dirty
// A publish clones the dirty ones and swaps the pointer Match loads
type publisher struct {
	// lines trained since the last publish
	lines int
	// when the first change after the last publish happened
	// zero when the view is current
	since time.Time
	timer *time.Timer
}

func (p *publisher) lag() time.Duration {
	if p.since.IsZero() {
		return 0
	}
	return time.Since(p.since)
}

// Called under mu after anything changed the tree
// Publishes now on MatchAfterTrain or every PublishLines lines
// Otherwise arms a timer for PublishEvery
func (m *Miner) changedLocked() {
	m.pub.lines++
	if m.opt.MatchAfterTrain || m.pub.lines >= m.opt.PublishLines {
		m.publishLocked()
		return
	}
	if !m.pub.since.IsZero() {
		return
	}
	m.pub.since = time.Now()
	if m.pub.timer == nil {
		m.pub.timer = time.AfterFunc(m.opt.PublishEvery, m.publishTimer)
	} else {
		m.pub.timer.Reset(m.opt.PublishEvery)
	}
}

func (m *Miner) publishTimer() {
	m.mu.Lock()
	defer m.mu.Unlock()
	if !m.pub.since.IsZero() {
		m.publishLocked()
	}
}

func (m *Miner) publishLocked() {
	if m.tree.Dirty() {
		m.view.Store(m.tree.Publish(m.view.Load()))
	}
	m.pub.lines = 0
	m.pub.since = time.Time{}
	if m.pub.timer != nil {
		m.pub.timer.Stop()
	}
}

// Publishes every change now so Match sees all lines trained before the call
// It also stops the publish timer until the next change
func (m *Miner) Publish() {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.publishLocked()
}
