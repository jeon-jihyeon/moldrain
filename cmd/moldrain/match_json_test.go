package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jeon-jihyeon/moldrain"
	"github.com/jeon-jihyeon/moldrain/persist"
)

func matchState(t *testing.T) (string, string, string) {
	t.Helper()
	dir := t.TempDir()
	m, err := moldrain.New(moldrain.Options{})
	require.NoError(t, err)
	store, err := persist.NewFileStore(dir)
	require.NoError(t, err)
	p, err := persist.Attach(context.Background(), m, store, persist.Options{})
	require.NoError(t, err)
	for _, line := range []string{"disk full on sda", "disk full on sdb"} {
		m.Train([]byte(line), time.Now())
	}
	m.Publish()
	canonical, ok := m.Match([]byte("disk full on sda"))
	require.True(t, ok)
	m.Train([]byte("no space left on device sdc"), time.Now())
	alias := m.Train([]byte("no space left on device sdd"), time.Now())
	require.NoError(t, m.Alias(alias.ID, canonical.ID))
	m.Train([]byte("request 1 done"), time.Now())
	m.Train([]byte("bad \xff"), time.Now())
	require.NoError(t, p.Close(context.Background()))
	return dir, canonical.ID.String(), alias.ID.String()
}

func TestMatchJSON(t *testing.T) {
	t.Parallel()
	state, id, alias := matchState(t)
	id = "^" + id + "$"
	type args struct {
		line   string
		strict bool
	}
	type want struct {
		matched       bool
		id            string
		alias         string
		template      any
		templateBytes string
		params        []any
		paramBytes    []string
		line          any
		truncated     bool
		err           error
	}
	longLine := "disk full on " + strings.Repeat("x", 70<<10)
	tcs := []struct {
		name string
		args args
		want want
	}{
		{name: "hit", args: args{line: "disk full on sdz"},
			want: want{matched: true, id: id, line: "disk full on sdz", template: "disk full on <*>",
				templateBytes: "disk full on <*>", params: []any{"sdz"}, paramBytes: []string{"sdz"}}},
		{name: "miss", args: args{line: "nothing like it at all"},
			want: want{id: "^$", line: "nothing like it at all", params: []any{}, paramBytes: []string{}}},
		{name: "strict miss", args: args{line: "nothing like it at all", strict: true},
			want: want{id: "^$", line: "nothing like it at all", params: []any{}, paramBytes: []string{}, err: errUnmatched}},
		{name: "alias slots", args: args{line: "no space left on device sde"},
			want: want{matched: true, id: id, alias: alias, line: "no space left on device sde", template: "disk full on <*>",
				templateBytes: "disk full on <*>", params: []any{"sde"}, paramBytes: []string{"sde"}}},
		{name: "unicode parameter", args: args{line: "disk full on 文"},
			want: want{matched: true, id: id, line: "disk full on 文", template: "disk full on <*>",
				templateBytes: "disk full on <*>", params: []any{"文"}, paramBytes: []string{"文"}}},
		{name: "invalid parameter bytes", args: args{line: "disk full on \xff"},
			want: want{matched: true, id: id, template: "disk full on <*>", templateBytes: "disk full on <*>",
				params: []any{nil}, paramBytes: []string{"\xff"}}},
		{name: "large integer text", args: args{line: "request 9007199254740993 done"},
			want: want{matched: true, id: "^[0-9a-f]{16}$", line: "request 9007199254740993 done", template: "request <*> done",
				templateBytes: "request <*> done", params: []any{"9007199254740993"}, paramBytes: []string{"9007199254740993"}}},
		{name: "invalid template bytes", args: args{line: "bad \xff"},
			want: want{matched: true, id: "^[0-9a-f]{16}$", templateBytes: "bad \xff", params: []any{}, paramBytes: []string{}}},
		{name: "empty line", args: args{}, want: want{id: "^$", line: "", params: []any{}, paramBytes: []string{}}},
		{name: "embedded zero byte", args: args{line: "disk full on a\x00b"},
			want: want{matched: true, id: id, line: "disk full on a\x00b", template: "disk full on <*>",
				templateBytes: "disk full on <*>", params: []any{"a\x00b"}, paramBytes: []string{"a\x00b"}}},
		{name: "matching cuts only its prefix", args: args{line: longLine},
			want: want{matched: true, id: id, line: longLine, template: "disk full on <*>", templateBytes: "disk full on <*>",
				params:     []any{strings.Repeat("x", (64<<10)-len("disk full on "))},
				paramBytes: []string{strings.Repeat("x", (64<<10)-len("disk full on "))}, truncated: true}},
	}
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			argv := []string{"match", "--state", state, "--json", "--strict=" + strconv.FormatBool(tc.args.strict)}
			var out bytes.Buffer
			before := time.Now()
			err := run(argv, strings.NewReader(tc.args.line+"\n"), &out, noenv)
			assert.ErrorIs(t, err, tc.want.err)
			sc := bufio.NewScanner(&out)
			sc.Buffer(make([]byte, 4096), 1<<20)
			require.True(t, sc.Scan())
			var row map[string]any
			require.NoError(t, json.Unmarshal(sc.Bytes(), &row))
			assert.Equal(t, "moldrain.match/v1", row["schema_version"])
			assert.Equal(t, "dev", row["version"])
			stamp, ok := row["emitted_at"].(string)
			require.True(t, ok)
			at, err := time.Parse(time.RFC3339Nano, stamp)
			require.NoError(t, err)
			assert.False(t, at.Before(before))
			assert.False(t, at.After(time.Now()))
			assert.True(t, strings.HasSuffix(stamp, "Z"))
			assert.Equal(t, tc.want.matched, row["matched"])
			gotID, _ := row["id"].(string)
			assert.Regexp(t, tc.want.id, gotID)
			gotAlias, _ := row["alias"].(string)
			assert.Equal(t, tc.want.alias, gotAlias)
			assert.Equal(t, tc.want.line, row["line"])
			assert.Equal(t, tc.want.template, row["template"])
			line64, ok := row["line_base64"].(string)
			require.True(t, ok)
			raw, err := base64.StdEncoding.DecodeString(line64)
			require.NoError(t, err)
			assert.Equal(t, tc.args.line, string(raw))
			template64, _ := row["template_base64"].(string)
			template, err := base64.StdEncoding.DecodeString(template64)
			require.NoError(t, err)
			assert.Equal(t, tc.want.templateBytes, string(template))
			assert.Equal(t, float64(len(tc.args.line)), row["input_bytes"])
			assert.Equal(t, float64(min(len(tc.args.line), 64<<10)), row["matched_bytes"])
			assert.Equal(t, tc.want.truncated, row["truncated"])
			assert.Equal(t, tc.want.params, row["params"])
			var encoded struct {
				Params []string `json:"params_base64"`
			}
			require.NoError(t, json.Unmarshal(sc.Bytes(), &encoded))
			params := []string{}
			for _, text := range encoded.Params {
				p, err := base64.StdEncoding.DecodeString(text)
				require.NoError(t, err)
				params = append(params, string(p))
			}
			assert.Equal(t, tc.want.paramBytes, params)
			assert.False(t, sc.Scan())
			assert.NoError(t, sc.Err())
		})
	}
}

