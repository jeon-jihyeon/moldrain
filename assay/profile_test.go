package assay_test

import (
	"errors"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/jeon-jihyeon/moldrain/assay"
)

func TestLoadProfiles(t *testing.T) {
	type want struct {
		names []string
		err   string
	}
	tcs := []struct {
		name string
		json string
		want want
	}{
		{
			name: "valid",
			json: `[{"name":"lat","match":"done *","value":{"after":"latency="},"unit":"ms","ops":["p95","count"],
				"group_by":{"slot":1},"max_groups":5},{"name":"ids","ids":["00000000000000ff"],"ops":["count"]}]`,
			want: want{names: []string{"lat", "ids"}},
		},
		{"not JSON", `[{`, want{err: "unexpected EOF"}},
		{"unknown field", `[{"name":"a","match":"x","ops":["count"],"window":"5m"}]`, want{err: "unknown field"}},
		{"empty name", `[{"match":"x","ops":["count"]}]`, want{err: "Name is empty"}},
		{"name twice", `[{"name":"a","match":"x","ops":["count"]},{"name":"a","match":"y","ops":["count"]}]`,
			want{err: "Name is not unique"}},
		{"no match", `[{"name":"a","ops":["count"]}]`, want{err: "Match and IDs are both empty"}},
		{"blank match", `[{"name":"a","match":"  ","ops":["count"]}]`, want{err: "Match and IDs are both empty"}},
		{"match and ids", `[{"name":"a","match":"x","ids":["00000000000000ff"],"ops":["count"]}]`,
			want{err: "Match and IDs are both set"}},
		{"bad id", `[{"name":"a","ids":["xyz"],"ops":["count"]}]`, want{err: "IDs"}},
		{"negative groups", `[{"name":"a","match":"x","ops":["count"],"max_groups":-1}]`, want{err: "MaxGroups"}},
		{"no ops", `[{"name":"a","match":"x"}]`, want{err: "Ops is empty"}},
		{"unknown op", `[{"name":"a","match":"x","ops":["p42"]}]`, want{err: `Ops "p42" is not an op`}},
		{"op twice is kept once", `[{"name":"a","match":"x","ops":["count","count"]}]`, want{names: []string{"a"}}},
		{"op without value", `[{"name":"a","match":"x","ops":["sum"]}]`, want{err: `Ops "sum" needs Value`}},
		{"negative slot", `[{"name":"a","match":"x","value":{"slot":-1},"ops":["sum"]}]`, want{err: "Value Slot -1"}},
		{"slot and after", `[{"name":"a","match":"x","group_by":{"slot":1,"after":"k"},"ops":["count"]}]`,
			want{err: "GroupBy sets both"}},
		{"unknown unit", `[{"name":"a","match":"x","value":{"slot":0},"unit":"kg","ops":["sum"]}]`, want{err: "Unit"}},
	}
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			ps, err := assay.LoadProfiles(strings.NewReader(tc.json))
			assert.Contains(t, errText(err), tc.want.err)
			assert.Equal(t, tc.want.err != "", errors.Is(err, assay.ErrProfile))
			assert.Equal(t, tc.want.names, names(ps))
		})
	}
}

func errText(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}

func names(ps []assay.Profile) []string {
	var out []string
	for _, p := range ps {
		out = append(out, p.Name)
	}
	return out
}
