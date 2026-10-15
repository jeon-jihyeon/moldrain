package kafka_test

import (
	"context"
	"fmt"
	"math/rand/v2"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/twmb/franz-go/pkg/kadm"
	"github.com/twmb/franz-go/pkg/kfake"
	"github.com/twmb/franz-go/pkg/kgo"

	"github.com/jeon-jihyeon/moldrain"
	"github.com/jeon-jihyeon/moldrain/persist"
	"github.com/jeon-jihyeon/moldrain/persist/storetest"
	"github.com/jeon-jihyeon/moldrain/store/kafka"
)

// A client of an in process broker that lives as long as the test
func fakeClient(t *testing.T) *kgo.Client {
	t.Helper()
	c, err := kfake.NewCluster(kfake.NumBrokers(1))
	require.NoError(t, err)
	t.Cleanup(c.Close)
	cl, err := kgo.NewClient(kgo.SeedBrokers(c.ListenAddrs()...))
	require.NoError(t, err)
	t.Cleanup(cl.Close)
	return cl
}

func TestFakeContract(t *testing.T) {
	cl := fakeClient(t)
	n := 0
	storetest.Run(t, func(t *testing.T) persist.Store {
		n++
		s, err := kafka.New(t.Context(), cl, fmt.Sprintf("moldrain-%d", n))
		require.NoError(t, err)
		return s
	})
}

// A miner that restarts on the topic answers as before
// Once after a crash and once after a close
func TestFakeRestart(t *testing.T) {
	ctx := context.Background()
	cl := fakeClient(t)
	open := func() *kafka.Store {
		s, err := kafka.New(ctx, cl, "state")
		require.NoError(t, err)
		return s
	}
	attach := func(m *moldrain.Miner) *persist.Persister {
		p, err := persist.Attach(ctx, m, open(), persist.Options{FlushEvery: time.Hour})
		require.NoError(t, err)
		return p
	}
	ls := lines(3000)
	t0 := time.Date(2026, 1, 2, 3, 4, 0, 0, time.UTC)
	m := newMiner(t)
	p := attach(m)
	for i, l := range ls[:1000] {
		m.Train([]byte(l), t0.Add(time.Duration(i)*time.Second))
	}
	require.NoError(t, p.Flush(ctx))

	// the first persister crashed
	m2 := newMiner(t)
	p2 := attach(m2)
	assert.Equal(t, answers(m, ls), answers(m2, ls))
	for i, l := range ls[1000:] {
		m.Train([]byte(l), t0.Add(time.Duration(1000+i)*time.Second))
		m2.Train([]byte(l), t0.Add(time.Duration(1000+i)*time.Second))
	}
	require.NoError(t, p2.Flush(ctx))
	require.NoError(t, p2.Close(ctx))

	m3 := newMiner(t)
	attach(m3)
	assert.Equal(t, answers(m, ls), answers(m3, ls))
	assert.Equal(t, counts(m), counts(m3))
}

// A topic whose records were all deleted loads empty at once
func TestFakeLoadDeletedLog(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	cl := fakeClient(t)
	s, err := kafka.New(ctx, cl, "state")
	require.NoError(t, err)
	require.NoError(t, s.Append(ctx, [][]byte{[]byte("a"), []byte("b")}))
	adm := kadm.NewClient(cl)
	ends, err := adm.ListEndOffsets(ctx, "state")
	require.NoError(t, err)
	_, err = adm.DeleteRecords(ctx, ends.Offsets())
	require.NoError(t, err)
	snap, changes, err := s.Load(ctx)
	require.NoError(t, err)
	assert.Nil(t, snap)
	for range changes {
		t.Fatal("a deleted log yields a record")
	}
}

// An existing topic must compact only and fit a snapshot chunk
func TestFakeNewRefuses(t *testing.T) {
	tcs := []struct {
		name       string
		partitions int32
		configs    map[string]string
		ok         bool
	}{
		{"compacted single partition", 1, map[string]string{"cleanup.policy": "compact"}, true},
		{"two partitions", 2, map[string]string{"cleanup.policy": "compact"}, false},
		{"deleting", 1, map[string]string{"cleanup.policy": "delete"}, false},
		{"compacting and deleting", 1, map[string]string{"cleanup.policy": "compact,delete"}, false},
		{"records smaller than a chunk", 1, map[string]string{"cleanup.policy": "compact", "max.message.bytes": "100000"}, false},
	}
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			ctx := context.Background()
			cl := fakeClient(t)
			configs := map[string]*string{}
			for k, v := range tc.configs {
				configs[k] = &v
			}
			_, err := kadm.NewClient(cl).CreateTopic(ctx, tc.partitions, -1, configs, "state")
			require.NoError(t, err)
			_, err = kafka.New(ctx, cl, "state")
			assert.Equal(t, tc.ok, err == nil, "%v", err)
		})
	}
}

func newMiner(t *testing.T) *moldrain.Miner {
	t.Helper()
	m, err := moldrain.New(moldrain.Options{})
	require.NoError(t, err)
	return m
}

func lines(n int) []string {
	r := rand.New(rand.NewPCG(3, 11))
	words := []string{"alice", "bob", "sda", "pay", "ship", "zeta"}
	out := make([]string, n)
	for i := range out {
		w := words[r.IntN(len(words))]
		switch r.IntN(3) {
		case 0:
			out[i] = fmt.Sprintf("user %s logged in from 2001:db8::%x:%x", w, r.IntN(256), r.IntN(256))
		case 1:
			out[i] = fmt.Sprintf("disk full on %s at %d percent", w, r.IntN(100))
		default:
			out[i] = fmt.Sprintf("job %s %s finished", w, words[r.IntN(len(words))])
		}
	}
	return out
}

func answers(m *moldrain.Miner, ls []string) []moldrain.ID {
	m.Publish()
	out := make([]moldrain.ID, len(ls))
	for i, l := range ls {
		r, _ := m.Match([]byte(l))
		out[i] = r.ID
	}
	return out
}

func counts(m *moldrain.Miner) map[moldrain.ID]uint64 {
	m.Publish()
	out := map[moldrain.ID]uint64{}
	for _, c := range m.Clusters() {
		out[c.ID] = c.Count
	}
	return out
}
