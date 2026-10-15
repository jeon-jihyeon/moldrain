package persist_test

//go:generate mockgen -destination store_mock_test.go -package persist_test -typed=false github.com/jeon-jihyeon/moldrain/persist Store

import (
	"context"
	"errors"
	"fmt"
	"iter"
	"math/rand/v2"
	"os"
	"path/filepath"
	"slices"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"

	"github.com/jeon-jihyeon/moldrain"
	"github.com/jeon-jihyeon/moldrain/persist"
	"github.com/jeon-jihyeon/moldrain/persist/storetest"
)

var (
	t0        = time.Date(2026, 1, 2, 3, 4, 0, 0, time.UTC)
	errBroken = errors.New("store broken")
)

func lines(seed uint64, n int) []string {
	r := rand.New(rand.NewPCG(seed, 11))
	formats := []string{
		"user %s logged in from 2001:db8::%x:%x",
		"request %x took %d ms",
		"disk full on %s at %d percent",
		"job finished %s with %d items",
		"cache miss key %s shard %d",
		"%s %s %s",
	}
	words := []string{"alice", "bob", "sda", "pay", "ship", "zeta", "x9", "k"}
	out := make([]string, n)
	for i := range out {
		w := func() string { return words[r.IntN(len(words))] }
		switch k := r.IntN(len(formats)); k {
		case 0:
			out[i] = fmt.Sprintf(formats[k], w(), r.IntN(256), r.IntN(256))
		case 1:
			out[i] = fmt.Sprintf(formats[k], r.Uint32(), r.IntN(900))
		case 5:
			out[i] = fmt.Sprintf(formats[k], w(), w(), w())
		default:
			out[i] = fmt.Sprintf(formats[k], w(), r.IntN(100))
		}
	}
	return out
}

func newMiner(t *testing.T, opt moldrain.Options) *moldrain.Miner {
	t.Helper()
	m, err := moldrain.New(opt)
	require.NoError(t, err)
	return m
}

func fileStore(t *testing.T, dir string) *persist.FileStore {
	t.Helper()
	s, err := persist.NewFileStore(dir)
	require.NoError(t, err)
	return s
}

func attach(t *testing.T, m *moldrain.Miner, s persist.Store, opt persist.Options) *persist.Persister {
	t.Helper()
	if opt.FlushEvery == 0 {
		opt.FlushEvery = time.Hour
	}
	p, err := persist.Attach(context.Background(), m, s, opt)
	require.NoError(t, err)
	return p
}

func train(m *moldrain.Miner, ls []string, from int) {
	for i, l := range ls {
		m.Train([]byte(l), t0.Add(time.Duration(from+i)*time.Second))
	}
}

type answer struct {
	ID       moldrain.ID
	Template string
	OK       bool
}

func answers(m *moldrain.Miner, ls []string) []answer {
	m.Publish()
	out := make([]answer, len(ls))
	for i, l := range ls {
		r, ok := m.Match([]byte(l))
		out[i] = answer{r.ID, r.Template, ok}
	}
	return out
}

func clusters(m *moldrain.Miner) map[moldrain.ID]uint64 {
	m.Publish()
	out := map[moldrain.ID]uint64{}
	for _, c := range m.Clusters() {
		out[c.ID] = c.Count
	}
	return out
}

func TestFileStoreContract(t *testing.T) {
	storetest.Run(t, func(t *testing.T) persist.Store {
		return fileStore(t, filepath.Join(t.TempDir(), "state"))
	})
}

