package moldrain

import (
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/jeon-jihyeon/moldrain/internal/token"
	"github.com/jeon-jihyeon/moldrain/internal/tree"
)

// How a line is split into tokens
type Tokenizer string

const (
	Plain  Tokenizer = "plain"  // whitespace and Delimiters as the default
	Punct  Tokenizer = "punct"  // whitespace and most punctuation
	JSON   Tokenizer = "json"   // keys and scalar values of a JSON object
	Logfmt Tokenizer = "logfmt" // key=value pairs
)

// Defaults of Options
const (
	DefaultSimTh        = tree.DefaultSimTh
	DefaultDepth        = tree.DefaultDepth
	DefaultMaxChildren  = tree.DefaultMaxChildren
	DefaultMaxRebuilds  = tree.DefaultMaxRebuilds
	DefaultMaxTokens    = token.DefaultMaxTokens
	DefaultMaxLineBytes = 64 << 10
	DefaultSplitValues  = tree.DefaultSplitValues
	DefaultSplitMin     = tree.DefaultSplitMin
	DefaultMaxSpan      = tree.DefaultMaxSpan
	DefaultPublishEvery = 100 * time.Millisecond
	DefaultPublishLines = 10000
	DefaultBucket       = time.Minute
	DefaultKeepBuckets  = 1440
)

// What a miner learns with
// A zero field takes its default
// Options{} is Drain3 at its defaults with typed scanners
type Options struct {
	// Similarity a line needs to join a template
	SimTh float64
	// Branch positions
	Depth int
	// Children per branch node
	MaxChildren int
	// Rebuilds per token count bucket when a branch position turns unstable
	MaxRebuilds int

	Tokenizer Tokenizer
	// Bytes that split tokens besides whitespace under Plain and Punct
	Delimiters string
	// Tokens per line
	// A longer line keeps its first tokens and one variable for the rest
	MaxTokens int
	// Bytes per line
	// A longer line is cut and counted in the Truncated field of Stats
	MaxLineBytes int
	// Turns the typed scanners off for data whose numbers are part of the event
	Verbatim bool
	// Treats 10 digit and 13 digit numbers as Unix times
	EpochTokens bool
	// Unit suffixes of numbers
	// nil means DefaultUnits and an empty slice means none
	Units []string
	// Scanners of the caller's own that run after the built in ones
	// The options hash knows only how many there are and MaskVersion
	Masks []Mask
	// Change it whenever a mask marks other tokens so configurations with different masks stay distinct
	MaskVersion string

	// Renders <num>, <ip> and so on instead of <*>
	KindPlaceholders bool
	// Recent values kept per variable slot of each cluster
	KeepValues int

	// Splits a variable slot whose values stayed within SplitValues over SplitMin lines
	Respecialize bool
	// Distinct values a slot may take and still split
	SplitValues int
	// Lines a slot needs before it splits
	SplitMin int
	// Lets a line up to MaxSpan tokens longer or shorter than a template match it through one span
	Spans bool
	// Tokens a line may be longer or shorter than the template it joins through a span
	MaxSpan int

	// Match sees new templates within this delay
	PublishEvery time.Duration
	// Lines trained between publishes at most
	PublishLines int
	// Publishes after every Train for callers that need to read their writes
	MatchAfterTrain bool

	// Templates kept
	// 0 sets no cap
	// Over the cap the least recently trained template goes first
	MaxClusters int

	// Width of one count bucket aligned to Unix time
	Bucket time.Duration
	// Count buckets kept
	KeepBuckets int
}

func (o Options) withDefaults() Options {
	if o.SimTh == 0 {
		o.SimTh = DefaultSimTh
	}
	if o.Depth == 0 {
		o.Depth = DefaultDepth
	}
	if o.MaxChildren == 0 {
		o.MaxChildren = DefaultMaxChildren
	}
	if o.MaxRebuilds == 0 {
		o.MaxRebuilds = DefaultMaxRebuilds
	}
	if o.Tokenizer == "" {
		o.Tokenizer = Plain
	}
	if o.MaxTokens == 0 {
		o.MaxTokens = DefaultMaxTokens
	}
	if o.MaxLineBytes == 0 {
		o.MaxLineBytes = DefaultMaxLineBytes
	}
	if o.SplitValues == 0 {
		o.SplitValues = DefaultSplitValues
	}
	if o.SplitMin == 0 {
		o.SplitMin = DefaultSplitMin
	}
	if o.MaxSpan == 0 {
		o.MaxSpan = DefaultMaxSpan
	}
	if o.PublishEvery == 0 {
		o.PublishEvery = DefaultPublishEvery
	}
	if o.PublishLines == 0 {
		o.PublishLines = DefaultPublishLines
	}
	if o.Bucket == 0 {
		o.Bucket = DefaultBucket
	}
	if o.KeepBuckets == 0 {
		o.KeepBuckets = DefaultKeepBuckets
	}
	return o
}

