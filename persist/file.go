package persist

import (
	"context"
	"errors"
	"io/fs"
	"iter"
	"os"
	"path/filepath"
	"sync"
)

// A store in one directory
// snapshot is replaced by a write and an atomic rename
// changes is appended with an fsync per flush
// One process writes a directory and any number read it
type FileStore struct {
	dir string
	mu  sync.Mutex
	// the log ends with a whole record
	// The first Append of a store cuts a torn tail a crash left
	clean bool
}

const (
	snapshotFile = "snapshot"
	changesFile  = "changes"
)

// The store in dir created on first use
func NewFileStore(dir string) (*FileStore, error) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, err
	}
	return &FileStore{dir: dir}, nil
}

// The snapshot and the whole records of the log
// It never writes
// A torn tail from a crash mid append is left for the writer's next Append to cut
// A Replace of another process between the two reads makes it read both again
func (s *FileStore) Load(ctx context.Context) ([]byte, iter.Seq2[[]byte, error], error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	path := filepath.Join(s.dir, snapshotFile)
	var snap, log []byte
	for {
		before, err := os.Stat(path)
		if err != nil && !errors.Is(err, fs.ErrNotExist) {
			return nil, nil, err
		}
		if snap, err = readOptional(path); err != nil {
			return nil, nil, err
		}
		if log, err = readOptional(filepath.Join(s.dir, changesFile)); err != nil {
			return nil, nil, err
		}
		after, err := os.Stat(path)
		if err != nil && !errors.Is(err, fs.ErrNotExist) {
			return nil, nil, err
		}
		// a rename gives the snapshot another file
		if (before == nil && after == nil) || (before != nil && after != nil && os.SameFile(before, after)) {
			break
		}
	}
	frames, _ := splitFrames(log)
	return snap, func(yield func([]byte, error) bool) {
		for _, f := range frames {
			if !yield(f, nil) {
				return
			}
		}
	}, nil
}

// A failed write is cut back off the file
// A later append never follows a torn record
func (s *FileStore) Append(ctx context.Context, records [][]byte) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	path := filepath.Join(s.dir, changesFile)
	if !s.clean {
		if err := cutTornTail(path); err != nil {
			return err
		}
		s.clean = true
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return err
	}
	info, err := f.Stat()
	if err != nil {
		_ = f.Close()
		return err
	}
	var buf []byte
	for _, r := range records {
		buf = append(buf, r...)
	}
	_, err = f.Write(buf)
	if err == nil {
		err = f.Sync()
	}
	if err != nil {
		_ = f.Truncate(info.Size())
		_ = f.Close()
		return err
	}
	return f.Close()
}

// Writes the snapshot beside the old one
// Then renames it over and empties the log
// A crash before the log is emptied leaves records the snapshot already holds
// Load skips them by seq
func (s *FileStore) Replace(ctx context.Context, snapshot []byte) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	tmp := filepath.Join(s.dir, snapshotFile+".tmp")
	f, err := os.OpenFile(tmp, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o644)
	if err != nil {
		return err
	}
	if _, err := f.Write(snapshot); err != nil {
		_ = f.Close()
		return err
	}
	if err := f.Sync(); err != nil {
		_ = f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	if err := os.Rename(tmp, filepath.Join(s.dir, snapshotFile)); err != nil {
		return err
	}
	if err := syncDir(s.dir); err != nil {
		return err
	}
	err = os.Truncate(filepath.Join(s.dir, changesFile), 0)
	if errors.Is(err, fs.ErrNotExist) {
		err = nil
	}
	s.clean = err == nil
	return err
}

// The bytes of a file and nil when it does not exist
func readOptional(path string) ([]byte, error) {
	b, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	return b, err
}

// Cuts the log after its last whole record
func cutTornTail(path string) error {
	log, err := readOptional(path)
	if err != nil {
		return err
	}
	if _, good := splitFrames(log); good < len(log) {
		return os.Truncate(path, int64(good))
	}
	return nil
}

func syncDir(dir string) error {
	d, err := os.Open(dir)
	if err != nil {
		return err
	}
	defer func() { _ = d.Close() }()
	return d.Sync()
}
