package main

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

var recordStart = time.Date(2026, 1, 2, 3, 4, 0, 0, time.UTC)

func emptyEnv(string) string { return "" }

func recordJSON(t *testing.T, at time.Time, edit func(map[string]any)) string {
	t.Helper()
	r := map[string]any{"schema_version": "moldrain.record/v1", "source": "acme", "event_id": "C1",
		"collector": "acme", "collector_revision": "C1", "observed_time": recordStart.Add(24 * time.Hour),
		"event_time": at, "body": "acme request 200", "attributes": map[string]any{"status": 500},
		"original_ref": "https://example.com/C1"}
	if edit != nil {
		edit(r)
	}
	b, err := json.Marshal(r)
	require.NoError(t, err)
	return string(b)
}

func TestRecordMineAndMatch(t *testing.T) {
	t.Parallel()
	type args struct {
		body     string
		encoded  bool
		fallback bool
		number   string
	}
	tcs := []struct {
		name string
		args args
		want string
	}{
		{name: "text body", args: args{body: "acme request 200"}, want: "event"},
		{name: "binary body", args: args{body: "acme " + string([]byte{0xff}), encoded: true}, want: "event"},
		{name: "observation fallback", args: args{body: "acme request 200", fallback: true}, want: "observed"},
		{name: "empty body", want: "event"},
		{name: "large attribute number", args: args{body: "acme request 200", number: "1e400"}, want: "event"},
	}
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			state := filepath.Join(t.TempDir(), "state")
			number := tc.args.number
			if number == "" {
				number = "500"
			}
			line := recordJSON(t, recordStart, func(r map[string]any) {
				r["attributes"] = map[string]any{"status": json.Number(number)}
				r["body"] = tc.args.body
				if tc.args.encoded {
					delete(r, "body")
					r["body_base64"] = base64.StdEncoding.EncodeToString([]byte(tc.args.body))
				}
				if tc.args.fallback {
					delete(r, "event_time")
				}
			})
			var out bytes.Buffer
			args := []string{"mine", "--records", "--replay-at", recordStart.Format(time.RFC3339),
				"--state", state, "--json"}
			require.NoError(t, run(args, strings.NewReader(line+"\n"+line+"\n"), &out, emptyEnv))
			var cluster struct {
				ID    string `json:"id"`
				Count int    `json:"count"`
			}
			require.NoError(t, json.Unmarshal(bytes.TrimSpace(out.Bytes()), &cluster))
			assert.Equal(t, 2, cluster.Count)
			firstID := cluster.ID
			out.Reset()
			require.NoError(t, run(args, strings.NewReader(line+"\n"), &out, emptyEnv))
			require.NoError(t, json.Unmarshal(bytes.TrimSpace(out.Bytes()), &cluster))
			assert.Equal(t, 3, cluster.Count)
			assert.Equal(t, firstID, cluster.ID)
			out.Reset()
			require.NoError(t, run([]string{"match", "--records", "--state", state, "--json"},
				strings.NewReader(line+"\n"), &out, emptyEnv))
			var row map[string]any
			d := json.NewDecoder(&out)
			d.UseNumber()
			require.NoError(t, d.Decode(&row))
			assert.Equal(t, base64.StdEncoding.EncodeToString([]byte(tc.args.body)), row["line_base64"])
			assert.Equal(t, tc.want, row["time_basis"])
			metadata, ok := row["record"].(map[string]any)
			require.True(t, ok)
			assert.Equal(t, "acme", metadata["source"])
			assert.Equal(t, "C1", metadata["event_id"])
			assert.Equal(t, "https://example.com/C1", metadata["original_ref"])
			assert.Equal(t, map[string]any{"status": json.Number(number)}, metadata["attributes"])
		})
	}
}

