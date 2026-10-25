// Command moldrain learns log templates from files or stdin and matches lines against them
package main

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"time"

	"github.com/jeon-jihyeon/moldrain"
	"github.com/jeon-jihyeon/moldrain/persist"
)

var version = "dev"

const usage = `moldrain learns log templates the way Drain does

  moldrain mine [--state <dir>] [--tokenizer plain|punct|json|logfmt] [--sim 0.4] [--json] [file ...]
  moldrain match --state <dir> [--tokenizer plain|punct|json|logfmt] [--sim 0.4] [--strict] [--json] [file ...]
  moldrain show --state <dir> [--tokenizer plain|punct|json|logfmt] [--sim 0.4] [--json]
  moldrain assay --profiles <file.json> [--window 5m] [--lateness 30s] [--state <dir>] [--tokenizer plain|punct|json|logfmt] [--sim 0.4] [file ...]
  moldrain version

Lines come from stdin when no file is given. MOLDRAIN_STATE sets --state when the flag is absent
mine, match and assay accept --records for moldrain.record/v1 JSONL and --replay-at for a trusted historical clock
match --records requires --json so collector provenance remains in the result
`

// Exit when match --strict met a line no template fits
var errUnmatched = errors.New("some lines matched no template")

func main() {
	err := run(os.Args[1:], os.Stdin, os.Stdout, os.Getenv)
	switch {
	case err == nil:
	case errors.Is(err, errUnmatched):
		os.Exit(1)
	case errors.Is(err, flag.ErrHelp):
		os.Exit(2)
	default:
		fmt.Fprintln(os.Stderr, "moldrain:", err)
		os.Exit(2)
	}
}

func run(args []string, stdin io.Reader, stdout io.Writer, getenv func(string) string) error {
	if len(args) == 0 {
		fmt.Fprint(stdout, usage)
		return flag.ErrHelp
	}
	switch args[0] {
	case "mine":
		return mine(args[1:], stdin, stdout, getenv)
	case "match":
		return match(args[1:], stdin, stdout, getenv)
	case "show":
		return show(args[1:], stdout, getenv)
	case "assay":
		return runAssay(args[1:], stdin, stdout, getenv)
	case "version":
		fmt.Fprintln(stdout, version)
		return nil
	}
	fmt.Fprint(stdout, usage)
	return fmt.Errorf("unknown command %q", args[0])
}

// The flags every command shares
// The template options must match those of the stored state
type common struct {
	fs        *flag.FlagSet
	state     *string
	tokenizer *string
	sim       *float64
}

func flags(name string, getenv func(string) string) common {
	fs := flag.NewFlagSet(name, flag.ContinueOnError)
	return common{
		fs:        fs,
		state:     fs.String("state", getenv("MOLDRAIN_STATE"), "state directory, none to learn from scratch"),
		tokenizer: fs.String("tokenizer", "plain", "plain, punct, json or logfmt"),
		sim:       fs.Float64("sim", 0.4, "similarity a line needs to join a template"),
	}
}

func jsonFlag(c common) *bool { return c.fs.Bool("json", false, "print JSON lines") }

func (c common) miner(clock func() time.Time) (*moldrain.Miner, error) {
	return moldrain.New(moldrain.Options{Tokenizer: moldrain.Tokenizer(*c.tokenizer), SimTh: *c.sim, CountClock: clock})
}

func mine(args []string, stdin io.Reader, stdout io.Writer, getenv func(string) string) error {
	c := flags("mine", getenv)
	asJSON := jsonFlag(c)
	in := inputFlags(c)
	if err := c.fs.Parse(args); err != nil {
		return err
	}
	clock, err := in.clock()
	if err != nil {
		return err
	}
	m, err := c.miner(clock)
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
	err = eachInput(c.fs.Args(), stdin, *in.records, func(line inputLine) error {
		m.Train(line.body, line.at)
		return nil
	})
	if p != nil {
		err = errors.Join(err, p.Close(ctx))
	}
	if err != nil {
		return err
	}
	m.Publish()
	return printClusters(stdout, m.Clusters(), *asJSON)
}

