package token

import "slices"

// A caller's scanner over the bytes of one token
// It runs after the built in scanners and claims the token when ok
type Mask func(tok []byte) (kind Kind, ok bool)

var defaultUnits = []string{
	"ns", "us", "µs", "ms", "s", "m", "h", "d",
	"B", "b", "KB", "MB", "GB", "TB", "KiB", "MiB", "GiB", "TiB",
	"k", "K", "M", "G", "%",
}

// The unit suffixes a number may carry when Scanner leaves Units nil
func DefaultUnits() []string { return slices.Clone(defaultUnits) }

// The typed scanners run in a fixed order
// Each is a byte scan
// Masking costs one pass instead of one regex per rule
type Scanner struct {
	// Turns every scanner off for a dataset whose numbers are part of the event
	Verbatim bool
	// Claims 10 digit Unix seconds and 13 digit milliseconds as Time
	EpochTokens bool
	// Unit suffixes of a number
	// nil means the default units and an empty slice means none
	Units []string
	Masks []Mask
}

// The kind of one token
// Const when no scanner claims it whole
func (s *Scanner) Kind(tok []byte) Kind {
	if s.Verbatim || len(tok) == 0 {
		return Const
	}
	// a word without a digit or a colon can only be a UUID
	// Most words skip the scanners
	if len(tok) != 36 && !hasDigitOrColon(tok) {
		return s.masked(tok)
	}
	switch {
	case isUUID(tok):
		return UUID
	case isIP(tok):
		return IP
	case isTime(tok) || (s.EpochTokens && isEpoch(tok)):
		return Time
	case isHex(tok):
		return Hex
	case s.isNum(tok):
		return Num
	}
	return s.masked(tok)
}

func (s *Scanner) masked(tok []byte) Kind {
	for _, m := range s.Masks {
		if k, ok := m(tok); ok {
			return k
		}
	}
	return Const
}

func hasDigitOrColon(b []byte) bool {
	for _, c := range b {
		if isDigit(c) || c == ':' {
			return true
		}
	}
	return false
}

func (s *Scanner) isNum(b []byte) bool {
	n := numPrefix(b)
	if n == 0 {
		return false
	}
	if n == len(b) {
		return true
	}
	units := s.Units
	if units == nil {
		units = defaultUnits
	}
	rest := b[n:]
	for _, u := range units {
		if string(rest) == u {
			return true
		}
	}
	return false
}

// Length of the number at the start of b: sign, digits, fraction and exponent
// 0 when b does not start with one
func numPrefix(b []byte) int {
	i := 0
	if i < len(b) && (b[i] == '+' || b[i] == '-') {
		i++
	}
	d := digits(b[i:])
	if d == 0 {
		return 0
	}
	i += d
	if i+1 < len(b) && b[i] == '.' {
		if f := digits(b[i+1:]); f > 0 {
			i += 1 + f
		}
	}
	if i+1 < len(b) && (b[i] == 'e' || b[i] == 'E') {
		j := i + 1
		if j < len(b) && (b[j] == '+' || b[j] == '-') {
			j++
		}
		if e := digits(b[j:]); e > 0 {
			i = j + e
		}
	}
	return i
}

func digits(b []byte) int {
	i := 0
	for i < len(b) && isDigit(b[i]) {
		i++
	}
	return i
}

func hexDigits(b []byte, maxLen int) int {
	i := 0
	for i < len(b) && i < maxLen && isHexDigit(b[i]) {
		i++
	}
	return i
}

func isDigit(c byte) bool { return c >= '0' && c <= '9' }

func isHexLetter(c byte) bool { return (c >= 'a' && c <= 'f') || (c >= 'A' && c <= 'F') }

func isHexDigit(c byte) bool { return isDigit(c) || isHexLetter(c) }

// 8-4-4-4-12 hex digits with dashes
func isUUID(b []byte) bool {
	if len(b) != 36 {
		return false
	}
	for i, c := range b {
		switch i {
		case 8, 13, 18, 23:
			if c != '-' {
				return false
			}
		default:
			if !isHexDigit(c) {
				return false
			}
		}
	}
	return true
}

// Dotted IPv4 with an optional port
// Or IPv6 in its colon forms with an optional bracket and port
func isIP(b []byte) bool {
	if n := ipv4(b); n > 0 {
		return n == len(b) || port(b[n:])
	}
	if len(b) > 0 && b[0] == '[' {
		end := -1
		for i := 1; i < len(b); i++ {
			if b[i] == ']' {
				end = i
				break
			}
		}
		if end < 0 || !ipv6(b[1:end]) {
			return false
		}
		return end+1 == len(b) || port(b[end+1:])
	}
	return ipv6(b)
}

