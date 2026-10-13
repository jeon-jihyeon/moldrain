package jev_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jeon-jihyeon/moldrain/resolve"
	"github.com/jeon-jihyeon/moldrain/resolve/jev"
)

type wireReq struct {
	Model     string `json:"model"`
	State     string `json:"state"`
	Questions map[string]struct {
		Type         string `json:"type"`
		Instructions string `json:"instructions"`
	} `json:"questions"`
}

func pending(line string, ids ...string) resolve.Pending {
	p := resolve.Pending{Line: []byte(line), Template: line}
	for _, id := range ids {
		p.Candidates = append(p.Candidates, resolve.Candidate{ID: id, Template: "tpl " + id + " <*>"})
	}
	return p
}

func TestResolve(t *testing.T) {
	tcs := []struct {
		name   string
		answer string
		want   string
	}{
		{"confident yes for one candidate", `{"answers":{"same_0":{"noul":0.04},"same_1":{"noul":0.93}}}`, "b"},
		{"yes below confidence is new", `{"answers":{"same_0":{"noul":0.7},"same_1":{"noul":0.1}}}`, ""},
		{"no for every candidate is new", `{"answers":{"same_0":{"noul":0.1},"same_1":{"noul":0.02}}}`, ""},
		{"two candidates above 0.5 is new", `{"answers":{"same_0":{"noul":0.9},"same_1":{"noul":0.95}}}`, ""},
		{"a missing answer is new", `{"answers":{"same_1":{"noul":0.99}}}`, ""},
		{"a probability above 1 is new", `{"answers":{"same_0":{"noul":1.5},"same_1":{"noul":0.01}}}`, ""},
	}
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			var got wireReq
			var auth string
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				auth = r.Header.Get("Authorization")
				_ = json.NewDecoder(r.Body).Decode(&got)
				_, _ = w.Write([]byte(tc.answer))
			}))
			t.Cleanup(srv.Close)
			r := jev.New(jev.Options{URL: srv.URL, Model: "m1", Key: "k"})
			ds, err := r.Resolve(context.Background(), []resolve.Pending{pending("disk is full on sdb", "a", "b")})
			require.NoError(t, err)
			assert.Equal(t, []resolve.Decision{{Index: 0, Same: tc.want}}, ds)
			assert.Equal(t, "Bearer k", auth)
			assert.Equal(t, "m1", got.Model)
			assert.Equal(t, "disk is full on sdb", got.State)
			require.Len(t, got.Questions, 2)
			assert.Equal(t, "noul", got.Questions["same_1"].Type)
			assert.Equal(t, "Does the line follow the template tpl b <*>? <*> stands for any value", got.Questions["same_1"].Instructions)
		})
	}
}

func TestResolveErrors(t *testing.T) {
	tcs := []struct {
		name   string
		status int
		body   string
		want   error
	}{
		{"error status fails the batch", http.StatusBadGateway, "down", jev.ErrStatus},
		{"a body that is not json fails the batch", http.StatusOK, "<html>", jev.ErrResponse},
	}
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(tc.status)
				_, _ = w.Write([]byte(tc.body))
			}))
			t.Cleanup(srv.Close)
			_, err := jev.New(jev.Options{URL: srv.URL}).Resolve(context.Background(), []resolve.Pending{pending("x", "a")})
			assert.ErrorIs(t, err, tc.want)
		})
	}
}

// A failed request costs only its own line
func TestResolveKeepsAnswers(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req wireReq
		_ = json.NewDecoder(r.Body).Decode(&req)
		if req.State == "bad" {
			w.WriteHeader(http.StatusBadGateway)
			return
		}
		_, _ = w.Write([]byte(`{"answers":{"same_0":{"noul":0.9}}}`))
	}))
	t.Cleanup(srv.Close)
	batch := []resolve.Pending{pending("l1", "a"), pending("bad", "a"), pending("l3", "a")}
	ds, err := jev.New(jev.Options{URL: srv.URL}).Resolve(context.Background(), batch)
	assert.ErrorIs(t, err, jev.ErrStatus)
	assert.Equal(t, []resolve.Decision{{Index: 0, Same: "a"}, {Index: 2, Same: "a"}}, ds)
}

func TestConcurrency(t *testing.T) {
	var mu sync.Mutex
	inflight, peak := 0, 0
	release := make(chan struct{})
	// inflight reaches 2 again after the first release
	var once sync.Once
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		inflight++
		peak = max(peak, inflight)
		full := inflight == 2
		mu.Unlock()
		if full {
			once.Do(func() { close(release) })
		}
		<-release
		mu.Lock()
		inflight--
		mu.Unlock()
		_, _ = w.Write([]byte(`{"answers":{"same_0":{"noul":0.9}}}`))
	}))
	t.Cleanup(srv.Close)
	batch := []resolve.Pending{pending("l1", "a"), pending("l2", "a"), pending("l3", "a"), pending("l4", "a")}
	ds, err := jev.New(jev.Options{URL: srv.URL, Concurrency: 2}).Resolve(context.Background(), batch)
	require.NoError(t, err)
	assert.Len(t, ds, 4)
	assert.Equal(t, 2, peak)
}
