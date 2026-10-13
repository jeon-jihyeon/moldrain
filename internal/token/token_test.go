package token_test

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/jeon-jihyeon/moldrain/internal/token"
)

type tok struct {
	text string
	kind token.Kind
}

func texts(line string, toks []token.Token) []tok {
	out := make([]tok, 0, len(toks))
	for _, t := range toks {
		out = append(out, tok{line[t.Start:t.End], t.Kind})
	}
	return out
}

func c(s string) tok { return tok{s, token.Const} }

func TestPlain(t *testing.T) {
	tcs := []struct {
		name  string
		delim string
		line  string
		want  []tok
	}{
		{"empty line has no tokens", "", "", []tok{}},
		{"only delimiters have no tokens", "", " \t  \t", []tok{}},
		{"words and kinds", "", "Accepted password for 10.0.0.1 port 22 took 15ms", []tok{
			c("Accepted"), c("password"), c("for"), {"10.0.0.1", token.IP}, c("port"), {"22", token.Num}, c("took"), {"15ms", token.Num},
		}},
		{"tabs and runs of spaces split once", "", "a\t\tb   c", []tok{c("a"), c("b"), c("c")}},
		{"extra delimiters split like spaces", "=,", "user=bob,id=7", []tok{c("user"), c("bob"), c("id"), {"7", token.Num}}},
		{"unicode stays inside a token", "", "héllo 세계 ok", []tok{c("héllo"), c("세계"), c("ok")}},
		{"invalid utf-8 stays inside a token", "", "a\xff\xfe b", []tok{c("a\xff\xfe"), c("b")}},
		{"uuid and time", "", "job 123e4567-e89b-12d3-a456-426614174000 at 2024-05-01T10:00:00Z", []tok{
			c("job"), {"123e4567-e89b-12d3-a456-426614174000", token.UUID}, c("at"), {"2024-05-01T10:00:00Z", token.Time},
		}},
	}
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			p := token.NewPlain(token.Config{Delimiters: tc.delim})
			assert.Equal(t, tc.want, texts(tc.line, p.Tokens([]byte(tc.line), nil)))
		})
	}
}

func TestMaxTokens(t *testing.T) {
	cfg := token.Config{MaxTokens: 3}
	tcs := []struct {
		name string
		tz   token.Tokenizer
		line string
		want []tok
	}{
		{"plain line at the cap keeps every token", token.NewPlain(cfg), "a b c", []tok{c("a"), c("b"), c("c")}},
		{"plain line past the cap folds the rest into one Var", token.NewPlain(cfg), "a b c d  e ", []tok{c("a"), c("b"), {"c d  e", token.Var}}},
		{"punct line past the cap folds the rest", token.NewPunct(cfg), "a;b;c;d", []tok{c("a"), c("b"), {"c;d", token.Var}}},
		{"json object at the cap keeps every token", token.NewJSON(token.Config{MaxTokens: 4}), `{"a":"x","b":1}`, []tok{c("a"), c("x"), c("b"), {"1", token.Num}}},
		{"json object past the cap folds the rest", token.NewJSON(cfg), `{"a":1,"b":2}`, []tok{c("a"), {"1", token.Num}, {`b":2`, token.Var}}},
		{"logfmt pairs past the cap fold the rest", token.NewLogfmt(cfg), "a=1 b=2", []tok{c("a"), {"1", token.Num}, {"b=2", token.Var}}},
	}
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tc.want, texts(tc.line, tc.tz.Tokens([]byte(tc.line), nil)))
		})
	}
}

func TestPunct(t *testing.T) {
	tcs := []struct {
		name string
		line string
		want []tok
	}{
		{"punctuation splits", "user=bob;id:[42]", []tok{c("user"), c("bob"), c("id"), {"42", token.Num}}},
		{"dot inside a number stays", "took 1.5s ok.", []tok{c("took"), {"1.5s", token.Num}, c("ok")}},
		{"dash inside a word stays", "re-try -x a-", []tok{c("re-try"), c("x"), c("a")}},
		{"ip keeps its dots", "from 10.1.2.3:80", []tok{c("from"), {"10.1.2.3", token.IP}, {"80", token.Num}}},
		{"dotted words split", "java.lang.Error", []tok{c("java"), c("lang"), c("Error")}},
		{"only punctuation has no tokens", "=:;[]", []tok{}},
	}
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			p := token.NewPunct(token.Config{})
			assert.Equal(t, tc.want, texts(tc.line, p.Tokens([]byte(tc.line), nil)))
		})
	}
}

