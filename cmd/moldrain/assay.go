package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"sync"
	"time"

	"github.com/jeon-jihyeon/moldrain"
	"github.com/jeon-jihyeon/moldrain/assay"
	"github.com/jeon-jihyeon/moldrain/persist"
)

// How often the windows of quiet profiles are closed while lines come
const flushEvery = time.Second

// Trains on the input and prints one JSON record per line as windows close
// Flushes every window at the end
func runAssay(args []string, stdin io.Reader, stdout io.Writer, getenv func(string) string) error {
	c := flags("assay", getenv)
	profiles := c.fs.String("profiles", "", "JSON file with an array of profiles")
	window := c.fs.Duration("window", assay.DefaultWindow, "tumbling window width")
	lateness := c.fs.Duration("lateness", assay.DefaultLateness, "how long a window stays open after its end")
	if err := c.fs.Parse(args); err != nil {
		return err
	}
	if *profiles == "" {
		return errors.New("--profiles is required")
	}
	// the final flush closes the windows by these widths
	// A zero the library would default leaves them open
	if *window <= 0 || *lateness <= 0 {
		return fmt.Errorf("--window %v and --lateness %v must be positive", *window, *lateness)
	}
	ps, err := readProfiles(*profiles)
	if err != nil {
		return err
	}
	m, err := c.miner()
	if err != nil {
		return err
	}
	ctx := context.Background()
	var p *persist.Persister
	if *c.state != "" {
		store, err := persist.NewFileStore(*c.state)
		if err != nil {
			return err
		}
		if p, err = persist.Attach(ctx, m, store, persist.Options{}); err != nil {
			return err
		}
	}
	opt := assay.Options{Window: *window, Lateness: *lateness}
	a, err := assay.New(m, ps, opt)
	if err != nil {
		return errors.Join(err, closePersister(ctx, p))
	}
	sink := &jsonSink{enc: json.NewEncoder(stdout)}
	a.OnRecord(sink.write)
	last, err := observe(m, a, c.fs.Args(), stdin, sink)
	a.Flush(last.Add(opt.Window + opt.Lateness))
	a.Close()
	return errors.Join(err, sink.failed(), closePersister(ctx, p))
}

// Trains and observes every line while a ticker closes the windows that ended
// Returns the time of the last line
func observe(m *moldrain.Miner, a *assay.Assay, files []string, stdin io.Reader, sink *jsonSink) (time.Time, error) {
	stop, ticked := make(chan struct{}), make(chan struct{})
	go func() {
		defer close(ticked)
		t := time.NewTicker(flushEvery)
		defer t.Stop()
		for {
			select {
			case <-stop:
				return
			case now := <-t.C:
				a.Flush(now)
			}
		}
	}()
	last := time.Now()
	err := eachLine(files, stdin, func(line []byte) error {
		last = time.Now()
		a.Observe(m.Train(line, last), last)
		return sink.failed()
	})
	close(stop)
	<-ticked
	return last, err
}

// Writes each record as one JSON line once its window closes
// It runs on the delivery goroutine of the assay
type jsonSink struct {
	mu  sync.Mutex
	enc *json.Encoder
	// the first write that failed
	err error
}

func (s *jsonSink) write(r assay.Record) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.err == nil {
		s.err = s.enc.Encode(r)
	}
}

func (s *jsonSink) failed() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.err
}

func readProfiles(name string) ([]assay.Profile, error) {
	f, err := os.Open(name)
	if err != nil {
		return nil, err
	}
	defer func() { _ = f.Close() }()
	return assay.LoadProfiles(f)
}

func closePersister(ctx context.Context, p *persist.Persister) error {
	if p == nil {
		return nil
	}
	return p.Close(ctx)
}