func TestRecordRefusals(t *testing.T) {
	t.Parallel()
	tcs := []struct {
		name string
		edit func(map[string]any)
	}{
		{name: "wrong schema", edit: func(r map[string]any) { r["schema_version"] = "other" }},
		{name: "unknown field", edit: func(r map[string]any) { r["extra"] = true }},
		{name: "missing source", edit: func(r map[string]any) { delete(r, "source") }},
		{name: "missing event id", edit: func(r map[string]any) { delete(r, "event_id") }},
		{name: "missing collector", edit: func(r map[string]any) { delete(r, "collector") }},
		{name: "missing revision", edit: func(r map[string]any) { delete(r, "collector_revision") }},
		{name: "missing observed time", edit: func(r map[string]any) { delete(r, "observed_time") }},
		{name: "control character", edit: func(r map[string]any) { r["source"] = "acme\n" }},
		{name: "ambiguous body", edit: func(r map[string]any) { r["body_base64"] = "YQ==" }},
		{name: "missing body", edit: func(r map[string]any) { delete(r, "body") }},
		{name: "invalid base64", edit: func(r map[string]any) {
			delete(r, "body")
			r["body_base64"] = "!"
		}},
		{name: "invalid reference", edit: func(r map[string]any) { r["original_ref"] = "C1\n" }},
		{name: "time outside nanosecond range", edit: func(r map[string]any) { r["event_time"] = "9999-01-01T00:00:00Z" }},
	}
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			var out bytes.Buffer
			err := run([]string{"mine", "--records", "--json"},
				strings.NewReader(recordJSON(t, recordStart, tc.edit)), &out, emptyEnv)
			assert.Error(t, err)
			assert.Empty(t, out.String())
		})
	}
}

func TestRecordLabelBoundaries(t *testing.T) {
	t.Parallel()
	type args struct {
		field string
		value string
	}
	tcs := []struct {
		name string
		args args
	}{
		{name: "source ASCII", args: args{field: "source", value: strings.Repeat("a", 256)}},
		{name: "source UTF8", args: args{field: "source", value: strings.Repeat("é", 128)}},
		{name: "event id ASCII", args: args{field: "event_id", value: strings.Repeat("a", 256)}},
		{name: "event id UTF8", args: args{field: "event_id", value: strings.Repeat("é", 128)}},
		{name: "collector ASCII", args: args{field: "collector", value: strings.Repeat("a", 128)}},
		{name: "collector UTF8", args: args{field: "collector", value: strings.Repeat("é", 64)}},
		{name: "revision ASCII", args: args{field: "collector_revision", value: strings.Repeat("a", 128)}},
		{name: "revision UTF8", args: args{field: "collector_revision", value: strings.Repeat("é", 64)}},
		{name: "reference ASCII", args: args{field: "original_ref", value: strings.Repeat("a", 4096)}},
		{name: "reference UTF8", args: args{field: "original_ref", value: strings.Repeat("é", 2048)}},
	}
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			line := recordJSON(t, recordStart, func(r map[string]any) { r[tc.args.field] = tc.args.value })
			var out bytes.Buffer
			err := run([]string{"mine", "--records", "--json"}, strings.NewReader(line), &out, emptyEnv)
			assert.NoError(t, err)
			var row struct {
				Count uint64 `json:"count"`
			}
			require.NoError(t, json.Unmarshal(out.Bytes(), &row))
			assert.Equal(t, uint64(1), row.Count)
		})
	}
}

func TestRecordLabelBoundaryRefusals(t *testing.T) {
	t.Parallel()
	type args struct {
		field string
		value string
	}
	tcs := []struct {
		name string
		args args
		want string
	}{
		{name: "source ASCII", args: args{field: "source", value: strings.Repeat("a", 257)},
			want: "invalid record source"},
		{name: "source UTF8", args: args{field: "source", value: strings.Repeat("é", 128) + "a"},
			want: "invalid record source"},
		{name: "event id ASCII", args: args{field: "event_id", value: strings.Repeat("a", 257)},
			want: "invalid record event_id"},
		{name: "event id UTF8", args: args{field: "event_id", value: strings.Repeat("é", 128) + "a"},
			want: "invalid record event_id"},
		{name: "collector ASCII", args: args{field: "collector", value: strings.Repeat("a", 129)},
			want: "invalid record collector"},
		{name: "collector UTF8", args: args{field: "collector", value: strings.Repeat("é", 64) + "a"},
			want: "invalid record collector"},
		{name: "revision ASCII", args: args{field: "collector_revision", value: strings.Repeat("a", 129)},
			want: "invalid record collector_revision"},
		{name: "revision UTF8", args: args{field: "collector_revision", value: strings.Repeat("é", 64) + "a"},
			want: "invalid record collector_revision"},
		{name: "reference ASCII", args: args{field: "original_ref", value: strings.Repeat("a", 4097)},
			want: "invalid record original_ref"},
		{name: "reference UTF8", args: args{field: "original_ref", value: strings.Repeat("é", 2048) + "a"},
			want: "invalid record original_ref"},
	}
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			line := recordJSON(t, recordStart, func(r map[string]any) { r[tc.args.field] = tc.args.value })
			var out bytes.Buffer
			err := run([]string{"mine", "--records", "--json"}, strings.NewReader(line), &out, emptyEnv)
			assert.ErrorContains(t, err, tc.want)
			assert.Empty(t, out.String())
		})
	}
}