func TestFileStoreCutsTornTail(t *testing.T) {
	tcs := []struct {
		name string
		torn []byte
		want int
	}{
		{"a short frame head", []byte{0, 0, 0}, 2},
		{"a frame shorter than its length", []byte{0, 0, 0, 40, 1, 2}, 2},
	}
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			dir := t.TempDir()
			s := fileStore(t, dir)
			ctx := context.Background()
			require.NoError(t, s.Append(ctx, [][]byte{persist.EncodeChange(1, moldrain.Event{Kind: moldrain.Evicted, ID: 1, At: t0})}))
			f, err := os.OpenFile(filepath.Join(dir, "changes"), os.O_APPEND|os.O_WRONLY, 0)
			require.NoError(t, err)
			_, err = f.Write(tc.torn)
			require.NoError(t, err)
			require.NoError(t, f.Close())

			before, err := os.Stat(filepath.Join(dir, "changes"))
			require.NoError(t, err)
			_, _, err = s.Load(ctx)
			require.NoError(t, err)
			// a reader leaves the file to its writer
			after, err := os.Stat(filepath.Join(dir, "changes"))
			require.NoError(t, err)
			require.Equal(t, before.Size(), after.Size())
			require.NoError(t, fileStore(t, dir).Append(ctx, [][]byte{persist.EncodeChange(2, moldrain.Event{Kind: moldrain.Evicted, ID: 2, At: t0})}))
			_, changes, err := s.Load(ctx)
			require.NoError(t, err)
			evs, err := persist.ReadChanges(changes)
			require.NoError(t, err)
			assert.Len(t, evs, tc.want)
		})
	}
}

// A restarted miner answers Match identically and holds the same counts
func TestRestart(t *testing.T) {
	type args struct {
		opt moldrain.Options
		// the first persister stops without Close as a crash leaves the store
		crash bool
		// the second persister stops by Close
		// Otherwise by a crash after its flushes
		closeSecond bool
	}
	tcs := []struct {
		name string
		args args
	}{
		{"close then close", args{moldrain.Options{}, false, true}},
		{"close then crash", args{moldrain.Options{}, false, false}},
		{"crash then crash", args{moldrain.Options{}, true, false}},
		{"crash then close", args{moldrain.Options{}, true, true}},
		{"small trees with rebuilds", args{moldrain.Options{MaxChildren: 4}, true, false}},
		{"spans and respecialize", args{moldrain.Options{Spans: true, Respecialize: true, SplitMin: 20}, true, false}},
		{"cluster cap", args{moldrain.Options{MaxClusters: 10}, true, false}},
	}
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			ctx := context.Background()
			dir := t.TempDir()
			ls := lines(3, 3000)
			m := newMiner(t, tc.args.opt)
			p := attach(t, m, fileStore(t, dir), persist.Options{})
			train(m, ls[:500], 0)
			require.NoError(t, p.Flush(ctx))
			train(m, ls[500:1000], 500)
			require.NoError(t, p.Flush(ctx))
			if !tc.args.crash {
				require.NoError(t, p.Close(ctx))
			}

			m2 := newMiner(t, tc.args.opt)
			p2 := attach(t, m2, fileStore(t, dir), persist.Options{})
			require.Equal(t, answers(m, ls), answers(m2, ls))
			for _, part := range [][2]int{{1000, 1500}, {1500, 2000}} {
				train(m, ls[part[0]:part[1]], part[0])
				train(m2, ls[part[0]:part[1]], part[0])
				require.NoError(t, p2.Flush(ctx))
			}
			if tc.args.closeSecond {
				require.NoError(t, p2.Close(ctx))
			}

			m3 := newMiner(t, tc.args.opt)
			attach(t, m3, fileStore(t, dir), persist.Options{})
			assert.Equal(t, answers(m, ls), answers(m3, ls))
			assert.Equal(t, clusters(m), clusters(m3))
		})
	}
}

// A crash after the snapshot's rename and before the log was emptied leaves records the snapshot holds
// Load skips them by seq
func TestRestartAfterRenameBeforeTruncate(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	ls := lines(13, 1000)
	m := newMiner(t, moldrain.Options{})
	p := attach(t, m, fileStore(t, dir), persist.Options{})
	train(m, ls[:500], 0)
	require.NoError(t, p.Flush(ctx))
	log, err := os.ReadFile(filepath.Join(dir, "changes"))
	require.NoError(t, err)
	require.NotEmpty(t, log)
	require.NoError(t, p.Close(ctx))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "changes"), log, 0o600))

	m2 := newMiner(t, moldrain.Options{})
	attach(t, m2, fileStore(t, dir), persist.Options{})
	assert.Equal(t, answers(m, ls), answers(m2, ls))
	assert.Equal(t, clusters(m), clusters(m2))
}

