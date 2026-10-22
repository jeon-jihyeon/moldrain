// Command bench measures moldrain against Drain3 and the drain port of faceair on Loghub 2.0
//
//	go run ./bench -data <datasets dir> -out <results dir> [-datasets all] [-contenders all] [-runs 3] [-match=true] [-jev <url>] [-jev-model <name>] [-jev-key-file <path>]
//
// The datasets dir is what the fetch command writes
// results.json and a markdown table go to the results dir
package main

import (
	"bufio"
	"bytes"
	"context"
	_ "embed"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/jeon-jihyeon/moldrain"
	"github.com/jeon-jihyeon/moldrain/bench/faceair"
	"github.com/jeon-jihyeon/moldrain/resolve"
	"github.com/jeon-jihyeon/moldrain/resolve/jev"
)

// The Drain3 runner
// Written next to its outputs so the bench runs from any directory
//
//go:embed drain3/run.py
var drain3Runner []byte

var allDatasets = []string{
	"Apache", "BGL", "HDFS", "HPC", "Hadoop", "HealthApp", "Linux",
	"Mac", "OpenSSH", "OpenStack", "Proxifier", "Spark", "Thunderbird", "Zookeeper",
}

// The contenders in the order the table lists them
// jev runs only when -jev names an endpoint
var allContenders = []string{"moldrain", "respecialize", "spans", "punct", "oracle", "jev", "drain3", "faceair"}

// Contenders whose throughput is measured over -runs runs
// The rest run once as only their accuracy counts
var timed = map[string]bool{"moldrain": true, "drain3": true, "faceair": true}

// Contenders that train with a resolver
// Their time includes the resolver's so the table shows no throughput for them
var resolved = map[string]bool{"oracle": true, "jev": true}

type Row struct {
	Dataset   string `json:"dataset"`
	Contender string `json:"contender"`
	Lines     int    `json:"lines"`
	Accuracy
	// Lines per second of training
	// The median of the runs
	LinesPerSec float64   `json:"lines_per_sec"`
	Runs        []float64 `json:"run_seconds"`
	// Allocations per line during training
	// Go contenders only
	AllocsPerLine float64 `json:"allocs_per_line,omitempty"`
	BytesPerLine  float64 `json:"bytes_per_line,omitempty"`
	// The live heap the parser holds after the last line
	// Go contenders only
	HeapBytes uint64 `json:"heap_bytes,omitempty"`
	// Peak resident memory of the Python process with its lines
	// Drain3 only
	MaxRSSKiB int64 `json:"max_rss_kib,omitempty"`
	// Match per second at 1, 4 and 8 goroutines while one goroutine trains
	// moldrain only
	MatchPerSec map[string]float64 `json:"match_per_sec,omitempty"`
	// Lines the resolver was asked about counted once per try
	// Contenders with a resolver only
	ResolverAsked uint64 `json:"resolver_asked,omitempty"`
	// Resolve calls that failed
	// One makes the row an error so a dead endpoint never scores as a resolver
	ResolverFailed uint64 `json:"resolver_failed,omitempty"`
	Error          string `json:"error,omitempty"`
}

type Run struct {
	Date     string `json:"date"`
	Machine  string `json:"machine"`
	Go       string `json:"go"`
	Python   string `json:"python,omitempty"`
	Drain3   string `json:"drain3,omitempty"`
	Faceair  string `json:"faceair"`
	Commit   string `json:"commit"`
	Datasets string `json:"datasets"`
	Rows     []Row  `json:"rows"`
}

