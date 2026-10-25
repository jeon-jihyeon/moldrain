package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
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
	in := inputFlags(c)
	profiles := c.fs.String("profiles", "", "JSON file with an array of profiles")
	window := c.fs.Duration("window", assay.DefaultWindow, "tumbling window width")
	lateness := c.fs.Duration("lateness", assay.DefaultLateness, "how long a window stays open after its end")
	if err := c.fs.Parse(args); err != nil {
		return err
	}
	if *profiles == "" {
		return errors.New("--profiles is required")
	}
	opt, err := in.assayOptions(*window, *lateness)
	if err != nil {
		return err
	}
	ps, err := readProfiles(*profiles)
	if err != nil {
		return err
	}
	m, err := c.miner(opt.Clock)
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
	a, err := assay.New(m, ps, opt)
	if err != nil {
		return errors.Join(err, closePersister(ctx, p))
	}
	sink := &jsonSink{enc: json.NewEncoder(stdout)}
	a.OnRecord(sink.write)
	var last time.Time
	if *in.replay != "" {
		last, err = observeReplay(m, a, c.fs.Args(), stdin, sink, opt)
	} else {
		last, err = observe(m, a, c.fs.Args(), stdin, sink, *in.records, opt)
	}
	if !last.IsZero() {
		a.Flush(last.Add(opt.Window + opt.Lateness))
	}
	a.Close()
	return errors.Join(err, sink.failed(), closePersister(ctx, p))
}

func (in inputOptions) assayOptions(window, lateness time.Duration) (assay.Options, error) {
	// The EOF flush needs explicit positive widths instead of library defaults
	if window <= 0 || lateness <= 0 {
		return assay.Options{}, fmt.Errorf("--window %v and --lateness %v must be positive", window, lateness)
	}
	if window > time.Duration(math.MaxInt64)-lateness {
		return assay.Options{}, errors.New("--window and --lateness sum exceeds duration range")
	}
	clock, err := in.clock()
	if err != nil {
		return assay.Options{}, err
	}
	if !safeAssayTime(clock(), window, lateness) {
		return assay.Options{}, errors.New("clock outside safe assay range for window and lateness")
	}
	return assay.Options{Window: window, Lateness: lateness, Clock: clock}, nil
}

// Wall clock closes quiet live windows independently of record timestamps
func observe(m *moldrain.Miner, a *assay.Assay, files []string, stdin io.Reader,
	sink *jsonSink, records bool, opt assay.Options) (time.Time, error) {
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
	last := opt.Clock()
	err := eachInput(files, stdin, records, func(line inputLine) error {
		if !safeAssayTime(line.at, opt.Window, opt.Lateness) {
			return errors.New("record time outside safe assay range for window and lateness")
		}
		if !line.at.After(opt.Clock().Add(opt.Window)) && line.at.After(last) {
			last = line.at
		}
		a.Observe(m.Train(line.body, line.at), line.at)
		return sink.failed()
	})
	close(stop)
	<-ticked
	return last, err
}

func observeReplay(m *moldrain.Miner, a *assay.Assay, files []string, stdin io.Reader,
	sink *jsonSink, opt assay.Options) (time.Time, error) {
	var latest time.Time
	err := eachInput(files, stdin, true, func(line inputLine) error {
		if !safeAssayTime(line.at, opt.Window, opt.Lateness) {
			return errors.New("record time outside safe assay range for window and lateness")
		}
		a.Observe(m.Train(line.body, line.at), line.at)
		if !line.at.After(opt.Clock().Add(opt.Window)) && line.at.After(latest) {
			latest = line.at
		}
		return sink.failed()
	})
	return latest, err
}

func safeAssayTime(at time.Time, window, lateness time.Duration) bool {
	ns, margin := at.UnixNano(), int64(window+lateness)
	return time.Unix(0, ns).Equal(at) && ns >= math.MinInt64+margin && ns <= math.MaxInt64-margin
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
