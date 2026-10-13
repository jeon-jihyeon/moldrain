// Package kafka is a persist store in one compacted Kafka topic
package kafka

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"iter"
	"strconv"
	"strings"
	"sync"

	"github.com/twmb/franz-go/pkg/kadm"
	"github.com/twmb/franz-go/pkg/kerr"
	"github.com/twmb/franz-go/pkg/kgo"

	"github.com/jeon-jihyeon/moldrain/persist"
)

// Bytes of snapshot per record
// Below the default max.message.bytes of 1 MB with room for the batch
const chunkBytes = 512 << 10

// Prefix of the key of a snapshot chunk
// Its generation and its index follow
const snapshotKey = "snapshot."

// The state in one single partition topic
type Store struct {
	c     *kgo.Client
	topic string

	mu sync.Mutex
	// keys whose latest record is a value
	// The next Replace tombstones them
	// The change records and the chunks of the latest snapshot
	// Also the chunks of one that never completed
	keys []string
	// a counter that makes each change key and each snapshot generation unique
	next uint64
}

var _ persist.Store = (*Store)(nil)

var (
	errNoPartition = errors.New("partition 0 not found")
	errTopic       = errors.New("topic is not a compacted single partition topic that fits a snapshot chunk")
)

// A store on topic
// Created as a compacted single partition topic when missing
// An existing topic must be one already
// Load reads with a copy of the client's options
// The client must not set a consumer group or consumed topics
// It must keep the default idempotent producer with acks from all in sync replicas
// The records of one call then land once and in order
// The caller owns the client and closes it
func New(ctx context.Context, c *kgo.Client, topic string) (*Store, error) {
	adm := kadm.NewClient(c)
	details, err := adm.ListTopics(ctx, topic)
	if err != nil {
		return nil, fmt.Errorf("describe topic: %w", err)
	}
	td, ok := details[topic]
	if ok && td.Err == nil {
		return newChecked(ctx, adm, c, td)
	}
	if ok && !errors.Is(td.Err, kerr.UnknownTopicOrPartition) {
		return nil, fmt.Errorf("describe topic %s: %w", topic, td.Err)
	}
	compact := "compact"
	resp, err := adm.CreateTopic(ctx, 1, -1, map[string]*string{"cleanup.policy": &compact}, topic)
	if err == nil {
		err = resp.Err
	}
	if err == nil {
		return &Store{c: c, topic: topic}, nil
	}
	if !errors.Is(err, kerr.TopicAlreadyExists) {
		return nil, fmt.Errorf("create topic: %w", err)
	}
	// another process created it after the describe
	details, err = adm.ListTopics(ctx, topic)
	if err != nil {
		return nil, fmt.Errorf("describe topic: %w", err)
	}
	td, ok = details[topic]
	if !ok || td.Err != nil {
		return nil, fmt.Errorf("describe topic %s: %w", topic, errors.Join(td.Err, errNoPartition))
	}
	return newChecked(ctx, adm, c, td)
}

// Bytes a chunk record may take past its chunk for its header and key and batch
const chunkSlack = 4 << 10

// A store on an existing topic once it has one partition and the configs a snapshot needs
// 1. cleanup.policy exactly compact
// 2. max.message.bytes that fits one chunk record
func newChecked(ctx context.Context, adm *kadm.Client, c *kgo.Client, td kadm.TopicDetail) (*Store, error) {
	if len(td.Partitions) != 1 {
		return nil, fmt.Errorf("%w: %s has %d partitions", errTopic, td.Topic, len(td.Partitions))
	}
	configs, err := adm.DescribeTopicConfigs(ctx, td.Topic)
	if err != nil {
		return nil, fmt.Errorf("describe topic configs: %w", err)
	}
	rc, err := configs.On(td.Topic, nil)
	if err != nil {
		return nil, fmt.Errorf("describe topic configs: %w", err)
	}
	var policy, maxBytes string
	for _, cfg := range rc.Configs {
		switch {
		case cfg.Value == nil:
		case cfg.Key == "cleanup.policy":
			policy = *cfg.Value
		case cfg.Key == "max.message.bytes":
			maxBytes = *cfg.Value
		}
	}
	if policy != "compact" {
		return nil, fmt.Errorf("%w: %s has cleanup.policy %q", errTopic, td.Topic, policy)
	}
	// a broker that leaves the config out keeps its default above a chunk
	if n, err := strconv.Atoi(maxBytes); err == nil && n < chunkBytes+chunkSlack {
		return nil, fmt.Errorf("%w: %s has max.message.bytes %d", errTopic, td.Topic, n)
	}
	return &Store{c: c, topic: td.Topic}, nil
}