func TestJSON(t *testing.T) {
	tcs := []struct {
		name string
		line string
		want []tok
	}{
		{"keys constant and scalars scanned", `{"level":"info","ms":12,"ok":true}`, []tok{
			c("level"), c("info"), c("ms"), {"12", token.Num}, c("ok"), c("true"),
		}},
		{"a key that looks like a number stays constant", `{"404": "x"}`, []tok{c("404"), c("x")}},
		{"nested objects and arrays", `{"a":{"b":[1,"x",null]}}`, []tok{c("a"), c("b"), {"1", token.Num}, c("x"), c("null")}},
		{"empty string is an empty token", `{"a":""}`, []tok{c("a"), c("")}},
		{"escaped quote stays inside", `{"m":"say \"hi\""}`, []tok{c("m"), c(`say \"hi\"`)}},
		{"not an object falls back to plain", `[1, 2]`, []tok{c("[1,"), c("2]")}},
		{"broken object falls back to plain", `{"a": 1 x`, []tok{c(`{"a":`), {"1", token.Num}, c("x")}},
		{"trailing text falls back to plain", `{"a":1} tail`, []tok{c(`{"a":1}`), c("tail")}},
	}
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			j := token.NewJSON(token.Config{})
			assert.Equal(t, tc.want, texts(tc.line, j.Tokens([]byte(tc.line), nil)))
		})
	}
}

func TestLogfmt(t *testing.T) {
	tcs := []struct {
		name string
		line string
		want []tok
	}{
		{"pairs with constant keys", "level=info took=5ms", []tok{c("level"), c("info"), c("took"), {"5ms", token.Num}}},
		{"quoted value is one token", `msg="disk full" n=2`, []tok{c("msg"), c("disk full"), c("n"), {"2", token.Num}}},
		{"empty value keeps the pair shape", "a= b=1", []tok{c("a"), c(""), c("b"), {"1", token.Num}}},
		{"bare word is a token", "started 3", []tok{c("started"), {"3", token.Num}}},
		{"unterminated quote runs to the end", `m="abc`, []tok{c("m"), c("abc")}},
	}
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			l := token.NewLogfmt(token.Config{})
			assert.Equal(t, tc.want, texts(tc.line, l.Tokens([]byte(tc.line), nil)))
		})
	}
}

func TestVerbatim(t *testing.T) {
	p := token.NewPlain(token.Config{Scan: token.Scanner{Verbatim: true}})
	line := "port 22 from 10.0.0.1"
	assert.Equal(t, []tok{c("port"), c("22"), c("from"), c("10.0.0.1")}, texts(line, p.Tokens([]byte(line), nil)))
}

func TestMasks(t *testing.T) {
	blk := func(b []byte) (token.Kind, bool) {
		return token.Var, strings.HasPrefix(string(b), "blk_")
	}
	p := token.NewPlain(token.Config{Scan: token.Scanner{Masks: []token.Mask{blk}}})
	line := "block blk_-123 42"
	assert.Equal(t, []tok{c("block"), {"blk_-123", token.Var}, {"42", token.Num}}, texts(line, p.Tokens([]byte(line), nil)))
}

func TestInterner(t *testing.T) {
	in := token.NewInterner()
	_, ok := in.Lookup([]byte("a"))
	assert.False(t, ok)
	a := in.Intern([]byte("a"))
	b := in.Intern([]byte("b"))
	assert.Equal(t, a, in.Intern([]byte("a")))
	assert.Equal(t, []uint32{1, 2}, []uint32{a, b})
	got, ok := in.Lookup([]byte("b"))
	assert.True(t, ok)
	assert.Equal(t, b, got)
	assert.Equal(t, "b", in.Text(b))
	assert.Equal(t, 2, in.Len())
}

func TestInternerConcurrent(t *testing.T) {
	in := token.NewInterner()
	words := make([][]byte, 500)
	for i := range words {
		words[i] = []byte(strings.Repeat("w", i%7+1) + string(rune('a'+i%26)) + strings.Repeat("x", i/26))
	}
	done := make(chan struct{})
	for g := 0; g < 4; g++ {
		go func() {
			defer func() { done <- struct{}{} }()
			for _, w := range words {
				in.Lookup(w)
			}
		}()
	}
	for _, w := range words {
		in.Intern(w)
	}
	for g := 0; g < 4; g++ {
		<-done
	}
	for _, w := range words {
		sym, ok := in.Lookup(w)
		assert.True(t, ok)
		assert.Equal(t, string(w), in.Text(sym))
	}
}

func FuzzTokens(f *testing.F) {
	for _, s := range []string{"", "a b", `{"a":[1,{"b":"c"}]}`, `k="v x" y=`, "10.0.0.1:80 ::1 0xff", "\xff\x00 =="} {
		f.Add(s)
	}
	toks := []token.Tokenizer{
		token.NewPlain(token.Config{MaxTokens: 8}),
		token.NewPunct(token.Config{MaxTokens: 8}),
		token.NewJSON(token.Config{MaxTokens: 8}),
		token.NewLogfmt(token.Config{MaxTokens: 8}),
		token.NewPlain(token.Config{}),
		token.NewJSON(token.Config{}),
	}
	f.Fuzz(func(t *testing.T, line string) {
		for _, tz := range toks {
			prev := 0
			got := tz.Tokens([]byte(line), nil)
			for _, tk := range got {
				if tk.Start < prev || tk.End < tk.Start || tk.End > len(line) {
					t.Fatalf("%T token %d..%d out of order after %d in %q", tz, tk.Start, tk.End, prev, line)
				}
				prev = tk.End
			}
		}
	})
}