// A store that keeps a torn record as a whole one loses only its last record
func TestLoadTornRecord(t *testing.T) {
	tcs := []struct {
		name string
		// where the torn record goes among the whole ones
		at   int
		want error
	}{
		{"a torn last record is dropped", 2, nil},
		{"a torn record before the last is corrupt", 1, persist.ErrCorrupt},
	}
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			ctx := context.Background()
			fs := fileStore(t, t.TempDir())
			require.NoError(t, attach(t, newMiner(t, moldrain.Options{}), fs, persist.Options{}).Close(ctx))
			snap, _, err := fs.Load(ctx)
			require.NoError(t, err)
			whole := []moldrain.Event{
				{Kind: moldrain.Evicted, ID: 1, At: t0},
				{Kind: moldrain.Evicted, ID: 2, At: t0},
			}
			recs := [][]byte{persist.EncodeChange(1, whole[0]), persist.EncodeChange(2, whole[1])}
			torn := persist.EncodeChange(3, moldrain.Event{Kind: moldrain.Evicted, ID: 3, At: t0})
			recs = slices.Insert(recs, tc.at, torn[:len(torn)-2])
			s := NewMockStore(gomock.NewController(t))
			changes := func(yield func([]byte, error) bool) {
				for _, r := range recs {
					if !yield(r, nil) {
						return
					}
				}
			}
			s.EXPECT().Load(gomock.Any()).Return(snap, iter.Seq2[[]byte, error](changes), nil).AnyTimes()
			_, err = persist.Load(ctx, newMiner(t, moldrain.Options{}), s)
			assert.ErrorIs(t, err, tc.want)
		})
	}
}

// Counts a template took before it became an alias land on its target within one flush
func TestAliasMergesCounts(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	m := newMiner(t, moldrain.Options{})
	p := attach(t, m, fileStore(t, dir), persist.Options{})
	old := m.Train([]byte("disk full on sda now"), t0)
	require.NoError(t, p.Flush(ctx))
	train(m, []string{"disk full on sda now", "no space left on device sdc", "no space left on device sdc"}, 1)
	prov := m.Train([]byte("no space left on device sdc"), t0.Add(5*time.Second))
	require.NoError(t, m.Alias(prov.ID, old.ID))
	train(m, []string{"no space left on device sdc"}, 6)
	require.NoError(t, p.Flush(ctx))

	m2 := newMiner(t, moldrain.Options{})
	attach(t, m2, fileStore(t, dir), persist.Options{})
	assert.Equal(t, map[moldrain.ID]uint64{old.ID: 6}, clusters(m2))
	assert.Equal(t, clusters(m), clusters(m2))
}

// A lower cap evicts while the store loads and the next start still loads after a crash
func TestRestartUnderLowerCap(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	ls := []string{"alpha one", "beta two three", "gamma four five six", "delta seven eight nine ten"}
	m := newMiner(t, moldrain.Options{})
	p := attach(t, m, fileStore(t, dir), persist.Options{})
	train(m, ls, 0)
	require.NoError(t, p.Close(ctx))

	opt := moldrain.Options{MaxClusters: 1}
	m2 := newMiner(t, opt)
	p2 := attach(t, m2, fileStore(t, dir), persist.Options{})
	require.Equal(t, 1, m2.Stats().Clusters)
	// the templates the load evicted come back as new ones
	train(m2, ls, 10)
	require.NoError(t, p2.Flush(ctx))

	m3 := newMiner(t, opt)
	_, err := persist.Attach(ctx, m3, fileStore(t, dir), persist.Options{FlushEvery: time.Hour})
	require.NoError(t, err)
	assert.Equal(t, clusters(m2), clusters(m3))
}