func load(c common, clock func() time.Time) (*moldrain.Miner, error) {
	if *c.state == "" {
		return nil, errors.New("--state is required")
	}
	m, err := c.miner(clock)
	if err != nil {
		return nil, err
	}
	store, err := persist.NewFileStore(*c.state)
	if err != nil {
		return nil, err
	}
	ok, err := persist.Load(context.Background(), m, store)
	if err != nil {
		return nil, err
	}
	if !ok {
		return nil, fmt.Errorf("no state in %s", *c.state)
	}
	return m, nil
}

func match(args []string, stdin io.Reader, stdout io.Writer, getenv func(string) string) error {
	c := flags("match", getenv)
	strict := c.fs.Bool("strict", false, "exit 1 when any line matched no template")
	asJSON := jsonFlag(c)
	in := inputFlags(c)
	if err := c.fs.Parse(args); err != nil {
		return err
	}
	clock, err := in.clock()
	if err != nil {
		return err
	}
	if *in.records && !*asJSON {
		return errors.New("match --records requires --json to preserve provenance")
	}
	m, err := load(c, clock)
	if err != nil {
		return err
	}
	w := bufio.NewWriter(stdout)
	enc := json.NewEncoder(stdout)
	limit := m.Options().MaxLineBytes
	missed := false
	err = eachInput(c.fs.Args(), stdin, *in.records, func(line inputLine) error {
		r, ok := m.Match(line.body)
		missed = missed || !ok
		if *asJSON {
			row := newMatchRow(line.body, r, ok, limit)
			row.Record, row.TimeBasis = line.record, line.basis
			return enc.Encode(row)
		}
		id := "-"
		if ok {
			id = r.ID.String()
		}
		_, err := fmt.Fprintf(w, "%s\t%s\n", id, line.body)
		return err
	})
	if err = errors.Join(err, w.Flush()); err != nil {
		return err
	}
	if missed && *strict {
		return errUnmatched
	}
	return nil
}

func show(args []string, stdout io.Writer, getenv func(string) string) error {
	c := flags("show", getenv)
	asJSON := jsonFlag(c)
	if err := c.fs.Parse(args); err != nil {
		return err
	}
	m, err := load(c, time.Now)
	if err != nil {
		return err
	}
	return printClusters(stdout, m.Clusters(), *asJSON)
}

func printClusters(stdout io.Writer, cs []moldrain.Cluster, asJSON bool) error {
	w := bufio.NewWriter(stdout)
	enc := json.NewEncoder(w)
	for _, c := range cs {
		if asJSON {
			row := struct {
				ID       string `json:"id"`
				Count    uint64 `json:"count"`
				Template string `json:"template"`
			}{c.ID.String(), c.Count, c.Template}
			if err := enc.Encode(row); err != nil {
				return err
			}
			continue
		}
		fmt.Fprintf(w, "%d\t%s\t%s\n", c.Count, c.ID, c.Template)
	}
	return w.Flush()
}

// Calls fn per line of each file
// Reads stdin when no file is given
func eachLine(files []string, stdin io.Reader, fn func([]byte) error) error {
	scan := func(r io.Reader) error {
		sc := bufio.NewScanner(r)
		sc.Buffer(make([]byte, 0, 64<<10), 16<<20)
		for sc.Scan() {
			if err := fn(sc.Bytes()); err != nil {
				return err
			}
		}
		return sc.Err()
	}
	if len(files) == 0 {
		return scan(stdin)
	}
	for _, name := range files {
		f, err := os.Open(name)
		if err != nil {
			return err
		}
		err = scan(f)
		_ = f.Close()
		if err != nil {
			return fmt.Errorf("%s: %w", name, err)
		}
	}
	return nil
}