// Reads the topic from the start to its end
// The records after the latest whole snapshot are the change log
func (s *Store) Load(ctx context.Context) ([]byte, iter.Seq2[[]byte, error], error) {
	adm := kadm.NewClient(s.c)
	ends, err := adm.ListEndOffsets(ctx, s.topic)
	if err != nil {
		return nil, nil, err
	}
	end, ok := ends.Lookup(s.topic, 0)
	if !ok || end.Err != nil {
		return nil, nil, fmt.Errorf("end offset of %s: %w", s.topic, errors.Join(end.Err, errNoPartition))
	}
	starts, err := adm.ListStartOffsets(ctx, s.topic)
	if err != nil {
		return nil, nil, err
	}
	start, ok := starts.Lookup(s.topic, 0)
	if !ok || start.Err != nil {
		return nil, nil, fmt.Errorf("start offset of %s: %w", s.topic, errors.Join(start.Err, errNoPartition))
	}
	st := newLoaded()
	// a log whose records were all deleted has no record to reach the end with
	if start.Offset < end.Offset {
		if err := s.read(ctx, end.Offset, st); err != nil {
			return nil, nil, err
		}
	}
	s.mu.Lock()
	s.keys, s.next = st.stale(), max(s.next, st.next)
	s.mu.Unlock()
	return st.snap, func(yield func([]byte, error) bool) {
		for _, c := range st.changes {
			if !yield(c, nil) {
				return
			}
		}
	}, nil
}

// Reads partition 0 from the start up to end with a client of its own
func (s *Store) read(ctx context.Context, end int64, st *loaded) error {
	cl, err := kgo.NewClient(append(s.c.Opts(), kgo.ConsumePartitions(map[string]map[int32]kgo.Offset{
		s.topic: {0: kgo.NewOffset().AtStart()},
	}))...)
	if err != nil {
		return err
	}
	defer cl.Close()
	for done := false; !done; {
		fetches := cl.PollFetches(ctx)
		if err := ctx.Err(); err != nil {
			return err
		}
		if errs := fetches.Errors(); len(errs) > 0 {
			return errs[0].Err
		}
		fetches.EachRecord(func(r *kgo.Record) {
			if done {
				return
			}
			st.add(r.Key, r.Value)
			done = r.Offset+1 >= end
		})
	}
	return nil
}

// Change keys are recorded before the produce
// A partly failed produce is still tombstoned later
// The idempotent producer keeps the records in order
func (s *Store) Append(ctx context.Context, records [][]byte) error {
	s.mu.Lock()
	recs := make([]*kgo.Record, len(records))
	for i, r := range records {
		key := strconv.FormatUint(s.next, 10)
		s.next++
		s.keys = append(s.keys, key)
		recs[i] = &kgo.Record{Topic: s.topic, Partition: 0, Key: []byte(key), Value: r}
	}
	s.mu.Unlock()
	return s.c.ProduceSync(ctx, recs...).FirstErr()
}

