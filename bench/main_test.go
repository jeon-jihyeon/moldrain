package main

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jeon-jihyeon/moldrain/resolve/jev"
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
			map[string]string{
				"content.txt": "a 1\nb\na 2\n", "truth.txt": "E1\nE2\nE1\n", "templates.tsv": "E1\ta <*>\nE2\tb\n",
			},
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

// A resolver whose calls fail makes its row an error instead of a score
func TestRunResolverCalls(t *testing.T) {
	type args struct {
		contender string
		url       string
	}
	type want struct {
		failed uint64
		asked  bool
		err    string
	}
	refused := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
	}))
	t.Cleanup(refused.Close)
	tcs := []struct {
		name string
		args args
		want want
	}{
		{"the oracle answers every call", args{"oracle", ""}, want{0, true, ""}},
		{"an endpoint that refuses every call", args{"jev", refused.URL}, want{1, true, "1 resolve calls failed"}},
	}
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			data, out := t.TempDir(), t.TempDir()
			dir := filepath.Join(data, "A")
			require.NoError(t, os.Mkdir(dir, 0o755))
			write(t, dir, map[string]string{
				"content.txt":   "alpha beta gamma delta\nalpha zeta eta theta\n",
				"truth.txt":     "E1\nE1\n",
				"templates.tsv": "E1\talpha <*> <*> <*>\n",
			})
			env := runEnv{runs: 1, jev: jev.Options{URL: tc.args.url}}
			require.NoError(t, run(data, out, []string{"A"}, []string{tc.args.contender}, env))
			var res Run
			require.NoError(t, readJSON(filepath.Join(out, "results.json"), &res))
			require.Len(t, res.Rows, 1)
			r := res.Rows[0]
			assert.Equal(t, tc.want, want{r.ResolverFailed, r.ResolverAsked > 0, r.Error})
		})
	}
}
