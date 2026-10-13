// Package redis is a persist store in Redis: one key for the snapshot and one list for the change log
package redis

import (
	"context"
	"errors"
	"iter"

	goredis "github.com/redis/go-redis/v9"

	"github.com/jeon-jihyeon/moldrain/persist"
)

// The snapshot and the change log under one key prefix
type Store struct {
	c        goredis.UniversalClient
	snapshot string
	changes  string
}

var _ persist.Store = (*Store)(nil)

// A store under prefix such as "moldrain:checkout"
// The caller owns the client and closes it
func New(c goredis.UniversalClient, prefix string) *Store {
	// a hash tag keeps both keys in one slot
	// MULTI works on a cluster too
	tag := "{" + prefix + "}"
	return &Store{c: c, snapshot: tag + ":snapshot", changes: tag + ":changes"}
}

// Reads the snapshot and the whole log in one MULTI
// A Replace never lands between the two
// The log stays small since the persister compacts it past CompactBytes
func (s *Store) Load(ctx context.Context) ([]byte, iter.Seq2[[]byte, error], error) {
	var get *goredis.StringCmd
	var lrange *goredis.StringSliceCmd
	_, err := s.c.TxPipelined(ctx, func(p goredis.Pipeliner) error {
		get = p.Get(ctx, s.snapshot)
		lrange = p.LRange(ctx, s.changes, 0, -1)
		return nil
	})
	if err != nil && !errors.Is(err, goredis.Nil) {
		return nil, nil, err
	}
	snap, err := get.Bytes()
	if errors.Is(err, goredis.Nil) {
		snap = nil
	} else if err != nil {
		return nil, nil, err
	}
	recs, err := lrange.Result()
	if err != nil {
		return nil, nil, err
	}
	return snap, func(yield func([]byte, error) bool) {
		for _, r := range recs {
			if !yield([]byte(r), nil) {
				return
			}
		}
	}, nil
}

func (s *Store) Append(ctx context.Context, records [][]byte) error {
	if len(records) == 0 {
		return nil
	}
	vals := make([]any, len(records))
	for i, r := range records {
		vals[i] = r
	}
	return s.c.RPush(ctx, s.changes, vals...).Err()
}

// Sets the snapshot and drops the log in one MULTI
func (s *Store) Replace(ctx context.Context, snapshot []byte) error {
	_, err := s.c.TxPipelined(ctx, func(p goredis.Pipeliner) error {
		p.Set(ctx, s.snapshot, snapshot, 0)
		p.Del(ctx, s.changes)
		return nil
	})
	return err
}