func TestMatchJSONPartialInput(t *testing.T) {
	t.Parallel()
	state, _, _ := matchState(t)
	var out bytes.Buffer
	err := run([]string{"match", "--state", state, "--json"},
		strings.NewReader("disk full on sdz\n"+strings.Repeat("x", 17<<20)), &out, noenv)
	assert.ErrorContains(t, err, "token too long")
	var row matchRow
	require.NoError(t, json.NewDecoder(&out).Decode(&row))
	assert.True(t, row.Matched)
	require.NotNil(t, row.Line)
	assert.Equal(t, "disk full on sdz", *row.Line)
}

func TestMatchJSONStreams(t *testing.T) {
	t.Parallel()
	state, _, _ := matchState(t)
	inR, inW := io.Pipe()
	outR, outW := io.Pipe()
	t.Cleanup(func() { _ = inR.Close(); _ = inW.Close(); _ = outR.Close(); _ = outW.Close() })
	done := make(chan error, 1)
	go func() {
		done <- run([]string{"match", "--state", state, "--json"}, inR, outW, noenv)
		_ = outW.Close()
	}()
	_, err := io.WriteString(inW, "disk full on sdz\n")
	require.NoError(t, err)
	read := make(chan error, 1)
	go func() {
		var row matchRow
		read <- json.NewDecoder(outR).Decode(&row)
	}()
	select {
	case err := <-read:
		require.NoError(t, err)
	case <-time.After(5 * time.Second):
		t.Fatal("JSON row buffered until input closed")
	}
	require.NoError(t, inW.Close())
	require.NoError(t, <-done)
}

func TestMatchJSONWriteError(t *testing.T) {
	t.Parallel()
	state, _, _ := matchState(t)
	path := filepath.Join(t.TempDir(), "readonly")
	require.NoError(t, os.WriteFile(path, nil, 0o600))
	out, err := os.Open(path)
	require.NoError(t, err)
	defer func() { _ = out.Close() }()
	assert.Error(t, run([]string{"match", "--state", state, "--json"}, strings.NewReader(sample), out, noenv))
}