func TestRecordFlagRefusals(t *testing.T) {
	t.Parallel()
	tcs := []struct {
		name string
		args []string
		want string
	}{
		{name: "replay requires record input", args: []string{"mine", "--replay-at", "2026-01-02T00:00:00Z"},
			want: "requires --records"},
		{name: "bad replay time", args: []string{"mine", "--records", "--replay-at", "later"}, want: "RFC3339"},
		{name: "replay outside count range", args: []string{"mine", "--records", "--replay-at", "9999-01-01T00:00:00Z"},
			want: "Unix nanosecond"},
		{name: "matching preserves provenance", args: []string{"match", "--records"}, want: "requires --json"},
	}
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			var out bytes.Buffer
			err := run(tc.args, strings.NewReader(""), &out, emptyEnv)
			require.Error(t, err)
			assert.Contains(t, err.Error(), tc.want)
		})
	}
}

func TestRecordReplayCallerRefusals(t *testing.T) {
	t.Parallel()
	type args struct {
		command string
		replay  string
		records bool
	}
	tcs := []struct {
		name string
		args args
		want string
	}{
		{name: "match malformed replay", args: args{command: "match", replay: "later", records: true},
			want: "--replay-at must be RFC3339 within the Unix nanosecond range"},
		{name: "match replay outside count range",
			args: args{command: "match", replay: "9999-01-01T00:00:00Z", records: true},
			want: "--replay-at must be RFC3339 within the Unix nanosecond range"},
		{name: "match replay without records", args: args{command: "match", replay: "2026-01-02T03:04:00Z"},
			want: "--replay-at requires --records"},
		{name: "assay malformed replay", args: args{command: "assay", replay: "later", records: true},
			want: "--replay-at must be RFC3339 within the Unix nanosecond range"},
		{name: "assay replay outside count range",
			args: args{command: "assay", replay: "9999-01-01T00:00:00Z", records: true},
			want: "--replay-at must be RFC3339 within the Unix nanosecond range"},
		{name: "assay replay without records", args: args{command: "assay", replay: "2026-01-02T03:04:00Z"},
			want: "--replay-at requires --records"},
	}
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			state := filepath.Join(t.TempDir(), "state")
			var initial bytes.Buffer
			require.NoError(t, run([]string{"mine", "--state", state, "--json"},
				strings.NewReader("acme request 200"), &initial, emptyEnv))
			profiles := filepath.Join(t.TempDir(), "profiles.json")
			profile := `[{"name":"requests","match":"*","ops":["count"]}]`
			require.NoError(t, os.WriteFile(profiles, []byte(profile), 0o600))
			args := []string{tc.args.command, "--state", state, "--replay-at", tc.args.replay}
			input := "acme request 200"
			if tc.args.records {
				args = append(args, "--records")
				input = recordJSON(t, recordStart, nil)
			}
			if tc.args.command == "assay" {
				args = append(args, "--profiles", profiles)
			} else {
				args = append(args, "--json")
			}
			var out bytes.Buffer
			err := run(args, strings.NewReader(input), &out, emptyEnv)
			assert.ErrorContains(t, err, tc.want)
			assert.Empty(t, out.String())
			var persisted bytes.Buffer
			require.NoError(t, run([]string{"show", "--state", state, "--json"},
				strings.NewReader(""), &persisted, emptyEnv))
			assert.Equal(t, initial.String(), persisted.String())
		})
	}
}

