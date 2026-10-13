package token

// One token per key and one per scalar value of a JSON object
// Keys are always constant
// A line that is not one JSON object falls back to Plain
type JSON struct {
	plain     *Split
	maxTokens int
	scan      Scanner
}

// Nesting deeper than this falls back to Plain so a hostile line cannot grow the stack
const maxJSONDepth = 64

func NewJSON(c Config) *JSON {
	return &JSON{plain: NewPlain(c), maxTokens: c.maxTokens(), scan: c.Scan}
}

func (j *JSON) Tokens(line []byte, dst []Token) []Token {
	base := len(dst)
	p := jsonParser{line: line, dst: dst, scan: &j.scan}
	i := p.ws(0)
	if i >= len(line) || line[i] != '{' {
		return j.plain.Tokens(line, dst)
	}
	i, ok := p.value(i, 0)
	if !ok || p.ws(i) != len(line) {
		return j.plain.Tokens(line, p.dst[:base])
	}
	return capTokens(p.dst, base, j.maxTokens)
}

type jsonParser struct {
	line []byte
	dst  []Token
	scan *Scanner
}

func (p *jsonParser) ws(i int) int {
	for i < len(p.line) {
		switch p.line[i] {
		case ' ', '\t', '\r', '\n':
			i++
		default:
			return i
		}
	}
	return i
}

// Parses the value at i and returns the index after it
func (p *jsonParser) value(i, depth int) (int, bool) {
	if depth > maxJSONDepth || i >= len(p.line) {
		return i, false
	}
	switch p.line[i] {
	case '{':
		return p.object(i+1, depth+1)
	case '[':
		return p.array(i+1, depth+1)
	case '"':
		end, ok := p.str(i)
		if !ok {
			return i, false
		}
		p.dst = append(p.dst, Token{Start: i + 1, End: end - 1, Kind: p.scan.Kind(p.line[i+1 : end-1])})
		return end, true
	default:
		end := i
		for end < len(p.line) && isScalarByte(p.line[end]) {
			end++
		}
		if end == i {
			return i, false
		}
		text := p.line[i:end]
		kind := Const
		if string(text) != "true" && string(text) != "false" && string(text) != "null" {
			if numPrefix(text) != len(text) {
				return i, false
			}
			kind = p.scan.Kind(text)
		}
		p.dst = append(p.dst, Token{Start: i, End: end, Kind: kind})
		return end, true
	}
}

func isScalarByte(c byte) bool {
	return isDigit(c) || (c >= 'a' && c <= 'z') || c == '-' || c == '+' || c == '.' || c == 'E'
}

func (p *jsonParser) object(i, depth int) (int, bool) {
	i = p.ws(i)
	if i < len(p.line) && p.line[i] == '}' {
		return i + 1, true
	}
	for {
		i = p.ws(i)
		if i >= len(p.line) || p.line[i] != '"' {
			return i, false
		}
		end, ok := p.str(i)
		if !ok {
			return i, false
		}
		p.dst = append(p.dst, Token{Start: i + 1, End: end - 1, Kind: Const})
		i = p.ws(end)
		if i >= len(p.line) || p.line[i] != ':' {
			return i, false
		}
		if i, ok = p.value(p.ws(i+1), depth); !ok {
			return i, false
		}
		i = p.ws(i)
		if i >= len(p.line) {
			return i, false
		}
		switch p.line[i] {
		case ',':
			i++
		case '}':
			return i + 1, true
		default:
			return i, false
		}
	}
}

func (p *jsonParser) array(i, depth int) (int, bool) {
	i = p.ws(i)
	if i < len(p.line) && p.line[i] == ']' {
		return i + 1, true
	}
	for {
		var ok bool
		if i, ok = p.value(p.ws(i), depth); !ok {
			return i, false
		}
		i = p.ws(i)
		if i >= len(p.line) {
			return i, false
		}
		switch p.line[i] {
		case ',':
			i++
		case ']':
			return i + 1, true
		default:
			return i, false
		}
	}
}

// Index after the closing quote of the string that opens at i
func (p *jsonParser) str(i int) (int, bool) {
	for j := i + 1; j < len(p.line); j++ {
		switch p.line[j] {
		case '\\':
			j++
		case '"':
			return j + 1, true
		}
	}
	return i, false
}

// key=value pairs with constant keys
// A quoted value stays one token and a word without = is a token of its own
type Logfmt struct {
	maxTokens int
	scan      Scanner
}

func NewLogfmt(c Config) *Logfmt {
	return &Logfmt{maxTokens: c.maxTokens(), scan: c.Scan}
}

func (l *Logfmt) Tokens(line []byte, dst []Token) []Token {
	base := len(dst)
	i := 0
	for i < len(line) {
		for i < len(line) && IsSpace(line[i]) {
			i++
		}
		if i >= len(line) {
			break
		}
		start := i
		for i < len(line) && !IsSpace(line[i]) && line[i] != '=' {
			i++
		}
		if i >= len(line) || line[i] != '=' || i == start {
			for i < len(line) && !IsSpace(line[i]) {
				i++
			}
			dst = append(dst, Token{Start: start, End: i, Kind: l.scan.Kind(line[start:i])})
			continue
		}
		dst = append(dst, Token{Start: start, End: i, Kind: Const})
		i++
		var tok Token
		tok, i = l.value(line, i)
		dst = append(dst, tok)
	}
	return capTokens(dst, base, l.maxTokens)
}

// The value token that starts at i and the index after it
// An empty value is an empty token so key= and key=x keep one shape
func (l *Logfmt) value(line []byte, i int) (Token, int) {
	if i < len(line) && line[i] == '"' {
		j := i + 1
		for j < len(line) && line[j] != '"' {
			if line[j] == '\\' && j+1 < len(line) {
				j++
			}
			j++
		}
		end := min(j, len(line))
		next := min(j+1, len(line))
		return Token{Start: i + 1, End: end, Kind: l.scan.Kind(line[i+1 : end])}, next
	}
	start := i
	for i < len(line) && !IsSpace(line[i]) {
		i++
	}
	return Token{Start: start, End: i, Kind: l.scan.Kind(line[start:i])}, i
}