func (o Options) validate() error {
	ints := []struct {
		name string
		v    int
		min  int
	}{
		{"MaxTokens", o.MaxTokens, 1}, {"MaxLineBytes", o.MaxLineBytes, 1},
		{"PublishLines", o.PublishLines, 1}, {"MaxClusters", o.MaxClusters, 0}, {"KeepBuckets", o.KeepBuckets, 1},
	}
	for _, f := range ints {
		if f.v < f.min {
			return fmt.Errorf("%w: %s %d is below %d", ErrOptions, f.name, f.v, f.min)
		}
	}
	switch {
	case o.PublishEvery < 0:
		return fmt.Errorf("%w: PublishEvery %v is negative", ErrOptions, o.PublishEvery)
	case o.Bucket < 0:
		return fmt.Errorf("%w: Bucket %v is negative", ErrOptions, o.Bucket)
	}
	switch o.Tokenizer {
	case Plain, Punct, JSON, Logfmt:
	default:
		return fmt.Errorf("%w: Tokenizer %q is none of plain, punct, json and logfmt", ErrOptions, o.Tokenizer)
	}
	return nil
}

func (o Options) tokenizer() token.Tokenizer {
	cfg := token.Config{
		Delimiters: o.Delimiters,
		MaxTokens:  o.MaxTokens,
		Scan:       token.Scanner{Verbatim: o.Verbatim, EpochTokens: o.EpochTokens, Units: o.Units, Masks: o.Masks},
	}
	switch o.Tokenizer {
	case Punct:
		return token.NewPunct(cfg)
	case JSON:
		return token.NewJSON(cfg)
	case Logfmt:
		return token.NewLogfmt(cfg)
	}
	return token.NewPlain(cfg)
}

func (o Options) tree() tree.Options {
	return tree.Options{
		SimTh: o.SimTh, Depth: o.Depth, MaxChildren: o.MaxChildren, MaxRebuilds: o.MaxRebuilds,
		KeepValues: o.KeepValues, KindPlaceholders: o.KindPlaceholders,
		Respecialize: o.Respecialize, SplitValues: o.SplitValues, SplitMin: o.SplitMin,
		Spans: o.Spans, MaxSpan: o.MaxSpan,
	}
}

// Hash of the options that change templates
// A store refuses state learned under other ones
// Display and publishing are left out with the cap and the counts
// None of them changes a template
func (o Options) Hash() uint64 {
	o = o.withDefaults()
	units := "default"
	if o.Units != nil {
		quoted := make([]string, len(o.Units))
		for i, u := range o.Units {
			quoted[i] = strconv.Quote(u)
		}
		units = strings.Join(quoted, ",")
	}
	fields := []string{
		"sim=" + strconv.FormatFloat(o.SimTh, 'g', -1, 64),
		"depth=" + strconv.Itoa(o.Depth),
		"children=" + strconv.Itoa(o.MaxChildren),
		"rebuilds=" + strconv.Itoa(o.MaxRebuilds),
		"tokenizer=" + string(o.Tokenizer),
		"delimiters=" + strconv.Quote(o.Delimiters),
		"tokens=" + strconv.Itoa(o.MaxTokens),
		"bytes=" + strconv.Itoa(o.MaxLineBytes),
		"verbatim=" + strconv.FormatBool(o.Verbatim),
		"epoch=" + strconv.FormatBool(o.EpochTokens),
		"units=" + units,
		"masks=" + strconv.Itoa(len(o.Masks)),
		"respecialize=" + strconv.FormatBool(o.Respecialize) + "/" + strconv.Itoa(o.SplitValues) + "/" + strconv.Itoa(o.SplitMin),
		"spans=" + strconv.FormatBool(o.Spans) + "/" + strconv.Itoa(o.MaxSpan),
	}
	// an empty MaskVersion adds no field
	// The hash of state stored before the field stays the same
	if o.MaskVersion != "" {
		fields = append(fields, "maskversion="+strconv.Quote(o.MaskVersion))
	}
	h := uint64(14695981039346656037)
	for _, f := range fields {
		for i := 0; i < len(f); i++ {
			h ^= uint64(f[i])
			h *= 1099511628211
		}
		// a separator byte no field contains
		// Two field lists never hash alike by shifting
		h ^= 0xff
		h *= 1099511628211
	}
	return h
}
