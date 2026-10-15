package main

import (
	"bufio"
	"bytes"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const sample = `connected to 192.0.2.1 in 12ms
connected to 192.0.2.2 in 7ms
disk full on sda
disk full on sdb
connected to 192.0.2.3 in 9ms
`

func noenv(string) string { return "" }

func TestMine(t *testing.T) {
	var out bytes.Buffer
	require.NoError(t, run([]string{"mine"}, strings.NewReader(sample), &out, noenv))
	lines := strings.Split(strings.TrimSpace(out.String()), "\n")
	require.Len(t, lines, 2)
	assert.Regexp(t, `^3\t[0-9a-f]{16}\tconnected to <\*> in <\*>$`, lines[0])
	assert.Regexp(t, `^2\t[0-9a-f]{16}\tdisk full on <\*>$`, lines[1])
}

func TestMineMatchShow(t *testing.T) {
	state := filepath.Join(t.TempDir(), "state")
	file := filepath.Join(t.TempDir(), "in.log")
	require.NoError(t, os.WriteFile(file, []byte(sample), 0o600))
	var out bytes.Buffer
	require.NoError(t, run([]string{"mine", "--state", state, "--json", file}, nil, &out, noenv))
	var rows []struct {
		ID       string `json:"id"`
		Count    uint64 `json:"count"`
		Template string `json:"template"`
	}
	dec := json.NewDecoder(&out)
	for dec.More() {
		var r struct {
			ID       string `json:"id"`
			Count    uint64 `json:"count"`
			Template string `json:"template"`
		}
		require.NoError(t, dec.Decode(&r))
		rows = append(rows, r)
	}
	require.Len(t, rows, 2)

	out.Reset()
	in := "disk full on sdz\nnothing like it at all\n"
	err := run([]string{"match", "--strict"}, strings.NewReader(in), &out, func(k string) string {
		if k == "MOLDRAIN_STATE" {
			return state
		}
		return ""
	})
	assert.ErrorIs(t, err, errUnmatched)
	assert.Equal(t, rows[1].ID+"\tdisk full on sdz\n-\tnothing like it at all\n", out.String())

	out.Reset()
	require.NoError(t, run([]string{"show", "--state", state}, nil, &out, noenv))
	assert.Contains(t, out.String(), "3\t"+rows[0].ID+"\tconnected to <*> in <*>")
}

func TestShowNeedsState(t *testing.T) {
	var out bytes.Buffer
	assert.ErrorContains(t, run([]string{"show"}, nil, &out, noenv), "--state")
	assert.ErrorContains(t, run([]string{"show", "--state", t.TempDir()}, nil, &out, noenv), "no state")
	assert.ErrorContains(t, run([]string{"nope"}, nil, &out, noenv), "unknown command")
}

func TestAssay(t *testing.T) {
	dir := t.TempDir()
	profiles := filepath.Join(dir, "profiles.json")
	require.NoError(t, os.WriteFile(profiles, []byte(`[{"name":"conn","match":"connected to <*> in <*>",
		"value":{"slot":1},"unit":"ms","ops":["count","max"]}]`), 0o600))
	var out bytes.Buffer
	require.NoError(t, run([]string{"assay", "--profiles", profiles, "--state", filepath.Join(dir, "state")},
		strings.NewReader(sample), &out, noenv))
	type rec struct {
		Profile string  `json:"profile"`
		Op      string  `json:"op"`
		Value   float64 `json:"value"`
		Unit    string  `json:"unit"`
		Lines   uint64  `json:"lines"`
	}
	var got []rec
	dec := json.NewDecoder(&out)
	for dec.More() {
		var r rec
		require.NoError(t, dec.Decode(&r))
		got = append(got, r)
	}
	assert.Equal(t, []rec{{"conn", "count", 3, "", 3}, {"conn", "max", 12, "ms", 3}}, got)

	// A state learned under other template options is refused
	err := run([]string{"assay", "--profiles", profiles, "--state", filepath.Join(dir, "state"), "--sim", "0.7"},
		strings.NewReader(""), &out, noenv)
	assert.ErrorContains(t, err, "other options")

	out.Reset()
	require.NoError(t, run([]string{"assay", "--profiles", profiles}, strings.NewReader(""), &out, noenv))
	assert.Empty(t, out.String())
}

// A record reaches stdout once its window closes while the input is still open
func TestAssayStreams(t *testing.T) {
	profiles := filepath.Join(t.TempDir(), "profiles.json")
	require.NoError(t, os.WriteFile(profiles, []byte(`[{"name":"all","match":"*","ops":["count"]}]`), 0o600))
	inR, inW := io.Pipe()
	outR, outW := io.Pipe()
	done := make(chan error, 1)
	go func() {
		done <- run([]string{"assay", "--profiles", profiles, "--window", "1s", "--lateness", "1ms"}, inR, outW, noenv)
		_ = outW.Close()
	}()
	_, err := io.WriteString(inW, "disk full on sda\n")
	require.NoError(t, err)
	line := make(chan string, 1)
	go func() {
		sc := bufio.NewScanner(outR)
		if sc.Scan() {
			line <- sc.Text()
		}
		_, _ = io.Copy(io.Discard, outR)
	}()
	select {
	case l := <-line:
		assert.Contains(t, l, `"profile":"all"`)
	case <-time.After(10 * time.Second):
		t.Error("no record before the input ended")
	}
	require.NoError(t, inW.Close())
	assert.NoError(t, <-done)
}

func TestAssayRefuses(t *testing.T) {
	dir := t.TempDir()
	bad := filepath.Join(dir, "bad.json")
	require.NoError(t, os.WriteFile(bad, []byte(`[{"name":"x"}]`), 0o600))
	good := filepath.Join(dir, "good.json")
	require.NoError(t, os.WriteFile(good, []byte(`[{"name":"x","match":"*","ops":["count"]}]`), 0o600))
	tcs := []struct {
		name string
		args []string
		want string
	}{
		{"no profiles", []string{"assay"}, "--profiles"},
		{"missing file", []string{"assay", "--profiles", filepath.Join(dir, "none.json")}, "no such file"},
		{"invalid profile", []string{"assay", "--profiles", bad}, "invalid profile"},
		{"bad flag", []string{"assay", "--window", "soon"}, "invalid value"},
		{"bad tokenizer", []string{"assay", "--profiles", good, "--tokenizer", "xml"}, "Tokenizer"},
		{"negative window", []string{"assay", "--profiles", good, "--window", "-1m", "--state", dir}, "--window"},
		{"zero window", []string{"assay", "--profiles", good, "--window", "0"}, "--window"},
		{"zero lateness", []string{"assay", "--profiles", good, "--lateness", "0"}, "--lateness"},
		{"state on a file", []string{"assay", "--profiles", good, "--state", good}, "good.json"},
	}
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			var out bytes.Buffer
			assert.ErrorContains(t, run(tc.args, strings.NewReader(""), &out, noenv), tc.want)
		})
	}
}

