package token

// Splits on whitespace and a set of bytes
// Plain splits on the caller's delimiters the way Drain3 treats its extra delimiters
// Punct also splits on most punctuation
type Split struct {
	split [256]bool
	// a dot between digits and a dash between word bytes stay inside the token
	inner     bool
	maxTokens int
	scan      Scanner
}

// The punctuation Punct splits on besides whitespace
// Underscore and percent belong to words and units
// Dot and dash split only outside numbers and words
const punctDelimiters = "!\"#$&'()*+,/:;<=>?@[\\]^`{|}~"

// Splits on whitespace and the caller's delimiters
// The default tokenizer
func NewPlain(c Config) *Split { return newSplit(c, "", false) }

// Splits on whitespace and most ASCII punctuation
// Numbers like 1.5 and words like re-try keep one token
func NewPunct(c Config) *Split { return newSplit(c, punctDelimiters, true) }

func newSplit(c Config, delimiters string, inner bool) *Split {
	s := &Split{inner: inner, maxTokens: c.maxTokens(), scan: c.Scan}
	for b := 0; b < 256; b++ {
		s.split[b] = IsSpace(byte(b))
	}
	for _, set := range []string{delimiters, c.Delimiters} {
		for i := 0; i < len(set); i++ {
			s.split[set[i]] = true
		}
	}
	return s
}

func (s *Split) Tokens(line []byte, dst []Token) []Token {
	split, inner := &s.split, s.inner
	splits := func(i int) bool {
		c := line[i]
		return split[c] || (inner && (c == '.' || c == '-') && innerSplits(line, i))
	}
	count := 0
	i := 0
	for i < len(line) {
		for i < len(line) && splits(i) {
			i++
		}
		if i >= len(line) {
			break
		}
		start := i
		for i < len(line) && !splits(i) {
			i++
		}
		if count == s.maxTokens-1 {
			end := len(line)
			for end > i && splits(end-1) {
				end--
			}
			if end > i {
				// more tokens follow
				// This one and the rest fold into one Var
				return append(dst, Token{Start: start, End: end, Kind: Var})
			}
		}
		dst = append(dst, Token{Start: start, End: i, Kind: s.scan.Kind(line[start:i])})
		count++
	}
	return dst
}

// Whether a dot or a dash at i ends a token under Punct
func innerSplits(line []byte, i int) bool {
	if i == 0 || i+1 >= len(line) {
		return true
	}
	c, prev, next := line[i], line[i-1], line[i+1]
	if c == '.' {
		return !isDigit(prev) || !isDigit(next)
	}
	return !isWordByte(prev) || !isWordByte(next)
}

func isWordByte(c byte) bool {
	return isDigit(c) || (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || c == '_' || c >= 0x80
}