func main() {
	data := flag.String("data", "", "datasets directory written by bench/fetch")
	out := flag.String("out", "", "results directory")
	datasets := flag.String("datasets", "all", "comma separated datasets, or all")
	contenders := flag.String("contenders", "all", "comma separated contenders, or all")
	runs := flag.Int("runs", 3, "runs of each timed contender, the median is reported")
	match := flag.Bool("match", true, "measure moldrain Match scaling")
	jevURL := flag.String("jev", "", "Jev endpoint the jev contender resolves with, none to leave it out")
	jevModel := flag.String("jev-model", "", "model the Jev endpoint answers with")
	jevKeyFile := flag.String("jev-key-file", "", "file holding the bearer token of the Jev endpoint")
	flag.Parse()
	if *data == "" || *out == "" {
		fmt.Fprintln(os.Stderr, "bench: -data and -out are required")
		os.Exit(2)
	}
	cs := list(*contenders, allContenders)
	if *jevURL == "" && *contenders == "all" {
		cs = slices.DeleteFunc(cs, func(c string) bool { return c == "jev" })
	}
	// a token on the command line would show in the process list
	key, err := readKey(*jevKeyFile)
	if err != nil {
		fmt.Fprintln(os.Stderr, "bench:", err)
		os.Exit(2)
	}
	env := runEnv{runs: *runs, match: *match, jev: jev.Options{URL: *jevURL, Model: *jevModel, Key: key}}
	if err := run(*data, *out, list(*datasets, allDatasets), cs, env); err != nil {
		fmt.Fprintln(os.Stderr, "bench:", err)
		os.Exit(1)
	}
}

// The token in a key file without its surrounding whitespace
// Empty for no file
func readKey(path string) (string, error) {
	if path == "" {
		return "", nil
	}
	b, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(b)), nil
}

// A copy of all or of the names in s
// A caller may change it
func list(s string, all []string) []string {
	if s == "all" {
		return slices.Clone(all)
	}
	return strings.Split(s, ",")
}

// What a run takes besides its datasets and contenders
type runEnv struct {
	runs  int
	match bool
	jev   jev.Options
}

func run(data, out string, datasets, contenders []string, env runEnv) error {
	if err := os.MkdirAll(out, 0o755); err != nil {
		return err
	}
	res := Run{
		Date:     time.Now().Format(time.RFC3339),
		Machine:  machine(),
		Go:       runtime.Version(),
		Faceair:  "v0.0.0-20220227014011-bcc52881b814",
		Commit:   commit(),
		Datasets: "Loghub 2.0, Zenodo record 8275861",
	}
	for _, ds := range datasets {
		lines, tr, err := load(filepath.Join(data, ds))
		if err != nil {
			return fmt.Errorf("%s: %w", ds, err)
		}
		for _, c := range contenders {
			fmt.Fprintf(os.Stderr, "%s %s (%d lines)\n", ds, c, len(lines))
			row := Row{Dataset: ds, Contender: c, Lines: len(lines)}
			n := 1
			if timed[c] {
				n = env.runs
			}
			var p parse
			for i := 0; i < n; i++ {
				p, err = contend(c, lines, tr, filepath.Join(data, ds), out, env.jev)
				if err != nil {
					break
				}
				row.Runs = append(row.Runs, p.seconds)
			}
			if err != nil {
				row.Error = err.Error()
				res.Rows = append(res.Rows, row)
				continue
			}
			row.Accuracy = measure(p.groups, p.templates, tr)
			row.LinesPerSec = float64(len(lines)) / median(row.Runs)
			row.AllocsPerLine, row.BytesPerLine, row.HeapBytes, row.MaxRSSKiB = p.allocs, p.bytes, p.heap, p.rss
			row.ResolverAsked, row.ResolverFailed = p.asked, p.failed
			if p.failed > 0 {
				row.Error = fmt.Sprintf("%d resolve calls failed", p.failed)
			}
			if p.python != "" {
				res.Python, res.Drain3 = p.python, p.drain3
			}
			if c == "moldrain" && env.match {
				row.MatchPerSec = matchScaling(lines)
			}
			res.Rows = append(res.Rows, row)
			fmt.Fprintf(os.Stderr, "  GA %.3f FGA %.3f PA %.3f FTA %.3f groups %d/%d %.0f lines/s\n",
				row.GA, row.FGA, row.PA, row.FTA, row.Groups, row.Events, row.LinesPerSec)
		}
		runtime.GC()
	}
	b, err := json.MarshalIndent(res, "", "  ")
	if err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(out, "results.json"), b, 0o644); err != nil {
		return err
	}
	table := markdown(res)
	fmt.Print(table)
	return os.WriteFile(filepath.Join(out, "table.md"), []byte(table), 0o644)
}