// A write the store failed after it wrote all or part of it is never left in the log for a retry to follow
func TestFailedWrite(t *testing.T) {
	tcs := []struct {
		name string
		opt  persist.Options
		init func(s *MockStore, fs *persist.FileStore)
	}{
		{
			"append wrote every record then failed",
			persist.Options{},
			func(s *MockStore, fs *persist.FileStore) {
				s.EXPECT().Append(gomock.Any(), gomock.Any()).DoAndReturn(func(ctx context.Context, recs [][]byte) error {
					_ = fs.Append(ctx, recs)
					return errBroken
				})
				s.EXPECT().Append(gomock.Any(), gomock.Any()).DoAndReturn(fs.Append).AnyTimes()
				s.EXPECT().Replace(gomock.Any(), gomock.Any()).DoAndReturn(fs.Replace).AnyTimes()
			},
		},
		{
			"append wrote half a frame then failed",
			persist.Options{},
			func(s *MockStore, fs *persist.FileStore) {
				s.EXPECT().Append(gomock.Any(), gomock.Any()).DoAndReturn(func(ctx context.Context, recs [][]byte) error {
					var all []byte
					for _, r := range recs {
						all = append(all, r...)
					}
					_ = fs.Append(ctx, [][]byte{all[:len(all)/2]})
					return errBroken
				})
				s.EXPECT().Append(gomock.Any(), gomock.Any()).DoAndReturn(fs.Append).AnyTimes()
				s.EXPECT().Replace(gomock.Any(), gomock.Any()).DoAndReturn(fs.Replace).AnyTimes()
			},
		},
		{
			"snapshot failed after the records landed",
			persist.Options{CompactBytes: 1},
			func(s *MockStore, fs *persist.FileStore) {
				s.EXPECT().Append(gomock.Any(), gomock.Any()).DoAndReturn(fs.Append).AnyTimes()
				s.EXPECT().Replace(gomock.Any(), gomock.Any()).Return(errBroken)
				s.EXPECT().Replace(gomock.Any(), gomock.Any()).DoAndReturn(fs.Replace).AnyTimes()
			},
		},
	}
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			ctx := context.Background()
			dir := t.TempDir()
			fs := fileStore(t, dir)
			m := newMiner(t, moldrain.Options{})
			// the first snapshot of an empty store lands before the case breaks the store
			require.NoError(t, attach(t, m, fs, persist.Options{}).Close(ctx))
			s := NewMockStore(gomock.NewController(t))
			s.EXPECT().Load(gomock.Any()).DoAndReturn(fs.Load).AnyTimes()
			tc.init(s, fs)
			p := attach(t, m, s, tc.opt)
			ls := lines(5, 2000)
			train(m, ls[:1000], 0)
			assert.ErrorIs(t, p.Flush(ctx), errBroken)
			train(m, ls[1000:], 1000)
			require.NoError(t, p.Flush(ctx))

			m2 := newMiner(t, moldrain.Options{})
			attach(t, m2, fileStore(t, dir), persist.Options{})
			assert.Equal(t, answers(m, ls), answers(m2, ls))
			assert.Equal(t, clusters(m), clusters(m2))
		})
	}
}

func TestAttachRefuses(t *testing.T) {
	tcs := []struct {
		name string
		// writes the store the miner then attaches to
		init func(t *testing.T, dir string)
		want error
	}{
		{
			"state learned with other options",
			func(t *testing.T, dir string) {
				m := newMiner(t, moldrain.Options{SimTh: 0.5})
				p := attach(t, m, fileStore(t, dir), persist.Options{})
				m.Train([]byte("a b c"), t0)
				require.NoError(t, p.Close(context.Background()))
			},
			persist.ErrOptionsChanged,
		},
		{
			"a snapshot that is not one",
			func(t *testing.T, dir string) {
				require.NoError(t, os.WriteFile(filepath.Join(dir, "snapshot"), []byte("MOLD\x01 not really"), 0o600))
			},
			persist.ErrCorrupt,
		},
		{
			"a change log with a missing record",
			func(t *testing.T, dir string) {
				m := newMiner(t, moldrain.Options{})
				require.NoError(t, attach(t, m, fileStore(t, dir), persist.Options{}).Close(context.Background()))
				first := persist.EncodeChange(1, moldrain.Event{Kind: moldrain.Evicted, ID: 1, At: t0})
				third := persist.EncodeChange(3, moldrain.Event{Kind: moldrain.Evicted, ID: 3, At: t0})
				require.NoError(t, fileStore(t, dir).Append(context.Background(), [][]byte{first, third, first}))
			},
			persist.ErrCorrupt,
		},
	}
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			dir := t.TempDir()
			tc.init(t, dir)
			m := newMiner(t, moldrain.Options{})
			_, err := persist.Attach(context.Background(), m, fileStore(t, dir), persist.Options{})
			assert.ErrorIs(t, err, tc.want)
			assert.Equal(t, 0, m.Stats().Clusters)
		})
	}
}