// Writes the snapshot in chunks and then tombstones the records it replaces
// The tombstones go only once every chunk is acknowledged
// A failure between the two leaves old records that the next Replace tombstones
func (s *Store) Replace(ctx context.Context, snapshot []byte) error {
	s.mu.Lock()
	gen := s.next
	s.next++
	chunks := split(gen, snapshot)
	keys := make([]string, len(chunks))
	for i := range chunks {
		keys[i] = chunkKey(gen, i)
	}
	old := s.keys
	// the next Replace tombstones the chunks
	// When this one fails they are tombstoned with the rest then
	s.keys = append(append([]string(nil), old...), keys...)
	s.mu.Unlock()
	recs := make([]*kgo.Record, len(chunks))
	for i, c := range chunks {
		recs[i] = &kgo.Record{Topic: s.topic, Partition: 0, Key: []byte(keys[i]), Value: c}
	}
	if err := s.c.ProduceSync(ctx, recs...).FirstErr(); err != nil {
		return err
	}
	tombs := make([]*kgo.Record, len(old))
	for i, k := range old {
		tombs[i] = &kgo.Record{Topic: s.topic, Partition: 0, Key: []byte(k)}
	}
	if err := s.c.ProduceSync(ctx, tombs...).FirstErr(); err != nil {
		return err
	}
	s.mu.Lock()
	s.keys = s.keys[len(old):]
	s.mu.Unlock()
	return nil
}

func chunkKey(gen uint64, i int) string {
	return snapshotKey + strconv.FormatUint(gen, 10) + "." + strconv.Itoa(i)
}

// The chunks of a snapshot
// Each is led by its generation and its index and the chunk count as uvarints
// An empty snapshot is one chunk
// It never reads as a tombstone
func split(gen uint64, snapshot []byte) [][]byte {
	n := max(1, (len(snapshot)+chunkBytes-1)/chunkBytes)
	out := make([][]byte, n)
	for i := range out {
		part := snapshot[min(i*chunkBytes, len(snapshot)):min((i+1)*chunkBytes, len(snapshot))]
		b := binary.AppendUvarint(nil, gen)
		b = binary.AppendUvarint(b, uint64(i))
		b = binary.AppendUvarint(b, uint64(n))
		out[i] = append(b, part...)
	}
	return out
}

// What a read of the topic found: the latest whole snapshot and the change records after it
type loaded struct {
	snap    []byte
	changes [][]byte
	// the chunks of each generation seen since the latest whole snapshot
	parts map[uint64][][]byte
	// keys in the order read and whether their latest record is still a value
	keys []string
	live map[string]bool
	next uint64
}

func newLoaded() *loaded { return &loaded{parts: map[uint64][][]byte{}, live: map[string]bool{}} }

func (st *loaded) add(k, value []byte) {
	key := string(k)
	if value == nil {
		// a tombstone of a record a snapshot replaced
		st.live[key] = false
		return
	}
	if _, seen := st.live[key]; !seen {
		st.keys = append(st.keys, key)
	}
	st.live[key] = true
	if strings.HasPrefix(key, snapshotKey) {
		st.chunk(value)
		return
	}
	st.changes = append(st.changes, value)
	if n, err := strconv.ParseUint(key, 10, 64); err == nil && n >= st.next {
		st.next = n + 1
	}
}

// Collects one chunk and takes the snapshot once its generation is whole
// A chunk that does not parse is skipped
// A later Replace writes a whole snapshot again
func (st *loaded) chunk(value []byte) {
	gen, a := binary.Uvarint(value)
	if a <= 0 {
		return
	}
	i, b := binary.Uvarint(value[a:])
	if b <= 0 {
		return
	}
	n, c := binary.Uvarint(value[a+b:])
	if c <= 0 || i >= n || n > uint64(len(value)) {
		return
	}
	st.next = max(st.next, gen+1)
	parts := st.parts[gen]
	if parts == nil {
		parts = make([][]byte, n)
		st.parts[gen] = parts
	}
	if uint64(len(parts)) != n {
		return
	}
	parts[i] = value[a+b+c:]
	var snap []byte
	for _, p := range parts {
		if p == nil {
			return
		}
		snap = append(snap, p...)
	}
	st.snap, st.changes = nonNil(snap), nil
	st.parts = map[uint64][][]byte{}
}

// Keys the next Replace tombstones
// Every key whose latest record is a value with the latest snapshot's chunks among them
func (st *loaded) stale() []string {
	var out []string
	for _, k := range st.keys {
		if st.live[k] {
			out = append(out, k)
		}
	}
	return out
}

// An empty snapshot must not read as a missing one
func nonNil(b []byte) []byte {
	if b == nil {
		return []byte{}
	}
	return b
}