// One parse of a dataset
// The group and template of each line and what it cost
type parse struct {
	groups    []uint64
	templates map[uint64]string
	seconds   float64
	allocs    float64
	bytes     float64
	heap      uint64
	rss       int64
	python    string
	drain3    string
	// resolve calls of a contender with a resolver
	asked  uint64
	failed uint64
}

func contend(c string, lines [][]byte, tr *truth, dir, out string, jo jev.Options) (parse, error) {
	switch c {
	case "moldrain":
		return runMoldrain(lines, tr, setup{}), nil
	case "respecialize":
		return runMoldrain(lines, tr, setup{opt: moldrain.Options{Respecialize: true}}), nil
	case "spans":
		return runMoldrain(lines, tr, setup{opt: moldrain.Options{Spans: true}}), nil
	case "punct":
		return runMoldrain(lines, tr, setup{opt: moldrain.Options{Tokenizer: moldrain.Punct}}), nil
	case "oracle":
		return runMoldrain(lines, tr, setup{oracle: true}), nil
	case "jev":
		if jo.URL == "" {
			return parse{}, errors.New("no -jev endpoint")
		}
		return runMoldrain(lines, tr, setup{resolver: jev.New(jo)}), nil
	case "faceair":
		return runFaceair(lines), nil
	case "drain3":
		return runDrain3(filepath.Join(dir, "content.txt"), out)
	}
	return parse{}, fmt.Errorf("unknown contender %q", c)
}

// How a moldrain contender runs
type setup struct {
	opt moldrain.Options
	// answers from the ground truth
	oracle bool
	// a resolver of its own such as Jev
	resolver resolve.Resolver
}

func runMoldrain(lines [][]byte, tr *truth, s setup) parse {
	m, err := moldrain.New(s.opt)
	if err != nil {
		panic(err)
	}
	t := newTracker(s.oracle)
	m.Subscribe(t.onEvent)
	r := s.resolver
	if s.oracle {
		r = t
	}
	var loop *resolve.Loop
	if r != nil {
		// the bench flushes the loop itself
		// The batches come at the same lines in every run
		loop = resolve.Attach(m, r, resolve.Options{ResolveEvery: time.Hour, ResolveBatch: 1 << 30, MaxPending: 1 << 30})
	}
	labels := make([]int32, len(lines))
	at := time.Unix(1700000000, 0)
	ctx := context.Background()
	runtime.GC()
	var before, after runtime.MemStats
	runtime.ReadMemStats(&before)
	start := time.Now()
	for i, l := range lines {
		if s.oracle {
			t.next(tr.events[i])
		}
		res := m.Train(l, at)
		labels[i] = t.line(res.ID, tr.events[i])
		if loop != nil && i%2000 == 1999 {
			m.Publish()
			loop.Flush(ctx)
		}
	}
	elapsed := time.Since(start).Seconds()
	runtime.ReadMemStats(&after)
	var calls resolve.Stats
	if loop != nil {
		m.Publish()
		loop.Flush(ctx)
		calls = loop.Stats()
		loop.Close()
	}
	m.Publish()
	// the live heap now is the miner and the tracker
	// The outputs below are built after
	runtime.GC()
	var end runtime.MemStats
	runtime.ReadMemStats(&end)
	p := parse{
		groups:    make([]uint64, len(lines)),
		templates: map[uint64]string{},
		seconds:   elapsed,
		allocs:    float64(after.Mallocs-before.Mallocs) / float64(len(lines)),
		bytes:     float64(after.TotalAlloc-before.TotalAlloc) / float64(len(lines)),
		asked:     calls.Asked,
		failed:    calls.Failed,
	}
	for i, l := range labels {
		p.groups[i] = uint64(t.find(l))
	}
	for _, c := range m.Clusters() {
		if l, ok := t.cur[c.ID]; ok {
			p.templates[uint64(t.find(l))] = c.Template
		}
	}
	if end.HeapAlloc > before.HeapAlloc {
		p.heap = end.HeapAlloc - before.HeapAlloc
	}
	return p
}

