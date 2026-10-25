package main

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"
)

type recordMetadata struct {
	SchemaVersion     string                     `json:"schema_version"`
	Source            string                     `json:"source"`
	EventID           string                     `json:"event_id"`
	Collector         string                     `json:"collector"`
	CollectorRevision string                     `json:"collector_revision"`
	EventTime         *time.Time                 `json:"event_time,omitempty"`
	ObservedTime      time.Time                  `json:"observed_time"`
	Attributes        map[string]json.RawMessage `json:"attributes,omitempty"`
	OriginalRef       string                     `json:"original_ref,omitempty"`
}

type inputRecord struct {
	recordMetadata
	Body       *string `json:"body,omitempty"`
	BodyBase64 *string `json:"body_base64,omitempty"`
}

type inputLine struct {
	body   []byte
	at     time.Time
	record *recordMetadata
	basis  string
}

type inputOptions struct {
	records *bool
	replay  *string
}

func inputFlags(c common) inputOptions {
	return inputOptions{records: c.fs.Bool("records", false, "read moldrain.record/v1 JSONL instead of raw lines"),
		replay: c.fs.String("replay-at", "", "trusted fixed RFC3339 clock for historical records")}
}

func (o inputOptions) clock() (func() time.Time, error) {
	if *o.replay == "" {
		return time.Now, nil
	}
	if !*o.records {
		return nil, errors.New("--replay-at requires --records")
	}
	at, err := time.Parse(time.RFC3339Nano, *o.replay)
	if err != nil || !recordTime(at) {
		return nil, errors.New("--replay-at must be RFC3339 within the Unix nanosecond range")
	}
	return func() time.Time { return at }, nil
}

func eachInput(files []string, stdin io.Reader, records bool, fn func(inputLine) error) error {
	n := 0
	return eachLine(files, stdin, func(line []byte) error {
		n++
		if !records {
			return fn(inputLine{body: line, at: time.Now()})
		}
		in, err := decodeRecord(line)
		if err != nil {
			return fmt.Errorf("record %d: %w", n, err)
		}
		return fn(in)
	})
}

func decodeRecord(line []byte) (inputLine, error) {
	var r inputRecord
	d := json.NewDecoder(bytes.NewReader(line))
	d.UseNumber()
	if !utf8.Valid(line) || !uniqueRecordJSON(d, true) {
		return inputLine{}, errors.New("invalid record JSON or duplicate field")
	}
	d = json.NewDecoder(bytes.NewReader(line))
	d.DisallowUnknownFields()
	if d.Decode(&r) != nil || d.Decode(new(any)) != io.EOF {
		return inputLine{}, errors.New("invalid record fields")
	}
	if err := r.validate(); err != nil {
		return inputLine{}, err
	}
	in := inputLine{at: r.ObservedTime, record: &r.recordMetadata, basis: "observed"}
	if r.EventTime != nil {
		in.at, in.basis = *r.EventTime, "event"
	}
	if r.Body != nil {
		in.body = []byte(*r.Body)
		return in, nil
	}
	var err error
	in.body, err = base64.StdEncoding.DecodeString(*r.BodyBase64)
	if err != nil {
		return inputLine{}, errors.New("invalid record body_base64")
	}
	return in, nil
}

func (r inputRecord) validate() error {
	if r.SchemaVersion != "moldrain.record/v1" {
		return errors.New("record schema_version must be moldrain.record/v1")
	}
	for _, field := range []struct {
		name, text string
		max        int
	}{
		{"source", r.Source, 256}, {"event_id", r.EventID, 256},
		{"collector", r.Collector, 128}, {"collector_revision", r.CollectorRevision, 128},
	} {
		if strings.TrimSpace(field.text) == "" || !recordLabel(field.text, field.max) {
			return fmt.Errorf("invalid record %s", field.name)
		}
	}
	if !recordLabel(r.OriginalRef, 4096) {
		return errors.New("invalid record original_ref")
	}
	if !recordTime(r.ObservedTime) || (r.EventTime != nil && !recordTime(*r.EventTime)) {
		return errors.New("record times must be nonzero and within the Unix nanosecond range")
	}
	if (r.Body == nil) == (r.BodyBase64 == nil) {
		return errors.New("record needs exactly one of body and body_base64")
	}
	return nil
}

func recordLabel(text string, limit int) bool {
	return len(text) <= limit && strings.IndexFunc(text, unicode.IsControl) < 0
}

func recordTime(at time.Time) bool {
	return !at.IsZero() && time.Unix(0, at.UnixNano()).Equal(at)
}

// Attribute names stay case sensitive while protocol field names stay canonical
func uniqueRecordJSON(d *json.Decoder, top bool) bool {
	token, err := d.Token()
	if err != nil {
		return false
	}
	opening, compound := token.(json.Delim)
	if !compound {
		return true
	}
	seen := map[string]bool{}
	for d.More() {
		if opening == '{' {
			key, err := d.Token()
			if err != nil {
				return false
			}
			name, ok := key.(string)
			if !ok || seen[name] || (top && name != strings.ToLower(name)) {
				return false
			}
			seen[name] = true
		}
		if !uniqueRecordJSON(d, false) {
			return false
		}
	}
	_, err = d.Token()
	return err == nil
}