func TestAttachFirstSnapshotFails(t *testing.T) {
	s := NewMockStore(gomock.NewController(t))
	s.EXPECT().Load(gomock.Any()).Return(nil, nil, nil)
	s.EXPECT().Replace(gomock.Any(), gomock.Any()).Return(errBroken)
	m := newMiner(t, moldrain.Options{})
	_, err := persist.Attach(context.Background(), m, s, persist.Options{})
	assert.ErrorIs(t, err, errBroken)
	// the persister of a failed Attach takes no more events
	// Training goes on without it
	m.Train([]byte("a b c"), t0)
	assert.Equal(t, 1, m.Stats().Clusters)
}

func TestLoad(t *testing.T) {
	tcs := []struct {
		name  string
		lines int
		want  bool
	}{
		{"an empty store holds nothing", 0, false},
		{"a closed persister left a snapshot", 300, true},
	}
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			dir := t.TempDir()
			ls := lines(7, tc.lines)
			m := newMiner(t, moldrain.Options{})
			if tc.want {
				p := attach(t, m, fileStore(t, dir), persist.Options{})
				train(m, ls, 0)
				require.NoError(t, p.Close(context.Background()))
			}
			m2 := newMiner(t, moldrain.Options{})
			ok, err := persist.Load(context.Background(), m2, fileStore(t, dir))
			require.NoError(t, err)
			assert.Equal(t, tc.want, ok)
			assert.Equal(t, answers(m, ls), answers(m2, ls))
		})
	}
}

// The background goroutine flushes on a full backlog and keeps the error of its latest flush
func TestErr(t *testing.T) {
	s := NewMockStore(gomock.NewController(t))
	fs := fileStore(t, t.TempDir())
	s.EXPECT().Load(gomock.Any()).DoAndReturn(fs.Load).AnyTimes()
	s.EXPECT().Replace(gomock.Any(), gomock.Any()).DoAndReturn(fs.Replace).AnyTimes()
	failed := make(chan struct{})
	s.EXPECT().Append(gomock.Any(), gomock.Any()).DoAndReturn(func(context.Context, [][]byte) error {
		close(failed)
		return errBroken
	})
	m := newMiner(t, moldrain.Options{})
	p := attach(t, m, s, persist.Options{MaxBacklog: 1})
	m.Train([]byte("a b c"), t0)
	<-failed
	require.Eventually(t, func() bool { return errors.Is(p.Err(), errBroken) }, 5*time.Second, time.Millisecond)
	require.NoError(t, p.Flush(context.Background()))
	assert.NoError(t, p.Err())
	require.NoError(t, p.Close(context.Background()))
	assert.ErrorIs(t, p.Flush(context.Background()), persist.ErrClosed)
	assert.ErrorIs(t, p.Close(context.Background()), persist.ErrClosed)
}

