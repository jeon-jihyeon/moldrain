// Package jev resolves pending lines with a model behind the Jev wire format
package jev

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"sync"
	"time"

	"github.com/jeon-jihyeon/moldrain/resolve"
)

// Defaults of Options
// A default batch of the loop takes 4 rounds of 8 requests
// That is 40 s at worst and within the loop's default call deadline of 60 s
const (
	DefaultTimeout     = 10 * time.Second
	DefaultConcurrency = 8
	// The yes the chosen candidate needs
	DefaultConfidence = 0.8
)

// Bytes of an answer read at most
// A few questions answer in well under a kilobyte
// A larger body is a wrong endpoint
const bodyLimit = 1 << 20

var (
	// A response outside 2xx
	ErrStatus = errors.New("jev endpoint answered with an error status")
	// A body that is not a Jev answer
	ErrResponse = errors.New("invalid jev response")
)

type Options struct {
	URL   string
	Model string
	// Sent as a bearer token when not empty
	// The caller passes it
	// The module never reads the environment
	Key         string
	Timeout     time.Duration
	Concurrency int
	Confidence  float64
	// nil means a client with Timeout
	Client *http.Client
}

type Resolver struct {
	opt    Options
	client *http.Client
}

var _ resolve.Resolver = (*Resolver)(nil)

func New(opt Options) *Resolver {
	if opt.Timeout <= 0 {
		opt.Timeout = DefaultTimeout
	}
	if opt.Concurrency <= 0 {
		opt.Concurrency = DefaultConcurrency
	}
	if opt.Confidence <= 0 {
		opt.Confidence = DefaultConfidence
	}
	client := opt.Client
	if client == nil {
		client = &http.Client{Timeout: opt.Timeout}
	}
	return &Resolver{opt: opt, client: client}
}

type question struct {
	Type         string `json:"type"`
	Instructions string `json:"instructions"`
}

type request struct {
	Model     string              `json:"model,omitempty"`
	State     string              `json:"state"`
	Questions map[string]question `json:"questions"`
}

type response struct {
	Answers map[string]struct {
		Noul *float64 `json:"noul"`
	} `json:"answers"`
}

// Asks about each line with up to Concurrency requests at once
// A failed request leaves its line without a decision and fails the call
// The loop keeps the other answers and tries that line again
// An answer that picks no single template decides New
func (r *Resolver) Resolve(ctx context.Context, batch []resolve.Pending) ([]resolve.Decision, error) {
	sames := make([]string, len(batch))
	errs := make([]error, len(batch))
	sem := make(chan struct{}, r.opt.Concurrency)
	var wg sync.WaitGroup
	for i := range batch {
		wg.Add(1)
		sem <- struct{}{}
		go func(i int) {
			defer wg.Done()
			defer func() { <-sem }()
			sames[i], errs[i] = r.ask(ctx, batch[i])
		}(i)
	}
	wg.Wait()
	out := make([]resolve.Decision, 0, len(batch))
	for i, same := range sames {
		if errs[i] == nil {
			out = append(out, resolve.Decision{Index: i, Same: same})
		}
	}
	return out, errors.Join(errs...)
}

func key(i int) string { return "same_" + strconv.Itoa(i) }

// The candidate id the model picks for one line
// Empty for New
func (r *Resolver) ask(ctx context.Context, p resolve.Pending) (string, error) {
	qs := make(map[string]question, len(p.Candidates))
	for i, c := range p.Candidates {
		qs[key(i)] = question{
			Type:         "noul",
			Instructions: fmt.Sprintf("Does the line follow the template %s? <*> stands for any value", c.Template),
		}
	}
	yes, err := r.post(ctx, request{Model: r.opt.Model, State: string(p.Line), Questions: qs})
	if err != nil {
		return "", err
	}
	best, above := -1, 0
	for i := range p.Candidates {
		y, ok := yes[key(i)]
		if !ok || y < 0 || y > 1 {
			// a missing or impossible answer is no basis for a merge
			return "", nil
		}
		if y >= 0.5 {
			above++
		}
		if best < 0 || y > yes[key(best)] {
			best = i
		}
	}
	if best < 0 || above != 1 {
		// the line fits no template or more than one
		// A merge would pick at random
		return "", nil
	}
	// the one candidate at 0.5 or more is the best
	// Its yes is its confidence
	if yes[key(best)] < r.opt.Confidence {
		return "", nil
	}
	return p.Candidates[best].ID, nil
}

func (r *Resolver) post(ctx context.Context, body request) (map[string]float64, error) {
	b, err := json.Marshal(body)
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(ctx, r.opt.Timeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, r.opt.URL, bytes.NewReader(b))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	if r.opt.Key != "" {
		req.Header.Set("Authorization", "Bearer "+r.opt.Key)
	}
	res, err := r.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer func() { _ = res.Body.Close() }()
	data, err := io.ReadAll(io.LimitReader(res.Body, bodyLimit))
	if err != nil {
		return nil, err
	}
	if res.StatusCode < 200 || res.StatusCode > 299 {
		return nil, fmt.Errorf("%w: %d %s", ErrStatus, res.StatusCode, data[:min(len(data), 200)])
	}
	var wire response
	if err := json.Unmarshal(data, &wire); err != nil {
		return nil, fmt.Errorf("%w: %w", ErrResponse, err)
	}
	out := make(map[string]float64, len(wire.Answers))
	for k, a := range wire.Answers {
		if a.Noul != nil {
			out[k] = *a.Noul
		}
	}
	return out, nil
}