func runFaceair(lines [][]byte) parse {
	groups := make([]uint64, len(lines))
	runtime.GC()
	var before, after, end runtime.MemStats
	runtime.ReadMemStats(&before)
	start := time.Now()
	p := faceair.New()
	for i, l := range lines {
		groups[i] = p.Train(l)
	}
	elapsed := time.Since(start).Seconds()
	runtime.ReadMemStats(&after)
	// the live heap now is the parser
	// Its templates are built after
	runtime.GC()
	runtime.ReadMemStats(&end)
	out := parse{
		groups: groups, templates: p.Templates(), seconds: elapsed,
		allocs: float64(after.Mallocs-before.Mallocs) / float64(len(lines)),
		bytes:  float64(after.TotalAlloc-before.TotalAlloc) / float64(len(lines)),
	}
	if end.HeapAlloc > before.HeapAlloc {
		out.heap = end.HeapAlloc - before.HeapAlloc
	}
	return out
}

// Runs the Python runner under uv with Drain3 0.9.11
func runDrain3(content, out string) (parse, error) {
	dir, err := os.MkdirTemp(out, "drain3-")
	if err != nil {
		return parse{}, err
	}
	defer func() { _ = os.RemoveAll(dir) }()
	script := filepath.Join(dir, "run.py")
	if err := os.WriteFile(script, drain3Runner, 0o600); err != nil {
		return parse{}, err
	}
	cmd := exec.Command("uv", "run", "--quiet", "--with", "drain3==0.9.11", "python", "-I", script, content, dir)
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		return parse{}, fmt.Errorf("drain3 runner: %w", err)
	}
	return readDrain3(dir)
}

// The parse the Drain3 runner wrote to dir
func readDrain3(dir string) (parse, error) {
	var stats struct {
		Lines   int     `json:"lines"`
		Seconds float64 `json:"seconds"`
		RSS     int64   `json:"max_rss_kib"`
		Python  string  `json:"python"`
		Drain3  string  `json:"drain3"`
	}
	if err := readJSON(filepath.Join(dir, "stats.json"), &stats); err != nil {
		return parse{}, err
	}
	var templates map[string]string
	if err := readJSON(filepath.Join(dir, "templates.json"), &templates); err != nil {
		return parse{}, err
	}
	ids, err := os.ReadFile(filepath.Join(dir, "ids.txt"))
	if err != nil {
		return parse{}, err
	}
	p := parse{templates: map[uint64]string{}, seconds: stats.Seconds, rss: stats.RSS, python: stats.Python, drain3: stats.Drain3}
	for _, f := range bytes.Fields(ids) {
		v, err := strconv.ParseUint(string(f), 10, 64)
		if err != nil {
			return parse{}, err
		}
		p.groups = append(p.groups, v)
	}
	if len(p.groups) != stats.Lines {
		return parse{}, errors.New("drain3 ids do not cover every line")
	}
	for k, v := range templates {
		id, err := strconv.ParseUint(k, 10, 64)
		if err != nil {
			return parse{}, err
		}
		p.templates[id] = v
	}
	return p, nil
}

func readJSON(path string, v any) error {
	b, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	return json.Unmarshal(b, v)
}

// How long one Match scaling measurement runs
// A small dataset loops so every goroutine count is measured over the same time
const matchFor = 2 * time.Second

// Matches per second at 1, 4 and 8 goroutines
// The median of 3 measurements of matchFor each
// Each goroutine loops over the dataset from its own offset
// One goroutine retrains the dataset meanwhile
func matchScaling(lines [][]byte) map[string]float64 {
	m, err := moldrain.New(moldrain.Options{})
	if err != nil {
		panic(err)
	}
	at := time.Unix(1700000000, 0)
	for _, l := range lines {
		m.Train(l, at)
	}
	m.Publish()
	out := map[string]float64{}
	for _, g := range []int{1, 4, 8} {
		var rates []float64
		for r := 0; r < 3; r++ {
			rates = append(rates, matchRate(m, lines, g, at))
		}
		out[strconv.Itoa(g)] = median(rates)
	}
	return out
}