func TestRecordAssayEventWindows(t *testing.T) {
	t.Parallel()
	type args struct {
		offsets []time.Duration
		clock   time.Duration
	}
	tcs := []struct {
		name string
		args args
		want []float64
	}{
		{name: "future and out of order EOF",
			args: args{clock: 3 * time.Minute,
				offsets: []time.Duration{0, 48 * time.Hour, 10 * time.Second, 2 * time.Minute, 90 * time.Second}},
			want: []float64{2, 1, 1}},
		{name: "same observations without future row",
			args: args{clock: 3 * time.Minute,
				offsets: []time.Duration{0, 10 * time.Second, 2 * time.Minute, 90 * time.Second}},
			want: []float64{2, 1, 1}},
		{name: "closed event window stays late",
			args: args{clock: 3 * time.Minute,
				offsets: []time.Duration{0, 2 * time.Minute, 10 * time.Second, 90 * time.Second}},
			want: []float64{1, 1, 1}},
		{name: "historical events span fifty minutes",
			args: args{clock: 51 * time.Minute, offsets: []time.Duration{
				0, 10 * time.Minute, 20 * time.Minute, 30 * time.Minute, 40 * time.Minute, 50 * time.Minute,
			}},
			want: []float64{1, 1, 1, 1, 1, 1}},
	}
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			profiles := filepath.Join(t.TempDir(), "profiles.json")
			write := `[{"name":"requests","match":"*","ops":["count"]}]`
			require.NoError(t, os.WriteFile(profiles, []byte(write), 0o600))
			var input strings.Builder
			for _, offset := range tc.args.offsets {
				input.WriteString(recordJSON(t, recordStart.Add(offset), nil) + "\n")
			}
			var out bytes.Buffer
			args := []string{"assay", "--records", "--replay-at", recordStart.Add(tc.args.clock).Format(time.RFC3339),
				"--profiles", profiles, "--window", "1m", "--lateness", "30s"}
			require.NoError(t, run(args, strings.NewReader(input.String()), &out, emptyEnv))
			var counts []float64
			d := json.NewDecoder(&out)
			for d.More() {
				var row struct {
					Value float64 `json:"value"`
				}
				require.NoError(t, d.Decode(&row))
				counts = append(counts, row.Value)
			}
			assert.Equal(t, tc.want, counts)
		})
	}
}

func TestAssayDurationOverflow(t *testing.T) {
	t.Parallel()
	tcs := []struct {
		name   string
		record bool
	}{
		{name: "live lines"},
		{name: "historical records", record: true},
	}
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			profiles := filepath.Join(t.TempDir(), "profiles.json")
			profile := `[{"name":"requests","match":"*","ops":["count"]}]`
			require.NoError(t, os.WriteFile(profiles, []byte(profile), 0o600))
			args := []string{"assay", "--profiles", profiles, "--window", "2000000h", "--lateness", "2000000h"}
			input := "acme\n"
			if tc.record {
				args = append(args, "--records", "--replay-at", recordStart.Format(time.RFC3339))
				input = recordJSON(t, recordStart, nil) + "\n"
			}
			var out bytes.Buffer
			err := run(args, strings.NewReader(input), &out, emptyEnv)
			require.Error(t, err)
			assert.Contains(t, err.Error(), "duration range")
			assert.Empty(t, out.String())
		})
	}
}

func TestRecordAssayTimeBoundary(t *testing.T) {
	t.Parallel()
	type args struct {
		stamp       string
		observation string
	}
	tcs := []struct {
		name string
		args args
	}{
		{name: "safe lower window", args: args{stamp: "1677-09-21T00:14:43.145224192Z"}},
		{name: "safe upper window", args: args{stamp: "2262-04-11T23:45:16.854775807Z"}},
		{name: "unselected lower observation remains valid", args: args{stamp: "2026-01-02T03:04:00Z",
			observation: "1677-09-21T00:12:43.145224192Z"}},
		{name: "unselected upper observation remains valid", args: args{stamp: "2026-01-02T03:04:00Z",
			observation: "2262-04-11T23:47:16.854775807Z"}},
	}
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			profiles := filepath.Join(t.TempDir(), "profiles.json")
			profile := `[{"name":"requests","match":"*","ops":["count"]}]`
			require.NoError(t, os.WriteFile(profiles, []byte(profile), 0o600))
			at, err := time.Parse(time.RFC3339Nano, tc.args.stamp)
			require.NoError(t, err)
			var out bytes.Buffer
			state := filepath.Join(t.TempDir(), "state")
			args := []string{"assay", "--records", "--state", state,
				"--profiles", profiles, "--window", "1m", "--lateness", "30s", "--replay-at", tc.args.stamp}
			input := recordJSON(t, at, func(r map[string]any) {
				if tc.args.observation != "" {
					r["observed_time"] = tc.args.observation
				}
			})
			err = run(args, strings.NewReader(input), &out, emptyEnv)
			require.NoError(t, err)
			var row struct {
				Start, End time.Time
				Value      float64
			}
			require.NoError(t, json.Unmarshal(out.Bytes(), &row))
			assert.Equal(t, float64(1), row.Value)
			assert.Equal(t, at.Year(), row.Start.Year())
			assert.True(t, !at.Before(row.Start) && at.Before(row.End))
		})
	}
}