func TestRun(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "in.log")
	require.NoError(t, os.WriteFile(file, []byte(sample), 0o600))
	other := filepath.Join(dir, "other")
	require.NoError(t, run([]string{"mine", "--state", other, "--sim", "0.7"}, strings.NewReader(sample), &bytes.Buffer{}, noenv))
	long := strings.Repeat("x", 17<<20)
	type want struct {
		out string
		err string
	}
	tcs := []struct {
		name  string
		args  []string
		stdin string
		want  want
	}{
		{"version", []string{"version"}, "", want{out: "dev\n"}},
		{"no command prints the usage", nil, "", want{out: "moldrain mine", err: "help requested"}},
		{"an unknown command prints the usage", []string{"nope"}, "", want{out: "moldrain mine", err: "unknown command"}},
		{"match without strict lets a line miss", []string{"match", "--state", other, "--sim", "0.7"}, "nothing like it\n", want{out: "-\tnothing like it\n"}},
		{"a missing file", []string{"mine", filepath.Join(dir, "none.log")}, "", want{err: "no such file"}},
		{"a state on a file", []string{"mine", "--state", file}, "", want{err: "in.log"}},
		{"a state learned with other options", []string{"mine", "--state", other}, "", want{err: "other options"}},
		{"a line past the scanner's limit", []string{"mine"}, long, want{err: "token too long"}},
		{"json is no flag of assay", []string{"assay", "--json"}, "", want{err: "-json"}},
	}
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			var out bytes.Buffer
			err := run(tc.args, strings.NewReader(tc.stdin), &out, noenv)
			assert.Equal(t, tc.want.err == "", err == nil)
			assert.Contains(t, errText(err), tc.want.err)
			assert.Contains(t, out.String(), tc.want.out)
		})
	}
}

// A write to stdout that fails ends the command with its error
func TestWriteError(t *testing.T) {
	path := filepath.Join(t.TempDir(), "out")
	require.NoError(t, os.WriteFile(path, nil, 0o600))
	readOnly, err := os.Open(path)
	require.NoError(t, err)
	t.Cleanup(func() { _ = readOnly.Close() })
	profiles := filepath.Join(t.TempDir(), "profiles.json")
	require.NoError(t, os.WriteFile(profiles, []byte(`[{"name":"all","match":"*","ops":["count"]}]`), 0o600))
	tcs := []struct {
		name string
		args []string
	}{
		{"mine", []string{"mine"}},
		{"mine as json", []string{"mine", "--json"}},
		{"assay", []string{"assay", "--profiles", profiles}},
	}
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			assert.Error(t, run(tc.args, strings.NewReader(sample), readOnly, noenv))
		})
	}
}

func errText(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}
