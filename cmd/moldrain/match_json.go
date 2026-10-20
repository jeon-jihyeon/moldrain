package main

import (
	"encoding/base64"
	"time"
	"unicode/utf8"

	"github.com/jeon-jihyeon/moldrain"
)

type matchRow struct {
	SchemaVersion  string    `json:"schema_version"`
	Version        string    `json:"version"`
	EmittedAt      time.Time `json:"emitted_at"`
	Matched        bool      `json:"matched"`
	ID             string    `json:"id,omitempty"`
	Alias          string    `json:"alias,omitempty"`
	Template       *string   `json:"template"`
	TemplateBase64 *string   `json:"template_base64,omitempty"`
	Params         []*string `json:"params"`
	ParamsBase64   []string  `json:"params_base64"`
	Line           *string   `json:"line"`
	LineBase64     string    `json:"line_base64"`
	InputBytes     int       `json:"input_bytes"`
	MatchedBytes   int       `json:"matched_bytes"`
	Truncated      bool      `json:"truncated"`
}

func newMatchRow(line []byte, r moldrain.Result, matched bool, limit int) matchRow {
	row := matchRow{
		SchemaVersion: "moldrain.match/v1", Version: version, EmittedAt: time.Now().UTC(), Matched: matched,
		Params: []*string{}, ParamsBase64: []string{},
		Line: jsonText(line), LineBase64: base64.StdEncoding.EncodeToString(line),
		InputBytes: len(line), MatchedBytes: min(len(line), limit), Truncated: len(line) > limit,
	}
	if !matched {
		return row
	}
	row.ID = r.ID.String()
	if r.Alias != 0 {
		row.Alias = r.Alias.String()
	}
	row.Template = jsonText([]byte(r.Template))
	text := base64.StdEncoding.EncodeToString([]byte(r.Template))
	row.TemplateBase64 = &text
	for _, p := range r.Params {
		row.Params = append(row.Params, jsonText(p))
		row.ParamsBase64 = append(row.ParamsBase64, base64.StdEncoding.EncodeToString(p))
	}
	return row
}

func jsonText(b []byte) *string {
	if !utf8.Valid(b) {
		return nil
	}
	text := string(b)
	return &text
}