func TestRecordAssayTimeBoundaryRefusals(t *testing.T) {
	t.Parallel()
	type args struct {
		stamp string
		clock string
	}
	tcs := []struct {
		name string
		args args
	}{
		{name: "lower edge", args: args{stamp: "1677-09-21T00:12:43.145224192Z"}},
		{name: "upper edge", args: args{stamp: "2262-04-11T23:47:16.854775807Z"}},
		{name: "unsafe lower clock with normal event", args: args{stamp: "2026-01-02T03:04:00Z",
			clock: "1677-09-21T00:12:43.145224192Z"}},
		{name: "unsafe upper clock with normal event", args: args{stamp: "2026-01-02T03:04:00Z",
			clock: "2262-04-11T23:47:16.854775807Z"}},
	}
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			profiles := filepath.Join(t.TempDir(), "profiles.json")
			profile := `[{"name":"requests","match":"*","ops":["count"]}]`
			require.NoError(t, os.WriteFile(profiles, []byte(profile), 0o600))
			at, err := time.Parse(time.RFC3339Nano, tc.args.stamp)
			require.NoError(t, err)
			clock := tc.args.clock
			if clock == "" {
				clock = tc.args.stamp
			}
			state := filepath.Join(t.TempDir(), "state")
			args := []string{"assay", "--records", "--state", state,
				"--profiles", profiles, "--window", "1m", "--lateness", "30s", "--replay-at", clock}
			var out bytes.Buffer
			err = run(args, strings.NewReader(recordJSON(t, at, nil)), &out, emptyEnv)
			assert.Error(t, err)
			assert.ErrorContains(t, err, "safe assay range")
			assert.Empty(t, out.String())
		})
	}
}

func TestRecordAssayTimeBoundaryPrefix(t *testing.T) {
	t.Parallel()
	type args struct {
		stamp string
		live  bool
	}
	tcs := []struct {
		name string
		args args
	}{
		{name: "unsafe lower replay record preserves prefix", args: args{stamp: "1677-09-21T00:12:43.145224192Z"}},
		{name: "unsafe upper replay record preserves prefix", args: args{stamp: "2262-04-11T23:47:16.854775807Z"}},
		{name: "unsafe lower live record preserves prefix", args: args{stamp: "1677-09-21T00:12:43.145224192Z", live: true}},
		{name: "unsafe upper live record preserves prefix", args: args{stamp: "2262-04-11T23:47:16.854775807Z", live: true}},
	}
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			profiles := filepath.Join(t.TempDir(), "profiles.json")
			profile := `[{"name":"requests","match":"*","ops":["count"]}]`
			require.NoError(t, os.WriteFile(profiles, []byte(profile), 0o600))
			at, err := time.Parse(time.RFC3339Nano, tc.args.stamp)
			require.NoError(t, err)
			state := filepath.Join(t.TempDir(), "state")
			args := []string{"assay", "--records", "--state", state,
				"--profiles", profiles, "--window", "1m", "--lateness", "30s"}
			if !tc.args.live {
				args = append(args, "--replay-at", recordStart.Format(time.RFC3339))
			}
			input := recordJSON(t, recordStart, nil) + "\n" + recordJSON(t, at, nil)
			var out bytes.Buffer
			err = run(args, strings.NewReader(input), &out, emptyEnv)
			assert.Error(t, err)
			assert.ErrorContains(t, err, "safe assay range")
			var persisted bytes.Buffer
			require.NoError(t, run([]string{"show", "--state", state, "--json"},
				strings.NewReader(""), &persisted, emptyEnv))
			assert.Contains(t, persisted.String(), `"count":1`)
			var row struct {
				Start, End time.Time
				Value      float64
			}
			require.NoError(t, json.Unmarshal(out.Bytes(), &row))
			assert.Equal(t, float64(1), row.Value)
			assert.Equal(t, recordStart.Year(), row.Start.Year())
			assert.True(t, !recordStart.Before(row.Start) && recordStart.Before(row.End))
		})
	}
}

func TestRecordJSONIntegrity(t *testing.T) {
	t.Parallel()
	tcs := []struct {
		name string
		edit func(string) string
	}{
		{name: "duplicate key", edit: func(s string) string {
			return strings.Replace(s, `"source":"acme"`, `"source":"other","source":"acme"`, 1)
		}},
		{name: "case alias", edit: func(s string) string {
			return strings.Replace(s, `"source":"acme"`, `"Source":"acme"`, 1)
		}},
		{name: "invalid UTF8", edit: func(s string) string { return s + string([]byte{0xff}) }},
		{name: "trailing object", edit: func(s string) string { return s + "{}" }},
	}
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			var out bytes.Buffer
			err := run([]string{"mine", "--records", "--json"},
				strings.NewReader(tc.edit(recordJSON(t, recordStart, nil))), &out, emptyEnv)
			assert.Error(t, err)
			assert.Empty(t, out.String())
		})
	}
}