// Matches per second of g goroutines over matchFor while one goroutine trains
func matchRate(m *moldrain.Miner, lines [][]byte, g int, at time.Time) float64 {
	var stop atomic.Bool
	trained := make(chan struct{})
	go func() {
		defer close(trained)
		for i := 0; !stop.Load(); i++ {
			m.Train(lines[i%len(lines)], at)
		}
	}()
	counts := make([]int, g)
	start := time.Now()
	timer := time.AfterFunc(matchFor, func() { stop.Store(true) })
	defer timer.Stop()
	var wg sync.WaitGroup
	for w := 0; w < g; w++ {
		wg.Add(1)
		go func(w int) {
			defer wg.Done()
			n := 0
			for i := w * len(lines) / g; !stop.Load(); i++ {
				m.Match(lines[i%len(lines)])
				n++
			}
			counts[w] = n
		}(w)
	}
	wg.Wait()
	elapsed := time.Since(start).Seconds()
	<-trained
	total := 0
	for _, n := range counts {
		total += n
	}
	return float64(total) / elapsed
}

func median(v []float64) float64 {
	s := slices.Clone(v)
	slices.Sort(s)
	if len(s) == 0 {
		return 0
	}
	if len(s)%2 == 1 {
		return s[len(s)/2]
	}
	return (s[len(s)/2-1] + s[len(s)/2]) / 2
}

// Reads content.txt, truth.txt and templates.tsv of one dataset
func load(dir string) ([][]byte, *truth, error) {
	content, err := os.ReadFile(filepath.Join(dir, "content.txt"))
	if err != nil {
		return nil, nil, err
	}
	lines := bytes.Split(bytes.TrimSuffix(content, []byte("\n")), []byte("\n"))
	tf, err := os.Open(filepath.Join(dir, "truth.txt"))
	if err != nil {
		return nil, nil, err
	}
	defer func() { _ = tf.Close() }()
	tr := &truth{events: make([]int32, 0, len(lines))}
	index := map[string]int32{}
	sc := bufio.NewScanner(tf)
	for sc.Scan() {
		id := sc.Text()
		e, ok := index[id]
		if !ok {
			e = int32(len(index))
			index[id] = e
			tr.sizes = append(tr.sizes, 0)
			tr.templates = append(tr.templates, "")
		}
		tr.events = append(tr.events, e)
		tr.sizes[e]++
	}
	if err := sc.Err(); err != nil {
		return nil, nil, err
	}
	if len(tr.events) != len(lines) {
		return nil, nil, fmt.Errorf("%d truth rows for %d lines", len(tr.events), len(lines))
	}
	tpl, err := os.ReadFile(filepath.Join(dir, "templates.tsv"))
	if err != nil {
		return nil, nil, err
	}
	for _, row := range strings.Split(strings.TrimSpace(string(tpl)), "\n") {
		id, t, _ := strings.Cut(row, "\t")
		if e, ok := index[id]; ok {
			tr.templates[e] = t
		}
	}
	return lines, tr, nil
}

func machine() string {
	out, err := exec.Command("sysctl", "-n", "machdep.cpu.brand_string").Output()
	cpu := strings.TrimSpace(string(out))
	if err != nil || cpu == "" {
		cpu = runtime.GOARCH
	}
	return fmt.Sprintf("%s, %d cores, %s", cpu, runtime.NumCPU(), runtime.GOOS)
}

// The commit measured
// Marked dirty when the working tree differs from it
func commit() string {
	out, err := exec.Command("git", "rev-parse", "--short", "HEAD").Output()
	if err != nil {
		return "uncommitted"
	}
	id := strings.TrimSpace(string(out))
	if status, err := exec.Command("git", "status", "--porcelain").Output(); err != nil || len(status) > 0 {
		id += "-dirty"
	}
	return id
}