// Close cancels a flush of the goroutine that waits on the store instead of waiting up to FlushEvery
// Its snapshot holds what that flush did not write
func TestCloseCancelsFlush(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	fs := fileStore(t, dir)
	s := NewMockStore(gomock.NewController(t))
	s.EXPECT().Load(gomock.Any()).DoAndReturn(fs.Load).AnyTimes()
	s.EXPECT().Replace(gomock.Any(), gomock.Any()).DoAndReturn(fs.Replace).AnyTimes()
	entered := make(chan struct{})
	s.EXPECT().Append(gomock.Any(), gomock.Any()).DoAndReturn(func(ctx context.Context, _ [][]byte) error {
		close(entered)
		<-ctx.Done()
		return ctx.Err()
	})
	m := newMiner(t, moldrain.Options{})
	p := attach(t, m, s, persist.Options{MaxBacklog: 1})
	m.Train([]byte("a b c"), t0)
	<-entered
	closed := make(chan error)
	go func() { closed <- p.Close(ctx) }()
	select {
	case err := <-closed:
		require.NoError(t, err)
	case <-time.After(5 * time.Second):
		t.Fatal("Close waited for the flush in flight")
	}
	m2 := newMiner(t, moldrain.Options{})
	attach(t, m2, fileStore(t, dir), persist.Options{})
	assert.Equal(t, clusters(m), clusters(m2))
}

// For a steady stream the file store writes less per hour than a full snapshot every 5 minutes
func TestWriteVolume(t *testing.T) {
	fs := fileStore(t, t.TempDir())
	var appended, replaced, snapshots, lastSnapshot int
	store := NewMockStore(gomock.NewController(t))
	store.EXPECT().Load(gomock.Any()).DoAndReturn(fs.Load).AnyTimes()
	store.EXPECT().Append(gomock.Any(), gomock.Any()).DoAndReturn(func(ctx context.Context, recs [][]byte) error {
		for _, r := range recs {
			appended += len(r)
		}
		return fs.Append(ctx, recs)
	}).AnyTimes()
	store.EXPECT().Replace(gomock.Any(), gomock.Any()).DoAndReturn(func(ctx context.Context, snap []byte) error {
		replaced += len(snap)
		snapshots++
		lastSnapshot = len(snap)
		return fs.Replace(ctx, snap)
	}).AnyTimes()
	now := t0
	opt := persist.Options{FlushEvery: time.Hour, Clock: func() time.Time { return now }}
	ctx := context.Background()

	// 300 events learned first give the hour measured a steady template set
	// Each event has its own words
	// No two events are similar enough to merge
	words := rand.New(rand.NewPCG(4, 4))
	word := func() string {
		b := make([]byte, 4+words.IntN(6))
		for i := range b {
			b[i] = byte('a' + words.IntN(26))
		}
		return string(b)
	}
	events := make([]string, 300)
	for i := range events {
		events[i] = fmt.Sprintf("%s %s %s %s task %%d on %s%%d", word(), word(), word(), word(), word())
	}
	m := newMiner(t, moldrain.Options{})
	p := attach(t, m, store, opt)
	for i := range events {
		m.Train([]byte(fmt.Sprintf(events[i], 0, 0)), now)
		m.Train([]byte(fmt.Sprintf(events[i], 1, 1)), now)
	}
	require.NoError(t, p.Close(ctx))
	snapshot := lastSnapshot

	m = newMiner(t, moldrain.Options{})
	p = attach(t, m, store, opt)
	appended, replaced, snapshots = 0, 0, 0
	// Zipf frequencies at 50 lines per second for an hour
	// A flush every 10 s
	r := rand.New(rand.NewPCG(9, 9))
	zipf := rand.NewZipf(rand.New(rand.NewPCG(1, 2)), 1.2, 1, uint64(len(events)-1))
	for sec := 0; sec < 3600; sec++ {
		now = t0.Add(time.Duration(sec) * time.Second)
		for j := 0; j < 50; j++ {
			ev := events[zipf.Uint64()]
			m.Train([]byte(fmt.Sprintf(ev, r.IntN(1e6), r.IntN(64))), now)
		}
		if sec%10 == 9 {
			require.NoError(t, p.Flush(ctx))
		}
	}
	now = t0.Add(time.Hour)
	require.NoError(t, p.Flush(ctx))
	written := appended + replaced
	full := 12 * snapshot
	t.Logf("snapshot %d bytes, log %d bytes and %d snapshots in the hour, %d bytes against %d for 12 full snapshots",
		snapshot, appended, snapshots, written, full)
	assert.Less(t, written, full)
	assert.Equal(t, 1, snapshots)
}