// A colon followed by 1 to 5 digits
func port(b []byte) bool {
	if len(b) < 2 || b[0] != ':' {
		return false
	}
	d := digits(b[1:])
	return d >= 1 && d <= 5 && d == len(b)-1
}

// Length of a dotted IPv4 address at the start of b
// 0 when there is none
func ipv4(b []byte) int {
	i := 0
	for part := 0; part < 4; part++ {
		if part > 0 {
			if i >= len(b) || b[i] != '.' {
				return 0
			}
			i++
		}
		d := digits(b[i:])
		if d == 0 || d > 3 || (d > 1 && b[i] == '0') {
			return 0
		}
		v := 0
		for _, c := range b[i : i+d] {
			v = v*10 + int(c-'0')
		}
		if v > 255 {
			return 0
		}
		i += d
	}
	// a fifth dotted part means a version string or an OID and no address
	if i < len(b) && b[i] == '.' {
		return 0
	}
	return i
}

// IPv6 in full, compressed or IPv4 suffixed form
func ipv6(b []byte) bool {
	if len(b) < 2 {
		return false
	}
	for i := 0; i+1 < len(b); i++ {
		if b[i] != ':' || b[i+1] != ':' {
			continue
		}
		// one :: stands for one or more zero groups
		// The two sides hold at most 7
		left, okl := groups16(b[:i], false)
		right, okr := groups16(b[i+2:], true)
		return okl && okr && left+right <= 7
	}
	n, ok := groups16(b, true)
	return ok && n == 8
}

// Counts colon separated groups of 1 to 4 hex digits
// With v4 the last group may be a dotted IPv4 address that counts as two
func groups16(b []byte, v4 bool) (int, bool) {
	if len(b) == 0 {
		return 0, true
	}
	n := 0
	for start := 0; start <= len(b); {
		end := start
		for end < len(b) && b[end] != ':' {
			end++
		}
		part := b[start:end]
		switch {
		case end == len(b) && v4 && ipv4(part) == len(part) && len(part) > 0:
			n += 2
		case len(part) == 0 || len(part) > 4 || hexDigits(part, 4) != len(part):
			return 0, false
		default:
			n++
		}
		start = end + 1
	}
	return n, true
}

// RFC 3339 date, time or date-time
// Or a clock time with an optional fraction
func isTime(b []byte) bool {
	if n := fullDate(b); n > 0 {
		if n == len(b) {
			return true
		}
		if b[n] != 'T' && b[n] != 't' {
			return false
		}
		b = b[n+1:]
		if len(b) == 0 {
			return false
		}
	}
	n := clock(b)
	if n == 0 {
		return false
	}
	return n == len(b) || zone(b[n:])
}

// Length of YYYY-MM-DD at the start of b
func fullDate(b []byte) int {
	if len(b) < 10 || digits(b[:4]) != 4 || b[4] != '-' || b[7] != '-' {
		return 0
	}
	if !twoDigits(b[5:7], 1, 12) || !twoDigits(b[8:10], 1, 31) {
		return 0
	}
	return 10
}

// Length of HH:MM:SS with an optional fraction after a dot or comma
func clock(b []byte) int {
	if len(b) < 8 || b[2] != ':' || b[5] != ':' {
		return 0
	}
	if !twoDigits(b[0:2], 0, 23) || !twoDigits(b[3:5], 0, 59) || !twoDigits(b[6:8], 0, 60) {
		return 0
	}
	i := 8
	if i+1 < len(b) && (b[i] == '.' || b[i] == ',') {
		if f := digits(b[i+1:]); f > 0 {
			i += 1 + f
		}
	}
	return i
}

func zone(b []byte) bool {
	if len(b) == 1 && (b[0] == 'Z' || b[0] == 'z') {
		return true
	}
	return len(b) == 6 && (b[0] == '+' || b[0] == '-') && twoDigits(b[1:3], 0, 23) && b[3] == ':' && twoDigits(b[4:6], 0, 59)
}

func twoDigits(b []byte, lo, hi int) bool {
	if !isDigit(b[0]) || !isDigit(b[1]) {
		return false
	}
	v := int(b[0]-'0')*10 + int(b[1]-'0')
	return v >= lo && v <= hi
}

func isEpoch(b []byte) bool {
	d := digits(b)
	return d == len(b) && (d == 10 || d == 13)
}

// 0x followed by hex digits
// Or 8 or more hex digits with at least one digit and one letter
func isHex(b []byte) bool {
	if len(b) > 2 && b[0] == '0' && (b[1] == 'x' || b[1] == 'X') {
		return hexDigits(b[2:], len(b)) == len(b)-2
	}
	if len(b) < 8 {
		return false
	}
	letter, digit := false, false
	for _, c := range b {
		switch {
		case isDigit(c):
			digit = true
		case isHexLetter(c):
			letter = true
		default:
			return false
		}
	}
	return letter && digit
}
