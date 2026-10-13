package token_test

import (
	"regexp"
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/jeon-jihyeon/moldrain/internal/token"
)

// Reference regexes that cross check the byte scanners in tests only
var (
	octet     = `(25[0-5]|2[0-4][0-9]|1[0-9][0-9]|[1-9]?[0-9])`
	h16       = `[0-9a-fA-F]{1,4}`
	ipv4Re    = octet + `\.` + octet + `\.` + octet + `\.` + octet
	ipv6Re    = `(` + `(` + h16 + `:){7}` + h16 + `|` + `((` + h16 + `:){0,6}` + h16 + `)?::((` + h16 + `:){0,6}` + h16 + `)?` + `|` + `(` + h16 + `:){6}` + ipv4Re + `|` + `((` + h16 + `:){0,5}` + h16 + `)?::((` + h16 + `:){0,5})?` + ipv4Re + `)`
	refUUID   = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`)
	refIP     = regexp.MustCompile(`^(` + ipv4Re + `(:[0-9]{1,5})?|` + ipv6Re + `|\[` + ipv6Re + `\](:[0-9]{1,5})?)$`)
	clockRe   = `([01][0-9]|2[0-3]):[0-5][0-9]:([0-5][0-9]|60)([.,][0-9]+)?`
	dateRe    = `[0-9]{4}-(0[1-9]|1[0-2])-(0[1-9]|[12][0-9]|3[01])`
	zoneRe    = `([Zz]|[+-]([01][0-9]|2[0-3]):[0-5][0-9])?`
	refTime   = regexp.MustCompile(`^(` + dateRe + `|(` + dateRe + `[Tt])?` + clockRe + zoneRe + `)$`)
	refHex0x  = regexp.MustCompile(`^0[xX][0-9a-fA-F]+$`)
	refHexLen = regexp.MustCompile(`^[0-9a-fA-F]{8,}$`)
	refDigit  = regexp.MustCompile(`[0-9]`)
	refLetter = regexp.MustCompile(`[a-fA-F]`)
	refNum    = regexp.MustCompile(`^[+-]?[0-9]+(\.[0-9]+)?([eE][+-]?[0-9]+)?(ns|us|µs|ms|s|m|h|d|B|b|KB|MB|GB|TB|KiB|MiB|GiB|TiB|k|K|M|G|%)?$`)
)

type matcher interface{ MatchString(string) bool }

type refFunc func(string) bool

func (f refFunc) MatchString(s string) bool { return f(s) }

var refHex = refFunc(func(s string) bool {
	return refHex0x.MatchString(s) || (refHexLen.MatchString(s) && refDigit.MatchString(s) && refLetter.MatchString(s))
})

// One corpus for every scanner with the near misses of each kind
var corpus = []string{
	// uuid
	"123e4567-e89b-12d3-a456-426614174000", "123E4567-E89B-12D3-A456-426614174000",
	"123e4567-e89b-12d3-a456-42661417400", "123e4567e89b-12d3-a456-426614174000-", "g23e4567-e89b-12d3-a456-426614174000",
	// ip
	"10.0.0.1", "255.255.255.255", "10.0.0.1:8080", "192.168.1.1:65535", "::1", "::", "fe80::1", "2001:db8::8a2e:370:7334",
	"2001:0db8:0000:0000:0000:ff00:0042:8329", "::ffff:192.0.2.1", "[::1]:443", "[2001:db8::1]",
	"1.2.3", "1.2.3.4.5", "256.1.1.1", "01.2.3.4", "1.2.3.4:", "1.2.3.4:123456", "1:2:3", "2001:db8:::1", "12345::1",
	"a:b", "fe80:::", "1::2::3", "[::1", "[::1]x",
	// time
	"2024-01-02", "2024-01-02T03:04:05Z", "2024-01-02T03:04:05.123+09:00", "2024-01-02t03:04:05", "03:04:05", "23:59:60",
	"03:04:05,123", "03:04:05.5Z", "2024-13-02", "2024-01-32", "24:00:00", "3:04:05", "03:04", "03:04:05+9", "2024-01-02T",
	// hex
	"0x1f", "0XABCDEF", "deadbeef1", "1a2b3c4d", "abcdef12345", "deadbeef", "12345678", "0x", "0xg1", "1a2b3c4", "abcdefgh1",
	// num
	"0", "42", "-7", "+3.14", "1e9", "2.5E-3", "15ms", "100%", "3GB", "1.5s", "12abc", "1.", ".5", "1e", "-", "1-2", "1,000", "7KB2",
	// words
	"", "a", "error", "user_id", "blk_-1608", "v1.2.3", "http://x", "café",
}

func TestScanners(t *testing.T) {
	tcs := []struct {
		name string
		got  func([]byte) bool
		ref  matcher
	}{
		{"uuid", token.IsUUID, refUUID},
		{"ip", token.IsIP, refIP},
		{"time", token.IsTime, refTime},
		{"hex", token.IsHex, refHex},
	}
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			for _, s := range corpus {
				assert.Equal(t, tc.ref.MatchString(s), tc.got([]byte(s)), "%q", s)
			}
		})
	}
}

// The scanner order: the first scanner that claims the token wins
func TestScannerKind(t *testing.T) {
	refs := []struct {
		kind token.Kind
		re   matcher
	}{
		{token.UUID, refUUID}, {token.IP, refIP}, {token.Time, refTime}, {token.Hex, refHex}, {token.Num, refNum},
	}
	var s token.Scanner
	for _, w := range corpus {
		want := token.Const
		for _, r := range refs {
			if w != "" && r.re.MatchString(w) {
				want = r.kind
				break
			}
		}
		assert.Equal(t, want, s.Kind([]byte(w)), "%q", w)
	}
}

func TestEpochTokens(t *testing.T) {
	tcs := []struct {
		name  string
		epoch bool
		tok   string
		want  token.Kind
	}{
		{"seconds stay a number by default", false, "1700000000", token.Num},
		{"seconds are time when asked", true, "1700000000", token.Time},
		{"milliseconds are time when asked", true, "1700000000123", token.Time},
		{"other lengths stay numbers", true, "17000000001", token.Num},
	}
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			s := token.Scanner{EpochTokens: tc.epoch}
			assert.Equal(t, tc.want, s.Kind([]byte(tc.tok)))
		})
	}
}

func TestUnits(t *testing.T) {
	s := token.Scanner{Units: []string{}}
	assert.Equal(t, token.Const, s.Kind([]byte("15ms")))
	s = token.Scanner{Units: []string{"req"}}
	assert.Equal(t, token.Num, s.Kind([]byte("15req")))
}
