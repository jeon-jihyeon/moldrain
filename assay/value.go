package assay

import (
	"math"
	"strconv"
)

type suffix struct {
	text   string
	factor float64
}

// Suffixes each unit reads with their size in its base
// The base is nanoseconds for durations and bytes for sizes
// An empty suffix stands for the unit itself
var unitFactors = map[Unit][]suffix{
	NoUnit:       nil,
	Milliseconds: durations,
	Seconds:      durations,
	Bytes: {
		{"B", 1}, {"KB", 1e3}, {"MB", 1e6}, {"GB", 1e9}, {"TB", 1e12},
		{"KiB", 1 << 10}, {"MiB", 1 << 20}, {"GiB", 1 << 30}, {"TiB", 1 << 40},
	},
}

var durations = []suffix{
	{"ns", 1}, {"us", 1e3}, {"µs", 1e3}, {"ms", 1e6}, {"s", 1e9}, {"m", 60e9}, {"min", 60e9}, {"h", 3600e9},
}

// Size of the unit itself in its base
var unitBase = map[Unit]float64{NoUnit: 1, Milliseconds: 1e6, Seconds: 1e9, Bytes: 1}

// Reads a number with an optional suffix of the unit and converts it to the unit
// Allocates nothing for numbers of up to 15 significant digits
func readValue(b []byte, u Unit) (float64, bool) {
	v, n, ok := readNumber(b)
	if !ok {
		return 0, false
	}
	rest := b[n:]
	if len(rest) == 0 {
		return v, true
	}
	for _, s := range unitFactors[u] {
		if string(rest) != s.text {
			continue
		}
		base := unitBase[u]
		switch {
		case s.factor == base:
			return v, true
		case s.factor > base:
			return v * (s.factor / base), true
		default:
			return v / (base / s.factor), true
		}
	}
	return 0, false
}

// Exact powers of ten for the fast path of Clinger
// One multiplication or division by them rounds correctly
var pow10 = [...]float64{1, 1e1, 1e2, 1e3, 1e4, 1e5, 1e6, 1e7, 1e8, 1e9, 1e10, 1e11,
	1e12, 1e13, 1e14, 1e15, 1e16, 1e17, 1e18, 1e19, 1e20, 1e21, 1e22}

// Parses the decimal number at the start of b and returns it with the bytes it took
// Falls back to strconv when the mantissa or the exponent leaves the exact range
func readNumber(b []byte) (float64, int, bool) {
	i, neg := 0, false
	if i < len(b) && (b[i] == '-' || b[i] == '+') {
		neg = b[i] == '-'
		i++
	}
	mant, digits, frac, exact, i := readMantissa(b, i)
	if digits == 0 {
		return 0, 0, false
	}
	exp, n := readExponent(b[i:])
	i += n
	scale := exp - frac
	if !exact || mant > 1<<53 || scale < -22 || scale > 22 {
		v, err := strconv.ParseFloat(string(b[:i]), 64)
		return v, i, err == nil
	}
	v := float64(mant)
	if scale >= 0 {
		v *= pow10[scale]
	} else {
		v /= pow10[-scale]
	}
	if neg {
		v = -v
	}
	return v, i, true
}

// Digits with at most one dot from i on
// exact tells whether the mantissa stayed exact in a float64
func readMantissa(b []byte, i int) (mant uint64, digits, frac int, exact bool, end int) {
	dot := false
	exact = true
	for ; i < len(b) && (isDigit(b[i]) || b[i] == '.' && !dot); i++ {
		if b[i] == '.' {
			dot = true
			continue
		}
		digits++
		if dot {
			frac++
		}
		if mant > (1<<53)/10 {
			exact = false
			continue
		}
		mant = mant*10 + uint64(b[i]-'0')
	}
	return mant, digits, frac, exact, i
}

// An exponent such as e3 or E-2
// Only when digits follow so a suffix like 5e stays a suffix
func readExponent(b []byte) (int, int) {
	if len(b) < 2 || (b[0] != 'e' && b[0] != 'E') {
		return 0, 0
	}
	i, sign := 1, 1
	if b[i] == '-' || b[i] == '+' {
		if b[i] == '-' {
			sign = -1
		}
		i++
	}
	start, exp := i, 0
	for ; i < len(b) && isDigit(b[i]) && exp < math.MaxInt16; i++ {
		exp = exp*10 + int(b[i]-'0')
	}
	if i == start {
		return 0, 0
	}
	return sign * exp, i
}

func isDigit(c byte) bool { return c >= '0' && c <= '9' }
