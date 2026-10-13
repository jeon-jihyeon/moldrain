package main

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func write(t *testing.T, dir string, files map[string]string) {
	t.Helper()
	for name, body := range files {
		require.NoError(t, os.WriteFile(filepath.Join(dir, name), []byte(body), 0o600))
	}
}

func TestLoad(t *testing.T) {
	type want struct {
		lines     []string
		events    []int32
		templates []string
		sizes     []int
		err       bool
	}
	tcs := []struct {
		name  string
		files map[string]string
		want  want
	}{
		{
			"three lines of two events",
			map[string]string{"content.txt": "a 1\nb\na 2\n", "truth.txt": "E1\nE2\nE1\n", "templates.tsv": "E1\ta <*>\nE2\tb\n"},
			want{[]string{"a 1", "b", "a 2"}, []int32{0, 1, 0}, []string{"a <*>", "b"}, []int{2, 1}, false},
		},
		{
			"truth rows that do not cover the lines",
			map[string]string{"content.txt": "a\nb\n", "truth.txt": "E1\n", "templates.tsv": "E1\ta\n"},
			want{err: true},
		},
		{
			"no templates",
			map[string]string{"content.txt": "a\n", "truth.txt": "E1\n"},
			want{err: true},
		},
	}
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			dir := t.TempDir()
			write(t, dir, tc.files)
			lines, tr, err := load(dir)
			got := want{err: err != nil}
			for _, l := range lines {
				got.lines = append(got.lines, string(l))
			}
			if tr != nil {
				got.events, got.templates, got.sizes = tr.events, tr.templates, tr.sizes
			}
			assert.Equal(t, tc.want, got)
		})
	}
}

func TestReadDrain3(t *testing.T) {
	type want struct {
		groups    []uint64
		templates map[uint64]string
		seconds   float64
		err       bool
	}
	stats := `{"lines":3,"seconds":1.5,"max_rss_kib":2048,"python":"3.12","drain3":"0.9.11"}`
	tcs := []struct {
		name  string
		files map[string]string
		want  want
	}{
		{
			"ids and templates",
			map[string]string{"stats.json": stats, "templates.json": `{"1":"a <*>","2":"b"}`, "ids.txt": "1\n2\n1\n"},
			want{[]uint64{1, 2, 1}, map[uint64]string{1: "a <*>", 2: "b"}, 1.5, false},
		},
		{
			"ids that do not cover every line",
			map[string]string{"stats.json": stats, "templates.json": `{}`, "ids.txt": "1\n"},
			want{err: true},
		},
		{
			"an id that is no number",
			map[string]string{"stats.json": stats, "templates.json": `{}`, "ids.txt": "1\nx\n1\n"},
			want{err: true},
		},
		{
			"a template key that is no number",
			map[string]string{"stats.json": stats, "templates.json": `{"x":"a"}`, "ids.txt": "1\n2\n1\n"},
			want{err: true},
		},
	}
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			dir := t.TempDir()
			write(t, dir, tc.files)
			p, err := readDrain3(dir)
			got := want{err: err != nil}
			if err == nil {
				got.groups, got.templates, got.seconds = p.groups, p.templates, p.seconds
			}
			assert.Equal(t, tc.want, got)
		})
	}
}
